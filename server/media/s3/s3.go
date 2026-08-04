// Package s3 实现将媒体文件对象存储在 Amazon S3 或兼容 S3 协议的对象存储（如 MinIO、阿里云 OSS 等）中的 media 处理器。
package s3

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"chat/server/logs"
	"chat/server/media"
	"chat/server/store"
	"chat/server/store/types"
)

const (
	// defaultServeURL 指定默认ServeURL。
	defaultServeURL = "/v0/file/s/"
	// defaultCacheControl 指定默认缓存Control。
	defaultCacheControl = "no-cache, must-revalidate"

	// handlerName 指定处理器名称。
	handlerName = "s3"
	// 生成预签名 GET URL 的默认有效秒数
	defaultPresignDuration = 120
)

// awsconfig 保存awsconfig的数据和运行状态。
type awsconfig struct {
	// AccessKeyId 保存Access键标识。
	AccessKeyId string `json:"access_key_id"`
	// SecretAccessKey 保存密钥Access键。
	SecretAccessKey string `json:"secret_access_key"`
	// Region 保存Region。
	Region string `json:"region"`
	// DisableSSL 保存DisableSSL。
	DisableSSL bool `json:"disable_ssl"`
	// ForcePathStyle 保存ForcePathStyle。
	ForcePathStyle bool `json:"force_path_style"`
	// Endpoint 保存Endpoint。
	Endpoint string `json:"endpoint"`
	// BucketName 保存Bucket名称。
	BucketName string `json:"bucket"`
	// CorsOrigins 保存CorsOrigins列表。
	CorsOrigins []string `json:"cors_origins"`
	// ServeURL 保存ServeURL。
	ServeURL string `json:"serve_url"`
	// PresignTTL 保存PresignTTL。
	PresignTTL int `json:"presign_ttl"`
	// CacheControl 保存缓存Control。
	CacheControl string `json:"cache_control"`
	// DirectUpload 启用浏览器预签名 Multipart 直传。
	DirectUpload bool `json:"direct_upload"`
	// CDNBaseURL 是通过文件 ACL 后使用的 CDN 源地址。
	CDNBaseURL    string `json:"cdn_base_url"`
	CDNKeyID      string `json:"cdn_key_id"`
	CDNHMACSecret string `json:"cdn_hmac_secret"`
	CDNTTL        int    `json:"cdn_ttl"`
}

// awshandler 保存awshandler的数据和运行状态。
type awshandler struct {
	// svc 保存svc。
	svc *s3.Client
	// presign 保存presign。
	presign *s3.PresignClient
	// conf 保存conf。
	conf awsconfig
	// corsOrigins 保存corsOrigins列表。
	corsOrigins []media.AllowedOrigin
}

var _ media.QuarantineHandler = (*awshandler)(nil)

// readerCounter 用于通过 io.Reader 实时统计读取字节数的计数器。
type readerCounter struct {
	// Embedded 嵌入公共状态或行为，供当前结构直接复用。
	io.Reader
	// count 保存数量。
	count int64
	// reader 保存reader。
	reader io.Reader
}

// Read 读取字节并累加记录已读取的字节总数。
func (rc *readerCounter) Read(buf []byte) (int, error) {
	n, err := rc.reader.Read(buf)
	atomic.AddInt64(&rc.count, int64(n))
	return n, err
}

