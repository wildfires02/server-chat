package main

import (
	"time"

	"chat/server/logs"
	"chat/server/store/types"
)

// passesPresenceFilters 根据提供的 uid 对应的用户想要 (want) 和给予 (given) 权限，为 msg 应用在线状态过滤器
func (t *Topic) passesPresenceFilters(pres *MsgServerPres, uid types.Uid) bool {
	modeWant, modeGiven := t.getPerUserAcs(uid)
	// "gone" 和 "acs" 通知即使 Topic 静音也会发送
	return ((modeGiven & modeWant).IsPresencer() || pres.What == "gone" || pres.What == "acs") &&
		(pres.FilterIn == 0 || int(modeGiven&modeWant)&pres.FilterIn != 0) &&
		(pres.FilterOut == 0 || int(modeGiven&modeWant)&pres.FilterOut == 0)
}

// userIsReader 如果指定 uid 的用户可以读取给定的 Topic 则返回 true
func (t *Topic) userIsReader(uid types.Uid) bool {
	modeWant, modeGiven := t.getPerUserAcs(uid)
	return (modeGiven & modeWant).IsReader()
}

// sessToForeground 更新 perUser 在线状态计数，并为提供的 Session 触发到期的延迟通知
func (t *Topic) sessToForeground(sess *Session) {
	s := sess
	if s.multi != nil {
		s = s.multi
	}

	if pssd, ok := t.sessions[s]; ok && !pssd.isChanSub {
		uid := pssd.uid
		if s.isMultiplex() {
			// 如果 's' 是多路复用 Session，则 sess 是代理并包含正确的 UID
			// 将 UID 添加到在线用户列表
			uid = sess.uid
			pssd.muids = append(pssd.muids, uid)
		}
		// 标记用户在线
		pud := t.perUser[uid]
		pud.online++
		t.perUser[uid] = pud

		t.sendSubNotifications(uid, sess.sid, sess.userAgent)
	}
}

// sendImmediateSubNotifications 响应订阅请求发送即时在线状态通知，向 P2P 对方发送推送通知
func (t *Topic) sendImmediateSubNotifications(asUid types.Uid, acs *MsgAccessMode, sreg *ClientComMessage, now time.Time) {
	modeWant, _ := types.ParseAcs([]byte(acs.Want))
	modeGiven, _ := types.ParseAcs([]byte(acs.Given))
	mode := modeWant & modeGiven

	asChan := t.isChan && types.IsChannel(sreg.Original)

	if t.cat == types.TopicCatP2P {
		uid2 := t.p2pOtherUser(asUid)
		pud2 := t.perUser[uid2]
		mode2 := pud2.modeGiven & pud2.modeWant
		if pud2.deleted {
			mode2 = types.ModeInvalid
		}

		// 通知另一位用户 Topic 刚刚创建
		if sreg.Sub.Created {
			t.presSingleUserOffline(uid2, mode2, "acs", &presParams{
				dWant:  pud2.modeWant.String(),
				dGiven: pud2.modeGiven.String(),
				actor:  asUid.UserId(),
			}, "", false)
		}

		if sreg.Sub.Newsub {
			// 通知当前用户的 'me' Topic 接受来自 user2 的通知
			t.presSingleUserOffline(asUid, mode, "?none+en", nilPresParams, "", false)

			// 发起与对方用户的在线状态交换
			status := "?unkn"
			if mode2.IsPresencer() {
				// 如果 user2 应该接收通知，开启它
				status += "+en"
			}
			t.presSingleUserOffline(uid2, mode2, status, nilPresParams, "", false)

			// 同时也向对方用户发送推送通知
			sendPush(t.pushForP2PSub(asUid, uid2, pud2.modeWant, pud2.modeGiven, now))
		}
	} else if t.cat == types.TopicCatGrp && !asChan && sreg.Sub.Newsub {
		// 对于新的群组订阅，通知群组内的其它成员
		sendPush(t.pushForGroupSub(asUid, now))
	}

	// newsub 仅在 p2p 和 group Topic 为 true，无需显示检查 Topic 类别
	if sreg.Sub.Newsub {
		// 通知创建者的其它 Session 订阅（或整个 Topic）已创建
		t.presSingleUserOffline(asUid, mode, "acs",
			&presParams{
				dWant:  acs.Want,
				dGiven: acs.Given,
				actor:  asUid.UserId(),
			},
			sreg.sess.sid, false)

		if asChan {
			t.channelSubUnsub(asUid, true)
		}
	}
}

