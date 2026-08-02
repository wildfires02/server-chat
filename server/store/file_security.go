package store

import (
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"chat/server/store/types"
)

const (
	fileACLPrefix        = "fileacl:"
	fileProcessingPrefix = "fileproc:"
)

// FileProcessingState 保存内容摘要、安全扫描和在线预览处理结果。
type FileProcessingState struct {
	SHA256        string            `json:"sha256,omitempty"`
	ScanStatus    string            `json:"scan_status,omitempty"`
	ProcessStatus string            `json:"process_status,omitempty"`
	Preview       map[string]string `json:"preview,omitempty"`
	Error         string            `json:"error,omitempty"`
	Attempts      int               `json:"attempts,omitempty"`
	NextRetryAt   *time.Time        `json:"next_retry_at,omitempty"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

type fileAccessGrant struct {
	Topics []string `json:"topics,omitempty"`
	Users  []string `json:"users,omitempty"`
	Public bool     `json:"public,omitempty"`
}

// SetFilePublicAccess 发布或撤销公共素材文件；仍要求请求方已登录。
func SetFilePublicAccess(rawURL string, public bool) error {
	fid := localFileID(rawURL)
	if fid == "" {
		// 外部 CDN 地址不由本服务的文件 ACL 管理。
		return nil
	}
	lock := fileSecurityLock(fid)
	lock.Lock()
	grant, err := loadFileGrant(fid)
	if err != nil {
		lock.Unlock()
		return err
	}
	grant.Public = public
	err = saveFileGrant(fid, grant)
	lock.Unlock()
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

func loadFileGrant(fid string) (*fileAccessGrant, error) {
	raw, err := PCache.Get(fileACLPrefix + fid)
	if errors.Is(err, types.ErrNotFound) {
		return &fileAccessGrant{}, nil
	}
	if err != nil {
		return nil, err
	}
	var grant fileAccessGrant
	if err = json.Unmarshal([]byte(raw), &grant); err != nil {
		return nil, err
	}
	return &grant, nil
}

func saveFileGrant(fid string, grant *fileAccessGrant) error {
	raw, err := json.Marshal(grant)
	if err != nil {
		return err
	}
	return PCache.Upsert(fileACLPrefix+fid, string(raw), false)
}

// GrantFileAccess 把本地文件授权给 Topic 成员或指定用户。
func GrantFileAccess(topic string, user types.Uid, attachmentURLs []string) error {
	for _, rawURL := range attachmentURLs {
		fid := localFileID(rawURL)
		if fid == "" {
			continue
		}
		lock := fileSecurityLock(fid)
		lock.Lock()
		grant, err := loadFileGrant(fid)
		if err == nil {
			if topic != "" && !containsString(grant.Topics, topic) {
				grant.Topics = append(grant.Topics, topic)
				sort.Strings(grant.Topics)
			}
			if !user.IsZero() && !containsString(grant.Users, user.UserId()) {
				grant.Users = append(grant.Users, user.UserId())
				sort.Strings(grant.Users)
			}
			err = saveFileGrant(fid, grant)
		}
		lock.Unlock()
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
		Topics: append([]string(nil), source.Topics...),
		Users:  append([]string(nil), source.Users...),
		Public: source.Public,
	}
	lock := fileSecurityLock(targetFID)
	lock.Lock()
	err = saveFileGrant(targetFID, copyGrant)
	lock.Unlock()
	return err
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
	case "", "clean", "skipped", "disabled":
		return false
	default:
		return true
	}
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
	for _, topic := range grant.Topics {
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
