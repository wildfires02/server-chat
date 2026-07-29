/******************************************************************************
 *
 *  描述 :
 *
 *  服务端配置解析与初始化主程序。
 *
 *****************************************************************************/

// Package main 实现即时通信服务端的协议、路由和业务逻辑。
package main

//go:generate protoc --go_out=../pbx --go_opt=paths=source_relative --go-grpc_out=../pbx --go-grpc_opt=paths=source_relative ../pbx/model.proto

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"time"

	gh "github.com/gorilla/handlers"

	// Viper 配置解析库
	"github.com/spf13/viper"

	// 身份验证器模块
	"chat/server/auth"
	_ "chat/server/auth/anon"
	_ "chat/server/auth/basic"
	_ "chat/server/auth/code"
	_ "chat/server/auth/rest"
	_ "chat/server/auth/token"
	"chat/server/store/types"

	// 数据库适配器后端
	_ "chat/server/db/mongodb"
	_ "chat/server/db/mysql"
	_ "chat/server/db/postgres"
	_ "chat/server/db/rethinkdb"

	"chat/server/logs"

	// 消息推送模块
	"chat/server/push"
	_ "chat/server/push/fcm"
	_ "chat/server/push/stdout"
	_ "chat/server/push/tnpg"

	"chat/server/store"

	// 凭证验证器
	_ "chat/server/validate/email"
	_ "chat/server/validate/tel"
	"google.golang.org/grpc"

	// 大文件上传处理器
	_ "chat/server/media/fs"
	_ "chat/server/media/s3"
)

const (
	// currentVersion 是当前 API/协议版本
	currentVersion = "0.29"
	// minSupportedVersion 是支持的最小 API 版本
	minSupportedVersion = "0.20"

	// idleSessionTimeout 定义 Session 空闲多久后被终止
	idleSessionTimeout = time.Second * 55
	// idleMasterTopicTimeout 定义最后一个 Session 断开后主 Topic 保持存活的时间
	idleMasterTopicTimeout = time.Second * 4
	// 与上面类似，但代理 Topic 应更快关闭。否则主 Topic 会保持太久
	idleProxyTopicTimeout = time.Second * 2

	// defaultMaxMessageSize 是默认的最大消息大小
	defaultMaxMessageSize = 1 << 19 // 512K

	// defaultMaxSubscriberCount 是群组 Topic 默认最大订阅者数量
	// 也在适配器中设置
	defaultMaxSubscriberCount = 256

	// defaultMaxTagCount 是默认可索引标签最大数量
	defaultMaxTagCount = 16

	// minTagLength 是标签可接受的最短长度（以符文计）。更短的标签会被丢弃
	minTagLength = 2
	// maxTagLength 是标签可接受的最大长度（以符文计）。更长的标签会被截断
	maxTagLength = 96

	// uaTimerDelay 更新用户代理前的延迟
	uaTimerDelay = time.Second * 5

	// defaultMaxDeleteCount 是一次调用中允许删除的最大消息数
	defaultMaxDeleteCount = 1024

	// defaultApiPath 流式 API 服务的基础 URL 路径
	defaultApiPath = "/"

	// defaultStaticMount 静态内容服务的挂载点，http://host-name<defaultStaticMount>
	defaultStaticMount = "/"

	// defaultStaticPath 静态内容的本地路径
	defaultStaticPath = "static"

	// defaultCountryCode 如果配置中未指定 "default_country_code" 字段，
	// 则回退使用的默认国家代码
	defaultCountryCode = "US"

	// defaultCallEstablishmentTimeout 通话未接听的默认超时时间（秒）
	defaultCallEstablishmentTimeout = 30
)

