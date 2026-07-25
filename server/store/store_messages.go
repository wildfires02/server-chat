package store

import (
	"sort"
	"time"

	"chat/server/logs"
	"chat/server/store/types"
)

// MessagesPersistenceInterface 定义消息持久化存储的方法接口。
type MessagesPersistenceInterface interface {
	Save(msg *types.Message, attachmentURLs []string, readBySender bool) (error, bool)
	DeleteList(topic string, delID int, forUser types.Uid, msgDelAge time.Duration, ranges []types.Range) error
	GetAll(topic string, forUser types.Uid, opt *types.QueryOpt) ([]types.Message, error)
	GetDeleted(topic string, forUser types.Uid, opt *types.QueryOpt) ([]types.Range, int, error)
}

// messagesMapper 是实现 MessagesPersistenceInterface 的具体类型。
type messagesMapper struct{}

// Messages 是导出 MessagesPersistenceInterface 的单例锚对象。
var Messages MessagesPersistenceInterface

// Save 消息
func (messagesMapper) Save(msg *types.Message, attachmentURLs []string, readBySender bool) (error, bool) {
	msg.InitTimes()
	msg.SetUid(Store.GetUid())
	// 递增 Topic 或用户的 SeqId
	err := adp.TopicUpdateOnMessage(msg.Topic, msg)
	if err != nil {
		return err, false
	}

	err = adp.MessageSave(msg)
	if err != nil {
		return err, false
	}

	markedReadBySender := false
	// 将消息标记为发送者已读。
	if readBySender {
		// 确保 From 有效，否则将重置所有订阅者的值。
		fromUid := types.ParseUid(msg.From)
		if !fromUid.IsZero() {
			// 忽略此处的错误。失败也不是大问题。
			if subErr := adp.SubsUpdate(msg.Topic, fromUid,
				map[string]any{
					"RecvSeqId": msg.SeqId,
					"ReadSeqId": msg.SeqId}); subErr != nil {
				logs.Warn.Printf("topic[%s]: failed to mark message (seq: %d) read by sender - err: %+v", msg.Topic, msg.SeqId, subErr)
			} else {
				markedReadBySender = true
			}
		}
	}

	if len(attachmentURLs) > 0 {
		var attachments []string
		for _, url := range attachmentURLs {
			// 将附件 URL 转换为文件 ID。
			if fid := mediaHandler.GetIdFromUrl(url); !fid.IsZero() {
				attachments = append(attachments, fid.String())
			}
		}
		if len(attachments) > 0 {
			return adp.FileLinkAttachments("", types.ZeroUid, msg.Uid(), attachments), markedReadBySender
		}
	}

	return nil, markedReadBySender
}

// DeleteList 删除由范围列表定义的多条消息。
func (messagesMapper) DeleteList(topic string, delID int, forUser types.Uid, msgDelAge time.Duration, ranges []types.Range) error {
	var toDel *types.DelMessage
	if delID > 0 {
		toDel = &types.DelMessage{
			Topic:       topic,
			DelId:       delID,
			DeletedFor:  forUser.String(),
			SeqIdRanges: ranges}
		toDel.SetUid(Store.GetUid())
		toDel.InitTimes()
		if msgDelAge > 0 {
			toDel.SetNewerThan(toDel.CreatedAt.Add(-msgDelAge))
		}
	}

	return adp.MessageDeleteList(topic, toDel)
}

// GetAll 返回多条消息。
func (messagesMapper) GetAll(topic string, forUser types.Uid, opt *types.QueryOpt) ([]types.Message, error) {
	return adp.MessageGetAll(topic, forUser, opt)
}

// GetDeleted 返回已删除消息的范围和列表中报告的最大 DelId。
func (messagesMapper) GetDeleted(topic string, forUser types.Uid, opt *types.QueryOpt) ([]types.Range, int, error) {
	dmsgs, err := adp.MessageGetDeleted(topic, forUser, opt)
	if err != nil {
		return nil, 0, err
	}

	var ranges []types.Range
	var maxID int
	// 扁平化范围s
	for i := range dmsgs {
		dm := &dmsgs[i]
		if dm.DelId > maxID {
			maxID = dm.DelId
		}
		ranges = append(ranges, dm.SeqIdRanges...)
	}
	sort.Sort(types.RangeSorter(ranges))
	ranges = types.RangeSorter(ranges).Normalize()

	return ranges, maxID, nil
}