// Init 初始化 S3 媒体处理器。
func (ah *awshandler) Init(jsconf string) error {
	var err error
	if err = json.Unmarshal([]byte(jsconf), &ah.conf); err != nil {
		return errors.New("解析配置失败: " + err.Error())
	}

	if ah.conf.AccessKeyId == "" {
		return errors.New("缺少 Access Key ID 配置")
	}
	if ah.conf.SecretAccessKey == "" {
		return errors.New("缺少 Secret Access Key 配置")
	}
	if ah.conf.Region == "" {
		return errors.New("缺少 Region 配置")
	}
	if ah.conf.BucketName == "" {
		return errors.New("缺少 Bucket 名称配置")
	}
	if ah.conf.PresignTTL <= 0 {
		ah.conf.PresignTTL = defaultPresignDuration
	}
	if ah.conf.CacheControl == "" {
		ah.conf.CacheControl = defaultCacheControl
	}
	if ah.conf.ServeURL == "" {
		ah.conf.ServeURL = defaultServeURL
	}
	if ah.conf.CDNTTL <= 0 {
		ah.conf.CDNTTL = ah.conf.PresignTTL
	}
	ah.conf.CDNBaseURL = strings.TrimRight(ah.conf.CDNBaseURL, "/")
	ah.corsOrigins, err = media.ParseCORSAllow(ah.conf.CorsOrigins)
	if err != nil {
		return errors.New("解析 CORS 允许源失败: " + err.Error())
	}

	cfgOpts := []func(*config.LoadOptions) error{
		config.WithRegion(ah.conf.Region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			ah.conf.AccessKeyId,
			ah.conf.SecretAccessKey,
			"",
		)),
	}

	var cfg aws.Config
	if cfg, err = config.LoadDefaultConfig(context.Background(), cfgOpts...); err != nil {
		return err
	}

	// 创建 S3 服务客户端
	clientOpts := []func(*s3.Options){
		func(o *s3.Options) {
			o.UsePathStyle = ah.conf.ForcePathStyle
		},
	}
	if ah.conf.Endpoint != "" {
		endpoint := ah.conf.Endpoint
		if !strings.Contains(endpoint, "://") {
			if ah.conf.DisableSSL {
				endpoint = "http://" + endpoint
			} else {
				endpoint = "https://" + endpoint
			}
		}
		clientOpts = append(clientOpts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(endpoint)
		})
	}
	ah.svc = s3.NewFromConfig(cfg, clientOpts...)
	ah.presign = s3.NewPresignClient(ah.svc)

	// 检查指定存储桶 (Bucket) 是否已存在
	_, err = ah.svc.HeadBucket(context.Background(), &s3.HeadBucketInput{Bucket: aws.String(ah.conf.BucketName)})
	if err == nil {
		// Bucket 已存在
		if ah.conf.DirectUpload {
			return ah.configureDirectUploadCORS(context.Background())
		}
		return nil
	}

	if !isAPIError(err, "NoSuchBucket", "NotFound") {
		// 严重的 API 错误直接返回
		return err
	}

	// Bucket 不存在，自动创建存储桶
	_, err = ah.svc.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(ah.conf.BucketName)})
	if err != nil {
		if isAPIError(err, "BucketAlreadyExists", "BucketAlreadyOwnedByYou", "OperationAborted") {
			// 在集群环境下可能已有其它节点创建了 Bucket
			err = nil
		}
	} else {
		// 存储桶创建成功，设置 CORS 跨域策略
		origins := ah.conf.CorsOrigins
		if len(origins) == 0 {
			origins = append(origins, "*")
		}
		_, err = ah.svc.PutBucketCors(context.Background(), &s3.PutBucketCorsInput{
			Bucket: aws.String(ah.conf.BucketName),
			CORSConfiguration: &s3types.CORSConfiguration{
				CORSRules: []s3types.CORSRule{{
					AllowedMethods: []string{http.MethodGet, http.MethodHead, http.MethodPut},
					AllowedOrigins: origins,
					AllowedHeaders: []string{"*"},
					ExposeHeaders:  []string{"ETag"},
				}},
			},
		})
	}
	return err
}

