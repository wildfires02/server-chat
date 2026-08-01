package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"chat/server/media"
	"chat/server/store"
	mockstore "chat/server/store/mock_store"
	"chat/server/store/types"
	"go.uber.org/mock/gomock"
)

type resumableTestCache struct {
	mu     sync.Mutex
	values map[string]string
}

type resumableTestChunkStore struct {
	data []byte
}

type resumableTestMultipartHandler struct {
	data []byte
}

func (*resumableTestMultipartHandler) Init(string) error { return nil }
func (*resumableTestMultipartHandler) Headers(string, *url.URL, http.Header, bool) (http.Header, int, error) {
	return nil, 0, nil
}
func (*resumableTestMultipartHandler) Upload(*types.FileDef, io.Reader) (string, int64, error) {
	return "", 0, types.ErrUnsupported
}
func (*resumableTestMultipartHandler) Download(string) (*types.FileDef, media.ReadSeekCloser, error) {
	return nil, nil, types.ErrUnsupported
}
func (*resumableTestMultipartHandler) Delete([]string) error         { return nil }
func (*resumableTestMultipartHandler) GetIdFromUrl(string) types.Uid { return types.ZeroUid }
func (*resumableTestMultipartHandler) CreateMultipartUpload(context.Context, *types.FileDef) (string, error) {
	return "upload-test", nil
}
func (*resumableTestMultipartHandler) PresignMultipartPart(context.Context, *types.FileDef, string, int) (*media.PresignedPart, error) {
	return nil, types.ErrUnsupported
}
func (handler *resumableTestMultipartHandler) UploadMultipartPart(_ context.Context, _ *types.FileDef, _ string, number int, _ int64, body io.Reader, size int64) (media.MultipartPart, error) {
	data, err := io.ReadAll(body)
	if err != nil || int64(len(data)) != size {
		return media.MultipartPart{}, io.ErrUnexpectedEOF
	}
	handler.data = append(handler.data, data...)
	return media.MultipartPart{PartNumber: number, ETag: "etag-test"}, nil
}
func (*resumableTestMultipartHandler) CompleteMultipartUpload(context.Context, *types.FileDef, string, []media.MultipartPart) (string, int64, error) {
	return "", 0, types.ErrUnsupported
}
func (*resumableTestMultipartHandler) AbortMultipartUpload(context.Context, *types.FileDef, string) error {
	return nil
}

func (storage *resumableTestChunkStore) Put(_ string, source io.Reader,
	limit int64) (resumableUploadChunk, error) {
	data, err := io.ReadAll(io.LimitReader(source, limit+1))
	if err != nil {
		return resumableUploadChunk{}, err
	}
	if int64(len(data)) > limit {
		return resumableUploadChunk{}, errFileUploadTooLarge
	}
	storage.data = append(storage.data, data...)
	return resumableUploadChunk{
		ID: "chunk-test", URL: "memory://chunk-test",
		Location: "chunk-test", Size: int64(len(data)),
	}, nil
}

func (storage *resumableTestChunkStore) Open(
	_ []resumableUploadChunk,
) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(string(storage.data))), nil
}

func (storage *resumableTestChunkStore) Delete(_ []resumableUploadChunk) error {
	return nil
}

func (cache *resumableTestCache) Get(key string) (string, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	value, ok := cache.values[key]
	if !ok {
		return "", types.ErrNotFound
	}
	return value, nil
}
func (cache *resumableTestCache) Upsert(key, value string, failOnDuplicate bool) error {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if _, exists := cache.values[key]; exists && failOnDuplicate {
		return types.ErrDuplicate
	}
	cache.values[key] = value
	return nil
}
func (cache *resumableTestCache) Delete(key string) error {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	delete(cache.values, key)
	return nil
}
func (*resumableTestCache) Expire(string, time.Time) error { return nil }

func (cache *resumableTestCache) List(prefix string, limit int) (map[string]string, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	result := make(map[string]string)
	for key, value := range cache.values {
		if strings.HasPrefix(key, prefix) && len(result) < limit {
			result[key] = value
		}
	}
	return result, nil
}

func (cache *resumableTestCache) CompareAndSwap(key, oldValue, newValue string) (bool, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.values[key] != oldValue {
		return false, nil
	}
	cache.values[key] = newValue
	return true, nil
}

func TestAppendResumableChunkAndOffsetConflict(t *testing.T) {
	previous := store.PCache
	store.PCache = &resumableTestCache{values: make(map[string]string)}
	t.Cleanup(func() { store.PCache = previous })

	previousChunks := resumableChunks
	chunks := &resumableTestChunkStore{}
	resumableChunks = chunks
	t.Cleanup(func() { resumableChunks = previousChunks })

	state := &resumableUploadState{
		Id:        types.Uid(900).String(),
		Owner:     types.Uid(10).String(),
		Length:    10,
		CreatedAt: time.Now(),
	}
	if err := saveResumableUpload(state); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPatch, "/file/resumable/"+state.Id, strings.NewReader("abc"))
	request.Header.Set("Upload-Offset", "0")
	response := httptest.NewRecorder()
	appendResumableChunk(response, request, state, time.Now())
	if response.Code != http.StatusNoContent || state.Offset != 3 ||
		response.Header().Get("Upload-Offset") != "3" {
		t.Fatalf("partial upload response=%d offset=%d headers=%v",
			response.Code, state.Offset, response.Header())
	}
	if string(chunks.data) != "abc" {
		t.Fatalf("分块内容=%q，期望 abc", chunks.data)
	}

	conflict := httptest.NewRequest(http.MethodPatch, "/file/resumable/"+state.Id, strings.NewReader("x"))
	conflict.Header.Set("Upload-Offset", "1")
	conflictResponse := httptest.NewRecorder()
	appendResumableChunk(conflictResponse, conflict, state, time.Now())
	if conflictResponse.Code != http.StatusConflict ||
		conflictResponse.Header().Get("Upload-Offset") != "3" {
		t.Fatalf("offset conflict response=%d headers=%v",
			conflictResponse.Code, conflictResponse.Header())
	}
}

