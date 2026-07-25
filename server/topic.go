/******************************************************************************
 *
 *  描述 :
 *    独立通信通道（聊天室、1:1 会话），通常包含多个用户。
 *    Topic 之间相互隔离，不跨 Topic 通信。
 *
 *****************************************************************************/

package main

import (
	"sync/atomic"
	"time"

	"chat/server/auth"
	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
)

// Topic 表示一个独立的通信通道
type Topic struct {
	// Topic 的完整/唯一名称
	name string
	// 单用户 Topic（如 'me'）的 Session 专属名称，其它 Topic 与 name 相同
	xoriginal string

	// Topic 分类
	cat types.TopicCat

	// 如果 isProxy 为 true，主节点的名称
	masterNode string

	// Topic 首次创建时间
	created time.Time
	// Topic 最后更新时间
	updated time.Time
	// 最后一条发出消息的时间
	touched time.Time

	// 服务端分配的最后一条数据消息 ID
	lastID int
	// 删除操作的序列 ID（非消息 ID）
	delID int

	// 订阅者总数（不含已删除的订阅者）。
	// 在 Channel 类型 Topic 中与 subsCount() 区别对待
	subCnt int

	// 最后发布的 userAgent（仅 'me' Topic 使用）
	userAgent string

	// Topic 创建者/所有者的用户 ID，可能为 0
	owner types.Uid

	// 默认访问权限模式
	accessAuth types.AccessMode
	accessAnon types.AccessMode

	// Topic 检索/发现标签
	tags []string

	// 辅助键值对映射
	aux map[string]any

	// Topic 公开数据
	public any
	// Topic 受信数据
	trusted any

	// Topic 针对每个订阅者的缓存数据
	perUser map[types.Uid]perUserData
	// 跨所有用户的权限并集（由 uid = 0 的代理 Session 使用）
	modeWantUnion  types.AccessMode
	modeGivenUnion types.AccessMode

	// 用户联系人列表（仅 'me' Topic 非空）
	perSubs map[string]perSubsData

	// 挂载到此 Topic 的 Session
	sessions map[*Session]perSessionData

	// 当前视频通话数据。无通话时为 nil
	currentCall *videoCall

	// 接收客户端消息的 Channel，buffer = 256
	clientMsg chan *ClientComMessage
	// 接收服务端/集群节点消息的 Channel，buffer = 64
	serverMsg chan *ServerComMessage
	// 接收 {get}/{set}/{del} 请求的 Channel，buffer = 64
	meta chan *ClientComMessage
	// 来自 Session 的订阅请求 Channel，buffer = 256
	reg chan *ClientComMessage
	// 来自 Session 的取消订阅请求 Channel，buffer = 256
	unreg chan *ClientComMessage
	// Session 状态更新 Channel，buffer = 32
	supd chan *sessionUpdate
	// 终止 Topic 的 Channel，buffer = 1
	exit chan *shutDown
	// 接收主节点响应的 Channel（仅代理 Topic 使用）
	proxy chan *ClusterResp
	// 接收代理服务请求的 Channel
	master chan *ClusterSessUpdate

	// Topic 生命周期状态标志：新建、就绪、暂停、标记删除
	status int32

	// 群组 Topic 是否开启 Channel 功能
	isChan bool

	// 如果 isProxy == true，实际 Topic 运行在集群的另一个节点上
	isProxy bool

	// 当无 Session 连接时，用于销毁 Topic 的倒计时定时器
	killTimer *time.Timer

	// 未建立通话的超时倒计时定时器
	callEstablishmentTimer *time.Timer
}

// perUserData 存放 Topic 针对单个订阅者的缓存数据
type perUserData struct {
	// 在线且已广播（未延迟）的订阅数
	online int

	// 用户通过 {pres} 汇报已接收/已读的最后消息 ID
	recvID int
	readID int
	// 最新一次删除操作的 ID
	delID int

	private any

	modeWant  types.AccessMode
	modeGiven types.AccessMode

	// 仅 P2P:
	public   any
	trusted  any
	lastSeen *time.Time
	lastUA   string

	topicName string
	deleted   bool

	// 用户是否为 Channel 订阅者
	isChan bool
}

