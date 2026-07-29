// Package common 提供消息推送实现。
package common

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"chat/server/push"

	"google.golang.org/api/googleapi"
)

// 针对特定通知类型要发送的载荷。
type Payload struct {
	// APNS 和 Android 通用
	Body string `json:"body,omitempty"`
	// Title 保存Title。
	Title string `json:"title,omitempty"`
	// TitleLocKey 保护载荷的并发读写。
	TitleLocKey string `json:"title_loc_key,omitempty"`
	// TitleLocArgs 保存TitleLocArgs列表。
	TitleLocArgs []string `json:"title_loc_args,omitempty"`

	// Android 专用
	BodyLocKey string `json:"body_loc_key,omitempty"`
	// BodyLocArgs 保存BodyLocArgs列表。
	BodyLocArgs []string `json:"body_loc_args,omitempty"`
	// Icon 保存Icon。
	Icon string `json:"icon,omitempty"`
	// Color 保存Color。
	Color string `json:"color,omitempty"`
	// ClickAction 保存ClickAction。
	ClickAction string `json:"click_action,omitempty"`
	// Sound 保存Sound。
	Sound string `json:"sound,omitempty"`
	// Image 保存Image。
	Image string `json:"image,omitempty"`

	// APNS 专用
	Action string `json:"action,omitempty"`
	// ActionLocKey 保护载荷的并发读写。
	ActionLocKey string `json:"action_loc_key,omitempty"`
	// LaunchImage 保存LaunchImage。
	LaunchImage string `json:"launch_image,omitempty"`
	// LocArgs 保存LocArgs列表。
	LocArgs []string `json:"loc_args,omitempty"`
	// LocKey 保护载荷的并发读写。
	LocKey string `json:"loc_key,omitempty"`
	// Subtitle 保存Subtitle。
	Subtitle string `json:"subtitle,omitempty"`
	// SummaryArg 保存SummaryArg。
	SummaryArg string `json:"summary_arg,omitempty"`
	// SummaryArgCount 保存SummaryArg数量。
	SummaryArgCount int `json:"summary_arg_count,omitempty"`
}

// Config 是通知载荷的配置。
type Config struct {
	// Enabled 指示是否启用或满足Enabled。
	Enabled bool `json:"enabled,omitempty"`
	// 所有推送类型的通用默认值。
	Payload
	// 各推送类型的专属配置。
	Msg Payload `json:"msg,omitempty"`
	// Sub 保存订阅。
	Sub Payload `json:"sub,omitempty"`
}

// getStringAttr 查询并返回StringAttr。
func (cp Payload) getStringAttr(field string) string {
	val := reflect.ValueOf(cp).FieldByName(field)
	if !val.IsValid() {
		return ""
	}
	if val.Kind() == reflect.String {
		return val.String()
	}
	return ""
}

// getIntAttr 查询并返回IntAttr。
func (cp Payload) getIntAttr(field string) int {
	val := reflect.ValueOf(cp).FieldByName(field)
	if !val.IsValid() {
		return 0
	}
	if val.Kind() == reflect.Int {
		return int(val.Int())
	}
	return 0
}

// GetStringField 返回 String Field。
func (cc *Config) GetStringField(what, field string) string {
	var val string
	switch what {
	case push.ActMsg:
		val = cc.Msg.getStringAttr(field)
	case push.ActSub:
		val = cc.Sub.getStringAttr(field)
	}
	if val == "" {
		val = cc.Payload.getStringAttr(field)
	}
	return val
}

// GetIntField 返回 Int Field。
func (cc *Config) GetIntField(what, field string) int {
	var val int
	switch what {
	case push.ActMsg:
		val = cc.Msg.getIntAttr(field)
	case push.ActSub:
		val = cc.Sub.getIntAttr(field)
	}
	if val == 0 {
		val = cc.Payload.getIntAttr(field)
	}
	return val
}

// AndroidVisibilityType 定义通知可见性常量
// https://developer.android.com/reference/android/app/Notification.html#visibility
type AndroidVisibilityType string

