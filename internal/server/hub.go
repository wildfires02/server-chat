/******************************************************************************
 *
 *  描述 :
 *
 *    主 Hub，用于处理创建/销毁 Topic 以及在 Topic 之间路由消息的事件。
 *
 *****************************************************************************/

// Package server 实现即时通信服务端的协议、路由和业务逻辑。
package server

import (
	"strings"
	"sync"

	"chat/server/auth"
	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
)

// requestLatencyDistribution 请求延迟分布边界数组（单位：毫秒）
var requestLatencyDistribution = []float64{1, 2, 3, 4, 5, 6, 8, 10, 13, 16, 20, 25, 30, 40, 50, 65, 80, 100, 130,
	160, 200, 250, 300, 400, 500, 650, 800, 1000, 2000, 5000, 10000, 20000, 50000, 100000}

// outgoingMessageSizeDistribution 发送消息大小分布边界数组（单位：字节）
var outgoingMessageSizeDistribution = []float64{1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 1024, 2048, 4096, 16384,
	65536, 262144, 1048576, 4194304, 16777216, 67108864, 268435456, 1073741824, 4294967296}

// 向 Hub 请求移除 Topic
type topicUnreg struct {
	// 原始请求数据包，可能为 nil
	pkt *ClientComMessage
	// 发起请求的 Session，可能为 nil
	sess *Session
	// 待移除的 Topic 名称
	rcptTo string
	// 被删除用户的 UID
	forUser types.Uid
	// 先注销然后删除 Topic
	del bool
	// 删除用户 Topic 时汇报操作完成的 Channel
	done chan<- bool
}

// userStatusReq 保存用户状态Req的数据和运行状态。
type userStatusReq struct {
	// 受影响用户的 UID
	forUser types.Uid
	// 新的 Topic 状态值。目前仅支持 types.StateSuspended
	state types.ObjState
}

// Hub 是管理所有 Topic 的核心结构
type Hub struct {

	// Topic 映射表，按名称索引
	topics *sync.Map

	// 当前加载的 Topic 数量
	numTopics int

	// 用于路由客户端消息的 Channel，buffer = 4096
	routeCli chan *ClientComMessage
	// 持久化定时消息到期后的内部路由 Channel。
	schedule chan *ClientComMessage

	// 处理未订阅 Topic 的 get.info 请求 Channel，buffer = 128
	meta chan *ClientComMessage

	// 用于路由服务端生成消息的 Channel，buffer = 4096
	routeSrv chan *ServerComMessage

	// 将 Session 订阅至 Topic，必要时创建新 Topic，buffer = 256
	join chan *ClientComMessage

	// 从 Hub 中移除 Topic，必要时后续进行删除，buffer = 32
	unreg chan *topicUnreg

	// 用于暂停/恢复用户状态的 Channel，buffer = 128
	userStatus chan *userStatusReq

	// 集群重新哈希分布 Topic 的请求 Channel，无缓冲
	rehash chan bool

	// 关停服务请求 Channel，无缓冲
	shutdown chan chan<- bool
}

// topicGet 将输入编码为picGet。
func (h *Hub) topicGet(name string) *Topic {
	if t, ok := h.topics.Load(name); ok {
		return t.(*Topic)
	}
	return nil
}

// topicPut 将输入编码为picPut。
func (h *Hub) topicPut(name string, t *Topic) {
	h.numTopics++
	h.topics.Store(name, t)
}

// topicDel 将输入编码为picDel。
func (h *Hub) topicDel(name string) {
	h.numTopics--
	h.topics.Delete(name)
}

