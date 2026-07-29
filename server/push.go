/******************************************************************************
 *
 *  描述：
 *    推送通知处理。
 *
 *****************************************************************************/

// Package main 实现即时通信服务端的协议、路由和业务逻辑。
package main

import (
	"time"

	"chat/server/push"
	"chat/server/store/types"
)

// 为用户订阅或取消订阅 FCM Topic（Channel）
func (t *Topic) channelSubUnsub(uid types.Uid, sub bool) {
	push.ChannelSub(&push.ChannelReq{
		Uid:     uid,
		Channel: types.GrpToChn(t.name),
		Unsub:   !sub,
	})
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

	if t.isChan {
		// Channel readers should get a push on a Channel name (as an FCM Topic push).
		receipt.Channel = types.GrpToChn(t.name)
	}

	for uid, pud := range t.perUser {
		online := pud.online
		if uid == fromUid && online == 0 {
			// Make sure the sender's devices receive a silent push.
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
			Channel: rcpt.Channel,
			To:      make(map[types.Uid]push.Recipient),
		}}

		for uid, recipient := range rcpt.To {
			if globals.cluster.isRemoteTopic(uid.UserId()) {
				remote.PushRcpt.To[uid] = recipient
			} else {
				local.PushRcpt.To[uid] = recipient
			}
		}

		if len(remote.PushRcpt.To) > 0 || remote.PushRcpt.Channel != "" {
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
