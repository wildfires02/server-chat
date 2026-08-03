// Package server 实现即时通信服务端的协议、路由和业务逻辑。
package server

import (
	"errors"

	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
)

// removeScheduled 在定时消息已投递或确定无法重试时删除队列记录。
// 临时存储或 Hub 错误不会调用它，记录将保留到下一次扫描。
func (t *Topic) removeScheduled(msg *ClientComMessage, reason string) {
	if msg.scheduled == nil {
		return
	}
	asUid := types.ParseUserId(msg.AsUser)
	if err := store.Messages.DeleteScheduled(msg.scheduled.Id, t.name, asUid); err != nil {
		logs.Warn.Printf("topic[%s]: 删除%s定时消息失败: %v", t.name, reason, err)
	}
}

// prepareBroadcastableMessage 完成prepareBroadcastable消息所需的内部处理。
func (t *Topic) prepareBroadcastableMessage(msg *ServerComMessage, uid types.Uid, isChanSub bool) {
	// 仅处理广播类型的消息
	if msg.Data == nil && msg.Pres == nil && msg.Info == nil {
		return
	}

	if (t.cat == types.TopicCatP2P && !uid.IsZero()) || (t.cat == types.TopicCatGrp && t.isChan) {
		// 对于 P2P Topic，Topic 名称取决于接收者
		// Channel Topic 可以展示为 grpXXX 或 chnXXX
		var topicName string
		if isChanSub {
			topicName = types.GrpToChn(t.xoriginal)
		} else {
			topicName = t.original(uid)
		}
		switch {
		case msg.Data != nil:
			msg.Data.Topic = topicName
		case msg.Pres != nil:
			msg.Pres.Topic = topicName
		case msg.Info != nil:
			msg.Info.Topic = topicName
		}
	}

	// 匿名发送 Channel 消息
	if isChanSub && msg.Data != nil {
		msg.Data.From = ""
	}
}

