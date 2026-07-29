package server

import (
	"container/heap"

	"chat/server/logs"
	"chat/server/push"
	"chat/server/store"
	"chat/server/store/types"
)

const (
	// 未读计数器更新返回码
	// 计数器未初始化，IO 挂起
	unreadUpdateIOPending = -1
	// 计数器初始化错误
	unreadUpdateError = -2
)

// UserCacheReq 包含用于变更一个或多个用户缓存条目的数据
type UserCacheReq struct {
	// 集群模式下发送此请求的节点名称，否则不设置
	Node string

	// 当单个用户的未读消息数更新或用户被删除时设置
	UserId types.Uid
	// 当 Topic 的订阅者数量更新时设置
	UserIdList []types.Uid
	// 未读计数（UserId 已设置）
	Unread int

	// 如果设置了 UserId：将 Unread 计数视为增量而非最终值
	// 如果设置了 UserIdList：增加 (Inc == true) 或减少订阅计数
	Inc bool
	// 用户正在被删除，从缓存中移除用户
	Gone bool

	// 可选的推送通知
	PushRcpt *push.Receipt
}

// userCacheEntry 保存用户缓存Entry的数据和运行状态。
type userCacheEntry struct {
	// unread 保存unread。
	unread int
	// topics 保存topics。
	topics int
}

// 从数据库读取未读计数器时保留的更新条目
type bufferedUpdate struct {
	// val 保存val。
	val int
	// inc 保存inc。
	inc bool
}

// ioResult 保存io结果的数据和运行状态。
type ioResult struct {
	// counts 按键索引counts。
	counts map[types.Uid]int
	// err 保存err。
	err error
}

// 表示待处理的推送通知回执
type pendingReceipt struct {
	// 当前正在从数据库读取的未读计数器数量
	pendingIOs int
	// 索引由 update 使用，由 heap.Interface 方法维护
	index int
	// 底层回执
	rcpt *push.Receipt
}

// 待处理推送按优先级队列组织（优先级 = 待处理 IO 数量）
// 可快速发现已准备好发送的回执（待处理 IO 数为 0）
type pendingReceiptsQueue []*pendingReceipt

// Heap 接口方法
func (pq pendingReceiptsQueue) Len() int { return len(pq) }

// Less 报告排序位置 i 的元素是否应位于位置 j 之前。
func (pq pendingReceiptsQueue) Less(i, j int) bool {
	// 我们希望 Pop 返回最高优先级而非最低优先级，因此使用大于号
	return pq[i].pendingIOs < pq[j].pendingIOs
}

// Swap 交换排序集合中两个位置的元素。
func (pq pendingReceiptsQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}

// Push 完成Push所需的内部处理。
func (pq *pendingReceiptsQueue) Push(x any) {
	n := len(*pq)
	item := x.(*pendingReceipt)
	item.index = n
	*pq = append(*pq, item)
}

// Pop 完成Pop所需的内部处理。
func (pq *pendingReceiptsQueue) Pop() any {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil  // 避免内存泄漏
	item.index = -1 // 安全起见
	*pq = old[0 : n-1]
	return item
}

// fix 完成fix所需的内部处理。
func (pq *pendingReceiptsQueue) fix(index int) {
	heap.Fix(pq, index)
}

// 初始化用户缓存
func usersInit() {
	globals.usersUpdate = make(chan *UserCacheReq, 1024)

	go userUpdater()
}

// 关闭用户缓存
func usersShutdown() {
	if globals.usersUpdate != nil {
		globals.usersUpdate <- nil
	}
}

// usersUpdateUnread 完成usersUpdateUnread所需的内部处理。
func usersUpdateUnread(uid types.Uid, val int, inc bool) {
	if globals.usersUpdate == nil || (val == 0 && inc) {
		return
	}

	upd := &UserCacheReq{UserId: uid, Unread: val, Inc: inc}
	if globals.cluster.isRemoteTopic(uid.UserId()) {
		// 发送请求到拥有该用户的远程节点
		globals.cluster.routeUserReq(upd)
	} else {
		select {
		case globals.usersUpdate <- upd:
		default:
		}
	}
}

// 开始跟踪单个用户。用于缓存管理
// 'add' 增加/减少用户订阅的 Topic 数量
func usersRegisterUser(uid types.Uid, add bool) {
	if globals.usersUpdate == nil {
		return
	}

	upd := &UserCacheReq{UserIdList: make([]types.Uid, 1), Inc: add}
	upd.UserIdList[0] = uid

	if globals.cluster.isRemoteTopic(uid.UserId()) {
		// 发送请求到拥有该用户的远程节点
		globals.cluster.routeUserReq(upd)
	} else {
		select {
		case globals.usersUpdate <- upd:
		default:
		}
	}
}