// Headers 添加 CORS 响应头，并将 GET 和 HEAD 请求重定向到 S3 预签名 URL。
func (ah *awshandler) Headers(method string, url *url.URL, headers http.Header, serve bool) (http.Header, int, error) {
	// 添加 CORS 响应标头
	headers, status := media.CORSHandler(method, headers, ah.corsOrigins, serve)
	if status != 0 || method == http.MethodPost || method == http.MethodPut {
		return headers, status, nil
	}

	fid := ah.GetIdFromUrl(url.String())
	if fid.IsZero() {
		return nil, 0, types.ErrNotFound
	}

	fdef, err := ah.getFileRecord(fid)
	if err != nil {
		return nil, 0, err
	}

	if fdef.ETag != "" && headers.Get("If-None-Match") == `"`+fdef.ETag+`"` {
		return http.Header{
				"ETag":          {`"` + fdef.ETag + `"`},
				"Cache-Control": {ah.conf.CacheControl},
			},
			http.StatusNotModified, nil
	}

	ctx := context.Background()
	var redirURL string
	if ah.conf.CDNBaseURL != "" {
		redirURL = ah.cdnURL(fdef.Location)
	}
	switch method {
	case http.MethodGet:
		if redirURL != "" {
			break
		}
		// 如果查询参数 "asatt" 设为 true，则设置 Content-Disposition 为 attachment（附件强制下载），
		// 促使浏览器下载文件而非内联展示，防止 HTML 等文件引发的 XSS 漏洞。
		var contentDisposition *string
		if isAttachment, _ := strconv.ParseBool(url.Query().Get("asatt")); isAttachment {
			contentDisposition = aws.String("attachment")
		}
		presigned, err := ah.presign.PresignGetObject(ctx, &s3.GetObjectInput{
			Bucket:                     aws.String(ah.conf.BucketName),
			Key:                        aws.String(fid.String32()),
			ResponseCacheControl:       aws.String(ah.conf.CacheControl),
			ResponseContentType:        aws.String(fdef.MimeType),
			ResponseContentDisposition: contentDisposition,
		}, func(opts *s3.PresignOptions) {
			opts.Expires = time.Second * time.Duration(ah.conf.PresignTTL)
		})
		if err != nil {
			return nil, 0, err
		}
		redirURL = presigned.URL
	case http.MethodHead:
		if redirURL != "" {
			break
		}
		presigned, err := ah.presign.PresignHeadObject(ctx, &s3.HeadObjectInput{
			Bucket: aws.String(ah.conf.BucketName),
			Key:    aws.String(fid.String32()),
		}, func(opts *s3.PresignOptions) {
			opts.Expires = time.Second * time.Duration(ah.conf.PresignTTL)
		})
		if err != nil {
			return nil, 0, err
		}
		redirURL = presigned.URL
	}

	if redirURL != "" {
		// 返回预签名 URL 并使用 308 永久重定向。
		return http.Header{
				"Location":      {redirURL},
				"ETag":          {`"` + fdef.ETag + `"`},
				"Content-Type":  {"application/json; charset=utf-8"},
				"Cache-Control": {ah.conf.CacheControl},
			},
			http.StatusPermanentRedirect, nil
	}
	return nil, 0, nil
}

func (ah *awshandler) configureDirectUploadCORS(ctx context.Context) error {
	origins := ah.conf.CorsOrigins
	if len(origins) == 0 {
		origins = []string{"*"}
	}
	_, err := ah.svc.PutBucketCors(ctx, &s3.PutBucketCorsInput{
		Bucket: aws.String(ah.conf.BucketName),
		CORSConfiguration: &s3types.CORSConfiguration{CORSRules: []s3types.CORSRule{{
			AllowedMethods: []string{http.MethodGet, http.MethodHead, http.MethodPut},
			AllowedOrigins: origins,
			AllowedHeaders: []string{"*"},
			ExposeHeaders:  []string{"ETag"},
		}}},
	})
	return err
}

