package main

import (
	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
)

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

func (t *Topic) saveAndBroadcastMessage(msg *ClientComMessage, asUid types.Uid, noEcho bool, attachments []string, head map[string]any, content any) error {
	pud, userFound := t.perUser[asUid]
	// 任何人都允许向 'sys' Topic 发送消息
	if t.cat != types.TopicCatSys {
		// 非 'sys' Topic 校验写权限
		if !(pud.modeWant & pud.modeGiven).IsWriter() {
			msg.sess.queueOut(ErrPermissionDenied(msg.Id, t.original(asUid), msg.Timestamp))
			return types.ErrPermissionDenied
		}
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

	markedReadBySender := false
	if err, unreadUpdated := store.Messages.Save(
		&types.Message{
			ObjHeader: types.ObjHeader{CreatedAt: msg.Timestamp},
			SeqId:     t.lastID + 1,
			Topic:     t.name,
			From:      asUid.String(),
			Head:      head,
			Content:   content,
		}, attachments, (pud.modeGiven & pud.modeWant).IsReader()); err != nil {
		logs.Warn.Printf("topic[%s]: 保存消息失败: %v", t.name, err)
		msg.sess.queueOut(ErrUnknown(msg.Id, t.original(asUid), msg.Timestamp))

		return err
	} else {
		markedReadBySender = unreadUpdated
	}

	t.lastID++
	t.touched = msg.Timestamp

	if userFound {
		pud.readID = t.lastID
		pud.recvID = t.lastID
		t.perUser[asUid] = pud
	}

	if msg.Id != "" && msg.sess != nil {
		reply := NoErrAccepted(msg.Id, t.original(asUid), msg.Timestamp)
		reply.Ctrl.Params = map[string]any{"seq": t.lastID}
		msg.sess.queueOut(reply)
	}

	data := &ServerComMessage{
		Data: &MsgServerData{
			Topic:     msg.Original,
			From:      msg.AsUser,
			Timestamp: msg.Timestamp,
			SeqId:     t.lastID,
			Head:      head,
			Content:   content,
		},
		// 内部保留字段
		Id:        msg.Id,
		RcptTo:    msg.RcptTo,
		AsUser:    msg.AsUser,
		Timestamp: msg.Timestamp,
		sess:      msg.sess,
	}
	if noEcho {
		data.SkipSid = msg.sess.sid
	}

	// 消息已发送：在 'me' 上通知离线的 'R' 订阅者
	t.presSubsOffline("msg", &presParams{seqID: t.lastID, actor: msg.AsUser},
		&presFilters{filterIn: types.ModeRead}, nilPresFilters, "", true)

	// 通知插件消息已成功接受递送
	pluginMessage(data.Data, plgActCreate)

	t.broadcastToSessions(data)

	// sendPush 更新未读消息计数并发送推送通知
	if pushRcpt := t.pushForData(asUid, data.Data, markedReadBySender); pushRcpt != nil {
		sendPush(pushRcpt)
	}
	return nil
}

// handlePubBroadcast 负责向主 Topic 中的接收者扇出广播 {pub} -> {data} 消息
// 此为非代理广播
func (t *Topic) handlePubBroadcast(msg *ClientComMessage) {
	asUid := types.ParseUserId(msg.AsUser)
	if t.isInactive() {
		// 忽略广播 - Topic 已暂停或正在被删除
		msg.sess.queueOut(ErrLocked(msg.Id, t.original(asUid), msg.Timestamp))
		return
	}

	if t.isReadOnly() {
		msg.sess.queueOut(ErrPermissionDenied(msg.Id, t.original(asUid), msg.Timestamp))
		return
	}

	isCall := msg.Pub.Head != nil && msg.Pub.Head["webrtc"] != nil
	if isCall {
		if len(globals.iceServers) == 0 {
			msg.sess.queueOut(ErrNotImplementedReply(msg, types.TimeNow()))
			return
		}
		if t.cat != types.TopicCatP2P {
			msg.sess.queueOut(ErrPermissionDeniedReply(msg, types.TimeNow()))
			return
		}
		if t.currentCall != nil {
			msg.sess.queueOut(ErrCallBusyReply(msg, types.TimeNow()))
			return
		}
	}

	// 保存到主 Topic 的数据库
	var attachments []string
	if msg.Extra != nil && len(msg.Extra.Attachments) > 0 {
		attachments = msg.Extra.Attachments
	}

	if err := t.saveAndBroadcastMessage(msg, asUid, msg.Pub.NoEcho, attachments, msg.Pub.Head, msg.Pub.Content); err != nil {
		logs.Err.Printf("topic[%s]: 保存消息失败 - %s", t.name, err)
		return
	}

	if isCall {
		t.handleCallInvite(msg, asUid)
	}
}

// handleNoteBroadcast 负责向主 Topic 中的接收者扇出广播 {note} -> {info} 消息
func (t *Topic) handleNoteBroadcast(msg *ClientComMessage) {
	if t.isInactive() {
		// 忽略广播 - Topic 已暂停或正在被删除
		return
	}

	if msg.Note.SeqId > t.lastID {
		// 丢弃伪造的已读通知
		return
	}

	asChan, err := t.verifyChannelAccess(msg.Original)
	if err != nil {
		// 静默丢弃无效通知
		return
	}

	asUid := types.ParseUserId(msg.AsUser)
	pud := t.perUser[asUid]
	mode := pud.modeGiven & pud.modeWant
	if pud.deleted {
		mode = types.ModeInvalid
	}

	switch msg.Note.What {
	case "kp", "kpa", "kpv":
		// 过滤掉无 'W' (写) 权限用户的 "kp*" 通知
		if !mode.IsWriter() || t.isReadOnly() {
			return
		}
	case "read", "recv":
		// 过滤掉无 'R' (读) 权限用户的 "read/recv" 通知
		if !mode.IsReader() {
			return
		}
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
			return
		}

		// 未读消息数减少
		unread = pud.readID - msg.Note.SeqId
		pud.readID = msg.Note.SeqId
		if pud.readID > pud.recvID {
			pud.recvID = pud.readID
		}
		read = pud.readID
		seq = read
	case "recv":
		if msg.Note.SeqId <= pud.recvID {
			// 陈旧的已接收状态
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
		}
		if err := store.Subs.Update(topicName, asUid, upd); err != nil {
			logs.Warn.Printf("topic[%s]: 更新 SeqRead/Recv 计数器失败: %v", t.name, err)
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

		// 复制一份消息（不同 Session 接收到的消息格式可能不同）
		msgCopy := msg.copy()
		// 根据 Session 所属用户准备广播消息
		t.prepareBroadcastableMessage(msgCopy, pssd.uid, pssd.isChanSub)
		// 发送消息给 Session
		if !sess.queueOut(msgCopy) {
			logs.Warn.Printf("topic[%s]: 连接卡顿，正在剥离 - %s", t.name, sess.sid)
			dropSessions = append(dropSessions, sess)
		}
	}

	// 丢弃异常 Session
	for _, sess := range dropSessions {
		t.unregisterSession(&ClientComMessage{sess: sess, init: false})
	}
}