// perSubsData 存放用户（在 'me' Topic 上）的订阅缓存数据
type perSubsData struct {
	// 该用户视角下对方用户/Topic 的在线状态
	online bool
	// 是否关心对方的更新
	enabled bool
}

// Session 在 Topic 上的订阅关联数据
type perSessionData struct {
	// 订阅用户的 ID
	uid types.Uid
	// 是否为 Channel 订阅
	isChanSub bool
	// 多路复用 Session 中的订阅用户 ID 列表
	muids []types.Uid
}

// Topic 关停原因枚举
const (
	// StopNone 默认/未说明原因
	StopNone = iota
	// StopShutdown 系统关停
	StopShutdown
	// StopDeleted 被删除
	StopDeleted
	// StopRehashing 集群重新哈希分布
	StopRehashing
)

// Topic 关停事件
type shutDown struct {
	// 汇报关停完成的 Channel
	done chan<- bool
	// 关停原因
	reason int
}

// sessionUpdate 用户 agent 变更或后台 Session 转为前台。
// 如果 sess 为 nil 则表示用户 agent 变更，否则表示从后台转前台的更新。
type sessionUpdate struct {
	sess      *Session
	userAgent string
}

var (
	nilPresParams  = &presParams{}
	nilPresFilters = &presFilters{}
)

func (t *Topic) run(hub *Hub) {
	if !t.isProxy {
		t.runLocal(hub)
	} else {
		t.runProxy(hub)
	}
}

// getPerUserAcs 返回指定用户 ID 的 `want` 和 `given` 权限。
func (t *Topic) getPerUserAcs(uid types.Uid) (types.AccessMode, types.AccessMode) {
	if uid.IsZero() {
		// 对于零值 uid（通常为代理 Session），返回所有权限的并集。
		return t.modeWantUnion, t.modeGivenUnion
	}
	pud := t.perUser[uid]
	return pud.modeWant, pud.modeGiven
}



// prepareBroadcastableMessage 根据 uid 和订阅类型设置 `msg` 中的 Topic 字段。


// computePerUserAcsUnion 计算 Topic 所有订阅者的 want 和 given 权限并集。
func (t *Topic) computePerUserAcsUnion() {
	wantUnion := types.ModeNone
	givenUnion := types.ModeNone
	for _, pud := range t.perUser {
		if pud.isChan {
			continue
		}
		wantUnion |= pud.modeWant
		givenUnion |= pud.modeGiven
	}

	if t.isChan {
		// 对 Channel Topic 应用标准 Channel 权限。
		wantUnion |= types.ModeCChnReader
		givenUnion |= types.ModeCChnReader
	}

	t.modeWantUnion = wantUnion
	t.modeGivenUnion = givenUnion
}





func (t *Topic) handleSessionUpdate(upd *sessionUpdate, currentUA *string, uaTimer *time.Timer) {
	if upd.sess != nil {
		// 仅 'me' 和 'grp'。后台 Session 超时后重新上线。
		t.sessToForeground(upd.sess)
	} else if *currentUA != upd.userAgent {
		if t.cat != types.TopicCatMe {
			logs.Warn.Panicln("invalid topic category in UA update", t.name)
		}
		// 仅 'me'。处理来自某个 Session 的用户 agent 更新。
		*currentUA = upd.userAgent
		uaTimer.Reset(uaTimerDelay)
	}
}

func (t *Topic) handleUATimerEvent(currentUA string) {
	// 延迟发布用户 agent 变更
	if currentUA == "" || currentUA == t.userAgent {
		return
	}
	t.userAgent = currentUA
	t.presUsersOfInterest("ua", t.userAgent)
}