const (
	// AndroidVisibilityUnspecified 如果未指定，默认为 `Visibility.PRIVATE`。
	AndroidVisibilityUnspecified AndroidVisibilityType = "VISIBILITY_UNSPECIFIED"

	// AndroidVisibilityPrivate 在所有锁屏界面上显示此通知，但在安全锁屏界面上隐藏敏感或私密信息。
	AndroidVisibilityPrivate AndroidVisibilityType = "PRIVATE"

	// AndroidVisibilityPublic 在所有锁屏界面上完整显示此通知。
	AndroidVisibilityPublic AndroidVisibilityType = "PUBLIC"

	// AndroidVisibilitySecret 在安全锁屏界面上不显示此通知的任何内容。
	AndroidVisibilitySecret AndroidVisibilityType = "SECRET"
)

// AndroidNotificationPriorityType 定义客户端收到通知后消费的通知优先级。
// 不影响 FCM 发送。
type AndroidNotificationPriorityType string

const (
	// 如果未指定优先级，通知优先级设为 `PRIORITY_DEFAULT`。
	AndroidNotificationPriorityUnspecified AndroidNotificationPriorityType = "PRIORITY_UNSPECIFIED"

	// 最低通知优先级。具有此 `PRIORITY_MIN` 的通知可能不会显示给用户，
	// 除非在特殊情况下，例如详细的通知日志。
	AndroidNotificationPriorityMin AndroidNotificationPriorityType = "PRIORITY_MIN"

	// 较低通知优先级。与 `PRIORITY_DEFAULT` 的通知相比，
	// UI 可能会选择以更小的尺寸或在列表中的不同位置显示这些通知。
	AndroidNotificationPriorityLow AndroidNotificationPriorityType = "PRIORITY_LOW"

	// 默认通知优先级。如果应用程序未对自己的通知进行优先级排序，
	// 请对所有通知使用此值。
	AndroidNotificationPriorityDefault AndroidNotificationPriorityType = "PRIORITY_DEFAULT"

	// 较高通知优先级。用于更重要的通知或提醒。
	// 与 `PRIORITY_DEFAULT` 的通知相比，UI 可能会选择以更大的尺寸或在通知列表中的不同位置显示这些通知。
	AndroidNotificationPriorityHigh AndroidNotificationPriorityType = "PRIORITY_HIGH"

	// 最高通知优先级。用于应用程序中最重要的事项，
	// 需要用户立即关注或输入。
	AndroidNotificationPriorityMax AndroidNotificationPriorityType = "PRIORITY_MAX"
)

// AndroidPriorityType 定义服务器端优先级 https://goo.gl/GjONJv。它影响 FCM 发送推送的速度。
type AndroidPriorityType string

const (
	// 数据消息的默认优先级。普通优先级消息不会在休眠设备上打开网络连接，
	// 其传递可能会延迟以节省电池。对于时效性较低的消息，
	// 如新邮件或其他数据同步通知，请选择普通传递优先级。
	AndroidPriorityNormal AndroidPriorityType = "NORMAL"

	// 通知消息的默认优先级。FCM 尝试立即发送高优先级消息，
	// 允许 FCM 服务在可能时唤醒休眠设备并打开网络连接连接到您的应用服务器。
	// 例如，具有即时消息、聊天或语音通话提醒的应用通常需要打开网络连接，
	// 并确保 FCM 将消息无延迟地传递到设备。
	// 如果消息时间紧迫且需要用户立即交互，请设置高优先级，
	// 但请注意，与正常优先级消息相比，将消息设置为高优先级会更多地消耗电池。
	AndroidPriorityHigh AndroidPriorityType = "HIGH"
)

// InterruptionLevelType 定义 APNS 载荷中 aps.InterruptionLevel 的值。
type InterruptionLevelType string

const (
	// InterruptionLevelPassive 用于表示通知以被动方式传递。
	InterruptionLevelPassive InterruptionLevelType = "passive"

	// InterruptionLevelActive 用于表示通知的重要性和传递时机。
	InterruptionLevelActive InterruptionLevelType = "active"

	// InterruptionLevelTimeSensitive 用于表示通知的重要性和传递时机。
	InterruptionLevelTimeSensitive InterruptionLevelType = "time-sensitive"

	// InterruptionLevelCritical 用于表示通知的重要性和传递时机。
	// 此中断级别需要 Apple 批准的授权。
	// 参见：https://developer.apple.com/documentation/usernotifications/unnotificationinterruptionlevel/
	InterruptionLevelCritical InterruptionLevelType = "critical"
)

