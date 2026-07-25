package main

import (
	"errors"
	"sort"
	"strings"
	"time"

	"chat/server/auth"
	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
)

func (t *Topic) handleMetaGet(msg *ClientComMessage, asUid types.Uid, asChan bool, authLevel auth.Level) {
	if msg.MetaWhat&constMsgMetaDesc != 0 {
		if err := t.replyGetDesc(msg.sess, asUid, asChan, msg.Get.Desc, msg); err != nil {
			logs.Warn.Printf("topic[%s] meta.Get.Desc failed: %s", t.name, err)
		}
	}
	if msg.MetaWhat&constMsgMetaSub != 0 {
		if err := t.replyGetSub(msg.sess, asUid, authLevel, asChan, msg); err != nil {
			logs.Warn.Printf("topic[%s] meta.Get.Sub failed: %s", t.name, err)
		}
	}
	if msg.MetaWhat&constMsgMetaData != 0 {
		if err := t.replyGetData(msg.sess, asUid, asChan, msg.Get.Data, msg); err != nil {
			logs.Warn.Printf("topic[%s] meta.Get.Data failed: %s", t.name, err)
		}
	}
	if msg.MetaWhat&constMsgMetaDel != 0 {
		if err := t.replyGetDel(msg.sess, asUid, msg.Get.Del, msg); err != nil {
			logs.Warn.Printf("topic[%s] meta.Get.Del failed: %s", t.name, err)
		}
	}
	if msg.MetaWhat&constMsgMetaTags != 0 {
		if err := t.replyGetTags(msg.sess, asUid, msg); err != nil {
			logs.Warn.Printf("topic[%s] meta.Get.Tags failed: %s", t.name, err)
		}
	}
	if msg.MetaWhat&constMsgMetaCred != 0 {
		if err := t.replyGetCreds(msg.sess, asUid, msg); err != nil {
			logs.Warn.Printf("topic[%s] meta.Get.Creds failed: %s", t.name, err)
		}
	}
	if msg.MetaWhat&constMsgMetaAux != 0 {
		logs.Warn.Printf("topic[%s] handle getAux", t.name)
		if err := t.replyGetAux(msg.sess, asUid, msg); err != nil {
			logs.Warn.Printf("topic[%s] meta.Get.Aux failed: %s", t.name, err)
		}
	}
}

func (t *Topic) handleMetaSet(msg *ClientComMessage, asUid types.Uid, asChan bool, authLevel auth.Level) {
	if msg.MetaWhat&constMsgMetaDesc != 0 {
		if err := t.replySetDesc(msg.sess, asUid, asChan, authLevel, msg); err == nil {
			// 通知插件更新
			pluginTopic(t, plgActUpd)
		} else {
			logs.Warn.Printf("topic[%s] meta.Set.Desc failed: %v", t.name, err)
		}
	}
	if msg.MetaWhat&constMsgMetaSub != 0 {
		if err := t.replySetSub(msg.sess, msg, asChan); err != nil {
			logs.Warn.Printf("topic[%s] meta.Set.Sub failed: %v", t.name, err)
		}
	}
	if msg.MetaWhat&constMsgMetaTags != 0 {
		if err := t.replySetTags(msg.sess, asUid, msg); err != nil {
			logs.Warn.Printf("topic[%s] meta.Set.Tags failed: %v", t.name, err)
		}
	}
	if msg.MetaWhat&constMsgMetaCred != 0 {
		if err := t.replySetCred(msg.sess, asUid, authLevel, msg); err != nil {
			logs.Warn.Printf("topic[%s] meta.Set.Cred failed: %v", t.name, err)
		}
	}
	if msg.MetaWhat&constMsgMetaAux != 0 {
		if err := t.replySetAux(msg.sess, asUid, msg); err != nil {
			logs.Warn.Printf("topic[%s] meta.Set.Aux failed: %v", t.name, err)
		}
	}
}

func (t *Topic) handleMetaDel(msg *ClientComMessage, asUid types.Uid, asChan bool, authLevel auth.Level) {
	var err error
	switch msg.MetaWhat {
	case constMsgDelMsg:
		err = t.replyDelMsg(msg.sess, asUid, asChan, msg)
	case constMsgDelSub:
		err = t.replyDelSub(msg.sess, asUid, msg)
	case constMsgDelTopic:
		err = t.replyDelTopic(msg.sess, asUid, msg)
	case constMsgDelCred:
		err = t.replyDelCred(msg.sess, asUid, authLevel, msg)
	}

	if err != nil {
		logs.Warn.Printf("topic[%s] meta.Del failed: %v", t.name, err)
	}
}

// handleMeta 实现通过 Topic.meta Channel 接收的元数据请求处理逻辑
func (t *Topic) handleMeta(msg *ClientComMessage) {
	// 请求获取/设置 Topic 元数据
	asUid := types.ParseUserId(msg.AsUser)
	authLevel := auth.Level(msg.AuthLvl)
	asChan, err := t.verifyChannelAccess(msg.Original)
	if err != nil {
		// 用户不应能将非 Channel Topic 作为 Channel 寻址。
		msg.sess.queueOut(ErrNotFoundReply(msg, types.TimeNow()))
		return
	}
	switch {
	case msg.Get != nil:
		// Get 请求
		t.handleMetaGet(msg, asUid, asChan, authLevel)

	case msg.Set != nil:
		// Set 请求
		t.handleMetaSet(msg, asUid, asChan, authLevel)

	case msg.Del != nil:
		// Del 请求
		t.handleMetaDel(msg, asUid, asChan, authLevel)
	}
}

