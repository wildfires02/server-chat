package server

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"chat/server/media"
	"chat/server/store"
	"chat/server/store/types"
)

const resumableStorageTimeout = 2 * time.Minute

// resumableUploadChunk 随上传会话一起持久化。
// Location 是清理时使用的存储服务原生对象键，URL 用于跨节点读取。
type resumableUploadChunk struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	Location string `json:"location"`
	Size     int64  `json:"size"`
}

type resumableChunkStore interface {
	Put(owner string, source io.Reader, limit int64) (resumableUploadChunk, error)
	Open(chunks []resumableUploadChunk) (io.ReadCloser, error)
	Delete(chunks []resumableUploadChunk) error
}

var resumableChunks resumableChunkStore = mediaResumableChunkStore{}

type mediaResumableChunkStore struct{}

func (mediaResumableChunkStore) Put(owner string, source io.Reader, limit int64) (resumableUploadChunk, error) {
	handler := store.Store.GetMediaHandler()
	if handler == nil || store.Files == nil || limit <= 0 {
		return resumableUploadChunk{}, types.ErrMalformed
	}
	definition := &types.FileDef{
		ObjHeader: types.ObjHeader{Id: store.Store.GetUidString()},
		User:      owner,
		MimeType:  "application/octet-stream",
	}
	definition.InitTimes()
	reader := &limitedFileUploadReader{reader: source, remaining: limit}
	rawURL, size, err := handler.Upload(definition, reader)
	if err != nil {
		cleanupResumableChunk(handler, definition)
		return resumableUploadChunk{}, err
	}
	if size <= 0 || size > limit {
		cleanupResumableChunk(handler, definition)
		if size > limit {
			return resumableUploadChunk{}, errFileUploadTooLarge
		}
		return resumableUploadChunk{}, io.ErrUnexpectedEOF
	}
	if _, err = store.Files.FinishUpload(definition, true, size); err != nil {
		cleanupResumableChunk(handler, definition)
		return resumableUploadChunk{}, err
	}
	_ = store.SetFileProcessingState(definition.Id, store.FileProcessingState{
		ScanStatus: "skipped", ProcessStatus: "resumable",
	})
	return resumableUploadChunk{
		ID: definition.Id, URL: rawURL, Location: definition.Location, Size: size,
	}, nil
}

func (mediaResumableChunkStore) Open(chunks []resumableUploadChunk) (io.ReadCloser, error) {
	if len(chunks) == 0 {
		return nil, io.ErrUnexpectedEOF
	}
	handler := store.Store.GetMediaHandler()
	if handler == nil {
		return nil, types.ErrMalformed
	}
	file, err := os.CreateTemp("", "chat-resumable-assembly-*")
	if err != nil {
		return nil, err
	}
	if err = file.Chmod(0o600); err != nil {
		file.Close()
		os.Remove(file.Name())
		return nil, err
	}
	for _, chunk := range chunks {
		if chunk.URL == "" || chunk.Size <= 0 {
			file.Close()
			os.Remove(file.Name())
			return nil, types.ErrMalformed
		}
		var copied int64
		copied, err = copyStoredMediaURL(handler, chunk.URL, file, resumableStorageTimeout)
		if err != nil || copied != chunk.Size {
			file.Close()
			os.Remove(file.Name())
			if err == nil {
				err = fmt.Errorf("resumable chunk size mismatch: expected %d, got %d", chunk.Size, copied)
			}
			return nil, err
		}
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		os.Remove(file.Name())
		return nil, err
	}
	return &removeOnCloseFile{File: file, path: file.Name()}, nil
}

func (mediaResumableChunkStore) Delete(chunks []resumableUploadChunk) error {
	handler := store.Store.GetMediaHandler()
	if handler == nil {
		return types.ErrMalformed
	}
	locations := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		if chunk.Location != "" {
			locations = append(locations, chunk.Location)
		}
	}
	var result error
	if len(locations) > 0 {
		result = handler.Delete(locations)
	}
	for _, chunk := range chunks {
		if types.ParseUid(chunk.ID).IsZero() {
			continue
		}
		definition := &types.FileDef{ObjHeader: types.ObjHeader{Id: chunk.ID}, Location: chunk.Location}
		if _, err := store.Files.FinishUpload(definition, false, 0); err != nil {
			result = errors.Join(result, err)
		}
		if err := store.DeleteFileProcessingState(chunk.ID); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func cleanupResumableChunk(handler media.Handler, definition *types.FileDef) {
	if definition == nil {
		return
	}
	if definition.Location != "" {
		_ = handler.Delete([]string{definition.Location})
	}
	if store.Files != nil {
		_, _ = store.Files.FinishUpload(definition, false, 0)
	}
}

type removeOnCloseFile struct {
	*os.File
	path string
}

func (file *removeOnCloseFile) Close() error {
	closeErr := file.File.Close()
	removeErr := os.Remove(file.path)
	return errors.Join(closeErr, removeErr)
}
