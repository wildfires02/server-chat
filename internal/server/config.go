package server

import (
	"encoding/json"
)

// 凭证验证器配置。
type validatorConfig struct {
	// 设置为 TRUE 或 FALSE
	AddToTags bool `json:"add_to_tags"`
	// 触发此验证器的认证级别："auth"、"anon"... 或 ""
	Required []string `json:"required"`
	// 验证器参数，原样传递给验证器
	Config json.RawMessage `json:"config"`
}

// 未验证用户账号垃圾回收配置。
type accountGcConfig struct {
	// Enabled 指示是否启用或满足Enabled。
	Enabled bool `json:"enabled"`
	// 运行 GC 的频率（秒）
	GcPeriod int `json:"gc_period"`
	// 一次删除的账号数量
	GcBlockSize int `json:"gc_block_size"`
	// 账号最后修改后的最小小时数
	GcMinAccountAge int `json:"gc_min_account_age"`
}

// 大文件处理配置。
type mediaConfig struct {
	// 用于文件上传的处理器名称
	UseHandler string `json:"use_handler"`
	// 上传文件的最大允许大小
	MaxFileUploadSize int64 `json:"max_size"`
	// 垃圾回收超时时间
	GcPeriod int `json:"gc_period"`
	// 一次删除的条目数
	GcBlockSize int `json:"gc_block_size"`
	// 各个处理器的配置参数，原样传递给处理器
	Handlers map[string]json.RawMessage `json:"handlers"`
	// 后台安全扫描、压缩与在线预览处理。
	Processing *mediaProcessingConfig `json:"processing,omitempty"`
}

type mediaProcessingConfig struct {
	Enabled      bool   `json:"enabled"`
	Workers      int    `json:"workers"`
	QueueSize    int    `json:"queue_size"`
	Timeout      int    `json:"timeout"`
	PollInterval int    `json:"poll_interval"`
	MaxAttempts  int    `json:"max_attempts"`
	RetryBase    int    `json:"retry_base"`
	LeaseSeconds int    `json:"lease_seconds"`
	ClamAVAddr   string `json:"clamav_addr"`
	FFmpeg       string `json:"ffmpeg"`
	LibreOffice  string `json:"libreoffice"`
}

//adminAPIConfig控制独立的Svelte管理API。 引导
// 令牌是临时的：团购身份和Casbin政策同步
// 将在最终集成阶段替换它。
type adminAPIConfig struct {
	Enabled        bool     `json:"enabled"`
	WorkerID       int      `json:"worker_id"`
	BootstrapToken string   `json:"bootstrap_token"`
	AllowedOrigins []string `json:"allowed_origins"`
}

//translationConsumerConfig控制im-server使用编写的设置方式
// 通过独立启动的im-admin进程。
type translationConsumerConfig struct {
	Enabled         bool `json:"enabled"`
	RefreshInterval int  `json:"refresh_interval"`
}

