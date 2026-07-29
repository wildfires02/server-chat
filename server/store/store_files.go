// Package store 提供领域模型及持久化访问层。
package store

import (
	"time"

	"chat/server/logs"
	"chat/server/store/types"
)

// FilePersistenceInterface 定义文件处理（记录或上传文件）的方法接口。
type FilePersistenceInterface interface {
	// StartUpload 记录给定用户发起了文件上传
	StartUpload(fd *types.FileDef) error
	// FinishUpload 将已开始的上传标记为成功完成。
	FinishUpload(fd *types.FileDef, success bool, size int64) (*types.FileDef, error)
	// Get 获取唯一文件 ID 的文件记录。
	Get(fid string) (*types.FileDef, error)
	// DeleteUnused 移除未使用的附件。
	DeleteUnused(olderThan time.Time, limit int) error
	// LinkAttachments 将之前上传的附件连接到消息或 Topic，以防止被垃圾回收。
	LinkAttachments(topic string, msgId types.Uid, attachments []string) error
}

// fileMapper 是实现 FilePersistenceInterface 的具体类型。
type fileMapper struct{}

// Files 是 FilePersistenceInterface 的单例实例，用于处理文件上传。
var Files FilePersistenceInterface

// StartUpload 记录给定用户发起了文件上传
func (fileMapper) StartUpload(fd *types.FileDef) error {
	fd.Status = types.UploadStarted
	return adp.FileStartUpload(fd)
}

// FinishUpload 将已开始的上传标记为成功完成或失败。
func (fileMapper) FinishUpload(fd *types.FileDef, success bool, size int64) (*types.FileDef, error) {
	return adp.FileFinishUpload(fd, success, size)
}

// Get 获取唯一文件 ID 的文件记录。
func (fileMapper) Get(fid string) (*types.FileDef, error) {
	return adp.FileGet(fid)
}

// DeleteUnused 移除未使用的附件和头像。
func (fileMapper) DeleteUnused(olderThan time.Time, limit int) error {
	toDel, err := adp.FileDeleteUnused(olderThan, limit)
	if err != nil {
		return err
	}
	if len(toDel) > 0 {
		logs.Warn.Println("deleting media", toDel)
		return Store.GetMediaHandler().Delete(toDel)
	}
	return nil
}

// LinkAttachments 将之前上传的附件连接到消息或 Topic，以防止被垃圾回收。
func (fileMapper) LinkAttachments(topic string, msgId types.Uid, attachments []string) error {
	// 将附件 URL 转换为文件 ID。
	var fids []string
	if len(attachments) > 0 && mediaHandler == nil {
		// 未配置本地媒体后端时，所有新引用均视为外部文件；编辑已有消息仍需
		// 清理过去由本地后端管理的附件关联。
		if !msgId.IsZero() {
			return adp.FileLinkAttachments(topic, types.ZeroUid, msgId, nil)
		}
		return nil
	}
	for _, url := range attachments {
		if fid := mediaHandler.GetIdFromUrl(url); !fid.IsZero() {
			fids = append(fids, fid.String())
		}
	}

	if len(fids) > 0 || !msgId.IsZero() {
		userId := types.ZeroUid
		if types.GetTopicCat(topic) == types.TopicCatMe {
			userId = types.ParseUserId(topic)
			topic = ""
		}
		return adp.FileLinkAttachments(topic, userId, msgId, fids)
	}
	return nil
}

// PersistentCacheInterface 定义访问持久化键值缓存的方法接口。
type PersistentCacheInterface interface {
	// Get 读取持久缓存条目。
	Get(key string) (string, error)
	// Upsert 创建或更新持久缓存条目。
	Upsert(key string, value string, failOnDuplicate bool) error
	// Delete 删除单个持久缓存条目。
	Delete(key string) error
	// Expire 使指定键前缀的较早条目过期。
	Expire(keyPrefix string, olderThan time.Time) error
}

// pcacheMapper 是实现 PersistentCacheInterface 的具体类型。
type pcacheMapper struct{}

// PCache 保存P缓存的共享实例或运行状态。
var PCache PersistentCacheInterface

// Get 读取持久缓存条目。
func (pcacheMapper) Get(key string) (string, error) {
	return adp.PCacheGet(key)
}

// Upsert 创建或更新持久缓存条目。
func (pcacheMapper) Upsert(key string, value string, failOnDuplicate bool) error {
	return adp.PCacheUpsert(key, value, failOnDuplicate)
}

// Delete 删除单个持久缓存条目。
func (pcacheMapper) Delete(key string) error {
	return adp.PCacheDelete(key)
}

// Expire 使指定键前缀的较早条目过期。
func (pcacheMapper) Expire(keyPrefix string, olderThan time.Time) error {
	return adp.PCacheExpire(keyPrefix, olderThan)
}