func (t *Topic) handleTopicTimeout(hub *Hub, currentUA string, uaTimer, defrNotifTimer *time.Timer) {
	// Topic 超时
	hub.unreg <- &topicUnreg{rcptTo: t.name}
	defrNotifTimer.Stop()
	switch t.cat {
	case types.TopicCatMe:
		uaTimer.Stop()
		t.presUsersOfInterest("off", currentUA)
	case types.TopicCatGrp:
		t.presSubsOffline("off", nilPresParams, nilPresFilters, nilPresFilters, "", false)
	}
}

func (t *Topic) handleTopicTermination(sd *shutDown) {
	// 处理四种情况：
	// 1. Topic 因不活跃而通过定时器关闭（reason == StopNone）
	// 2. Topic 正在被删除（reason == StopDeleted）
	// 3. 系统关停（reason == StopShutdown, done != nil）
	// 4. 集群重新哈希（reason == StopRehashing）

	switch sd.reason {
	case StopDeleted:
		if t.cat == types.TopicCatGrp {
			t.presSubsOffline("gone", nilPresParams, nilPresFilters, nilPresFilters, "", false)
		}
		// P2P 用户在流程早期已收到 "off+remove"

		// 通知插件 Topic 已被删除
		pluginTopic(t, plgActDel)

	case StopRehashing:
		// 必须向 Session 发送单独消息，因为通过 Topic 的
		// 广播 Channel 正常发送将不起作用 - 它很快会被关闭。
		t.presSubsOnlineDirect("term", nilPresParams, nilPresFilters, "")
	}
	// 系统关停时不费心发送通知。反正也不会被送达。

	// 告诉 Session 移除 Topic
	for s := range t.sessions {
		s.detachSession(t.name)
	}

	if t.cat == types.TopicCatGrp {
		// 更新 Topic 订阅者数量。
		if err := store.Topics.UpdateSubCnt(t.name); err != nil {
			logs.Warn.Println("topic update sub cnt:", err)
		}
	}

	usersRegisterTopic(t, false)

	// 如果 'done' 不为 nil，向发送者报告完成情况。
	if sd.done != nil {
		sd.done <- true
	}
}

func (t *Topic) runLocal(hub *Hub) {
	// 在一段时间不活跃后杀死 Topic。
	t.killTimer = time.NewTimer(time.Hour)
	t.killTimer.Stop()

	// 通知用户 agent 变更。仅 'me'
	uaTimer := time.NewTimer(time.Minute)
	var currentUA string
	uaTimer.Stop()

	// 延迟在线状态通知的定时器。
	defrNotifTimer := time.NewTimer(time.Millisecond * 500)

	t.callEstablishmentTimer = time.NewTimer(time.Second)
	t.callEstablishmentTimer.Stop()

	for {
		select {
		case msg := <-t.reg:
			t.registerSession(msg)

		case msg := <-t.unreg:
			t.unregisterSession(msg)

		case msg := <-t.clientMsg:
			t.handleClientMsg(msg)

		case msg := <-t.serverMsg:
			t.handleServerMsg(msg)

		case meta := <-t.meta:
			t.handleMeta(meta)

		case upd := <-t.supd:
			t.handleSessionUpdate(upd, &currentUA, uaTimer)

		case <-uaTimer.C:
			t.handleUATimerEvent(currentUA)

		case <-t.killTimer.C:
			t.handleTopicTimeout(hub, currentUA, uaTimer, defrNotifTimer)

		case <-t.callEstablishmentTimer.C:
			t.terminateCallInProgress(true)

		case sd := <-t.exit:
			t.handleTopicTermination(sd)
			return
		}
	}
}

// handleClientMsg 是 Topic 从 Session 接收消息的顶层处理器。
func (t *Topic) handleClientMsg(msg *ClientComMessage) {
	if msg.Pub != nil {
		t.handlePubBroadcast(msg)
	} else if msg.Note != nil {
		t.handleNoteBroadcast(msg)
	} else {
		logs.Warn.Printf("topic[%s]: wrong client message type for broadcasting, dropped", t.name)
		if msg.sess != nil {
			msg.sess.queueOut(ErrMalformed(msg.Id, t.name, types.TimeNow()))
		}
	}
}

