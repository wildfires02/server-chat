package server

import (
	"errors"
	"time"

	"chat/server/store"
	"chat/server/store/types"
)

// replyGetData 是对 get.data 请求的响应 - 加载存储消息列表，作为 {data} 发送给 Session
// 响应仅发送给单个 Session 而不是 Topic 中的所有 Session
func (t *Topic) replyGetData(sess *Session, asUid types.Uid, asChan bool, req *MsgGetOpts, msg *ClientComMessage) error {
	now := types.TimeNow()
	toriginal := t.original(asUid)

	if req != nil && (req.User != "" || req.Topic != "") {
		sess.queueOut(ErrMalformedReply(msg, now))
		return errors.New("invalid MsgGetOpts query")
	}

	// 检查是否用户有权限读取 Topic 数据
	count := 0
	var modified *time.Time
	high := t.lastID
	cursor := 0
	queryReq := req
	if req != nil && req.Forward {
		// 首次请求固定本轮同步快照上界；后续分页继续沿用 before 游标，
		// 避免同步期间的新消息混入当前批次。
		if req.BeforeId > 0 && req.BeforeId-1 < high {
			high = req.BeforeId - 1
		}
		snapshotReq := *req
		if snapshotReq.BeforeId == 0 || snapshotReq.BeforeId > high+1 {
			snapshotReq.BeforeId = high + 1
		}
		queryReq = &snapshotReq
		cursor = snapshotReq.SinceId - 1
	}
	if userData := t.perUser[asUid]; (userData.modeGiven & userData.modeWant).IsReader() {
		// 从 DB 读取消息
		messages, err := store.Messages.GetAll(t.name, asUid, msgOpts2storeOpts(queryReq))
		if err != nil {
			sess.queueOut(ErrUnknownReply(msg, now))
			return err
		}

		// 将消息列表作为 {data} 推送到客户端。
		if messages != nil {
			count = len(messages)
			if count > 0 {
				outgoingMessages := make([]*ServerComMessage, count)
				startTranslations := make([]func(), 0, count)
				for i := range messages {
					mm := &messages[i]
					// 返回本批次最大的修改时间，供客户端增量拉取编辑结果。
					if !mm.UpdatedAt.IsZero() && (modified == nil || mm.UpdatedAt.After(*modified)) {
						ts := mm.UpdatedAt
						modified = &ts
					}
					from := ""
					if !asChan {
						// 不显示 Channel 读者的发送者
						from = types.ParseUid(mm.From).UserId()
					}
					data := serverDataFromStored(toriginal, from, mm)
					if t.cat == types.TopicCatP2P && globals.translation != nil {
						var start func()
						data, start = globals.translation.projectHistoricalData(t.name, data, sess, asUid)
						if start != nil {
							startTranslations = append(startTranslations, start)
						}
					}
					outgoingMessages[i] = &ServerComMessage{Data: data}
				}
				if req != nil && req.Forward {
					cursor = messages[count-1].SeqId
				}
				if !sess.queueOutBatch(outgoingMessages) {
					sess.queueOut(ErrServiceUnavailableReply(msg, now))
					return errors.New("session send queue is full")
				}
				for _, start := range startTranslations {
					start()
				}
			}
		}
	} else {
		sess.queueOut(ErrPermissionDeniedReply(msg, now))
		return errors.New("attempt to get messages by non-reader")
	}

	params := map[string]any{"what": "data", "count": count}
	if modified != nil {
		params["modified"] = modified
		if req != nil && req.IfModifiedSince != nil {
			params["hasMoreModified"] = req.Limit > 0 && count >= req.Limit
		}
	}
	if req != nil && req.Forward {
		if count == 0 {
			cursor = high
			if req.SinceId-1 > cursor {
				cursor = req.SinceId - 1
			}
		}
		params["low"] = req.SinceId
		params["high"] = high
		params["cursor"] = cursor
		params["hasMore"] = cursor < high
	}

	// 通知请求者所有数据已提供服务。
	if count == 0 {
		sess.queueOut(NoContentParamsReply(msg, now, params))
	} else {
		sess.queueOut(NoErrDeliveredParams(msg.Id, msg.Original, now, params))
	}

	return nil
}