const (
	// 规范 UUID，用于标识通知。如果发送通知时出现错误，
	// APNs 使用此值向您的服务器标识该通知。
	// 规范格式为 32 个小写十六进制数字，分为五组，以连字符分隔，
	// 格式为 8-4-4-4-12。UUID 示例如下：123e4567-e89b-12d3-a456-42665544000
	// 如果省略此头部，APNs 将创建一个新的 UUID 并在响应中返回。
	HeaderApnsID = "apns-id"

	// 以秒为单位的 UNIX 纪元日期（UTC）。此头部标识通知不再有效
	// 可以被丢弃的日期。
	// 如果此值非零，APNs 会存储通知并尝试至少传递一次，
	// 如果第一次无法传递则根据需要重复尝试。如果值为 0，
	// APNs 将通知视为立即过期，不会存储通知也不会尝试重新传递。
	HeaderApnsExpiration = "apns-expiration"

	// 通知的优先级。请指定以下值之一：
	// 10–立即发送推送消息。具有此优先级的通知必须在目标设备上触发提醒、声音或徽章。
	// 对于仅包含 content-available 键的推送通知，使用此优先级是错误的。
	// 5—在考虑设备功耗的时间发送推送消息。
	// 具有此优先级的通知可能会被分组并批量传递。它们会被限速，在某些情况下可能不会传递。
	// 如果省略此头部，APNs 服务器将优先级设为 10。
	HeaderApnsPriority = "apns-priority"

	// 远程通知的 Topic，通常为您的应用包 ID。
	// 您在开发者账户中创建的证书必须包含此 Topic 的能力。
	// 如果您的证书包含多个 Topic，则必须指定此头部的值。
	// 如果省略此请求头部且您的 APNs 证书未指定多个 Topic，
	// APNs 服务器将证书的 Subject 作为默认 Topic。
	// 如果您使用提供者令牌而非证书，则必须指定此请求头部的值。
	// 您提供的 Topic 应为您开发者账户中团队名称的配置。
	HeaderApnsTopic = "apns-topic"

	// 具有相同折叠标识符的多个通知会作为单个通知显示给用户。
	// 此键的值不得超过 64 字节。更多信息请参见 Quality of Service、
	// 存储与转发以及合并通知。
	HeaderApnsCollapseID = "apns-collapse-id"

	// 此头部的值必须准确反映通知载荷的内容。
	// 如果不匹配或在必需系统上缺少此头部，APNs 可能返回错误、
	// 延迟传递通知或完全丢弃通知。
	HeaderApnsPushType = "apns-push-type"
)

// ApnsPushTypeType 表示ApnsPushTypeType领域值。
type ApnsPushTypeType string

const (
	// 对触发用户交互的通知使用 alert 推送类型，例如提醒、徽章或声音。
	// 如果设置此推送类型，apns-Topic 头部字段必须使用您的应用包 ID 作为 Topic。
	// 更多信息请参见生成远程通知。
	// 如果通知需要用户立即操作，将通知优先级设为 10；否则使用 5。
	ApnsPushTypeAlert ApnsPushTypeType = "alert"

	// 对后台传递内容且不触发任何用户交互的通知使用 background 推送类型。
	// 如果设置此推送类型，apns-Topic 头部字段必须使用您的应用包 ID 作为 Topic。始终使用优先级 5。
	// 使用优先级 10 是错误的。更多信息请参见推送后台更新到您的应用。
	ApnsPushTypeBackground ApnsPushTypeType = "background"

	// 对请求用户位置的通知使用 location 推送类型。如果设置此推送类型，
	// apns-Topic 头部字段必须使用您的应用包 ID 并在末尾附加 .location-query。
	// 如果位置查询需要位置推送服务扩展的立即响应，将 apns-priority 设为 10；
	// 否则使用 5。location 推送类型仅支持基于令牌的认证。
	ApnsPushTypeLocation ApnsPushTypeType = "location"

	// 对提供传入 VoIP 通话信息的通知使用 voip 推送类型。
	// 更多信息请参见响应来自 PushKit 的 VoIP 通知。
	// 如果设置此推送类型，apns-Topic 头部字段必须使用您的应用包 ID 并在末尾附加 .voip。
	// 如果您使用基于证书的认证，还必须为 VoIP 服务注册证书。
	// 此时 Topic 是 1.2.840.113635.100.6.3.4 或 1.2.840.113635.100.6.3.6 扩展的一部分。
	ApnsPushTypeVoip ApnsPushTypeType = "voip"

	// 使用 fileprovider 推送类型向文件提供者扩展发出变更信号。如果设置此推送类型，
	// apns-Topic 头部字段必须使用您的应用包 ID 并在末尾附加 .pushkit.fileprovider。
	// 更多信息请参见使用推送通知发出变更信号。
	ApnsPushTypeFileprovider ApnsPushTypeType = "fileprovider"
)

