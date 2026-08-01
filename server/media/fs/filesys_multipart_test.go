package fs

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"chat/server/media"
	"chat/server/store/types"
)

func TestStreamingMultipartWritesDirectlyIntoFinalTemporaryFile(t *testing.T) {
	uploadDir := t.TempDir()
	handler := &fshandler{fileConfig: fileConfig{
		FileUploadDirectory: uploadDir,
		ServeURL:            defaultServeURL,
	}}
	definition := &types.FileDef{
		ObjHeader: types.ObjHeader{Id: types.Uid(123).String()},
		MimeType:  "application/octet-stream",
	}
	definition.InitTimes()

	uploadID, err := handler.CreateMultipartUpload(context.Background(), definition)
	if err != nil {
		t.Fatal(err)
	}
	first, err := handler.UploadMultipartPart(
		context.Background(), definition, uploadID, 1, 0,
		bytes.NewReader([]byte("hello ")), 6,
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := handler.UploadMultipartPart(
		context.Background(), definition, uploadID, 2, 6,
		bytes.NewReader([]byte("world")), 5,
	)
	if err != nil {
		t.Fatal(err)
	}

	_, size, err := handler.CompleteMultipartUpload(
		context.Background(), definition, uploadID, []media.MultipartPart{first, second},
	)
	if err != nil {
		t.Fatal(err)
	}
	if size != 11 {
		t.Fatalf("completed size=%d, want 11", size)
	}
	content, err := os.ReadFile(definition.Location)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "hello world" {
		t.Fatalf("completed content=%q", content)
	}
	if _, err = os.Stat(filepath.Join(uploadDir, uploadID)); !os.IsNotExist(err) {
		t.Fatalf("multipart temporary file still exists: %v", err)
	}
}
