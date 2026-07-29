// Package main 实现即时通信服务端的协议、路由和业务逻辑。
package main

/******************************************************************************
 *
 *  描述 :
 *
 *    通信协议（Wire Protocol）数据模型结构体定义。
 *
 *****************************************************************************/

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"chat/server/store/types"
)

// MsgGetOpts 定义 Get 查询参数。
type MsgGetOpts struct {
	// 可选的用户 ID，用于仅返回指定用户的结果。
	User string `json:"user,omitempty"`
	// 可选的 Topic 名称，用于仅返回指定 Topic 的结果。
	Topic string `json:"topic,omitempty"`
	// 返回在此时间戳之后修改过的结果。
	IfModifiedSince *time.Time `json:"ims,omitempty"`
	// 加载 ID 大于或等于此值的消息/区间（包含）。
	SinceId int `json:"since,omitempty"`
	// 加载 ID 小于此值的消息/区间（不包含）。
	BeforeId int `json:"before,omitempty"`
	// 限制加载的消息数量。
	Limit int `json:"limit,omitempty"`
	// 获取指定 ID 区间范围内的消息。
	IdRanges []MsgRange `json:"ranges,omitempty"`
	// 按 SeqId 升序返回消息。用于从 SinceId 开始进行无间隙的断线追赶。
	Forward bool `json:"forward,omitempty"`
}

// MsgGetQuery 表示 Topic 元数据或消息数据的获取查询请求。
type MsgGetQuery struct {
	// What 保存What时间。
	What string `json:"what"`

	// "desc" 描述查询参数: IfModifiedSince
	Desc *MsgGetOpts `json:"desc,omitempty"`
	// "sub" 订阅关系查询参数: User, Topic, IfModifiedSince, Limit
	Sub *MsgGetOpts `json:"sub,omitempty"`
	// "data" 消息数据查询参数: Since, Before, Limit, IfModifiedSince
	Data *MsgGetOpts `json:"data,omitempty"`
	// "del" 删除记录查询参数: Since, Before, Limit
	Del *MsgGetOpts `json:"del,omitempty"`
	// "search" 关键词发现或当前 Topic 消息全文搜索参数。
	Search *MsgSearchOpts `json:"search,omitempty"`
}

// MsgSearchOpts 定义 Peer 发现与会话内消息搜索参数。
type MsgSearchOpts struct {
	// Query 是用户输入的关键词。
	Query string `json:"q"`
	// Scope 可取 peers 或 topic；在 fnd Topic 上默认为 peers，其它 Topic 默认为 topic。
	Scope string `json:"scope,omitempty"`
	// From 仅返回该用户发送的消息，仅适用于 topic 范围。
	From string `json:"from,omitempty"`
	// Kinds 仅返回指定的服务端消息类型，仅适用于 topic 范围。
	Kinds []string `json:"kinds,omitempty"`
	// MinDate 仅返回该时间点及之后创建的消息。
	MinDate *time.Time `json:"min_date,omitempty"`
	// MaxDate 仅返回该时间点之前创建的消息。
	MaxDate *time.Time `json:"max_date,omitempty"`
	// Cursor 是上一页返回的不透明分页游标。
	Cursor string `json:"cursor,omitempty"`
	// Limit 是本页最多返回的结果数量。
	Limit int `json:"limit,omitempty"`
}

// MsgSetSub 在 {set.sub} 请求中用于更新当前订阅或邀请其他用户。
type MsgSetSub struct {
	// 受此请求影响的目标用户。默认为空（即当前用户）。
	User string `json:"user,omitempty"`

	// 访问权限模式变更（Given 或 Want）
	Mode string `json:"mode,omitempty"`

	// 面向业务层的成员角色。设置他人角色时可使用 admin、member、
	// readonly、banned；广播频道还支持 publisher、subscriber。
	// Role 与 Mode 互斥，服务端会把角色转换为受约束的 ACL。
	Role string `json:"role,omitempty"`
}

// MsgSetDesc 是在 set.what == "desc" 时用于更新属性的结构体。
type MsgSetDesc struct {
	// 默认访问权限模式。
	DefaultAcs *MsgDefaultAcsMode `json:"defacs,omitempty"`
	// 用户或 Topic 的公开描述信息。
	Public any `json:"public,omitempty"`
	// 系统提供的受信任数据（Trusted）。
	Trusted any `json:"trusted,omitempty"`
	// 每个订阅专属的私有数据（Private）。
	Private any `json:"private,omitempty"`
}

// MsgCredClient 表示账号凭证信息（如邮箱或手机号）。
type MsgCredClient struct {
	// 凭证类型，例如 `email` 或 `tel`。
	Method string `json:"meth,omitempty"`
	// 待验证的值，例如 `user@example.com` 或 `+18003287448`
	Value string `json:"val,omitempty"`
	// 验证响应码/验证码
	Response string `json:"resp,omitempty"`
	// 请求附加参数（如偏好设置），直接透传给验证器
	Params map[string]any `json:"params,omitempty"`
}