// Aps 是 APNS 载荷。说明请参见：
// https://developer.apple.com/documentation/usernotifications/setting_up_a_remote_notification_server/generating_a_remote_notification#2943363
type Aps struct {
	// Alert 保存Alert。
	Alert *ApsAlert `json:"alert,omitempty"`
	// Badge 保存Badge。
	Badge int `json:"badge,omitempty"`
	// Category 保存Category。
	Category string `json:"category,omitempty"`
	// ContentAvailable 保存正文Available。
	ContentAvailable int `json:"content-available,omitempty"`
	// InterruptionLevel 保存InterruptionLevel。
	InterruptionLevel InterruptionLevelType `json:"interruption-level,omitempty"`
	// MutableContent 保存Mutable正文。
	MutableContent int `json:"mutable-content,omitempty"`
	// RelevanceScore 保存RelevanceScore。
	RelevanceScore any `json:"relevance-score,omitempty"`
	// Sound 保存Sound。
	Sound any `json:"sound,omitempty"`
	// ThreadID 保存Thread标识。
	ThreadID string `json:"thread-id,omitempty"`
	// URLArgs 保存URLArgs列表。
	URLArgs []string `json:"url-args,omitempty"`
}

// ApsAlert 是 aps.Alert 字段的内容。
type ApsAlert struct {
	// Action 保存Action。
	Action string `json:"action,omitempty"`
	// ActionLocKey 保护ApsAlert的并发读写。
	ActionLocKey string `json:"action-loc-key,omitempty"`
	// Body 保存Body。
	Body string `json:"body,omitempty"`
	// LaunchImage 保存LaunchImage。
	LaunchImage string `json:"launch-image,omitempty"`
	// LocArgs 保存LocArgs列表。
	LocArgs []string `json:"loc-args,omitempty"`
	// LocKey 保护ApsAlert的并发读写。
	LocKey string `json:"loc-key,omitempty"`
	// Title 保存Title。
	Title string `json:"title,omitempty"`
	// Subtitle 保存Subtitle。
	Subtitle string `json:"subtitle,omitempty"`
	// TitleLocArgs 保存TitleLocArgs列表。
	TitleLocArgs []string `json:"title-loc-args,omitempty"`
	// TitleLocKey 保护ApsAlert的并发读写。
	TitleLocKey string `json:"title-loc-key,omitempty"`
	// SummaryArg 保存SummaryArg。
	SummaryArg string `json:"summary-arg,omitempty"`
	// SummaryArgCount 保存SummaryArg数量。
	SummaryArgCount int `json:"summary-arg-count,omitempty"`
}

