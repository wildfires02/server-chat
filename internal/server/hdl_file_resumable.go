package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"chat/server/logs"
	"chat/server/media"
	"chat/server/store"
	"chat/server/store/types"
)

const (
	resumableUploadPrefix      = "uploadsession:"
	resumableMultipartMinPart  = int64(5 * 1024 * 1024)
	resumableMultipartMaxPart  = int64(64 * 1024 * 1024)
	resumableMultipartMaxParts = 10_000
)

type resumableUploadState struct {
	Id                string                 `json:"id"`
	Owner             string                 `json:"owner"`
	MimeType          string                 `json:"mime"`
	Length            int64                  `json:"length"`
	Offset            int64                  `json:"offset"`
	Chunks            []resumableUploadChunk `json:"chunks,omitempty"`
	MultipartUploadID string                 `json:"multipart_upload_id,omitempty"`
	MultipartLocation string                 `json:"multipart_location,omitempty"`
	MultipartPartSize int64                  `json:"multipart_part_size,omitempty"`
	MultipartParts    []media.MultipartPart  `json:"multipart_parts,omitempty"`
	ResultURL         string                 `json:"result_url,omitempty"`
	CreatedAt         time.Time              `json:"created_at"`
	UpdatedAt         time.Time              `json:"updated_at"`
	CompletedAt       *time.Time             `json:"completed_at,omitempty"`
}

func saveResumableUpload(state *resumableUploadState) error {
	state.UpdatedAt = types.TimeNow()
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return store.PCache.Upsert(resumableUploadPrefix+state.Id, string(raw), false)
}

func loadResumableUpload(id string) (*resumableUploadState, error) {
	raw, err := store.PCache.Get(resumableUploadPrefix + id)
	if err != nil {
		return nil, err
	}
	var state resumableUploadState
	if err = json.Unmarshal([]byte(raw), &state); err != nil {
		return nil, err
	}
	if state.Id != id || state.Owner == "" || state.Length <= 0 ||
		state.Offset < 0 || state.Offset > state.Length {
		return nil, types.ErrMalformed
	}
	return &state, nil
}

func deleteResumableUpload(state *resumableUploadState) error {
	if state == nil {
		return nil
	}
	if err := resumableChunks.Delete(state.Chunks); err != nil {
		return err
	}
	if state.MultipartUploadID != "" && state.CompletedAt == nil {
		handler, ok := store.Store.GetMediaHandler().(media.MultipartHandler)
		if !ok {
			return types.ErrUnsupported
		}
		definition := resumableMultipartDefinition(state)
		if err := handler.AbortMultipartUpload(context.Background(), definition, state.MultipartUploadID); err != nil {
			return err
		}
		_, _ = store.Files.FinishUpload(definition, false, 0)
	}
	return store.PCache.Delete(resumableUploadPrefix + state.Id)
}

func writeResumableError(wrt http.ResponseWriter, message *ServerComMessage) {
	wrt.Header().Set("Content-Type", "application/json; charset=utf-8")
	wrt.WriteHeader(message.Ctrl.Code)
	_ = json.NewEncoder(wrt).Encode(message)
}

func authenticateResumableRequest(req *http.Request) (types.Uid, *ServerComMessage) {
	now := types.TimeNow()
	if valid, _ := checkAPIKey(getAPIKey(req)); !valid {
		return types.ZeroUid, ErrAPIKeyRequired(now)
	}
	authMethod, secret := getHttpAuth(req)
	uid, challenge, err := authFileRequest(authMethod, secret, req.FormValue("sid"), getRemoteAddr(req))
	if err != nil {
		return types.ZeroUid, decodeStoreError(err, "", now, nil)
	}
	if challenge != nil {
		return types.ZeroUid, InfoChallenge("", now, challenge)
	}
	if uid.IsZero() {
		return types.ZeroUid, ErrAuthRequired("", "", now, now)
	}
	return uid, nil
}