// saveAndBroadcastMessage 保存AndBroadcast消息。
func (t *Topic) saveAndBroadcastMessage(msg *ClientComMessage, asUid types.Uid, noEcho bool, attachments []string, head map[string]any, content any) error {
	pud, userFound := t.perUser[asUid]
	// 任何人都允许向 'sys' Topic 发送消息
	if t.cat != types.TopicCatSys {
		// 非 'sys' Topic 校验写权限
		// 频道读者即使因旧数据或误配置携带 W，也必须保持只读。
		if pud.isChan || !(pud.modeWant & pud.modeGiven).IsWriter() {
			if msg.sess != nil {
				msg.sess.queueOut(ErrPermissionDenied(msg.Id, t.original(asUid), msg.Timestamp))
			}
			return types.ErrPermissionDenied
		}
	}
	scope := businessPolicyAction(head, content, attachments)
	if t.cat == types.TopicCatP2P && globals.businessPolicy != nil {
		if err := globals.businessPolicy.authorizeUIDs(asUid, t.p2pOtherUser(asUid), scope, t.name); err != nil {
			if msg.sess != nil {
				if errors.Is(err, errBusinessPolicyUnavailable) || errors.Is(err, errBusinessPolicyRateLimited) {
					msg.sess.queueOut(ErrServiceUnavailableExplicitTs(
						msg.Id, t.original(asUid), msg.Timestamp, msg.Timestamp))
				} else {
					msg.sess.queueOut(ErrPermissionDenied(msg.Id, t.original(asUid), msg.Timestamp))
				}
			}
			return err
		}
	}
	if err := t.checkOfficialPublish(asUid, scope, types.TimeNow()); err != nil {
		if msg.sess != nil {
			msg.sess.queueOut(ErrPermissionDenied(msg.Id, t.original(asUid), msg.Timestamp))
		}
		return err
	}
	if retryAfter, err := t.enforceOfficialSlowMode(asUid, scope, types.TimeNow()); err != nil {
		if msg.sess != nil {
			if errors.Is(err, errOfficialSlowMode) {
				msg.sess.queueOut(ErrTooManyRequestsReply(msg, t.original(asUid), types.TimeNow(), retryAfter))
			} else {
				msg.sess.queueOut(ErrServiceUnavailableExplicitTs(
					msg.Id, t.original(asUid), msg.Timestamp, msg.Timestamp))
			}
		}
		return err
	}

	if msg.sess != nil && msg.sess.uid != asUid {
		// "sender" 标头包含代表 asUid 发送消息的用户 ID
		if head == nil {
			head = map[string]any{}
		}
		head["sender"] = msg.sess.uid.UserId()
	} else if head != nil {
		// 确保接收到的 Head 不包含伪造的 "sender" 标头
		delete(head, "sender")
	}

	clientID := ""
	if msg.Pub != nil {
		clientID = msg.Pub.ClientId
	}
	if t.cat == types.TopicCatP2P && globals.businessPolicy != nil && clientID == "" {
		if msg.sess != nil {
			msg.sess.queueOut(ErrMalformed(msg.Id, t.original(asUid), msg.Timestamp))
		}
		return types.ErrMalformed
	}
	archiveForCompliance := func(stored *types.Message) error {
		if t.cat != types.TopicCatP2P || globals.businessPolicy == nil {
			return nil
		}
		if err := globals.businessPolicy.archiveTextMessage(stored, t.p2pOtherUser(asUid)); err != nil {
			logs.Warn.Printf("topic[%s]: 文字审计写入持久 outbox 失败: %v", t.name, err)
			if msg.sess != nil {
				msg.sess.queueOut(ErrServiceUnavailableExplicitTs(
					msg.Id, t.original(asUid), msg.Timestamp, msg.Timestamp))
			}
			return err
		}
		return nil
	}
	ackDuplicate := func(existing *types.Message) {
		if msg.Id != "" && msg.sess != nil {
			msg.sess.queueOut(NoErrDeliveredParams(msg.Id, t.original(asUid), msg.Timestamp,
				map[string]any{
					"seq":       existing.SeqId,
					"cid":       existing.ClientId,
					"duplicate": true,
				}))
		}
	}
	replayDuplicate := func(existing *types.Message) {
		if t.cat != types.TopicCatP2P || globals.businessPolicy == nil {
			return
		}
		data := &ServerComMessage{
			Data:      serverDataFromStored(msg.Original, msg.AsUser, existing),
			RcptTo:    msg.RcptTo,
			AsUser:    msg.AsUser,
			Timestamp: msg.Timestamp,
			sess:      msg.sess,
		}
		if noEcho && msg.sess != nil {
			data.SkipSid = msg.sess.sid
		}
		t.broadcastToSessions(data)
	}
	if clientID != "" {
		existing, err := store.Messages.GetByClientId(t.name, asUid, clientID)
		if err != nil {
			logs.Warn.Printf("topic[%s]: 查询消息幂等键失败: %v", t.name, err)
			if msg.sess != nil {
				msg.sess.queueOut(ErrUnknown(msg.Id, t.original(asUid), msg.Timestamp))
			}
			return err
		}
		if existing != nil {
			if err = archiveForCompliance(existing); err != nil {
				return err
			}
			ackDuplicate(existing)
			replayDuplicate(existing)
			return nil
		}
	}

	clusterID, clusterEpoch, clusterOwner := "", int64(0), ""
	if globals.cluster != nil {
		var err error
		clusterID, clusterEpoch, clusterOwner, err = globals.cluster.writeFenceToken(t.name)
		if err != nil {
			logs.Warn.Printf("topic[%s]: 集群写入令牌不可用: %v", t.name, err)
			if msg.sess != nil {
				msg.sess.queueOut(ErrServiceUnavailableExplicitTs(
					msg.Id, t.original(asUid), msg.Timestamp, msg.Timestamp))
			}
			return err
		}
	}

	stored := &types.Message{
		ObjHeader:    types.ObjHeader{CreatedAt: msg.Timestamp},
		SeqId:        t.lastID + 1,
		Topic:        t.name,
		From:         asUid.String(),
		ClientId:     clientID,
		ClientKey:    types.MessageClientKey(asUid, clientID),
		ClusterId:    clusterID,
		ClusterEpoch: clusterEpoch,
		ClusterOwner: clusterOwner,
		Head:         head,
		Content:      content,
	}
	markedReadBySender := false
	if err, unreadUpdated := store.Messages.Save(
		stored, attachments, (pud.modeGiven & pud.modeWant).IsReader()); err != nil {
		if errors.Is(err, types.ErrClusterFenced) {
			statsInc("ClusterFencingRejected", 1)
		}
		// 多节点竞争时唯一索引可能先于本节点的预查询命中。重新读取并按成功重试确认。
		if clientID != "" {
			if existing, lookupErr := store.Messages.GetByClientId(t.name, asUid, clientID); lookupErr == nil && existing != nil {
				if archiveErr := archiveForCompliance(existing); archiveErr != nil {
					return archiveErr
				}
				ackDuplicate(existing)
				replayDuplicate(existing)
				return nil
			}
		}
		logs.Warn.Printf("topic[%s]: 保存消息失败: %v", t.name, err)
		if msg.sess != nil {
			if errors.Is(err, types.ErrClusterFenced) {
				// 503 明确告诉客户端这是可重试的集群任期切换，而不是永久业务错误。
				msg.sess.queueOut(ErrServiceUnavailableExplicitTs(
					msg.Id, t.original(asUid), msg.Timestamp, msg.Timestamp))
			} else {
				msg.sess.queueOut(ErrUnknown(msg.Id, t.original(asUid), msg.Timestamp))
			}
		}

		return err
	} else {
		markedReadBySender = unreadUpdated
	}

	t.lastID++
	t.touched = msg.Timestamp
	if err := archiveForCompliance(stored); err != nil {
		return err
	}

	if userFound {
		pud.readID = t.lastID
		pud.recvID = t.lastID
		t.perUser[asUid] = pud
	}

	if msg.Id != "" && msg.sess != nil {
		reply := NoErrAccepted(msg.Id, t.original(asUid), msg.Timestamp)
		params := map[string]any{"seq": t.lastID}
		if clientID != "" {
			params["cid"] = clientID
		}
		reply.Ctrl.Params = params
		msg.sess.queueOut(reply)
	}

	data := &ServerComMessage{
		Data: serverDataFromStored(msg.Original, msg.AsUser, stored),
		// 内部保留字段
		Id:        msg.Id,
		RcptTo:    msg.RcptTo,
		AsUser:    msg.AsUser,
		Timestamp: msg.Timestamp,
		sess:      msg.sess,
	}
	if noEcho && msg.sess != nil {
		data.SkipSid = msg.sess.sid
	}

	// 消息已发送：在 'me' 上通知离线的 'R' 订阅者
	t.presSubsOffline("msg", &presParams{seqID: t.lastID, actor: msg.AsUser},
		&presFilters{filterIn: types.ModeRead}, nilPresFilters, "", true)

	// 通知插件消息已成功接受递送
	pluginMessage(data.Data, plgActCreate)

	t.broadcastToSessions(data)

	// sendPush 更新未读消息计数并发送推送通知
	t.sendPushForData(asUid, data.Data, markedReadBySender)
	return nil
}

