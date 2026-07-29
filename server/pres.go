// Package main 实现即时通信服务端的协议、路由和业务逻辑。
package main

import (
	"encoding/json"
	"strings"

	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
)

// presParams 定义创建在线通知的参数
type presParams struct {
	// userAgent 指示是否启用或满足用户Agent。
	userAgent string
	// seqID 保存序列号标识。
	seqID int
	// delID 保存del标识。
	delID int
	// delSeq 保存del序列号列表。
	delSeq []MsgRange

	// 执行操作的用户 Uid
	actor string
	// 操作目标
	target string
	// dWant 保存dWant。
	dWant string
	// dGiven 保存dGiven。
	dGiven string
}

// presFilters 保存presFilters的数据和运行状态。
type presFilters struct {
	// 仅向具有此访问模式非零的用户发送消息
	filterIn types.AccessMode
	// 排除具有此访问模式非零的用户
	filterOut types.AccessMode
	// 仅向指定 ID（如 'usrABC'）的单个用户的 Session 发送消息
	singleUser string
	// 不向指定 ID（如 'usrABC'）的用户的 Session 发送消息
	excludeUser string
}

// packAcs 完成packAcs所需的内部处理。
func (p *presParams) packAcs() *MsgAccessMode {
	if p.dWant != "" || p.dGiven != "" {
		return &MsgAccessMode{Want: p.dWant, Given: p.dGiven}
	}
	return nil
}

// 在线通知：将另一个用户添加到通知列表中，以通知在线状态和其他变更
func (t *Topic) addToPerSubs(topic string, online, enabled bool) {
	if topic == t.name {
		// 无需向自己推送更新
		return
	}

	// 跳过广播频道（Channel）订阅。频道订阅者不发送也不接收双向 Presence 在线状态通知
	if types.IsChannel(topic) {
		return
	}

	if uid1, uid2, err := types.ParseP2P(topic); err == nil {
		// 如果是 P2P Topic，按另一个用户的 ID 索引
		if uid1.UserId() == t.name {
			topic = uid2.UserId()
		} else {
			topic = uid1.UserId()
		}
	}

	t.perSubs[topic] = perSubsData{online: online, enabled: enabled}
}

// loadContacts 加载 Topic.perSubs 以支持在线通知
// perSubs 包含 (a) 用户希望通知其在线状态的 Topic，以及
// (b) 希望接收该用户通知的 Topic
func (t *Topic) loadContacts(uid types.Uid) error {
	subs, err := store.Users.GetSubs(uid)
	if err != nil {
		return err
	}

	for i := range subs {
		t.addToPerSubs(subs[i].Topic, false, (subs[i].ModeGiven & subs[i].ModeWant).IsPresencer())
	}
	return nil
}

