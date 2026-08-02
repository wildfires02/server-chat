package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"chat/server/store"
	"chat/server/store/types"
)

type fileMetaResponse struct {
	Id         string                     `json:"id"`
	MimeType   string                     `json:"mime"`
	Size       int64                      `json:"size"`
	ETag       string                     `json:"etag,omitempty"`
	Processing *store.FileProcessingState `json:"processing,omitempty"`
}

// largeFileMetaHTTP 返回文件摘要、安全扫描和在线预览状态。
func largeFileMetaHTTP(wrt http.ResponseWriter, req *http.Request) {
	now := types.TimeNow()
	writeError := func(message *ServerComMessage, err error) {
		wrt.Header().Set("Content-Type", "application/json; charset=utf-8")
		wrt.WriteHeader(message.Ctrl.Code)
		_ = json.NewEncoder(wrt).Encode(message)
		if err != nil {
			return
		}
	}
	if req.Method == http.MethodOptions {
		wrt.WriteHeader(http.StatusNoContent)
		return
	}
	if req.Method != http.MethodGet && req.Method != http.MethodHead && req.Method != http.MethodPost {
		writeError(ErrOperationNotAllowed("", "", now), errors.New("method not allowed"))
		return
	}
	if valid, _ := checkAPIKey(getAPIKey(req)); !valid {
		writeError(ErrAPIKeyRequired(now), types.ErrPermissionDenied)
		return
	}
	authMethod, secret := getHttpAuth(req)
	uid, challenge, err := authFileRequest(authMethod, secret, req.FormValue("sid"), getRemoteAddr(req))
	if err != nil {
		writeError(decodeStoreError(err, "", now, nil), err)
		return
	}
	if challenge != nil {
		writeError(InfoChallenge("", now, challenge), nil)
		return
	}
	if uid.IsZero() {
		writeError(ErrAuthRequired("", "", now, now), types.ErrPermissionDenied)
		return
	}
	rawURL := req.URL.Query().Get("url")
	if rawURL == "" {
		writeError(ErrMalformed("", "", now), types.ErrMalformed)
		return
	}
	definition, accessTopic, err := store.AuthorizeFileMetadataContext(uid, rawURL)
	if err == nil {
		err = authorizeBusinessFileTopic(uid, accessTopic)
	}
	if err != nil {
		writeError(decodeStoreError(err, "", now, nil), err)
		return
	}
	if req.Method == http.MethodPost {
		if definition.User != uid.String() {
			writeError(ErrPermissionDenied("", "", now), types.ErrPermissionDenied)
			return
		}
		if globals.fileProcessor == nil {
			writeError(ErrOperationNotAllowed("", "", now), types.ErrUnsupported)
			return
		}
		queueFileProcessing(definition, rawURL)
		wrt.Header().Set("Content-Type", "application/json; charset=utf-8")
		wrt.WriteHeader(http.StatusAccepted)
		processing, _ := store.GetFileProcessingState(definition.Id)
		_ = json.NewEncoder(wrt).Encode(fileMetaResponse{
			Id: definition.Id, MimeType: definition.MimeType, Size: definition.Size,
			ETag: definition.ETag, Processing: processing,
		})
		return
	}
	processing, err := store.GetFileProcessingState(definition.Id)
	if err != nil && !errors.Is(err, types.ErrNotFound) {
		writeError(decodeStoreError(err, "", now, nil), err)
		return
	}
	wrt.Header().Set("Content-Type", "application/json; charset=utf-8")
	if req.Method == http.MethodHead {
		wrt.WriteHeader(http.StatusOK)
		return
	}
	_ = json.NewEncoder(wrt).Encode(fileMetaResponse{
		Id:         definition.Id,
		MimeType:   definition.MimeType,
		Size:       definition.Size,
		ETag:       definition.ETag,
		Processing: processing,
	})
}
