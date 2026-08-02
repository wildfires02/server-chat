package server

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
)

const businessMessageMaxTextLength = 2000

var businessP2PLocks [256]sync.Mutex

type adminBusinessMessageInput struct {
	Provider         string `json:"provider"`
	ActorExternalID  string `json:"actor_external_id"`
	TargetExternalID string `json:"target_external_id"`
	Action           string `json:"action"`
	Text             string `json:"text"`
	ClientID         string `json:"client_id"`
}

// enqueueBusinessMessage 接收业务服务已经持久化的投递任务，并写入 IM 的持久化调度队列。
// 浏览器不能直接调用此入口，权限仍由 server-chat 在入队前独立复核。
func (handler *adminHTTPHandler) enqueueBusinessMessage(
	wrt http.ResponseWriter,
	req *http.Request,
	requestID string,
) {
	var input adminBusinessMessageInput
	if !handler.decode(wrt, req, &input, requestID) {
		return
	}
	input.Provider = strings.ToLower(strings.TrimSpace(input.Provider))
	input.ActorExternalID = strings.TrimSpace(input.ActorExternalID)
	input.TargetExternalID = strings.TrimSpace(input.TargetExternalID)
	input.Action = strings.TrimSpace(input.Action)
	input.Text = strings.TrimSpace(input.Text)
	input.ClientID = strings.TrimSpace(input.ClientID)
	if !externalIdentityProviderPattern.MatchString(input.Provider) ||
		!externalIdentityIDPattern.MatchString(input.ActorExternalID) ||
		!externalIdentityIDPattern.MatchString(input.TargetExternalID) ||
		input.ActorExternalID == input.TargetExternalID ||
		input.Action != "batch_greeting" || input.Text == "" ||
		utf8.RuneCountInString(input.Text) > businessMessageMaxTextLength ||
		len(input.ClientID) < 8 || len(input.ClientID) > 160 {
		handler.writeError(wrt, http.StatusBadRequest, "invalid_business_message", requestID)
		return
	}

	actor, err := businessExternalUID(input.Provider, input.ActorExternalID)
	if err != nil {
		handler.writeError(wrt, http.StatusNotFound, "actor_identity_unavailable", requestID)
		return
	}
	target, err := businessExternalUID(input.Provider, input.TargetExternalID)
	if err != nil {
		handler.writeError(wrt, http.StatusNotFound, "target_identity_unavailable", requestID)
		return
	}
	topic := actor.P2PName(target)
	if topic == "" {
		handler.writeError(wrt, http.StatusBadRequest, "invalid_business_message", requestID)
		return
	}
	if globals.businessPolicy == nil {
		handler.writeError(wrt, http.StatusServiceUnavailable, "business_policy_unavailable", requestID)
		return
	}
	// 入队阶段只复核双方当前是否允许私聊；batch_greeting 的全局额度在真正投递时消费一次。
	if err = globals.businessPolicy.authorizeUIDs(actor, target, "message", topic); err != nil {
		handler.writeError(wrt, http.StatusForbidden, "business_message_forbidden", requestID)
		return
	}
	if err = ensureBusinessP2P(actor, target); err != nil {
		handler.writeBusinessMessageStoreError(wrt, err, requestID)
		return
	}

	if delivered, lookupErr := store.Messages.GetByClientId(topic, actor, input.ClientID); lookupErr != nil {
		handler.writeBusinessMessageStoreError(wrt, lookupErr, requestID)
		return
	} else if delivered != nil {
		handler.writeData(wrt, http.StatusOK, map[string]any{
			"accepted": true, "duplicate": true, "delivered": true,
			"topic": topic, "sequence": delivered.SeqId, "client_id": input.ClientID,
		}, requestID)
		return
	}
	if pending, lookupErr := store.Messages.GetScheduledByClientId(topic, actor, input.ClientID); lookupErr != nil {
		handler.writeBusinessMessageStoreError(wrt, lookupErr, requestID)
		return
	} else if pending != nil {
		handler.writeData(wrt, http.StatusOK, map[string]any{
			"accepted": true, "duplicate": true, "scheduled_id": pending.Id,
			"topic": topic, "client_id": input.ClientID,
		}, requestID)
		return
	}

	scheduled := &types.ScheduledMessage{
		ObjHeader: types.ObjHeader{CreatedAt: types.TimeNow()},
		Topic:     topic, From: actor.String(), ClientId: input.ClientID,
		PublishAt: types.TimeNow().Add(100 * time.Millisecond),
		Head: types.KVMap{
			"mime": "text/plain", headMessageKind: "text",
			"x-business-action": input.Action,
		},
		Content: input.Text,
	}
	if err = store.Messages.Schedule(scheduled); err != nil {
		// 多个任务消费者并发重试同一个幂等键时，以已经存在的记录为成功结果。
		if pending, lookupErr := store.Messages.GetScheduledByClientId(
			topic, actor, input.ClientID,
		); lookupErr == nil && pending != nil {
			handler.writeData(wrt, http.StatusOK, map[string]any{
				"accepted": true, "duplicate": true, "scheduled_id": pending.Id,
				"topic": topic, "client_id": input.ClientID,
			}, requestID)
			return
		}
		handler.writeBusinessMessageStoreError(wrt, err, requestID)
		return
	}
	handler.writeData(wrt, http.StatusAccepted, map[string]any{
		"accepted": true, "scheduled_id": scheduled.Id,
		"topic": topic, "client_id": input.ClientID,
	}, requestID)
}