// MsgSetQuery 表示对 Topic 或用户元数据的更新请求（包含描述、订阅、标签、凭证）。
type MsgSetQuery struct {
	// Topic/用户描述信息（创建新对象或新订阅时使用）
	Desc *MsgSetDesc `json:"desc,omitempty"`
	// 订阅参数
	Sub *MsgSetSub `json:"sub,omitempty"`
	// 用于用户发现的可检索标签列表
	Tags []string `json:"tags,omitempty"`
	// 账号凭证更新
	Cred *MsgCredClient `json:"cred,omitempty"`
	// 辅助数据更新
	Aux map[string]any
}

// MsgRange 表示单个 ID (HiId=0) 或一个连续的 ID 范围 [LowId .. HiId)（左闭右开）。
type MsgRange struct {
	// LowId 保存Low标识。
	LowId int `json:"low,omitempty"`
	// HiId 保存Hi标识。
	HiId int `json:"hi,omitempty"`
}

/****************************************************************
 * 客户端发往服务端 (C2S) 的消息结构定义。
 ****************************************************************/

// MsgClientHi 表示握手消息 {hi}。
type MsgClientHi struct {
	// 消息 ID
	Id string `json:"id,omitempty"`
	// 客户端 User-Agent
	UserAgent string `json:"ua,omitempty"`
	// 协议版本号（如 "0.13"）
	Version string `json:"ver,omitempty"`
	// 客户端的唯一设备 ID
	DeviceID string `json:"dev,omitempty"`
	// 客户端设备的 ISO 639-1 语言代码
	Lang string `json:"lang,omitempty"`
	// 客户端平台标识: ios, android, web
	Platform string `json:"platf,omitempty"`
	// 是否为后台/非交互式会话（Presence 状态通知将延迟发送）
	Background bool `json:"bkg,omitempty"`
}

// MsgClientAcc 表示用于创建或更新用户账号的 {acc} 消息。
type MsgClientAcc struct {
	// 消息 ID
	Id string `json:"id,omitempty"`
	// "newXYZ" 表示创建新用户，UserId 表示更新用户；默认为当前用户
	User string `json:"user,omitempty"`
	// 一次性操作（如密码重置）的临时认证参数
	TmpScheme string `json:"tmpscheme,omitempty"`
	// TmpSecret 保存Tmp密钥列表。
	TmpSecret []byte `json:"tmpsecret,omitempty"`
	// 账号状态: normal, suspended
	State string `json:"status,omitempty"`
	// 指定用户 ID 且不等于当前用户时的身份认证级别
	AuthLevel string `json:"authlevel,omitempty"`
	// 初始账号认证方案
	Scheme string `json:"scheme,omitempty"`
	// 认证密钥/密码
	Secret []byte `json:"secret,omitempty"`
	// 创建新账号后是否自动在当前 Session 登录
	Login bool `json:"login,omitempty"`
	// 用于用户搜索发现的标签列表
	Tags []string `json:"tags,omitempty"`
	// 创建新用户时的初始描述信息
	Desc *MsgSetDesc `json:"desc,omitempty"`
	// 待验证的凭证（邮箱、手机号或验证码）
	Cred []MsgCredClient `json:"cred,omitempty"`
}

// MsgClientLogin 表示登录请求 {login} 消息。
type MsgClientLogin struct {
	// 消息 ID
	Id string `json:"id,omitempty"`
	// 认证方案名称
	Scheme string `json:"scheme,omitempty"`
	// 认证密钥/密码
	Secret []byte `json:"secret"`
	// 待验证凭证列表
	Cred []MsgCredClient `json:"cred,omitempty"`
}

// MsgClientSub 表示订阅请求 {sub} 消息。
type MsgClientSub struct {
	// Id 保存标识。
	Id string `json:"id,omitempty"`
	// Topic 保存Topic。
	Topic string `json:"topic"`

	// 镜像 {set} 更新选项
	Set *MsgSetQuery `json:"set,omitempty"`

	// 镜像 {get} 查询选项
	Get *MsgGetQuery `json:"get,omitempty"`

	// 集群内部专有字段

	// 本次订阅是否创建了新的 Topic。
	// 在 P2P Topic 中，表示是否创建了对方用户的订阅。
	Created bool `json:"-"`
	// 是否为全新订阅关系
	Newsub bool `json:"-"`
}

const (
	// constMsgMetaDesc 指定constMsg元数据Desc。
	constMsgMetaDesc = 1 << iota
	// constMsgMetaSub 指定constMsg元数据订阅。
	constMsgMetaSub
	// constMsgMetaData 指定constMsg元数据数据。
	constMsgMetaData
	// constMsgMetaTags 指定constMsg元数据Tags。
	constMsgMetaTags
	// constMsgMetaDel 指定constMsg元数据Del。
	constMsgMetaDel
	// constMsgMetaCred 指定constMsg元数据凭据。
	constMsgMetaCred
	// constMsgMetaAux 指定constMsg元数据Aux。
	constMsgMetaAux
	// constMsgMetaSearch 指定关键词发现或消息全文搜索。
	constMsgMetaSearch
)

const (
	// constMsgDelTopic 指定constMsgDelTopic。
	constMsgDelTopic = iota + 1
	// constMsgDelMsg 指定constMsgDelMsg。
	constMsgDelMsg
	// constMsgDelSub 指定constMsgDel订阅。
	constMsgDelSub
	// constMsgDelUser 指定constMsgDel用户。
	constMsgDelUser
	// constMsgDelCred 指定constMsgDel凭据。
	constMsgDelCred
	// constMsgDelScheduled 指定constMsgDel定时消息。
	constMsgDelScheduled
)