// 本 Topic 收到来自 'me' Topic 的请求，开始/停止发送在线状态更新
// 源 Topic 在 'what' 中报告自身状态为 "on"、"off"、"gone" 或 "?unkn"
//
//		"on" - 请求者已上线
//		"off" - 请求者已下线
//	 "?none" - "+" 命令的锚点：请求者状态未知，不生成响应
//				且不转发给客户端
//	 "gone" - Topic 已删除或以其他方式消失 - 等同于 "off+remove"
//		"?unkn" - 请求者希望发起在线状态交换，但自身状态尚未知。此
//	 通知不转发给用户
//
// "+" 命令：
// "+en": 启用订阅，即开始接受来自 user2 的入站通知
// "+rem": 终止并移除订阅（订阅已删除）
// "+dis" 禁用订阅但不移除，与 "en" 相反
// "+en/rem/dis" 命令本身会从通知中剥离
func (t *Topic) procPresReq(fromUserID, what string, wantReply bool) string {
	if t.isInactive() {
		return ""
	}

	if t.isProxy {
		// 代理透传：在代理处维护对端状态没有意义，
		// 它是主节点的精确副本
		return what
	}

	var reqReply, onlineUpdate bool

	online := &onlineUpdate
	replyAs := "on"

	parts := strings.Split(what, "+")
	what = parts[0]
	cmd := ""
	if len(parts) > 1 {
		cmd = parts[1]
	}

	switch what {
	case "on":
		// 上线
		*online = true
	case "off":
		// 下线
	case "?none":
		// 在线状态无变化
		online = nil
		what = ""
	case "gone":
		// 下线：off+rem
		cmd = "rem"
	case "?unkn":
		// 在线状态无变化
		online = nil
		reqReply = true
		what = ""
	default:
		// 所有其他通知不在此处处理
		return what
	}

	if t.cat == types.TopicCatMe {
		// 查找联系人是否已列出
		if psd, ok := t.perSubs[fromUserID]; ok {
			if cmd == "rem" {
				replyAs = "off+rem"
				if !psd.enabled && what == "off" {
					// 如果之前已禁用，不发送冗余更新
					what = ""
				}
				delete(t.perSubs, fromUserID)
			} else {
				switch cmd {
				case "":
					// 启用/禁用状态无变化，且未添加或移除
					if !psd.enabled || online == nil || psd.online == *online {
						// 未启用或在线状态无变化 - 移除不必要的通知
						what = ""
					}
				case "en":
					if !psd.enabled {
						psd.enabled = true
					} else if online == nil || psd.online == *online {
						// 之前已激活且在线状态无变化：跳过不必要的更新
						what = ""
					}
				case "dis":
					if psd.enabled {
						psd.enabled = false
						if !psd.online {
							what = ""
						}
					} else {
						// 之前已禁用且因此离线，仍然离线 - 跳过更新
						what = ""
					}
				default:
					panic("presProcReq: unknown command '" + cmd + "'")
				}

				if !psd.enabled {
					// 如果不关心更新，让其他用户保持离线状态
					psd.online = false
				} else if online != nil {
					psd.online = *online
				}

				t.perSubs[fromUserID] = psd
			}
		} else if cmd != "rem" {
			// 收到来自新 Topic 的请求。这必须是新订阅。记录下来
			// 如果未知，记录为离线状态
			t.addToPerSubs(fromUserID, onlineUpdate, cmd == "en")

			if cmd != "en" {
				// 如果连接未启用，忽略更新
				what = ""
			}
		} else {
			// 不在列表中且被要求移除 - 忽略
			what = ""
		}
	}

	// 如果请求者的在线状态未变，不回复，否则会导致无限循环
	// wantReply 用于确保不发送不必要的 {pres}：
	// A[online, B:off] 到 B[online, A:off]: {pres A on}
	// B[online, A:on] 到 A[online, B:off]: {pres B on}
	// A[online, B:on] 到 B[online, A:on]: {pres A on} <<-- 不必要，这就是为什么需要 wantReply
	if (onlineUpdate || reqReply) && wantReply {
		globals.hub.routeSrv <- &ServerComMessage{
			// Topic 是 'me'，即使是群组 Topic；群组 Topic 会将 'me' 作为信号丢弃消息
			// 而不转发到 Session
			Pres: &MsgServerPres{
				Topic:     "me",
				What:      replyAs,
				Src:       t.name,
				WantReply: reqReply,
			},
			RcptTo: fromUserID,
		}
	}

	return what
}

// 获取用户特定的 Topic 名称以通知感兴趣的用户，或跳过通知
func notifyOnOrSkip(topic, what string, online bool) string {
	// 不在 Channel 上发送通知
	if types.IsChannel(topic) {
		return ""
	}

	// P2P 联系人通过 'me' 通知，群组 Topic 通过正确的 Topic 名称通知
	notifyOn := "me"
	if what == "upd" || what == "ua" {
		if !online {
			// 如果联系人离线，跳过 "upd" 和 "ua" 通知
			return ""
		}
		if types.GetTopicCat(topic) == types.TopicCatGrp {
			notifyOn = topic
		}
	}
	return notifyOn
}