func (handler *adminHTTPHandler) writeBusinessMessageStoreError(
	wrt http.ResponseWriter,
	err error,
	requestID string,
) {
	switch {
	case errors.Is(err, types.ErrUserNotFound), errors.Is(err, types.ErrNotFound):
		handler.writeError(wrt, http.StatusNotFound, "business_message_target_unavailable", requestID)
	case errors.Is(err, types.ErrPermissionDenied), errors.Is(err, types.ErrPolicy):
		handler.writeError(wrt, http.StatusForbidden, "business_message_forbidden", requestID)
	case errors.Is(err, types.ErrMalformed):
		handler.writeError(wrt, http.StatusBadRequest, "invalid_business_message", requestID)
	default:
		logs.Warn.Printf("business message request %s failed: %v", requestID, err)
		handler.writeError(wrt, http.StatusInternalServerError, "business_message_failed", requestID)
	}
}

func businessExternalUID(provider, externalID string) (types.Uid, error) {
	uid, _, _, _, err := store.Users.GetAuthUniqueRecord(
		externalIdentityAuthScheme,
		externalIdentityUnique(provider, externalID),
	)
	if err != nil || uid.IsZero() {
		return types.ZeroUid, types.ErrUserNotFound
	}
	user, err := store.Users.Get(uid)
	if err != nil || user == nil || user.State != types.StateOK {
		return types.ZeroUid, types.ErrUserNotFound
	}
	return uid, nil
}

func ensureBusinessP2P(actor, target types.Uid) error {
	topic := actor.P2PName(target)
	lock := &businessP2PLocks[(uint64(actor)^uint64(target))%uint64(len(businessP2PLocks))]
	lock.Lock()
	defer lock.Unlock()

	stored, err := store.Topics.Get(topic)
	if err != nil {
		return err
	}
	if stored != nil {
		return ensureBusinessP2PSubscriptions(topic, actor, target)
	}
	users, err := store.Users.GetAll(actor, target)
	if err != nil {
		return err
	}
	if len(users) != 2 {
		return types.ErrUserNotFound
	}
	byUID := make(map[types.Uid]*types.User, 2)
	for index := range users {
		user := &users[index]
		byUID[user.Uid()] = user
	}
	actorUser, actorOK := byUID[actor]
	targetUser, targetOK := byUID[target]
	if !actorOK || !targetOK || actorUser.State != types.StateOK || targetUser.State != types.StateOK {
		return types.ErrUserNotFound
	}
	actorGranted := actorUser.Access.Auth&globals.typesModeCP2P | types.ModeApprove
	targetGranted := targetUser.Access.Auth&globals.typesModeCP2P | types.ModeApprove
	actorSub := &types.Subscription{
		User: actor.String(), Topic: topic,
		ModeWant: actorGranted, ModeGiven: targetGranted,
	}
	actorSub.SetPublic(targetUser.Public)
	actorSub.SetTrusted(targetUser.Trusted)
	targetSub := &types.Subscription{
		User: target.String(), Topic: topic,
		ModeWant: targetGranted, ModeGiven: actorGranted,
	}
	targetSub.SetPublic(actorUser.Public)
	targetSub.SetTrusted(actorUser.Trusted)
	if err = store.Topics.CreateP2P(actorSub, targetSub); err == nil {
		return nil
	}
	// 跨节点同时创建时，数据库唯一约束只允许一个成功；重新读取即可确认结果。
	if latest, lookupErr := store.Topics.Get(topic); lookupErr == nil && latest != nil {
		return ensureBusinessP2PSubscriptions(topic, actor, target)
	}
	return err
}

func ensureBusinessP2PSubscriptions(topic string, actor, target types.Uid) error {
	users, err := store.Users.GetAll(actor, target)
	if err != nil {
		return err
	}
	if len(users) != 2 {
		return types.ErrUserNotFound
	}
	byUID := make(map[types.Uid]*types.User, 2)
	for index := range users {
		byUID[users[index].Uid()] = &users[index]
	}
	for _, uid := range []types.Uid{actor, target} {
		user, userOK := byUID[uid]
		peerUID := actor
		if uid == actor {
			peerUID = target
		}
		peer, peerOK := byUID[peerUID]
		if !userOK || !peerOK || user.State != types.StateOK || peer.State != types.StateOK {
			return types.ErrUserNotFound
		}
		sub, err := store.Subs.Get(topic, uid, false)
		if err != nil && !errors.Is(err, types.ErrNotFound) {
			return err
		}
		if sub == nil {
			created := &types.Subscription{
				User: uid.String(), Topic: topic,
				ModeWant:  user.Access.Auth&globals.typesModeCP2P | types.ModeApprove,
				ModeGiven: peer.Access.Auth&globals.typesModeCP2P | types.ModeApprove,
			}
			created.SetPublic(peer.Public)
			created.SetTrusted(peer.Trusted)
			if createErr := store.Subs.Create(created); createErr != nil {
				// 跨节点同时恢复订阅时，重新读取确认另一节点已经完成。
				latest, lookupErr := store.Subs.Get(topic, uid, false)
				if lookupErr != nil || latest == nil {
					return createErr
				}
				sub = latest
			} else {
				sub = created
			}
		}
		if !(sub.ModeWant & sub.ModeGiven).IsWriter() {
			return types.ErrPermissionDenied
		}
	}
	return nil
}
