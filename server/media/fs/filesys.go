// Package fs 实现基于本地文件系统单一目录存储媒体文件的 media 处理器。
package fs

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/fnv"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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

var _ media.StreamingMultipartHandler = (*fshandler)(nil)
var _ media.DirectUploadCapability = (*fshandler)(nil)
var _ media.QuarantineHandler = (*fshandler)(nil)

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

	fdef.Location = location
	// 使用文件路径哈希作为 ETag
	fdef.ETag = etagFromPath(fdef.Location)

	return fh.fileURL(fdef), size, nil
}

func (fh *fshandler) fileURL(fdef *types.FileDef) string {
	fname := fdef.Id
	ext, _ := mime.ExtensionsByType(fdef.MimeType)
	if len(ext) > 0 {
		fname += ext[0]
	}
	return fh.ServeURL + fname
}

func (fh *fshandler) multipartPath(uploadID string) (string, error) {
	if uploadID == "" || filepath.Base(uploadID) != uploadID ||
		!strings.HasPrefix(uploadID, ".multipart-") {
		return "", types.ErrMalformed
	}
	return filepath.Join(fh.FileUploadDirectory, uploadID), nil
}

// CreateMultipartUpload creates one sparse temporary file. Every tus PATCH is
// written directly at its confirmed offset, so completion only needs an atomic rename.
func (fh *fshandler) CreateMultipartUpload(_ context.Context, fdef *types.FileDef) (string, error) {
	if fdef == nil || fdef.Uid().IsZero() {
		return "", types.ErrMalformed
	}
	temporary, err := os.CreateTemp(
		fh.FileUploadDirectory,
		".multipart-"+fdef.Uid().String32()+"-",
	)
	if err != nil {
		return "", err
	}
	uploadID := filepath.Base(temporary.Name())
	if err = temporary.Close(); err != nil {
		_ = os.Remove(temporary.Name())
		return "", err
	}
	fdef.Location = filepath.Join(fh.FileUploadDirectory, fdef.Uid().String32())
	return uploadID, nil
}

// PresignMultipartPart is intentionally unsupported for local storage: the
// browser cannot address the server filesystem directly.
func (*fshandler) PresignMultipartPart(
	context.Context,
	*types.FileDef,
	string,
	int,
) (*media.PresignedPart, error) {
	return nil, types.ErrUnsupported
}

// DirectUploadEnabled prevents the direct-upload endpoint from exposing the
// local filesystem while still allowing server-streamed tus multipart writes.
func (*fshandler) DirectUploadEnabled() bool { return false }

func (fh *fshandler) UploadMultipartPart(
	_ context.Context,
	_ *types.FileDef,
	uploadID string,
	partNumber int,
	offset int64,
	body io.Reader,
	size int64,
) (media.MultipartPart, error) {
	if partNumber <= 0 || offset < 0 || size <= 0 {
		return media.MultipartPart{}, types.ErrMalformed
	}
	temporaryPath, err := fh.multipartPath(uploadID)
	if err != nil {
		return media.MultipartPart{}, err
	}
	temporary, err := os.OpenFile(temporaryPath, os.O_WRONLY, 0600)
	if err != nil {
		return media.MultipartPart{}, err
	}
	defer temporary.Close()

	hasher := sha256.New()
	limited := io.LimitReader(body, size+1)
	written, err := io.Copy(
		io.MultiWriter(io.NewOffsetWriter(temporary, offset), hasher),
		limited,
	)
	if err != nil {
		return media.MultipartPart{}, err
	}
	if written != size {
		return media.MultipartPart{}, types.ErrMalformed
	}
	if err = temporary.Sync(); err != nil {
		return media.MultipartPart{}, err
	}
	return media.MultipartPart{
		PartNumber: partNumber,
		ETag:       hex.EncodeToString(hasher.Sum(nil)),
	}, nil
}

func (fh *fshandler) CompleteMultipartUpload(
	_ context.Context,
	fdef *types.FileDef,
	uploadID string,
	parts []media.MultipartPart,
) (string, int64, error) {
	if fdef == nil || len(parts) == 0 {
		return "", 0, types.ErrMalformed
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber })
	for index, part := range parts {
		if part.PartNumber != index+1 || part.ETag == "" {
			return "", 0, types.ErrMalformed
		}
	}
	temporaryPath, err := fh.multipartPath(uploadID)
	if err != nil {
		return "", 0, err
	}
	finalPath := fdef.Location
	if finalPath == "" {
		finalPath = filepath.Join(fh.FileUploadDirectory, fdef.Uid().String32())
		fdef.Location = finalPath
	}
	info, err := os.Stat(temporaryPath)
	if os.IsNotExist(err) {
		// Completion can be retried if persisting the upload state failed after rename.
		if info, finalErr := os.Stat(finalPath); finalErr == nil {
			fdef.ETag = etagFromPath(finalPath)
			return fh.fileURL(fdef), info.Size(), nil
		}
	}
	if err != nil {
		return "", 0, err
	}
	if info.Size() <= 0 {
		return "", 0, types.ErrMalformed
	}
	if err = os.Rename(temporaryPath, finalPath); err != nil {
		return "", 0, err
	}
	fdef.ETag = etagFromPath(finalPath) + "-" + strconv.FormatInt(info.Size(), 10)
	return fh.fileURL(fdef), info.Size(), nil
}

func (fh *fshandler) AbortMultipartUpload(
	_ context.Context,
	_ *types.FileDef,
	uploadID string,
) error {
	temporaryPath, err := fh.multipartPath(uploadID)
	if err != nil {
		return err
	}
	if err = os.Remove(temporaryPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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

func (fh *fshandler) quarantinePath(location string) (string, error) {
	root, err := filepath.Abs(fh.FileUploadDirectory)
	if err != nil {
		return "", err
	}
	source, err := filepath.Abs(location)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, source)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || relative == ".." {
		return "", types.ErrPermissionDenied
	}
	quarantineRoot := filepath.Join(root, ".quarantine")
	if err = os.MkdirAll(quarantineRoot, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(quarantineRoot, filepath.Base(source)), nil
}

// Quarantine 把恶意文件原子移动到上传根目录下不可公开访问的隔离目录。
func (fh *fshandler) Quarantine(_ context.Context, location string) (string, error) {
	target, err := fh.quarantinePath(location)
	if err != nil {
		return "", err
	}
	if err = os.Rename(location, target); err != nil {
		return "", err
	}
	return target, nil
}

func (fh *fshandler) ReleaseQuarantine(_ context.Context, quarantineLocation, originalLocation string) error {
	expected, err := fh.quarantinePath(originalLocation)
	if err != nil || expected != quarantineLocation {
		return types.ErrPermissionDenied
	}
	return os.Rename(quarantineLocation, originalLocation)
}

func (fh *fshandler) DeleteQuarantine(_ context.Context, quarantineLocation string) error {
	root, err := filepath.Abs(filepath.Join(fh.FileUploadDirectory, ".quarantine"))
	if err != nil {
		return err
	}
	target, err := filepath.Abs(quarantineLocation)
	if err != nil || filepath.Dir(target) != root {
		return types.ErrPermissionDenied
	}
	if err = os.Remove(target); err != nil && !os.IsNotExist(err) {
		return err
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
