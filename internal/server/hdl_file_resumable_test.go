package server

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"chat/server/store"
	"chat/server/store/types"
)

type resumableTestCache struct {
	mu     sync.Mutex
	values map[string]string
}

type resumableTestChunkStore struct {
	data []byte
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