// 发布用户更新到其订阅者：p2p 通过 'me' Topic，群组 Topic 通过 Topic 本身
// 情况 A：用户上线，"on"，ua
// 情况 B：用户下线，"off"，ua
// 情况 C：用户代理变更，"ua"，ua
// 情况 D：用户更新 'public'，"upd"
func (t *Topic) presUsersOfInterest(what, ua string) {
	parts := strings.Split(what, "+")
	wantReply := parts[0] == "on"
	goOffline := len(parts) > 1 && parts[1] == "dis"

	// 推送更新到订阅者
	for topic, psd := range t.perSubs {
		notifyOn := notifyOnOrSkip(topic, what, psd.online)
		if notifyOn == "" {
			continue
		}

		globals.hub.routeSrv <- &ServerComMessage{
			Pres: &MsgServerPres{
				Topic:     notifyOn,
				What:      what,
				Src:       t.name,
				UserAgent: ua,
				WantReply: wantReply,
			},
			RcptTo: topic,
		}

		if psd.online && goOffline {
			psd.online = false
			t.perSubs[topic] = psd
		}
	}
}

// 在用户的 'me' Topic 离线时，发布用户更新到其感兴趣的用户
// 情况 A：用户正在被删除，"gone"
func presUsersOfInterestOffline(uid types.Uid, subs []types.Subscription, what string) {
	// 推送更新到订阅者
	for i := range subs {
		notifyOn := notifyOnOrSkip(subs[i].Topic, what, true)
		if notifyOn == "" {
			continue
		}

		globals.hub.routeSrv <- &ServerComMessage{
			Pres: &MsgServerPres{
				Topic:     notifyOn,
				What:      what,
				Src:       uid.UserId(),
				WantReply: false,
			},
			RcptTo: subs[i].Topic,
		}
	}
}

// 向 Topic 订阅者报告在线变更，群组或 p2p
//
// 情况 I：用户加入 Topic，"on"
// 情况 J：用户离开 Topic，"off"
// 情况 K.2：用户更改了 WANT（可能获得默认 Given），"acs"
// 情况 L.1：管理员更改了 GIVEN，"acs" 发给受影响的用户
// 情况 L.3：管理员更改了 GIVEN（可能获得默认 WANT），"acs" 发给管理员
// 情况 M：Topic 不可访问（集群故障），"left" 发给所有当前在线用户
// 情况 V.2：消息软删除，"del" 仅发给一个用户
// 情况 W.2：消息硬删除，"del"
func (t *Topic) presSubsOnline(what, src string, params *presParams, filter *presFilters, skipSid string) {
	// 如果受影响用户与执行变更的用户相同，清空 'who'
	actor := params.actor
	target := params.target
	if actor == src {
		actor = ""
	}

	if target == src {
		target = ""
	}

	globals.hub.routeSrv <- &ServerComMessage{
		Pres: &MsgServerPres{
			Topic:       t.xoriginal,
			What:        what,
			Src:         src,
			Acs:         params.packAcs(),
			AcsActor:    actor,
			AcsTarget:   target,
			SeqId:       params.seqID,
			DelId:       params.delID,
			DelSeq:      params.delSeq,
			FilterIn:    int(filter.filterIn),
			FilterOut:   int(filter.filterOut),
			SingleUser:  filter.singleUser,
			ExcludeUser: filter.excludeUser,
		},
		RcptTo: t.name, SkipSid: skipSid,
	}
}

// userIsPresencer 如果用户（由 `uid` 指定）可以接收在线通知则返回 true
func (t *Topic) userIsPresencer(uid types.Uid) bool {
	var want, given types.AccessMode
	if uid.IsZero() {
		// 对于零 uid（通常用于代理 Session），返回所有权限的并集
		want = t.modeWantUnion
		given = t.modeGivenUnion
	} else {
		pud := t.perUser[uid]
		if pud.deleted {
			return false
		}
		want = pud.modeWant
		given = pud.modeGiven
	}
	return (want & given).IsPresencer()
}