// 停止跟踪用户并从缓存中移除
func usersRemoveUser(uid types.Uid) {
	if globals.usersUpdate == nil {
		return
	}

	upd := &UserCacheReq{UserId: uid, Gone: true}
	if !globals.cluster.isRemoteTopic(uid.UserId()) {
		select {
		case globals.usersUpdate <- upd:
		default:
		}
	}

	if globals.cluster != nil {
		// 即使用户是本地的也向集群广播
		globals.cluster.routeUserReq(upd)
	}
}

// 将用户计入活跃 Topic 的成员。用于缓存管理
// 在集群模式下，此方法仅在 Topic 为本地时调用：
// globals.cluster.isRemoteTopic(t.name) == false
func usersRegisterTopic(t *Topic, add bool) {
	if globals.usersUpdate == nil {
		return
	}

	if t.cat == types.TopicCatFnd || t.cat == types.TopicCatMe {
		// 忽略 me 和 fnd Topic
		return
	}

	local := &UserCacheReq{Inc: add}

	// 在集群模式下，UID 可能是本地或远程的。在本地处理本地 UID，
	// 将远程 UID 发送到其他集群节点处理。UID 可能需要发送到多个节点
	remote := &UserCacheReq{Inc: add}
	for uid, pud := range t.perUser {
		if pud.isChan {
			// 跳过 Channel 订阅者
			continue
		}
		if globals.cluster.isRemoteTopic(uid.UserId()) {
			remote.UserIdList = append(remote.UserIdList, uid)
		} else {
			local.UserIdList = append(local.UserIdList, uid)
		}
	}

	if len(remote.UserIdList) > 0 {
		globals.cluster.routeUserReq(remote)
	}

	if len(local.UserIdList) > 0 {
		select {
		case globals.usersUpdate <- local:
		default:
			logs.Err.Println("User cache: globals.usersUpdate queue full: ", len(globals.usersUpdate))
		}
	}
}

// usersRequestFromCluster 处理来自其他集群节点的请求
func usersRequestFromCluster(req *UserCacheReq) {
	if globals.usersUpdate == nil {
		return
	}

	select {
	case globals.usersUpdate <- req:
	default:
	}
}

// usersCache 保存users缓存的共享实例或运行状态。
var usersCache map[types.Uid]userCacheEntry