// replyGetDesc 是对 Topic 上 get.desc 请求的响应，仅作为 {meta} 数据包发送给 Session
func (t *Topic) replyGetDesc(sess *Session, asUid types.Uid, _ bool, opts *MsgGetOpts, msg *ClientComMessage) error {
	now := types.TimeNow()
	id := msg.Id

	if opts != nil && (opts.User != "" || opts.Limit != 0) {
		sess.queueOut(ErrMalformedReply(msg, now))
		return errors.New("invalid GetDesc query")
	}

	// 检查是否用户请求了修改后的数据
	ifUpdated := opts == nil || opts.IfModifiedSince == nil || opts.IfModifiedSince.Before(t.updated)

	desc := &MsgTopicDesc{}
	if opts == nil || opts.IfModifiedSince == nil {
		// 仅当用户请求完整信息时发送 CreatedAt（客户端未缓存任何内容）。
		desc.CreatedAt = &t.created
	}
	if !t.updated.IsZero() {
		desc.UpdatedAt = &t.updated
	}

	pud, full := t.perUser[asUid]

	full = full || t.cat == types.TopicCatMe

	if t.cat == types.TopicCatGrp {
		desc.IsChan = t.isChan
		desc.SubCnt = t.subCnt
		logs.Info.Println("replyGetDesc: grp topic", t.name, "subs", t.subCnt)
	}

	if ifUpdated {
		if t.public != nil || t.trusted != nil {
			// 不是 p2p Topic。
			desc.Public = t.public
			desc.Trusted = t.trusted
		} else if full && t.cat == types.TopicCatP2P {
			// 优先获取对端用户最新资料，解决个人中心更新 desc 后 P2P 缓存不同步的问题
			p2pOther := t.p2pOtherUser(asUid)
			if suser, err := store.Users.Get(p2pOther); err == nil && suser != nil {
				desc.Public = suser.Public
				desc.Trusted = suser.Trusted
				pud.public = suser.Public
				pud.trusted = suser.Trusted
			} else {
				desc.Public = pud.public
				desc.Trusted = pud.trusted
			}
		}
	}

	// 请求可能来自订阅者（full == true）或陌生人。
	// 为订阅者提供比陌生人/Channel 读者更完整的描述。
	if full {
		if t.cat == types.TopicCatP2P {
			// 对于 p2p Topic，默认访问模式没有意义：只有参与者能访问 Topic。
			// 不汇报它。
		} else if t.cat == types.TopicCatMe || (pud.modeGiven & pud.modeWant).IsSharer() {
			desc.DefaultAcs = &MsgDefaultAcsMode{
				Auth: t.accessAuth.String(),
				Anon: t.accessAnon.String(),
			}
		}

		desc.Acs = &MsgAccessMode{
			Want:  pud.modeWant.String(),
			Given: pud.modeGiven.String(),
			Mode:  (pud.modeGiven & pud.modeWant).String(),
		}

		if t.cat == types.TopicCatMe && sess.authLvl == auth.LevelRoot {
			// 如果 'me' 在内存中，则用户账户肯定未被挂起。
			desc.State = types.StateOK.String()
		}

		if (pud.modeGiven & pud.modeWant).IsPresencer() {
			switch t.cat {
			case types.TopicCatGrp:
				desc.Online = t.isOnline()
			case types.TopicCatP2P:
				// 若对手完全离线（online == 0）且存在离线时间戳，返回准确的 LastSeen 与 UserAgent
				if pud.online == 0 && pud.lastSeen != nil {
					desc.LastSeen = &MsgLastSeenInfo{
						When:      pud.lastSeen,
						UserAgent: pud.lastUA,
					}
				}
			}
		}

		if ifUpdated {
			desc.Private = pud.private
		}

		// 不要向没有 Read 权限的用户汇报消息 ID。
		if (pud.modeGiven & pud.modeWant).IsReader() {
			desc.SeqId = t.lastID
			if !t.touched.IsZero() {
				desc.TouchedAt = &t.touched
			}

			// 确保汇报的值合理：
			// t.delID <= pud.delID; t.readID <= t.recvID <= t.lastID
			desc.DelId = max(pud.delID, t.delID)
			desc.ReadSeqId = pud.readID
			desc.RecvSeqId = max(pud.recvID, pud.readID)
		} else {
			// 发送一些合理的 touched 值。
			desc.TouchedAt = &t.updated
		}
	}

	sess.queueOut(&ServerComMessage{
		Meta: &MsgServerMeta{
			Id:        id,
			Topic:     msg.Original,
			Desc:      desc,
			Timestamp: &now,
		},
	})

	return nil
}