// parseMsgClientMeta 将输入解析为Msg客户端元数据。
func parseMsgClientMeta(params string) int {
	var bits int
	parts := strings.SplitN(params, " ", 9)
	for _, p := range parts {
		switch p {
		case "desc":
			bits |= constMsgMetaDesc
		case "sub":
			bits |= constMsgMetaSub
		case "data":
			bits |= constMsgMetaData
		case "tags":
			bits |= constMsgMetaTags
		case "del":
			bits |= constMsgMetaDel
		case "cred":
			bits |= constMsgMetaCred
		case "aux":
			bits |= constMsgMetaAux
		case "search":
			bits |= constMsgMetaSearch
		default:
			// 忽略未知项
		}
	}
	return bits
}

// parseMsgClientDel 将输入解析为Msg客户端Del。
func parseMsgClientDel(params string) int {
	switch params {
	case "", "msg":
		return constMsgDelMsg
	case "topic":
		return constMsgDelTopic
	case "sub":
		return constMsgDelSub
	case "user":
		return constMsgDelUser
	case "cred":
		return constMsgDelCred
	case "sched":
		return constMsgDelScheduled
	default:
		// 忽略未知项
	}
	return 0
}

// MsgDefaultAcsMode 表示 Topic 的默认访问权限模式。
type MsgDefaultAcsMode struct {
	// Auth 保存认证。
	Auth string `json:"auth,omitempty"`
	// Anon 保存Anon。
	Anon string `json:"anon,omitempty"`
}

// MsgClientLeave 表示退订请求 {leave} 消息。
type MsgClientLeave struct {
	// Id 保存标识。
	Id string `json:"id,omitempty"`
	// Topic 保存Topic。
	Topic string `json:"topic"`
	// Unsub 保存Unsub。
	Unsub bool `json:"unsub,omitempty"`
}

// MsgMessageRef 是客户端对已存在消息的引用。Topic 为空表示当前 Topic。
type MsgMessageRef struct {
	// Topic 是原始消息所在会话；为空时使用当前发布目标。
	Topic string `json:"topic,omitempty"`
	// SeqId 是原始消息在对应 Topic 中的服务端序列号。
	SeqId int `json:"seq"`
}

// MsgClientPub 表示客户端向 Topic 订阅者发布数据的 {pub} 请求。
type MsgClientPub struct {
	// Id 保存标识。
	Id string `json:"id,omitempty"`
	// Topic 保存Topic。
	Topic string `json:"topic"`
	// ClientId 是客户端生成的持久化幂等键；同一发送者重试时必须保持不变。
	ClientId string `json:"cid,omitempty"`
	// NoEcho 保存NoEcho。
	NoEcho bool `json:"noecho,omitempty"`
	// Kind 是客户端声明的正文类型，服务端会根据正文重新推导并校验。
	Kind string `json:"kind,omitempty"`
	// ReplyTo 是当前 Topic 中被回复消息的 SeqId。
	ReplyTo int `json:"reply,omitempty"`
	// ReplaceSeq 是要原地编辑的消息 SeqId。
	ReplaceSeq int `json:"replace,omitempty"`
	// Forward 指向要复制正文的原始消息。
	Forward *MsgMessageRef `json:"forward,omitempty"`
	// GroupId 将连续的图片或视频组成同一媒体相册。
	GroupId string `json:"group,omitempty"`
	// ScheduleAt 指定未来投递时间；为空表示立即发送。
	ScheduleAt *time.Time `json:"schedule,omitempty"`
	// Head 按键索引消息头。
	Head map[string]any `json:"head,omitempty"`
	// Content 保存正文。
	Content any `json:"content"`
}

// MsgClientGet 表示查询 Topic 状态的 {get} 请求。
type MsgClientGet struct {
	// Id 保存标识。
	Id string `json:"id,omitempty"`
	// Topic 保存Topic。
	Topic string `json:"topic"`
	// Embedded 嵌入公共状态或行为，供当前结构直接复用。
	MsgGetQuery
}

// MsgClientSet 表示更新 Topic 状态的 {set} 请求。
type MsgClientSet struct {
	// Id 保存标识。
	Id string `json:"id,omitempty"`
	// Topic 保存Topic。
	Topic string `json:"topic"`
	// Embedded 嵌入公共状态或行为，供当前结构直接复用。
	MsgSetQuery
}

// MsgClientDel 表示删除消息或 Topic 的 {del} 请求。
type MsgClientDel struct {
	// Id 保存标识。
	Id string `json:"id,omitempty"`
	// Topic 保存Topic。
	Topic string `json:"topic,omitempty"`
	// 删除目标类型:
	// * "msg" 删除消息（默认）
	// * "topic" 删除整个 Topic
	// * "sub" 删除 Topic 订阅关系
	// * "user" 删除或禁用用户
	// * "cred" 删除凭证（邮箱或手机号）
	What string `json:"what"`
	// 待删除消息的 ID 或区间范围列表
	DelSeq []MsgRange `json:"delseq,omitempty"`
	// 待删除的目标用户 ID 或订阅 ID
	User string `json:"user,omitempty"`
	// 待删除的凭证信息
	Cred *MsgCredClient `json:"cred,omitempty"`
	// 是否物理强行删除（如对所有用户物理删除该消息）
	Hard bool `json:"hard,omitempty"`
	// what=sched 时要取消的定时消息 ID。
	ScheduledId string `json:"scheduled,omitempty"`
}