func TestResumableUploadContinuesFromAnotherNode(t *testing.T) {
	previous := store.PCache
	store.PCache = &resumableTestCache{values: make(map[string]string)}
	t.Cleanup(func() { store.PCache = previous })
	previousChunks := resumableChunks
	chunks := &resumableTestChunkStore{}
	resumableChunks = chunks
	t.Cleanup(func() { resumableChunks = previousChunks })

	state := &resumableUploadState{
		Id: types.Uid(901).String(), Owner: types.Uid(11).String(),
		Length: 9, CreatedAt: time.Now(),
	}
	if err := saveResumableUpload(state); err != nil {
		t.Fatal(err)
	}
	first := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader("abc"))
	first.Header.Set("Upload-Offset", "0")
	firstResponse := httptest.NewRecorder()
	appendResumableChunk(firstResponse, first, state, time.Now())
	if firstResponse.Code != http.StatusNoContent {
		t.Fatalf("first node response=%d", firstResponse.Code)
	}

	// 第二个节点只依赖共享元数据重建会话，并追加一个共享媒体分片。
	recovered, err := loadResumableUpload(state.Id)
	if err != nil {
		t.Fatal(err)
	}
	second := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader("def"))
	second.Header.Set("Upload-Offset", "3")
	secondResponse := httptest.NewRecorder()
	appendResumableChunk(secondResponse, second, recovered, time.Now())
	if secondResponse.Code != http.StatusNoContent ||
		secondResponse.Header().Get("Upload-Offset") != "6" {
		t.Fatalf("second node response=%d headers=%v", secondResponse.Code, secondResponse.Header())
	}
	reloaded, err := loadResumableUpload(state.Id)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Offset != 6 || len(reloaded.Chunks) != 2 || string(chunks.data) != "abcdef" {
		t.Fatalf("recovered state offset=%d chunks=%d data=%q",
			reloaded.Offset, len(reloaded.Chunks), chunks.data)
	}
}

func TestResumableUploadLeaseIsExclusiveAndRecoverable(t *testing.T) {
	previous := store.PCache
	store.PCache = &resumableTestCache{values: make(map[string]string)}
	t.Cleanup(func() { store.PCache = previous })
	id := types.Uid(902).String()

	first, err := acquireResumableUploadLease(id, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = acquireResumableUploadLease(id, time.Minute); !errors.Is(err, errResumableLeaseBusy) {
		t.Fatalf("second lease error=%v", err)
	}
	releaseResumableUploadLease(id, first)
	if _, err = acquireResumableUploadLease(id, time.Minute); err != nil {
		t.Fatalf("reacquire released lease: %v", err)
	}
}

func TestResumableMultipartStreamsPatchWithoutChunkStore(t *testing.T) {
	previousCache := store.PCache
	store.PCache = &resumableTestCache{values: make(map[string]string)}
	t.Cleanup(func() { store.PCache = previousCache })
	previousStore := store.Store
	controller := gomock.NewController(t)
	handler := &resumableTestMultipartHandler{}
	mockStorage := mockstore.NewMockPersistentStorageInterface(controller)
	mockStorage.EXPECT().GetMediaHandler().Return(handler).AnyTimes()
	store.Store = mockStorage
	t.Cleanup(func() { store.Store = previousStore })
	previousChunks := resumableChunks
	chunks := &resumableTestChunkStore{}
	resumableChunks = chunks
	t.Cleanup(func() { resumableChunks = previousChunks })

	state := &resumableUploadState{
		Id: types.Uid(903).String(), Owner: types.Uid(12).String(), MimeType: "application/octet-stream",
		Length: 6, CreatedAt: time.Now(), MultipartUploadID: "upload-test",
		MultipartLocation: "object-test", MultipartPartSize: 3,
	}
	if err := saveResumableUpload(state); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader("abc"))
	response := httptest.NewRecorder()
	appendResumableMultipartPart(response, request, state, time.Now())
	if response.Code != http.StatusNoContent || state.Offset != 3 || len(state.MultipartParts) != 1 {
		t.Fatalf("streaming response=%d offset=%d parts=%v", response.Code, state.Offset, state.MultipartParts)
	}
	if string(handler.data) != "abc" || len(chunks.data) != 0 {
		t.Fatalf("multipart data=%q legacy chunk data=%q", handler.data, chunks.data)
	}
}
