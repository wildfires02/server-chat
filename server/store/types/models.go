// Package types 提供领域模型及持久化访问层。
package types

import "time"

// User 是数据库中存储的用户记录数据结构。
type User struct {
	// Embedded 嵌入公共状态或行为，供当前结构直接复用。
	ObjHeader `bson:",inline"`

	// State 保存状态。
	State ObjState
	// StateAt 保存状态At时间。
	StateAt *time.Time `json:"StateAt,omitempty" bson:",omitempty"`

	// 用户针对 P2P Topic 的默认访问权限（作为默认的 modeGiven 赠与权限）
	Access DefaultAccess

	// 用于 'me' Topic 的状态属性：

	// 用户上次由 User Agent 客户端连接加入 'me' Topic 的时间
	LastSeen *time.Time
	// 用户上次访问 Topic 时提供的 UserAgent
	UserAgent string

	// Public 保存公开资料。
	Public any
	// Trusted 保存可信资料。
	Trusted any

	// 用于查找该用户的唯一索引标签（邮箱、手机号等）。
	// 存储在 'user' 对象上，并在 'tagunique' 表中建立唯一索引
	Tags StringSlice

	// 已知设备信息，用于推送通知 (Push Notifications)
	Devices map[string]*DeviceDef `bson:"__devices,skip,omitempty"`
	// 用于 MongoDB 模式的设备数组
	DeviceArray []*DeviceDef `json:"-" bson:"devices"`
}

// Credential 包含验证和检查凭据（如邮箱或手机号）有效性所需的数据。
type Credential struct {
	// Embedded 嵌入公共状态或行为，供当前结构直接复用。
	ObjHeader `bson:",inline"`
	// 凭据所有者
	User string
	// 验证方式（email、tel、captcha 等）
	Method string
	// 凭据值 - `jdoe@example.com` 或 `+12345678901`
	Value string
	// 期望的响应
	Resp string
	// 如果凭据已成功确认
	Done bool
	// 重试次数
	Retries int
}

// LastSeenUA 是用户上次被看到时的时间戳和用户代理。
type LastSeenUA struct {
	// When 是用户上次上线的时间戳。
	When time.Time
	// UserAgent 是上次上线访问时的客户端 UA。
	UserAgent string
}

// 订阅到 Topic
type Subscription struct {
	// Embedded 嵌入公共状态或行为，供当前结构直接复用。
	ObjHeader `bson:",inline"`
	// 与 Topic 有关联关系的用户
	User string
	// 订阅的 Topic
	Topic string
	// DeletedAt 保存DeletedAt时间。
	DeletedAt *time.Time `bson:",omitempty"`

	// 在订阅软删除期间持久化的值

	// 最近一次软删除操作的 ID
	DelId int
	// 用户报告的至少一个 Session 已接收的最新 SeqId
	RecvSeqId int
	// 用户报告的已读最新 SeqID
	ReadSeqId int
	// 最近七天内逐段记录已读序号推进时间，用于群聊 Seen by 查询。
	ReadHistory ReadHistory `json:"ReadHistory,omitempty" bson:"readhistory,omitempty"`

	// 该用户请求的访问模式
	ModeWant AccessMode
	// 授予该用户的访问模式
	ModeGiven AccessMode
	// 与 Topic 订阅关联的用户私有数据
	Private any

	// 反序列化后的临时值

	// 从 Topic 或用户反序列化的公共值（取决于上下文）
	// 在 P2P Topic 中，这是对方的 Public 值。
	public any
	// 在 P2P Topic 中，这是对方的 Trusted 值。
	trusted any
	// 从用户或 Topic 反序列化的 SeqID
	seqId int
	// 从 Topic 反序列化的 TouchedAt
	touchedAt time.Time
	// 用户上次上线的时间戳和用户代理。
	lastSeenUA *LastSeenUA

	// 订阅者数量。
	subCnt int

	// 仅 P2P。对方的用户 ID
	with string
	// 仅 P2P。默认访问：这是对方用户授予本用户的模式
	modeDefault *DefaultAccess

	// Topic 或用户的状态。
	state ObjState

	// 这不是一个完全初始化的订阅对象
	dummy bool
	// searchScore 是数据库搜索阶段计算的临时相关性分数，不持久化也不下发。
	searchScore int
}

// SetPublic 将值分配给 `public`，否则无法从包外部访问。
func (s *Subscription) SetPublic(pub any) {
	s.public = pub
}

// GetPublic 读取 `public` 的值。
func (s *Subscription) GetPublic() any {
	return s.public
}

// SetTrusted 将值分配给 `trusted`，否则无法从包外部访问。
func (s *Subscription) SetTrusted(tstd any) {
	s.trusted = tstd
}

// GetTrusted 读取 `trusted` 的值。
func (s *Subscription) GetTrusted() any {
	return s.trusted
}

// SetWith 设置 P2P 订阅的对方用户。
func (s *Subscription) SetWith(with string) {
	s.with = with
}

// GetWith 返回 P2P 订阅的对方用户。
func (s *Subscription) GetWith() string {
	return s.with
}

