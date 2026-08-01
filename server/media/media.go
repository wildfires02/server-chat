// Package media 定义了媒体文件上传与下载处理核心接口、跨域 (CORS) 校验逻辑及通用处理函数。
package media

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"

	"slices"

	"chat/server/store/types"
)

// ReadSeekCloser 正在被下载的媒体流对象必须实现的读、寻址与关闭接口组合。
type ReadSeekCloser interface {
	// Embedded 嵌入公共状态或行为，供当前结构直接复用。
	io.Reader
	// Embedded 嵌入公共状态或行为，供当前结构直接复用。
	io.Seeker
	// Embedded 嵌入公共状态或行为，供当前结构直接复用。
	io.Closer
}

// Handler 所有媒体文件处理器（上传/下载处理器，如本地文件系统、S3 等）必须实现的接口。
type Handler interface {
	// Init 初始化媒体上传处理器
	Init(jsconf string) error

	// Headers 检查处理器是否需要为 HTTP 请求提供额外的 Header（如 CORS 响应头、重定向到其它 URL 或缓存控制头）。
	// 返回 Header Map、用于终止请求处理的 HTTP 状态码（0 表示继续处理）及错误信息。
	Headers(method string, url *url.URL, headers http.Header, serve bool) (http.Header, int, error)

	// Upload 处理文件上传请求，返回上传后的文件 URL、文件字节大小及错误信息。
	Upload(fdef *types.FileDef, file io.Reader) (string, int64, error)

	// Download 处理文件下载请求。
	Download(url string) (*types.FileDef, ReadSeekCloser, error)

	// Delete 从存储介质中批量删除文件。
	Delete(locations []string) error

	// GetIdFromUrl 从下载 URL 中解析并提取文件 UID。
	GetIdFromUrl(url string) types.Uid
}

// MultipartPart 是完成对象存储 Multipart Upload 所需的已上传分块标识。
type MultipartPart struct {
	PartNumber int    `json:"part_number"`
	ETag       string `json:"etag"`
}

// PresignedPart 描述浏览器可直接 PUT 的对象存储分块地址。
type PresignedPart struct {
	PartNumber int               `json:"part_number"`
	URL        string            `json:"url"`
	Headers    map[string]string `json:"headers,omitempty"`
}

// MultipartHandler 是支持浏览器直传对象存储的可选媒体处理器能力。
type MultipartHandler interface {
	CreateMultipartUpload(context.Context, *types.FileDef) (string, error)
	PresignMultipartPart(context.Context, *types.FileDef, string, int) (*PresignedPart, error)
	CompleteMultipartUpload(context.Context, *types.FileDef, string, []MultipartPart) (string, int64, error)
	AbortMultipartUpload(context.Context, *types.FileDef, string) error
}

// StreamingMultipartHandler lets the application server stream a tus chunk directly
// into one object-storage multipart part without persisting and re-reading it.
type StreamingMultipartHandler interface {
	MultipartHandler
	UploadMultipartPart(context.Context, *types.FileDef, string, int, int64, io.Reader, int64) (MultipartPart, error)
}

// DirectUploadCapability controls whether presigned browser uploads are exposed.
// Server-side streaming multipart may remain available when this is false.
type DirectUploadCapability interface {
	DirectUploadEnabled() bool
}

// AllowedOrigin 存储解析后的允许跨域源地址配置结构。
type AllowedOrigin struct {
	// Origin 保存Origin。
	Origin string
	// URL 保存URL。
	URL url.URL
	// HostParts 保存HostParts列表。
	HostParts []string
	// HasWildcard 指示是否启用或满足HasWildcard。
	HasWildcard bool
}

// fileNamePattern 保存文件名称Pattern的共享实例或运行状态。
var fileNamePattern = regexp.MustCompile(`^[-_A-Za-z0-9]+`)

// GetIdFromUrl 从 URL 中提取文件 UID 的辅助函数。
func GetIdFromUrl(url, serveUrl string) types.Uid {
	dir, fname := path.Split(path.Clean(url))

	if dir != "" && dir != serveUrl {
		return types.ZeroUid
	}

	return types.ParseUid(fileNamePattern.FindString(fname))
}

