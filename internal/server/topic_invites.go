package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"chat/server/store"
	"chat/server/store/types"
)

const (
	topicInviteTokenVersion = 1
	defaultTopicInviteTTL   = 30 * 24 * time.Hour
	maxTopicInviteTTL       = 365 * 24 * time.Hour
	topicInviteStatePrefix  = "official-topic:invite:"
)

type topicInvitePayload struct {
	Version int    `json:"v"`
	Topic   string `json:"t"`
	Expires int64  `json:"e"`
	ID      string `json:"i,omitempty"`
}

// topicInviteState 为可撤销、可限制使用次数的邀请保存服务端事实。
type topicInviteState struct {
	ID        string    `json:"id"`
	Topic     string    `json:"topic"`
	ExpiresAt time.Time `json:"expires_at"`
	MaxUses   int       `json:"max_uses"`
	Uses      int       `json:"uses"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by,omitempty"`
}

func topicInviteSigningKey() ([]byte, error) {
	if len(globals.apiKeySalt) == 0 {
		return nil, errors.New("topic invite signing is not configured")
	}
	hasher := hmac.New(sha256.New, globals.apiKeySalt)
	_, _ = hasher.Write([]byte("server-chat/topic-invite/v1"))
	return hasher.Sum(nil), nil
}

// issueTopicInvite 保留旧版无状态邀请，用于兼容已经发出的普通群邀请。
func issueTopicInvite(topicName string, expiresAt time.Time) (string, error) {
	return signTopicInvite(topicName, expiresAt, "")
}

func signTopicInvite(topicName string, expiresAt time.Time, inviteID string) (string, error) {
	key, err := topicInviteSigningKey()
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(topicInvitePayload{
		Version: topicInviteTokenVersion, Topic: topicName,
		Expires: expiresAt.Unix(), ID: inviteID,
	})
	if err != nil {
		return "", err
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	hasher := hmac.New(sha256.New, key)
	_, _ = hasher.Write([]byte(encodedPayload))
	return encodedPayload + "." + base64.RawURLEncoding.EncodeToString(hasher.Sum(nil)), nil
}

func topicInviteStateKey(topicName, inviteID string) string {
	return topicInviteStatePrefix + topicName + ":" + inviteID
}

func issueManagedTopicInvite(topicName, createdBy string, expiresAt time.Time,
	maxUses int, now time.Time) (string, topicInviteState, error) {
	if maxUses < 0 || maxUses > 100000 || !expiresAt.After(now) {
		return "", topicInviteState{}, types.ErrMalformed
	}
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return "", topicInviteState{}, err
	}
	state := topicInviteState{
		ID: hex.EncodeToString(random), Topic: topicName, ExpiresAt: expiresAt.UTC(),
		MaxUses: maxUses, Active: true, CreatedAt: now.UTC(), CreatedBy: createdBy,
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return "", topicInviteState{}, err
	}
	if err = store.PCache.Upsert(topicInviteStateKey(topicName, state.ID), string(raw), true); err != nil {
		return "", topicInviteState{}, err
	}
	token, err := signTopicInvite(topicName, expiresAt, state.ID)
	if err != nil {
		_ = store.PCache.Delete(topicInviteStateKey(topicName, state.ID))
		return "", topicInviteState{}, err
	}
	return token, state, nil
}

func parseTopicInvite(token, topicName string, now time.Time) (topicInvitePayload, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return topicInvitePayload{}, false
	}
	key, err := topicInviteSigningKey()
	if err != nil {
		return topicInvitePayload{}, false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return topicInvitePayload{}, false
	}
	hasher := hmac.New(sha256.New, key)
	_, _ = hasher.Write([]byte(parts[0]))
	if !hmac.Equal(signature, hasher.Sum(nil)) {
		return topicInvitePayload{}, false
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return topicInvitePayload{}, false
	}
	var payload topicInvitePayload
	if json.Unmarshal(payloadBytes, &payload) != nil {
		return topicInvitePayload{}, false
	}
	valid := payload.Version == topicInviteTokenVersion && payload.Topic == topicName &&
		payload.Expires > now.Unix()
	return payload, valid
}

func loadTopicInviteState(topicName, inviteID string) (topicInviteState, string, error) {
	raw, err := store.PCache.Get(topicInviteStateKey(topicName, inviteID))
	if err != nil {
		return topicInviteState{}, "", err
	}
	var state topicInviteState
	if err = json.Unmarshal([]byte(raw), &state); err != nil {
		return topicInviteState{}, "", err
	}
	return state, raw, nil
}

