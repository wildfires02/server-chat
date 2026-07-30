package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	admincontrol "chat/server/admin"
	"chat/server/store"
	"chat/server/store/types"
)

// officialMuteInput 支持单人和批量禁言。
type officialMuteInput struct {
	User       string     `json:"user,omitempty"`
	Users      []string   `json:"users,omitempty"`
	Scope      string     `json:"scope"`
	ReasonCode string     `json:"reason_code,omitempty"`
	Note       string     `json:"note,omitempty"`
	StartsAt   *time.Time `json:"starts_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	Minutes    int        `json:"minutes,omitempty"`
}

// officialBanInput 支持单人和批量封禁。
type officialBanInput struct {
	User       string   `json:"user,omitempty"`
	Users      []string `json:"users,omitempty"`
	ReasonCode string   `json:"reason_code,omitempty"`
	Note       string   `json:"note,omitempty"`
}

// officialModerationView 是治理操作的版本化响应。
type officialModerationView struct {
	Version uint64   `json:"version"`
	Topic   string   `json:"topic"`
	Action  string   `json:"action"`
	Users   []string `json:"users"`
}

// officialModerationUndo 保存一次治理操作的补偿函数。
type officialModerationUndo func()

// validModerationText 限制审计原因和备注大小，避免控制面文档被超大输入放大。
func validModerationText(reasonCode, note string) bool {
	return len(strings.TrimSpace(reasonCode)) <= 64 && len(strings.TrimSpace(note)) <= 1000
}

// requireOfficialLargeTopic 校验治理目标确实是已注册的官方大群。
func (manager *officialTopicManager) requireOfficialLargeTopic(topic string) (admincontrol.OfficialTopic, error) {
	record, err := manager.control.OfficialTopic(topic)
	if err != nil {
		return admincontrol.OfficialTopic{}, err
	}
	stored, err := manager.topics.Get(topic)
	if err != nil {
		return admincontrol.OfficialTopic{}, err
	}
	if stored == nil || stored.UseBt || record.ScaleClass != "large" || !record.Official {
		return admincontrol.OfficialTopic{}, admincontrol.ErrProtected
	}
	return record, nil
}

// moderationTargets 规范化、去重并校验治理目标。
func moderationTargets(owner, single string, values []string) ([]types.Uid, []string, error) {
	if strings.TrimSpace(single) != "" {
		values = append(values, single)
	}
	if len(values) == 0 || len(values) > 100 {
		return nil, nil, admincontrol.ErrInvalid
	}
	seen := make(map[types.Uid]struct{}, len(values))
	uids := make([]types.Uid, 0, len(values))
	names := make([]string, 0, len(values))
	for _, value := range values {
		uid := types.ParseUserId(strings.TrimSpace(value))
		if uid.IsZero() || uid.UserId() == owner {
			return nil, nil, admincontrol.ErrProtected
		}
		if _, ok := seen[uid]; ok {
			continue
		}
		seen[uid] = struct{}{}
		uids = append(uids, uid)
		names = append(names, uid.UserId())
	}
	return uids, names, nil
}

// restoreModerationCache 创建持久治理状态的补偿操作。
func restoreModerationCache(topic string, uid types.Uid) (officialModerationUndo, error) {
	key := officialModerationKey(topic, uid)
	previous, err := store.PCache.Get(key)
	if err != nil && !errors.Is(err, types.ErrNotFound) {
		return nil, err
	}
	hadPrevious := err == nil
	return func() {
		if hadPrevious {
			_ = store.PCache.Upsert(key, previous, false)
		} else {
			_ = store.PCache.Delete(key)
		}
	}, nil
}

// mute 保存当前禁言状态，再以同一个控制面版本写入审计。
func (manager *officialTopicManager) mute(expected uint64, topic string, input officialMuteInput,
	actor admincontrol.Actor) (officialModerationView, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	record, err := manager.requireOfficialLargeTopic(topic)
	if err != nil {
		return officialModerationView{}, err
	}
	uids, names, err := moderationTargets(record.Owner, input.User, input.Users)
	if err != nil {
		return officialModerationView{}, err
	}
	scope := strings.ToLower(strings.TrimSpace(input.Scope))
	if !validModerationScope(scope) {
		return officialModerationView{}, admincontrol.ErrInvalid
	}
	settings := manager.control.Snapshot().Settings.Moderation
	if !validModerationText(input.ReasonCode, input.Note) {
		return officialModerationView{}, admincontrol.ErrInvalid
	}
	if settings.RequireReason && strings.TrimSpace(input.ReasonCode) == "" {
		return officialModerationView{}, admincontrol.ErrInvalid
	}
	now := time.Now().UTC()
	startsAt := now
	if input.StartsAt != nil {
		startsAt = input.StartsAt.UTC()
	}
	expiresAt := input.ExpiresAt
	if expiresAt == nil {
		minutes := input.Minutes
		if minutes <= 0 {
			minutes = settings.DefaultMuteMinutes
		}
		value := startsAt.Add(time.Duration(minutes) * time.Minute)
		expiresAt = &value
	}
	expiry := expiresAt.UTC()
	if !expiry.After(startsAt) ||
		expiry.Sub(startsAt) > time.Duration(settings.MaxMuteMinutes)*time.Minute {
		return officialModerationView{}, admincontrol.ErrInvalid
	}

	undos := make([]officialModerationUndo, 0, len(uids))
	for idx, uid := range uids {
		sub, getErr := manager.subs.Get(topic, uid, false)
		if getErr != nil {
			for _, undo := range undos {
				undo()
			}
			return officialModerationView{}, getErr
		}
		if sub == nil || !(sub.ModeWant & sub.ModeGiven).IsJoiner() {
			for _, undo := range undos {
				undo()
			}
			return officialModerationView{}, admincontrol.ErrNotFound
		}
		undo, cacheErr := restoreModerationCache(topic, uid)
		if cacheErr != nil {
			for _, previousUndo := range undos {
				previousUndo()
			}
			return officialModerationView{}, cacheErr
		}
		undos = append(undos, undo)
		state := officialModerationState{
			Topic: topic, Target: names[idx], Action: "mute", Scope: scope,
			ReasonCode: strings.TrimSpace(input.ReasonCode), Note: strings.TrimSpace(input.Note),
			StartsAt: startsAt, ExpiresAt: &expiry, Operator: actor.Subject,
			OperatorRole: "platform_root", RequestID: actor.RequestID, CreatedAt: now,
		}
		if cacheErr = saveOfficialModeration(state); cacheErr != nil {
			for _, previousUndo := range undos {
				previousUndo()
			}
			return officialModerationView{}, cacheErr
		}
	}
	detail := map[string]any{
		"users": names, "scope": scope, "reason_code": strings.TrimSpace(input.ReasonCode),
		"note": strings.TrimSpace(input.Note), "starts_at": startsAt, "expires_at": expiry,
	}
	snapshot, err := manager.control.RecordOfficialAction(expected, actor, topic,
		"official_topic.member.mute", "", detail)
	if err != nil {
		for _, undo := range undos {
			undo()
		}
		return officialModerationView{}, err
	}
	manager.invalidateTopic(topic)
	return officialModerationView{
		Version: snapshot.Version, Topic: topic, Action: "mute", Users: names,
	}, nil
}

// unmute 清除当前禁言状态并写入审计。
func (manager *officialTopicManager) unmute(expected uint64, topic, user string,
	actor admincontrol.Actor) (officialModerationView, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	record, err := manager.requireOfficialLargeTopic(topic)
	if err != nil {
		return officialModerationView{}, err
	}
	uids, names, err := moderationTargets(record.Owner, user, nil)
	if err != nil {
		return officialModerationView{}, err
	}
	undo, err := restoreModerationCache(topic, uids[0])
	if err != nil {
		return officialModerationView{}, err
	}
	if err = clearOfficialModeration(topic, uids[0]); err != nil {
		return officialModerationView{}, err
	}
	snapshot, err := manager.control.RecordOfficialAction(expected, actor, topic,
		"official_topic.member.unmute", names[0], nil)
	if err != nil {
		undo()
		return officialModerationView{}, err
	}
	manager.invalidateTopic(topic)
	return officialModerationView{
		Version: snapshot.Version, Topic: topic, Action: "unmute", Users: names,
	}, nil
}

// kick 移除成员订阅；该用户能否再次加入由 Topic 入群策略决定。
func (manager *officialTopicManager) kick(expected uint64, topic, user, reasonCode, note string,
	actor admincontrol.Actor) (officialModerationView, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	record, err := manager.requireOfficialLargeTopic(topic)
	if err != nil {
		return officialModerationView{}, err
	}
	if manager.control.Snapshot().Settings.Moderation.RequireReason &&
		strings.TrimSpace(reasonCode) == "" {
		return officialModerationView{}, admincontrol.ErrInvalid
	}
	if !validModerationText(reasonCode, note) {
		return officialModerationView{}, admincontrol.ErrInvalid
	}
	uids, names, err := moderationTargets(record.Owner, user, nil)
	if err != nil {
		return officialModerationView{}, err
	}
	sub, err := manager.subs.Get(topic, uids[0], false)
	if err != nil {
		return officialModerationView{}, err
	}
	if sub == nil {
		return officialModerationView{}, admincontrol.ErrNotFound
	}
	if err = manager.subs.Delete(topic, uids[0]); err != nil {
		return officialModerationView{}, err
	}
	cacheUndo, err := restoreModerationCache(topic, uids[0])
	if err == nil {
		err = clearOfficialModeration(topic, uids[0])
	}
	if err != nil {
		_ = manager.subs.Create(sub)
		return officialModerationView{}, err
	}
	snapshot, err := manager.control.RecordOfficialAction(expected, actor, topic,
		"official_topic.member.kick", names[0], map[string]any{
			"reason_code": strings.TrimSpace(reasonCode), "note": strings.TrimSpace(note),
		})
	if err != nil {
		_ = manager.subs.Create(sub)
		cacheUndo()
		return officialModerationView{}, err
	}
	manager.invalidateTopic(topic)
	return officialModerationView{
		Version: snapshot.Version, Topic: topic, Action: "kick", Users: names,
	}, nil
}

// ban 把目标成员 ACL 持久化为 ModeNone，直到显式解封。
func (manager *officialTopicManager) ban(expected uint64, topic string, input officialBanInput,
	actor admincontrol.Actor) (officialModerationView, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	record, err := manager.requireOfficialLargeTopic(topic)
	if err != nil {
		return officialModerationView{}, err
	}
	uids, names, err := moderationTargets(record.Owner, input.User, input.Users)
	if err != nil {
		return officialModerationView{}, err
	}
	settings := manager.control.Snapshot().Settings.Moderation
	if !validModerationText(input.ReasonCode, input.Note) {
		return officialModerationView{}, admincontrol.ErrInvalid
	}
	if settings.RequireReason && strings.TrimSpace(input.ReasonCode) == "" {
		return officialModerationView{}, admincontrol.ErrInvalid
	}
	undos := make([]officialModerationUndo, 0, len(uids))
	for _, uid := range uids {
		user, getErr := manager.users.Get(uid)
		if getErr != nil || user == nil {
			for _, undo := range undos {
				undo()
			}
			if getErr != nil {
				return officialModerationView{}, getErr
			}
			return officialModerationView{}, admincontrol.ErrNotFound
		}
		current, getErr := manager.subs.Get(topic, uid, true)
		if getErr != nil {
			for _, undo := range undos {
				undo()
			}
			return officialModerationView{}, getErr
		}
		if current != nil && current.DeletedAt == nil {
			oldWant, oldGiven := current.ModeWant, current.ModeGiven
			if getErr = manager.subs.Update(topic, uid, map[string]any{
				"ModeWant": types.ModeNone, "ModeGiven": types.ModeNone,
			}); getErr != nil {
				for _, undo := range undos {
					undo()
				}
				return officialModerationView{}, getErr
			}
			undos = append(undos, func() {
				_ = manager.subs.Update(topic, uid, map[string]any{
					"ModeWant": oldWant, "ModeGiven": oldGiven,
				})
			})
		} else {
			if getErr = manager.subs.Create(&types.Subscription{
				User: uid.String(), Topic: topic,
				ModeWant: types.ModeNone, ModeGiven: types.ModeNone,
			}); getErr != nil {
				for _, undo := range undos {
					undo()
				}
				return officialModerationView{}, getErr
			}
			undos = append(undos, func() { _ = manager.subs.Delete(topic, uid) })
		}
	}
	snapshot, err := manager.control.RecordOfficialAction(expected, actor, topic,
		"official_topic.member.ban", "", map[string]any{
			"users": names, "reason_code": strings.TrimSpace(input.ReasonCode),
			"note": strings.TrimSpace(input.Note),
		})
	if err != nil {
		for _, undo := range undos {
			undo()
		}
		return officialModerationView{}, err
	}
	manager.invalidateTopic(topic)
	return officialModerationView{
		Version: snapshot.Version, Topic: topic, Action: "ban", Users: names,
	}, nil
}

// unban 恢复大群普通成员 ACL。
func (manager *officialTopicManager) unban(expected uint64, topic, user string,
	actor admincontrol.Actor) (officialModerationView, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	record, err := manager.requireOfficialLargeTopic(topic)
	if err != nil {
		return officialModerationView{}, err
	}
	uids, names, err := moderationTargets(record.Owner, user, nil)
	if err != nil {
		return officialModerationView{}, err
	}
	sub, err := manager.subs.Get(topic, uids[0], true)
	if err != nil {
		return officialModerationView{}, err
	}
	if sub == nil || (sub.ModeWant & sub.ModeGiven).IsJoiner() {
		return officialModerationView{}, admincontrol.ErrNotFound
	}
	memberMode := types.ModeJoin | types.ModeRead | types.ModeWrite | types.ModePres
	if err = manager.subs.Update(topic, uids[0], map[string]any{
		"ModeWant": memberMode, "ModeGiven": memberMode, "DeletedAt": nil,
	}); err != nil {
		return officialModerationView{}, err
	}
	snapshot, err := manager.control.RecordOfficialAction(expected, actor, topic,
		"official_topic.member.unban", names[0], nil)
	if err != nil {
		_ = manager.subs.Update(topic, uids[0], map[string]any{
			"ModeWant": sub.ModeWant, "ModeGiven": sub.ModeGiven,
			"DeletedAt": sub.DeletedAt,
		})
		return officialModerationView{}, err
	}
	manager.invalidateTopic(topic)
	return officialModerationView{
		Version: snapshot.Version, Topic: topic, Action: "unban", Users: names,
	}, nil
}

// invalidateTopic 使本节点旧 Actor 尽快退出；其它节点由短 TTL 权限刷新收敛。
func (manager *officialTopicManager) invalidateTopic(topic string) {
	if manager.invalidate != nil {
		manager.invalidate(topic)
	}
}

// writeOfficialModerationMutation 统一输出治理变更响应。
func (handler *adminHTTPHandler) writeOfficialModerationMutation(wrt http.ResponseWriter, status int,
	view officialModerationView, err error, requestID string) {
	if err != nil {
		handler.writeAdminError(wrt, err, requestID)
		return
	}
	wrt.Header().Set("ETag", strconv.Quote(strconv.FormatUint(view.Version, 10)))
	handler.writeData(wrt, status, view, requestID)
}
