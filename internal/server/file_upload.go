package server

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"chat/api/pbx"
	"chat/server/media"
	"chat/server/store"
	"chat/server/store/types"
)

const uploadMIMESniffSize = 512

var errFileUploadTooLarge = errors.New("file upload exceeds configured size limit")

// fileUploadReceiver 是 gRPC 上传流读取端的最小接口，便于独立验证分块读取。
type fileUploadReceiver interface {
	Recv() (*pbx.FileUpReq, error)
}

// grpcFileUploadReader 把首帧和后续 gRPC 数据帧组合成连续的 io.Reader。
type grpcFileUploadReader struct {
	receiver fileUploadReceiver
	pending  []byte
}

func (reader *grpcFileUploadReader) Read(buffer []byte) (int, error) {
	for len(reader.pending) == 0 {
		request, err := reader.receiver.Recv()
		if err != nil {
			return 0, err
		}
		reader.pending = request.GetContent()
	}

	read := copy(buffer, reader.pending)
	reader.pending = reader.pending[read:]
	return read, nil
}

// limitedFileUploadReader 在不信任客户端声明大小的情况下强制执行实际字节上限。
type limitedFileUploadReader struct {
	reader    io.Reader
	remaining int64
}

func (reader *limitedFileUploadReader) Read(buffer []byte) (int, error) {
	if reader.remaining == 0 {
		var probe [1]byte
		read, err := reader.reader.Read(probe[:])
		if read > 0 {
			return 0, errFileUploadTooLarge
		}
		return 0, err
	}
	if int64(len(buffer)) > reader.remaining {
		buffer = buffer[:reader.remaining]
	}
	read, err := reader.reader.Read(buffer)
	reader.remaining -= int64(read)
	return read, err
}

// prepareFileUploadReader 在不消耗数据的前提下读取 MIME 样本，并返回可继续完整读取的流。
func prepareFileUploadReader(reader io.Reader, declaredMIME string) (io.Reader, string, error) {
	buffered := bufio.NewReaderSize(reader, uploadMIMESniffSize)
	sample, err := buffered.Peek(uploadMIMESniffSize)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return nil, "", err
	}
	if len(sample) == 0 {
		return nil, "", io.ErrUnexpectedEOF
	}
	return buffered, detectFileUploadMIME(sample, declaredMIME), nil
}

func detectFileUploadMIME(sample []byte, declared string) string {
	detected := http.DetectContentType(sample)
	if detected != "application/octet-stream" {
		return detected
	}

	userContentType, params, err := mime.ParseMediaType(declared)
	if err != nil {
		return detected
	}
	for _, allowed := range allowedMimeTypes {
		if strings.HasPrefix(userContentType, allowed) {
			if formatted := mime.FormatMediaType(userContentType, params); formatted != "" {
				return formatted
			}
			break
		}
	}
	return detected
}

// uploadAndFinalizeFile 保证每次已开始的上传最终只留下成功或失败状态。
func uploadAndFinalizeFile(
	handler media.Handler,
	files store.FilePersistenceInterface,
	definition *types.FileDef,
	reader io.Reader,
	maxSize int64,
) (string, *types.FileDef, int64, error) {
	if maxSize > 0 {
		reader = &limitedFileUploadReader{reader: reader, remaining: maxSize}
	}
	hasher := sha256.New()
	reader = io.TeeReader(reader, hasher)

	location, size, err := handler.Upload(definition, reader)
	if err != nil {
		_, _ = files.FinishUpload(definition, false, 0)
		return "", definition, 0, err
	}

	completed, err := files.FinishUpload(definition, true, size)
	if err != nil {
		_ = handler.Delete([]string{definition.Location})
		return "", definition, 0, err
	}
	if completed == nil {
		completed = definition
	}
	if definition.Id != "" {
		// 摘要由服务端在实际读取上传流时计算，不能信任客户端上报值。
		_ = store.SetFileProcessingState(definition.Id, store.FileProcessingState{
			SHA256:        hex.EncodeToString(hasher.Sum(nil)),
			ScanStatus:    "disabled",
			ProcessStatus: "ready",
		})
	}
	return location, completed, size, nil
}