func validateTopicInvite(token, topicName string, now time.Time) bool {
	payload, ok := parseTopicInvite(token, topicName, now)
	if !ok {
		return false
	}
	if payload.ID == "" {
		return true
	}
	state, _, err := loadTopicInviteState(topicName, payload.ID)
	return err == nil && state.Active && state.ExpiresAt.After(now) &&
		(state.MaxUses == 0 || state.Uses < state.MaxUses)
}

// consumeTopicInvite 原子占用一次邀请名额；旧版无状态签名继续保持无限使用兼容。
func consumeTopicInvite(token, topicName string, now time.Time) bool {
	payload, ok := parseTopicInvite(token, topicName, now)
	if !ok || payload.ID == "" {
		return ok
	}
	for attempt := 0; attempt < 8; attempt++ {
		state, oldRaw, err := loadTopicInviteState(topicName, payload.ID)
		if err != nil || !state.Active || !state.ExpiresAt.After(now) ||
			(state.MaxUses > 0 && state.Uses >= state.MaxUses) {
			return false
		}
		if state.MaxUses == 0 {
			// 无限使用邀请不制造高并发 CAS 热点。
			return true
		}
		state.Uses++
		newRaw, err := json.Marshal(state)
		if err != nil {
			return false
		}
		swapped, err := store.PCache.CompareAndSwap(
			topicInviteStateKey(topicName, payload.ID), oldRaw, string(newRaw))
		if err != nil {
			return false
		}
		if swapped {
			return true
		}
	}
	return false
}

func listTopicInvites(topicName string, limit int) ([]topicInviteState, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	entries, err := store.PCache.List(topicInviteStatePrefix+topicName+":", limit*2)
	if err != nil {
		return nil, err
	}
	result := make([]topicInviteState, 0, len(entries))
	for _, raw := range entries {
		var state topicInviteState
		if json.Unmarshal([]byte(raw), &state) == nil && state.Topic == topicName {
			result = append(result, state)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func revokeTopicInvite(topicName, inviteID string) error {
	for attempt := 0; attempt < 8; attempt++ {
		state, oldRaw, err := loadTopicInviteState(topicName, inviteID)
		if err != nil {
			return err
		}
		if !state.Active {
			return nil
		}
		state.Active = false
		newRaw, err := json.Marshal(state)
		if err != nil {
			return err
		}
		swapped, err := store.PCache.CompareAndSwap(
			topicInviteStateKey(topicName, inviteID), oldRaw, string(newRaw))
		if err != nil {
			return err
		}
		if swapped {
			return nil
		}
	}
	return errors.New("invite revocation conflicted")
}

func topicInviteExpiry(request *MsgSetInvite, now time.Time) time.Time {
	ttl := defaultTopicInviteTTL
	if request != nil && request.ExpiresIn > 0 {
		maxSeconds := int64(maxTopicInviteTTL / time.Second)
		if request.ExpiresIn < maxSeconds {
			ttl = time.Duration(request.ExpiresIn) * time.Second
		} else {
			ttl = maxTopicInviteTTL
		}
	}
	return now.Add(ttl)
}

func (t *Topic) replySetInvite(sess *Session, asUid types.Uid, pkt *ClientComMessage) error {
	now := types.TimeNow()
	if t.cat != types.TopicCatGrp || pkt.Set == nil || pkt.Set.Invite == nil {
		sess.queueOut(ErrMalformedReply(pkt, now))
		return errors.New("group invite must target a group topic")
	}
	pud, found := t.perUser[asUid]
	if !found || pud.deleted || !(pud.modeWant & pud.modeGiven).IsSharer() {
		sess.queueOut(ErrPermissionDeniedReply(pkt, now))
		return errors.New("insufficient permission to create a group invite")
	}
	expiresAt := topicInviteExpiry(pkt.Set.Invite, now)
	token, state, err := issueManagedTopicInvite(t.name, asUid.UserId(), expiresAt,
		pkt.Set.Invite.MaxUses, now)
	if err != nil {
		sess.queueOut(ErrServiceUnavailableReply(pkt, now))
		return err
	}
	sess.queueOut(NoErrParamsReply(pkt, now, map[string]any{
		"invite": map[string]any{
			"token": token, "id": state.ID, "expires": expiresAt,
			"max_uses": state.MaxUses,
		},
	}))
	return nil
}
