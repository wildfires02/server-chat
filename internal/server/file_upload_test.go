package server

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"chat/api/pbx"
	"chat/server/media"
	"chat/server/store/types"
)

type testFileUploadReceiver struct {
	requests []*pbx.FileUpReq
}

func (receiver *testFileUploadReceiver) Recv() (*pbx.FileUpReq, error) {
	if len(receiver.requests) == 0 {
		return nil, io.EOF
	}
	request := receiver.requests[0]
	receiver.requests = receiver.requests[1:]
	return request, nil
}

type testFilePersistence struct {
	started    int
	finished   []bool
	finishErr  error
	completion *types.FileDef
}

func (files *testFilePersistence) StartUpload(*types.FileDef) error {
	files.started++
	return nil
}

func (files *testFilePersistence) FinishUpload(
	definition *types.FileDef,
	success bool,
	size int64,
) (*types.FileDef, error) {
	files.finished = append(files.finished, success)
	if files.finishErr != nil {
		return nil, files.finishErr
	}
	definition.Size = size
	files.completion = definition
	return definition, nil
}

func (*testFilePersistence) Get(string) (*types.FileDef, error) {
	return nil, nil
}

func (*testFilePersistence) DeleteUnused(time.Time, int) error {
	return nil
}

func (*testFilePersistence) LinkAttachments(string, types.Uid, []string) error {
	return nil
}

type testMediaUploadHandler struct {
	files   *testFilePersistence
	data    []byte
	deleted []string
}

func (*testMediaUploadHandler) Init(string) error {
	return nil
}

func (*testMediaUploadHandler) Headers(
	string,
	*url.URL,
	http.Header,
	bool,
) (http.Header, int, error) {
	return nil, 0, nil
}

func (handler *testMediaUploadHandler) Upload(
	definition *types.FileDef,
	reader io.Reader,
) (string, int64, error) {
	if err := handler.files.StartUpload(definition); err != nil {
		return "", 0, err
	}
	data, err := io.ReadAll(reader)
	handler.data = data
	if err != nil {
		return "", 0, err
	}
	definition.Location = "stored/object"
	definition.ETag = "etag"
	return "https://example.test/file", int64(len(data)), nil
}

func (*testMediaUploadHandler) Download(string) (*types.FileDef, media.ReadSeekCloser, error) {
	return nil, nil, types.ErrNotFound
}

func (handler *testMediaUploadHandler) Delete(locations []string) error {
	handler.deleted = append(handler.deleted, locations...)
	return nil
}

func (*testMediaUploadHandler) GetIdFromUrl(string) types.Uid {
	return types.ZeroUid
}

func TestGRPCFileUploadReaderIncludesFirstFrame(t *testing.T) {
	reader := &grpcFileUploadReader{
		receiver: &testFileUploadReceiver{requests: []*pbx.FileUpReq{
			{},
			{Content: []byte("second")},
			{Content: []byte("-third")},
		}},
		pending: []byte("first-"),
	}

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "first-second-third" {
		t.Fatalf("组合后的上传内容 = %q", got)
	}
}

func TestUploadAndFinalizeFileSuccess(t *testing.T) {
	files := &testFilePersistence{}
	handler := &testMediaUploadHandler{files: files}
	definition := &types.FileDef{}

	location, completed, size, err := uploadAndFinalizeFile(
		handler,
		files,
		definition,
		strings.NewReader("payload"),
		64,
	)
	if err != nil {
		t.Fatal(err)
	}
	if location != "https://example.test/file" || size != 7 || completed != definition {
		t.Fatalf("上传结果 = %q, %d, %p", location, size, completed)
	}
	if files.started != 1 || len(files.finished) != 1 || !files.finished[0] {
		t.Fatalf("持久化调用 = started:%d finished:%v", files.started, files.finished)
	}
}

func TestUploadAndFinalizeFileRejectsOversizeStream(t *testing.T) {
	files := &testFilePersistence{}
	handler := &testMediaUploadHandler{files: files}

	_, _, _, err := uploadAndFinalizeFile(
		handler,
		files,
		&types.FileDef{},
		strings.NewReader("12345"),
		4,
	)
	if !errors.Is(err, errFileUploadTooLarge) {
		t.Fatalf("超限上传错误 = %v", err)
	}
	if string(handler.data) != "1234" {
		t.Fatalf("写入上限前的数据 = %q", handler.data)
	}
	if len(files.finished) != 1 || files.finished[0] {
		t.Fatalf("超限上传完成状态 = %v", files.finished)
	}
}

func TestUploadAndFinalizeFileCleansUpOnFinalizeFailure(t *testing.T) {
	files := &testFilePersistence{finishErr: errors.New("finish failed")}
	handler := &testMediaUploadHandler{files: files}

	_, _, _, err := uploadAndFinalizeFile(
		handler,
		files,
		&types.FileDef{},
		strings.NewReader("payload"),
		64,
	)
	if err == nil || err.Error() != "finish failed" {
		t.Fatalf("finalize 错误 = %v", err)
	}
	if len(handler.deleted) != 1 || handler.deleted[0] != "stored/object" {
		t.Fatalf("清理位置 = %v", handler.deleted)
	}
}

func TestDetectFileUploadMIMEValidatesDeclaredType(t *testing.T) {
	sample := []byte{0, 1, 2, 3, 4, 5}
	if got := detectFileUploadMIME(sample, "image/x-custom"); got != "image/x-custom" {
		t.Fatalf("允许的声明 MIME = %q", got)
	}
	if got := detectFileUploadMIME(sample, "model/x-custom"); got != "application/octet-stream" {
		t.Fatalf("禁止的声明 MIME = %q", got)
	}
}

func TestPrepareFileUploadReaderPreservesSniffedBytes(t *testing.T) {
	reader, mimeType, err := prepareFileUploadReader(strings.NewReader("short payload"), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(mimeType, "text/plain") {
		t.Fatalf("MIME 类型 = %q", mimeType)
	}

	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "short payload" {
		t.Fatalf("读取内容 = %q", content)
	}
}

func TestPrepareFileUploadReaderRejectsEmptyUpload(t *testing.T) {
	if _, _, err := prepareFileUploadReader(strings.NewReader(""), "text/plain"); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("空上传错误 = %v", err)
	}
}
