package server

import (
	"errors"
	"strings"
	"time"

	"chat/server/auth"
	"chat/server/store"
	"chat/server/store/types"
)

// replyGetSub 是对 Topic 上 get.sub 请求的响应 - 加载订阅/订阅者列表，
// 仅作为 {meta} 数据包发送给 Session
func (t *Topic) replyGetSub(sess *Session, asUid types.Uid, authLevel auth.Level, asChan bool, msg *ClientComMessage) error {
	now := types.TimeNow()
	id := msg.Id
	incomingReqTs := msg.Timestamp
	var req *MsgGetOpts
	if msg.Sub != nil {
		req = msg.Sub.Get.Sub
	} else {
		req = msg.Get.Sub
	}

	if req != nil && (req.SinceId != 0 || req.BeforeId != 0) {
		sess.queueOut(ErrMalformedReply(msg, now))
		return errors.New("invalid MsgGetOpts query")
	}

	var err error

	var ifModified time.Time
	if req != nil && req.IfModifiedSince != nil {
		ifModified = *req.IfModifiedSince
	}

	userData := t.perUser[asUid]
	var subs []types.Subscription

	switch t.cat {
	case types.TopicCatMe:
		if req != nil {
			// 如果提供了 Topic，它可能是用户 ID 'usrAbCd' 的形式。
			// 将其转换为 P2P Topic 名称。同样对于 Self Topic 'slf' -> 'slfAbcD'。
			if uid2 := types.ParseUserId(req.Topic); !uid2.IsZero() {
				req.Topic = uid2.P2PName(asUid)
			}
			if req.Topic == "slf" {
				req.Topic = asUid.SlfName()
			}
		}
		// 获取用户的订阅，其中 Topic.Public+Topic.Trusted 已反规范化到订阅中。
		if ifModified.IsZero() {
			// 无缓存管理。跳过已删除的订阅。
			subs, err = store.Users.GetTopics(asUid, msgOpts2storeOpts(req))
		} else {
			// 用户管理缓存。也包括已删除的订阅。
			subs, err = store.Users.GetTopicsAny(asUid, msgOpts2storeOpts(req))

			// 返回的订阅不包含现在在线但未更改的 Topic。
			// 我们需要将这些 Topic 添加到列表中，否则用户会看到它们离线。
			selected := map[string]struct{}{}
			for i := range subs {
				sub := &subs[i]
				with := sub.GetWith()
				if with != "" {
					selected[with] = struct{}{}
				} else {
					selected[sub.Topic] = struct{}{}
				}
			}

			// 为缺失的在线 Topic 添加虚拟订阅。
			for topic, psd := range t.perSubs {
				_, present := selected[topic]
				if !present && psd.online {
					sub := types.Subscription{Topic: topic}
					sub.SetWith(topic)
					sub.SetDummy(true)
					subs = append(subs, sub)
				}
			}
		}
	case types.TopicCatFnd:
		// 选择公共或私有查询。公共查询是交互式设置的，具有优先级。
		query := t.fndGetPublic(sess)
		if query == "" {
			query, _ = userData.private.(string)
		}

		// 空查询被忽略，返回 "NoContent"。
		if query != "" {
			query, subs, err = pluginFind(asUid, query)
			if err == nil && subs == nil && query != "" {
				if and, opt, err := parseTagSearchQuery(query); err == nil {
					var req [][]string
					for _, tag := range and {
						rewritten := rewriteTag(tag, sess.countryCode)
						if len(rewritten) > 0 {
							req = append(req, rewritten)
						}
					}
					opt = rewriteTagSlice(opt, sess.countryCode)

					// 检查查询是否包含用户不允许使用的术语。
					if restr, _, _ := stringSliceDelta(t.tags,
						filterTags(append(types.FlattenDoubleSlice(req), opt...), globals.maskedTagNS)); len(restr) > 0 {
						sess.queueOut(ErrPermissionDeniedReply(msg, now))
						return errors.New("attempt to search by restricted tags")
					}

					// 普通用户：只查找活跃的 Topic 和账户。
					// Root 用户：查找所有 Topic 和账户，包括已挂起和软删除的。
					subs, err = store.Users.FindSubs(asUid, globals.aliasTagNS, req, opt, sess.authLvl != auth.LevelRoot)
					if err != nil {
						sess.queueOut(decodeStoreErrorExplicitTs(err, id, msg.Original, now, incomingReqTs, nil))
						return err
					}
				}
			}
		}
	case types.TopicCatP2P:
		// 内存优先：无 If-Modified-Since 校验且内存中包含订阅缓存时，直接使用 perUserData 构造订阅，免除 DB 查询
		if ifModified.IsZero() && len(t.perUser) > 0 {
			subs = make([]types.Subscription, 0, len(t.perUser))
			for uid, pud := range t.perUser {
				sub := types.Subscription{
					User:      uid.String(),
					Topic:     t.name,
					ModeWant:  pud.modeWant,
					ModeGiven: pud.modeGiven,
					Private:   pud.private,
					DelId:     pud.delID,
					ReadSeqId: pud.readID,
					RecvSeqId: pud.recvID,
				}
				sub.SetPublic(pud.public)
				sub.SetTrusted(pud.trusted)
				subs = append(subs, sub)
			}
		} else if ifModified.IsZero() {
			subs, err = store.Topics.GetSubs(t.name, msgOpts2storeOpts(req))
		} else {
			// 含有 ifModified 增量筛选条件，退避从 DB 查询（可能包含已软删除的旧订阅）
			subs, err = store.Topics.GetSubsAny(t.name, msgOpts2storeOpts(req))
		}
	case types.TopicCatGrp:
		topicName := t.name
		if asChan {
			// 在 Channel 情况下，仅允许获取当前用户的订阅。
			if req == nil {
				req = &MsgGetOpts{}
			}
			req.User = asUid.UserId()
			// Channel 订阅者使用 chnXXX Topic 名称而不是 grpXXX。
			topicName = msg.Original
		} else if t.isChan && req != nil && req.Topic != "" {
			if req.Topic != types.GrpToChn(t.name) {
				sess.queueOut(ErrMalformedReply(msg, now))
				return errors.New("channel subscriber topic does not match the current channel")
			}
			// 频道管理员可通过 sub.topic=chn... 查询离线读者；普通频道访问仍只返回自己。
			if !(userData.modeGiven & userData.modeWant).IsAdmin() {
				sess.queueOut(ErrPermissionDeniedReply(msg, now))
				return errors.New("channel subscriber list requires admin permission")
			}
			topicName = req.Topic
		}
		// 包含 sub.Public。
		if ifModified.IsZero() {
			// 无缓存管理。跳过已删除的订阅。
			subs, err = store.Topics.GetUsers(topicName, msgOpts2storeOpts(req))
		} else {
			// 用户管理缓存。也包括已删除的订阅。
			subs, err = store.Topics.GetUsersAny(topicName, msgOpts2storeOpts(req))
		}
		// 对所有其它 Topic 类型（如 'sys'、'slf'）不执行任何操作。
	}

	if err != nil {
		sess.queueOut(decodeStoreErrorExplicitTs(err, id, msg.Original, now, incomingReqTs, nil))
		return err
	}

	if len(subs) == 0 {
		// 通知客户端没有订阅。
		sess.queueOut(NoContentParamsReply(msg, now, map[string]any{"what": "sub"}))
		return nil
	}

	meta := &MsgServerMeta{
		Id:        id,
		Topic:     msg.Original,
		Sub:       make([]MsgTopicSub, 0, len(subs)),
		Timestamp: &now}
	presencer := (userData.modeGiven & userData.modeWant).IsPresencer()
	sharer := (userData.modeGiven & userData.modeWant).IsSharer()

	for i := range subs {
		sub := &subs[i]
		// 指示符表示请求者是否提供了 pub & priv 更新的时间截止日期。
		var sendPubPriv bool
		var banned bool
		var mts MsgTopicSub
		deleted := sub.DeletedAt != nil

		if ifModified.IsZero() {
			sendPubPriv = true
		} else {
			// 如果在截止日期之前删除，则跳过发送已删除的订阅。
			// 如果它们是最近删除的，则发送最少的信息。
			if deleted {
				if !sub.DeletedAt.After(ifModified) {
					continue
				}
				mts.DeletedAt = sub.DeletedAt
			}
			sendPubPriv = !deleted && sub.UpdatedAt.After(ifModified)
		}

		uid := types.ParseUid(sub.User)
		subMode := sub.ModeGiven & sub.ModeWant
		isReader := subMode.IsReader()
		if t.cat == types.TopicCatMe {
			// 标记用户不关心的订阅。
			if !subMode.IsJoiner() {
				banned = true
			}

			// 向其它 Topic 汇报用户的订阅。P2P Topic 名称是
			// 另一个用户的 UID。
			with := sub.GetWith()
			if with != "" {
				mts.Topic = with
				mts.Online = t.perSubs[with].online && !deleted && presencer
			} else if strings.HasPrefix(sub.Topic, "slf") {
				mts.Topic = "slf"
				// 不汇报 Online，因为对 slf 没有意义。
			} else {
				mts.Topic = sub.Topic
				mts.Online = t.perSubs[sub.Topic].online && !deleted && presencer
			}

			if !deleted && !banned {
				if isReader {
					touchedAt := sub.GetTouchedAt()
					if touchedAt.IsZero() {
						mts.TouchedAt = nil
					} else {
						mts.TouchedAt = &touchedAt
					}
					mts.SeqId = sub.GetSeqId()
					mts.DelId = sub.DelId
				} else if !sub.UpdatedAt.IsZero() {
					mts.TouchedAt = &sub.UpdatedAt
				}

				lastSeen := sub.GetLastSeen()
				if lastSeen != nil && !mts.Online {
					mts.LastSeen = &MsgLastSeenInfo{
						When:      lastSeen,
						UserAgent: sub.GetUserAgent(),
					}
				}

				mts.SubCnt = sub.GetSubCnt()
			}
		} else {
			// 标记用户不关心的订阅。
			if t.cat == types.TopicCatGrp && !subMode.IsJoiner() {
				banned = true
			}

			// 向 fnd、群组或 p2p Topic 汇报订阅者
			mts.User = uid.UserId()
			if t.cat == types.TopicCatFnd {
				mts.Topic = sub.Topic
			}

			if !deleted {
				if uid == asUid && isReader && !banned {
					// 仅汇报自己订阅的已删除 ID
					mts.DelId = sub.DelId
				}

				if t.cat == types.TopicCatGrp {
					pud := t.perUser[uid]
					mts.Online = pud.online > 0 && presencer
				}
			}
		}

		if !deleted {
			if !sub.UpdatedAt.IsZero() {
				mts.UpdatedAt = &sub.UpdatedAt
			}

			if isReader && !banned {
				mts.ReadSeqId = sub.ReadSeqId
				mts.RecvSeqId = sub.RecvSeqId
			}

			if t.cat != types.TopicCatFnd {
				// p2p and grp
				if !sub.IsDummy() && (sharer || uid == asUid || subMode.IsAdmin()) {
					// 如果用户不是 sharer，则无法访问其它普通用户的访问模式。
					// 仅自己和 admin 权限对非 sharer 可见。
					mts.Acs.Mode = subMode.String()
					mts.Acs.Want = sub.ModeWant.String()
					mts.Acs.Given = sub.ModeGiven.String()
					mts.Acs.Role = topicRoleFromAccess(subMode, t.isChan, types.IsChannel(sub.Topic))
				}
			} else {
				// Topic 'fnd'
				// sub.ModeXXX 可能由插件定义。
				if sub.ModeGiven.IsDefined() && sub.ModeWant.IsDefined() {
					mts.Acs.Mode = subMode.String()
					mts.Acs.Want = sub.ModeWant.String()
					mts.Acs.Given = sub.ModeGiven.String()
				} else if types.IsChannel(sub.Topic) {
					mts.Acs.Mode = types.ModeCChnReader.String()
				} else if defacs := sub.GetDefaultAccess(); defacs != nil {
					switch authLevel {
					case auth.LevelAnon:
						mts.Acs.Mode = defacs.Anon.String()
					case auth.LevelAuth, auth.LevelRoot:
						mts.Acs.Mode = defacs.Auth.String()
					}
				}
				mts.SubCnt = sub.GetSubCnt()
			}

			// 返回 public 和 private 仅当它们自 ifModified 以来已更改
			if sendPubPriv {
				// 'sub' 在 P2P Topic 中有 nil 'public'/'trusted'，这是正常的。
				mts.Public = sub.GetPublic()
				mts.Trusted = sub.GetTrusted()
				// 仅当是用户自己的订阅时才汇报 'private'。
				if uid == asUid {
					mts.Private = sub.Private
				}
			}

			// 始终为 fnd Topic 汇报 'private'。
			if t.cat == types.TopicCatFnd {
				mts.Private = sub.Private
			}
		}

		meta.Sub = append(meta.Sub, mts)
	}

	sess.queueOut(&ServerComMessage{Meta: meta})

	return nil
}