func (ah *awshandler) cdnURL(key string) string {
	resourcePath := "/" + strings.TrimLeft(key, "/")
	result := ah.conf.CDNBaseURL + resourcePath
	if ah.conf.CDNHMACSecret == "" {
		return result
	}
	expires := time.Now().Add(time.Duration(ah.conf.CDNTTL) * time.Second).Unix()
	message := fmt.Sprintf("%s\n%d", resourcePath, expires)
	mac := hmac.New(sha256.New, []byte(ah.conf.CDNHMACSecret))
	_, _ = mac.Write([]byte(message))
	values := url.Values{
		"expires":   {strconv.FormatInt(expires, 10)},
		"signature": {base64.RawURLEncoding.EncodeToString(mac.Sum(nil))},
	}
	if ah.conf.CDNKeyID != "" {
		values.Set("key_id", ah.conf.CDNKeyID)
	}
	return result + "?" + values.Encode()
}

// CreateMultipartUpload 创建由浏览器直接写入的 S3 Multipart 会话。
func (ah *awshandler) CreateMultipartUpload(
	ctx context.Context,
	fdef *types.FileDef,
) (string, error) {
	key := strings.TrimLeft(fdef.Location, "/")
	if key == "" {
		key = fdef.Uid().String32()
	}
	result, err := ah.svc.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(ah.conf.BucketName), Key: aws.String(key),
		ContentType: aws.String(fdef.MimeType), CacheControl: aws.String(ah.conf.CacheControl),
	})
	if err != nil {
		return "", err
	}
	fdef.Location = key
	return aws.ToString(result.UploadId), nil
}

// DirectUploadEnabled reports whether presigned browser-to-S3 uploads are enabled.
func (ah *awshandler) DirectUploadEnabled() bool {
	return ah.conf.DirectUpload
}

// UploadMultipartPart streams one tus request body directly into S3.
func (ah *awshandler) UploadMultipartPart(
	ctx context.Context,
	fdef *types.FileDef,
	uploadID string,
	partNumber int,
	_ int64,
	body io.Reader,
	size int64,
) (media.MultipartPart, error) {
	if partNumber <= 0 || size <= 0 {
		return media.MultipartPart{}, types.ErrMalformed
	}
	result, err := ah.svc.UploadPart(ctx, &s3.UploadPartInput{
		Bucket: aws.String(ah.conf.BucketName), Key: aws.String(fdef.Location),
		UploadId: aws.String(uploadID), PartNumber: aws.Int32(int32(partNumber)),
		Body: body, ContentLength: aws.Int64(size),
	})
	if err != nil {
		return media.MultipartPart{}, err
	}
	if strings.TrimSpace(aws.ToString(result.ETag)) == "" {
		return media.MultipartPart{}, errors.New("S3 upload part returned an empty ETag")
	}
	return media.MultipartPart{PartNumber: partNumber, ETag: aws.ToString(result.ETag)}, nil
}

// PresignMultipartPart 为单个浏览器 PUT 分块生成短期签名。
func (ah *awshandler) PresignMultipartPart(
	ctx context.Context,
	fdef *types.FileDef,
	uploadID string,
	partNumber int,
) (*media.PresignedPart, error) {
	result, err := ah.presign.PresignUploadPart(ctx, &s3.UploadPartInput{
		Bucket: aws.String(ah.conf.BucketName), Key: aws.String(fdef.Location),
		UploadId: aws.String(uploadID), PartNumber: aws.Int32(int32(partNumber)),
	}, func(options *s3.PresignOptions) {
		options.Expires = time.Duration(ah.conf.PresignTTL) * time.Second
	})
	if err != nil {
		return nil, err
	}
	return &media.PresignedPart{PartNumber: partNumber, URL: result.URL}, nil
}