// FCM 错误码
const (
	// 没有关于此错误的更多信息。
	ErrorUnspecified = "UNSPECIFIED_ERROR"

	// 请求参数无效（HTTP 错误码 = 400）。返回 google.rpc.BadRequest 类型的扩展
	// 以指定哪个字段无效。
	// 可能原因：
	// - 注册无效：检查您传递给服务器的注册令牌格式。确保它与
	//   客户端应用从 Firebase Notifications 注册中获取的注册令牌匹配。
	//   不要截断或添加额外字符。
	// - 包名无效：确保消息寻址到的注册令牌的包名与
	//   请求中传递的值匹配。
	// - 消息过大：检查消息中包含的载荷数据总大小不超过 FCM 限制：
	//   大多数消息为 4096 字节，Topic 消息为 2048 字节。包括键和值。
	// - 数据键无效：检查载荷数据不包含 FCM 内部使用的键
	//   （如 from、gcm 或任何以 google 为前缀的值）。
	//   请注意，某些词（如 collapse_key）也由 FCM 使用，但
	//   允许在载荷中使用，此时载荷值将被 FCM 值覆盖。
	// - TTL 无效：检查 ttl 中使用的值为整数，表示以秒为单位的持续时间，介于 0 和
	//   2,419,200（4 周）之间。
	// - 参数无效：检查提供的参数是否具有正确的名称和类型。
	ErrorInvalidArgument = "INVALID_ARGUMENT"

	// 应用实例已从 FCM 取消注册（HTTP 错误码 = 404）。这通常意味着使用的令牌不再有效，
	// 必须使用新令牌。
	// 此错误可能由缺少注册令牌或未注册令牌引起。
	// - 缺少注册：如果消息的目标是令牌值，检查请求是否包含注册令牌。
	// - 未注册：现有注册令牌可能在多种场景下失效，包括：
	//   - 如果客户端应用向 FCM 取消注册。
	//   - 如果客户端应用被自动取消注册，可能在用户卸载应用时发生。
	//     例如，在 iOS 上，如果 APNS 反馈服务报告 APNS 令牌无效。
	//   - 如果注册令牌过期（例如，Google 可能决定刷新注册令牌，
	//     或 iOS 设备的 APNS 令牌已过期）。
	//   - 如果客户端应用已更新但新版本未配置为接收消息。
	// 对于所有这些情况，从应用服务器中移除此注册令牌并停止使用它发送消息。
	ErrorUnregistered = "UNREGISTERED"

	// 已认证的发送者 ID 与注册令牌的发送者 ID 不同（HTTP 错误码 = 403）。
	// 注册令牌与特定的发送者组绑定。当客户端应用注册 FCM 时，必须指定
	// 允许哪些发送者发送消息。发送消息到客户端应用时应使用这些发送者 ID 之一。
	// 如果切换到不同的发送者，现有注册令牌将无法工作。
	ErrorSenderIDMismatch = "SENDER_ID_MISMATCH"

	// 消息目标的发送限制已超出（HTTP 错误码 = 429）。返回 google.rpc.QuotaFailure 类型的扩展
	// 以指定哪个配额已超出。此错误可能由超出消息速率配额、
	// 超出设备消息速率配额或超出 Topic 消息速率配额引起。
	// - 消息速率已超出：消息发送速率过高。减少发送的消息数量并使用
	//   指数退避重试发送。
	// - 设备消息速率已超出：向特定设备发送消息的速率过高。如果 iOS 应用发送
	//   消息的速率超过 APNs 限制，可能会收到此错误消息。减少向该设备发送的
	//   消息数量并使用指数退避重试发送。
	// - Topic 消息速率已超出：向特定 Topic 订阅者发送消息的速率过高。
	//   减少此 Topic 的发送消息数量并使用指数退避重试发送。
	ErrorQuotaExceeded = "QUOTA_EXCEEDED"

	// 服务器过载（HTTP 错误码 = 503）。服务器无法及时处理请求。重试
	// 相同请求，但您必须：
	// - 如果 FCM 连接服务器响应中包含 Retry-After 头部，请遵守它。
	// - 在重试机制中实现指数退避。（例如，如果第一次重试前等待了一秒，
	//   则下一次至少等待两秒，然后 4 秒，依此类推）。如果您发送多条消息，
	//   独立地为每条消息延迟额外的随机时间，以避免同时发出所有消息的新请求。
	//   造成问题的发送者可能会被列入黑名单。
	ErrorUnavailable = "UNAVAILABLE"

	// 发生未知的内部错误（HTTP 错误码 = 500）。服务器在尝试处理请求时遇到错误。
	// 您可以按照“超时”中列出的要求重试相同请求（见上一行）。
	// 如果错误持续存在，请联系 Firebase 支持。
	ErrorInternal = "INTERNAL"

	// APNs 证书或 Web 推送认证密钥无效或缺失（HTTP 错误码 = 401）。
	// 无法发送针对 iOS 设备或 Web 推送注册的消息。检查您的开发和生产凭据的有效性。
	ErrorThirdPartyAuth = "THIRD_PARTY_AUTH_ERROR"
)