// newHub 创建并初始化Hub。
func newHub() *Hub {
	h := &Hub{
		topics: &sync.Map{},
		// 管道缓冲区配置：为高并发与集群跨节点路由提供缓冲屏障，防止网络抖动阻塞主循环
		routeCli:   make(chan *ClientComMessage, 4096),
		schedule:   make(chan *ClientComMessage, 512),
		routeSrv:   make(chan *ServerComMessage, 4096),
		join:       make(chan *ClientComMessage, 256),
		unreg:      make(chan *topicUnreg, 256),
		rehash:     make(chan bool),
		meta:       make(chan *ClientComMessage, 128),
		userStatus: make(chan *userStatusReq, 128),
		shutdown:   make(chan chan<- bool),
	}

	statsRegisterInt("LiveTopics")
	statsRegisterInt("TotalTopics")
	// SessionStore 在新建和删除连接时异步更新这两个指标。
	// 必须在 HTTP 接入开始前注册，否则首个连接会让 statsUpdater 崩溃。
	statsRegisterInt("LiveSessions")
	statsRegisterInt("TotalSessions")

	statsRegisterInt("IncomingMessagesWebsockTotal")
	statsRegisterInt("OutgoingMessagesWebsockTotal")

	statsRegisterInt("IncomingMessagesLongpollTotal")
	statsRegisterInt("OutgoingMessagesLongpollTotal")

	statsRegisterInt("IncomingMessagesGrpcTotal")
	statsRegisterInt("OutgoingMessagesGrpcTotal")

	statsRegisterInt("FileDownloadsTotal")
	statsRegisterInt("FileUploadsTotal")
	statsRegisterInt("FileProcessingClaims")
	statsRegisterInt("FileProcessingLeaseRecoveries")
	statsRegisterInt("FileProcessingCompleted")
	statsRegisterInt("FileProcessingRetries")
	statsRegisterInt("FileProcessingDeadLetters")
	statsRegisterInt("ResumableUploadChunks")
	statsRegisterInt("ResumableUploadLeaseConflicts")

	statsRegisterInt("CtrlCodesTotal2xx")
	statsRegisterInt("CtrlCodesTotal3xx")
	statsRegisterInt("CtrlCodesTotal4xx")
	statsRegisterInt("CtrlCodesTotal5xx")

	statsRegisterHistogram("RequestLatency", requestLatencyDistribution)
	statsRegisterHistogram("OutgoingMessageSize", outgoingMessageSizeDistribution)

	go h.run()

	// 初始化 'sys' Topic。它将被初始化为 master 或 proxy
	h.join <- &ClientComMessage{RcptTo: "sys", Original: "sys"}

	return h
}

// makeTopic 创建处于暂停状态的 Topic 路由并异步加载其持久化状态。
// 普通加入和定时消息唤醒共用此入口，避免两套初始化逻辑发生偏差。
func (h *Hub) makeTopic(join *ClientComMessage) *Topic {
	t := &Topic{
		name:      join.RcptTo,
		xoriginal: join.Original,
		isProxy:   globals.cluster.isRemoteTopic(join.RcptTo),
		sessions:  make(map[*Session]perSessionData),
		clientMsg: make(chan *ClientComMessage, 192),
		serverMsg: make(chan *ServerComMessage, 64),
		reg:       make(chan *ClientComMessage, 256),
		unreg:     make(chan *ClientComMessage, 256),
		meta:      make(chan *ClientComMessage, 64),
		perUser:   make(map[types.Uid]perUserData),
		exit:      make(chan *shutDown, 1),
	}
	if globals.cluster != nil {
		if t.isProxy {
			t.proxy = make(chan *ClusterResp, 128)
			t.masterNode = globals.cluster.topicOwner(t.name)
		} else {
			t.master = make(chan *ClusterSessUpdate, 8)
		}
	}
	t.markPaused(true)
	h.topicPut(join.RcptTo, t)
	go topicInit(t, join, h)
	return t
}