// handleServerMsg 是服务端生成消息的顶层处理器。
func (t *Topic) handleServerMsg(msg *ServerComMessage) {
	// 服务端生成的消息：{info} 或 {pres}。
	if t.isInactive() {
		// 忽略消息 - Topic 已暂停或正在被删除。
		return
	}
	if msg.Pres != nil {
		t.handlePresence(msg)
	} else if msg.Info != nil {
		t.broadcastToSessions(msg)
	} else {
		logs.Warn.Printf("topic[%s]: wrong server message type for broadcasting, dropped", t.name)
	}
}

// mostRecentSession 返回当前 Topic 中最近活跃的 Session 实例。
func (t *Topic) mostRecentSession() *Session {
	var sess *Session
	var latest int64
	for s := range t.sessions {
		if s == nil {
			continue
		}
		sessionLastAction := atomic.LoadInt64(&s.lastAction)
		if sessionLastAction > latest {
			sess = s
			latest = sessionLastAction
		}
	}
	return sess
}

const (
	// Topic 已完全初始化。
	topicStatusLoaded = 0x1
	// Topic 已暂停：所有数据包被拒绝。
	topicStatusPaused = 0x2

	// Topic 正在被删除。这是不可恢复的。
	topicStatusMarkedDeleted = 0x10
	// Topic 已挂起：只读模式。
	topicStatusReadOnly = 0x20
)

// statusChangeBits 设置或移除 t.status 中的指定位
func (t *Topic) statusChangeBits(bits int32, set bool) {
	for {
		oldStatus := atomic.LoadInt32(&t.status)
		newStatus := oldStatus
		if set {
			newStatus |= bits
		} else {
			newStatus &= ^bits
		}
		if newStatus == oldStatus {
			break
		}
		if atomic.CompareAndSwapInt32(&t.status, oldStatus, newStatus) {
			break
		}
	}
}

// markLoaded 表示 Topic 订阅者已加载到内存中。
func (t *Topic) markLoaded() {
	t.statusChangeBits(topicStatusLoaded, true)
}

// markPaused 暂停或取消暂停 Topic。当 Topic 暂停时，
// 所有消息被拒绝。
func (t *Topic) markPaused(pause bool) {
	t.statusChangeBits(topicStatusPaused, pause)
}

// markDeleted 将 Topic 标记为正在被删除。
func (t *Topic) markDeleted() {
	t.statusChangeBits(topicStatusMarkedDeleted, true)
}

// markReadOnly 挂起/取消挂起 Topic：添加或移除 'read-only' 标志。
func (t *Topic) markReadOnly(readOnly bool) {
	t.statusChangeBits(topicStatusReadOnly, readOnly)
}

// isInactive 检查 Topic 是否已暂停或正在被删除。
func (t *Topic) isInactive() bool {
	return (atomic.LoadInt32(&t.status) & (topicStatusPaused | topicStatusMarkedDeleted)) != 0
}

func (t *Topic) isReadOnly() bool {
	return (atomic.LoadInt32(&t.status) & topicStatusReadOnly) != 0
}

func (t *Topic) isLoaded() bool {
	return (atomic.LoadInt32(&t.status) & topicStatusLoaded) != 0
}

func (t *Topic) isDeleted() bool {
	return (atomic.LoadInt32(&t.status) & topicStatusMarkedDeleted) != 0
}

// 获取适合给定客户端的 Topic 名称
func (t *Topic) original(uid types.Uid) string {
	if t.cat == types.TopicCatP2P {
		if pud, ok := t.perUser[uid]; ok {
			return pud.topicName
		}
		panic("Invalid P2P topic")
	}

	if t.cat == types.TopicCatGrp && t.isChan {
		if t.perUser[uid].isChan {
			// 这是 Channel 读者。
			return types.GrpToChn(t.xoriginal)
		}
	}
	return t.xoriginal
}

