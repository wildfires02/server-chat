package server

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	admincontrol "chat/server/admin"
	"chat/server/store"
	"chat/server/store/types"
)

const officialJoinRequestPrefix = "official-topic:join-request:"

var errOfficialJoinPending = errors.New("official topic join request is pending")

// officialJoinRequest 是官方群入群审批队列中的持久记录。
type officialJoinRequest struct {
	Topic       string    `json:"topic"`
	User        string    `json:"user"`
	Status      string    `json:"status"`
	RequestedAt time.Time `json:"requested_at"`
	ReviewedAt  time.Time `json:"reviewed_at,omitempty"`
	ReviewedBy  string    `json:"reviewed_by,omitempty"`
	Note        string    `json:"note,omitempty"`
	Version     uint64    `json:"version"`
}

type officialJoinDecisionInput struct {
	Note string `json:"note,omitempty"`
}

func officialJoinRequestKey(topic string, uid types.Uid) string {
	return officialJoinRequestPrefix + topic + ":" + uid.String()
}

func loadOfficialJoinRequest(topic string, uid types.Uid) (officialJoinRequest, string, error) {
	key := officialJoinRequestKey(topic, uid)
	raw, err := store.PCache.Get(key)
	if err != nil {
		return officialJoinRequest{}, "", err
	}
	var request officialJoinRequest
	if err = json.Unmarshal([]byte(raw), &request); err != nil {
		return officialJoinRequest{}, "", err
	}
	return request, raw, nil
}

func submitOfficialJoinRequest(topic string, uid types.Uid, now time.Time) (officialJoinRequest, error) {
	key := officialJoinRequestKey(topic, uid)
	for attempt := 0; attempt < 8; attempt++ {
		request, oldRaw, err := loadOfficialJoinRequest(topic, uid)
		if errors.Is(err, types.ErrNotFound) {
			request = officialJoinRequest{
				Topic: topic, User: uid.UserId(), Status: "pending",
				RequestedAt: now.UTC(), Version: 1,
			}
			newRaw, marshalErr := json.Marshal(request)
			if marshalErr != nil {
				return officialJoinRequest{}, marshalErr
			}
			if err = store.PCache.Upsert(key, string(newRaw), true); err == nil {
				return request, nil
			}
			continue
		}
		if err != nil {
			return officialJoinRequest{}, err
		}
		if request.Status == "pending" {
			return request, nil
		}
		// 被拒绝后至少等待一小时才能再次申请，避免审批队列被反复刷入。
		if request.Status == "rejected" && now.Before(request.ReviewedAt.Add(time.Hour)) {
			return request, admincontrol.ErrProtected
		}
		request.Status = "pending"
		request.RequestedAt = now.UTC()
		request.ReviewedAt = time.Time{}
		request.ReviewedBy = ""
		request.Note = ""
		request.Version++
		newRaw, marshalErr := json.Marshal(request)
		if marshalErr != nil {
			return officialJoinRequest{}, marshalErr
		}
		swapped, swapErr := store.PCache.CompareAndSwap(key, oldRaw, string(newRaw))
		if swapErr != nil {
			return officialJoinRequest{}, swapErr
		}
		if swapped {
			return request, nil
		}
	}
	return officialJoinRequest{}, admincontrol.ErrConflict
}