// run 启动并运行run处理流程。
func (h *Hub) run() {
	for {
		select {
		case join := <-h.join:
			// 处理订阅请求：
			// 1. 初始化 Topic
			// 1.1 如果请求新 Topic，创建它
			// 1.2 如果请求订阅现有 Topic：
			// 1.2.1 检查 Topic 是否已加载
			// 1.2.2 如果未加载，加载它
			// 1.2.3 如果无法加载（未找到），失败
			// 2. 检查访问权限，如适当则拒绝
			// 3. 将 Session 附加到 Topic
			// Topic 是否已加载？
			t := h.topicGet(join.RcptTo)
			if t == nil {
				h.makeTopic(join)
			} else {
				// 找到 Topic
				if t.isInactive() {
					// Topic 未就绪或正在删除
					if join.sess.inflightReqs != nil {
						join.sess.inflightReqs.Done()
					}
					join.sess.queueOut(ErrLockedReply(join, join.Timestamp))
					continue
				}
				// Topic 会检查访问权限并发送适当的 {ctrl}
				select {
				case t.reg <- join:
				default:
					if join.sess.inflightReqs != nil {
						join.sess.inflightReqs.Done()
					}
					join.sess.queueOut(ErrServiceUnavailableReply(join, join.Timestamp))
					logs.Err.Println("hub.join loop: topic's reg queue full", join.RcptTo, join.sess.sid,
						" - total queue len:", len(t.reg))
				}
			}

		case msg := <-h.schedule:
			// 到期消息可唤醒无在线会话的 Topic，随后走与普通发布相同的串行通道。
			dst := h.topicGet(msg.RcptTo)
			if dst == nil {
				dst = h.makeTopic(&ClientComMessage{
					Original: msg.Original,
					RcptTo:   msg.RcptTo,
					AsUser:   msg.AsUser,
				})
			}
			select {
			case dst.clientMsg <- msg:
			default:
				// 不删除数据库队列记录，调度器下一轮仍可重试。
				logs.Err.Printf("hub: scheduled queue is full for topic %s", dst.name)
			}

		case msg := <-h.routeCli:
			// 这是来自未订阅 Topic 的 Session 的消息
			// 如果 Topic 允许此类路由，将传入消息路由到 Topic
			if dst := h.topicGet(msg.RcptTo); dst != nil {
				// 一切正常，发送数据包到已知 Topic
				if dst.clientMsg != nil {
					select {
					case dst.clientMsg <- msg:
					default:
						logs.Err.Println("hub: topic's broadcast queue is full", dst.name)
					}
				} else {
					logs.Warn.Println("hub: invalid topic category for broadcast", dst.name)
				}
			} else if msg.Note == nil {
				// Topic 未知或离线
				// Note 会被静默忽略，其他消息会被报告为已接受，以防止
				// 客户端猜测有效的 Topic 名称

				// 检查 Topic 名称格式，且确认其既不在本地也不在集群远端节点
				if len(msg.RcptTo) < 3 || (!globals.cluster.isRemoteTopic(msg.RcptTo) &&
					!strings.HasPrefix(msg.RcptTo, "usr") &&
					!strings.HasPrefix(msg.RcptTo, "p2p") &&
					!strings.HasPrefix(msg.RcptTo, "grp") &&
					!strings.HasPrefix(msg.RcptTo, "chn") &&
					!strings.HasPrefix(msg.RcptTo, "fnd") &&
					!strings.HasPrefix(msg.RcptTo, "sys") &&
					!strings.HasPrefix(msg.RcptTo, "slf")) {
					logs.Warn.Printf("Hub: invalid or unknown cluster topic '%s'", msg.RcptTo)
					msg.sess.queueOut(ErrMalformed(msg.Id, msg.RcptTo, types.TimeNow()))
					return
				}

				logs.Info.Printf("Hub. Topic[%s] is unknown or offline", msg.RcptTo)

				msg.sess.queueOut(NoErrAcceptedExplicitTs(msg.Id, msg.RcptTo, types.TimeNow(), msg.Timestamp))
			}
		case msg := <-h.routeSrv:
			// 这是来自未订阅 Topic 连接的服务端消息
			// 如果 Topic 允许此类路由，将传入消息路由到 Topic
			if dst := h.topicGet(msg.RcptTo); dst != nil {
				// 一切正常，发送数据包到已知 Topic
				select {
				case dst.serverMsg <- msg:
				default:
					logs.Err.Println("hub: topic's broadcast queue is full", dst.name)
				}
			} else if (strings.HasPrefix(msg.RcptTo, "usr") || strings.HasPrefix(msg.RcptTo, "grp")) &&
				globals.cluster.isRemoteTopic(msg.RcptTo) {
				// 这是一个远程 Topic
				if err := globals.cluster.routeToTopicIntraCluster(msg.RcptTo, msg, msg.sess); err != nil {
					logs.Warn.Printf("hub: routing to '%s' failed", msg.RcptTo)
				}
			}
		case msg := <-h.meta:
			// 来自未附加到 Topic 的用户的元数据读取或更新
			if msg.Get != nil {
				if msg.MetaWhat == constMsgMetaDesc {
					go replyOfflineTopicGetDesc(msg.sess, msg)
				} else {
					go replyOfflineTopicGetSub(msg.sess, msg)
				}
			} else if msg.Set != nil {
				go replyOfflineTopicSetSub(msg.sess, msg)
			}

		case status := <-h.userStatus:
			// 暂停/激活用户的 Topic
			go h.topicsStateForUser(status.forUser, status.state == types.StateSuspended)

		case unreg := <-h.unreg:
			reason := StopNone
			if unreg.del {
				reason = StopDeleted
			}
			if unreg.forUser.IsZero() {
				// Topic 正在被垃圾回收或删除
				if err := h.topicUnreg(unreg.sess, unreg.rcptTo, unreg.pkt, reason); err != nil {
					logs.Err.Println("hub.topicUnreg failed:", err)
				}
			} else {
				// 用户正在被删除
				go h.stopTopicsForUser(unreg.forUser, reason, unreg.done)
			}

		case <-h.rehash:
			// 集群重新哈希。某些之前本地的 Topic 变为远程，反之亦然
			// 这些 Topic 必须在此节点关闭
			h.topics.Range(func(_, t any) bool {
				topic := t.(*Topic)
				// 处理两种情况：
				// 1. 主 Topic 已移动到其他节点
				// 2. 代理 Topic 运行在新的主节点上
				//    （即主 Topic 已移动到此节点）
				if topic.isProxy != globals.cluster.isRemoteTopic(topic.name) {
					h.topicUnreg(nil, topic.name, nil, StopRehashing)
				}
				return true
			})

			// 检查 'sys' Topic 是否已迁移到此节点
			if h.topicGet("sys") == nil && !globals.cluster.isRemoteTopic("sys") {
				// 是的，'sys' 已迁移到这里，初始化它
				// h.join 无缓冲，必须在另一个协程中调用，否则会死锁
				go func() {
					h.join <- &ClientComMessage{RcptTo: "sys", Original: "sys"}
				}()
			}

		case hubdone := <-h.shutdown:
			// 开始清理过程
			topicsdone := make(chan bool)
			topicCount := 0
			h.topics.Range(func(_, topic any) bool {
				topic.(*Topic).exit <- &shutDown{done: topicsdone}
				topicCount++
				return true
			})

			for range topicCount {
				<-topicsdone
			}

			logs.Info.Printf("Hub shutdown completed with %d topics", topicCount)

			// 让主协程知道我们已完成清理
			hubdone <- true

			return

		}
	}
}