// 获取 P2P Topic 中另一个用户的 ID
func (t *Topic) p2pOtherUser(uid types.Uid) types.Uid {
	if t.cat == types.TopicCatP2P {
		// 尝试在订阅者中查找用户
		for u2 := range t.perUser {
			if u2.Compare(uid) != 0 {
				return u2
			}
		}
	}

	// 即使用户之一已被删除，订阅也必须在
	// 调用 p2pOtherUser 之前恢复。
	panic("Not a valid P2P topic")
}

// 获取每个 Session 的 fnd.Public 值
func (t *Topic) fndGetPublic(sess *Session) string {
	if t.cat == types.TopicCatFnd {
		if t.public == nil {
			return ""
		}
		if pubmap, ok := t.public.(map[string]any); ok {
			if public, ok := pubmap[sess.sid].(string); ok {
				return public
			}
			return ""
		}
		panic("Invalid Fnd.Public type")
	}
	panic("Not Fnd topic")
}

// 为每个 Session 分配 fnd.Public。如果值已更改则返回 true。
func (t *Topic) fndSetPublic(sess *Session, public any) bool {
	if t.cat != types.TopicCatFnd {
		panic("Not Fnd topic")
	}

	var pubmap map[string]any
	var ok bool
	if t.public != nil {
		if pubmap, ok = t.public.(map[string]any); !ok {
			// 只有在此函数之外分配 fnd.public 时才会发生这种情况。
			panic("Invalid Fnd.Public type")
		}
	}
	if pubmap == nil {
		pubmap = make(map[string]any)
	}

	if public != nil {
		pubmap[sess.sid] = public
	} else {
		ok = (pubmap[sess.sid] != nil)
		delete(pubmap, sess.sid)
		if len(pubmap) == 0 {
			pubmap = nil
		}
	}
	t.public = pubmap
	return ok
}

// 移除每个 Session 的 fnd.Public 值。
func (t *Topic) fndRemovePublic(sess *Session) {
	if t.public == nil || sess == nil {
		return
	}
	if pubmap, ok := t.public.(map[string]any); ok {
		delete(pubmap, sess.sid)
	} else {
		logs.Warn.Printf("topic[%s]: invalid fnd.Public type %T, reset to nil", t.name, t.public)
		t.public = nil
	}
}

func (t *Topic) accessFor(authLvl auth.Level) types.AccessMode {
	return selectAccessMode(authLvl, t.accessAnon, t.accessAuth, getDefaultAccess(t.cat, true, false))
}

// subsCount 返回 Topic 订阅者数量。此方法与 subCnt 在 Channel 方面有所不同：
// * subsCount 计算订阅者 + 已挂载的 Channel 用户。
// * subCnt 计算所有订阅者（包括所有 Channel 用户）。
func (t *Topic) subsCount() int {
	if t.cat == types.TopicCatP2P {
		count := 0
		for _, pud := range t.perUser {
			if !pud.deleted {
				count++
			}
		}
		return count
	}
	return len(t.perUser)
}

// 添加 Session 记录。'用户' 可能与 sess.uid 不同。
func (t *Topic) addSession(sess *Session, asUid types.Uid, isChanSub bool) {
	s := sess
	if sess.multi != nil {
		s = s.multi
	}

	if pssd, ok := t.sessions[s]; ok {
		// 订阅 already exists.
		if s.isMultiplex() && !sess.background {
			// 此切片预计相对较短。
			// 这里不做任何花哨操作，如使用 map 或排序。
			pssd.muids = append(pssd.muids, asUid)
			t.sessions[s] = pssd
		}
		// 或许应该 panic。
		return
	}

	if s.isMultiplex() {
		if sess.background {
			t.sessions[s] = perSessionData{}
		} else {
			t.sessions[s] = perSessionData{muids: []types.Uid{asUid}, isChanSub: isChanSub}
		}
	} else {
		t.sessions[s] = perSessionData{uid: asUid, isChanSub: isChanSub}
	}
}

