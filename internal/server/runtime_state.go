package server

import (
	"sync"
	"time"

	admincontrol "chat/server/admin"
	"chat/server/auth"
	_ "chat/server/auth/anon"
	_ "chat/server/auth/basic"
	_ "chat/server/auth/code"
	_ "chat/server/auth/rest"
	_ "chat/server/auth/token"
	_ "chat/server/db/mongodb"
	_ "chat/server/db/mysql"
	_ "chat/server/db/postgres"
	_ "chat/server/db/rethinkdb"
	_ "chat/server/media/fs"
	_ "chat/server/media/s3"
	_ "chat/server/push/fcm"
	"chat/server/store/types"
	_ "chat/server/validate/email"
	_ "chat/server/validate/tel"

	"google.golang.org/grpc"
)

// CredValidator 保存凭证验证器的额外配置参数。
type credValidator struct {
	// 需要此验证器的认证级别
	requiredAuthLvl []auth.Level
	// addToTags 保存addToTags。
	addToTags bool
}

// globals 保存globals的共享实例或运行状态。
var globals struct {
	// Topic 缓存和处理
	hub *Hub
	// 指示是否正在关闭
	shuttingDown bool
	// Session 缓存
	sessionStore *SessionStore
	// 集群数据
	cluster *Cluster
	// health 聚合 Liveness、Readiness、Drain 和写入门禁状态。
	health *serviceHealth
	// adminControl 保存版本化权限与基础配置控制面。
	adminControl *admincontrol.ControlPlane
	// translation 提供支持热加载和多服务商的消息翻译能力。
	translation *translationRuntime
	// businessPolicy 是商城业务权限的短 TTL、失败关闭客户端。
	businessPolicy *businessPolicyClient
	// gRPC 服务器
	grpcServer *grpc.Server
	// 插件
	plugins []Plugin
	// 运行时统计通信管道
	statsUpdate       chan *varUpdate
	statsShutdownOnce sync.Once
	// 用户缓存通信管道
	usersUpdate chan *UserCacheReq

	// 凭证验证器
	validators map[string]credValidator
	// 传递给客户端的凭证验证器配置
	validatorClientConfig map[string][]string
	// 每个认证级别需要的验证器
	authValidators map[auth.Level][]string

	// 用于签名 API 密钥的盐值
	apiKeySalt []byte
	// 对客户端不可变的标签命名空间（前缀）
	immutableTagNS map[string]bool
	// 对用户不可变、对 Topic 部分可变的标签命名空间：
	// 用户只能修改自己拥有的标签
	maskedTagNS map[string]bool
	// 用于唯一用户和 Topic 别名的命名空间
	aliasTagNS string

	// 添加 Strict-Transport-Security 头部，值表示有效时间
	// 空字符串 "" 表示关闭
	tlsStrictMaxAge string
	// 监听此地址:端口并将连接重定向到 HTTPS 端口
	tlsRedirectHTTP string
	// 允许的对端最大消息大小
	maxMessageSize int64
	// 群组 Topic 最大订阅者数量
	maxSubscriberCount int
	// 可索引标签最大数量
	maxTagCount int
	// 如果为 true，普通用户不能删除账号
	permanentAccounts bool

	// 最大允许上传大小
	maxFileUploadSize int64
	// 废弃媒体上传的垃圾回收周期
	mediaGcPeriod time.Duration
	// 文件扫描、转码与预览后台处理器。
	fileProcessor *fileProcessingRuntime

	// 优先使用 X-Forwarded-For 头部作为客户端 IP 地址
	useXForwardedFor bool

	// 添加 X-Frame-Options 头部到 HTTP 响应
	xFrameOptions string

	// 默认分配给 Session 的国家代码
	defaultCountryCode string

	// 通话未接听前超时时间
	callEstablishmentTimeout int

	// Agora 语音和视频通话服务端配置；nil 表示未启用。
	agora *agoraProvider

	// 是否启用 WebSocket 每消息压缩协商
	wsCompression bool

	// 主端点 URL
	// 已废弃：应使用文件服务 gRPC API。此功能将被移除
	servingAt string

	// P2P 认证访问模式。根据 P2PDeleteAge 决定是否包含 D 权限
	typesModeCP2P types.AccessMode

	// 可使用 'D' 权限删除的消息最大存活时间
	msgDeleteAge time.Duration
}
