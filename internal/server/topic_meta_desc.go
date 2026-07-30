package server

import (
	"errors"

	"chat/server/auth"
	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
)

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
			Role: topicRoleFromAccess(pud.modeGiven&pud.modeWant,
				t.isChan, pud.isChan),
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
		if t.isOfficialTopic() && t.cat == types.TopicCatGrp &&
			(set.Desc.Public != nil || set.Desc.Trusted != nil || set.Desc.DefaultAcs != nil) {
			sess.queueOut(ErrPermissionDeniedReply(msg, now))
			return errors.New("官方频道资料和访问策略只能通过平台管理接口修改")
		}
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
		// t.public/t.trusted, t.accessAuth/Anon have changed, announce
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
