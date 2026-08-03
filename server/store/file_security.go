package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"chat/server/media"
	"chat/server/store/types"
)

const (
	fileACLPrefix        = "fileacl:"
	fileProcessingPrefix = "fileproc:"
	fileMessageACLPrefix = "filemsgacl:v1:"
	fileTopicACLPrefix   = "filetopicacl:v1:"
	fileGrantCASAttempts = 64
)

// FileProcessingState 保存内容摘要、安全扫描和在线预览处理结果。
type FileProcessingState struct {
	SHA256             string            `json:"sha256,omitempty"`
	DuplicateOf        string            `json:"duplicate_of,omitempty"`
	ScannerVersion     string            `json:"scanner_version,omitempty"`
	ScanStatus         string            `json:"scan_status,omitempty"`
	ProcessStatus      string            `json:"process_status,omitempty"`
	Preview            map[string]string `json:"preview,omitempty"`
	Error              string            `json:"error,omitempty"`
	Attempts           int               `json:"attempts,omitempty"`
	NextRetryAt        *time.Time        `json:"next_retry_at,omitempty"`
	QuarantineStatus   string            `json:"quarantine_status,omitempty"`
	QuarantineLocation string            `json:"quarantine_location,omitempty"`
	ReviewedBy         string            `json:"reviewed_by,omitempty"`
	ReviewReason       string            `json:"review_reason,omitempty"`
	ReviewedAt         *time.Time        `json:"reviewed_at,omitempty"`
	UpdatedAt          time.Time         `json:"updated_at"`
}

type QuarantinedFileRecord struct {
	FileID string              `json:"file_id"`
	State  FileProcessingState `json:"state"`
}

type fileAccessGrant struct {
	Topics     []string               `json:"topics,omitempty"`
	Users      []string               `json:"users,omitempty"`
	Public     bool                   `json:"public,omitempty"`
	References []fileMessageReference `json:"references,omitempty"`
}

type fileMessageReference struct {
	Message string `json:"message"`
	Topic   string `json:"topic"`
}

type fileMessageAccessIndex struct {
	Message string   `json:"message"`
	Topic   string   `json:"topic"`
	Files   []string `json:"files"`
}