// Update state of all Topic associated with the given 用户:
// * all p2p Topic with the given 用户
// * group Topic where the given 用户 is the owner.
// 'me' and fnd' are ignored here because they are direcly tied to the 用户 object.
func (h *Hub) topicsStateForUser(uid types.Uid, suspended bool) {
	h.topics.Range(func(name any, t any) bool {
		topic := t.(*Topic)
		if topic.cat == types.TopicCatMe || topic.cat == types.TopicCatFnd {
			return true
		}

		if _, isMember := topic.perUser[uid]; (topic.cat == types.TopicCatP2P && isMember) || topic.owner == uid {
			topic.markReadOnly(suspended)

			// Don't send "off" notification on suspension. They will be sent when the 用户 is evicted.
		}
		return true
	})
}

//topicUnreg删除或取消注册主题：
//
//案例：
// 1. Topic being deleted
// 1.1 Topic is online
// 1.1.1 If the requester is the owner or if it's the last sub in a p2p Topic (p2p may be sent internally when the last 用户 unsubscribes):
// 1.1.1.1 Tell Topic to stop accepting requests.
// 1.1.1.2 Hub deletes the Topic from 数据库
// 1.1.1.3 Hub unregisters the Topic
// 1.1.1.4 Hub informs the origin of success or failure
// 1.1.1.5 Hub forwards 请求 to Topic
// 1.1.1.6 Topic evicts all Session
// 1.1.1.7 Topic exits the run() loop
// 1.1.2 If the requester is not the owner
// 1.1.2.1 Send it to Topic to be treated like {leave unsub=true}
//
// 1.2 Topic is offline
// 1.2.1 If requester is the owner
// 1.2.1.1 Hub deletes Topic from 数据库
// 1.2.2 If not the owner
// 1.2.2.1 Delete 订阅 from DB
// 1.2.3 Hub informs the origin of success or failure
// 1.2.4 Send notification to subscribers that the Topic was deleted

