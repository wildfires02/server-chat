package server

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"chat/server/logs"
	"chat/server/media"
	"chat/server/store"
	"chat/server/store/types"
)

const (
	directUploadPrefix   = "directupload:"
	directUploadPartSize = int64(8 * 1024 * 1024)
	directUploadMaxParts = 10_000
)

type directUploadState struct {
	ID          string     `json:"id"`
	Owner       string     `json:"owner"`
	MimeType    string     `json:"mime"`
	Length      int64      `json:"length"`
	PartSize    int64      `json:"part_size"`
	PartCount   int        `json:"part_count"`
	UploadID    string     `json:"upload_id"`
	Location    string     `json:"location"`
	ResultURL   string     `json:"result_url,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type directUploadCreateRequest struct {
	Size     int64  `json:"size"`
	MimeType string `json:"mime"`
}

type directUploadCompleteRequest struct {
	Parts []media.MultipartPart `json:"parts"`
}

type directUploadResponse struct {
	ID        string                 `json:"id"`
	PartSize  int64                  `json:"part_size"`
	Parts     []*media.PresignedPart `json:"parts,omitempty"`
	URL       string                 `json:"url,omitempty"`
	ExpiresAt time.Time              `json:"expires_at,omitempty"`
}

func saveDirectUpload(state *directUploadState) error {
	state.UpdatedAt = types.TimeNow()
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return store.PCache.Upsert(directUploadPrefix+state.ID, string(raw), false)
}

func loadDirectUpload(id string) (*directUploadState, error) {
	raw, err := store.PCache.Get(directUploadPrefix + id)
	if err != nil {
		return nil, err
	}
	var state directUploadState
	if err = json.Unmarshal([]byte(raw), &state); err != nil {
		return nil, err
	}
	if state.ID != id || state.Owner == "" || state.Length <= 0 || state.UploadID == "" ||
		state.Location == "" || state.PartCount <= 0 {
		return nil, types.ErrMalformed
	}
	return &state, nil
}

func directMultipartHandler() (media.MultipartHandler, error) {
	handler, ok := store.Store.GetMediaHandler().(media.MultipartHandler)
	if !ok {
		return nil, types.ErrUnsupported
	}
	if capability, ok := handler.(media.DirectUploadCapability); ok && !capability.DirectUploadEnabled() {
		return nil, types.ErrUnsupported
	}
	return handler, nil
}

func multipartHandler() (media.MultipartHandler, error) {
	handler, ok := store.Store.GetMediaHandler().(media.MultipartHandler)
	if !ok {
		return nil, types.ErrUnsupported
	}
	return handler, nil
}

func normalizeDirectUploadMIME(value string) string {
	mediaType, params, err := mime.ParseMediaType(value)
	if err != nil {
		return "application/octet-stream"
	}
	for _, prefix := range allowedMimeTypes {
		if strings.HasPrefix(mediaType, prefix) {
			if formatted := mime.FormatMediaType(mediaType, params); formatted != "" {
				return formatted
			}
		}
	}
	return "application/octet-stream"
}

// directFileHTTP 创建、完成或取消浏览器到对象存储的 Multipart 直传。
func directFileHTTP(wrt http.ResponseWriter, req *http.Request) {
	now := types.TimeNow()
	wrt.Header().Set("Content-Type", "application/json; charset=utf-8")
	wrt.Header().Set("Cache-Control", "no-store")
	if origin := req.Header.Get("Origin"); origin != "" {
		wrt.Header().Set("Access-Control-Allow-Origin", origin)
		wrt.Header().Set("Access-Control-Allow-Credentials", "true")
		wrt.Header().Add("Vary", "Origin")
	}
	if req.Method == http.MethodOptions {
		wrt.Header().Set("Allow", "POST, DELETE, OPTIONS")
		wrt.Header().Set("Access-Control-Allow-Methods", "POST, DELETE, OPTIONS")
		wrt.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-IM-APIKey, X-IM-Auth")
		wrt.WriteHeader(http.StatusNoContent)
		return
	}
	uid, authError := authenticateResumableRequest(req)
	if authError != nil {
		writeResumableError(wrt, authError)
		return
	}
	handler, err := directMultipartHandler()
	if err != nil {
		writeResumableError(wrt, ErrOperationNotAllowed("", "", now))
		return
	}

	trimmed := strings.Trim(strings.TrimPrefix(req.URL.Path, "/"), "/")
	segments := strings.Split(trimmed, "/")
	// 路由挂载点最后一段是 direct；没有后续 ID 时创建会话。
	directIndex := -1
	for index, segment := range segments {
		if segment == "direct" {
			directIndex = index
		}
	}
	if directIndex < 0 {
		writeResumableError(wrt, ErrMalformed("", "", now))
		return
	}
	remaining := segments[directIndex+1:]
	if len(remaining) == 0 || remaining[0] == "" {
		if req.Method != http.MethodPost {
			writeResumableError(wrt, ErrOperationNotAllowed("", "", now))
			return
		}
		createDirectUpload(wrt, req, uid, handler)
		return
	}
	id := path.Base(remaining[0])
	if types.ParseUid(id).IsZero() {
		writeResumableError(wrt, ErrMalformed("", "", now))
		return
	}
	state, err := loadDirectUpload(id)
	if err != nil {
		writeResumableError(wrt, decodeStoreError(err, "", now, nil))
		return
	}
	if state.Owner != uid.String() {
		writeResumableError(wrt, ErrPermissionDenied("", "", now))
		return
	}
	lease, err := acquireResumableUploadLease(id, resumableLeaseTTL)
	if err != nil {
		writeResumableError(wrt, &ServerComMessage{Ctrl: &MsgServerCtrl{
			Code: http.StatusLocked, Text: "upload is active on another node", Timestamp: now,
		}})
		return
	}
	defer releaseResumableUploadLease(id, lease)

	if req.Method == http.MethodDelete && len(remaining) == 1 {
		abortDirectUpload(req.Context(), state, handler)
		wrt.WriteHeader(http.StatusNoContent)
		return
	}
	if req.Method == http.MethodPost && len(remaining) == 2 && remaining[1] == "complete" {
		completeDirectUpload(wrt, req, state, handler)
		return
	}
	writeResumableError(wrt, ErrOperationNotAllowed("", "", now))
}

func createDirectUpload(
	wrt http.ResponseWriter,
	req *http.Request,
	uid types.Uid,
	handler media.MultipartHandler,
) {
	now := types.TimeNow()
	var input directUploadCreateRequest
	decoder := json.NewDecoder(http.MaxBytesReader(wrt, req.Body, 64*1024))
	if err := decoder.Decode(&input); err != nil || input.Size <= 0 {
		writeResumableError(wrt, ErrMalformed("", "", now))
		return
	}
	if globals.maxFileUploadSize > 0 && input.Size > globals.maxFileUploadSize {
		writeResumableError(wrt, ErrTooLarge("", "", now))
		return
	}
	partCount := int((input.Size + directUploadPartSize - 1) / directUploadPartSize)
	if partCount <= 0 || partCount > directUploadMaxParts {
		writeResumableError(wrt, ErrTooLarge("", "", now))
		return
	}
	definition := &types.FileDef{
		ObjHeader: types.ObjHeader{Id: store.Store.GetUidString()},
		User:      uid.String(), MimeType: normalizeDirectUploadMIME(input.MimeType),
	}
	definition.InitTimes()
	if err := store.Files.StartUpload(definition); err != nil {
		writeResumableError(wrt, decodeStoreError(err, "", now, nil))
		return
	}
	uploadID, err := handler.CreateMultipartUpload(req.Context(), definition)
	if err != nil {
		_, _ = store.Files.FinishUpload(definition, false, 0)
		writeResumableError(wrt, decodeStoreError(err, "", now, nil))
		return
	}
	state := &directUploadState{
		ID: definition.Id, Owner: definition.User, MimeType: definition.MimeType,
		Length: input.Size, PartSize: directUploadPartSize, PartCount: partCount,
		UploadID: uploadID, Location: definition.Location, CreatedAt: now,
	}
	if err = saveDirectUpload(state); err != nil {
		_ = handler.AbortMultipartUpload(context.Background(), definition, uploadID)
		_, _ = store.Files.FinishUpload(definition, false, 0)
		writeResumableError(wrt, decodeStoreError(err, "", now, nil))
		return
	}
	parts := make([]*media.PresignedPart, 0, partCount)
	for partNumber := 1; partNumber <= partCount; partNumber++ {
		part, partErr := handler.PresignMultipartPart(req.Context(), definition, uploadID, partNumber)
		if partErr != nil {
			abortDirectUpload(context.Background(), state, handler)
			writeResumableError(wrt, decodeStoreError(partErr, "", now, nil))
			return
		}
		parts = append(parts, part)
	}
	wrt.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(wrt).Encode(directUploadResponse{
		ID: state.ID, PartSize: state.PartSize, Parts: parts, ExpiresAt: now.Add(24 * time.Hour),
	})
}

func directUploadDefinition(state *directUploadState) *types.FileDef {
	definition := &types.FileDef{
		ObjHeader: types.ObjHeader{Id: state.ID, CreatedAt: state.CreatedAt, UpdatedAt: state.UpdatedAt},
		User:      state.Owner, MimeType: state.MimeType, Location: state.Location,
	}
	return definition
}

func completeDirectUpload(
	wrt http.ResponseWriter,
	req *http.Request,
	state *directUploadState,
	handler media.MultipartHandler,
) {
	now := types.TimeNow()
	if state.ResultURL != "" {
		_ = json.NewEncoder(wrt).Encode(directUploadResponse{ID: state.ID, URL: state.ResultURL})
		return
	}
	var input directUploadCompleteRequest
	if err := json.NewDecoder(http.MaxBytesReader(wrt, req.Body, 1024*1024)).Decode(&input); err != nil ||
		len(input.Parts) != state.PartCount {
		writeResumableError(wrt, ErrMalformed("", "", now))
		return
	}
	sort.Slice(input.Parts, func(i, j int) bool { return input.Parts[i].PartNumber < input.Parts[j].PartNumber })
	for index, part := range input.Parts {
		if part.PartNumber != index+1 || strings.TrimSpace(part.ETag) == "" {
			writeResumableError(wrt, ErrMalformed("", "", now))
			return
		}
	}
	definition := directUploadDefinition(state)
	rawURL, size, err := handler.CompleteMultipartUpload(
		req.Context(), definition, state.UploadID, input.Parts,
	)
	if err != nil {
		writeResumableError(wrt, decodeStoreError(err, "", now, nil))
		return
	}
	if size != state.Length {
		_ = store.Store.GetMediaHandler().Delete([]string{definition.Location})
		_, _ = store.Files.FinishUpload(definition, false, 0)
		writeResumableError(wrt, ErrMalformed("", "", now))
		return
	}
	completed, err := store.Files.FinishUpload(definition, true, size)
	if err != nil {
		_ = store.Store.GetMediaHandler().Delete([]string{definition.Location})
		_, _ = store.Files.FinishUpload(definition, false, 0)
		writeResumableError(wrt, decodeStoreError(err, "", now, nil))
		return
	}
	if completed == nil {
		completed = definition
	}
	completedAt := now
	state.ResultURL, state.CompletedAt = rawURL, &completedAt
	if err = saveDirectUpload(state); err != nil {
		logs.Warn.Printf("direct upload completion state save failed, id=%s: %v", state.ID, err)
	}
	queueFileProcessing(completed, rawURL)
	_ = json.NewEncoder(wrt).Encode(directUploadResponse{ID: state.ID, URL: rawURL})
}

func abortDirectUpload(ctx context.Context, state *directUploadState, handler media.MultipartHandler) {
	if state == nil {
		return
	}
	definition := directUploadDefinition(state)
	if state.CompletedAt == nil {
		_ = handler.AbortMultipartUpload(ctx, definition, state.UploadID)
		_, _ = store.Files.FinishUpload(definition, false, 0)
	}
	_ = store.PCache.Delete(directUploadPrefix + state.ID)
}

func expireDirectUploads(olderThan time.Time) error {
	handler, err := multipartHandler()
	if err != nil {
		if errors.Is(err, types.ErrUnsupported) {
			return nil
		}
		return err
	}
	entries, err := store.PCache.List(directUploadPrefix, 1000)
	if err != nil {
		return err
	}
	for key, raw := range entries {
		var state directUploadState
		if json.Unmarshal([]byte(raw), &state) != nil || !strings.HasPrefix(key, directUploadPrefix) ||
			!state.CreatedAt.Before(olderThan) {
			continue
		}
		abortDirectUpload(context.Background(), &state, handler)
	}
	return nil
}