// 在以下任一条件为真时断开 Session 与 Topic 的连接：
// * 's' 是普通 Session 且（'asUid' 为零或 'asUid' 匹配已订阅用户）。
// * 's' 是多路复用 Session 且正在被完全移除（'asUid' 为零）。
// 如果 's' 是多路复用 Session 且 asUid 不为零，则从 Session
// 用户 'muids' 列表中移除。
// 如果找到 perSessionData 则返回它，如果 Session 实际从 Topic 断开则返回 true。
func (t *Topic) remSession(sess *Session, asUid types.Uid) (*perSessionData, bool) {
	s := sess
	if sess.multi != nil {
		s = s.multi
	}
	pssd, ok := t.sessions[s]
	if !ok {
		// 完全找不到 Session。
		return nil, false
	}

	if pssd.uid == asUid || asUid.IsZero() {
		delete(t.sessions, s)
		return &pssd, true
	}

	for i := range pssd.muids {
		if pssd.muids[i] == asUid {
			pssd.muids[i] = pssd.muids[len(pssd.muids)-1]
			pssd.muids = pssd.muids[:len(pssd.muids)-1]
			t.sessions[s] = pssd
			if len(pssd.muids) == 0 {
				delete(t.sessions, s)
				return &pssd, true
			}

			return &pssd, false
		}
	}

	return nil, false
}

// 检查是否 Topic 有任何在线（非后台）用户。
func (t *Topic) isOnline() bool {
	// 查找至少一个非后台 Session。
	for s, pssd := range t.sessions {
		if s.isMultiplex() && len(pssd.muids) > 0 {
			return true
		}
		if !s.background {
			return true
		}
	}
	return false
}

// 验证 Topic 是否可以通过提供的名称访问：以非 Channel 方式访问任何 Topic，以 Channel 方式访问 Channel。
// 如果是 Channel 访问则返回 true，否则返回 false，如果访问无效则返回错误。
func (t *Topic) verifyChannelAccess(asTopic string) (bool, error) {
	if !types.IsChannel(asTopic) {
		return false, nil
	}
	if t.isChan {
		return true, nil
	}
	return false, types.ErrNotFound
}

// 从名称推断 Topic 分类。
func topicCat(name string) types.TopicCat {
	return types.GetTopicCat(name)
}

// 生成群组 Topic 的名称，以 "grp" 后跟随机的
// 唯一字符串。
func genTopicName() string {
	return "grp" + store.Store.GetUidString()
}

// 将扩展（可路由）Topic 名称转换为适合发送给用户的名称。
// 例如 p2pAbCDef123 -> usrAbCDef
func topicNameForUser(name string, uid types.Uid, isChan bool) string {
	switch topicCat(name) {
	case types.TopicCatMe:
		return "me"
	case types.TopicCatFnd:
		return "fnd"
	case types.TopicCatP2P:
		topic, _ := types.P2PNameForUser(uid, name)
		return topic
	case types.TopicCatGrp:
		if isChan {
			return types.GrpToChn(name)
		}
	}
	return name
}

// calculateUnreadInRanges 计算给定范围内有多少未读消息。
// unreadStart 是第一条未读消息的 SeqId（readID + 1），unreadEnd 是最后可能的消息 SeqId。
// 假设范围按 Low 升序排序。
func calculateUnreadInRanges(readID, lastID int, ranges []types.Range) int {
	if readID >= lastID {
		// 没有未读消息
		return 0
	}

	unreadStart := readID + 1
	unreadEnd := lastID

	// 累加未读消息。
	count := 0

	for i := 0; i < len(ranges); i++ {
		rangeStart := ranges[i].Low
		rangeEnd := ranges[i].Hi
		if rangeEnd == 0 {
			rangeEnd = rangeStart + 1
		}
		// 查找第一个 rangeEnd > readID 的范围
		if rangeEnd <= readID {
			continue
		}

		// 查找 [unreadStart, unreadEnd] 和 [rangeStart, rangeEnd) 的交集
		intersectionStart := max(unreadStart, rangeStart)
		intersectionEnd := min(unreadEnd+1, rangeEnd) // +1 因为 unreadEnd 是包含的

		if intersectionStart < intersectionEnd {
			count += intersectionEnd - intersectionStart
		}
	}

	return count
}
