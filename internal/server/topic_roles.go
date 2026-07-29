// Package server 实现即时通信服务端的协议、路由和业务逻辑。
package server

import (
	"errors"
	"strings"

	"chat/server/store"
	"chat/server/store/types"
)

// topicRoleAccess 保存业务角色对应的 ACL 以及频道订阅命名空间。
// ChannelSub 为 true 时订阅记录存放在 chn... 下，用户只能以读者身份访问频道。
type topicRoleAccess struct {
	Name       string
	Mode       types.AccessMode
	ChannelSub bool
}

// resolveTopicRole 把客户端角色转换为服务端 ACL。
// 角色是对 ACL 的安全预设，不取代底层 Mode 接口。
func resolveTopicRole(role string, channel bool) (topicRoleAccess, error) {
	role = strings.ToLower(strings.TrimSpace(role))

	switch role {
	case "admin":
		return topicRoleAccess{
			Name: role,
			Mode: types.ModeCFull &^ types.ModeOwner,
		}, nil
	case "banned":
		return topicRoleAccess{
			Name:       role,
			Mode:       types.ModeNone,
			ChannelSub: channel,
		}, nil
	}

	if channel {
		switch role {
		case "publisher":
			return topicRoleAccess{
				Name: role,
				Mode: types.ModeJoin | types.ModeRead | types.ModeWrite | types.ModePres,
			}, nil
		case "subscriber", "readonly":
			return topicRoleAccess{
				Name:       "subscriber",
				Mode:       types.ModeCChnReader,
				ChannelSub: true,
			}, nil
		default:
			return topicRoleAccess{}, errors.New("invalid channel role")
		}
	}

	switch role {
	case "member":
		return topicRoleAccess{
			Name: role,
			Mode: types.ModeJoin | types.ModeRead | types.ModeWrite | types.ModePres,
		}, nil
	case "readonly":
		return topicRoleAccess{
			Name: role,
			Mode: types.ModeCChnReader,
		}, nil
	default:
		return topicRoleAccess{}, errors.New("invalid group role")
	}
}

// topicRoleFromAccess 根据最终生效的 ACL 推导稳定的业务角色。
func topicRoleFromAccess(mode types.AccessMode, channel, channelSub bool) string {
	switch {
	case !mode.IsJoiner():
		return "banned"
	case mode.IsOwner():
		return "owner"
	case channel && channelSub:
		// chn... 命名空间始终代表只读读者；忽略旧数据中残留的管理或写位。
		return "subscriber"
	case mode.IsAdmin():
		return "admin"
	case channel && mode.IsWriter():
		return "publisher"
	case channel:
		return "readonly"
	case mode.IsWriter():
		return "member"
	default:
		return "readonly"
	}
}