// handlePubBroadcast 负责向主 Topic 中的接收者扇出广播 {pub} -> {data} 消息
// 此为非代理广播
func (t *Topic) handlePubBroadcast(msg *ClientComMessage) {
	asUid := types.ParseUserId(msg.AsUser)
	if t.isInactive() {
		// 忽略广播 - Topic 已暂停或正在被删除
		if msg.sess != nil {
			msg.sess.queueOut(ErrLocked(msg.Id, t.original(asUid), msg.Timestamp))
		}
		if t.isDeleted() {
			t.removeScheduled(msg, "已取消")
		}
		return
	}

	if t.isReadOnly() {
		if msg.sess != nil {
			msg.sess.queueOut(ErrPermissionDenied(msg.Id, t.original(asUid), msg.Timestamp))
		}
		t.removeScheduled(msg, "已取消")
		return
	}
	if err := t.refreshOfficialChannelMember(asUid); err != nil {
		if msg.sess != nil {
			msg.sess.queueOut(ErrPermissionDenied(msg.Id, t.original(asUid), msg.Timestamp))
		}
		t.removeScheduled(msg, "已取消")
		return
	}

	isCall := msg.Pub.Head != nil && msg.Pub.Head["webrtc"] != nil
	if isCall {
		// 呼叫邀请同样必须遵守 Topic 写权限，避免群组只读成员利用
		// 特殊的通话消息路径绕过普通消息 ACL。
		userData, userFound := t.perUser[asUid]
		if !userFound || userData.isChan ||
			!(userData.modeGiven & userData.modeWant).IsWriter() {
			msg.sess.queueOut(ErrPermissionDeniedReply(msg, types.TimeNow()))
			return
		}
		if t.cat != types.TopicCatP2P && t.cat != types.TopicCatGrp {
			msg.sess.queueOut(ErrPermissionDeniedReply(msg, types.TimeNow()))
			return
		}
		if globals.agora == nil {
			msg.sess.queueOut(ErrNotImplementedReply(msg, types.TimeNow()))
			return
		}
		if t.currentCall != nil {
			msg.sess.queueOut(ErrCallBusyReply(msg, types.TimeNow()))
			return
		}
		if err := t.checkOfficialPublish(asUid, "call", types.TimeNow()); err != nil {
			msg.sess.queueOut(ErrPermissionDeniedReply(msg, types.TimeNow()))
			return
		}
	}

	if msg.Pub.ReplaceSeq > 0 {
		if err := t.editMessage(msg, asUid); err != nil {
			if msg.sess != nil {
				msg.sess.queueOut(decodeStoreErrorExplicitTs(err, msg.Id, t.original(asUid),
					types.TimeNow(), msg.Timestamp, map[string]any{"what": "edit"}))
			}
		}
		return
	}

	var head map[string]any
	var content any
	var attachments []string
	var err error
	if msg.scheduled != nil {
		// 定时消息已在入队时完成内容校验，直接使用持久化快照。
		head = msg.scheduled.Head
		content = msg.scheduled.Content
		attachments = msg.scheduled.AttachmentURLs
	} else if isCall {
		head = stripServerMessageHead(msg.Pub.Head)
		if head == nil {
			head = make(map[string]any)
		}
		// 服务端统一决定 Agora 通话提供方，忽略客户端伪造的 provider。
		head["call-provider"] = constCallProviderAgora
		content = msg.Pub.Content
		if msg.Extra != nil {
			attachments = msg.Extra.Attachments
		}
	} else {
		head, content, attachments, err = t.prepareMessagePublication(msg, asUid)
		if err != nil {
			if msg.sess != nil {
				msg.sess.queueOut(decodeStoreErrorExplicitTs(err, msg.Id, t.original(asUid),
					types.TimeNow(), msg.Timestamp, map[string]any{"what": "pub"}))
			}
			return
		}
	}

	if msg.scheduled == nil && msg.Pub.ScheduleAt != nil &&
		msg.Pub.ScheduleAt.After(msg.Timestamp.Add(minScheduleDelay)) {
		// 距离当前时间足够远的消息进入持久化队列；近时刻请求立即投递。
		if err := t.scheduleMessage(msg, asUid, head, content, attachments); err != nil && msg.sess != nil {
			msg.sess.queueOut(decodeStoreErrorExplicitTs(err, msg.Id, t.original(asUid),
				types.TimeNow(), msg.Timestamp, map[string]any{"what": "schedule"}))
		}
		return
	}

	if err := t.saveAndBroadcastMessage(msg, asUid, msg.Pub.NoEcho, attachments, head, content); err != nil {
		logs.Err.Printf("topic[%s]: 保存消息失败 - %s", t.name, err)
		switch err {
		case types.ErrPermissionDenied, types.ErrMalformed, types.ErrPolicy,
			types.ErrNotFound, types.ErrTopicNotFound, types.ErrUserNotFound:
			t.removeScheduled(msg, "无法投递")
		}
		return
	}
	if msg.scheduled != nil {
		// 普通消息和 Topic 游标已经原子提交，此时才安全清理队列快照。
		t.removeScheduled(msg, "已投递")
		if len(t.sessions) == 0 && t.killTimer != nil {
			t.killTimer.Reset(idleMasterTopicTimeout)
		}
	}

	if isCall {
		t.handleCallInvite(msg, asUid)
	}
}

