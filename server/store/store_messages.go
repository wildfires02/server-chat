// Package store 提供领域模型及持久化访问层。
package store

import (
	"sort"
	"time"

	"chat/server/drafty"
	"chat/server/logs"
	"chat/server/store/types"
)

// MessagesPersistenceInterface 定义消息持久化存储的方法接口。
type MessagesPersistenceInterface interface {
	// Save 原子保存普通消息，并关联正文中的附件。
	Save(msg *types.Message, attachmentURLs []string, readBySender bool) (error, bool)
	// GetByClientId 按发送者范围内的客户端幂等键查询已投递消息。
	GetByClientId(topic string, from types.Uid, clientID string) (*types.Message, error)
	// Get 按 Topic 与 SeqId 查询单条未硬删除消息。
	Get(topic string, seqID int) (*types.Message, error)
	// Update 更新已有消息的正文与服务端元数据。
	Update(msg *types.Message) error
	// Schedule 保存尚未进入 Topic 序列的定时消息。
	Schedule(msg *types.ScheduledMessage) error
	// GetScheduledByClientId 按发送者范围内的幂等键查询待投递消息。
	GetScheduledByClientId(topic string, from types.Uid, clientID string) (*types.ScheduledMessage, error)
	// GetDueScheduled 按投递时间读取一批已到期消息。
	GetDueScheduled(now time.Time, limit int) ([]types.ScheduledMessage, error)
	// DeleteScheduled 删除已投递或被取消的定时消息。
	DeleteScheduled(id, topic string, from types.Uid) error
	// DeleteList 删除或清理List。
	DeleteList(topic string, delID int, forUser types.Uid, msgDelAge time.Duration, ranges []types.Range) error
	// GetAll 查询并返回All。
	GetAll(topic string, forUser types.Uid, opt *types.QueryOpt) ([]types.Message, error)
	// GetLatest 批量查询每个 Topic 对当前用户可见的最后一条消息。
	GetLatest(topics []string, forUser types.Uid) ([]types.Message, error)
	// GetDeleted 查询并返回Deleted。
	GetDeleted(topic string, forUser types.Uid, opt *types.QueryOpt) ([]types.Range, int, error)
	// Search 在单个 Topic 内执行权限感知的消息全文搜索。
	Search(topic string, forUser types.Uid, query *types.MessageSearchQuery) ([]types.Message, error)
}

// messagesMapper 是实现 MessagesPersistenceInterface 的具体类型。
type messagesMapper struct{}

// Messages 是导出 MessagesPersistenceInterface 的单例锚对象。
var Messages MessagesPersistenceInterface