// APNS 错误消息
const (
	// 折叠标识符超出最大允许大小（HTTP 错误码 = 400）。
	ErrorApnsBadCollapseId = "BadCollapseId"

	// 指定的设备令牌无效。验证请求是否包含有效令牌且
	// 令牌与环境匹配（HTTP 错误码 = 400）。
	ErrorApnsBadDeviceToken = "BadDeviceToken"

	// apns-expiration 值无效（HTTP 错误码 = 400）。
	ErrorApnsBadExpirationDate = "BadExpirationDate"

	// apns-id 值无效（HTTP 错误码 = 400）。
	ErrorApnsBadMessageId = "BadMessageId"

	// apns-priority 值无效（HTTP 错误码 = 400）。
	ErrorApnsBadPriority = "BadPriority"

	// apns-Topic 无效（HTTP 错误码 = 400）。
	ErrorApnsBadTopic = "BadTopic"

	// 设备令牌与指定 Topic 不匹配（HTTP 错误码 = 400）。
	ErrorApnsDeviceTokenNotForTopic = "DeviceTokenNotForTopic"

	// 一个或多个头部被重复（HTTP 错误码 = 400）。
	ErrorApnsDuplicateHeaders = "DuplicateHeaders"

	// 空闲超时（HTTP 错误码 = 400）。
	ErrorApnsIdleTimeout = "IdleTimeout"

	// 请求 :path 中未指定设备令牌。验证 :path 头部
	// 包含设备令牌（HTTP 错误码 = 400）。
	ErrorApnsMissingDeviceToken = "MissingDeviceToken"

	// 请求的 apns-Topic 头部未指定但为必需。
	// 当客户端使用支持多个 Topic 的证书连接时，
	// apns-Topic 头部是强制性的（HTTP 错误码 = 400）。
	ErrorApnsMissingTopic = "MissingTopic"

	// 消息载荷为空（HTTP 错误码 = 400）。
	ErrorApnsPayloadEmpty = "PayloadEmpty"

	// 不允许推送到此 Topic（HTTP 错误码 = 400）。
	ErrorApnsTopicDisallowed = "TopicDisallowed"

	// 证书无效（HTTP 错误码 = 403）。
	ErrorApnsBadCertificate = "BadCertificate"

	// 客户端证书用于错误的环境（HTTP 错误码 = 403）。
	ErrorApnsBadCertificateEnvironment = "BadCertificateEnvironment"

	// 提供者令牌已过期，应生成新令牌（HTTP 错误码 = 403）。
	ErrorApnsExpiredProviderToken = "ExpiredProviderToken"

	// 不允许指定的操作（HTTP 错误码 = 403）。
	ErrorApnsForbidden = "Forbidden"

	// 提供者令牌无效或令牌签名无法验证（HTTP 错误码 = 403）。
	ErrorApnsInvalidProviderToken = "InvalidProviderToken"

	// 未使用提供者证书连接 APNs，且 Authorization 头部缺失
	// 或未指定提供者令牌（HTTP 错误码 = 403）。
	ErrorApnsMissingProviderToken = "MissingProviderToken"

	// 请求包含错误的 :path 值（HTTP 错误码 = 404）。
	ErrorApnsBadPath = "BadPath"

	// 指定的 :method 不是 POST（HTTP 错误码 = 405）。
	ErrorApnsMethodNotAllowed = "MethodNotAllowed"

	// 设备令牌对指定 Topic 无效（HTTP 错误码 = 410）。
	ErrorApnsUnregistered = "Unregistered"

	// 消息载荷过大。请参阅创建远程通知载荷
	// 了解最大载荷大小的详细信息（HTTP 错误码 = 413）。
	ErrorApnsPayloadTooLarge = "PayloadTooLarge"

	// 提供者令牌更新过于频繁（HTTP 错误码 = 429）。
	ErrorApnsTooManyProviderTokenUpdates = "TooManyProviderTokenUpdates"

	// 向同一设备令牌发送了过多连续请求（HTTP 错误码 = 429）。
	ErrorApnsTooManyRequests = "TooManyRequests"

	// 发生内部服务器错误（HTTP 错误码 = 500）。
	ErrorApnsInternalServerError = "InternalServerError"

	// 服务不可用（HTTP 错误码 = 503）。
	ErrorApnsServiceUnavailable = "ServiceUnavailable"

	// 服务器正在关闭（HTTP 错误码 = 503）。
	ErrorApnsShutdown = "Shutdown"
)

