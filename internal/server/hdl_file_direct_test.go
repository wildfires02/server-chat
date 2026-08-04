package server

import (
	"context"
	"strings"
	"testing"

	"chat/server/media"
	"chat/server/store/types"
)

func TestDirectUploadObjectKeyUsesChatTypeAndFileName(t *testing.T) {
	key := directUploadObjectKey(
		"file123",
		"短信接口文档 HTTP API_v3.5 CN.pdf",
		"application/pdf",
	)
	if !strings.HasPrefix(key, "chat/documents/file123/") {
		t.Fatalf("unexpected direct upload prefix: %q", key)
	}
	if !strings.HasSuffix(key, "短信接口文档_HTTP_API_v3.5_CN.pdf") {
		t.Fatalf("unexpected direct upload filename: %q", key)
	}
}

func TestDirectUploadObjectKeyRejectsPathTraversal(t *testing.T) {
	key := directUploadObjectKey("file456", "../../secret?.jpg", "image/jpeg")
	if key != "chat/images/file456/secret_.jpg" {
		t.Fatalf("unexpected sanitized direct upload key: %q", key)
	}
}

type directPartsTestHandler struct {
	resumableTestMultipartHandler
	parts []media.MultipartPart
	err   error
}

func (handler *directPartsTestHandler) ListMultipartParts(
	context.Context,
	*types.FileDef,
	string,
) ([]media.MultipartPart, error) {
	return append([]media.MultipartPart(nil), handler.parts...), handler.err
}

func TestResolveDirectUploadPartsUsesObjectStorageETags(t *testing.T) {
	handler := &directPartsTestHandler{parts: []media.MultipartPart{
		{PartNumber: 2, ETag: `"etag-two"`},
		{PartNumber: 1, ETag: `"etag-one"`},
	}}
	state := &directUploadState{
		PartCount: 2,
		UploadID:  "upload-test",
		Location:  "object-test",
	}
	parts, err := resolveDirectUploadParts(
		context.Background(),
		handler,
		directUploadDefinition(state),
		state,
		[]media.MultipartPart{{PartNumber: 1}, {PartNumber: 2}},
	)
	if err != nil {
		t.Fatalf("resolve direct upload parts: %v", err)
	}
	if len(parts) != 2 || parts[0].PartNumber != 1 || parts[0].ETag != `"etag-one"` ||
		parts[1].PartNumber != 2 || parts[1].ETag != `"etag-two"` {
		t.Fatalf("unexpected object-storage parts: %#v", parts)
	}
}

func TestResolveDirectUploadPartsRejectsMissingObjectStoragePart(t *testing.T) {
	handler := &directPartsTestHandler{parts: []media.MultipartPart{
		{PartNumber: 1, ETag: `"etag-one"`},
	}}
	state := &directUploadState{
		PartCount: 2,
		UploadID:  "upload-test",
		Location:  "object-test",
	}
	if _, err := resolveDirectUploadParts(
		context.Background(),
		handler,
		directUploadDefinition(state),
		state,
		[]media.MultipartPart{{PartNumber: 1}, {PartNumber: 2}},
	); err == nil {
		t.Fatal("expected a missing object-storage part to be rejected")
	}
}