// replySetDesc 更新 Topic 元数据，保存到数据库，作为 {ctrl} 消息回复给调用者，
// 必要时生成 {pres} 更新。
func (t *Topic) replySetDesc(sess *Session, asUid types.Uid, asChan bool,
	authLevel auth.Level, msg *ClientComMessage) error {
	now := types.TimeNow()

	assignAccess := func(upd map[string]any, mode *MsgDefaultAcsMode) error {
		if mode == nil {
			return nil
		}
		if auth, anon, err := parseTopicAccess(mode, types.ModeUnset, types.ModeUnset); err != nil {
			return err
		} else if auth.IsOwner() || anon.IsOwner() {
			return errors.New("default 'owner' access is not permitted")
		} else {
			access := types.DefaultAccess{Auth: t.accessAuth, Anon: t.accessAnon}
			if auth != types.ModeUnset {
				if t.cat == types.TopicCatMe {
					auth &= types.ModeCAuth
					if auth != types.ModeNone {
						// 这是 P2P Topic 的默认访问模式。
						// 它必须是 N 或必须包含 A 权限。
						auth |= types.ModeApprove
					}
				}
				access.Auth = auth
			}
			if anon != types.ModeUnset {
				if t.cat == types.TopicCatMe {
					anon &= globals.typesModeCP2P
					if anon != types.ModeNone {
						anon |= types.ModeApprove
					}
				}
				access.Anon = anon
			}
			if access.Auth != t.accessAuth || access.Anon != t.accessAnon {
				upd["Access"] = access
			}
		}
		return nil
	}

	assignGenericValues := func(upd map[string]any, what string, dst, src any) (changed bool) {
		if dst, changed = mergeInterfaces(dst, src); changed {
			upd[what] = dst
		}
		return
	}

	// DefaultAccess 和/或 Public 已更改
	var sendCommon bool
	// Private 已更改
	var sendPriv bool
	var err error

	// 对主对象（用户或 Topic）的更改。
	core := make(map[string]any)
	// 对订阅的更改。
	sub := make(map[string]any)
	if set := msg.Set; set.Desc != nil {
		if set.Desc.Trusted != nil && authLevel != auth.LevelRoot {
			// 只有 ROOT 能更改 Trusted。
			sess.queueOut(ErrPermissionDeniedReply(msg, now))
			return errors.New("attempt to change Trusted by non-root")
		}

		switch t.cat {
		case types.TopicCatMe:
			// 更新当前用户
			err = assignAccess(core, set.Desc.DefaultAcs)
			sendCommon = assignGenericValues(core, "Public", t.public, set.Desc.Public)
			sendCommon = assignGenericValues(core, "Trusted", t.trusted, set.Desc.Trusted) || sendCommon
		case types.TopicCatFnd:
			// set.Desc.DefaultAcs 被忽略。
			if set.Desc.Trusted != nil {
				// 'fnd' 不支持 Trusted。
				sess.queueOut(ErrPermissionDeniedReply(msg, now))
				return errors.New("attempt to assign Trusted in fnd topic")
			}
			// fnd.Public 更改时不发送在线状态。
			assignGenericValues(core, "Public", t.fndGetPublic(sess), set.Desc.Public)
		case types.TopicCatP2P:
			// 拒绝对 P2P Topic 的直接更改。
			if set.Desc.Public != nil || set.Desc.Trusted != nil || set.Desc.DefaultAcs != nil {
				sess.queueOut(ErrPermissionDeniedReply(msg, now))
				return errors.New("incorrect attempt to change metadata of a p2p topic")
			}
		case types.TopicCatGrp:
			// 更新群组 Topic
			if t.owner == asUid {
				err = assignAccess(core, set.Desc.DefaultAcs)
				sendCommon = assignGenericValues(core, "Public", t.public, set.Desc.Public)
				sendCommon = assignGenericValues(core, "Trusted", t.trusted, set.Desc.Trusted) || sendCommon
			} else if set.Desc.DefaultAcs != nil || set.Desc.Public != nil || set.Desc.Trusted != nil {
				// 这是来自非所有者的请求
				sess.queueOut(ErrPermissionDeniedReply(msg, now))
				return errors.New("attempt to change public or permissions by non-owner")
			}
		}

		if err != nil {
			sess.queueOut(ErrMalformedReply(msg, now))
			return err
		}

		sendPriv = assignGenericValues(sub, "Private", t.perUser[asUid].private, set.Desc.Private)
	}

	if len(core)+len(sub) == 0 {
		sess.queueOut(InfoNotModifiedReply(msg, now))
		return errors.New("{set} generated no update to DB")
	}

	if len(core) > 0 {
		core["UpdatedAt"] = now
		switch t.cat {
		case types.TopicCatMe:
			err = store.Users.Update(asUid, core)
		case types.TopicCatFnd:
			// 唯一要存储在 Topic 中的值是 Public，而 fnd 的 Public 根据规范不保存。
		default:
			err = store.Topics.Update(t.name, core)
		}
	}
	if err == nil && len(sub) > 0 {
		tname := t.name
		if asChan {
			tname = types.GrpToChn(tname)
		}
		err = store.Subs.Update(tname, asUid, sub)
	}

	if err != nil {
		sess.queueOut(ErrUnknownReply(msg, now))
		return err
	}

	if len(core) > 0 && msg.Extra != nil && len(msg.Extra.Attachments) > 0 {
		if err := store.Files.LinkAttachments(t.name, types.ZeroUid, msg.Extra.Attachments); err != nil {
			logs.Warn.Printf("topic[%s] failed to link avatar attachment: %v", t.name, err)
			// 这不是关键错误，继续执行。
		}
	}

	// 更新 Topic 对象中缓存的值
	switch t.cat {
	case types.TopicCatMe, types.TopicCatGrp:
		if tmp, ok := core["Access"]; ok {
			access := tmp.(types.DefaultAccess)
			t.accessAuth = access.Auth
			t.accessAnon = access.Anon
		}
		if public, ok := core["Public"]; ok {
			t.public = public
		}
		if trusted, ok := core["Trusted"]; ok {
			t.trusted = trusted
		}
	case types.TopicCatFnd:
		// 分配每个 Session 的 fnd.Public。
		t.fndSetPublic(sess, core["Public"])
	}

	pud := t.perUser[asUid]
	mode := pud.modeGiven & pud.modeWant
	if private, ok := sub["Private"]; ok {
		pud.private = private
		t.perUser[asUid] = pud
	}

	if sendCommon || sendPriv {
			// t.public/t.trusted、t.accessAuth/Anon 已更改，发布公告
		if sendCommon {
			if t.cat == types.TopicCatMe {
				t.presUsersOfInterest("upd", "")
			} else {
				// 通知 'me' 上的所有订阅者，除了进行更改的用户和被屏蔽的用户。
				// 进行更改的用户将单独收到通知（见下文）。
				filter := &presFilters{excludeUser: asUid.UserId(), filterIn: types.ModeJoin}
				t.presSubsOffline("upd", nilPresParams, filter, filter, sess.sid, false)
			}

			t.updated = now
		}
		// 通知用户的其它 Session。
		t.presSingleUserOffline(asUid, mode, "upd", nilPresParams, sess.sid, false)
	}

	sess.queueOut(NoErrReply(msg, now))

	return nil
}

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
				if and, opt, err := parseSearchQuery(query); err == nil {
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
		// 请求新订阅或修改自己的订阅
		modeChanged, err = t.thisUserSub(sess, pkt, asUid, asChan, set.Sub.Mode, nil)
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

// replyGetData 是对 get.data 请求的响应 - 加载存储消息列表，作为 {data} 发送给 Session
// 响应仅发送给单个 Session 而不是 Topic 中的所有 Session
func (t *Topic) replyGetData(sess *Session, asUid types.Uid, asChan bool, req *MsgGetOpts, msg *ClientComMessage) error {
	now := types.TimeNow()
	toriginal := t.original(asUid)

	if req != nil && (req.IfModifiedSince != nil || req.User != "" || req.Topic != "") {
		sess.queueOut(ErrMalformedReply(msg, now))
		return errors.New("invalid MsgGetOpts query")
	}

	// 检查是否用户有权限读取 Topic 数据
	count := 0
	if userData := t.perUser[asUid]; (userData.modeGiven & userData.modeWant).IsReader() {
		// 从 DB 读取消息
		messages, err := store.Messages.GetAll(t.name, asUid, msgOpts2storeOpts(req))
		if err != nil {
			sess.queueOut(ErrUnknownReply(msg, now))
			return err
		}

		// 将消息列表作为 {data} 推送到客户端。
		if messages != nil {
			count = len(messages)
			if count > 0 {
				outgoingMessages := make([]*ServerComMessage, count)
				for i := range messages {
					mm := &messages[i]
					from := ""
					if !asChan {
						// 不显示 Channel 读者的发送者
						from = types.ParseUid(mm.From).UserId()
					}
					outgoingMessages[i] = &ServerComMessage{
						Data: &MsgServerData{
							Topic:     toriginal,
							Head:      mm.Head,
							SeqId:     mm.SeqId,
							From:      from,
							Timestamp: mm.CreatedAt,
							Content:   mm.Content,
						},
					}
				}
				sess.queueOutBatch(outgoingMessages)
			}
		}
	} else {
		sess.queueOut(ErrPermissionDeniedReply(msg, now))
		return errors.New("attempt to get messages by non-reader")
	}

	// 通知请求者所有数据已提供服务。
	if count == 0 {
		sess.queueOut(NoContentParamsReply(msg, now, map[string]any{"what": "data"}))
	} else {
		sess.queueOut(NoErrDeliveredParams(msg.Id, msg.Original, now,
			map[string]any{"what": "data", "count": count}))
	}

	return nil
}

// replyGetTags 返回 Topic 的标签 - 用于发现的令牌。
func (t *Topic) replyGetTags(sess *Session, asUid types.Uid, msg *ClientComMessage) error {
	now := types.TimeNow()

	if t.cat == types.TopicCatFnd {
		// Fnd：检查别名可用性。

		// 仅检查公共（Session）数据。
		if tag := t.fndGetPublic(sess); tag != "" {
			var found string
			tag, subs, err := pluginFind(asUid, tag)
			if err == nil {
				if subs == nil {
					if prefix, _ := validateTag(tag); prefix != "" {
						// 仅当发送了完全限定标签时才检查。否则忽略请求。
						found, err = store.Users.FindOne(tag)
					}
				} else {
					// 插件返回了 Topic 列表。发送第一个。
					found = subs[0].Topic
				}
			}

			if err != nil {
				sess.queueOut(decodeStoreErrorExplicitTs(err, msg.Id, msg.Original, now, msg.Timestamp, nil))
				return err
			}

			if found != "" {
				sess.queueOut(&ServerComMessage{
					Meta: &MsgServerMeta{
						Id:        msg.Id,
						Topic:     msg.Original,
						Timestamp: &now,
						Tags:      []string{found},
					},
				})
				return nil
			}
		}

		// 通知请求者没有标签。
		sess.queueOut(NoContentParamsReply(msg, now, map[string]string{"what": "tags"}))
		return nil
	}

	if t.cat != types.TopicCatMe && t.cat != types.TopicCatGrp {
		sess.queueOut(ErrOperationNotAllowedReply(msg, now))
		return errors.New("invalid topic category for getting tags")
	}
	if t.cat == types.TopicCatGrp && t.owner != asUid {
		sess.queueOut(ErrPermissionDeniedReply(msg, now))
		return errors.New("request for tags from non-owner")
	}

	if len(t.tags) > 0 {
		sess.queueOut(&ServerComMessage{
			Meta: &MsgServerMeta{
				Id:        msg.Id,
				Topic:     t.original(asUid),
				Timestamp: &now,
				Tags:      t.tags,
			},
		})
		return nil
	}

	// 通知请求者没有标签。
	sess.queueOut(NoContentParamsReply(msg, now, map[string]string{"what": "tags"}))

	return nil
}

// replySetTags 更新 Topic 的标签 - 用于发现的令牌。
func (t *Topic) replySetTags(sess *Session, asUid types.Uid, msg *ClientComMessage) error {
	now := types.TimeNow()

	if t.cat != types.TopicCatMe && t.cat != types.TopicCatGrp {
		sess.queueOut(ErrOperationNotAllowedReply(msg, now))
		return errors.New("invalid topic category to assign tags")
	}

	if t.cat == types.TopicCatGrp && t.owner != asUid {
		sess.queueOut(ErrPermissionDeniedReply(msg, now))
		return errors.New("tags update by non-owner")
	}

	tags := normalizeTags(msg.Set.Tags, globals.maxTagCount)
	if len(tags) == 0 {
		sess.queueOut(InfoNotModifiedReply(msg, now))
		return nil
	}

	if !restrictedTagsEqual(t.tags, tags, globals.immutableTagNS) {
		sess.queueOut(ErrPermissionDeniedReply(msg, now))
		return errors.New("attempt to mutate restricted tags")
	}

	if hasDuplicateNamespaceTags(tags, globals.aliasTagNS) {
		sess.queueOut(ErrMalformedReply(msg, now))
		return errors.New("duplicate unique tags")
	}

	added, removed, _ := stringSliceDelta(t.tags, tags)

	if t.cat == types.TopicCatMe && len(added) > 0 {
		// 用户标签必须全部带前缀。用户无法通过通用标签被找到。
		var prefixed []string
		for _, tag := range added {
			if prefix, _ := validateTag(tag); prefix != "" {
				prefixed = append(prefixed, prefix)
			}
		}
		added = prefixed
	}

	if len(added) == 0 && len(removed) == 0 {
		sess.queueOut(InfoNotModifiedReply(msg, now))
		return nil
	}

	// 移除无前缀的标签
	if unique := filterTags(added, map[string]bool{globals.aliasTagNS: true}); len(unique) > 0 {
		// 检查全局唯一性。
		// 它不在事务内，所以可能会发生竞争。
		for _, tag := range unique {
			result, err := store.Users.FindOne(tag)

			if err != nil {
				sess.queueOut(ErrUnknownReply(msg, now))
				return err
			}

			if result != "" {
				sess.queueOut(ErrMalformedReply(msg, now))
				return errors.New("globally duplicate unique tags")
			}
		}
	}

	update := map[string]any{"Tags": tags, "UpdatedAt": now}
	var err error
	switch t.cat {
	case types.TopicCatMe:
		err = store.Users.Update(asUid, update)
	case types.TopicCatGrp:
		err = store.Topics.Update(t.name, update)
	}

	if err != nil {
		sess.queueOut(ErrUnknownReply(msg, now))
		return err
	}

	t.tags = tags
	t.presSubsOnline("tags", "", nilPresParams, &presFilters{singleUser: asUid.UserId()}, sess.sid)

	params := make(map[string]any)
	if len(added) > 0 {
		params["added"] = len(added)
	}
	if len(removed) > 0 {
		params["removed"] = len(removed)
	}

	sess.queueOut(NoErrParamsReply(msg, now, params))
	return nil
}

// replyGetCreds 返回用户的凭证，如电子邮件和电话号码。
func (t *Topic) replyGetCreds(sess *Session, asUid types.Uid, msg *ClientComMessage) error {
	now := types.TimeNow()
	id := msg.Id

	if t.cat != types.TopicCatMe {
		sess.queueOut(ErrOperationNotAllowedReply(msg, now))
		return errors.New("invalid topic category for getting credentials")
	}

	screds, err := store.Users.GetAllCreds(asUid, "", false)
	if err != nil {
		sess.queueOut(decodeStoreErrorExplicitTs(err, id, msg.Original, now, msg.Timestamp, nil))
		return err
	}

	if len(screds) > 0 {
		creds := make([]*MsgCredServer, len(screds))
		for i, sc := range screds {
			creds[i] = &MsgCredServer{Method: sc.Method, Value: sc.Value, Done: sc.Done}
		}
		sess.queueOut(&ServerComMessage{
			Meta: &MsgServerMeta{
				Id:        id,
				Topic:     t.original(asUid),
				Timestamp: &now,
				Cred:      creds,
			},
		})
		return nil
	}

	// 通知请求者没有凭证。
	sess.queueOut(NoContentParamsReply(msg, now, map[string]string{"what": "creds"}))

	return nil
}

// replySetCred 添加或验证用户凭证，如电子邮件和电话号码。
func (t *Topic) replySetCred(sess *Session, asUid types.Uid, authLevel auth.Level, msg *ClientComMessage) error {
	now := types.TimeNow()
	set := msg.Set

	if t.cat != types.TopicCatMe {
		sess.queueOut(ErrOperationNotAllowedReply(msg, now))
		return errors.New("invalid topic category for updating credentials")
	}

	var err error
	var tags []string
	creds := []MsgCredClient{*set.Cred}
	if set.Cred.Response != "" {
		// 凭证正在被验证。如果响应无效则返回错误。
		_, tags, err = validatedCreds(asUid, authLevel, creds, true)
	} else {
		// 凭证正在被添加或更新。
		tmpToken, _, _ := store.Store.GetLogicalAuthHandler("token").GenSecret(&auth.Rec{
			Uid:       asUid,
			AuthLevel: auth.LevelNone,
			Lifetime:  auth.Duration(time.Hour * 24),
			Features:  auth.FeatureNoLogin,
		})
		_, tags, err = addCreds(asUid, creds, nil, sess.lang, tmpToken)
	}

	if tags != nil {
		t.tags = tags
		t.presSubsOnline("tags", "", nilPresParams, nilPresFilters, "")
	}

	sess.queueOut(decodeStoreErrorExplicitTs(err, set.Id, t.original(asUid), now, msg.Timestamp, nil))

	return err
}

// replyGetAux 返回 Topic 的辅助键值对集合。
func (t *Topic) replyGetAux(sess *Session, asUid types.Uid, msg *ClientComMessage) error {
	now := types.TimeNow()

	if t.cat != types.TopicCatP2P && t.cat != types.TopicCatGrp && t.cat != types.TopicCatSlf {
		sess.queueOut(ErrOperationNotAllowedReply(msg, now))
		return errors.New("invalid topic category to query aux")
	}

	if len(t.aux) > 0 {
		sess.queueOut(&ServerComMessage{
			Meta: &MsgServerMeta{
				Id:        msg.Id,
				Topic:     t.original(asUid),
				Timestamp: &now,
				Aux:       t.aux,
			},
		})
		return nil
	}

	// 通知请求者没有标签。
	sess.queueOut(NoContentParamsReply(msg, now, map[string]string{"what": "aux"}))

	return nil
}

// replyGetAux 返回 Topic 的辅助键值对集合。
func (t *Topic) replySetAux(sess *Session, asUid types.Uid, msg *ClientComMessage) error {
	now := types.TimeNow()

	if t.cat != types.TopicCatP2P && t.cat != types.TopicCatGrp && t.cat != types.TopicCatSlf {
		sess.queueOut(ErrOperationNotAllowedReply(msg, now))
		return errors.New("invalid topic category to assign aux")
	}

	if userData := t.perUser[asUid]; !(userData.modeGiven & userData.modeWant).IsAdmin() {
		sess.queueOut(ErrPermissionDeniedReply(msg, now))
		return errors.New("aux update by non-admin")
	}

	if aux, changed := mergeMaps(copyMap(t.aux), msg.Set.Aux); changed {
		err := store.Topics.Update(t.name, map[string]any{"Aux": aux, "UpdatedAt": now})
		if err == nil {
			t.aux = aux
			t.presSubsOnline("aux", "", nilPresParams, nilPresFilters, sess.sid)
		}
		sess.queueOut(decodeStoreErrorExplicitTs(err, msg.Set.Id, t.original(asUid), now, msg.Timestamp, nil))
		return err
	}

	sess.queueOut(InfoNotModifiedReply(msg, now))
	return nil
}

// replyGetDel 是对 get[what=del] 请求的响应：加载已删除消息 ID 列表，作为 {meta} 发送给
// Session
// 响应仅发送给单个 Session 而不是 Topic 中的所有 Session
func (t *Topic) replyGetDel(sess *Session, asUid types.Uid, req *MsgGetOpts, msg *ClientComMessage) error {
	now := types.TimeNow()
	toriginal := t.original(asUid)

	id := msg.Id
	incomingReqTs := msg.Timestamp

	if req != nil && (req.IfModifiedSince != nil || req.User != "" || req.Topic != "") {
		sess.queueOut(ErrMalformedReply(msg, now))
		return errors.New("invalid MsgGetOpts query")
	}

	// 检查是否用户有权限读取 Topic 数据且请求有效。
	if userData := t.perUser[asUid]; (userData.modeGiven & userData.modeWant).IsReader() {
		ranges, delID, err := store.Messages.GetDeleted(t.name, asUid, msgOpts2storeOpts(req))
		if err != nil {
			sess.queueOut(ErrUnknownReply(msg, now))
			return err
		}

		if len(ranges) > 0 {
			sess.queueOut(&ServerComMessage{
				Meta: &MsgServerMeta{
					Id:    id,
					Topic: toriginal,
					Del: &MsgDelValues{
						DelId:  delID,
						DelSeq: rangeDeserialize(ranges),
					},
					Timestamp: &now,
				},
			})
			return nil
		}
	}

	sess.queueOut(NoContentParams(id, toriginal, now, incomingReqTs, map[string]string{"what": "del"}))

	return nil
}

// replyDelMsg 删除（软删除或硬删除）消息以响应 del.msg 数据包。
func (t *Topic) replyDelMsg(sess *Session, asUid types.Uid, asChan bool, msg *ClientComMessage) error {
	now := types.TimeNow()

	if asChan {
		// 不允许 Channel 读者删除消息。
		sess.queueOut(ErrOperationNotAllowedReply(msg, now))
		return errors.New("channel readers cannot delete messages")
	}

	del := msg.Del

	pud := t.perUser[asUid]
	if !(pud.modeGiven & pud.modeWant).IsDeleter() {
		// 用户必须有 R 权限：如果用户无法读取消息，则
		// 无权删除它们。
		if !(pud.modeGiven & pud.modeWant).IsReader() {
			sess.queueOut(ErrPermissionDeniedReply(msg, now))
			return errors.New("del.msg: permission denied")
		}

		// 用户仅有 R 权限，无法硬删除消息，静默
		// 切换为软删除
		del.Hard = false
	}

	var err error
	var ranges []types.Range
	if len(del.DelSeq) == 0 {
		err = errors.New("del.msg: no IDs to delete")
	} else {
		count := 0
		for _, dq := range del.DelSeq {
			if dq.LowId > t.lastID || dq.LowId < 0 || dq.HiId < 0 ||
				(dq.HiId > 0 && dq.LowId > dq.HiId) ||
				(dq.LowId == 0 && dq.HiId == 0) {
				err = errors.New("del.msg: invalid entry in list")
				break
			}

			if dq.HiId > t.lastID {
				// 范围是包含-排除 [low, hi)，
				// 要删除所有消息 hi 必须是 lastId + 1
				dq.HiId = t.lastID + 1
			} else if dq.LowId == dq.HiId || dq.LowId+1 == dq.HiId {
				dq.HiId = 0
			}

			if dq.HiId == 0 {
				count++
			} else {
				count += dq.HiId - dq.LowId
			}

			ranges = append(ranges, types.Range{Low: dq.LowId, Hi: dq.HiId})
		}

		if err == nil {
			// 按 Low 升序然后按 Hi 降序排序。
			sort.Sort(types.RangeSorter(ranges))
			// 折叠重叠的范围
			ranges = types.RangeSorter(ranges).Normalize()
		}

		if count > defaultMaxDeleteCount && len(ranges) > 1 {
			err = errors.New("del.msg: too many messages to delete")
		}
	}

	if err != nil {
		sess.queueOut(ErrMalformedReply(msg, now))
		return err
	}

	forUser := asUid
	var age time.Duration
	if del.Hard {
		forUser = types.ZeroUid
		age = globals.msgDeleteAge
	}
	if err = store.Messages.DeleteList(t.name, t.delID+1, forUser, age, ranges); err != nil {
		sess.queueOut(ErrUnknownReply(msg, now))
		return err
	}

	// 增加删除事务 ID
	t.delID++
	dr := rangeDeserialize(ranges)
	if del.Hard {
		for uid, pud := range t.perUser {
			pud.delID = t.delID
			t.perUser[uid] = pud

			// 更新所有可能将这些消息作为未读的用户的未读计数器
			if (pud.modeGiven & pud.modeWant).IsReader() {
				// 计算此用户删除了多少未读消息
				unreadDeleted := calculateUnreadInRanges(pud.readID, t.lastID, ranges)
				if unreadDeleted > 0 {
					// 减少未读计数（负值）
					usersUpdateUnread(uid, -unreadDeleted, true)
				}
			}
		}

		// 将更改广播给所有在线和离线的用户，排除进行更改的 Session。
		params := &presParams{delID: t.delID, delSeq: dr, actor: asUid.UserId()}
		filters := &presFilters{filterIn: types.ModeRead}
		t.presSubsOnline("del", params.actor, params, filters, sess.sid)
		t.presSubsOffline("del", params, filters, nilPresFilters, sess.sid, true)
	} else {
		pud := t.perUser[asUid]
		pud.delID = t.delID
		t.perUser[asUid] = pud

		// 通知用户的其它 Session
		t.presPubMessageDelete(asUid, pud.modeGiven&pud.modeWant, t.delID, dr, sess.sid)
	}

	sess.queueOut(NoErrParamsReply(msg, now, map[string]int{"del": t.delID}))

	return nil
}

// 处理删除 Topic 的请求 {del what="Topic"}。
// 1. 如果请求者是所有者，则应在 hub 层处理，记录错误。
// 2. 如果请求者不是所有者，则像 {leave unsub=true} 一样处理。
func (t *Topic) replyDelTopic(sess *Session, asUid types.Uid, msg *ClientComMessage) error {
	if t.owner != asUid {
		return t.replyLeaveUnsub(sess, msg, asUid)
	}

	// 这是一个 bug 指示。
	logs.Err.Println("replyDelTopic called by owner (SHOULD NOT HAPPEN!)")
	return nil
}

// 删除凭证
func (t *Topic) replyDelCred(sess *Session, asUid types.Uid, authLvl auth.Level, msg *ClientComMessage) error {
	now := types.TimeNow()
	incomingReqTs := msg.Timestamp
	del := msg.Del

	if t.cat != types.TopicCatMe {
		sess.queueOut(ErrPermissionDeniedReply(msg, now))
		return errors.New("del.cred: invalid topic category")
	}
	if del.Cred == nil || del.Cred.Method == "" {
		sess.queueOut(ErrMalformedReply(msg, now))
		return errors.New("del.cred: missing method")
	}

	tags, err := deleteCred(asUid, authLvl, del.Cred)
	if tags != nil {
		// 检查是否实际删除了任何内容。
		_, removed, _ := stringSliceDelta(t.tags, tags)
		if len(removed) > 0 {
			t.tags = tags
			t.presSubsOnline("tags", "", nilPresParams, nilPresFilters, "")
		}
	} else if err == nil {
		sess.queueOut(InfoNoActionReply(msg, now))
		return nil
	}
	sess.queueOut(decodeStoreErrorExplicitTs(err, del.Id, del.Topic, now, incomingReqTs, nil))
	return err
}

// 删除订阅。
func (t *Topic) replyDelSub(sess *Session, asUid types.Uid, msg *ClientComMessage) error {
	now := types.TimeNow()
	del := msg.Del

	asChan, err := t.verifyChannelAccess(msg.Original)
	if err != nil {
		// 用户不应能将非 Channel Topic 作为 Channel 寻址。
		sess.queueOut(ErrNotFoundReply(msg, now))
		return types.ErrNotFound
	}
	if asChan {
		// 不允许 Channel 读者删除自订阅。使用 leave-unsub 或 del-Topic。
		sess.queueOut(ErrPermissionDeniedReply(msg, now))
		return errors.New("channel access denied: cannot delete subscription")
	}

	// 获取受影响用户的 ID
	uid := types.ParseUserId(del.User)

	pud := t.perUser[asUid]
	if !(pud.modeGiven & pud.modeWant).IsAdmin() {
		err = errors.New("del.sub: permission denied")
	} else if uid.IsZero() || uid == asUid {
		// 不能删除自订阅。用户 [leave unsub] 或 [delete Topic]
		err = errors.New("del.sub: cannot delete self-subscription")
	} else if t.cat == types.TopicCatP2P {
		// 不要尝试删除另一个 P2P 用户
		err = errors.New("del.sub: cannot apply to a P2P topic")
	}

	if err != nil {
		sess.queueOut(ErrPermissionDeniedReply(msg, now))
		return err
	}

	pud, ok := t.perUser[uid]
	if !ok {
		sess.queueOut(InfoNoActionReply(msg, now))
		return errors.New("del.sub: user not found")
	}

	// 检查是否被驱逐的用户是所有者。
	if (pud.modeGiven & pud.modeWant).IsOwner() {
		err = errors.New("del.sub: cannot evict topic owner")
	} else if !pud.modeWant.IsJoiner() {
		// 如果用户已禁止 Topic，则不应删除订阅。否则用户可能被重新邀请，
		// 这就违背了禁止的目的。
		err = errors.New("del.sub: cannot delete banned subscription")
	}

	if err != nil {
		sess.queueOut(ErrPermissionDeniedReply(msg, now))
		return err
	}

	// 从数据库中删除用户的订阅
	if err := store.Subs.Delete(t.name, uid); err != nil {
		if err == types.ErrNotFound {
			sess.queueOut(InfoNoActionReply(msg, now))
		} else {
			sess.queueOut(ErrUnknownReply(msg, now))
			return err
		}
	} else {
		sess.queueOut(NoErrReply(msg, now))
	}

	// 更新缓存的未读计数：负值
	if (pud.modeWant & pud.modeGiven).IsReader() {
		usersUpdateUnread(uid, pud.readID-t.lastID, true)
	}

	// ModeUnset 表示已删除的订阅，与 ModeNone（无访问权限）相反。
	t.notifySubChange(uid, asUid, false,
		pud.modeWant, pud.modeGiven, types.ModeUnset, types.ModeUnset, sess.sid)

	t.evictUser(uid, true, "")

	// 通知插件。
	pluginSubscription(&types.Subscription{Topic: t.name, User: uid.String()}, plgActDel)

	// 如果所有 P2P 用户都被删除，则挂起 Topic 以让其关闭。
	if t.cat == types.TopicCatP2P && t.subsCount() == 0 {
		t.markPaused(true)
		globals.hub.unreg <- &topicUnreg{del: true, sess: nil, rcptTo: t.name, pkt: nil}
	}

	return nil
}