// 2. Topic is just being unregistered (Topic is going offline)
// 2.1 Unregister it with no further action
func (h *Hub) topicUnreg(sess *Session, topic string, msg *ClientComMessage, reason int) error {
	now := types.TimeNow()

	// 在集群模式下，仅由 Master 主控节点在 Channel 彻底删除时触发单点推送清理
	if t := h.topicGet(topic); reason == StopDeleted && t != nil && !t.isProxy && (t.isChan || types.IsChannel(topic)) {
		if rcpt := pushForChanDelete(topic, now); rcpt != nil {
			sendPush(rcpt)
		}
	}

	if reason == StopDeleted {
		var asUid types.Uid
		if msg != nil {
			asUid = types.ParseUserId(msg.AsUser)
		}
		// 情况 1（注销并删除）
		if t := h.topicGet(topic); t != nil {
			// 情况 1.1: Topic 在线
			if (!asUid.IsZero() && t.owner == asUid) || (t.cat == types.TopicCatP2P && t.subsCount() < 2) {
				if t.isOfficialTopic() {
					if sess != nil {
						sess.queueOut(ErrPermissionDeniedReply(msg, now))
					}
					return types.ErrPermissionDenied
				}
				// 情况 1.1.1: 请求者是所有者，或者属于 p2p Topic 的最后一个订阅者
				t.markPaused(true)
				hard := true
				if msg != nil && msg.Del != nil {
					// 软删除对 p2p Topic 没有意义。
					hard = msg.Del.Hard || t.cat == types.TopicCatP2P
				}
				if err := store.Topics.Delete(topic, t.isChan, hard); err != nil {
					t.markPaused(false)
					if sess != nil {
						sess.queueOut(ErrUnknownReply(msg, now))
					}
					return err
				}
				if sess != nil {
					sess.queueOut(NoErrReply(msg, now))
				}

				if t.isChan {
					// 通知频道订阅者频道已被删除。
					sendPush(pushForChanDelete(t.name, now))
				}

				h.topicDel(topic)
				t.markDeleted()
				t.exit <- &shutDown{reason: StopDeleted}
				statsInc("LiveTopics", -1)
			} else {
				// 情况 1.1.2: 请求者不是所有者或非空 P2P。
				msg.MetaWhat = constMsgDelTopic
				msg.sess = sess
				t.meta <- msg
			}
		} else {
			// 情况 1.2: Topic 离线。
			if stored, getErr := store.Topics.Get(topic); getErr != nil {
				if sess != nil {
					sess.queueOut(ErrUnknownReply(msg, now))
				}
				return getErr
			} else if stored != nil {
				policy, policyErr := officialPolicyFromAux(topic, stored.Aux)
				if policyErr != nil {
					if sess != nil {
						sess.queueOut(ErrUnknownReply(msg, now))
					}
					return policyErr
				}
				if policy != nil && policy.Official && msg != nil &&
					!types.IsChannel(msg.Original) {
					if sess != nil {
						sess.queueOut(ErrPermissionDeniedReply(msg, now))
					}
					return types.ErrPermissionDenied
				}
			}

			// 用户是否为频道订阅者？使用 chnABC 代替 grpABC 且仅获取此用户的订阅。
			var opts *types.QueryOpt
			if types.IsChannel(msg.Original) {
				topic = msg.Original
				opts = &types.QueryOpt{User: asUid}
			}

			// 获取非频道 Topic 的所有订阅者：我们需要知道还剩多少人并通知他们。
			// 对于频道用户仅获取一条订阅。
			subs, err := store.Topics.GetSubs(topic, opts)
			if err != nil {
				sess.queueOut(ErrUnknownReply(msg, now))
				return err
			}

			tcat := topicCat(topic)
			if len(subs) == 0 {
				if tcat == types.TopicCatP2P {
					// 无订阅者：直接删除。
					store.Topics.Delete(topic, false, true)
				}
				sess.queueOut(InfoNoActionReply(msg, now))
				return nil
			}

			// 查找当前用户的订阅。
			var sub *types.Subscription
			user := asUid.String()
			for i := range subs {
				if subs[i].User == user {
					sub = &subs[i]
					break
				}
			}

			if sub == nil {
				// 如果用户没有订阅，告知一切正常
				sess.queueOut(InfoNoActionReply(msg, now))
				return nil
			}

			if !(sub.ModeGiven & sub.ModeWant).IsOwner() {
				// 情况 1.2.2.1 不是所有者，但可能是 P2P Topic 中的最后一个订阅。

				if tcat == types.TopicCatP2P && len(subs) < 2 {
					// 这是 P2P Topic 且少于 2 个订阅，删除整个 Topic
					if err := store.Topics.Delete(topic, false, msg.Del.Hard); err != nil {
						sess.queueOut(ErrUnknownReply(msg, now))
						return err
					}
					// 通知插件 Topic 已被删除。
					pluginTopic(&Topic{name: topic}, plgActDel)
				} else if err := store.Subs.Delete(topic, asUid); err != nil {
					// 非 P2P 或仍剩有超过 1 个订阅。
					// 仅删除用户自身的订阅
					if err == types.ErrNotFound {
						sess.queueOut(InfoNoActionReply(msg, now))
						err = nil
					} else {
						sess.queueOut(ErrUnknownReply(msg, now))
					}
					return err
				}

				// 通知用户的其他 Session 订阅已消失
				presSingleUserOfflineOffline(asUid, msg.Original, "gone", nilPresParams, sess.sid)
				if tcat == types.TopicCatP2P && len(subs) == 2 {
					uname1 := asUid.UserId()
					uid2 := types.ParseUserId(msg.Original)
					// 告知 user1 停止向 user2 发送更新，而不将更改传递给 user1 的 Session。
					presSingleUserOfflineOffline(asUid, uid2.UserId(), "?none+rem", nilPresParams, "")
					// 不要改变 user1 的在线状态，只需让 user2 停止交换通知。
					// 告知 user2 用户 1 已离线，但如果 user1 重新订阅则允许其继续发送更新。
					presSingleUserOfflineOffline(uid2, uname1, "off", nilPresParams, "")
				}

				// 通知插件订阅已被删除。
				pluginSubscription(sub, plgActDel)
			} else {
				// 情况 1.2.1.1: 所有者，从数据库删除群组 Topic。只有群组 Topic 拥有所有者。
				// 我们不知道该群组 Topic 是否为频道，但按频道清理除了微小的性能开销外没有危害。
				if err := store.Topics.Delete(topic, true, msg.Del.Hard); err != nil {
					sess.queueOut(ErrUnknownReply(msg, now))
					return err
				}

				// 通知订阅者群组 Topic 已消失。
				presSubsOfflineOffline(topic, tcat, subs, "gone", &presParams{}, sess.sid)

				// 通知频道订阅者频道已被删除。
				// 如果 Topic 不是频道，推送不会交付给任何人。
				sendPush(pushForChanDelete(topic, now))

				// 通知插件 Topic 已被删除。
				pluginTopic(&Topic{name: topic}, plgActDel)
			}

			sess.queueOut(NoErrReply(msg, now))
		}
	} else {
		// 情况 2: 仅注销下线。
		// 如果 t 为 nil，表示未注册，无需做任何处理
		if t := h.topicGet(topic); t != nil {
			t.markDeleted()
			h.topicDel(topic)

			t.exit <- &shutDown{reason: reason}

			statsInc("LiveTopics", -1)
		}

		// 如果 Topic 因定时器或重新哈希被杀掉，sess 和 msg 可能为 nil。
		if sess != nil && msg != nil {
			sess.queueOut(NoErrReply(msg, now))
		}
	}

	return nil
}