// resumableFileHTTP 实现基于 Upload-Length/Upload-Offset 的断点续传。
func resumableFileHTTP(wrt http.ResponseWriter, req *http.Request) {
	now := types.TimeNow()
	if origin := req.Header.Get("Origin"); origin != "" {
		wrt.Header().Set("Access-Control-Allow-Origin", origin)
		wrt.Header().Set("Access-Control-Allow-Credentials", "true")
		wrt.Header().Add("Vary", "Origin")
	}
	wrt.Header().Set("Cache-Control", "no-store")
	wrt.Header().Set("Tus-Resumable", "1.0.0")
	wrt.Header().Set("Tus-Version", "1.0.0")
	wrt.Header().Set("Tus-Extension", "creation,termination")
	if globals.maxFileUploadSize > 0 {
		wrt.Header().Set("Tus-Max-Size", strconv.FormatInt(globals.maxFileUploadSize, 10))
	}
	wrt.Header().Set("Access-Control-Expose-Headers",
		"Location, Upload-Length, Upload-Offset, Upload-Result-URL, Upload-Part-Size, Tus-Resumable, Tus-Version, Tus-Extension, Tus-Max-Size")
	if req.Method == http.MethodOptions {
		wrt.Header().Set("Allow", "POST, PATCH, HEAD, DELETE, OPTIONS")
		wrt.Header().Set("Access-Control-Allow-Methods", "POST, PATCH, HEAD, DELETE, OPTIONS")
		wrt.Header().Set("Access-Control-Allow-Headers",
			"Authorization, X-IM-APIKey, X-IM-Auth, Tus-Resumable, Upload-Length, Upload-Offset, Upload-Metadata, Upload-Mime-Type, Upload-Part-Size, Content-Type")
		wrt.WriteHeader(http.StatusNoContent)
		return
	}
	if req.Header.Get("Tus-Resumable") != "1.0.0" {
		wrt.WriteHeader(http.StatusPreconditionFailed)
		return
	}
	uid, authError := authenticateResumableRequest(req)
	if authError != nil {
		writeResumableError(wrt, authError)
		return
	}
	if req.Method == http.MethodPost {
		createResumableUpload(wrt, req, uid)
		return
	}

	id := path.Base(strings.TrimSuffix(req.URL.Path, "/"))
	if types.ParseUid(id).IsZero() {
		writeResumableError(wrt, ErrMalformed("", "", now))
		return
	}
	state, err := loadResumableUpload(id)
	if err != nil {
		writeResumableError(wrt, decodeStoreError(err, "", now, nil))
		return
	}
	if state.Owner != uid.String() {
		writeResumableError(wrt, ErrPermissionDenied("", "", now))
		return
	}

	if req.Method == http.MethodHead {
		wrt.Header().Set("Upload-Length", strconv.FormatInt(state.Length, 10))
		wrt.Header().Set("Upload-Offset", strconv.FormatInt(state.Offset, 10))
		if state.MultipartPartSize > 0 {
			wrt.Header().Set("Upload-Part-Size", strconv.FormatInt(state.MultipartPartSize, 10))
		}
		if state.ResultURL != "" {
			wrt.Header().Set("Upload-Result-URL", state.ResultURL)
		}
		wrt.WriteHeader(http.StatusNoContent)
		return
	}
	if req.Method != http.MethodPatch && req.Method != http.MethodDelete {
		writeResumableError(wrt, ErrOperationNotAllowed("", "", now))
		return
	}
	lease, err := acquireResumableUploadLease(id, resumableLeaseTTL)
	if err != nil {
		if errors.Is(err, errResumableLeaseBusy) {
			statsInc("ResumableUploadLeaseConflicts", 1)
			wrt.Header().Set("Retry-After", "2")
			writeResumableError(wrt, &ServerComMessage{Ctrl: &MsgServerCtrl{
				Code: http.StatusLocked, Text: "upload is active on another node", Timestamp: now,
			}})
			return
		}
		writeResumableError(wrt, decodeStoreError(err, "", now, nil))
		return
	}
	defer releaseResumableUploadLease(id, lease)
	state, err = loadResumableUpload(id)
	if err != nil {
		writeResumableError(wrt, decodeStoreError(err, "", now, nil))
		return
	}
	if state.Owner != uid.String() {
		writeResumableError(wrt, ErrPermissionDenied("", "", now))
		return
	}

	switch req.Method {
	case http.MethodPatch:
		if contentType := req.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/offset+octet-stream") {
			writeResumableError(wrt, ErrMalformed("", "", now))
			return
		}
		appendResumableChunk(wrt, req, state, now)
	case http.MethodDelete:
		if err = deleteResumableUpload(state); err != nil {
			writeResumableError(wrt, decodeStoreError(err, "", now, nil))
			return
		}
		wrt.WriteHeader(http.StatusNoContent)
	}
}