// handleNoteBroadcast 负责向主 Topic 中的接收者扇出广播 {note} -> {info} 消息
func (t *Topic) handleNoteBroadcast(msg *ClientComMessage) {
	if t.isInactive() {
		// 忽略广播 - Topic 已暂停或正在被删除
		if msg.Id != "" {
			msg.sess.queueOut(ErrLockedReply(msg, types.TimeNow()))
		}
		return
	}

	if msg.Note.SeqId > t.lastID {
		// 丢弃伪造的已读通知
		if msg.Id != "" {
			msg.sess.queueOut(ErrMalformedReply(msg, types.TimeNow()))
		}
		return
	}

	asChan, err := t.verifyChannelAccess(msg.Original)
	if err != nil {
		// 静默丢弃无效通知
		if msg.Id != "" {
			msg.sess.queueOut(ErrNotFoundReply(msg, types.TimeNow()))
		}
		return
	}

	asUid := types.ParseUserId(msg.AsUser)
	if msg.Note.What == "pin" {
		if err = t.refreshOfficialChannelMember(asUid); err != nil {
			if msg.Id != "" {
				msg.sess.queueOut(ErrPermissionDeniedReply(msg, types.TimeNow()))
			}
			return
		}
	}
	pud := t.perUser[asUid]
	mode := pud.modeGiven & pud.modeWant
	if pud.deleted {
		mode = types.ModeInvalid
	}

	switch msg.Note.What {
	case "kp", "kpa", "kpv":
		if err := t.checkOfficialPublish(asUid, "message", types.TimeNow()); err != nil {
			return
		}
		pud = t.perUser[asUid]
		mode = pud.modeGiven & pud.modeWant
		// 过滤掉无 'W' (写) 权限用户的 "kp*" 通知
		if asChan || pud.isChan || !mode.IsWriter() || t.isReadOnly() {
			return
		}
	case "read", "recv":
		// 过滤掉无 'R' (读) 权限用户的 "read/recv" 通知
		if !mode.IsReader() {
			if msg.Id != "" {
				msg.sess.queueOut(ErrPermissionDeniedReply(msg, types.TimeNow()))
			}
			return
		}
	case "react":
		if t.isOfficialTopic() {
			if err := t.refreshOfficialChannelMember(asUid); err != nil {
				if msg.Id != "" {
					msg.sess.queueOut(ErrPermissionDeniedReply(msg, types.TimeNow()))
				}
				return
			}
			if err := t.refreshOfficialPolicy(types.TimeNow()); err != nil ||
				t.official == nil || !t.official.ReactionsEnabled {
				if msg.Id != "" {
					msg.sess.queueOut(ErrPermissionDeniedReply(msg, types.TimeNow()))
				}
				return
			}
		}
		if !mode.IsReader() {
			if msg.Id != "" {
				msg.sess.queueOut(ErrPermissionDeniedReply(msg, types.TimeNow()))
			}
			return
		}
		if err := t.reactToMessage(msg, asUid); err != nil && msg.Id != "" {
			msg.sess.queueOut(decodeStoreErrorExplicitTs(err, msg.Id, msg.Original,
				types.TimeNow(), msg.Timestamp, map[string]any{"what": "react"}))
		}
		return
	case "pin":
		if !mode.IsReader() {
			if msg.Id != "" {
				msg.sess.queueOut(ErrPermissionDeniedReply(msg, types.TimeNow()))
			}
			return
		}
		if err := t.pinMessage(msg, asUid); err != nil && msg.Id != "" {
			msg.sess.queueOut(decodeStoreErrorExplicitTs(err, msg.Id, msg.Original,
				types.TimeNow(), msg.Timestamp, map[string]any{"what": "pin"}))
		}
		return
	case "call":
		// 单独处理通话事件
		t.handleCallEvent(msg)
		return
	}

	var read, recv, unread, seq int

	switch msg.Note.What {
	case "read":
		if msg.Note.SeqId <= pud.readID {
			// 无需汇报陈旧或重复的已读状态
			if msg.Id != "" {
				msg.sess.queueOut(NoErrDeliveredParams(msg.Id, msg.Original, msg.Timestamp,
					map[string]any{"what": "read", "seq": pud.readID, "duplicate": true}))
			}
			return
		}

		// 未读消息数减少
		previousReadID := pud.readID
		unread = pud.readID - msg.Note.SeqId
		pud.readID = msg.Note.SeqId
		if t.cat == types.TopicCatGrp && !t.isChan && !asChan {
			readAt := types.TimeNow()
			pud.readHistory.Append(previousReadID+1, pud.readID, readAt,
				readAt.Add(-messageReadersRetention))
		}
		if pud.readID > pud.recvID {
			pud.recvID = pud.readID
		}
		read = pud.readID
		seq = read
	case "recv":
		if msg.Note.SeqId <= pud.recvID {
			// 陈旧的已接收状态
			if msg.Id != "" {
				msg.sess.queueOut(NoErrDeliveredParams(msg.Id, msg.Original, msg.Timestamp,
					map[string]any{"what": "recv", "seq": pud.recvID, "duplicate": true}))
			}
			return
		}

		pud.recvID = msg.Note.SeqId
		if pud.readID > pud.recvID {
			pud.recvID = pud.readID
		}
		recv = pud.recvID
		seq = recv
	}

	if seq > 0 {
		topicName := t.name
		if asChan {
			topicName = msg.Note.Topic
		}

		upd := map[string]any{}
		if recv > 0 {
			upd["RecvSeqId"] = recv
		}
		if read > 0 {
			upd["ReadSeqId"] = read
			if t.cat == types.TopicCatGrp && !t.isChan && !asChan {
				upd["ReadHistory"] = pud.readHistory
			}
		}
		if err := store.Subs.Update(topicName, asUid, upd); err != nil {
			logs.Warn.Printf("topic[%s]: 更新 SeqRead/Recv 计数器失败: %v", t.name, err)
			if msg.Id != "" {
				msg.sess.queueOut(ErrUnknownReply(msg, msg.Timestamp))
			}
			return
		}

		// Read/recv 已更新：通知用户的其它 Session 变更
		t.presPubMessageCount(asUid, mode, read, recv, msg.sess.sid)

		if read > 0 {
			// 向用户其它设备发送推送通知
			sendPush(t.pushForReadRcpt(asUid, read, msg.Timestamp))
		}

		// 更新未读消息缓存计数（不追踪 Channel 未读消息）
		if !asChan {
			usersUpdateUnread(asUid, unread, true)
		}

		if msg.Id != "" {
			msg.sess.queueOut(NoErrParamsReply(msg, msg.Timestamp,
				map[string]any{"what": msg.Note.What, "seq": seq}))
		}
	}

	if asChan {
		// 无需向 Channel 中的其它订阅者转发 {note}
		return
	}

	if seq > 0 {
		t.perUser[asUid] = pud
	}

	// Read/recv/kp: 在离线用户的 'me' Topic 上通知他们
	t.infoSubsOffline(asUid, msg.Note.What, seq, msg.sess.sid)

	info := &ServerComMessage{
		Info: &MsgServerInfo{
			Topic: msg.Original,
			From:  msg.AsUser,
			What:  msg.Note.What,
			SeqId: msg.Note.SeqId,
		},
		RcptTo:    msg.RcptTo,
		AsUser:    msg.AsUser,
		Timestamp: msg.Timestamp,
		SkipSid:   msg.sess.sid,
		sess:      msg.sess,
	}

	t.broadcastToSessions(info)
}