// 终止与给定用户关联的所有 Topic：
// * 与给定用户的所有 p2p Topic
// * 给定用户作为所有者的群组 Topic
// * 用户的 'me'、'fnd'、'slf' Topic。
func (h *Hub) stopTopicsForUser(uid types.Uid, reason int, alldone chan<- bool) {
	var done chan bool
	if alldone != nil {
		done = make(chan bool, 128)
	}

	count := 0
	h.topics.Range(func(name any, t any) bool {
		topic := t.(*Topic)
		if _, isMember := topic.perUser[uid]; (topic.cat != types.TopicCatGrp && isMember) ||
			topic.owner == uid {
			topic.markDeleted()
			h.topics.Delete(name)

			// 除非有其他协程同时尝试终止，否则此调用是非阻塞的。
			topic.exit <- &shutDown{reason: reason, done: done}

			// 此处仅向 p2p Topic 发送通知。
			if topic.cat == types.TopicCatP2P && len(topic.perUser) == 2 {
				presSingleUserOfflineOffline(topic.p2pOtherUser(uid), uid.UserId(), "gone", nilPresParams, "")
			}
			count++
		}
		return true
	})

	statsInc("LiveTopics", -count)

	if alldone != nil {
		for range count {
			<-done
		}
		alldone <- true
	}
}

