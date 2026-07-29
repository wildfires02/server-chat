package server

import (
	"errors"

	"chat/server/store"
	"chat/server/store/types"
)

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