// 编译器定义的构建版本号：
//
//	-ldflags "-X main.buildstamp=value_to_assign_to_buildstamp"
//
// 向客户端响应 {hi} 消息时汇报。
// 例如，要将 buildstamp 定义为服务端构建的时间戳，可以添加
// 编译命令行标志：
//
//	-ldflags "-X main.buildstamp=`date -u '+%Y%m%dT%H:%M:%SZ'`"
//
// 或者将其设置为 git 标签：
//
//	-ldflags "-X main.buildstamp=`git describe --tags`"
var buildstamp = "undef"

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

	// 优先使用 X-Forwarded-For 头部作为客户端 IP 地址
	useXForwardedFor bool

	// 添加 X-Frame-Options 头部到 HTTP 响应
	xFrameOptions string

	// 默认分配给 Session 的国家代码
	defaultCountryCode string

	// 通话未接听前超时时间
	callEstablishmentTimeout int

	// ICE 服务器配置（视频通话）
	iceServers []iceServer

	// Agora 群组语音和视频通话服务端配置；nil 表示未启用。
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
}

// 配置文件内容
type configType struct {
	// 监听 WebSocket 和长轮询客户端的 HTTP(S) 地址:端口。可以是
	// 数字或规范名称，例如 ":80" 或 ":https"。可以包含主机名，例如
	// "localhost:80"。
	// 可以为空：如果未配置 TLS，使用 ":80"，否则使用 ":443"。
	// 可以从命令行覆盖，参见 --listen 选项
	Listen string `json:"listen"`
	// 对外暴露的外部基础 URL（在负载均衡器 / 反向代理 / Unix Domain Socket 部署下使用）
	ExtUrl string `json:"ext_url"`
	// 流式和大文件 API 调用的基础 URL 路径，默认为 '/'
	// 可以从命令行覆盖，参见 --api_path 选项
	ApiPath string `json:"api_path"`
	// 静态内容的 Cache-Control 值
	CacheControl int `json:"cache_control"`
	// 如果为 true，不尝试协商 WebSocket 每消息压缩（RFC 7692.4）
	// 如果使用 MSFT IIS 作为反向代理，应禁用（设为 true）
	WSCompressionDisabled bool `json:"ws_compression_disabled"`
	// 监听 gRPC 客户端的地址:端口。如果为空则不初始化 gRPC 支持
	// 可以从命令行用 --grpc_listen 覆盖
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
}

