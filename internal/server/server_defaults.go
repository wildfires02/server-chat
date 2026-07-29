package server

import (
	"time"

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
	_ "chat/server/push/stdout"
	_ "chat/server/push/tnpg"
	_ "chat/server/validate/email"
	_ "chat/server/validate/tel"
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
	defaultStaticPath = "web/static"

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