// Save 原子保存消息、更新发送者已读游标，并关联正文中的附件。
func (messagesMapper) Save(msg *types.Message, attachmentURLs []string, readBySender bool) (error, bool) {
	msg.InitTimes()
	msg.SetUid(Store.GetUid())
	msg.InitClientKey()
	searchText, err := drafty.SearchText(msg.Content)
	if err != nil {
		return err, false
	}
	msg.SearchText = searchText
	// Topic 游标与消息必须作为一个持久化单元提交，避免崩溃产生 SeqId 空洞。
	err = adp.MessageSaveAtomic(msg)
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

	if len(attachmentURLs) > 0 && mediaHandler != nil {
		attachmentURLs = FileURLsWithPreviews(attachmentURLs)
		var attachments []string
		seenAttachments := make(map[string]bool)
		for _, url := range attachmentURLs {
			// 将附件 URL 转换为文件 ID。
			if fid := mediaHandler.GetIdFromUrl(url); !fid.IsZero() {
				id := fid.String()
				if !seenAttachments[id] {
					seenAttachments[id] = true
					attachments = append(attachments, id)
				}
			}
		}
		if len(attachments) > 0 {
			if err := adp.FileLinkAttachments("", types.ZeroUid, msg.Uid(), attachments); err != nil {
				// 核心消息已经提交，不能再向发布方返回失败，否则旧客户端会重试并产生歧义。
				logs.Warn.Printf("topic[%s]: failed to link attachments for message (seq: %d) - err: %+v",
					msg.Topic, msg.SeqId, err)
			} else if err := GrantFileAccess(msg.Topic, types.ZeroUid, attachmentURLs); err != nil {
				logs.Warn.Printf("topic[%s]: failed to create attachment ACL for message (seq: %d) - err: %+v",
					msg.Topic, msg.SeqId, err)
			}
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

// GetLatest 在一次持久化查询中返回各 Topic 的最后一条可见消息。
func (messagesMapper) GetLatest(topics []string, forUser types.Uid) ([]types.Message, error) {
	if len(topics) == 0 {
		return nil, nil
	}
	return adp.MessageGetLatest(topics, forUser)
}

// GetByClientId 按发送方生成的幂等键读取消息。
func (messagesMapper) GetByClientId(topic string, from types.Uid, clientID string) (*types.Message, error) {
	return adp.MessageGetByClientId(topic, from, clientID)
}

// Get 按 Topic 和 seq 读取未硬删除消息。
func (messagesMapper) Get(topic string, seqID int) (*types.Message, error) {
	return adp.MessageGet(topic, seqID)
}

// Update 原地更新消息内容和服务端管理的元数据。
func (messagesMapper) Update(msg *types.Message) error {
	if msg.UpdatedAt.IsZero() {
		msg.UpdatedAt = types.TimeNow()
	}
	searchText, err := drafty.SearchText(msg.Content)
	if err != nil {
		return err
	}
	msg.SearchText = searchText
	return adp.MessageUpdate(msg)
}

// Schedule 保存定时消息，并在独立关联表中保护待投递附件不被垃圾回收。
func (messagesMapper) Schedule(msg *types.ScheduledMessage) error {
	msg.InitTimes()
	// 定时消息使用全局 UID 作为跨节点稳定的队列主键。
	if msg.Uid().IsZero() {
		msg.SetUid(Store.GetUid())
	}
	// 内部调用未给 cid 时生成稳定值；协议入口仍要求客户端显式提供 cid。
	if msg.ClientId == "" {
		msg.ClientId = "sch-" + msg.Id
	}
	if mediaHandler != nil {
		// 数据库关联使用文件 ID；原始 URL 留到投递普通消息时再次关联。
		for _, url := range msg.AttachmentURLs {
			if fid := mediaHandler.GetIdFromUrl(url); !fid.IsZero() {
				msg.Attachments = append(msg.Attachments, fid.String())
			}
		}
	}
	if err := adp.MessageSchedule(msg); err != nil {
		return err
	}
	if len(msg.Attachments) > 0 {
		if err := adp.FileLinkScheduled(msg.Uid(), msg.Attachments); err != nil {
			// 附件保护失败时回滚队列记录，避免产生无法完整投递的定时消息。
			_ = adp.MessageDeleteScheduled(msg.Id, msg.Topic, types.ParseUid(msg.From))
			return err
		}
	}
	return nil
}

// GetScheduledByClientId 按 Topic、发送者和 cid 查询待投递消息。
func (messagesMapper) GetScheduledByClientId(topic string, from types.Uid, clientID string) (*types.ScheduledMessage, error) {
	return adp.MessageGetScheduledByClientId(topic, from, clientID)
}

// GetDueScheduled 返回指定时间前到期的定时消息。
func (messagesMapper) GetDueScheduled(now time.Time, limit int) ([]types.ScheduledMessage, error) {
	return adp.MessageGetDueScheduled(now, limit)
}

// DeleteScheduled 删除指定发送者拥有的定时消息及其级联附件关联。
func (messagesMapper) DeleteScheduled(id, topic string, from types.Uid) error {
	return adp.MessageDeleteScheduled(id, topic, from)
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

// Search 在单个 Topic 内执行权限感知的消息全文搜索。
func (messagesMapper) Search(topic string, forUser types.Uid, query *types.MessageSearchQuery) ([]types.Message, error) {
	if query == nil || query.Query == "" || query.Limit <= 0 {
		return nil, nil
	}
	return adp.MessageSearch(topic, forUser, query)
}