// sendSubNotifications 响应订阅请求发送即时或延迟的在线状态通知（Channel 不使用）
func (t *Topic) sendSubNotifications(asUid types.Uid, sid, userAgent string) {
	switch t.cat {
	case types.TopicCatMe:
		// 通知用户的联系人该用户当前已上线
		if !t.isLoaded() {
			t.markLoaded()
			if err := t.loadContacts(asUid); err != nil {
				logs.Err.Println("topic: 加载联系人失败", t.name, err.Error())
			}
			// 用户上线：通知关注的用户
			t.presUsersOfInterest("on", userAgent)
		}

	case types.TopicCatGrp:
		pud := t.perUser[asUid]
		if pud.isChan {
			// 不向 Channel 读者发送通知
			return
		}

		// 开启新群组 Topic 的通知
		if !t.isLoaded() {
			t.markLoaded()
			status := "on"
			if (pud.modeGiven & pud.modeWant).IsPresencer() {
				status += "+en"
			}

			// 通知 Topic 订阅者 Topic 当前上线
			t.presSubsOffline(status, nilPresParams, nilPresFilters, nilPresFilters, "", false)
		} else if pud.online == 1 {
			// 如果这是用户在 Topic 中的首个 Session，通知其它在线群组成员用户已上线
			t.presSubsOnline("on", asUid.UserId(), nilPresParams,
				&presFilters{filterIn: types.ModeRead}, sid)
		}
	}
}

// handlePresence 向 Topic 中的接收者扇出广播 {pres} 消息
func (t *Topic) handlePresence(msg *ServerComMessage) {
	what := t.procPresReq(msg.Pres.Src, msg.Pres.What, msg.Pres.WantReply)
	if t.xoriginal != msg.Pres.Topic || what == "" {
		// 这仅仅是状态查询请求，不向 Session 转发
		return
	}

	msg.Pres.What = what

	t.broadcastToSessions(msg)
}

// notifySubChange 用户对 Topic 的订阅状态发生变化时发送在线/权限变更通知：
// 1. 新订阅
// 2. 取消/删除订阅
// 3. 权限变更
func (t *Topic) notifySubChange(uid, actor types.Uid, isChan bool,
	oldWant, oldGiven, newWant, newGiven types.AccessMode, skip string) {

	unsub := newWant == types.ModeUnset || newGiven == types.ModeUnset

	target := uid.UserId()

	dWant := types.ModeNone.String()
	if newWant.IsDefined() {
		if oldWant.IsDefined() && !oldWant.IsZero() {
			dWant = oldWant.Delta(newWant)
		} else {
			dWant = newWant.String()
		}
	}

	dGiven := types.ModeNone.String()
	if newGiven.IsDefined() {
		if oldGiven.IsDefined() && !oldGiven.IsZero() {
			dGiven = oldGiven.Delta(newGiven)
		} else {
			dGiven = newGiven.String()
		}
	}
	params := &presParams{
		target: target,
		actor:  actor.UserId(),
		dWant:  dWant,
		dGiven: dGiven,
	}

	filterSharers := &presFilters{
		filterIn:    types.ModeCSharer,
		excludeUser: target,
	}

	// 向在线于 Topic 的管理员宣布权限变更，排除目标用户与操作者 Session
	t.presSubsOnline("acs", target, params, filterSharers, skip)

	// 如果是新订阅或用户请求的权限超出已授予权限，在 'me' 上向管理员广播以待审批
	if newWant.BetterThan(newGiven) || oldWant == types.ModeNone {
		t.presSubsOffline("acs", params, filterSharers, filterSharers, skip, true)
	}

	// 处理静音/取消静音
	if unsub {
		// 订阅已删除
		if t.cat == types.TopicCatP2P {
			uid2 := t.p2pOtherUser(uid)
			// 移除 user1 对 user2 的订阅，并通知 user1 的其它 Session 他已离线 (gone)
			t.presSingleUserOffline(uid, newWant&newGiven, "gone", nilPresParams, skip, false)
			// 告知 user2 用户 1 已离线
			presSingleUserOfflineOffline(uid2, target, "off", nilPresParams, "")
		} else if t.cat == types.TopicCatGrp && !isChan {
			// 通知所有管理员/共享者用户当前已离线
			t.presSubsOnline("off", uid.UserId(), nilPresParams, filterSharers, skip)
			// 通知目标用户订阅已移除 (gone)
			presSingleUserOfflineOffline(uid, t.name, "gone", nilPresParams, skip)
		}
	} else {
		// 订阅被修改
		if !(newWant & newGiven).IsPresencer() && (oldWant & oldGiven).IsPresencer() {
			// 订阅刚被静音
			var source string
			if t.cat == types.TopicCatP2P {
				source = t.p2pOtherUser(uid).UserId()
			} else if t.cat == types.TopicCatGrp && !isChan {
				source = t.name
			}
			if source != "" {
				presSingleUserOfflineOffline(uid, source, "off+dis", nilPresParams, "")
			}

		} else if (newWant & newGiven).IsPresencer() && !(oldWant & oldGiven).IsPresencer() {
			// 订阅取消静音
			if t.cat == types.TopicCatGrp && !isChan {
				t.presSingleUserOffline(uid, newWant&newGiven, "?unkn+en", nilPresParams, "", false)
			} else if t.cat == types.TopicCatMe {
				t.presUsersOfInterest("on+en", t.userAgent)
			}
		}

		// 通知目标用户权限已变更
		t.presSubsOnlineDirect("acs", params, &presFilters{singleUser: target}, skip)
		t.presSingleUserOffline(uid, newWant&newGiven, "acs", params, skip, true)
	}
}