// ParseCORSAllow 预解析配置中允许的 CORS 跨域源地址列表。
func ParseCORSAllow(allowed []string) ([]AllowedOrigin, error) {
	if len(allowed) == 0 {
		return nil, nil
	}

	result := make([]AllowedOrigin, 0, len(allowed))
	for _, val := range allowed {
		parsed := AllowedOrigin{Origin: val}
		switch val {
		case "*":
			if len(allowed) > 1 {
				return nil, errors.New("wildcard origin '*' must be the only entry")
			}
			parsed.HasWildcard = true
		case "":
			if len(allowed) > 1 {
				return nil, errors.New("empty allowed origin '' must be the only entry")
			}
			// 空字符串表示不允许任何源跨域 - 无需解析 URL
			parsed.HasWildcard = false
		default:
			u, err := url.ParseRequestURI(val)
			if err != nil {
				return nil, err
			}
			parsed.HostParts = strings.Split(u.Hostname(), ".")
			parsed.URL = *u
			parsed.HasWildcard = strings.Contains(u.Hostname(), "*")
		}
		result = append(result, parsed)
	}
	return result, nil
}

// matchCORSOrigin 将 HTTP 请求头中的 Origin 与允许的源列表进行匹配校验。
func matchCORSOrigin(allowed []AllowedOrigin, origin string) string {
	if len(allowed) == 0 {
		// 未配置允许的跨域源
		return ""
	}

	if origin == "" && allowed[0].Origin != "*" {
		// 请求无 Origin 标头，且未配置允许任意源 "*"
		return ""
	}

	if allowed[0].Origin == "*" {
		if origin == "" {
			return "*"
		}
		return origin
	}

	// 检查允许源列表中的空字符串 - 表示不允许任何 Origin 跨域
	if allowed[0].Origin == "" {
		return ""
	}

	originUrl, err := url.ParseRequestURI(origin)
	if err != nil {
		return ""
	}
	originParts := strings.Split(originUrl.Hostname(), ".")

	for _, val := range allowed {
		if val.Origin == origin {
			return origin
		}

		if !val.HasWildcard ||
			originUrl.Scheme != val.URL.Scheme ||
			originUrl.Port() != val.URL.Port() ||
			len(originParts) != len(val.HostParts) {
			continue
		}

		matched := true
		for i, part := range val.HostParts {
			if part == "*" {
				continue
			}
			if part != originParts[i] {
				matched = false
				break
			}
		}
		if matched {
			return origin
		}
	}

	return ""
}

// matchCORSMethod 检查请求方法是否在允许的 HTTP 方法列表中（allowMethods 必须全大写）。
func matchCORSMethod(allowMethods []string, method string) bool {
	if method == "" {
		// 请求缺少 Method 标头
		return false
	}

	return slices.Contains(allowMethods, strings.ToUpper(method))
}

// CORSHandler 供媒体处理器使用的默认 CORS 跨域处理函数。
// 为 OPTIONS 预检请求添加跨域标头，并为所有响应添加 Vary 及 Access-Control-Allow-Origin 标头。
func CORSHandler(method string, reqHeader http.Header, allowedOrigins []AllowedOrigin, serve bool) (http.Header, int) {
	respHeader := map[string][]string{
		// 始终添加 Vary 标头以处理中间缓存
		"Vary": {"Origin", "Access-Control-Request-Method, Access-Control-Request-Headers"},
	}

	origin := reqHeader.Get("Origin")

	allowedOrigin := matchCORSOrigin(allowedOrigins, origin)
	if acMethod := reqHeader.Get("Access-Control-Request-Method"); method == http.MethodOptions && acMethod != "" {
		// OPTIONS 预检请求

		if allowedOrigin == "" {
			return respHeader, http.StatusNoContent
		}

		var allowMethods []string
		if serve {
			allowMethods = []string{http.MethodGet, http.MethodHead, http.MethodOptions}
		} else {
			allowMethods = []string{http.MethodPost, http.MethodPut, http.MethodHead, http.MethodOptions}
		}

		if !matchCORSMethod(allowMethods, acMethod) {
			// CORS 策略不允许该 HTTP 方法
			return respHeader, http.StatusNoContent
		}

		respHeader["Access-Control-Allow-Headers"] = []string{"*"}
		respHeader["Access-Control-Allow-Credentials"] = []string{"true"}
		respHeader["Access-Control-Allow-Methods"] = []string{strings.Join(allowMethods, ", ")}
		respHeader["Access-Control-Max-Age"] = []string{"86400"}
		respHeader["Access-Control-Allow-Origin"] = []string{allowedOrigin}

		return respHeader, http.StatusNoContent
	}

	// 普通请求（非 OPTIONS 预检请求）

	if allowedOrigin != "" {
		// 返回实际请求的 Origin 而非 '*'，防止携带凭据时出现跨域错误
		respHeader["Access-Control-Allow-Origin"] = []string{origin}
	}

	return respHeader, 0
}