// replyOfflineTopicGetDesc 从数据库读取基础 Topic 描述元数据。
// 请求者可以已订阅或未订阅该 Topic。
func replyOfflineTopicGetDesc(sess *Session, msg *ClientComMessage) {
	now := types.TimeNow()
	desc := &MsgTopicDesc{}
	asUid := types.ParseUserId(msg.AsUser)
	topic := msg.RcptTo
	var isSuspended bool

	if strings.HasPrefix(topic, "grp") || topic == "sys" {
		stopic, err := store.Topics.Get(topic)
		if err != nil {
			logs.Info.Println("replyOfflineTopicGetDesc", err)
			sess.queueOut(decodeStoreErrorExplicitTs(err, msg.Id, msg.Original, now, msg.Timestamp, nil))
			return
		}
		if stopic == nil {
			sess.queueOut(ErrTopicNotFoundReply(msg, now))
			return
		}

		isSuspended = stopic.State == types.StateSuspended

		desc.CreatedAt = &stopic.CreatedAt
		desc.UpdatedAt = &stopic.UpdatedAt
		desc.Public = stopic.Public
		desc.Trusted = stopic.Trusted
		desc.IsChan = stopic.UseBt
		desc.SubCnt = stopic.SubCnt
		if stopic.Owner == msg.AsUser {
			desc.DefaultAcs = &MsgDefaultAcsMode{
				Auth: stopic.Access.Auth.String(),
				Anon: stopic.Access.Anon.String(),
			}
		}
		// 汇报适当的访问级别。如果存在订阅，可以在下方被覆盖。
		desc.Acs = &MsgAccessMode{}
		switch sess.authLvl {
		case auth.LevelAuth, auth.LevelRoot:
			desc.Acs.Mode = stopic.Access.Auth.String()
		case auth.LevelAnon:
			desc.Acs.Mode = stopic.Access.Anon.String()
		}
	} else {
		// 'me' 和 p2p Topic
		uid := types.ZeroUid
		if strings.HasPrefix(topic, "usr") {
			// 用户指定为 usrXXX
			uid = types.ParseUserId(topic)
			topic = asUid.P2PName(uid)
		} else if strings.HasPrefix(topic, "p2p") {
			// 用户指定为 p2pXXXYYY
			uid1, uid2, _ := types.ParseP2P(topic)
			if uid1 == asUid {
				uid = uid2
			} else if uid2 == asUid {
				uid = uid1
			}
		}

		if uid.IsZero() {
			logs.Warn.Println("replyOfflineTopicGetDesc: malformed p2p topic name")
			sess.queueOut(ErrMalformedReply(msg, now))
			return
		}

		suser, err := store.Users.Get(uid)
		if err != nil {
			sess.queueOut(decodeStoreErrorExplicitTs(err, msg.Id, msg.Original, now, msg.Timestamp, nil))
			return
		}
		if suser == nil {
			sess.queueOut(ErrUserNotFoundReply(msg, now))
			return
		}
		isSuspended = suser.State == types.StateSuspended

		desc.CreatedAt = &suser.CreatedAt
		desc.UpdatedAt = &suser.UpdatedAt
		desc.Public = suser.Public
		desc.Trusted = suser.Trusted
		if sess.authLvl == auth.LevelRoot {
			desc.State = suser.State.String()
		}

		// 汇报适当的访问级别。如果存在订阅，可以在下方被覆盖。
		desc.Acs = &MsgAccessMode{}
		switch sess.authLvl {
		case auth.LevelAuth, auth.LevelRoot:
			desc.Acs.Mode = suser.Access.Auth.String()
		case auth.LevelAnon:
			desc.Acs.Mode = suser.Access.Anon.String()
		}
	}

	subTopic := topic
	if types.IsChannel(msg.Original) {
		subTopic = msg.Original
	}
	sub, err := store.Subs.Get(subTopic, asUid, false)
	if err != nil {
		logs.Warn.Println("replyOfflineTopicGetDesc:", err)
		sess.queueOut(decodeStoreErrorExplicitTs(err, msg.Id, msg.Original, now, msg.Timestamp, nil))
		return
	}

	if sub != nil {
		desc.Private = sub.Private
		mode := sub.ModeGiven & sub.ModeWant
		if isSuspended && sess.authLvl != auth.LevelRoot {
			// 被挂起的 Topic / 用户，非 Root 用户不得给予任何 AW (Approve & Write) 权限
			mode = mode &^ (types.ModeWrite | types.ModeApprove)
		}
		desc.Acs = &MsgAccessMode{
			Want:  sub.ModeWant.String(),
			Given: sub.ModeGiven.String(),
			Mode:  mode.String(),
			Role:  topicRoleFromAccess(mode, desc.IsChan, types.IsChannel(sub.Topic)),
		}
	}

	sess.queueOut(&ServerComMessage{
		Meta: &MsgServerMeta{
			Id: msg.Id, Topic: msg.Original, Timestamp: &now, Desc: desc,
		},
	})
}

// replyOfflineTopicGetSub 从数据库读取用户的订阅信息。
// 仅允许获取自身的订阅。
// 请求者必须已订阅，但不一定处于附加挂载状态。
func replyOfflineTopicGetSub(sess *Session, msg *ClientComMessage) {
	now := types.TimeNow()

	if msg.Get.Sub != nil && msg.Get.Sub.User != "" && msg.Get.Sub.User != msg.AsUser {
		sess.queueOut(ErrPermissionDeniedReply(msg, now))
		return
	}

	topicName := msg.RcptTo
	if types.IsChannel(msg.Original) {
		topicName = msg.Original
	}

	ssub, err := store.Subs.Get(topicName, types.ParseUserId(msg.AsUser), true)
	if err != nil {
		logs.Warn.Println("replyOfflineTopicGetSub:", err)
		sess.queueOut(decodeStoreErrorExplicitTs(err, msg.Id, msg.Original, now, msg.Timestamp, nil))
		return
	}

	if ssub == nil {
		sess.queueOut(ErrNotFoundExplicitTs(msg.Id, msg.Original, now, msg.Timestamp))
		return
	}

	sub := MsgTopicSub{}
	if ssub.DeletedAt == nil {
		sub.UpdatedAt = &ssub.UpdatedAt
		sub.Acs = MsgAccessMode{
			Want:  ssub.ModeWant.String(),
			Given: ssub.ModeGiven.String(),
			Mode:  (ssub.ModeGiven & ssub.ModeWant).String(),
			Role: topicRoleFromAccess(ssub.ModeGiven&ssub.ModeWant,
				types.IsChannel(msg.Original), types.IsChannel(ssub.Topic)),
		}
		// Fnd 是非对称的：desc.private 是 string，但 sub.private 是 []string。
		if types.GetTopicCat(msg.RcptTo) != types.TopicCatFnd {
			sub.Private = ssub.Private
		}
		sub.User = types.ParseUid(ssub.User).UserId()

		if (ssub.ModeGiven & ssub.ModeWant).IsReader() && (ssub.ModeWant & ssub.ModeGiven).IsJoiner() {
			sub.DelId = ssub.DelId
			sub.ReadSeqId = ssub.ReadSeqId
			sub.RecvSeqId = ssub.RecvSeqId
		}
	} else {
		sub.DeletedAt = ssub.DeletedAt
	}

	sess.queueOut(&ServerComMessage{
		Meta: &MsgServerMeta{
			Id: msg.Id, Topic: msg.Original, Timestamp: &now, Sub: []MsgTopicSub{sub},
		},
	})
}