// MsgClientNote 表示客户端发起的事件状态通知 {note}（如已读、正在输入等）。
type MsgClientNote struct {
	// 可选请求 ID。read/recv 携带 ID 时服务端返回持久化确认；省略时保持旧版即发即弃语义。
	Id string `json:"id,omitempty"`
	// Topic 保存Topic。
	Topic string `json:"topic"`
	// 事件汇报类型: "recv" - 收到消息, "read" - 已读消息, "kp" - 正在输入通知
	What string `json:"what"`
	// 汇报的目标服务端消息 ID (seq)
	SeqId int `json:"seq,omitempty"`
	// 客户端未读消息计数（iOS 推送通知使用）
	Unread int `json:"unread,omitempty"`
	// 音视频通话事件类型
	Event string `json:"event,omitempty"`
	// 任意 JSON 载荷（用于音视频 WebRTC 协商）
	Payload json.RawMessage `json:"payload,omitempty"`
	// react 事件使用的 Unicode Emoji 或自定义反应标识。
	Reaction string `json:"reaction,omitempty"`
	// react/pin 事件为 true 时表示移除。
	Remove bool `json:"remove,omitempty"`
}

// MsgClientExtra 表示随主消息附带的额外扩展数据。
type MsgClientExtra struct {
	// 需免除垃圾回收的带外附件 URL 数组
	Attachments []string `json:"attachments,omitempty"`
	// 超级管理员代发消息时的实际代理用户 ID (obo = On Behalf Of)
	AsUser string `json:"obo,omitempty"`
	// 超级管理员修改的身份认证级别
	AuthLevel string `json:"authlevel,omitempty"`
}

// ClientComMessage 是客户端上行消息的统一封装外壳。
type ClientComMessage struct {
	// Hi 保存Hi。
	Hi *MsgClientHi `json:"hi"`
	// Acc 保存Acc。
	Acc *MsgClientAcc `json:"acc"`
	// Login 保存登录。
	Login *MsgClientLogin `json:"login"`
	// Sub 保存订阅。
	Sub *MsgClientSub `json:"sub"`
	// Leave 保存Leave。
	Leave *MsgClientLeave `json:"leave"`
	// Pub 保存Pub。
	Pub *MsgClientPub `json:"pub"`
	// Get 保存Get。
	Get *MsgClientGet `json:"get"`
	// Set 保存Set。
	Set *MsgClientSet `json:"set"`
	// Del 保存Del。
	Del *MsgClientDel `json:"del"`
	// Note 保存事件通知。
	Note *MsgClientNote `json:"note"`
	// 可选扩展数据
	Extra *MsgClientExtra `json:"extra"`

	// 内部字段，仅在集群内部路由传输

	// 反反序列化的消息 ID
	Id string `json:"-"`
	// 原始非路由 Topic 名称
	Original string `json:"-"`
	// 可路由（展开后）的 Topic 名称
	RcptTo string `json:"-"`
	// 发送者的用户 ID 字符串
	AsUser string `json:"-"`
	// 发送者的身份认证级别
	AuthLvl int `json:"-"`
	// 元消息 (set, get, del) 的解析操作掩码
	MetaWhat int `json:"-"`
	// 服务端接收到该消息的时间戳
	Timestamp time.Time `json:"-"`

	// 产生该消息的源 Session 引用
	sess *Session
	// 标识该消息已完成初始化
	init bool
	// 定时投递器提供的已校验持久化消息。
	scheduled *types.ScheduledMessage
}

/****************************************************************
 * 服务端发往客户端 (S2C) 的消息结构定义。
 ****************************************************************/

// MsgLastSeenInfo 包含用户最后在线时间与 User-Agent 信息。
type MsgLastSeenInfo struct {
	// 用户最后一次在线的时间戳
	When *time.Time `json:"when,omitempty"`
	// 用户最后在线时的设备 User-Agent
	UserAgent string `json:"ua,omitempty"`
}

// describe 完成describe所需的内部处理。
func (src *MsgLastSeenInfo) describe() string {
	return "'" + src.UserAgent + "' @ " + src.When.String()
}

// MsgCredServer 表示服务端返回的账号凭证信息。
type MsgCredServer struct {
	// 凭证类型，如 `email` 或 `tel`
	Method string `json:"meth,omitempty"`
	// 凭证具体值，如 `user@example.com` 或 `+18003287448`
	Value string `json:"val,omitempty"`
	// 标识该凭证是否已通过验证
	Done bool `json:"done,omitempty"`
}

// MsgAccessMode 表示访问权限模式定义。
type MsgAccessMode struct {
	// 用户申请的权限模式
	Want string `json:"want,omitempty"`
	// 管理员授予的权限模式
	Given string `json:"given,omitempty"`
	// 最终生效的综合权限模式 (want & given)
	Mode string `json:"mode,omitempty"`
	// 由最终 ACL 推导出的业务角色，便于客户端直接展示和管理成员。
	Role string `json:"role,omitempty"`
}