// GetTouchedAt 返回 touchedAt。
func (s *Subscription) GetTouchedAt() time.Time {
	return s.touchedAt
}

// SetTouchedAt 设置 touchedAt 的值。
func (s *Subscription) SetTouchedAt(touchedAt time.Time) {
	if touchedAt.After(s.touchedAt) {
		s.touchedAt = touchedAt
	}
}

// LastModified 返回 TouchedAt 和 UpdatedAt 中较大的一个。
func (s *Subscription) LastModified() time.Time {
	if s.UpdatedAt.Before(s.touchedAt) {
		return s.touchedAt
	}
	return s.UpdatedAt
}

// GetSeqId 返回 seqId。
func (s *Subscription) GetSeqId() int {
	return s.seqId
}

// SetSeqId 设置 seqId 字段。
func (s *Subscription) SetSeqId(id int) {
	s.seqId = id
}

// GetSubCnt 返回 subCnt（订阅者数量）。
func (s *Subscription) GetSubCnt() int {
	return s.subCnt
}

// SetSubCnt 设置 subCnt（订阅者数量）。
func (s *Subscription) SetSubCnt(cnt int) {
	s.subCnt = cnt
}

// GetLastSeen 返回 lastSeen。
func (s *Subscription) GetLastSeen() *time.Time {
	if s.lastSeenUA != nil {
		return &s.lastSeenUA.When
	}
	return nil
}

// GetUserAgent 返回 userAgent。
func (s *Subscription) GetUserAgent() string {
	if s.lastSeenUA != nil {
		return s.lastSeenUA.UserAgent
	}
	return ""
}

// SetLastSeenAndUA 更新 lastSeen 时间和 userAgent。
func (s *Subscription) SetLastSeenAndUA(when *time.Time, ua string) {
	if when != nil && !when.IsZero() {
		s.lastSeenUA = &LastSeenUA{
			When:      *when,
			UserAgent: ua,
		}
	} else {
		s.lastSeenUA = nil
	}
}

// SetDefaultAccess 更新默认访问值。
func (s *Subscription) SetDefaultAccess(auth, anon AccessMode) {
	s.modeDefault = &DefaultAccess{auth, anon}
}

// GetDefaultAccess 返回默认访问。
func (s *Subscription) GetDefaultAccess() *DefaultAccess {
	return s.modeDefault
}

// GetState 返回 Topic 或用户的状态。
func (s *Subscription) GetState() ObjState {
	return s.state
}

// SetState 分配 Topic 或用户的状态。
func (s *Subscription) SetState(state ObjState) {
	s.state = state
}

// SetDummy 将此订阅对象标记为仅部分初始化。
func (s *Subscription) SetDummy(dummy bool) {
	s.dummy = dummy
}

// IsDummy 在此订阅对象仅部分初始化时返回 true。
func (s *Subscription) IsDummy() bool {
	return s.dummy
}

// SetSearchScore 保存仅用于当前搜索结果排序的相关性分数。
func (s *Subscription) SetSearchScore(score int) {
	s.searchScore = score
}

// GetSearchScore 返回仅用于当前搜索结果排序的相关性分数。
func (s *Subscription) GetSearchScore() int {
	return s.searchScore
}

// Contact 是搜索连接的结果
type Contact struct {
	// Id 保存标识。
	Id string
	// MatchOn 保存MatchOn列表。
	MatchOn []string
	// Access 保存Access。
	Access DefaultAccess
	// LastSeen 保存LastSeen。
	LastSeen time.Time
	// Public 保存公开资料。
	Public any
}

// perUserData 保存per用户数据的数据和运行状态。
type perUserData struct {
	// private 保存private。
	private any
	// want 保存want。
	want AccessMode
	// given 保存given。
	given AccessMode
}

// DeviceDef 是连接设备提供的数据。主要用于推送通知。
type DeviceDef struct {
	// 设备注册 ID
	DeviceId string
	// 设备平台（iOS、Android、Web）
	Platform string
	// 最后登录时间
	LastSeen time.Time
	// 设备语言，ISO 代码
	Lang string
}

// 媒体处理常量
const (
	// UploadStarted 表示上传已开始但尚未完成。
	UploadStarted = iota
	// UploadCompleted 表示上传已成功完成。
	UploadCompleted
	// UploadFailed 表示上传失败。
	UploadFailed
	// UploadDeleted 表示上传已不再需要，可以删除。
	UploadDeleted
)

// FileDef 是文件上传的存储记录
type FileDef struct {
	// Embedded 嵌入公共状态或行为，供当前结构直接复用。
	ObjHeader `bson:",inline"`
	// 上传状态
	Status int
	// 创建文件的用户
	User string
	// 文件类型。
	MimeType string
	// 文件大小（字节）。
	Size int64
	// 内部文件位置，即磁盘路径或 S3 blob 地址。
	Location string
	// 文件服务器生成的 ETag。
	ETag string
}

// FlattenDoubleSlice 将二维切片转换为一维切片。
func FlattenDoubleSlice(data [][]string) []string {
	var result []string
	for _, el := range data {
		result = append(result, el...)
	}
	return result
}