// replyOfflineTopicSetSub 在 Topic 未加载到内存时更新 Desc.Private 和 Sub.Mode。
// 仅更新 Private 和 Mode，且仅对请求者有效。请求者必须已订阅该 Topic，但无需附加挂载。
func replyOfflineTopicSetSub(sess *Session, msg *ClientComMessage) {
	now := types.TimeNow()

	if msg.Set.Sub != nil && msg.Set.Sub.Role != "" {
		// 成员角色变更需要加载 Topic 的完整 ACL 与成员缓存，调用者必须先订阅 Topic。
		sess.queueOut(ErrAttachFirst(msg, now))
		return
	}
	if (msg.Set.Desc == nil || msg.Set.Desc.Private == nil) && (msg.Set.Sub == nil || msg.Set.Sub.Mode == "") {
		sess.queueOut(InfoNotModifiedReply(msg, now))
		return
	}

	if msg.Set.Sub != nil && msg.Set.Sub.User != "" && msg.Set.Sub.User != msg.AsUser {
		sess.queueOut(ErrPermissionDeniedReply(msg, now))
		return
	}

	asUid := types.ParseUserId(msg.AsUser)

	topicName := msg.RcptTo
	if types.IsChannel(msg.Original) {
		topicName = msg.Original
	}

	sub, err := store.Subs.Get(topicName, asUid, false)
	if err != nil {
		logs.Warn.Println("replyOfflineTopicSetSub get sub:", err)
		sess.queueOut(decodeStoreErrorExplicitTs(err, msg.Id, msg.Original, now, msg.Timestamp, nil))
		return
	}

	if sub == nil {
		sess.queueOut(ErrNotFoundExplicitTs(msg.Id, msg.Original, now, msg.Timestamp))
		return
	}

	update := make(map[string]any)
	if msg.Set.Desc != nil && msg.Set.Desc.Private != nil {
		private, ok := msg.Set.Desc.Private.(map[string]any)
		if !ok {
			update = map[string]any{"Private": msg.Set.Desc.Private}
		} else if private, changed := mergeInterfaces(sub.Private, private); changed {
			update = map[string]any{"Private": private}
		}
	}

	if msg.Set.Sub != nil && msg.Set.Sub.Mode != "" {
		var modeWant types.AccessMode
		if err = modeWant.UnmarshalText([]byte(msg.Set.Sub.Mode)); err != nil {
			logs.Warn.Println("replyOfflineTopicSetSub mode:", err)
			sess.queueOut(decodeStoreErrorExplicitTs(err, msg.Id, msg.Original, now, msg.Timestamp, nil))
			return
		}

		if modeWant.IsOwner() != sub.ModeWant.IsOwner() {
			// 不在此处修改所有权。
			sess.queueOut(ErrPermissionDeniedReply(msg, now))
			return
		}

		if types.GetTopicCat(msg.RcptTo) == types.TopicCatP2P {
			// 对于 P2P Topic，忽略超出 typesModeCP2P 的请求，且不允许移除 'A' 权限。
			modeWant = modeWant&globals.typesModeCP2P | types.ModeApprove
		}

		if modeWant != sub.ModeWant {
			update["ModeWant"] = modeWant
			// 缓存备用
			sub.ModeWant = modeWant
		}
	}

	if len(update) > 0 {
		err = store.Subs.Update(topicName, asUid, update)
		if err != nil {
			logs.Warn.Println("replyOfflineTopicSetSub update:", err)
			sess.queueOut(decodeStoreErrorExplicitTs(err, msg.Id, msg.Original, now, msg.Timestamp, nil))
		} else {
			var params any
			if update["ModeWant"] != nil {
				params = map[string]any{
					"acs": MsgAccessMode{
						Given: sub.ModeGiven.String(),
						Want:  sub.ModeWant.String(),
						Mode:  (sub.ModeGiven & sub.ModeWant).String(),
						Role: topicRoleFromAccess(sub.ModeGiven&sub.ModeWant,
							types.IsChannel(msg.Original), types.IsChannel(sub.Topic)),
					},
				}
			}
			sess.queueOut(NoErrParamsReply(msg, now, params))
		}
	} else {
		sess.queueOut(InfoNotModifiedReply(msg, now))
	}
}