func createResumableUpload(wrt http.ResponseWriter, req *http.Request, uid types.Uid) {
	now := types.TimeNow()
	length, err := strconv.ParseInt(req.Header.Get("Upload-Length"), 10, 64)
	if err != nil || length <= 0 {
		writeResumableError(wrt, ErrMalformed("", "", now))
		return
	}
	if globals.maxFileUploadSize > 0 && length > globals.maxFileUploadSize {
		writeResumableError(wrt, ErrTooLarge("", "", now))
		return
	}
	mimeType := req.Header.Get("Upload-Mime-Type")
	for _, item := range strings.Split(req.Header.Get("Upload-Metadata"), ",") {
		fields := strings.Fields(strings.TrimSpace(item))
		if len(fields) != 2 || (fields[0] != "mime" && fields[0] != "filetype") {
			continue
		}
		if decoded, decodeErr := base64.StdEncoding.DecodeString(fields[1]); decodeErr == nil {
			mimeType = string(decoded)
		}
	}
	state := &resumableUploadState{
		Id:        store.Store.GetUidString(),
		Owner:     uid.String(),
		MimeType:  mimeType,
		Length:    length,
		CreatedAt: now,
	}
	if rawPartSize := req.Header.Get("Upload-Part-Size"); rawPartSize != "" {
		partSize, parseErr := strconv.ParseInt(rawPartSize, 10, 64)
		if parseErr != nil || partSize < resumableMultipartMinPart ||
			partSize > resumableMultipartMaxPart {
			writeResumableError(wrt, ErrMalformed("", "", now))
			return
		}
		partCount := (length + partSize - 1) / partSize
		if partCount <= 0 || partCount > resumableMultipartMaxParts {
			writeResumableError(wrt, ErrMalformed("", "", now))
			return
		}
		streaming, supported := store.Store.GetMediaHandler().(media.StreamingMultipartHandler)
		if supported {
			definition := &types.FileDef{
				ObjHeader: types.ObjHeader{Id: state.Id}, User: state.Owner,
				MimeType: normalizeDirectUploadMIME(state.MimeType),
			}
			definition.InitTimes()
			state.CreatedAt = definition.CreatedAt
			if err = store.Files.StartUpload(definition); err != nil {
				writeResumableError(wrt, decodeStoreError(err, "", now, nil))
				return
			}
			uploadID, createErr := streaming.CreateMultipartUpload(req.Context(), definition)
			if createErr != nil {
				_, _ = store.Files.FinishUpload(definition, false, 0)
				writeResumableError(wrt, decodeStoreError(createErr, "", now, nil))
				return
			}
			state.MimeType = definition.MimeType
			state.MultipartUploadID = uploadID
			state.MultipartLocation = definition.Location
			state.MultipartPartSize = partSize
		}
	}
	if err = saveResumableUpload(state); err != nil {
		if state.MultipartUploadID != "" {
			_ = deleteResumableUpload(state)
		}
		writeResumableError(wrt, decodeStoreError(err, "", now, nil))
		return
	}
	location := strings.TrimSuffix(req.URL.Path, "/") + "/" + state.Id
	wrt.Header().Set("Location", location)
	wrt.Header().Set("Upload-Length", strconv.FormatInt(length, 10))
	wrt.Header().Set("Upload-Offset", "0")
	if state.MultipartPartSize > 0 {
		wrt.Header().Set("Upload-Part-Size", strconv.FormatInt(state.MultipartPartSize, 10))
	}
	wrt.WriteHeader(http.StatusCreated)
}

