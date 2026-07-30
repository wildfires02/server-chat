package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"chat/server/media"
	"chat/server/store/types"
)

// copyStoredMediaURL 从本地或共享对象存储读取媒体内容。S3 等处理器不直接暴露
// Download 时，通过处理器签发的受限重定向地址读取。
func copyStoredMediaURL(handler media.Handler, rawURL string, target io.Writer, timeout time.Duration) (int64, error) {
	_, reader, err := handler.Download(rawURL)
	if err == nil {
		defer reader.Close()
		return io.Copy(target, reader)
	}
	if !errors.Is(err, types.ErrUnsupported) {
		return 0, err
	}

	parsed, parseErr := url.Parse(rawURL)
	if parseErr != nil {
		return 0, parseErr
	}
	headers, status, headerErr := handler.Headers(http.MethodGet, parsed, http.Header{}, true)
	if headerErr != nil {
		return 0, headerErr
	}
	if status < 300 || status >= 400 || headers.Get("Location") == "" {
		return 0, types.ErrUnsupported
	}
	request, requestErr := http.NewRequest(http.MethodGet, headers.Get("Location"), nil)
	if requestErr != nil {
		return 0, requestErr
	}
	client := &http.Client{Timeout: timeout}
	response, requestErr := client.Do(request)
	if requestErr != nil {
		return 0, requestErr
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return 0, fmt.Errorf("object storage returned %s", response.Status)
	}
	return io.Copy(target, response.Body)
}