// 直接向附加的 Session 发送通知，不通过 Topic 路由
// 这是必要的，因为当消息通过 Topic 路由时，Session 可能已经断开
func (t *Topic) presSubsOnlineDirect(what string, params *presParams, filter *presFilters, skipSid string) {
	msg := &ServerComMessage{
		Pres: &MsgServerPres{
			Topic:  t.xoriginal,
			What:   what,
			Acs:    params.packAcs(),
			SeqId:  params.seqID,
			DelId:  params.delID,
			DelSeq: params.delSeq,
		},
	}

	for s, pssd := range t.sessions {
		if !s.isMultiplex() {
			if skipSid == s.sid {
				continue
			}

			pud := t.perUser[pssd.uid]
			// 检查在线过滤器
			if pud.deleted || !presOfflineFilter(pud.modeGiven&pud.modeWant, what, filter) {
				continue
			}

			if filter != nil {
				if filter.singleUser != "" && filter.singleUser != pssd.uid.UserId() {
					continue
				}
				if filter.excludeUser != "" && filter.excludeUser == pssd.uid.UserId() {
					continue
				}
			}

			// 对于 p2p Topic，Topic 名称取决于接收者
			// 在此处更改指针是安全的，因为消息会在 queueOut 中序列化
			// 然后才放入 Channel
			t.prepareBroadcastableMessage(msg, pssd.uid, pssd.isChanSub)
		}
		s.queueOut(msg)
	}
}

// 向 Topic 列表传达 "Topic 不可访问（集群重新哈希或节点连接丢失）" 事件
// 提示客户端重新订阅 Topic
func (s *Session) presTermDirect(subs []string) {
	msg := &ServerComMessage{
		Pres: &MsgServerPres{Topic: "me", What: "term"},
	}
	for _, topic := range subs {
		msg.Pres.Src = topic
		s.queueOut(msg)
	}
}

// 发布到 Topic 订阅者当前在 Topic 中离线的 Session，在它们的 'me' 上
// 群组和 P2P
// 情况 E：Topic 上线，"on"
// 情况 F：Topic 下线，"off"
// 情况 G：Topic 更新 'public'，"upd"，who
// 情况 H：Topic 删除，"gone"
// 情况 K.3：用户更改了 WANT，"acs" 发给管理员
// 情况 L.4：管理员更改了 GIVEN，"acs" 发给管理员
// 情况 T：消息发送，"msg" 发给所有有 'R' 的用户
// 情况 W.1：消息硬删除，"del" 发给所有有 'R' 的用户
func (t *Topic) presSubsOffline(what string, params *presParams,
	filterSource *presFilters, filterTarget *presFilters, skipSid string, offlineOnly bool) {
	var skipTopic string
	if offlineOnly {
		skipTopic = t.name
	}

	for uid, pud := range t.perUser {
		if pud.deleted || !presOfflineFilter(pud.modeGiven&pud.modeWant, what, filterSource) {
			continue
		}

		user := uid.UserId()
		actor := params.actor
		target := params.target
		if actor == user {
			actor = ""
		}

		if target == user {
			target = ""
		}

		globals.hub.routeSrv <- &ServerComMessage{
			Pres: &MsgServerPres{
				Topic:       "me",
				What:        what,
				Src:         t.original(uid),
				Acs:         params.packAcs(),
				AcsActor:    actor,
				AcsTarget:   target,
				SeqId:       params.seqID,
				DelId:       params.delID,
				FilterIn:    int(filterTarget.filterIn),
				FilterOut:   int(filterTarget.filterOut),
				SingleUser:  filterTarget.singleUser,
				ExcludeUser: filterTarget.excludeUser,
				SkipTopic:   skipTopic,
			},
			RcptTo:  user,
			SkipSid: skipSid,
		}
	}
}