// describe 完成describe所需的内部处理。
func (src *MsgAccessMode) describe() string {
	var s string
	if src.Want != "" {
		s = "w=" + src.Want
	}
	if src.Given != "" {
		s += " g=" + src.Given
	}
	if src.Mode != "" {
		s += " m=" + src.Mode
	}
	if src.Role != "" {
		s += " role=" + src.Role
	}
	return strings.TrimSpace(s)
}

// MsgTopicDesc 表示 Topic 的描述信息，在 Meta 消息中下发给客户端。
type MsgTopicDesc struct {
	// CreatedAt 保存CreatedAt时间。
	CreatedAt *time.Time `json:"created,omitempty"`
	// UpdatedAt 保存UpdatedAt时间。
	UpdatedAt *time.Time `json:"updated,omitempty"`
	// 最后一条消息发送的时间戳
	TouchedAt *time.Time `json:"touched,omitempty"`

	// 账号状态（仅 'me' Topic 使用）
	State string `json:"state,omitempty"`

	// 群组 Topic 是否处于在线在线状态
	Online bool `json:"online,omitempty"`

	// 该 Topic 是否作为 Channel 频道访问
	IsChan bool `json:"chan,omitempty"`

	// P2P 对方用户最后上线时间戳与 User-Agent
	LastSeen *MsgLastSeenInfo `json:"seen,omitempty"`

	// DefaultAcs 保存默认Acs。
	DefaultAcs *MsgDefaultAcsMode `json:"defacs,omitempty"`
	// 当前生效的访问权限模式
	Acs *MsgAccessMode `json:"acs,omitempty"`
	// 最大消息 ID
	SeqId int `json:"seq,omitempty"`
	// ReadSeqId 保存Read序列号标识。
	ReadSeqId int `json:"read,omitempty"`
	// RecvSeqId 保存Recv序列号标识。
	RecvSeqId int `json:"recv,omitempty"`
	// 请求用户视角的最新删除操作 ID
	DelId int `json:"clear,omitempty"`
	// SubCnt 保存订阅Cnt。
	SubCnt int `json:"subcnt,omitempty"`
	// Public 保存公开资料。
	Public any `json:"public,omitempty"`
	// Trusted 保存可信资料。
	Trusted any `json:"trusted,omitempty"`
	// 每个订阅专有的私有数据
	Private any `json:"private,omitempty"`
}

// describe 完成describe所需的内部处理。
func (src *MsgTopicDesc) describe() string {
	var s string
	if src.State != "" {
		s = " state=" + src.State
	}
	s += " online=" + strconv.FormatBool(src.Online)
	if src.Acs != nil {
		s += " acs={" + src.Acs.describe() + "}"
	}
	if src.SeqId != 0 {
		s += " seq=" + strconv.Itoa(src.SeqId)
	}
	if src.ReadSeqId != 0 {
		s += " read=" + strconv.Itoa(src.ReadSeqId)
	}
	if src.RecvSeqId != 0 {
		s += " recv=" + strconv.Itoa(src.RecvSeqId)
	}
	if src.DelId != 0 {
		s += " clear=" + strconv.Itoa(src.DelId)
	}
	if src.SubCnt != 0 {
		s += " subcnt=" + strconv.Itoa(src.SubCnt)
	}
	if src.Public != nil {
		s += " pub='...'"
	}
	if src.Trusted != nil {
		s += " trst='...'"
	}
	if src.Private != nil {
		s += " priv='...'"
	}
	return s
}

// MsgTopicSub 表示 Meta 消息中下发的 Topic 订阅详情。
type MsgTopicSub struct {
	// 所有订阅关系的通用字段

	// 订阅关系最后更新的时间戳
	UpdatedAt *time.Time `json:"updated,omitempty"`
	// 订阅关系被删除的时间戳
	DeletedAt *time.Time `json:"deleted,omitempty"`

	// 订阅者/Topic 是否在线
	Online bool `json:"online,omitempty"`

	// 访问权限模式
	Acs MsgAccessMode `json:"acs,omitempty"`
	// 用户已读的最大消息 ID
	ReadSeqId int `json:"read,omitempty"`
	// 用户已收到的最大消息 ID
	RecvSeqId int `json:"recv,omitempty"`
	// Topic 公开数据
	Public any `json:"public,omitempty"`
	// Topic 的受信任数据
	Trusted any `json:"trusted,omitempty"`
	// 用户在当前 Topic 下的私有数据
	Private any `json:"private,omitempty"`

	// 非 'me' Topic 的响应字段

	// 订阅用户的 Uid
	User string `json:"user,omitempty"`

	// 以下字段仅在获取用户自身订阅列表（'me' Topic 响应）时生效

	// 当前订阅关系的 Topic 名称
	Topic string `json:"topic,omitempty"`
	// Topic 中最后一条消息的时间戳
	TouchedAt *time.Time `json:"touched,omitempty"`
	// Topic 中最后一条 {data} 消息的 ID
	SeqId int `json:"seq,omitempty"`
	// 最新删除操作的 ID
	DelId int `json:"clear,omitempty"`
	// 订阅者总数（仅群组 Topic 生效）
	SubCnt int `json:"subcnt,omitempty"`

	// P2P Topic 对方用户的最后上线时间与 User-Agent
	LastSeen *MsgLastSeenInfo `json:"seen,omitempty"`
}