// setAnotherUserRole 使用业务角色邀请或更新成员。
// 它同时支持普通群成员与存储在 chn... 命名空间下的广播频道订阅读者。
func (t *Topic) setAnotherUserRole(sess *Session, asUid, target types.Uid, asChan bool,
	pkt *ClientComMessage) (*MsgAccessMode, error) {

	now := types.TimeNow()
	if t.cat != types.TopicCatGrp || asChan {
		sess.queueOut(ErrPermissionDeniedReply(pkt, now))
		return nil, errors.New("roles can only be managed through a group topic")
	}

	hostData, ok := t.perUser[asUid]
	hostMode := hostData.modeGiven & hostData.modeWant
	if !ok || !hostMode.IsAdmin() {
		sess.queueOut(ErrPermissionDeniedReply(pkt, now))
		return nil, errors.New("member role change requires admin permission")
	}
	if t.isReadOnly() {
		sess.queueOut(ErrPermissionDeniedReply(pkt, now))
		return nil, errors.New("topic is suspended")
	}

	requested, err := resolveTopicRole(pkt.Set.Sub.Role, t.isChan)
	if err != nil {
		sess.queueOut(ErrMalformedReply(pkt, now))
		return nil, err
	}
	if requested.Name == "admin" && t.owner != asUid {
		sess.queueOut(ErrPermissionDeniedReply(pkt, now))
		return nil, errors.New("only the owner can promote an admin")
	}
	if t.owner != asUid && !hostMode.BetterEqual(requested.Mode) {
		sess.queueOut(ErrPermissionDeniedReply(pkt, now))
		return nil, errors.New("admin cannot grant permissions they do not have")
	}

	groupTopic := t.name
	channelTopic := types.GrpToChn(t.name)
	var current *types.Subscription
	var currentIsChannel bool

	// 在线成员优先使用内存快照；频道的离线读者再从 chn... 订阅表加载。
	if pud, found := t.perUser[target]; found {
		currentIsChannel = pud.isChan
		currentTopic := groupTopic
		if currentIsChannel {
			currentTopic = channelTopic
		}
		current = &types.Subscription{
			User:      target.String(),
			Topic:     currentTopic,
			ModeWant:  pud.modeWant,
			ModeGiven: pud.modeGiven,
			Private:   pud.private,
			DelId:     pud.delID,
			ReadSeqId: pud.readID,
			RecvSeqId: pud.recvID,
		}
	} else {
		current, err = store.Subs.Get(groupTopic, target, false)
		if err == nil && current == nil && t.isChan {
			current, err = store.Subs.Get(channelTopic, target, false)
			currentIsChannel = current != nil
		}
		if err != nil {
			sess.queueOut(ErrUnknownReply(pkt, now))
			return nil, err
		}
	}

	if current != nil {
		currentMode := current.ModeWant & current.ModeGiven
		if currentMode.IsOwner() {
			sess.queueOut(ErrPermissionDeniedReply(pkt, now))
			return nil, errors.New("topic owner role cannot be changed")
		}
		if currentMode.IsAdmin() && t.owner != asUid {
			sess.queueOut(ErrPermissionDeniedReply(pkt, now))
			return nil, errors.New("only the owner can change another admin")
		}
		// 对已有频道成员执行 banned 时沿用当前命名空间，避免把发布者误封为读者。
		if requested.Name == "banned" {
			requested.ChannelSub = currentIsChannel
		}
	}

	targetTopic := groupTopic
	if requested.ChannelSub {
		targetTopic = channelTopic
	}

	oldWant, oldGiven := types.ModeUnset, types.ModeUnset
	if current != nil {
		oldWant, oldGiven = current.ModeWant, current.ModeGiven
	}
	if current != nil && current.Topic == targetTopic &&
		current.ModeWant == requested.Mode && current.ModeGiven == requested.Mode {
		return nil, nil
	}

	if current == nil {
		// 普通群成员和频道发布者受成员上限约束；频道读者保持可扩展订阅语义。
		if !requested.ChannelSub && t.subsCount() >= globals.maxSubscriberCount {
			sess.queueOut(ErrPolicyReply(pkt, now))
			return nil, errors.New("maximum group member count exceeded")
		}
		user, getErr := store.Users.Get(target)
		if getErr != nil {
			sess.queueOut(ErrUnknownReply(pkt, now))
			return nil, getErr
		}
		if user == nil {
			sess.queueOut(ErrUserNotFoundReply(pkt, now))
			return nil, errors.New("user not found")
		}
		if user.State != types.StateOK {
			sess.queueOut(ErrPermissionDeniedReply(pkt, now))
			return nil, errors.New("user is suspended")
		}
		if err = store.Subs.Create(&types.Subscription{
			User:      target.String(),
			Topic:     targetTopic,
			ModeWant:  requested.Mode,
			ModeGiven: requested.Mode,
		}); err != nil {
			sess.queueOut(ErrUnknownReply(pkt, now))
			return nil, err
		}
		t.subCnt++
	} else if current.Topic == targetTopic {
		if err = store.Subs.Update(targetTopic, target, map[string]any{
			"ModeWant":  requested.Mode,
			"ModeGiven": requested.Mode,
		}); err != nil {
			sess.queueOut(ErrUnknownReply(pkt, now))
			return nil, err
		}
	} else {
		// 发布者与频道读者使用不同 Topic 键。先创建新订阅，再删除旧订阅；
		// 删除失败时回滚新订阅，避免成员同时拥有两套身份。
		if err = store.Subs.Create(&types.Subscription{
			User:      target.String(),
			Topic:     targetTopic,
			ModeWant:  requested.Mode,
			ModeGiven: requested.Mode,
			Private:   current.Private,
		}); err != nil {
			sess.queueOut(ErrUnknownReply(pkt, now))
			return nil, err
		}
		if err = store.Subs.Delete(current.Topic, target); err != nil {
			_ = store.Subs.Delete(targetTopic, target)
			sess.queueOut(ErrUnknownReply(pkt, now))
			return nil, err
		}
	}

	oldPud, wasCached := t.perUser[target]
	if wasCached && currentIsChannel != requested.ChannelSub {
		t.evictUser(target, false, "")
		oldPud.online = 0
	}

	newPud := oldPud
	newPud.modeWant = requested.Mode
	newPud.modeGiven = requested.Mode
	newPud.isChan = requested.ChannelSub
	newPud.deleted = false

	// 普通群成员和频道发布者长期保留在内存；离线频道读者按需加载，避免大频道占用内存。
	if !requested.ChannelSub || wasCached {
		t.perUser[target] = newPud
	}
	t.computePerUserAcsUnion()

	t.notifySubChange(target, asUid, requested.ChannelSub,
		oldWant, oldGiven, requested.Mode, requested.Mode, sess.sid)
	if requested.ChannelSub {
		t.channelSubUnsub(target, requested.Mode.IsJoiner() && requested.Mode.IsPresencer())
	} else if currentIsChannel {
		// 读者提升为发布者后不再使用频道读者的推送订阅。
		t.channelSubUnsub(target, false)
	}
	if requested.Name == "banned" {
		t.evictUser(target, false, "")
	}

	sub := &types.Subscription{
		User:      target.String(),
		Topic:     targetTopic,
		ModeWant:  requested.Mode,
		ModeGiven: requested.Mode,
	}
	if current == nil {
		if !requested.ChannelSub && requested.Mode.IsJoiner() {
			usersRegisterUser(target, true)
		}
		pluginSubscription(sub, plgActCreate)
	} else {
		pluginSubscription(sub, plgActUpd)
	}

	return &MsgAccessMode{
		Want:  requested.Mode.String(),
		Given: requested.Mode.String(),
		Mode:  requested.Mode.String(),
		Role:  topicRoleFromAccess(requested.Mode, t.isChan, requested.ChannelSub),
	}, nil
}