// 发布 {info what=read|recv|kp} 到 Topic 订阅者当前在 Topic 中离线的 Session，
// 在订阅者的 'me' 上。群组和 P2P
func (t *Topic) infoSubsOffline(from types.Uid, what string, seq int, skipSid string) {
	user := from.UserId()

	for uid, pud := range t.perUser {
		mode := pud.modeGiven & pud.modeWant
		if pud.deleted || !mode.IsPresencer() || !mode.IsReader() {
			continue
		}

		globals.hub.routeSrv <- &ServerComMessage{
			Info: &MsgServerInfo{
				Topic:     "me",
				Src:       t.original(uid),
				From:      user,
				What:      what,
				SeqId:     seq,
				SkipTopic: t.name,
			},
			RcptTo:  uid.UserId(),
			SkipSid: skipSid,
		}
	}
}

// 发布 {info what=call} 到订阅者的 Session，在订阅者的 'me' 上
func (t *Topic) infoCallSubsOffline(from string, target types.Uid, event string, seq int,
	sdp json.RawMessage, skipSid string, offlineOnly bool) {
	if target.IsZero() {
		logs.Err.Printf("callSubs could not find target: topic %s - from %s", t.name, from)
		return
	}
	pud := t.perUser[target]
	mode := pud.modeGiven & pud.modeWant
	if pud.deleted || !mode.IsPresencer() || !mode.IsReader() {
		return
	}
	msg := &ServerComMessage{
		Info: &MsgServerInfo{
			Topic:   "me",
			Src:     t.original(target),
			From:    from,
			What:    "call",
			Event:   event,
			SeqId:   seq,
			Payload: sdp,
		},
		RcptTo:  target.UserId(),
		SkipSid: skipSid,
	}
	if offlineOnly {
		msg.Info.SkipTopic = t.name
	}
	globals.hub.routeSrv <- msg
}

// 与 presSubsOffline 相同，但 Topic 未预先加载/初始化：离线 Topic，离线订阅者
func presSubsOfflineOffline(topic string, cat types.TopicCat, subs []types.Subscription, what string,
	params *presParams, skipSid string) {

	count := 0
	original := topic
	for i := range subs {
		sub := &subs[i]
		// 无论 'P' 如何都允许 "acs" 和 "gone" 通过。不检查已删除的订阅：
		// 它们不会被传递到这里
		if !presOfflineFilter(sub.ModeWant&sub.ModeGiven, what, nil) {
			continue
		}

		if cat == types.TopicCatP2P {
			original = types.ParseUid(subs[(count+1)%2].User).UserId()
			count++
		}

		user := types.ParseUid(sub.User).UserId()
		actor := params.actor
		target := params.target
		if actor == user {
			actor = ""
		}

		if target == user {
			target = ""
		}

		globals.hub.routeSrv <- &ServerComMessage{
			Pres: &MsgServerPres{
				Topic:     "me",
				What:      what,
				Src:       original,
				Acs:       params.packAcs(),
				AcsActor:  actor,
				AcsTarget: target,
				SeqId:     params.seqID,
				DelId:     params.delID,
			},
			RcptTo:  user,
			SkipSid: skipSid,
		}
	}
}

