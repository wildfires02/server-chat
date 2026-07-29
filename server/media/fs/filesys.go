// Package fs 实现基于本地文件系统单一目录存储媒体文件的 media 处理器。
package fs

import (
	"encoding/base32"
	"encoding/json"
	"errors"
	"hash/fnv"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"chat/server/logs"
	"chat/server/media"
	"chat/server/store"
	"chat/server/store/types"
)

const (
	// defaultServeURL 指定默认ServeURL。
	defaultServeURL = "/v0/file/s/"
	// defaultCacheControl 指定默认缓存Control。
	defaultCacheControl = "max-age=86400"

	// handlerName 指定处理器名称。
	handlerName = "fs"
)

// fileConfig 保存文件配置的数据和运行状态。
type fileConfig struct {
	// FileUploadDirectory: 在集群模式下，FileUploadDirectory 必须对集群所有节点均可访问。
	FileUploadDirectory string `json:"upload_dir"`
	// ServeURL 保存ServeURL。
	ServeURL string `json:"serve_url"`
	// CorsOrigins 保存CorsOrigins列表。
	CorsOrigins []string `json:"cors_origins"`
	// CacheControl 保存缓存Control。
	CacheControl string `json:"cache_control"`
}

// fshandler 保存fshandler的数据和运行状态。
type fshandler struct {
	// Embedded 嵌入公共状态或行为，供当前结构直接复用。
	fileConfig
	// corsOrigins 解析后的允许跨域源配置切片
	corsOrigins []media.AllowedOrigin
}

// Init 初始化本地文件系统媒体处理器。
func (fh *fshandler) Init(jsconf string) error {
	var err error

	if err = json.Unmarshal([]byte(jsconf), &fh.fileConfig); err != nil {
		return errors.New("解析配置失败: " + err.Error())
	}

	if fh.FileUploadDirectory == "" {
		return errors.New("缺少上传目录配置 (upload_dir)")
	}

	if fh.ServeURL == "" {
		fh.ServeURL = defaultServeURL
	}

	if fh.CacheControl == "" {
		fh.CacheControl = defaultCacheControl
	}

	fh.corsOrigins, err = media.ParseCORSAllow(fh.CorsOrigins)
	if err != nil {
		return errors.New("解析 CORS 允许源失败: " + err.Error())
	}
	// 确保上传目录存在
	return os.MkdirAll(fh.FileUploadDirectory, 0777)
}

// Headers 处理 HTTP 缓存控制以及 CORS 跨域请求头。
func (fh *fshandler) Headers(method string, url *url.URL, headers http.Header, serve bool) (http.Header, int, error) {
	if method == http.MethodGet {

		fid := fh.GetIdFromUrl(url.String())
		if fid.IsZero() {
			return nil, 0, types.ErrNotFound
		}

		fdef, err := fh.getFileRecord(fid)
		if err != nil {
			return nil, 0, err
		}

		if etag := strings.Trim(headers.Get("If-None-Match"), "\""); etag != "" && etag == fdef.ETag {
			return http.Header{
					"Last-Modified": {fdef.UpdatedAt.Format(http.TimeFormat)},
					"ETag":          {`"` + fdef.ETag + `"`},
					"Cache-Control": {fh.CacheControl},
				},
				http.StatusNotModified, nil
		}

		return http.Header{
			"Content-Type":  {fdef.MimeType},
			"Cache-Control": {fh.CacheControl},
			"ETag":          {`"` + fdef.ETag + `"`},
		}, 0, nil
	}

	if method != http.MethodOptions {
		// 非 OPTIONS 请求，无特殊处理直接返回
		return nil, 0, nil
	}
	header, status := media.CORSHandler(method, headers, fh.corsOrigins, serve)
	return header, status, nil
}

// Upload 处理文件上传请求，传入的 file 数据为 io.Reader。
func (fh *fshandler) Upload(fdef *types.FileDef, file io.Reader) (string, int64, error) {
	// 生成唯一文件名并拼接本地文件系统路径。使用 base32 避免 Windows 下文件名大小写不敏感导致的冲突。
	location := filepath.Join(fh.FileUploadDirectory, fdef.Uid().String32())

	outfile, err := os.Create(location)
	if err != nil {
		logs.Warn.Println("Upload: 创建本地文件失败", fdef.Location, err)
		return "", 0, err
	}

	if err = store.Files.StartUpload(fdef); err != nil {
		outfile.Close()
		os.Remove(location)
		logs.Warn.Println("创建文件记录失败", fdef.Id, err)
		return "", 0, err
	}

	size, err := io.Copy(outfile, file)
	outfile.Close()
	if err != nil {
		os.Remove(location)
		return "", 0, err
	}

	fname := fdef.Id
	ext, _ := mime.ExtensionsByType(fdef.MimeType)
	if len(ext) > 0 {
		fname += ext[0]
	}

	fdef.Location = location
	// 使用文件路径哈希作为 ETag
	fdef.ETag = etagFromPath(fdef.Location)

	return fh.ServeURL + fname, size, nil
}

// Download 处理文件下载请求。
// 返回的 ReadSeekCloser 在使用完成后必须调用 Close() 关闭。
func (fh *fshandler) Download(url string) (*types.FileDef, media.ReadSeekCloser, error) {
	fid := fh.GetIdFromUrl(url)
	if fid.IsZero() {
		return nil, nil, types.ErrNotFound
	}

	fd, err := fh.getFileRecord(fid)
	if err != nil {
		logs.Warn.Println("Download: 未找到文件记录", fid)
		return nil, nil, err
	}

	file, err := os.Open(fd.Location)
	if err != nil {
		if os.IsNotExist(err) {
			// 文件不存在时返回 404 而不是默认 500
			err = types.ErrNotFound
		}
		return nil, nil, err
	}

	return fd, file, nil
}

// Delete 从本地文件存储中批量删除指定位置的文件。
func (fh *fshandler) Delete(locations []string) error {
	for _, loc := range locations {
		if err, _ := os.Remove(loc).(*os.PathError); err != nil {
			if err != os.ErrNotExist {
				logs.Warn.Println("fs: 删除文件失败", loc, err)
			}
		}
	}
	return nil
}

// GetIdFromUrl 从附件下载 URL 中解析并提取文件 UID。
func (fh *fshandler) GetIdFromUrl(url string) types.Uid {
	return media.GetIdFromUrl(url, fh.ServeURL)
}

// getFileRecord 根据文件 UID 从数据库中获取对应的文件元数据记录。
func (fh *fshandler) getFileRecord(fid types.Uid) (*types.FileDef, error) {
	fd, err := store.Files.Get(fid.String())
	if err != nil {
		return nil, err
	}
	if fd == nil {
		return nil, types.ErrNotFound
	}
	return fd, nil
}

// etagFromPath 根据文件路径计算生成唯一的 ETag 校验码。
func etagFromPath(path string) string {
	hasher := fnv.New128()
	hasher.Write([]byte(path))
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).
		EncodeToString(hasher.Sum(make([]byte, 0, hasher.Size()))))
}

// init 注册当前包提供的实现并初始化包级状态。
func init() {
	store.RegisterMediaHandler(handlerName, &fshandler{})
}