// 配置文件内容
type configType struct {
	//日志标志控制控制台日志格式。 它由Viper从YAML加载的。
	LogFlags string `json:"log_flags"`
	// Runtime 保存显式运行环境和单机/集群部署模式。
	Runtime runtimeConfig `json:"runtime"`
	// 监听 WebSocket 和长轮询客户端的 HTTP(S) 地址:端口。可以是
	// 数字或规范名称，例如 ":80" 或 ":https"。可以包含主机名，例如
	// "localhost:80"。
	// 可以为空：如果未配置 TLS，使用 ":80"，否则使用 ":443"。
	Listen string `json:"listen"`
	// 对外暴露的外部基础 URL（在负载均衡器 / 反向代理 / Unix Domain Socket 部署下使用）
	ExtUrl string `json:"ext_url"`
	// 流式和大文件 API 调用的基础 URL 路径，默认为 '/'
	ApiPath string `json:"api_path"`
	// 静态内容的 Cache-Control 值
	CacheControl int `json:"cache_control"`
	// 如果为 true，不尝试协商 WebSocket 每消息压缩（RFC 7692.4）
	// 如果使用 MSFT IIS 作为反向代理，应禁用（设为 true）
	WSCompressionDisabled bool `json:"ws_compression_disabled"`
	// 监听 gRPC 客户端的地址:端口。如果为空则不初始化 gRPC 支持
	GrpcListen string `json:"grpc_listen"`
	// 启用 gRPC 保活处理 https://github.com/grpc/grpc/blob/master/doc/keepalive.md
	// 这将服务器的 GRPC_ARG_KEEPALIVE_TIME_MS 设置为 60 秒而不是默认的 2 小时
	GrpcKeepalive bool `json:"grpc_keepalive_enabled"`
	// 挂载静态文件目录（通常是 IMWeb）的 URL 路径
	StaticMount string `json:"static_mount"`
	// 静态文件的本地路径。此路径中的所有文件都可通过 HTTP 访问
	StaticData string `json:"static_data"`
	// 用于签名 API 密钥的盐值
	APIKeySalt []byte `json:"api_key_salt"`
	// 允许客户端发送的最大消息大小。旨在防止恶意客户端发送
	// 非常大的带内文件（不影响带外上传）
	MaxMessageSize int `json:"max_message_size"`
	// 群组 Topic 最大订阅者数量
	MaxSubscriberCount int `json:"max_subscriber_count"`
	// 掩码标签命名空间：对用户不可变的标签（mask），对 Topic 仅在掩码内可变
	MaskedTagNamespaces []string `json:"masked_tags"`
	// 用于唯一用户和 Topic 别名的标签命名空间
	AliasTagNamespace string `json:"alias_tag"`
	// 可索引标签最大数量
	MaxTagCount int `json:"max_tag_count"`
	// 如果为 true，普通用户不能删除账号
	PermanentAccounts bool `json:"permanent_accounts"`
	// 暴露运行时统计的 URL 路径。如果为空则禁用
	ExpvarPath string `json:"expvar"`
	// 内部服务器状态的 URL 路径。如果为空则禁用
	ServerStatusPath string `json:"server_status"`
	// PprofFile 保存退出时写入 CPU 和内存分析文件的基础路径。
	PprofFile string `json:"pprof"`
	// PprofURL 暴露运行时分析信息的 HTTP 路径；生产环境必须禁用。
	PprofURL string `json:"pprof_url"`
	// Health 保存 Liveness、Readiness 和 Drain 配置。
	Health healthConfig `json:"health"`
	// 从 HTTP 头部 'X-Forwarded-For' 获取客户端 IP 地址
	// 当 im 在代理后时很有用。如果缺失，回退到默认的 RemoteAddr
	UseXForwardedFor bool `json:"use_x_forwarded_for"`
	// 添加 X-Frame-Options 到 HTTP 响应头部。应为 "DENY"、"SAMEORIGIN"、
	// "-"（禁用）之一。默认为 SAMEORIGIN
	XFrameOptions string `json:"x_frame_options"`
	// 默认分配给 Session 的 2 字母国家代码（ISO 3166-1 alpha-2）
	// 当客户端未明确指定国家且无法推断时使用
	DefaultCountryCode string `json:"default_country_code"`
	// 允许在 p2p Topic 中为双方硬删除消息
	// 如果设为 'false'，则消息仅为发出命令的对端删除
	// 如果设为 'true'，则任一参与者都可以完全删除消息
	// 更改此值仅影响后续新建的 Topic 的硬删除能力（添加或移除 D 权限）
	P2PDeleteEnabled bool `json:"p2p_delete_enabled"`
	// 用户可使用 'D' 权限删除消息的最大存活时间（秒）
	// 例如 600 表示最多可删除 10 分钟内的消息，更旧的不能删除
	// 缺失或 0 表示无年龄限制
	// 不影响 Topic 所有者：所有者可以删除任何消息
	MsgDeleteAge int `json:"msg_delete_age"`

	// 子系统配置
	Cluster json.RawMessage `json:"cluster_config"`
	// Plugin 保存Plugin。
	Plugin json.RawMessage `json:"plugins"`
	// Store 保存存储。
	Store json.RawMessage `json:"store_config"`
	// Push 保存Push。
	Push json.RawMessage `json:"push"`
	// TLS 保存TLS。
	TLS json.RawMessage `json:"tls"`
	// Auth 按键索引认证。
	Auth map[string]json.RawMessage `json:"auth_config"`
	// Validator 按键索引校验器。
	Validator map[string]*validatorConfig `json:"acc_validation"`
	// AccountGC 保存AccountGC。
	AccountGC *accountGcConfig `json:"acc_gc_config"`
	// Media 保存媒体。
	Media *mediaConfig `json:"media"`
	// WebRTC 保存WebRTC。
	WebRTC json.RawMessage `json:"webrtc"`
	//管理员仅由独立的im-admin入口点消耗。
	Admin *adminAPIConfig `json:"admin,omitempty"`
	//翻译启用了im-admin翻译设置的聊天端消费者。
	Translation *translationConsumerConfig `json:"translation,omitempty"`
}
