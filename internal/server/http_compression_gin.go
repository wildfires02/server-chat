package server

import (
	"compress/flate"
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

const acceptEncodingHeader = "Accept-Encoding"

// ginCompressedWriter 在保持 Gin ResponseWriter 的 Hijacker、Flusher 和
// Pusher 能力的同时，将正文写入协商出的压缩流。
type ginCompressedWriter struct {
	gin.ResponseWriter
	compressor io.Writer
}

func (writer *ginCompressedWriter) WriteHeader(status int) {
	writer.Header().Del("Content-Length")
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *ginCompressedWriter) Write(body []byte) (int, error) {
	header := writer.Header()
	if header.Get("Content-Type") == "" {
		header.Set("Content-Type", http.DetectContentType(body))
	}
	header.Del("Content-Length")
	return writer.compressor.Write(body)
}

func (writer *ginCompressedWriter) WriteString(body string) (int, error) {
	return writer.Write([]byte(body))
}

func (writer *ginCompressedWriter) ReadFrom(reader io.Reader) (int64, error) {
	return io.Copy(writer.compressor, reader)
}

func (writer *ginCompressedWriter) Flush() {
	if flusher, ok := writer.compressor.(interface{ Flush() error }); ok {
		_ = flusher.Flush()
	}
	writer.ResponseWriter.Flush()
}

// ginCompression 根据 Accept-Encoding 为普通 HTTP 响应启用 gzip 或
// deflate。WebSocket Upgrade 请求保持原始 ResponseWriter，不能压缩握手流。
func ginCompression() gin.HandlerFunc {
	return func(context *gin.Context) {
		context.Writer.Header().Add("Vary", acceptEncodingHeader)
		encoding := preferredCompression(context.GetHeader(acceptEncodingHeader))
		if encoding == "" || context.GetHeader("Upgrade") != "" {
			context.Next()
			return
		}

		var compressor io.WriteCloser
		switch encoding {
		case "gzip":
			compressor = gzip.NewWriter(context.Writer)
		case "deflate":
			compressor, _ = flate.NewWriter(context.Writer, flate.DefaultCompression)
		}
		if compressor == nil {
			context.Next()
			return
		}

		context.Header("Content-Encoding", encoding)
		context.Header("Content-Length", "")
		context.Request.Header.Del(acceptEncodingHeader)
		context.Writer = &ginCompressedWriter{
			ResponseWriter: context.Writer,
			compressor:     compressor,
		}
		defer compressor.Close()
		context.Next()
	}
}

// preferredCompression 按客户端顺序选择 gzip 或 deflate，并遵守 q=0。
func preferredCompression(header string) string {
	for _, item := range strings.Split(header, ",") {
		parts := strings.Split(strings.TrimSpace(item), ";")
		encoding := strings.ToLower(strings.TrimSpace(parts[0]))
		if encoding != "gzip" && encoding != "deflate" {
			continue
		}
		quality := 1.0
		for _, parameter := range parts[1:] {
			keyValue := strings.SplitN(strings.TrimSpace(parameter), "=", 2)
			if len(keyValue) == 2 && strings.EqualFold(keyValue[0], "q") {
				parsed, err := strconv.ParseFloat(keyValue[1], 64)
				if err != nil {
					quality = 0
				} else {
					quality = parsed
				}
			}
		}
		if quality > 0 {
			return encoding
		}
	}
	return ""
}