// ListMultipartParts 从 R2/S3 查询真实分片，避免依赖浏览器暴露 ETag 响应头。
func (ah *awshandler) ListMultipartParts(
	ctx context.Context,
	fdef *types.FileDef,
	uploadID string,
) ([]media.MultipartPart, error) {
	if fdef == nil || fdef.Location == "" || uploadID == "" {
		return nil, types.ErrMalformed
	}
	paginator := s3.NewListPartsPaginator(ah.svc, &s3.ListPartsInput{
		Bucket:   aws.String(ah.conf.BucketName),
		Key:      aws.String(fdef.Location),
		UploadId: aws.String(uploadID),
	})
	parts := make([]media.MultipartPart, 0)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, part := range page.Parts {
			partNumber := int(aws.ToInt32(part.PartNumber))
			etag := strings.TrimSpace(aws.ToString(part.ETag))
			if partNumber <= 0 || etag == "" {
				return nil, types.ErrMalformed
			}
			parts = append(parts, media.MultipartPart{
				PartNumber: partNumber,
				ETag:       etag,
			})
		}
	}
	sort.Slice(parts, func(i, j int) bool {
		return parts[i].PartNumber < parts[j].PartNumber
	})
	return parts, nil
}

// CompleteMultipartUpload 合并已确认分块并用 HeadObject 校验最终大小。
func (ah *awshandler) CompleteMultipartUpload(
	ctx context.Context,
	fdef *types.FileDef,
	uploadID string,
	parts []media.MultipartPart,
) (string, int64, error) {
	completed := make([]s3types.CompletedPart, 0, len(parts))
	for _, part := range parts {
		completed = append(completed, s3types.CompletedPart{
			ETag: aws.String(strings.TrimSpace(part.ETag)), PartNumber: aws.Int32(int32(part.PartNumber)),
		})
	}
	result, err := ah.svc.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(ah.conf.BucketName), Key: aws.String(fdef.Location),
		UploadId: aws.String(uploadID), MultipartUpload: &s3types.CompletedMultipartUpload{Parts: completed},
	})
	if err != nil {
		return "", 0, err
	}
	fdef.ETag = strings.Trim(aws.ToString(result.ETag), "\"")
	head, err := ah.svc.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(ah.conf.BucketName), Key: aws.String(fdef.Location),
	})
	if err != nil {
		_, _ = ah.svc.DeleteObject(context.Background(), &s3.DeleteObjectInput{
			Bucket: aws.String(ah.conf.BucketName), Key: aws.String(fdef.Location),
		})
		return "", 0, err
	}
	fname := fdef.Id
	if extensions, _ := mime.ExtensionsByType(fdef.MimeType); len(extensions) > 0 {
		fname += extensions[0]
	}
	return ah.conf.ServeURL + fname, aws.ToInt64(head.ContentLength), nil
}

// AbortMultipartUpload 释放未完成的对象存储分块。
func (ah *awshandler) AbortMultipartUpload(
	ctx context.Context,
	fdef *types.FileDef,
	uploadID string,
) error {
	_, err := ah.svc.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket: aws.String(ah.conf.BucketName), Key: aws.String(fdef.Location),
		UploadId: aws.String(uploadID),
	})
	return err
}

// Upload 处理文件上传请求，将 io.Reader 流数据通过 AWS S3 TransferManager 上传至存储桶。
func (ah *awshandler) Upload(fdef *types.FileDef, file io.Reader) (string, int64, error) {
	var err error

	key := fdef.Uid().String32()

	tmClient := transfermanager.New(ah.svc)

	if err = store.Files.StartUpload(fdef); err != nil {
		logs.Warn.Println("创建文件记录失败", fdef.Id, err)
		return "", 0, err
	}

	rc := readerCounter{reader: file}
	result, err := tmClient.UploadObject(context.Background(), &transfermanager.UploadObjectInput{
		CacheControl: aws.String(ah.conf.CacheControl),
		Bucket:       aws.String(ah.conf.BucketName),
		Key:          aws.String(key),
		Body:         &rc,
	})

	if err != nil {
		return "", 0, err
	}

	fname := fdef.Id
	ext, _ := mime.ExtensionsByType(fdef.MimeType)
	if len(ext) > 0 {
		fname += ext[0]
	}

	fdef.Location = key
	if result.ETag != nil {
		fdef.ETag = strings.Trim(*result.ETag, "\"")
	}

	return ah.conf.ServeURL + fname, rc.count, nil
}