func appendResumableChunk(
	wrt http.ResponseWriter,
	req *http.Request,
	state *resumableUploadState,
	now time.Time,
) {
	offset, err := strconv.ParseInt(req.Header.Get("Upload-Offset"), 10, 64)
	if err != nil || offset != state.Offset {
		wrt.Header().Set("Upload-Offset", strconv.FormatInt(state.Offset, 10))
		if state.ResultURL != "" {
			wrt.Header().Set("Upload-Result-URL", state.ResultURL)
		}
		writeResumableError(wrt, &ServerComMessage{Ctrl: &MsgServerCtrl{
			Code: http.StatusConflict, Text: "upload offset mismatch", Timestamp: now,
		}})
		return
	}
	if state.ResultURL != "" {
		wrt.Header().Set("Upload-Offset", strconv.FormatInt(state.Offset, 10))
		wrt.Header().Set("Upload-Result-URL", state.ResultURL)
		wrt.WriteHeader(http.StatusNoContent)
		return
	}
	if state.MultipartUploadID != "" {
		appendResumableMultipartPart(wrt, req, state, now)
		return
	}
	remaining := state.Length - state.Offset
	chunk, err := resumableChunks.Put(state.Owner, req.Body, remaining)
	if err != nil {
		if errors.Is(err, errFileUploadTooLarge) {
			writeResumableError(wrt, ErrTooLarge("", "", now))
		} else {
			writeResumableError(wrt, decodeStoreError(err, "", now, nil))
		}
		return
	}
	state.Chunks = append(state.Chunks, chunk)
	state.Offset += chunk.Size
	statsInc("ResumableUploadChunks", 1)
	if state.Offset < state.Length {
		if err = saveResumableUpload(state); err != nil {
			state.Chunks = state.Chunks[:len(state.Chunks)-1]
			state.Offset -= chunk.Size
			_ = resumableChunks.Delete([]resumableUploadChunk{chunk})
			writeResumableError(wrt, decodeStoreError(err, "", now, nil))
			return
		}
		wrt.Header().Set("Upload-Offset", strconv.FormatInt(state.Offset, 10))
		wrt.WriteHeader(http.StatusNoContent)
		return
	}

	source, err := resumableChunks.Open(state.Chunks)
	if err != nil {
		state.Chunks = state.Chunks[:len(state.Chunks)-1]
		state.Offset -= chunk.Size
		_ = resumableChunks.Delete([]resumableUploadChunk{chunk})
		writeResumableError(wrt, decodeStoreError(err, "", now, nil))
		return
	}
	defer source.Close()
	reader, mimeType, err := prepareFileUploadReader(source, state.MimeType)
	if err != nil {
		_ = deleteResumableUpload(state)
		writeResumableError(wrt, ErrMalformed("", "", now))
		return
	}
	definition := &types.FileDef{
		ObjHeader: types.ObjHeader{Id: store.Store.GetUidString()},
		User:      state.Owner,
		MimeType:  mimeType,
	}
	definition.InitTimes()
	rawURL, completed, _, err := uploadAndFinalizeFile(
		store.Store.GetMediaHandler(), store.Files, definition, reader, state.Length,
	)
	if err != nil {
		state.Chunks = state.Chunks[:len(state.Chunks)-1]
		state.Offset -= chunk.Size
		_ = resumableChunks.Delete([]resumableUploadChunk{chunk})
		writeResumableError(wrt, decodeStoreError(err, "", now, nil))
		return
	}
	completedAt := types.TimeNow()
	state.ResultURL = rawURL
	state.CompletedAt = &completedAt
	if err = saveResumableUpload(state); err != nil {
		writeResumableError(wrt, decodeStoreError(err, "", now, nil))
		return
	}
	if cleanupErr := resumableChunks.Delete(state.Chunks); cleanupErr != nil {
		logs.Warn.Printf("resumable upload chunk cleanup failed, id=%s: %v", state.Id, cleanupErr)
	} else {
		state.Chunks = nil
		if err = saveResumableUpload(state); err != nil {
			logs.Warn.Printf("resumable upload completion compaction failed, id=%s: %v", state.Id, err)
		}
	}
	queueFileProcessing(completed, rawURL)
	wrt.Header().Set("Upload-Offset", strconv.FormatInt(state.Offset, 10))
	wrt.Header().Set("Upload-Result-URL", rawURL)
	wrt.WriteHeader(http.StatusNoContent)
}

func resumableMultipartDefinition(state *resumableUploadState) *types.FileDef {
	return &types.FileDef{
		ObjHeader: types.ObjHeader{Id: state.Id, CreatedAt: state.CreatedAt, UpdatedAt: state.UpdatedAt},
		User:      state.Owner, MimeType: state.MimeType, Location: state.MultipartLocation,
	}
}