// describe 完成describe所需的内部处理。
func (src *MsgTopicSub) describe() string {
	s := src.Topic + ":" + src.User + " online=" + strconv.FormatBool(src.Online) + " acs=" + src.Acs.describe()

	if src.SeqId != 0 {
		s += " seq=" + strconv.Itoa(src.SeqId)
	}
	if src.ReadSeqId != 0 {
		s += " read=" + strconv.Itoa(src.ReadSeqId)
	}
	if src.RecvSeqId != 0 {
		s += " recv=" + strconv.Itoa(src.RecvSeqId)
	}
	if src.DelId != 0 {
		s += " clear=" + strconv.Itoa(src.DelId)
	}
	if src.SubCnt != 0 {
		s += " subcnt=" + strconv.Itoa(src.SubCnt)
	}
	if src.Public != nil {
		s += " pub='...'"
	}
	if src.Trusted != nil {
		s += " trst='...'"
	}
	if src.Private != nil {
		s += " priv='...'"
	}
	if src.LastSeen != nil {
		s += " seen={" + src.LastSeen.describe() + "}"
	}
	return s
}

// MsgDelValues 描述删除消息的请求结果参数。
type MsgDelValues struct {
	// DelId 保存Del标识。
	DelId int `json:"clear,omitempty"`
	// DelSeq 保存Del序列号列表。
	DelSeq []MsgRange `json:"delseq,omitempty"`
}

// MsgServerCtrl 表示服务端控制响应消息 {ctrl}。
type MsgServerCtrl struct {
	// Id 保存标识。
	Id string `json:"id,omitempty"`
	// Topic 保存Topic。
	Topic string `json:"topic,omitempty"`
	// Params 保存Params。
	Params any `json:"params,omitempty"`

	// Code 保存Code。
	Code int `json:"code"`
	// Text 保存Text。
	Text string `json:"text,omitempty"`
	// Timestamp 保存Timestamp。
	Timestamp time.Time `json:"ts"`
}

// 拷贝控制消息对象。
func (src *MsgServerCtrl) copy() *MsgServerCtrl {
	if src == nil {
		return nil
	}
	dst := *src
	return &dst
}

// describe 完成describe所需的内部处理。
func (src *MsgServerCtrl) describe() string {
	return src.Topic + " id=" + src.Id + " code=" + strconv.Itoa(src.Code) + " txt=" + src.Text
}

// MsgForwardedMessage 描述转发消息的原始来源。
type MsgForwardedMessage struct {
	// Topic 在 P2P 会话中为空，避免暴露内部路由名称。
	Topic string `json:"topic,omitempty"`
	// SeqId 是原始消息的服务端序列号。
	SeqId int `json:"seq"`
	// From 是原始发送者的公开用户 ID。
	From string `json:"from,omitempty"`
	// Timestamp 是原始消息的创建时间。
	Timestamp time.Time `json:"ts"`
}

// MsgReaction 是下发给客户端的反应聚合计数。
type MsgReaction struct {
	// Reaction 是 Unicode Emoji 或自定义反应标识。
	Reaction string `json:"reaction"`
	// Count 是当前参与该反应的用户数。
	Count int `json:"count"`
}

// MsgServerData 表示服务端数据广播消息 {data}。
type MsgServerData struct {
	// Topic 保存Topic。
	Topic string `json:"topic"`
	// 发送消息的用户 ID，由系统发出时可为空
	From string `json:"from,omitempty"`
	// ClientId 回传发布方提供的幂等键，便于多端对账。
	ClientId string `json:"cid,omitempty"`
	// Timestamp 保存Timestamp。
	Timestamp time.Time `json:"ts"`
	// EditedAt 是最近一次成功编辑时间。
	EditedAt *time.Time `json:"edited,omitempty"`
	// DeletedAt 保存DeletedAt时间。
	DeletedAt *time.Time `json:"deleted,omitempty"`
	// SeqId 保存序列号标识。
	SeqId int `json:"seq"`
	// Kind 是服务端从正文推导出的可信消息类型。
	Kind string `json:"kind,omitempty"`
	// ReplyTo 是当前 Topic 中被回复消息的 SeqId。
	ReplyTo int `json:"reply,omitempty"`
	// Forwarded 仅在转发消息中包含原始来源摘要。
	Forwarded *MsgForwardedMessage `json:"forwarded,omitempty"`
	// GroupId 是服务端按发送者命名空间归一化后的媒体相册 ID。
	GroupId string `json:"group,omitempty"`
	// Reactions 仅包含聚合计数，不暴露服务端存储的用户明细。
	Reactions []MsgReaction `json:"reactions,omitempty"`
	// Head 按键索引消息头。
	Head map[string]any `json:"head,omitempty"`
	// Content 保存正文。
	Content any `json:"content"`
}

// 拷贝数据消息对象。
func (src *MsgServerData) copy() *MsgServerData {
	if src == nil {
		return nil
	}
	dst := *src
	return &dst
}