// Download 处理文件下载请求（S3 模式下客户端由重定向或预签名 URL 轮询直接处理，因此此处返回 Unsupported）。
func (ah *awshandler) Download(url string) (*types.FileDef, media.ReadSeekCloser, error) {
	return nil, nil, types.ErrUnsupported
}

// Delete 从 AWS S3 存储桶中分批批量删除指定 key 的文件。
func (ah *awshandler) Delete(locations []string) error {
	ctx := context.Background()
	for i := 0; i < len(locations); i += 1000 {
		end := i + 1000
		if end > len(locations) {
			end = len(locations)
		}

		objects := make([]s3types.ObjectIdentifier, end-i)
		for j, key := range locations[i:end] {
			objects[j] = s3types.ObjectIdentifier{Key: aws.String(key)}
		}

		_, err := ah.svc.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(ah.conf.BucketName),
			Delete: &s3types.Delete{
				Objects: objects,
				Quiet:   aws.Bool(true),
			},
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (ah *awshandler) copyObject(ctx context.Context, source, target string) error {
	copySource := url.PathEscape(ah.conf.BucketName + "/" + strings.TrimLeft(source, "/"))
	_, err := ah.svc.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket: aws.String(ah.conf.BucketName), Key: aws.String(target), CopySource: aws.String(copySource),
	})
	return err
}

// Quarantine 把对象复制到不可由 ServeURL 解析的 quarantine/ 前缀，再删除公开位置。
func (ah *awshandler) Quarantine(ctx context.Context, location string) (string, error) {
	location = strings.TrimLeft(strings.TrimSpace(location), "/")
	if location == "" || strings.HasPrefix(location, "quarantine/") {
		return "", types.ErrMalformed
	}
	target := "quarantine/" + location
	if err := ah.copyObject(ctx, location, target); err != nil {
		return "", err
	}
	if _, err := ah.svc.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(ah.conf.BucketName), Key: aws.String(location),
	}); err != nil {
		return "", err
	}
	return target, nil
}

func (ah *awshandler) ReleaseQuarantine(ctx context.Context, quarantineLocation, originalLocation string) error {
	if !strings.HasPrefix(quarantineLocation, "quarantine/") ||
		strings.TrimPrefix(quarantineLocation, "quarantine/") != strings.TrimLeft(originalLocation, "/") {
		return types.ErrPermissionDenied
	}
	if err := ah.copyObject(ctx, quarantineLocation, originalLocation); err != nil {
		return err
	}
	_, err := ah.svc.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(ah.conf.BucketName), Key: aws.String(quarantineLocation),
	})
	return err
}

func (ah *awshandler) DeleteQuarantine(ctx context.Context, quarantineLocation string) error {
	if !strings.HasPrefix(quarantineLocation, "quarantine/") {
		return types.ErrPermissionDenied
	}
	_, err := ah.svc.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(ah.conf.BucketName), Key: aws.String(quarantineLocation),
	})
	return err
}

// isAPIError 辅助函数：判断错误是否为指定的 AWS API 错误码。
func isAPIError(err error, codes ...string) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	for _, code := range codes {
		if apiErr.ErrorCode() == code {
			return true
		}
	}
	return false
}

// GetIdFromUrl 从附件 URL 中解析并提取文件 UID。
func (ah *awshandler) GetIdFromUrl(url string) types.Uid {
	return media.GetIdFromUrl(url, ah.conf.ServeURL)
}

// getFileRecord 根据文件 UID 从数据库读取对应的文件元数据记录。
func (ah *awshandler) getFileRecord(fid types.Uid) (*types.FileDef, error) {
	fd, err := store.Files.Get(fid.String())
	if err != nil {
		return nil, err
	}
	if fd == nil {
		return nil, types.ErrNotFound
	}
	return fd, nil
}

// init 注册当前包提供的实现并初始化包级状态。
func init() {
	store.RegisterMediaHandler(handlerName, &awshandler{})
}