// replySetSub 是对新订阅请求或更新订阅 {set.sub} 的响应：
// 更新 Topic 元数据缓存，保存/更新订阅，作为 {ctrl} 消息回复给调用者，
// 如果适当则生成在线状态通知。
func (t *Topic) replySetSub(sess *Session, pkt *ClientComMessage, asChan bool) error {
	now := types.TimeNow()

	asUid := types.ParseUserId(pkt.AsUser)
	set := pkt.Set
	if set.Sub.Role != "" && set.Sub.Mode != "" {
		sess.queueOut(ErrMalformedReply(pkt, now))
		return errors.New("set.sub role and mode are mutually exclusive")
	}

	var target types.Uid
	if target = types.ParseUserId(set.Sub.User); target.IsZero() && set.Sub.User != "" {
		// 无效的用户 ID
		sess.queueOut(ErrMalformedReply(pkt, now))
		return errors.New("invalid user id")
	}

	// 如果 set.用户 未设置，请求是针对当前用户
	if target.IsZero() {
		target = asUid
	}

	var err error
	var modeChanged *MsgAccessMode
	if target == asUid {
		if set.Sub.Role != "" {
			sess.queueOut(ErrMalformedReply(pkt, now))
			return errors.New("set.sub role requires an explicit target user")
		}
		// 请求新订阅或修改自己的订阅
		modeChanged, err = t.thisUserSub(sess, pkt, asUid, asChan, set.Sub.Mode, nil)
	} else if set.Sub.Role != "" {
		// 使用安全角色预设批准、禁言、封禁或调整成员。
		modeChanged, err = t.setAnotherUserRole(sess, asUid, target, asChan, pkt)
	} else {
		// 请求批准/更改某人的订阅
		modeChanged, err = t.anotherUserSub(sess, asUid, target, asChan, pkt)
	}
	if err != nil {
		return err
	}

	var resp *ServerComMessage
	if modeChanged != nil {
		// 汇报结果访问模式。
		params := map[string]any{"acs": modeChanged}
		if target != asUid {
			params["user"] = target.UserId()
		}
		resp = NoErrParamsReply(pkt, now, params)
	} else {
		resp = InfoNotModifiedReply(pkt, now)
	}

	sess.queueOut(resp)

	return nil
}