// describe 完成describe所需的内部处理。
func (src *MsgServerData) describe() string {
	s := src.Topic + " from=" + src.From + " seq=" + strconv.Itoa(src.SeqId)
	if src.DeletedAt != nil {
		s += " deleted"
	} else {
		if src.Head != nil {
			s += " head=..."
		}
		s += " content='...'"
	}
	return s
}

// MsgServerPres 表示在线状态变更通知消息 {pres}。
type MsgServerPres struct {
	// Topic 保存Topic。
	Topic string `json:"topic"`
	// Src 保存Src。
	Src string `json:"src,omitempty"`
	// What 保存What时间。
	What string `json:"what"`
	// UserAgent 指示是否启用或满足用户Agent。
	UserAgent string `json:"ua,omitempty"`
	// SeqId 保存序列号标识。
	SeqId int `json:"seq,omitempty"`
	// DelId 保存Del标识。
	DelId int `json:"clear,omitempty"`
	// DelSeq 保存Del序列号列表。
	DelSeq []MsgRange `json:"delseq,omitempty"`
	// AcsTarget 保存AcsTarget。
	AcsTarget string `json:"tgt,omitempty"`
	// AcsActor 保存AcsActor。
	AcsActor string `json:"act,omitempty"`
	// Acs 变更增量
	Acs *MsgAccessMode `json:"dacs,omitempty"`

	// 非路由内部参数（排除 JSON 序列化，仅用于集群内部通信）

	// 终止响应循环的标志位
	WantReply bool `json:"-"`

	// 发送到 Topic 在线成员时的访问模式过滤器
	FilterIn int `json:"-"`
	// FilterOut 保存过滤条件Out。
	FilterOut int `json:"-"`

	// 发送到 'me' 时跳过已订阅当前 Topic 的 Session
	SkipTopic string `json:"-"`

	// 仅发往特定用户的 Session
	SingleUser string `json:"-"`

	// 排除特定用户的 Session
	ExcludeUser string `json:"-"`
}

// 拷贝状态通知对象。
func (src *MsgServerPres) copy() *MsgServerPres {
	if src == nil {
		return nil
	}
	dst := *src
	return &dst
}

// describe 完成describe所需的内部处理。
func (src *MsgServerPres) describe() string {
	s := src.Topic
	if src.Src != "" {
		s += " src=" + src.Src
	}
	if src.What != "" {
		s += " what=" + src.What
	}
	if src.UserAgent != "" {
		s += " ua=" + src.UserAgent
	}
	if src.SeqId != 0 {
		s += " seq=" + strconv.Itoa(src.SeqId)
	}
	if src.DelId != 0 {
		s += " clear=" + strconv.Itoa(src.DelId)
	}
	if src.DelSeq != nil {
		s += " delseq"
	}
	if src.AcsTarget != "" {
		s += " tgt=" + src.AcsTarget
	}
	if src.AcsActor != "" {
		s += " actor=" + src.AcsActor
	}
	if src.Acs != nil {
		s += " dacs=" + src.Acs.describe()
	}

	return s
}

// MsgServerMeta 表示 Topic 元数据变更响应 {meta}。
type MsgServerMeta struct {
	// Id 保存标识。
	Id string `json:"id,omitempty"`
	// Topic 保存Topic。
	Topic string `json:"topic"`

	// Timestamp 保存Timestamp。
	Timestamp *time.Time `json:"ts,omitempty"`

	// Topic 描述信息
	Desc *MsgTopicDesc `json:"desc,omitempty"`
	// 订阅者列表数组
	Sub []MsgTopicSub `json:"sub,omitempty"`
	// 已删除消息的范围和 ID 记录
	Del *MsgDelValues `json:"del,omitempty"`
	// 用户检索发现标签
	Tags []string `json:"tags,omitempty"`
	// 账号凭证列表（仅 'me' 下发）
	Cred []*MsgCredServer `json:"cred,omitempty"`
	// 辅助扩展数据
	Aux map[string]any `json:"aux,omitempty"`
	// Search 保存关键词发现或消息全文搜索结果。
	Search *MsgSearchResult `json:"search,omitempty"`
}

// MsgSearchResult 是统一的 Peer 发现与消息全文搜索结果。
type MsgSearchResult struct {
	// Scope 表示本响应属于 peers 或 topic 搜索。
	Scope string `json:"scope"`
	// Peers 保存用户、群组和频道发现结果。
	Peers []MsgTopicSub `json:"peers,omitempty"`
	// Messages 保存当前 Topic 中的消息命中结果。
	Messages []*MsgServerData `json:"messages,omitempty"`
	// Next 是下一页使用的不透明游标；为空表示已经到达结果末尾。
	Next string `json:"next,omitempty"`
}

// 拷贝元数据响应对象。
func (src *MsgServerMeta) copy() *MsgServerMeta {
	if src == nil {
		return nil
	}
	dst := *src
	return &dst
}