// GApiError 存储 Google API 调用返回的错误的简化表示。
type GApiError struct {
	// HttpCode 保存HTTPCode。
	HttpCode int
	// 此字段信息量不大，但可以记录供用户参考。
	ErrMessage string
	// FCM 错误码，信息丰富但可能缺失。
	FcmErrCode string
	// 根据 fcmErrCode 的不同而不同的扩展错误信息。
	ExtendedInfo string
}

// DecodeGoogleApiError 将非常复杂的 googleapi.Error 转换为更易管理的结构。
func DecodeGoogleApiError(err error) (decoded *GApiError, errs []error) {
	decoded = &GApiError{}
	if gerr, ok := err.(*googleapi.Error); ok {
		// HTTP 状态码。
		decoded.HttpCode = gerr.Code
		decoded.ErrMessage = gerr.Message
		for _, errInfo := range gerr.Errors {
			decoded.ErrMessage += "; " + errInfo.Reason + "/" + errInfo.Message
		}

		// 解码 FCM 错误。
		for _, iface := range gerr.Details {
			details, ok := iface.(map[string]any)
			if !ok {
				errs = append(errs, fmt.Errorf("error.Details unrecognized format %T", iface))
				continue
			}
			switch details["@type"] {
			case "type.googleapis.com/google.firebase.fcm.v1.FcmError":
				if errCode, ok := details["errorCode"].(string); ok {
					if decoded.FcmErrCode != "" {
						// 此情况尚未观察到，但 FCM 未明确是否可能发生。
						errs = append(errs, fmt.Errorf("multiple FcmError codes '%s', '%s'", errCode, decoded.FcmErrCode))
					} else {
						decoded.FcmErrCode = errCode
					}
				} else {
					errs = append(errs, fmt.Errorf("error.Details errorCode is not a string: %T", details["errorCode"]))
				}
			case "type.googleapis.com/google.rpc.BadRequest":
				// dst.fcmErrCode == INVALID_ARGUMENT
				if fieldViolations, ok := details["fieldViolations"].([]any); !ok {
					errs = append(errs, fmt.Errorf("wrong type of error.Details 'fieldViolations': %T", details["fieldViolations"]))
				} else {
					var fields []string
					for _, violationIface := range fieldViolations {
						if violation, ok := violationIface.(map[string]any); !ok {
							errs = append(errs, fmt.Errorf("wrong type of error.Details.fieldViolations item: %T", iface))
						} else if field, ok := violation["field"].(string); ok && field != "" {
							fields = append(fields, field)
						} else {
							errs = append(errs, fmt.Errorf("error.Details 'fieldViolation' has no 'field': %T, %s", violation["field"], violation["description"]))
						}
					}
					decoded.ExtendedInfo = strings.Join(fields, ",")
				}
			case "type.googleapis.com/google.rpc.QuotaFailure":
				if decoded.FcmErrCode == "" {
					decoded.FcmErrCode = string(ErrorQuotaExceeded)
				}
				if violations, ok := details["violations"].([]any); !ok {
					errs = append(errs, fmt.Errorf("wrong type of error.Details 'violations': %T", details["violations"]))
				} else {
					var info []string
					for _, item := range violations {
						if v, ok := item.(map[string]any); ok {
							subject, _ := v["subject"].(string)
							desc, _ := v["description"].(string)
							if subject != "" || desc != "" {
								info = append(info, fmt.Sprintf("%s: %s", subject, desc))
							}
						} else {
							errs = append(errs, fmt.Errorf("wrong type of error.Details.violations item: %T", item))
						}
					}
					if len(info) > 0 {
						decoded.ExtendedInfo = strings.Join(info, "; ")
					}
				}
			default:
				errs = append(errs, fmt.Errorf("unknown error '@type': %v", details))
			}
		}
	} else {
		decoded.HttpCode = http.StatusBadRequest
		decoded.ErrMessage = err.Error()
		errs = append(errs, fmt.Errorf("not googleapi.Error %w", err))
	}

	if decoded.FcmErrCode == "" {
		decoded.FcmErrCode = string(ErrorUnspecified)
	}

	return
}
