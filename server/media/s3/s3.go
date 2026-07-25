// Package s3 实现将媒体文件对象存储在 Amazon S3 或兼容 S3 协议的对象存储（如 MinIO、阿里云 OSS 等）中的 media 处理器。
package s3

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
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
	defaultServeURL     = "/v0/file/s/"
	defaultCacheControl = "no-cache, must-revalidate"

	handlerName = "s3"
	// 生成预签名 GET URL 的默认有效秒数
	defaultPresignDuration = 120
)

type awsconfig struct {
	AccessKeyId     string   `json:"access_key_id"`
	SecretAccessKey string   `json:"secret_access_key"`
	Region          string   `json:"region"`
	DisableSSL      bool     `json:"disable_ssl"`
	ForcePathStyle  bool     `json:"force_path_style"`
	Endpoint        string   `json:"endpoint"`
	BucketName      string   `json:"bucket"`
	CorsOrigins     []string `json:"cors_origins"`
	ServeURL        string   `json:"serve_url"`
	PresignTTL      int      `json:"presign_ttl"`
	CacheControl    string   `json:"cache_control"`
}

type awshandler struct {
	svc         *s3.Client
	presign     *s3.PresignClient
	conf        awsconfig
	corsOrigins []media.AllowedOrigin
}

// readerCounter 用于通过 io.Reader 实时统计读取字节数的计数器。
type readerCounter struct {
	io.Reader
	count  int64
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
					AllowedMethods: []string{http.MethodGet, http.MethodHead},
					AllowedOrigins: origins,
					AllowedHeaders: []string{"*"},
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
	switch method {
	case http.MethodGet:
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

func init() {
	store.RegisterMediaHandler(handlerName, &awshandler{})
}
