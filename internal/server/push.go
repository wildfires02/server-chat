/******************************************************************************
 *
 *  描述：
 *    推送通知处理。
 *
 *****************************************************************************/

// Package server 实现即时通信服务端的协议、路由和业务逻辑。
package server

import (
	"time"

	"chat/server/push"
	"chat/server/store/types"
)

// pushTopicSubUnsub 为用户订阅或取消订阅移动端 Push Topic。
//
// 普通广播频道沿用 chn... 名称；官方大群使用 grp... 名称。官方大群必须使用
// 供应商 Topic 做扇出，不能为了发送一条离线通知把全量成员加载进 Topic Actor。
func (t *Topic) pushTopicSubUnsub(uid types.Uid, sub bool) {
	channel := ""
	switch {
	case t.isChan:
		channel = types.GrpToChn(t.name)
	case t.isOfficialLargeGroup():
		channel = t.name
	default:
		return
	}
	push.ChannelSub(&push.ChannelReq{
		Uid:     uid,
		Channel: channel,
		Unsub:   !sub,
	})
}

// channelSubUnsub 保留旧调用名称，广播频道仍走同一条 Push Topic 同步链路。
func (t *Topic) channelSubUnsub(uid types.Uid, sub bool) {
	t.pushTopicSubUnsub(uid, sub)
}

// 准备要推送到移动设备的推送通知负载，响应 {data} 消息
func (t *Topic) pushForData(fromUid types.Uid, data *MsgServerData, msgMarkedAsReadBySender bool) *push.Receipt {
	// 对于群组 Topic 和 P2P Topic，将 `Topic` 作为 `t.name` 传递。p2p Topic 名称稍后会为
	// 每个接收者重写，然后创建负载：p2p 接收者将 Topic 视为另一个用户的 ID

	// 初始化推送回执
	contentType, _ := data.Head["mime"].(string)
	receipt := push.Receipt{
		To: make(map[types.Uid]push.Recipient, t.subsCount()),
		Payload: push.Payload{
			What:        push.ActMsg,
			Silent:      false,
			Topic:       t.name,
			From:        data.From,
			Timestamp:   data.Timestamp,
			SeqId:       data.SeqId,
			ContentType: contentType,
			Content:     data.Content,
		},
	}
	if webrtc, found := data.Head["webrtc"].(string); found {
		receipt.Payload.Webrtc = webrtc
		audioOnly, _ := data.Head["aonly"].(bool)
		receipt.Payload.AudioOnly = audioOnly
	}
	if replace, found := data.Head["replace"].(string); found {
		receipt.Payload.Replace = replace
	}
	if t.isOfficialLargeGroup() {
		// 官方大群只向供应商 Topic 投递一次。在线消息仍由 Topic Actor 向当前
		// Session 广播；离线 Push 不遍历冷成员，也不为每个成员重复查设备表。
		receipt.Channel = t.name
		return &receipt
	}

	if t.isChan {
		//频道读者应该推送频道名称（作为FCM主题推送）。
		receipt.Channel = types.GrpToChn(t.name)
	}

	for uid, pud := range t.perUser {
		online := pud.online
		if uid == fromUid && online == 0 {
			//确保发件人的设备收到无声推送。
			online = 1
		}

		// 仅向启用了通知的用户发送
		mode := pud.modeWant & pud.modeGiven
		if mode.IsPresencer() && mode.IsReader() && !pud.deleted && !pud.isChan {
			receipt.To[uid] = push.Recipient{
				// 数据消息将送达的附加 Session 数量
				// 发送给具有非零在线 Session 的用户的推送通知将被标记为静默
				Delivered: online,
				// 所有接收者的未读计数都会递增，
				// 仅当消息未被发送者标记为 'read' 时，发送者也会递增
				ShouldIncrementUnreadCountInCache: uid != fromUid || !msgMarkedAsReadBySender,
			}
		}
	}
	if len(receipt.To) > 0 || receipt.Channel != "" {
		return &receipt
	}
	// 如果没有接收者，则无需发送推送通知
	return nil
}

// sendPushForData防止未翻译的P2P文本泄露到
// 收件人的通知。 发件人和收件人的收据是分开的，因为
// 他们的有效载荷可能有所不同，但未读会计仍然必须发生一次。
func (t *Topic) sendPushForData(fromUid types.Uid, data *MsgServerData,
	msgMarkedAsReadBySender bool) {
	receipt := t.pushForData(fromUid, data, msgMarkedAsReadBySender)
	if receipt == nil {
		return
	}
	if t.cat != types.TopicCatP2P || globals.translation == nil {
		sendPush(receipt)
		return
	}
	toUid := t.p2pOtherUser(fromUid)
	projected, start := globals.translation.project(t.name, data, "", false,
		func(translated *MsgServerData) {
			sendPush(singleRecipientPush(receipt, toUid, translated.Content))
		})
	if projected == data || projected.Translation == nil {
		sendPush(receipt)
		return
	}
	if _, found := receipt.To[fromUid]; found {
		sendPush(singleRecipientPush(receipt, fromUid, data.Content))
	}
	if projected.Translation.Status != "pending" {
		sendPush(singleRecipientPush(receipt, toUid, projected.Content))
	}
	if start != nil {
		start()
	}
}

