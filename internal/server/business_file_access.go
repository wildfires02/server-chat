package server

import (
	"chat/server/store/types"
)

// authorizeBusinessFileTopic 对 P2P 附件重新执行当前客户范围鉴权。
// 文件 ACL 证明用户曾经能读取该会话，而业务鉴权保证调岗、客户转移后旧订阅
// 不会继续授予附件访问权。群组和频道仍使用原有 Topic ACL。
func authorizeBusinessFileTopic(requester types.Uid, topic string) error {
	if globals.businessPolicy == nil || topic == "" {
		return nil
	}
	uid1, uid2, err := types.ParseP2P(topic)
	if err != nil {
		return nil
	}
	var target types.Uid
	switch requester {
	case uid1:
		target = uid2
	case uid2:
		target = uid1
	default:
		return types.ErrPermissionDenied
	}
	return globals.businessPolicy.authorizeUIDs(requester, target, "message", topic)
}