// SetFilePublicAccess 发布或撤销公共素材文件；仍要求请求方已登录。
func SetFilePublicAccess(rawURL string, public bool) error {
	fid := localFileID(rawURL)
	if fid == "" {
		// 外部 CDN 地址不由本服务的文件 ACL 管理。
		return nil
	}
	err := updateFileGrant(fid, func(grant *fileAccessGrant) {
		grant.Public = public
	})
	if err != nil {
		return err
	}
	if state, stateErr := GetFileProcessingState(fid); stateErr == nil {
		for _, previewURL := range state.Preview {
			if previewURL != "" && previewURL != rawURL {
				if err = SetFilePublicAccess(previewURL, public); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

var fileSecurityLocks sync.Map

func fileSecurityLock(fid string) *sync.Mutex {
	value, _ := fileSecurityLocks.LoadOrStore(fid, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func localFileID(rawURL string) string {
	if mediaHandler == nil {
		return ""
	}
	return mediaHandler.GetIdFromUrl(rawURL).String()
}

// FileURLsWithPreviews 把服务端生成的预览/转码文件加入附件集合，确保它们与原消息
// 一起建立数据库引用，不会被未使用文件 GC 提前删除。
func FileURLsWithPreviews(rawURLs []string) []string {
	seen := make(map[string]bool, len(rawURLs))
	out := make([]string, 0, len(rawURLs))
	for _, rawURL := range rawURLs {
		if rawURL == "" || seen[rawURL] {
			continue
		}
		seen[rawURL] = true
		out = append(out, rawURL)
		fid := localFileID(rawURL)
		if fid == "" {
			continue
		}
		state, err := GetFileProcessingState(fid)
		if err != nil {
			continue
		}
		for _, previewURL := range state.Preview {
			if previewURL != "" && !seen[previewURL] {
				seen[previewURL] = true
				out = append(out, previewURL)
			}
		}
	}
	return out
}

func loadFileGrantRaw(fid string) (*fileAccessGrant, string, bool, error) {
	raw, err := PCache.Get(fileACLPrefix + fid)
	if errors.Is(err, types.ErrNotFound) {
		return &fileAccessGrant{}, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	var grant fileAccessGrant
	if err = json.Unmarshal([]byte(raw), &grant); err != nil {
		return nil, "", false, err
	}
	return &grant, raw, true, nil
}

func loadFileGrant(fid string) (*fileAccessGrant, error) {
	grant, _, _, err := loadFileGrantRaw(fid)
	return grant, err
}

func saveFileGrant(fid string, grant *fileAccessGrant) error {
	raw, err := json.Marshal(grant)
	if err != nil {
		return err
	}
	return PCache.Upsert(fileACLPrefix+fid, string(raw), false)
}

func updateFileGrant(fid string, mutate func(*fileAccessGrant)) error {
	for attempt := 0; attempt < fileGrantCASAttempts; attempt++ {
		grant, oldRaw, exists, err := loadFileGrantRaw(fid)
		if err != nil {
			return err
		}
		mutate(grant)
		newRaw, err := json.Marshal(grant)
		if err != nil {
			return err
		}
		if !exists {
			if err = PCache.Upsert(fileACLPrefix+fid, string(newRaw), true); err == nil {
				return nil
			}
			if errors.Is(err, types.ErrDuplicate) {
				continue
			}
			return err
		}
		swapped, err := PCache.CompareAndSwap(fileACLPrefix+fid, oldRaw, string(newRaw))
		if err != nil {
			return err
		}
		if swapped {
			return nil
		}
	}
	return types.ErrVersionConflict
}

// GrantFileAccess 把本地文件授权给 Topic 成员或指定用户。
func GrantFileAccess(topic string, user types.Uid, attachmentURLs []string) error {
	for _, rawURL := range attachmentURLs {
		fid := localFileID(rawURL)
		if fid == "" {
			continue
		}
		err := updateFileGrant(fid, func(grant *fileAccessGrant) {
			if topic != "" && !containsString(grant.Topics, topic) {
				grant.Topics = append(grant.Topics, topic)
				sort.Strings(grant.Topics)
			}
			if !user.IsZero() && !containsString(grant.Users, user.UserId()) {
				grant.Users = append(grant.Users, user.UserId())
				sort.Strings(grant.Users)
			}
		})
		if err != nil {
			return err
		}
		if state, stateErr := GetFileProcessingState(fid); stateErr == nil {
			for _, previewURL := range state.Preview {
				if previewURL != "" && previewURL != rawURL {
					if err = GrantFileAccess(topic, user, []string{previewURL}); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func fileMessageIndexKey(message types.Uid) string {
	return fileMessageACLPrefix + message.String()
}

func fileTopicIndexPrefix(topic string) string {
	return fileTopicACLPrefix + base64.RawURLEncoding.EncodeToString([]byte(topic)) + ":"
}

func fileTopicMessageIndexKey(topic string, message types.Uid) string {
	return fileTopicIndexPrefix(topic) + message.String()
}

func loadFileMessageAccess(message types.Uid) (*fileMessageAccessIndex, error) {
	raw, err := PCache.Get(fileMessageIndexKey(message))
	if errors.Is(err, types.ErrNotFound) {
		return &fileMessageAccessIndex{Message: message.String()}, nil
	}
	if err != nil {
		return nil, err
	}
	var index fileMessageAccessIndex
	if err = json.Unmarshal([]byte(raw), &index); err != nil {
		return nil, err
	}
	return &index, nil
}

// GrantFileMessageAccess 维护“消息 -> 文件”和“文件 -> 消息”的双向引用。
// 编辑消息时会自动撤销被移除附件的 Topic 授权。
func GrantFileMessageAccess(topic string, message types.Uid, attachmentURLs []string) error {
	if message.IsZero() {
		return types.ErrMalformed
	}
	index, err := loadFileMessageAccess(message)
	if err != nil {
		return err
	}
	if topic == "" {
		topic = index.Topic
	}
	attachments := FileURLsWithPreviews(attachmentURLs)
	newFiles := make([]string, 0, len(attachments))
	newSet := make(map[string]struct{}, len(attachments))
	for _, rawURL := range attachments {
		if fid := localFileID(rawURL); fid != "" {
			if _, exists := newSet[fid]; !exists {
				newSet[fid] = struct{}{}
				newFiles = append(newFiles, fid)
			}
		}
	}
	sort.Strings(newFiles)
	messageID := message.String()
	for _, fid := range index.Files {
		if _, keep := newSet[fid]; keep {
			continue
		}
		if err = updateFileGrant(fid, func(grant *fileAccessGrant) {
			grant.References = removeFileReference(grant.References, messageID)
		}); err != nil {
			return err
		}
	}
	for _, fid := range newFiles {
		if err = updateFileGrant(fid, func(grant *fileAccessGrant) {
			grant.References = removeFileReference(grant.References, messageID)
			grant.References = append(grant.References, fileMessageReference{Message: messageID, Topic: topic})
			sort.Slice(grant.References, func(i, j int) bool {
				return grant.References[i].Message < grant.References[j].Message
			})
		}); err != nil {
			return err
		}
	}
	if index.Topic != "" && index.Topic != topic {
		_ = PCache.Delete(fileTopicMessageIndexKey(index.Topic, message))
	}
	if len(newFiles) == 0 {
		_ = PCache.Delete(fileMessageIndexKey(message))
		if topic != "" {
			_ = PCache.Delete(fileTopicMessageIndexKey(topic, message))
		}
		return nil
	}
	index = &fileMessageAccessIndex{Message: messageID, Topic: topic, Files: newFiles}
	raw, err := json.Marshal(index)
	if err != nil {
		return err
	}
	if err = PCache.Upsert(fileMessageIndexKey(message), string(raw), false); err != nil {
		return err
	}
	if topic != "" {
		if err = PCache.Upsert(fileTopicMessageIndexKey(topic, message), fileMessageIndexKey(message), false); err != nil {
			return err
		}
	}
	return nil
}

// RevokeFileMessageAccess 在消息硬删除后回收它授予的文件访问权。
func RevokeFileMessageAccess(message types.Uid) error {
	if message.IsZero() {
		return nil
	}
	index, err := loadFileMessageAccess(message)
	if err != nil {
		return err
	}
	for _, fid := range index.Files {
		if err = updateFileGrant(fid, func(grant *fileAccessGrant) {
			grant.References = removeFileReference(grant.References, message.String())
		}); err != nil {
			return err
		}
	}
	_ = PCache.Delete(fileMessageIndexKey(message))
	if index.Topic != "" {
		_ = PCache.Delete(fileTopicMessageIndexKey(index.Topic, message))
	}
	return nil
}

// RevokeFileTopicAccess 在 Topic 硬删除后分页回收所有消息附件授权。
func RevokeFileTopicAccess(topic string) error {
	if strings.TrimSpace(topic) == "" {
		return nil
	}
	prefix := fileTopicIndexPrefix(topic)
	for {
		entries, err := PCache.List(prefix, 500)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			return nil
		}
		for key, messageIndexKey := range entries {
			messageID := strings.TrimPrefix(messageIndexKey, fileMessageACLPrefix)
			message := types.ParseUid(messageID)
			if !message.IsZero() {
				if err = RevokeFileMessageAccess(message); err != nil {
					return err
				}
			}
			_ = PCache.Delete(key)
		}
	}
}

// CopyFileAccess 把原文件的 ACL 复制给服务端生成的安全预览或转码版本。
func CopyFileAccess(sourceFID, targetURL string) error {
	targetFID := localFileID(targetURL)
	if types.ParseUid(sourceFID).IsZero() || targetFID == "" {
		return types.ErrMalformed
	}
	source, err := loadFileGrant(sourceFID)
	if err != nil {
		return err
	}
	copyGrant := &fileAccessGrant{
		Topics:     append([]string(nil), source.Topics...),
		Users:      append([]string(nil), source.Users...),
		Public:     source.Public,
		References: append([]fileMessageReference(nil), source.References...),
	}
	return updateFileGrant(targetFID, func(grant *fileAccessGrant) {
		*grant = *copyGrant
	})
}

func hasFileReference(references []fileMessageReference, message string) bool {
	for _, reference := range references {
		if reference.Message == message {
			return true
		}
	}
	return false
}

func removeFileReference(references []fileMessageReference, message string) []fileMessageReference {
	filtered := references[:0]
	for _, reference := range references {
		if reference.Message != message {
			filtered = append(filtered, reference)
		}
	}
	return append([]fileMessageReference(nil), filtered...)
}

func effectiveFileTopics(grant *fileAccessGrant) []string {
	if grant == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(grant.Topics)+len(grant.References))
	result := make([]string, 0, len(seen))
	for _, topic := range grant.Topics {
		if topic != "" {
			seen[topic] = struct{}{}
		}
	}
	for _, reference := range grant.References {
		if reference.Topic != "" {
			seen[reference.Topic] = struct{}{}
		}
	}
	for topic := range seen {
		result = append(result, topic)
	}
	sort.Strings(result)
	return result
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func fileScanBlocksAccess(state *FileProcessingState) bool {
	if state == nil {
		return false
	}
	switch state.ScanStatus {
	case "", "clean", "skipped", "disabled", "manually_released":
		return false
	default:
		return true
	}
}

// QuarantineFile 通过媒体后端把恶意对象移出正常下载位置。
func QuarantineFile(definition *types.FileDef) (string, error) {
	if definition == nil || definition.Id == "" || definition.Location == "" {
		return "", types.ErrMalformed
	}
	handler, ok := Store.GetMediaHandler().(media.QuarantineHandler)
	if !ok {
		return "", types.ErrUnsupported
	}
	return handler.Quarantine(context.Background(), definition.Location)
}

func ListQuarantinedFiles(limit int) ([]QuarantinedFileRecord, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	entries, err := PCache.List(fileProcessingPrefix, limit*4)
	if err != nil {
		return nil, err
	}
	result := make([]QuarantinedFileRecord, 0, limit)
	for key, raw := range entries {
		var state FileProcessingState
		if json.Unmarshal([]byte(raw), &state) != nil ||
			(state.QuarantineStatus != "isolated" && state.QuarantineStatus != "isolation_failed") {
			continue
		}
		result = append(result, QuarantinedFileRecord{
			FileID: strings.TrimPrefix(key, fileProcessingPrefix), State: state,
		})
		if len(result) >= limit {
			break
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].State.UpdatedAt.After(result[j].State.UpdatedAt) })
	return result, nil
}

func ReviewQuarantinedFile(fid, action, reviewer, reason string) (*FileProcessingState, error) {
	reason = strings.TrimSpace(reason)
	if types.ParseUid(fid).IsZero() || strings.TrimSpace(reviewer) == "" || reason == "" ||
		len(reason) > 500 || (action != "release" && action != "delete") {
		return nil, types.ErrMalformed
	}
	state, err := GetFileProcessingState(fid)
	if err != nil {
		return nil, err
	}
	if state.QuarantineStatus != "isolated" || state.QuarantineLocation == "" {
		return nil, types.ErrPolicy
	}
	definition, err := Files.Get(fid)
	if err != nil || definition == nil {
		if err == nil {
			err = types.ErrNotFound
		}
		return nil, err
	}
	handler, ok := Store.GetMediaHandler().(media.QuarantineHandler)
	if !ok {
		return nil, types.ErrUnsupported
	}
	if action == "release" {
		err = handler.ReleaseQuarantine(context.Background(), state.QuarantineLocation, definition.Location)
		if err == nil {
			state.QuarantineStatus = "released"
			state.ScanStatus = "manually_released"
			state.ProcessStatus = "ready"
		}
	} else {
		err = handler.DeleteQuarantine(context.Background(), state.QuarantineLocation)
		if err == nil {
			state.QuarantineStatus = "deleted"
			state.ProcessStatus = "blocked"
		}
	}
	if err != nil {
		return nil, err
	}
	now := types.TimeNow()
	state.ReviewedBy = reviewer
	state.ReviewReason = reason
	state.ReviewedAt = &now
	if err = SetFileProcessingState(fid, *state); err != nil {
		return nil, err
	}
	return state, nil
}

func isFileGCProtected(fid string) bool {
	grant, err := loadFileGrant(fid)
	if err == nil && grant.Public {
		return true
	}
	if err != nil && !errors.Is(err, types.ErrNotFound) {
		// 持久化 ACL 暂时不可读时采用 fail-closed，避免误删。
		return true
	}
	state, stateErr := GetFileProcessingState(fid)
	if stateErr == nil && (state.ProcessStatus == "queued" || state.ProcessStatus == "processing" ||
		state.ProcessStatus == "retrying" || state.ProcessStatus == "resumable") {
		return true
	}
	return stateErr != nil && !errors.Is(stateErr, types.ErrNotFound)
}

// ValidateFileAttachments 防止发送者把不属于自己的上传文件附加到新消息。
func ValidateFileAttachments(owner types.Uid, attachmentURLs []string) error {
	for _, rawURL := range attachmentURLs {
		fid := localFileID(rawURL)
		if fid == "" {
			continue
		}
		definition, err := Files.Get(fid)
		if err != nil {
			return err
		}
		if definition == nil || definition.User != owner.String() {
			return types.ErrPermissionDenied
		}
		state, err := GetFileProcessingState(fid)
		if err != nil && !errors.Is(err, types.ErrNotFound) {
			return err
		}
		if fileScanBlocksAccess(state) {
			return types.ErrPermissionDenied
		}
	}
	return nil
}

// AuthorizeFileDownload 校验文件所有者、显式用户授权或 Topic 读权限。
func AuthorizeFileDownload(requester types.Uid, rawURL string) (*types.FileDef, error) {
	definition, _, err := AuthorizeFileDownloadContext(requester, rawURL)
	return definition, err
}

// AuthorizeFileDownloadContext 在完成文件 ACL 校验的同时返回命中的 Topic。
// 上层可据此继续执行商城客户范围等实时业务鉴权；所有者、显式用户和公共授权
// 不依赖 Topic，因此返回空字符串。
func AuthorizeFileDownloadContext(requester types.Uid, rawURL string) (*types.FileDef, string, error) {
	return authorizeFileAccess(requester, rawURL, true)
}

// AuthorizeFileMetadata 允许已授权用户在扫描期间轮询处理状态。
func AuthorizeFileMetadata(requester types.Uid, rawURL string) (*types.FileDef, error) {
	definition, _, err := AuthorizeFileMetadataContext(requester, rawURL)
	return definition, err
}

// AuthorizeFileMetadataContext 是元数据接口对应的带 Topic 上下文版本。
func AuthorizeFileMetadataContext(requester types.Uid, rawURL string) (*types.FileDef, string, error) {
	return authorizeFileAccess(requester, rawURL, false)
}

func authorizeFileAccess(requester types.Uid, rawURL string, enforceScan bool) (*types.FileDef, string, error) {
	fid := localFileID(rawURL)
	if requester.IsZero() || fid == "" {
		return nil, "", types.ErrPermissionDenied
	}
	definition, err := Files.Get(fid)
	if err != nil {
		return nil, "", err
	}
	if definition == nil {
		return nil, "", types.ErrNotFound
	}
	state, err := GetFileProcessingState(fid)
	if err != nil && !errors.Is(err, types.ErrNotFound) {
		return nil, "", err
	}
	if enforceScan && fileScanBlocksAccess(state) {
		return nil, "", types.ErrPermissionDenied
	}
	if definition.User == requester.String() {
		return definition, "", nil
	}
	grant, err := loadFileGrant(fid)
	if err != nil {
		return nil, "", err
	}
	if containsString(grant.Users, requester.UserId()) {
		return definition, "", nil
	}
	if grant.Public {
		return definition, "", nil
	}
	for _, topic := range effectiveFileTopics(grant) {
		candidates := []string{topic}
		if channel := types.GrpToChn(topic); channel != "" && channel != topic {
			candidates = append(candidates, channel)
		}
		for _, candidate := range candidates {
			subscription, subErr := Subs.Get(candidate, requester, false)
			if subErr != nil {
				if errors.Is(subErr, types.ErrNotFound) {
					continue
				}
				return nil, "", subErr
			}
			if subscription != nil && (subscription.ModeWant & subscription.ModeGiven).IsReader() {
				return definition, topic, nil
			}
		}
	}
	return nil, "", types.ErrPermissionDenied
}

// SetFileProcessingState 保存文件扫描、摘要和预览处理状态。
func SetFileProcessingState(fid string, state FileProcessingState) error {
	if types.ParseUid(fid).IsZero() {
		return types.ErrMalformed
	}
	state.UpdatedAt = types.TimeNow()
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return PCache.Upsert(fileProcessingPrefix+fid, string(raw), false)
}

// GetFileProcessingState 读取文件扫描、摘要和预览处理状态。
func GetFileProcessingState(fid string) (*FileProcessingState, error) {
	if types.ParseUid(fid).IsZero() {
		return nil, types.ErrMalformed
	}
	raw, err := PCache.Get(fileProcessingPrefix + fid)
	if err != nil {
		return nil, err
	}
	var state FileProcessingState
	if err = json.Unmarshal([]byte(raw), &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// DeleteFileProcessingState 删除临时分块或已清理文件的处理状态。
func DeleteFileProcessingState(fid string) error {
	if types.ParseUid(fid).IsZero() {
		return types.ErrMalformed
	}
	return PCache.Delete(fileProcessingPrefix + fid)
}