// 向 'me' Topic 上的单个用户广播
//
// 情况 K.1：用户更改了 WANT（包括新订阅、删除订阅）
// 情况 L.2：分享者更改了 GIVEN（包括邀请、驱逐）
// 情况 U：已读/已接收通知
// 情况 V.1：消息软删除
func (t *Topic) presSingleUserOffline(uid types.Uid, mode types.AccessMode,
	what string, params *presParams, skipSid string,
	offlineOnly bool) {

	var skipTopic string
	if offlineOnly {
		skipTopic = t.name
	}

	// ModeInvalid 表示用户已删除 (pud.deleted == true)
	if mode != types.ModeInvalid && presOfflineFilter(mode, what, nil) {

		user := uid.UserId()
		actor := params.actor
		target := params.target
		if actor == user {
			actor = ""
		}

		if target == user {
			target = ""
		}

		globals.hub.routeSrv <- &ServerComMessage{
			Pres: &MsgServerPres{
				Topic:     "me",
				What:      what,
				Src:       t.original(uid),
				SeqId:     params.seqID,
				DelId:     params.delID,
				Acs:       params.packAcs(),
				AcsActor:  actor,
				AcsTarget: target,
				UserAgent: params.userAgent,
				WantReply: strings.HasPrefix(what, "?unkn"),
				SkipTopic: skipTopic,
			},
			RcptTo:  user,
			SkipSid: skipSid,
		}
	}
}

// 向 'me' Topic 上的单个用户广播。源 Topic 未使用（未加载或用户
// 已取消订阅）
func presSingleUserOfflineOffline(uid types.Uid, original, what string, params *presParams, skipSid string) {
	user := uid.UserId()
	actor := params.actor
	target := params.target
	if actor == user {
		actor = ""
	}

	if target == user {
		target = ""
	}

	globals.hub.routeSrv <- &ServerComMessage{
		Pres: &MsgServerPres{
			Topic:     "me",
			What:      what,
			Src:       original,
			SeqId:     params.seqID,
			DelId:     params.delID,
			Acs:       params.packAcs(),
			AcsActor:  actor,
			AcsTarget: target,
		},
		RcptTo:  uid.UserId(),
		SkipSid: skipSid,
	}
}

// 让指定用户的其他 Session 知道哪些消息已被接收/已读
// 如果 'read' 和 'recv' 都不为 0，则 'read' 优先于 'recv'
// 情况 U
func (t *Topic) presPubMessageCount(uid types.Uid, mode types.AccessMode, read, recv int, skip string) {
	var what string
	var seq int
	if read > 0 {
		what = "read"
		seq = read
	} else if recv > 0 {
		what = "recv"
		seq = recv
	}

	if what != "" {
		// 仅当用户其他 Session 未附加到此 Topic 时，才在 'me' 上广播
		// 附加的 Topic 会收到 {info}

		t.presSingleUserOffline(uid, mode, what, &presParams{seqID: seq}, skip, true)
	}
}

// 让指定用户的其他 Session 知道消息已被删除
// 情况 V.1，V.2
func (t *Topic) presPubMessageDelete(uid types.Uid, mode types.AccessMode, delID int, list []MsgRange, skip string) {
	if len(list) == 0 && delID <= 0 {
		logs.Warn.Printf("Case V.1, V.2: topic[%s] invalid request - missing payload", t.name)
		return
	}

	// 此检查仅在 V.1 中需要，但对 V.2 无害。在此处对两者都进行检查
	if !t.userIsPresencer(uid) {
		return
	}

	params := &presParams{delID: delID, delSeq: list}

	// Case V.2
	user := uid.UserId()
	t.presSubsOnline("del", user, params, &presFilters{singleUser: user}, skip)

	// Case V.1
	t.presSingleUserOffline(uid, mode, "del", params, skip, true)
}

// 按权限和通知类型过滤：先检查异常，
// 然后检查 mode.IsPresencer() 且 mode 至少包含
// 'filter' 中指定的一些位（或 filter 为 ModeNone）
func presOfflineFilter(mode types.AccessMode, what string, pf *presFilters) bool {
	if what == "acs" || what == "gone" {
		return true
	}
	if what == "upd" && mode.IsJoiner() {
		return true
	}
	return mode.IsPresencer() &&
		(pf == nil ||
			((pf.filterIn == types.ModeNone || mode&pf.filterIn != 0) &&
				(pf.filterOut == types.ModeNone || mode&pf.filterOut == 0)))
}
