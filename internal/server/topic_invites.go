package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"chat/server/store/types"
)

const (
	topicInviteTokenVersion = 1
	defaultTopicInviteTTL   = 30 * 24 * time.Hour
	maxTopicInviteTTL       = 365 * 24 * time.Hour
)

type topicInvitePayload struct {
	Version int    `json:"v"`
	Topic   string `json:"t"`
	Expires int64  `json:"e"`
}

func topicInviteSigningKey() ([]byte, error) {
	if len(globals.apiKeySalt) == 0 {
		return nil, errors.New("topic invite signing is not configured")
	}
	hasher := hmac.New(sha256.New, globals.apiKeySalt)
	_, _ = hasher.Write([]byte("server-chat/topic-invite/v1"))
	return hasher.Sum(nil), nil
}

func issueTopicInvite(topicName string, expiresAt time.Time) (string, error) {
	key, err := topicInviteSigningKey()
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(topicInvitePayload{
		Version: topicInviteTokenVersion,
		Topic:   topicName,
		Expires: expiresAt.Unix(),
	})
	if err != nil {
		return "", err
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	hasher := hmac.New(sha256.New, key)
	_, _ = hasher.Write([]byte(encodedPayload))
	return encodedPayload + "." + base64.RawURLEncoding.EncodeToString(hasher.Sum(nil)), nil
}

func validateTopicInvite(token, topicName string, now time.Time) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	key, err := topicInviteSigningKey()
	if err != nil {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	hasher := hmac.New(sha256.New, key)
	_, _ = hasher.Write([]byte(parts[0]))
	if !hmac.Equal(signature, hasher.Sum(nil)) {
		return false
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	var payload topicInvitePayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return false
	}
	return payload.Version == topicInviteTokenVersion &&
		payload.Topic == topicName &&
		payload.Expires > now.Unix()
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
	token, err := issueTopicInvite(t.name, expiresAt)
	if err != nil {
		sess.queueOut(ErrServiceUnavailableReply(pkt, now))
		return err
	}
	sess.queueOut(NoErrParamsReply(pkt, now, map[string]any{
		"invite": map[string]any{
			"token":   token,
			"expires": expiresAt,
		},
	}))
	return nil
}