// appendResumableMultipartPart streams one PATCH body straight into object storage.
func appendResumableMultipartPart(
	wrt http.ResponseWriter,
	req *http.Request,
	state *resumableUploadState,
	now time.Time,
) {
	handler, ok := store.Store.GetMediaHandler().(media.StreamingMultipartHandler)
	if !ok {
		writeResumableError(wrt, ErrOperationNotAllowed("", "", now))
		return
	}
	remaining := state.Length - state.Offset
	expected := state.MultipartPartSize
	if remaining < expected {
		expected = remaining
	}
	if expected <= 0 || (req.ContentLength >= 0 && req.ContentLength != expected) {
		writeResumableError(wrt, ErrMalformed("", "", now))
		return
	}
	partNumber := len(state.MultipartParts) + 1
	definition := resumableMultipartDefinition(state)
	body := http.MaxBytesReader(wrt, req.Body, expected)
	part, err := handler.UploadMultipartPart(
		req.Context(), definition, state.MultipartUploadID, partNumber, state.Offset, body, expected,
	)
	if err != nil {
		writeResumableError(wrt, decodeStoreError(err, "", now, nil))
		return
	}
	nextParts := append(append([]media.MultipartPart(nil), state.MultipartParts...), part)
	nextOffset := state.Offset + expected
	statsInc("ResumableUploadChunks", 1)
	if nextOffset < state.Length {
		state.MultipartParts = nextParts
		state.Offset = nextOffset
		if err = saveResumableUpload(state); err != nil {
			writeResumableError(wrt, decodeStoreError(err, "", now, nil))
			return
		}
		wrt.Header().Set("Upload-Offset", strconv.FormatInt(state.Offset, 10))
		wrt.WriteHeader(http.StatusNoContent)
		return
	}

	rawURL, size, err := handler.CompleteMultipartUpload(
		req.Context(), definition, state.MultipartUploadID, nextParts,
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
	completedAt := types.TimeNow()
	state.MultipartParts = nextParts
	state.Offset = nextOffset
	state.ResultURL = rawURL
	state.CompletedAt = &completedAt
	if err = saveResumableUpload(state); err != nil {
		logs.Warn.Printf("resumable multipart completion state save failed, id=%s: %v", state.Id, err)
	}
	queueFileProcessing(completed, rawURL)
	wrt.Header().Set("Upload-Offset", strconv.FormatInt(state.Offset, 10))
	wrt.Header().Set("Upload-Result-URL", rawURL)
	wrt.WriteHeader(http.StatusNoContent)
}

func expireResumableUploads(olderThan time.Time) error {
	if err := expireDirectUploads(olderThan); err != nil {
		return fmt.Errorf("expire direct uploads: %w", err)
	}
	for {
		entries, err := store.PCache.List(resumableUploadPrefix, 1000)
		if err != nil {
			return fmt.Errorf("list resumable upload metadata: %w", err)
		}
		foundExpired := false
		removedExpired := false
		for key, raw := range entries {
			var state resumableUploadState
			if jsonErr := json.Unmarshal([]byte(raw), &state); jsonErr != nil ||
				!strings.HasPrefix(key, resumableUploadPrefix) || !state.CreatedAt.Before(olderThan) {
				continue
			}
			foundExpired = true
			lease, leaseErr := acquireResumableUploadLease(state.Id, resumableLeaseTTL)
			if leaseErr != nil {
				if errors.Is(leaseErr, errResumableLeaseBusy) {
					continue
				}
				return leaseErr
			}
			current, loadErr := loadResumableUpload(state.Id)
			if loadErr == nil && current.CreatedAt.Before(olderThan) {
				if deleteErr := deleteResumableUpload(current); deleteErr != nil {
					releaseResumableUploadLease(state.Id, lease)
					return deleteErr
				}
				removedExpired = true
			}
			releaseResumableUploadLease(state.Id, lease)
		}
		if len(entries) < 1000 || !foundExpired || !removedExpired {
			break
		}
	}
	if err := store.PCache.Expire(resumableLeasePrefix, olderThan); err != nil {
		return fmt.Errorf("expire resumable upload leases: %w", err)
	}
	return nil
}