// broadcastToSessions 向已挂载的 Session 写入并广播消息
func (t *Topic) broadcastToSessions(msg *ServerComMessage) {
	// 待断开/丢弃的 Session 列表
	var dropSessions []*Session
	// Group payloads only differ between normal members and anonymous channel readers.
	// Reuse one immutable projected message per variant so all local writers share encoding.
	var groupVariants [2]*ServerComMessage
	// 广播消息。仅 {data}, {pres}, {info} 允许广播
	for sess, pssd := range t.sessions {
		// 将所有消息发送到多路复用 Session
		if !sess.isMultiplex() {
			if sess.sid == msg.SkipSid {
				continue
			}

			if msg.Pres != nil {
				// 跳过已在 Topic 上通知过的
				if msg.Pres.SkipTopic != "" && sess.getSub(msg.Pres.SkipTopic) != nil {
					continue
				}

				// 仅针对单个用户的通知
				if msg.Pres.SingleUser != "" && pssd.uid.UserId() != msg.Pres.SingleUser {
					continue
				}
				// 需排除单个用户的通知
				if msg.Pres.ExcludeUser != "" && pssd.uid.UserId() == msg.Pres.ExcludeUser {
					continue
				}

				// 校验 Presence 过滤器
				if !t.passesPresenceFilters(msg.Pres, pssd.uid) {
					continue
				}

			} else {
				if msg.Info != nil {
					// 不向 Channel 读者以及无 R (读) 权限的用户转发已读回执和正在输入状态
					if msg.Info.Src == "" && (pssd.isChanSub || !t.userIsReader(pssd.uid)) {
						continue
					}

					// 跳过已在 Topic 上通知过的
					if msg.Info.SkipTopic != "" && sess.getSub(msg.Info.SkipTopic) != nil {
						continue
					}

					// 不向发送者自己的其它 Session 转发按键输入 (kp)
					if msg.Info.What == "kp" && msg.Info.From == pssd.uid.UserId() {
						continue
					}

				} else if !t.userIsReader(pssd.uid) && !pssd.isChanSub {
					// 如果用户没有读权限且不是 Channel 读者，跳过 {data}
					continue
				}
			}
		} else if pssd.isChanSub && types.IsChannel(sess.sid) {
			grpSid := types.ChnToGrp(sess.sid)
			if grpSess := globals.sessionStore.Get(grpSid); grpSess != nil && grpSess.isMultiplex() {
				if _, attached := t.sessions[grpSess]; attached {
					continue
				}
			}
		}

		// Multiplex routing mutates internal routing fields, so only local group sessions
		// can safely share the same immutable projected object.
		var msgCopy *ServerComMessage
		if t.cat == types.TopicCatGrp && sess.multi == nil && !sess.isMultiplex() {
			variant := 0
			if pssd.isChanSub {
				variant = 1
			}
			msgCopy = groupVariants[variant]
			if msgCopy == nil {
				msgCopy = msg.copy()
				t.prepareBroadcastableMessage(msgCopy, pssd.uid, pssd.isChanSub)
				groupVariants[variant] = msgCopy
			}
		} else {
			msgCopy = msg.copy()
			t.prepareBroadcastableMessage(msgCopy, pssd.uid, pssd.isChanSub)
		}
		var startTranslation func()
		if t.cat == types.TopicCatP2P && msgCopy.Data != nil && globals.translation != nil {
			msgCopy.Data, startTranslation = globals.translation.project(
				t.name, msgCopy.Data, sess.lang, msgCopy.Data.From == pssd.uid.UserId(),
				func(translated *MsgServerData) {
					final := msgCopy.copy()
					final.Data = translated
					if !sess.queueOut(final) {
						logs.Warn.Printf("topic[%s]: translated message queue is full - %s",
							t.name, sess.sid)
					}
				})
		}
		// 发送消息给 Session
		if !sess.queueOut(msgCopy) {
			logs.Warn.Printf("topic[%s]: 连接卡顿，正在剥离 - %s", t.name, sess.sid)
			dropSessions = append(dropSessions, sess)
		} else if startTranslation != nil {
			startTranslation()
		}
	}

	// 丢弃异常 Session
	for _, sess := range dropSessions {
		t.unregisterSession(&ClientComMessage{sess: sess, init: false})
	}
}
