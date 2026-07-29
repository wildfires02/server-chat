// Package common 提供数据库持久化、迁移或测试支持。
package common

import (
	"time"

	"chat/server/store"
	t "chat/server/store/types"
)

// DecodeUidSlice 将 t.Uid 切片解码转换为 []any 切片，元素为 int64。
func DecodeUidSlice(uids []t.Uid) []any {
	if len(uids) == 0 {
		return nil
	}
	unums := make([]any, len(uids))
	for i, uid := range uids {
		unums[i] = store.DecodeUid(uid)
	}
	return unums
}

// InitUnreadCountMap 初始化用户未读消息数的返回 map 和解码后的 uid 切片。
func InitUnreadCountMap(ids []t.Uid) (map[t.Uid]int, []any) {
	result := make(map[t.Uid]int, len(ids))
	uids := make([]any, len(ids))
	for i, uid := range ids {
		uids[i] = store.DecodeUid(uid)
		result[ids[i]] = 0
	}
	return result, uids
}

// AddDeviceToMap 将设备记录转换并追加到结果 map 中。
func AddDeviceToMap(result map[t.Uid][]t.DeviceDef, userid int64, deviceid, platform string, lastseen time.Time, lang string) {
	uid := store.EncodeUid(userid)
	result[uid] = append(result[uid], t.DeviceDef{
		DeviceId: deviceid,
		Platform: platform,
		LastSeen: lastseen,
		Lang:     lang,
	})
}

// ProcessP2PTarget 统一处理 P2P 订阅的对方 UID 提取与目标属性设置。
func ProcessP2PTarget(sub *t.Subscription, topicName string, currentUid t.Uid, usrq *[]any) bool {
	uid1, uid2, err := t.ParseP2P(topicName)
	if err != nil {
		return false
	}
	if uid1 == currentUid {
		*usrq = append(*usrq, store.DecodeUid(uid2))
		sub.SetWith(uid2.UserId())
	} else {
		*usrq = append(*usrq, store.DecodeUid(uid1))
		sub.SetWith(uid1.UserId())
	}
	return true
}

// LinkAttachmentParams 将 optional 的 userId 与 msgId 转为 SQL 参数绑定需要的类型（nil 或 int64）。
func LinkAttachmentParams(userId, msgId t.Uid) (user, msg any) {
	if !userId.IsZero() {
		user = store.DecodeUid(userId)
	}
	if !msgId.IsZero() {
		msg = store.DecodeUid(msgId)
	}
	return
}
