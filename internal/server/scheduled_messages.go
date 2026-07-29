// Package server 实现即时通信服务端的协议、路由和业务逻辑。
package server

import (
	"context"
	"time"

	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
)

const (
	// 定时队列按秒扫描，并限制单次批量，避免大量到期消息阻塞 Hub。
	scheduledPollPeriod = time.Second
	// scheduledBatchSize 指定定时消息批次Size。
	scheduledBatchSize = 100
)

// scheduledMessagesRun 启动持久化定时消息调度器。
// 集群节点可以共同扫描队列，但只有 Topic 当前所在节点负责路由；普通消息表的
// cid 唯一约束保证故障切换和重复扫描不会生成重复消息。
func scheduledMessagesRun() chan<- bool {
	stop := make(chan bool, 1)
	go func() {
		ticker := time.NewTicker(scheduledPollPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				dispatchDueScheduledMessages()
			case <-stop:
				return
			}
		}
	}()
	return stop
}

// dispatchDueScheduledMessages 读取一批已到期记录并送入 Hub 的定时消息通道。
func dispatchDueScheduledMessages() {
	if globals.hub == nil || globals.shuttingDown {
		return
	}
	now := types.TimeNow()
	messages, err := store.Messages.GetDueScheduled(now, scheduledBatchSize)
	if err != nil {
		logs.Warn.Printf("scheduled messages: failed to read due queue: %v", err)
		return
	}
	for i := range messages {
		scheduled := &messages[i]
		// 远程 Topic 由其当前所有者处理，避免同一记录在多个节点同时投递。
		if globals.cluster != nil && globals.cluster.isRemoteTopic(scheduled.Topic) {
			continue
		}
		if !serviceAllowsWrites() {
			return
		}
		claimed, claimErr := globals.cluster.claimScheduledTask(
			context.Background(),
			scheduled.Id,
		)
		if claimErr != nil {
			logs.Warn.Printf(
				"scheduled messages: failed to claim %s: %v",
				scheduled.Id,
				claimErr,
			)
			return
		}
		if !claimed {
			continue
		}
		msg := &ClientComMessage{
			Pub: &MsgClientPub{
				Topic:    scheduled.Topic,
				ClientId: scheduled.ClientId,
				NoEcho:   scheduled.NoEcho,
			},
			Original:  scheduled.Topic,
			RcptTo:    scheduled.Topic,
			AsUser:    types.ParseUid(scheduled.From).UserId(),
			Timestamp: now,
			scheduled: scheduled,
		}
		// 使用非阻塞发送，Hub 繁忙时保留数据库记录供下一轮重试。
		select {
		case globals.hub.schedule <- msg:
		default:
			logs.Warn.Printf("scheduled messages: hub queue is full for %s", scheduled.Topic)
			return
		}
	}
}
