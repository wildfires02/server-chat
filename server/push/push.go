// Package push 定义了移动端推送通知 (Push Notifications) 插件的核心接口及模型。
package push

import (
	"encoding/json"
	"errors"
	"time"

	"chat/server/logs"
	t "chat/server/store/types"
)

// 推送动作类型常量定义
const (
	// 新消息推送 (msg)
	ActMsg = "msg"
	// 新订阅变更推送 (sub)
	ActSub = "sub"
	// 消息已读：重置清除未读消息计数 (read)
	ActRead = "read"
)

// MaxPayloadLength 推送消息载荷文本的最大多字节字符长度限制
const MaxPayloadLength = 128

// Recipient 接收推送通知的目标用户结构体。
type Recipient struct {
	// 当服务器推送数据包发出时，用户处于在线连接状态的连接数量
	Delivered int `json:"delivered"`
	// 数据包已送达的用户设备 Token 列表（如果已知）
	Devices []string `json:"devices,omitempty"`
	// 包含在推送消息中的未读消息数
	Unread int `json:"unread"`
	// 标识在发送推送前是否需要在缓存中增加未读计数
	ShouldIncrementUnreadCountInCache bool `json:"-"`
}

// Receipt 包含目标接收者列表的推送数据回执载荷。
type Receipt struct {
	// 接收者列表映射，包含未接收到消息的用户
	To map[t.Uid]Recipient `json:"to"`
	// 群组通知的 Channel/Topic 频道
	Channel string `json:"channel"`
	// 实际送达到客户端的内容载荷
	Payload Payload `json:"payload"`
}

// ChannelReq 设备 Token 订阅/取消订阅 FCM 频道的请求结构体。
type ChannelReq struct {
	// 发起请求的用户 UID
	Uid t.Uid
	// 单个设备订阅所有频道时的设备 Token
	DeviceID string
	// 要订阅或取消订阅的 Channel 频道
	Channel string
	// Unsub 设为 true 表示取消订阅设备，否则表示订阅设备
	Unsub bool
}

// Payload 推送的具体内容载荷数据。
type Payload struct {
	// 推送动作类型：新消息 (msg)、新订阅 (sub) 等
	What string `json:"what"`
	// 是否为静默推送（只更新数据，不弹出客户端通知栏提示）
	Silent bool `json:"silent"`
	// 受动作影响的 Topic 名称
	Topic string `json:"topic"`
	// 动作发生的时间戳
	Timestamp time.Time `json:"ts"`

	// {data} 消息通知字段：

	// 消息发送者 'usrXXX'
	From string `json:"from"`
	// 消息递增 Seq ID
	SeqId int `json:"seq"`
	// 消息内容 MIME 类型，如 text/x-drafty 或 text/plain
	ContentType string `json:"mime"`
	// 消息实际的数据内容 (Data.Content)
	Content any `json:"content,omitempty"`
	// 视频通话状态（仅在音视频通话消息中有效）
	Webrtc string `json:"webrtc,omitempty"`
	// 通话是否仅包含音频
	AudioOnly bool `json:"aonly,omitempty"`
	// 消息替换的目标 Seq ID
	Replace string `json:"replace,omitempty"`

	// 订阅变更通知字段：

	// 订阅状态变更通知时的最新权限模式
	ModeWant t.AccessMode `json:"want,omitempty"`
	// ModeGiven 保存访问模式Given。
	ModeGiven t.AccessMode `json:"given,omitempty"`
}

// Handler 所有推送提供者插件（如 FCM、TNPG、Stdout 等）必须实现的接口。
type Handler interface {
	// Init 初始化推送处理器
	Init(jsonconf json.RawMessage) (bool, error)

	// IsReady 检查推送处理器是否已完成初始化并就绪
	IsReady() bool

	// Push 返回服务器用于写入推送回执数据的 Channel
	Push() chan<- *Receipt

	// Channel 返回用于订阅/取消订阅 FCM Topic 频道的 Channel
	Channel() chan<- *ChannelReq

	// Stop 终止推送处理器的 Worker 协程并停止发送推送
	Stop()
}

// configType 保存配置Type的数据和运行状态。
type configType struct {
	// Name 保存名称。
	Name string `json:"name"`
	// Config 保存配置。
	Config json.RawMessage `json:"config"`
}

// handlers 保存handlers的共享实例或运行状态。
var handlers map[string]Handler

// Register 注册推送处理器
func Register(name string, hnd Handler) {
	if handlers == nil {
		handlers = make(map[string]Handler)
	}

	if hnd == nil {
		panic("Register: push handler 实例不能为 nil")
	}
	if _, dup := handlers[name]; dup {
		panic("Register: 重复注册推送处理器 " + name)
	}
	handlers[name] = hnd
}

// Init 初始化所有已注册的推送处理器。
func Init(jsconfig json.RawMessage) ([]string, error) {
	var config []configType

	if err := json.Unmarshal(jsconfig, &config); err != nil {
		return nil, errors.New("解析推送配置失败: " + err.Error())
	}

	var enabled []string
	for _, cc := range config {
		if hnd := handlers[cc.Name]; hnd != nil {
			if ok, err := hnd.Init(cc.Config); err != nil {
				return nil, err
			} else if ok {
				enabled = append(enabled, cc.Name)
			}
		}
	}

	return enabled, nil
}

// Push 向用户设备发送单条推送消息。
func Push(msg *Receipt) {
	if handlers == nil {
		return
	}

	for _, hnd := range handlers {
		if !hnd.IsReady() {
			continue
		}

		if durable, ok := hnd.(DurableEnqueuer); ok {
			if err := durable.Enqueue(msg); err != nil {
				logs.Warn.Println("持久化推送任务失败，降级为内存投递:", err)
				select {
				case hnd.Push() <- msg:
				default:
					logs.Err.Println("推送任务未能进入持久队列或内存队列")
				}
			}
			continue
		}

		select {
		case hnd.Push() <- msg:
		default:
			logs.Warn.Println("推送处理器内存队列已满")
		}
	}
}

// ChannelSub 处理设备的 Channel (FCM Topic) 订阅/取消订阅请求。
func ChannelSub(msg *ChannelReq) {
	if handlers == nil {
		return
	}

	for _, hnd := range handlers {
		if !hnd.IsReady() {
			continue
		}

		select {
		case hnd.Channel() <- msg:
		default:
		}
	}
}

// Stop 停止所有推送服务
func Stop() {
	if handlers == nil {
		return
	}

	for _, hnd := range handlers {
		if hnd.IsReady() {
			hnd.Stop()
		}
	}
}