// 处理用户缓存更新的协程
func userUpdater() {
	// 缓存未读计数器和用户订阅的 Topic 数量
	usersCache = make(map[types.Uid]userCacheEntry)

	// 每个用户因 IO 阻塞的未读计数器更新。IO 完成时刷新
	perUserBuffers := make(map[types.Uid][]bufferedUpdate)

	// 因 IO 阻塞的推送通知接收者（部分接收者的未读计数器
	// 正在从数据库读取），按用户分组
	perUserPendingReceipts := make(map[types.Uid][]*pendingReceipt)

	// 所有待处理推送回执按待处理 IO 数量组织的优先级队列
	receiptQueue := pendingReceiptsQueue{}

	// IO 回调队列
	ioDone := make(chan *ioResult, 1024)

	unreadUpdater := func(uids []types.Uid, vals []int, inc bool) map[types.Uid]int {
		var dbPending []types.Uid
		counts := make(map[types.Uid]int, len(uids))
		for i, uid := range uids {
			counts[uid] = 0
			uce, ok := usersCache[uid]
			if !ok {
				logs.Err.Println("ERROR: attempt to update unread count for user who has not been loaded", uid)
				counts[uid] = unreadUpdateError
				continue
			}

			val := vals[i]
			if uce.unread < 0 {
				// 未读计数器尚未初始化。是否启动数据库读取？
				if updateBuf, ioInProgress := perUserBuffers[uid]; ioInProgress {
					// 缓存此更新
					updateBuf = append(updateBuf, bufferedUpdate{val: val, inc: inc})
					perUserBuffers[uid] = updateBuf
				} else {
					// 调度从数据库读取计数器
					updateBuf = []bufferedUpdate{}
					perUserBuffers[uid] = updateBuf
					dbPending = append(dbPending, uid)
				}
				counts[uid] = unreadUpdateIOPending
				continue

			} else if inc {
				uce.unread += val
			} else {
				uce.unread = val
			}

			usersCache[uid] = uce
			counts[uid] = uce.unread
		}

		if len(dbPending) > 0 {
			go func() {
				dbUnread, err := store.Users.GetUnreadCount(dbPending...)
				if err != nil {
					logs.Warn.Println("users: failed to load unread count: ", err)
				}
				ioDone <- &ioResult{counts: dbUnread, err: err}
			}()
		}

		return counts
	}

	for {
		select {
		case io := <-ioDone:
			// 未读计数器读取完成
			for uid, count := range io.counts {
				updateBuf, ok := perUserBuffers[uid]
				// 停止缓存更新。新更新将正常处理
				delete(perUserBuffers, uid)
				if io.err != nil {
					continue
				}

				// 更新计数器
				if ok {
					for _, upd := range updateBuf {
						if upd.inc {
							count += upd.val
						} else {
							count = upd.val
						}
					}
				} else {
					logs.Warn.Println("ERROR: io didn't have an update buffer, uid", uid)
				}

				if uce, ok := usersCache[uid]; ok {
					if uce.unread >= 0 {
						logs.Warn.Println("users: unread count double initialization, uid", uid)
					}
					uce.unread = count
					usersCache[uid] = uce
				} else {
					logs.Warn.Println("users: missing users cache entry after IO completion, uid", uid)
				}

				// 未读计数器已初始化完成，处理待处理的推送通知回执
				// 减少该用户待处理推送回执的 IO 计数
				if pendingReceipts, ok := perUserPendingReceipts[uid]; ok {
					for _, pp := range pendingReceipts {
						pp.pendingIOs--
						receiptQueue.fix(pp.index)
					}
					delete(perUserPendingReceipts, uid)
				}
			}

			if io.err != nil {
				logs.Err.Println("users: failed to read unread count:", io.err)
				continue
			}

			// 发送就绪的回执
			for receiptQueue.Len() > 0 && receiptQueue[0].pendingIOs == 0 {
				rcpt := heap.Pop(&receiptQueue).(*pendingReceipt).rcpt
				for uid, rcptTo := range rcpt.To {
					if uce, ok := usersCache[uid]; ok && uce.unread >= 0 {
						rcptTo.Unread = uce.unread
						rcpt.To[uid] = rcptTo
					}
				}
				push.Push(rcpt)
			}
		case upd := <-globals.usersUpdate:
			if globals.shuttingDown {
				// 如果正在关闭，不处理任何请求
				// 忽略所有调用
				continue
			}

			// 请求关闭
			if upd == nil {
				globals.usersUpdate = nil
				// 无需关闭 Channel
				goto Exit
			}

			// 请求发送推送通知
			if upd.PushRcpt != nil {
				// 正在从数据库读取未读计数的用户 UID 列表
				pendingUsers := []types.Uid{}

				allUids := make([]types.Uid, 0, len(upd.PushRcpt.To))
				allDeltas := make([]int, 0, len(upd.PushRcpt.To))
				for uid, r := range upd.PushRcpt.To {
					allUids = append(allUids, uid)
					delta := 0
					if r.ShouldIncrementUnreadCountInCache {
						delta = 1
					}
					allDeltas = append(allDeltas, delta)
				}

				allUnread := unreadUpdater(allUids, allDeltas, true)
				for uid, unread := range allUnread {
					rcptTo := upd.PushRcpt.To[uid]
					// 处理更新
					if unread >= 0 {
						rcptTo.Unread = unread
						upd.PushRcpt.To[uid] = rcptTo
					} else if unread == unreadUpdateIOPending {
						pendingUsers = append(pendingUsers, uid)
					}
				}

				if len(pendingUsers) == 0 {
					// 所有数据都在内存中，直接发送推送
					push.Push(upd.PushRcpt)
				} else {
					// 正在等待 IO。将此回执加入队列
					pp := &pendingReceipt{
						pendingIOs: len(pendingUsers),
						rcpt:       upd.PushRcpt,
					}
					for _, uid := range pendingUsers {
						var queue []*pendingReceipt
						var ok bool
						if queue, ok = perUserPendingReceipts[uid]; !ok {
							queue = []*pendingReceipt{}
						}
						perUserPendingReceipts[uid] = append(queue, pp)
					}
					heap.Push(&receiptQueue, pp)
				}
				continue
			}

			// 请求从缓存中添加/移除用户
			if len(upd.UserIdList) > 0 {
				for _, uid := range upd.UserIdList {
					uce, ok := usersCache[uid]
					if upd.Inc {
						if !ok {
							// 这是新用户的注册
							// 此处不加载未读计数，设为 -1
							uce.unread = -1
						}
						uce.topics++
						usersCache[uid] = uce
					} else if ok {
						if uce.topics > 1 {
							uce.topics--
							usersCache[uid] = uce
						} else {
							// 从缓存中移除用户
							delete(usersCache, uid)
						}
					} else {
						// BUG!
						logs.Err.Println("ERROR: request to unregister user which has not been registered", uid)
					}
				}
				continue
			}

			if upd.Gone {
				// 用户正在被删除。不关心是否有记录
				delete(usersCache, upd.UserId)
				continue
			}

			// 请求更新单个用户的未读计数
			unreadUpdater([]types.Uid{upd.UserId}, []int{upd.Unread}, upd.Inc)
		}
	}

Exit:
	logs.Info.Println("users: shutdown")
}