// main 解析启动参数、初始化依赖并运行当前服务或命令。
func main() {
	executable, _ := os.Executable()

	logFlags := flag.String("log_flags", "stdFlags",
		"逗号分隔的日志标志列表（如 https://golang.org/pkg/log/#pkg-constants 定义，不带 L 前缀）")
	configfile := flag.String("config", "im.conf", "配置文件路径")
	// 静态内容路径
	staticPath := flag.String("static_data", defaultStaticPath, "待服务静态文件目录的文件路径")
	listenOn := flag.String("listen", "", "覆盖 HTTP(S) 客户端监听地址和端口")
	apiPath := flag.String("api_path", "", "覆盖 API 服务的基础 URL 路径")
	listenGrpc := flag.String("grpc_listen", "", "覆盖 gRPC 客户端监听地址和端口")
	tlsEnabled := flag.Bool("tls_enabled", false, "覆盖配置中 TLS 启用设置")
	clusterSelf := flag.String("cluster_self", "", "覆盖当前集群节点名称")
	expvarPath := flag.String("expvar", "", "覆盖暴露运行时统计的 URL 路径。使用 '-' 禁用")
	serverStatusPath := flag.String("server_status", "",
		"覆盖显示服务器内部状态的 URL 路径。使用 '-' 禁用")
	pprofFile := flag.String("pprof", "", "保存性能分析信息的文件名。未设置则禁用")
	pprofUrl := flag.String("pprof_url", "", "仅用于调试！暴露性能分析信息的 URL 路径。未设置则禁用")
	flag.Parse()

	logs.Init(os.Stderr, *logFlags)

	curwd, err := os.Getwd()
	if err != nil {
		logs.Err.Fatal("Couldn't get current working directory: ", err)
	}

	logs.Info.Printf("Server v%s:%s:%s; pid %d; %d process(es)",
		currentVersion, executable, buildstamp,
		os.Getpid(), runtime.GOMAXPROCS(runtime.NumCPU()))

	*configfile = toAbsolutePath(curwd, *configfile)
	logs.Info.Printf("Using config from '%s'", *configfile)

	var config configType
	data, err := os.ReadFile(*configfile)
	if err != nil {
		logs.Err.Fatalf("Failed to read config file '%s': %v", *configfile, err)
	}
	clean := stripJSONComments(data)
	v := viper.New()
	v.SetConfigType("json")
	if err := v.ReadConfig(bytes.NewBuffer(clean)); err != nil {
		logs.Err.Fatalf("Failed to read config file '%s': %v", *configfile, err)
	}
	jsonBytes, err := json.Marshal(v.AllSettings())
	if err != nil {
		logs.Err.Fatalf("Failed to marshal config file '%s': %v", *configfile, err)
	}
	if err := json.Unmarshal(jsonBytes, &config); err != nil {
		logs.Err.Fatalf("Failed to parse config file '%s': %v", *configfile, err)
	}

	if *listenOn != "" {
		config.Listen = *listenOn
	}

	// 设置 HTTP 服务器。必须使用非默认 mux，因为 expvar
	mux := http.NewServeMux()

	// 暴露统计和监控值
	evpath := *expvarPath
	if evpath == "" {
		evpath = config.ExpvarPath
	}
	statsInit(mux, evpath)
	statsRegisterInt("Version")
	decVersion := base10Version(parseVersion(buildstamp))
	if decVersion <= 0 {
		decVersion = base10Version(parseVersion(currentVersion))
	}
	statsSet("Version", decVersion)

	// 初始化调试性能分析服务（可选）
	servePprof(mux, *pprofUrl)

	// 初始化集群并接收计算出的 workerId
	// 集群此时尚未启动
	workerId := clusterInit(config.Cluster, clusterSelf)

	if *pprofFile != "" {
		*pprofFile = toAbsolutePath(curwd, *pprofFile)

		cpuf, err := os.Create(*pprofFile + ".cpu")
		if err != nil {
			logs.Err.Fatal("Failed to create CPU pprof file: ", err)
		}
		defer cpuf.Close()

		memf, err := os.Create(*pprofFile + ".mem")
		if err != nil {
			logs.Err.Fatal("Failed to create Mem pprof file: ", err)
		}
		defer memf.Close()

		pprof.StartCPUProfile(cpuf)
		defer pprof.StopCPUProfile()
		defer pprof.WriteHeapProfile(memf)

		logs.Info.Printf("Profiling info saved to '%s.(cpu|mem)'", *pprofFile)
	}

	err = store.Store.Open(workerId, config.Store)
	logs.Info.Println("DB adapter", store.Store.GetAdapterName(), store.Store.GetAdapterVersion())
	if err != nil {
		logs.Err.Fatal("Failed to connect to DB: ", err)
	}
	defer func() {
		store.Store.Close()
		logs.Info.Println("Closed database connection(s)")
		logs.Info.Println("All done, good bye")
	}()
	statsRegisterDbStats()

	// API 密钥签名密钥
	globals.apiKeySalt = config.APIKeySalt

	err = store.InitAuthLogicalNames(config.Auth["logical_names"])
	if err != nil {
		logs.Err.Fatal(err)
	}

	// 用户发现的标签命名空间列表，客户端不能直接修改
	// 例如 'email' 或 'tel'
	globals.immutableTagNS = make(map[string]bool)

	authNames := store.Store.GetAuthNames()
	for _, name := range authNames {
		if authhdl := store.Store.GetLogicalAuthHandler(name); authhdl == nil {
			logs.Err.Fatalln("Unknown authenticator", name)
		} else if jsconf := config.Auth[authhdl.GetRealName()]; jsconf != nil {
			if err := authhdl.Init(jsconf, name); err != nil {
				logs.Err.Fatalln("Failed to init auth scheme", name+":", err)
			}
			tags, err := authhdl.RestrictedTags()
			if err != nil {
				logs.Err.Fatalln("Failed get restricted tag namespaces (prefixes)", name+":", err)
			}
			for _, tag := range tags {
				if strings.Contains(tag, ":") {
					logs.Err.Fatalln("tags restricted by auth handler should not contain character ':'", tag)
				}
				globals.immutableTagNS[tag] = true
			}
		}
	}

	// 处理验证器
	for name, vconf := range config.Validator {
		// 检查验证器是否具有限制性。如果是，将验证器名称添加到受限标签列表
		// 即使验证器禁用，命名空间也可以受限
		if vconf.AddToTags {
			if strings.Contains(name, ":") {
				logs.Err.Fatalln("acc_validation names should not contain character ':'", name)
			}
			globals.immutableTagNS[name] = true
		}

		if len(vconf.Required) == 0 {
			// 跳过禁用的验证器
			continue
		}

		var reqLevels []auth.Level
		for _, req := range vconf.Required {
			lvl := auth.ParseAuthLevel(req)
			if lvl == auth.LevelNone {
				logs.Err.Fatalf("Invalid required AuthLevel '%s' in validator '%s'", req, name)
			}
			reqLevels = append(reqLevels, lvl)
			if globals.authValidators == nil {
				globals.authValidators = make(map[auth.Level][]string)
			}
			globals.authValidators[lvl] = append(globals.authValidators[lvl], name)
		}

		if val := store.Store.GetValidator(name); val == nil {
			logs.Err.Fatal("Config provided for an unknown validator '" + name + "'")
		} else if err = val.Init(string(vconf.Config)); err != nil {
			logs.Err.Fatal("Failed to init validator '"+name+"': ", err)
		}
		if globals.validators == nil {
			globals.validators = make(map[string]credValidator)
		}
		globals.validators[name] = credValidator{
			requiredAuthLvl: reqLevels,
			addToTags:       vconf.AddToTags,
		}
	}

	// 为客户端创建凭证验证器配置
	if len(globals.authValidators) > 0 {
		globals.validatorClientConfig = make(map[string][]string)
		for key, val := range globals.authValidators {
			globals.validatorClientConfig[key.String()] = val
		}
	}

	// 部分受限的标签命名空间
	globals.maskedTagNS = make(map[string]bool, len(config.MaskedTagNamespaces))
	for _, tag := range config.MaskedTagNamespaces {
		if strings.Contains(tag, ":") {
			logs.Err.Fatal("masked_tags namespaces should not contain character ':'", tag)
		}
		globals.maskedTagNS[tag] = true
	}

	// 别名命名空间
	config.AliasTagNamespace = strings.TrimSpace(config.AliasTagNamespace)
	if config.AliasTagNamespace != "" {
		if prefix, _ := validateTag(config.AliasTagNamespace + ":testing"); prefix == "" {
			logs.Err.Fatal("alias_tag namespace should contain only alphanumeric characters and '_'",
				config.AliasTagNamespace)
		}
		globals.aliasTagNS = config.AliasTagNamespace
	}

	var tags []string
	for tag := range globals.immutableTagNS {
		tags = append(tags, "'"+tag+"'")
	}
	if len(tags) > 0 {
		logs.Info.Println("Restricted tags:", tags)
	}
	tags = nil
	for tag := range globals.maskedTagNS {
		tags = append(tags, "'"+tag+"'")
	}
	if len(tags) > 0 {
		logs.Info.Println("Masked tags:", tags)
	}
	if len(globals.aliasTagNS) > 0 {
		logs.Info.Println("Alias tag:", globals.aliasTagNS)
	}

	// 最大消息大小
	globals.maxMessageSize = int64(config.MaxMessageSize)
	if globals.maxMessageSize <= 0 {
		globals.maxMessageSize = defaultMaxMessageSize
	}
	// 群组 Topic 最大订阅者数量
	globals.maxSubscriberCount = config.MaxSubscriberCount
	if globals.maxSubscriberCount <= 1 {
		globals.maxSubscriberCount = defaultMaxSubscriberCount
	}
	// 每个用户或 Topic 可索引标签最大数量
	globals.maxTagCount = config.MaxTagCount
	if globals.maxTagCount <= 0 {
		globals.maxTagCount = defaultMaxTagCount
	}
	// 是否禁用账号删除
	globals.permanentAccounts = config.PermanentAccounts

	globals.useXForwardedFor = config.UseXForwardedFor
	globals.defaultCountryCode = config.DefaultCountryCode
	if globals.defaultCountryCode == "" {
		globals.defaultCountryCode = defaultCountryCode
	}

	// P2P 默认访问权限模式：有/无 D 权限
	globals.typesModeCP2P = types.ModeCP2P
	if config.P2PDeleteEnabled {
		globals.typesModeCP2P = types.ModeCP2PD
	}

	if config.MsgDeleteAge > 0 {
		globals.msgDeleteAge = time.Duration(config.MsgDeleteAge) * time.Second
	}

	// X-Frame-Options 头部配置
	globals.xFrameOptions = config.XFrameOptions
	if globals.xFrameOptions == "" {
		globals.xFrameOptions = "SAMEORIGIN"
	}
	if globals.xFrameOptions != "SAMEORIGIN" && globals.xFrameOptions != "DENY" && globals.xFrameOptions != "-" {
		logs.Warn.Println("Ignored invalid x_frame_options", config.XFrameOptions)
		globals.xFrameOptions = "SAMEORIGIN"
	}

	// WebSocket 压缩
	globals.wsCompression = !config.WSCompressionDisabled

	if config.Media != nil {
		if config.Media.UseHandler == "" {
			config.Media = nil
		} else {
			globals.maxFileUploadSize = config.Media.MaxFileUploadSize
			if config.Media.Handlers != nil {
				var conf string
				if params := config.Media.Handlers[config.Media.UseHandler]; params != nil {
					conf = string(params)
				}
				if err = store.Store.UseMediaHandler(config.Media.UseHandler, conf); err != nil {
					logs.Err.Fatalf("Failed to init media handler '%s': %s", config.Media.UseHandler, err)
				}
			}
			if config.Media.GcPeriod > 0 && config.Media.GcBlockSize > 0 {
				globals.mediaGcPeriod = time.Second * time.Duration(config.Media.GcPeriod)
				stopFilesGc := largeFileRunGarbageCollection(globals.mediaGcPeriod, config.Media.GcBlockSize)
				defer func() {
					stopFilesGc <- true
					logs.Info.Println("Stopped files garbage collector")
				}()
			}
		}
	}

	// 未验证用户账号垃圾回收
	if config.AccountGC != nil && config.AccountGC.Enabled {
		if config.AccountGC.GcPeriod <= 0 || config.AccountGC.GcBlockSize <= 0 ||
			config.AccountGC.GcMinAccountAge <= 0 {
			logs.Err.Fatalln("Invalid account GC config")
		}
		gcPeriod := time.Second * time.Duration(config.AccountGC.GcPeriod)
		stopAccountGc := garbageCollectUsers(gcPeriod, config.AccountGC.GcBlockSize, config.AccountGC.GcMinAccountAge)

		defer func() {
			stopAccountGc <- true
			logs.Info.Println("Stopped account garbage collector")
		}()
	}

	pushHandlers, err := push.Init(config.Push)
	if err != nil {
		logs.Err.Fatal("Failed to initialize push notifications:", err)
	}
	defer func() {
		push.Stop()
		logs.Info.Println("Stopped push notifications")
	}()
	logs.Info.Println("Push handlers configured:", pushHandlers)

	if err = initVideoCalls(config.WebRTC); err != nil {
		logs.Err.Fatalf("Failed to init video calls: %v", err)
	}

	// 保持非活跃长轮询 Session 15 秒
	globals.sessionStore = NewSessionStore(idleSessionTimeout + 15*time.Second)
	// Hub（主消息路由器）
	globals.hub = newHub()
	// Hub 就绪后启动定时队列扫描；进程退出时显式停止后台 goroutine。
	stopScheduledMessages := scheduledMessagesRun()
	defer func() {
		stopScheduledMessages <- true
		logs.Info.Println("Stopped scheduled messages dispatcher")
	}()

	// 开始接受集群流量
	if globals.cluster != nil {
		globals.cluster.start()
	}

	tlsConfig, err := parseTLSConfig(*tlsEnabled, config.TLS)
	if err != nil {
		logs.Err.Fatalln(err)
	}

	// 初始化插件
	pluginsInit(config.Plugin)

	// 初始化用户缓存
	usersInit()

	// 设置 gRPC 服务器（如果配置了）
	if *listenGrpc == "" {
		*listenGrpc = config.GrpcListen
	}
	if globals.grpcServer, err = serveGrpc(*listenGrpc, config.GrpcKeepalive, tlsConfig); err != nil {
		logs.Err.Fatal(err)
	}

	// 从 -static_data 标志指定的目录服务静态内容，
	// 如果该目录不存在，则假定为 '<当前目录>/static'。内容在
	// 配置中 'static_mount' 指向的路径提供服务。如果缺失，则在根路径 '/' 提供服务
	var staticMountPoint string
	if *staticPath != "" && *staticPath != "-" {
		// 解析静态内容路径
		*staticPath = toAbsolutePath(curwd, *staticPath)
		if _, err = os.Stat(*staticPath); os.IsNotExist(err) {
			logs.Err.Fatal("Static content directory is not found", *staticPath)
		}

		staticMountPoint = config.StaticMount
		if staticMountPoint == "" {
			staticMountPoint = defaultStaticMount
		} else {
			if !strings.HasPrefix(staticMountPoint, "/") {
				staticMountPoint = "/" + staticMountPoint
			}
			if !strings.HasSuffix(staticMountPoint, "/") {
				staticMountPoint += "/"
			}
		}
		mux.Handle(staticMountPoint,
			// 添加可选 Cache-Control 头部
			cacheControlHandler(config.CacheControl,
				// 可选添加 Strict-Transport-Security 和 X-Frame-Options 到响应
				optionalHttpHeaders(
					// 添加 gzip 压缩
					gh.CompressHandler(
						// 添加自定义错误格式化
						httpErrorHandler(
							// 移除挂载点前缀
							http.StripPrefix(staticMountPoint,
								http.FileServer(http.Dir(*staticPath))))))))
		logs.Info.Printf("Serving static content from '%s' at '%s'", *staticPath, staticMountPoint)
	} else {
		logs.Info.Println("Static content is disabled")
	}

	// 配置服务 API 调用的根路径
	if *apiPath != "" {
		config.ApiPath = *apiPath
	}
	if config.ApiPath == "" {
		config.ApiPath = defaultApiPath
	} else {
		if !strings.HasPrefix(config.ApiPath, "/") {
			config.ApiPath = "/" + config.ApiPath
		}
		if !strings.HasSuffix(config.ApiPath, "/") {
			config.ApiPath += "/"
		}
	}
	logs.Info.Printf("API served from root URL path '%s'", config.ApiPath)

	// 确定对外暴露的主端点 URL (globals.servingAt)
	if config.ExtUrl != "" {
		// 优先使用配置的公网/外部 URL（适用于集群、负载均衡器、Unix Domain Socket）
		globals.servingAt = config.ExtUrl
		if !strings.HasPrefix(globals.servingAt, "http://") && !strings.HasPrefix(globals.servingAt, "https://") {
			if tlsConfig != nil {
				globals.servingAt = "https://" + globals.servingAt
			} else {
				globals.servingAt = "http://" + globals.servingAt
			}
		}
		if !strings.HasSuffix(globals.servingAt, "/") {
			globals.servingAt += "/"
		}
	} else if strings.HasPrefix(config.Listen, "unix:") || strings.HasPrefix(config.Listen, "unix://") {
		// 修复通过 Unix Domain Socket 监听时的非标准 URL 拼接问题
		logs.Warn.Printf("Server is listening on Unix Socket '%s'. Using 'localhost' for servingAt. Consider setting 'ext_url' in config for public endpoint.", config.Listen)
		scheme := "http://"
		if tlsConfig != nil {
			scheme = "https://"
		}
		globals.servingAt = scheme + "localhost" + config.ApiPath
	} else {
		// 常规 TCP 监听地址推断
		listenAddr := config.Listen
		if strings.HasPrefix(listenAddr, ":") {
			listenAddr = "localhost" + listenAddr
		}
		scheme := "http://"
		if tlsConfig != nil {
			scheme = "https://"
		}
		globals.servingAt = scheme + listenAddr + config.ApiPath
	}
	logs.Info.Printf("Serving endpoint URL set to '%s'", globals.servingAt)

	sspath := *serverStatusPath
	if sspath == "" {
		sspath = config.ServerStatusPath
	}
	if sspath != "" && sspath != "-" {
		logs.Info.Printf("Server status is available at '%s'", sspath)
		mux.HandleFunc(sspath, serveStatus)
	}

	// 处理 WebSocket 客户端
	mux.HandleFunc(config.ApiPath+"v0/channels", serveWebSocket)
	// 处理长轮询客户端，启用压缩
	mux.Handle(config.ApiPath+"v0/channels/lp", gh.CompressHandler(http.HandlerFunc(serveLongPoll)))
	if config.Media != nil {
		// 处理大文件上传
		mux.Handle(config.ApiPath+"v0/file/u/", gh.CompressHandler(http.HandlerFunc(largeFileReceiveHTTP)))
		// 服务大文件
		mux.Handle(config.ApiPath+"v0/file/s/", gh.CompressHandler(http.HandlerFunc(largeFileServeHTTP)))
		logs.Info.Println("Large media handling enabled", config.Media.UseHandler)
	}

	if staticMountPoint != "/" {
		// 为其他所有 URL 提供 JSON 格式的 404
		mux.HandleFunc("/", serve404)
	}

	if err = listenAndServe(config.Listen, mux, tlsConfig, signalHandler()); err != nil {
		logs.Err.Fatal(err)
	}
}

// stripJSONComments 完成stripJSONComments所需的内部处理。
func stripJSONComments(data []byte) []byte {
	var buf bytes.Buffer
	inString := false
	inCommentSingle := false
	inCommentMulti := false
	escaped := false

	for i := 0; i < len(data); i++ {
		ch := data[i]

		if inCommentSingle {
			if ch == '\n' {
				inCommentSingle = false
				buf.WriteByte(ch)
			}
			continue
		}

		if inCommentMulti {
			if ch == '*' && i+1 < len(data) && data[i+1] == '/' {
				inCommentMulti = false
				i++
			}
			continue
		}

		if inString {
			buf.WriteByte(ch)
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inString = false
			}
			continue
		}

		if ch == '"' {
			inString = true
			buf.WriteByte(ch)
			continue
		}

		if ch == '/' && i+1 < len(data) {
			if data[i+1] == '/' {
				inCommentSingle = true
				i++
				continue
			}
			if data[i+1] == '*' {
				inCommentMulti = true
				i++
				continue
			}
		}

		buf.WriteByte(ch)
	}
	return buf.Bytes()
}