// describe 完成describe所需的内部处理。
func (src *MsgServerMeta) describe() string {
	s := src.Topic + " id=" + src.Id

	if src.Desc != nil {
		s += " desc={" + src.Desc.describe() + "}"
	}
	if src.Sub != nil {
		var x []string
		for _, sub := range src.Sub {
			x = append(x, sub.describe())
		}
		s += " sub=[{" + strings.Join(x, "},{") + "}]"
	}
	if src.Del != nil {
		x, _ := json.Marshal(src.Del)
		s += " del={" + string(x) + "}"
	}
	if src.Tags != nil {
		s += " tags=[" + strings.Join(src.Tags, ",") + "]"
	}
	if src.Cred != nil {
		x, _ := json.Marshal(src.Cred)
		s += " cred=[" + string(x) + "]"
	}
	if src.Aux != nil {
		x, _ := json.Marshal(src.Aux)
		s += " aux=[" + string(x) + "]"
	}
	if src.Search != nil {
		s += " search=" + src.Search.Scope
	}
	return s
}

// MsgServerInfo 表示服务端的 MsgClientNote 事件副本（补充了 From 及可选的 Src 字段）。
type MsgServerInfo struct {
	// 发送事件的目标 Topic
	Topic string `json:"topic"`
	// 产生事件的原始 Topic（仅在 Topic='me' 时设置）
	Src string `json:"src,omitempty"`
	// 发起该消息的用户 ID
	From string `json:"from,omitempty"`
	// 汇报的事件类型: "rcpt" - 收到消息, "read" - 已读消息, "kp" - 正在输入通知, "call" - 音视频通话
	What string `json:"what"`
	// 对应的服务端消息序列号 (seq)
	SeqId int `json:"seq,omitempty"`
	// 通话事件类型
	Event string `json:"event,omitempty"`
	// 任意 JSON 载荷（音视频通话使用）
	Payload json.RawMessage `json:"payload,omitempty"`
	// react 事件中被添加或移除的反应。
	Reaction string `json:"reaction,omitempty"`
	// react/pin 事件为 true 时表示移除。
	Remove bool `json:"remove,omitempty"`

	// 非路由内部参数

	// 发送到 'me' 时跳过已订阅当前 Topic 的 Session
	SkipTopic string `json:"-"`
}

// 拷贝事件通知对象。
func (src *MsgServerInfo) copy() *MsgServerInfo {
	if src == nil {
		return nil
	}
	dst := *src
	return &dst
}

// describe 完成describe所需的内部处理。
func (src *MsgServerInfo) describe() string {
	s := src.Topic
	if src.Src != "" {
		s += " src=" + src.Src
	}
	s += " what=" + src.What + " from=" + src.From
	if src.SeqId > 0 {
		s += " seq=" + strconv.Itoa(src.SeqId)
	}
	if len(src.Payload) > 0 {
		s += " payload=<..." + strconv.Itoa(len(src.Payload)) + " bytes ...>"
	}
	return s
}

// ServerComMessage 是服务端下行消息的统一封装外壳。
type ServerComMessage struct {
	// Ctrl 保存Ctrl。
	Ctrl *MsgServerCtrl `json:"ctrl,omitempty"`
	// Data 保存数据。
	Data *MsgServerData `json:"data,omitempty"`
	// Meta 保存元数据。
	Meta *MsgServerMeta `json:"meta,omitempty"`
	// Pres 保存Pres。
	Pres *MsgServerPres `json:"pres,omitempty"`
	// Info 保存通知。
	Info *MsgServerInfo `json:"info,omitempty"`

	// 内部传输字段

	// 从 MsgServerData 反归一化的 Id 字段（用于 {ctrl} 确认包）
	Id string `json:"-"`
	// 可路由（展开后）的 Topic 名称
	RcptTo string `json:"-"`
	// 原始消息发送者的用户 ID
	AsUser string `json:"-"`
	// 保证 {ctrl} 消息时间戳一致性的时间戳
	Timestamp time.Time `json:"-"`
	// 需发送确认信息的源 Session 引用
	sess *Session
	// 发送时需跳过的 Session ID
	SkipSid string `json:"-"`
	// 受此消息影响的目标用户 ID (uid)
	uid types.Uid
}

// 深/浅拷贝 ServerComMessage。深拷贝服务字段，浅拷贝 Session 和载荷。
func (src *ServerComMessage) copy() *ServerComMessage {
	if src == nil {
		return nil
	}
	dst := &ServerComMessage{
		Id:        src.Id,
		RcptTo:    src.RcptTo,
		AsUser:    src.AsUser,
		Timestamp: src.Timestamp,
		sess:      src.sess,
		SkipSid:   src.SkipSid,
		uid:       src.uid,
	}

	dst.Ctrl = src.Ctrl.copy()
	dst.Data = src.Data.copy()
	dst.Meta = src.Meta.copy()
	dst.Pres = src.Pres.copy()
	dst.Info = src.Info.copy()

	return dst
}

// describe 完成describe所需的内部处理。
func (src *ServerComMessage) describe() string {
	if src == nil {
		return "-"
	}

	switch {
	case src.Ctrl != nil:
		return "{ctrl " + src.Ctrl.describe() + "}"
	case src.Data != nil:
		return "{data " + src.Data.describe() + "}"
	case src.Meta != nil:
		return "{meta " + src.Meta.describe() + "}"
	case src.Pres != nil:
		return "{pres " + src.Pres.describe() + "}"
	case src.Info != nil:
		return "{info " + src.Info.describe() + "}"
	default:
		return "{nil}"
	}
}