func listOfficialJoinRequests(topic, status string, limit int) ([]officialJoinRequest, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	entries, err := store.PCache.List(officialJoinRequestPrefix+topic+":", limit*3)
	if err != nil {
		return nil, err
	}
	result := make([]officialJoinRequest, 0, len(entries))
	for _, raw := range entries {
		var request officialJoinRequest
		if json.Unmarshal([]byte(raw), &request) != nil || request.Topic != topic {
			continue
		}
		if status != "" && request.Status != status {
			continue
		}
		result = append(result, request)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].RequestedAt.After(result[j].RequestedAt)
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func decideOfficialJoinRequest(topic string, uid types.Uid, status, reviewer, note string, now time.Time) (officialJoinRequest, error) {
	note = strings.TrimSpace(note)
	if (status != "approved" && status != "rejected") || len(note) > 500 {
		return officialJoinRequest{}, admincontrol.ErrInvalid
	}
	for attempt := 0; attempt < 8; attempt++ {
		request, oldRaw, err := loadOfficialJoinRequest(topic, uid)
		if err != nil {
			return officialJoinRequest{}, err
		}
		if request.Status != "pending" {
			return request, admincontrol.ErrConflict
		}
		request.Status = status
		request.ReviewedAt = now.UTC()
		request.ReviewedBy = reviewer
		request.Note = note
		request.Version++
		newRaw, err := json.Marshal(request)
		if err != nil {
			return officialJoinRequest{}, err
		}
		swapped, err := store.PCache.CompareAndSwap(
			officialJoinRequestKey(topic, uid), oldRaw, string(newRaw))
		if err != nil {
			return officialJoinRequest{}, err
		}
		if swapped {
			return request, nil
		}
	}
	return officialJoinRequest{}, admincontrol.ErrConflict
}

// checkOfficialSelfJoin 在默认 ACL 计算前强制执行官方群入群策略。
func (t *Topic) checkOfficialSelfJoin(sess *Session, pkt *ClientComMessage, uid types.Uid,
	hasInvite bool, now time.Time) error {
	if !t.isOfficialTopic() {
		return nil
	}
	if err := t.refreshOfficialPolicy(now); err != nil {
		sess.queueOut(ErrServiceUnavailableReply(pkt, now))
		return err
	}
	if t.official == nil || t.official.OfficialStatus != "verified" {
		sess.queueOut(ErrPermissionDeniedReply(pkt, now))
		return types.ErrPermissionDenied
	}
	switch t.official.JoinPolicy {
	case "open":
		return nil
	case "invite":
		if hasInvite {
			return nil
		}
	case "approval":
		if hasInvite {
			return nil
		}
		request, err := submitOfficialJoinRequest(t.name, uid, now)
		if err != nil {
			if errors.Is(err, admincontrol.ErrProtected) {
				sess.queueOut(ErrPolicyReply(pkt, now))
			} else {
				sess.queueOut(ErrServiceUnavailableReply(pkt, now))
			}
			return err
		}
		reply := NoErrAccepted(pkt.Id, t.original(uid), now)
		reply.Ctrl.Params = map[string]any{
			"join_request": map[string]any{
				"status": request.Status, "requested_at": request.RequestedAt,
			},
		}
		sess.queueOut(reply)
		return errOfficialJoinPending
	case "closed":
	}
	sess.queueOut(ErrPermissionDeniedReply(pkt, now))
	return types.ErrPermissionDenied
}

func (manager *officialTopicManager) approveJoin(expected uint64, topicName, userID string,
	input officialJoinDecisionInput, actor admincontrol.Actor) (officialTopicMutationView, error) {
	uid := types.ParseUserId(strings.TrimSpace(userID))
	if uid.IsZero() {
		return officialTopicMutationView{}, admincontrol.ErrInvalid
	}
	request, _, err := loadOfficialJoinRequest(topicName, uid)
	if err != nil {
		return officialTopicMutationView{}, err
	}
	if request.Status != "pending" {
		return officialTopicMutationView{}, admincontrol.ErrConflict
	}
	view, err := manager.assignRole(expected, topicName, userID, "member", actor)
	if err != nil {
		return officialTopicMutationView{}, err
	}
	if _, err = decideOfficialJoinRequest(topicName, uid, "approved", actor.Subject,
		input.Note, time.Now().UTC()); err != nil {
		return officialTopicMutationView{}, err
	}
	return view, nil
}

func (manager *officialTopicManager) rejectJoin(expected uint64, topicName, userID string,
	input officialJoinDecisionInput, actor admincontrol.Actor) (officialTopicMutationView, error) {
	uid := types.ParseUserId(strings.TrimSpace(userID))
	if uid.IsZero() {
		return officialTopicMutationView{}, admincontrol.ErrInvalid
	}
	record, err := manager.control.OfficialTopic(topicName)
	if err != nil {
		return officialTopicMutationView{}, err
	}
	request, err := decideOfficialJoinRequest(topicName, uid, "rejected", actor.Subject,
		input.Note, time.Now().UTC())
	if err != nil {
		return officialTopicMutationView{}, err
	}
	snapshot, err := manager.control.RecordOfficialAction(expected, actor, topicName,
		"official_topic.join.reject", request.User, map[string]any{"note": request.Note})
	if err != nil {
		return officialTopicMutationView{}, err
	}
	return officialTopicMutationView{Version: snapshot.Version, Topic: record}, nil
}