func singleRecipientPush(receipt *push.Receipt, uid types.Uid, content any) *push.Receipt {
	recipient, found := receipt.To[uid]
	if !found {
		return nil
	}
	out := &push.Receipt{
		To:      map[types.Uid]push.Recipient{uid: recipient},
		Payload: receipt.Payload,
	}
	out.Payload.Content = content
	return out
}

// preparePushForSubReceipt 完成preparePushFor订阅Receipt所需的内部处理。
func (t *Topic) preparePushForSubReceipt(fromUid types.Uid, now time.Time) *push.Receipt {
	// 推送回执中的 `Topic` 对于群组 Topic 是 `t.xoriginal`，对于 p2p Topic 是 `fromUid`，
	// 而非 t.original(fromUid)，因为这是接收者看到的 Topic 名称，而非发送者看到的
	topic := t.xoriginal
	if t.cat == types.TopicCatP2P {
		topic = fromUid.UserId()
	}

	// 初始化推送回执
	receipt := &push.Receipt{
		To: make(map[types.Uid]push.Recipient, t.subsCount()),
		Payload: push.Payload{
			What:      push.ActSub,
			Silent:    false,
			Topic:     topic,
			From:      fromUid.UserId(),
			Timestamp: now,
			SeqId:     t.lastID,
		},
	}
	return receipt
}

// 准备推送通知负载，响应 p2p Topic 中的新订阅
func (t *Topic) pushForP2PSub(fromUid, toUid types.Uid, want, given types.AccessMode, now time.Time) *push.Receipt {
	receipt := t.preparePushForSubReceipt(fromUid, now)
	receipt.Payload.ModeWant = want
	receipt.Payload.ModeGiven = given

	receipt.To[toUid] = push.Recipient{}

	return receipt
}

// 准备推送通知负载，响应群组 Topic 中的新订阅
func (t *Topic) pushForGroupSub(fromUid types.Uid, now time.Time) *push.Receipt {
	receipt := t.preparePushForSubReceipt(fromUid, now)
	if pud, ok := t.perUser[fromUid]; ok {
		receipt.Payload.ModeWant = pud.modeWant
		receipt.Payload.ModeGiven = pud.modeGiven
	} else {
		// 发送者不是订阅者（BUG？）
		return nil
	}

	for uid, pud := range t.perUser {
		// 仅向启用了通知的用户发送
		mode := pud.modeWant & pud.modeGiven
		if mode.IsPresencer() && mode.IsReader() && !pud.deleted && !pud.isChan {
			receipt.To[uid] = push.Recipient{}
		}
	}
	if len(receipt.To) > 0 || receipt.Channel != "" {
		return receipt
	}
	return nil
}

// 准备推送通知负载，响应所有者删除 Channel
func pushForChanDelete(topicName string, now time.Time) *push.Receipt {
	topicName = types.GrpToChn(topicName)
	// 初始化推送回执
	return &push.Receipt{
		Payload: push.Payload{
			What:      push.ActSub,
			Silent:    true,
			Topic:     topicName,
			Timestamp: now,
			ModeWant:  types.ModeNone,
			ModeGiven: types.ModeNone,
		},
		Channel: topicName,
	}
}

// 准备推送通知负载，响应接收 "read" 通知
func (t *Topic) pushForReadRcpt(uid types.Uid, seq int, now time.Time) *push.Receipt {
	// 推送回执中的 `Topic` 对于群组 Topic 是 `t.xoriginal`，对于 p2p Topic 是 `fromUid`，
	// 而非 t.original(fromUid)，因为这是接收者看到的 Topic 名称，而非发送者看到的
	topic := t.xoriginal
	if t.cat == types.TopicCatP2P {
		topic = uid.UserId()
	}

	// 初始化推送回执
	receipt := &push.Receipt{
		To: make(map[types.Uid]push.Recipient, 1),
		Payload: push.Payload{
			What:      push.ActRead,
			Silent:    true,
			Topic:     topic,
			From:      uid.UserId(),
			Timestamp: now,
			SeqId:     seq,
		},
	}
	receipt.To[uid] = push.Recipient{}
	return receipt
}

// 处理推送通知
func sendPush(rcpt *push.Receipt) {
	if rcpt == nil || globals.usersUpdate == nil {
		return
	}

	var local *UserCacheReq

	// 在集群模式下，推送将在拥有用户的节点上发起
	// 将用户分为本地和远程
	if globals.cluster != nil {
		local = &UserCacheReq{PushRcpt: &push.Receipt{
			Payload: rcpt.Payload,
			Channel: rcpt.Channel,
			To:      make(map[types.Uid]push.Recipient),
		}}
		remote := &UserCacheReq{PushRcpt: &push.Receipt{
			Payload: rcpt.Payload,
			To:      make(map[types.Uid]push.Recipient),
		}}

		for uid, recipient := range rcpt.To {
			if globals.cluster.isRemoteTopic(uid.UserId()) {
				remote.PushRcpt.To[uid] = recipient
			} else {
				local.PushRcpt.To[uid] = recipient
			}
		}

		if len(remote.PushRcpt.To) > 0 {
			globals.cluster.routeUserReq(remote)
		}
	} else {
		local = &UserCacheReq{PushRcpt: rcpt}
	}

	if len(local.PushRcpt.To) > 0 || local.PushRcpt.Channel != "" {
		select {
		case globals.usersUpdate <- local:
		default:
		}
	}
}
