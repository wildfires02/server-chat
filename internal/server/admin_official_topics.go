package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	admincontrol "chat/server/admin"
	"chat/server/store"
	"chat/server/store/types"
)

// officialTopicStore 抽象官方 Topic 管理所需的 Topic 持久化操作。
type officialTopicStore interface {
	Create(topic *types.Topic, owner types.Uid, private any) error
	Get(topic string) (*types.Topic, error)
	Update(topic string, update map[string]any) error
	Delete(topic string, isChannel, hard bool) error
}

// officialTopicSubscriptionStore 抽象官方成员角色管理所需的订阅操作。
type officialTopicSubscriptionStore interface {
	Create(subs ...*types.Subscription) error
	Get(topic string, user types.Uid, keepDeleted bool) (*types.Subscription, error)
	Update(topic string, user types.Uid, update map[string]any) error
	Delete(topic string, user types.Uid) error
}

// officialTopicUserStore 抽象平台分配成员前的用户状态查询。
type officialTopicUserStore interface {
	Get(uid types.Uid) (*types.User, error)
}

// officialTopicManager 协调官方 Topic、订阅和版本化控制面变更。
type officialTopicManager struct {
	mu         sync.Mutex
	control    *admincontrol.ControlPlane
	topics     officialTopicStore
	subs       officialTopicSubscriptionStore
	users      officialTopicUserStore
	newTopic   func() string
	invalidate func(string)
}

// officialTopicCreateInput 定义平台创建官方频道或大群的请求。
type officialTopicCreateInput struct {
	OrganizationID      string         `json:"org_id"`
	Owner               string         `json:"owner"`
	ScaleClass          string         `json:"scale_class,omitempty"`
	Admins              []string       `json:"admins,omitempty"`
	Public              map[string]any `json:"public,omitempty"`
	JoinPolicy          string         `json:"join_policy,omitempty"`
	DirectMessagePolicy string         `json:"dm_policy,omitempty"`
	ReactionsEnabled    bool           `json:"reactions_enabled,omitempty"`
}

// officialTopicPatchInput 定义官方 Topic 可热更新的策略字段。
type officialTopicPatchInput struct {
	OfficialStatus      *string         `json:"official_status,omitempty"`
	JoinPolicy          *string         `json:"join_policy,omitempty"`
	DirectMessagePolicy *string         `json:"dm_policy,omitempty"`
	AllMuted            *bool           `json:"all_muted,omitempty"`
	ReactionsEnabled    *bool           `json:"reactions_enabled,omitempty"`
	Public              json.RawMessage `json:"public,omitempty"`
}

// officialTopicRoleInput 定义一次平台成员角色分配。
type officialTopicRoleInput struct {
	Role string `json:"role"`
}

// officialTopicMutationView 返回变更后的版本和官方 Topic 快照。
type officialTopicMutationView struct {
	Version uint64                     `json:"version"`
	Topic   admincontrol.OfficialTopic `json:"topic"`
}

// newOfficialTopicManager 连接真实持久层并创建官方 Topic 管理器。
func newOfficialTopicManager(control *admincontrol.ControlPlane) *officialTopicManager {
	return &officialTopicManager{
		control: control, topics: store.Topics, subs: store.Subs, users: store.Users,
		newTopic: genTopicName,
		invalidate: func(topic string) {
			if globals.hub == nil {
				return
			}
			select {
			case globals.hub.unreg <- &topicUnreg{rcptTo: topic}:
			default:
			}
		},
	}
}

// create 原子化创建官方对象、所有者/管理员订阅和控制面审计。
func (manager *officialTopicManager) create(expected uint64, input officialTopicCreateInput,
	actor admincontrol.Actor) (officialTopicMutationView, error) {
	if manager == nil {
		return officialTopicMutationView{}, admincontrol.ErrInvalid
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.control == nil || manager.topics == nil ||
		manager.subs == nil || manager.users == nil || manager.newTopic == nil {
		return officialTopicMutationView{}, admincontrol.ErrInvalid
	}
	owner := types.ParseUserId(strings.TrimSpace(input.Owner))
	if owner.IsZero() {
		return officialTopicMutationView{}, admincontrol.ErrInvalid
	}
	user, err := manager.users.Get(owner)
	if err != nil {
		return officialTopicMutationView{}, err
	}
	if user == nil {
		return officialTopicMutationView{}, admincontrol.ErrNotFound
	}
	if user.State != types.StateOK {
		return officialTopicMutationView{}, admincontrol.ErrProtected
	}

	joinPolicy := strings.TrimSpace(input.JoinPolicy)
	if joinPolicy == "" {
		joinPolicy = "open"
	}
	scaleClass := strings.TrimSpace(input.ScaleClass)
	if scaleClass == "" {
		scaleClass = "normal"
	}
	settings := manager.control.Snapshot().Settings.Topics
	if (scaleClass == "large" && !settings.OfficialLargeGroupsEnabled) ||
		(scaleClass != "large" && !settings.OfficialChannelsEnabled) {
		return officialTopicMutationView{}, admincontrol.ErrProtected
	}
	directMessagePolicy := strings.TrimSpace(input.DirectMessagePolicy)
	if directMessagePolicy == "" {
		directMessagePolicy = "disabled"
	}
	topicName, err := manager.uniqueTopicName()
	if err != nil {
		return officialTopicMutationView{}, err
	}
	now := time.Now().UTC()
	record := admincontrol.OfficialTopic{
		Topic: topicName, OrganizationID: strings.TrimSpace(input.OrganizationID),
		Owner: owner.UserId(), Official: true, OfficialStatus: "verified",
		ScaleClass: scaleClass, MemberLimit: 0, JoinPolicy: joinPolicy,
		AdminAssignPolicy: "platform", DirectMessagePolicy: directMessagePolicy,
		ReactionsEnabled: input.ReactionsEnabled, CreatedBy: actor.Subject,
		CreatedAt: now, UpdatedAt: now,
	}
	if err = admincontrol.ValidateOfficialTopic(record); err != nil {
		return officialTopicMutationView{}, err
	}
	officialAux, err := officialPolicyIntoAux(nil, record)
	if err != nil {
		return officialTopicMutationView{}, err
	}

	isChannel := scaleClass != "large"
	defaultAuth := types.ModeNone
	if joinPolicy == "open" {
		if !isChannel {
			defaultAuth = types.ModeJoin | types.ModeRead | types.ModeWrite | types.ModePres
		}
	}
	topic := &types.Topic{
		ObjHeader: types.ObjHeader{Id: topicName},
		UseBt:     isChannel,
		Access: types.DefaultAccess{
			Auth: defaultAuth,
			Anon: types.ModeNone,
		},
		Public: input.Public,
		Aux:    officialAux,
	}
	topic.GiveAccess(owner, types.ModeCFull, types.ModeCFull)
	if err = manager.topics.Create(topic, owner, nil); err != nil {
		return officialTopicMutationView{}, err
	}
	if len(input.Admins) > 100 {
		_ = manager.topics.Delete(topicName, isChannel, true)
		return officialTopicMutationView{}, admincontrol.ErrInvalid
	}
	adminSeen := map[types.Uid]struct{}{owner: {}}
	for _, rawAdmin := range input.Admins {
		adminUID := types.ParseUserId(strings.TrimSpace(rawAdmin))
		if adminUID.IsZero() {
			_ = manager.topics.Delete(topicName, isChannel, true)
			return officialTopicMutationView{}, admincontrol.ErrInvalid
		}
		if _, duplicate := adminSeen[adminUID]; duplicate {
			continue
		}
		adminSeen[adminUID] = struct{}{}
		adminUser, getErr := manager.users.Get(adminUID)
		if getErr != nil || adminUser == nil || adminUser.State != types.StateOK {
			_ = manager.topics.Delete(topicName, isChannel, true)
			if getErr != nil {
				return officialTopicMutationView{}, getErr
			}
			return officialTopicMutationView{}, admincontrol.ErrNotFound
		}
		adminMode := types.ModeCFull &^ types.ModeOwner
		if err = manager.subs.Create(&types.Subscription{
			User: adminUID.String(), Topic: topicName,
			ModeWant: adminMode, ModeGiven: adminMode,
		}); err != nil {
			_ = manager.topics.Delete(topicName, isChannel, true)
			return officialTopicMutationView{}, err
		}
	}

	snapshot, err := manager.control.UpsertOfficialTopic(expected, record, actor)
	if err != nil {
		_ = manager.topics.Delete(topicName, isChannel, true)
		return officialTopicMutationView{}, err
	}
	record, err = manager.control.OfficialTopic(topicName)
	if err != nil {
		return officialTopicMutationView{}, err
	}
	if officialAux, projectionErr := officialPolicyIntoAux(topic.Aux, record); projectionErr == nil {
		_ = manager.topics.Update(topicName, map[string]any{"Aux": officialAux})
	}
	return officialTopicMutationView{Version: snapshot.Version, Topic: record}, nil
}

// uniqueTopicName 在有限次数内生成未被占用的群组 Topic 名称。
func (manager *officialTopicManager) uniqueTopicName() (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		topicName := manager.newTopic()
		if !strings.HasPrefix(topicName, "grp") || len(topicName) <= 3 {
			return "", admincontrol.ErrInvalid
		}
		existing, err := manager.topics.Get(topicName)
		if err != nil {
			return "", err
		}
		if existing == nil {
			return topicName, nil
		}
	}
	return "", admincontrol.ErrConflict
}

// patch 更新官方状态、入群、私聊、全员禁言和公开资料策略。
func (manager *officialTopicManager) patch(expected uint64, topicName string,
	input officialTopicPatchInput, actor admincontrol.Actor) (officialTopicMutationView, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if input.OfficialStatus == nil && input.JoinPolicy == nil &&
		input.DirectMessagePolicy == nil && input.AllMuted == nil &&
		input.ReactionsEnabled == nil && input.Public == nil {
		return officialTopicMutationView{}, admincontrol.ErrInvalid
	}
	record, err := manager.control.OfficialTopic(topicName)
	if err != nil {
		return officialTopicMutationView{}, err
	}
	stored, err := manager.topics.Get(topicName)
	if err != nil {
		return officialTopicMutationView{}, err
	}
	if stored == nil || (record.ScaleClass == "large" && stored.UseBt) ||
		(record.ScaleClass != "large" && !stored.UseBt) {
		return officialTopicMutationView{}, admincontrol.ErrNotFound
	}
	if policy, policyErr := officialPolicyFromAux(topicName, stored.Aux); policyErr != nil || policy == nil || !policy.Official {
		return officialTopicMutationView{}, admincontrol.ErrProtected
	}

	if input.OfficialStatus != nil {
		record.OfficialStatus = strings.TrimSpace(*input.OfficialStatus)
	}
	if input.JoinPolicy != nil {
		record.JoinPolicy = strings.TrimSpace(*input.JoinPolicy)
	}
	if input.DirectMessagePolicy != nil {
		record.DirectMessagePolicy = strings.TrimSpace(*input.DirectMessagePolicy)
	}
	if input.AllMuted != nil {
		record.AllMuted = *input.AllMuted
	}
	if input.ReactionsEnabled != nil {
		record.ReactionsEnabled = *input.ReactionsEnabled
	}
	record.UpdatedAt = time.Now().UTC()
	if err = admincontrol.ValidateOfficialTopic(record); err != nil {
		return officialTopicMutationView{}, err
	}

	officialAux, err := officialPolicyIntoAux(stored.Aux, record)
	if err != nil {
		return officialTopicMutationView{}, err
	}
	update := map[string]any{"Aux": officialAux}
	publicSet := input.Public != nil
	var public any
	if publicSet {
		if err = json.Unmarshal(input.Public, &public); err != nil {
			return officialTopicMutationView{}, admincontrol.ErrInvalid
		}
		update["Public"] = public
	}
	if err = manager.topics.Update(topicName, update); err != nil {
		return officialTopicMutationView{}, err
	}

	snapshot, err := manager.control.UpsertOfficialTopic(expected, record, actor)
	if err != nil {
		rollback := map[string]any{"Aux": stored.Aux}
		if publicSet {
			rollback["Public"] = stored.Public
		}
		_ = manager.topics.Update(topicName, rollback)
		return officialTopicMutationView{}, err
	}
	record, err = manager.control.OfficialTopic(topicName)
	if err != nil {
		return officialTopicMutationView{}, err
	}
	if manager.invalidate != nil {
		manager.invalidate(topicName)
	}
	return officialTopicMutationView{Version: snapshot.Version, Topic: record}, nil
}

// assignRole 通过平台接口分配官方频道或大群成员角色。
func (manager *officialTopicManager) assignRole(expected uint64, topicName, userID, role string,
	actor admincontrol.Actor) (officialTopicMutationView, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	record, err := manager.control.OfficialTopic(topicName)
	if err != nil {
		return officialTopicMutationView{}, err
	}
	storedTopic, err := manager.topics.Get(topicName)
	if err != nil {
		return officialTopicMutationView{}, err
	}
	if storedTopic == nil {
		return officialTopicMutationView{}, admincontrol.ErrNotFound
	}
	if policy, policyErr := officialPolicyFromAux(topicName, storedTopic.Aux); policyErr != nil || policy == nil || !policy.Official {
		return officialTopicMutationView{}, admincontrol.ErrProtected
	}
	target := types.ParseUserId(strings.TrimSpace(userID))
	role = strings.ToLower(strings.TrimSpace(role))
	if target.IsZero() || target.UserId() == record.Owner {
		return officialTopicMutationView{}, admincontrol.ErrProtected
	}
	mode, channelSubscriber, ok := officialRoleAccess(role, storedTopic.UseBt)
	if !ok {
		return officialTopicMutationView{}, admincontrol.ErrInvalid
	}
	user, err := manager.users.Get(target)
	if err != nil {
		return officialTopicMutationView{}, err
	}
	if user == nil {
		return officialTopicMutationView{}, admincontrol.ErrNotFound
	}
	if user.State != types.StateOK {
		return officialTopicMutationView{}, admincontrol.ErrProtected
	}

	groupSubscription, err := manager.subs.Get(topicName, target, false)
	if err != nil {
		return officialTopicMutationView{}, err
	}
	channelName := types.GrpToChn(topicName)
	channelSubscription, err := manager.subs.Get(channelName, target, false)
	if err != nil {
		return officialTopicMutationView{}, err
	}
	if groupSubscription != nil && channelSubscription != nil {
		return officialTopicMutationView{}, admincontrol.ErrConflict
	}

	undo, err := manager.applyOfficialRole(target, mode, channelSubscriber,
		topicName, channelName, groupSubscription, channelSubscription)
	if err != nil {
		return officialTopicMutationView{}, err
	}
	snapshot, err := manager.control.RecordOfficialAction(expected, actor, topicName,
		"official_topic.role.assign", target.UserId(), map[string]any{"role": role})
	if err != nil {
		undo()
		return officialTopicMutationView{}, err
	}
	if manager.invalidate != nil {
		manager.invalidate(topicName)
	}
	return officialTopicMutationView{Version: snapshot.Version, Topic: record}, nil
}

// officialRoleAccess 把官方对象的业务角色转换为受约束 ACL。
func officialRoleAccess(role string, channel bool) (types.AccessMode, bool, bool) {
	if role == "admin" {
		return types.ModeCFull &^ types.ModeOwner, false, true
	}
	if channel {
		switch role {
		case "publisher":
			return types.ModeJoin | types.ModeRead | types.ModeWrite | types.ModePres, false, true
		case "subscriber":
			return types.ModeCChnReader, true, true
		}
		return types.ModeNone, false, false
	}
	switch role {
	case "member":
		return types.ModeJoin | types.ModeRead | types.ModeWrite | types.ModePres, false, true
	case "readonly":
		return types.ModeJoin | types.ModeRead | types.ModePres, false, true
	case "banned":
		return types.ModeNone, false, true
	default:
		return types.ModeNone, false, false
	}
}

// applyOfficialRole 持久化角色并返回失败时可执行的补偿函数。
func (manager *officialTopicManager) applyOfficialRole(target types.Uid, mode types.AccessMode,
	channelSubscriber bool, groupName, channelName string,
	groupSubscription, channelSubscription *types.Subscription) (func(), error) {
	targetName := groupName
	current := groupSubscription
	if channelSubscriber {
		targetName = channelName
		current = channelSubscription
	}
	otherName := channelName
	other := channelSubscription
	if channelSubscriber {
		otherName = groupName
		other = groupSubscription
	}

	if current != nil {
		oldWant, oldGiven := current.ModeWant, current.ModeGiven
		if err := manager.subs.Update(targetName, target, map[string]any{
			"ModeWant": mode, "ModeGiven": mode,
		}); err != nil {
			return nil, err
		}
		return func() {
			_ = manager.subs.Update(targetName, target, map[string]any{
				"ModeWant": oldWant, "ModeGiven": oldGiven,
			})
		}, nil
	}

	if err := manager.subs.Create(&types.Subscription{
		User: target.String(), Topic: targetName, ModeWant: mode, ModeGiven: mode,
	}); err != nil {
		return nil, err
	}
	if other != nil {
		if err := manager.subs.Delete(otherName, target); err != nil {
			_ = manager.subs.Delete(targetName, target)
			return nil, err
		}
	}
	return func() {
		_ = manager.subs.Delete(targetName, target)
		if other != nil {
			_ = manager.subs.Create(&types.Subscription{
				User: target.String(), Topic: otherName,
				ModeWant: other.ModeWant, ModeGiven: other.ModeGiven,
				Private: other.Private,
			})
		}
	}, nil
}

// officialTopics 路由官方对象、角色、治理和审计接口。
func (handler *adminHTTPHandler) officialTopics(wrt http.ResponseWriter, req *http.Request,
	resource, requestID string) {
	parts := strings.Split(strings.TrimPrefix(resource, "official-topics"), "/")
	if len(parts) > 0 && parts[0] == "" {
		parts = parts[1:]
	}
	switch {
	case len(parts) == 0 && req.Method == http.MethodGet:
		handler.writeData(wrt, http.StatusOK, handler.control.Snapshot().OfficialTopics, requestID)
	case len(parts) == 0 && req.Method == http.MethodPost:
		handler.createOfficialTopic(wrt, req, requestID)
	case len(parts) == 1 && req.Method == http.MethodGet:
		record, err := handler.control.OfficialTopic(parts[0])
		if err != nil {
			handler.writeAdminError(wrt, err, requestID)
			return
		}
		handler.writeData(wrt, http.StatusOK, record, requestID)
	case len(parts) == 1 && req.Method == http.MethodPatch:
		handler.patchOfficialTopic(wrt, req, parts[0], requestID)
	case len(parts) == 2 && parts[1] == "audit" && req.Method == http.MethodGet:
		limit, _ := strconv.Atoi(req.URL.Query().Get("limit"))
		handler.writeData(wrt, http.StatusOK,
			handler.control.OfficialTopicAudit(parts[0], limit), requestID)
	case len(parts) == 4 && parts[1] == "members" && parts[3] == "role" &&
		req.Method == http.MethodPut:
		handler.assignOfficialTopicRole(wrt, req, parts[0], parts[2], requestID)
	case len(parts) == 3 && parts[1] == "moderation" && parts[2] == "mutes" &&
		req.Method == http.MethodPost:
		handler.muteOfficialTopicMembers(wrt, req, parts[0], requestID)
	case len(parts) == 4 && parts[1] == "moderation" && parts[2] == "mutes" &&
		req.Method == http.MethodDelete:
		handler.unmuteOfficialTopicMember(wrt, req, parts[0], parts[3], requestID)
	case len(parts) == 3 && parts[1] == "members" && req.Method == http.MethodDelete:
		handler.kickOfficialTopicMember(wrt, req, parts[0], parts[2], requestID)
	case len(parts) == 2 && parts[1] == "bans" && req.Method == http.MethodPost:
		handler.banOfficialTopicMembers(wrt, req, parts[0], requestID)
	case len(parts) == 3 && parts[1] == "bans" && req.Method == http.MethodDelete:
		handler.unbanOfficialTopicMember(wrt, req, parts[0], parts[2], requestID)
	default:
		handler.writeError(wrt, http.StatusNotFound, "admin_route_not_found", requestID)
	}
}

// muteOfficialTopicMembers 处理单人或批量禁言请求。
func (handler *adminHTTPHandler) muteOfficialTopicMembers(wrt http.ResponseWriter, req *http.Request,
	topicName, requestID string) {
	expected, ok := handler.expectedVersion(wrt, req, requestID)
	if !ok {
		return
	}
	var input officialMuteInput
	if !handler.decode(wrt, req, &input, requestID) {
		return
	}
	view, err := handler.official.mute(expected, topicName, input, handler.actor(req, requestID))
	handler.writeOfficialModerationMutation(wrt, http.StatusOK, view, err, requestID)
}

// unmuteOfficialTopicMember 处理单个成员解除禁言请求。
func (handler *adminHTTPHandler) unmuteOfficialTopicMember(wrt http.ResponseWriter, req *http.Request,
	topicName, userID, requestID string) {
	expected, ok := handler.expectedVersion(wrt, req, requestID)
	if !ok {
		return
	}
	view, err := handler.official.unmute(expected, topicName, userID,
		handler.actor(req, requestID))
	handler.writeOfficialModerationMutation(wrt, http.StatusOK, view, err, requestID)
}

// kickOfficialTopicMember 处理成员移出请求。
func (handler *adminHTTPHandler) kickOfficialTopicMember(wrt http.ResponseWriter, req *http.Request,
	topicName, userID, requestID string) {
	expected, ok := handler.expectedVersion(wrt, req, requestID)
	if !ok {
		return
	}
	view, err := handler.official.kick(expected, topicName, userID,
		req.URL.Query().Get("reason_code"), req.URL.Query().Get("note"),
		handler.actor(req, requestID))
	handler.writeOfficialModerationMutation(wrt, http.StatusOK, view, err, requestID)
}

// banOfficialTopicMembers 处理单人或批量封禁请求。
func (handler *adminHTTPHandler) banOfficialTopicMembers(wrt http.ResponseWriter, req *http.Request,
	topicName, requestID string) {
	expected, ok := handler.expectedVersion(wrt, req, requestID)
	if !ok {
		return
	}
	var input officialBanInput
	if !handler.decode(wrt, req, &input, requestID) {
		return
	}
	view, err := handler.official.ban(expected, topicName, input, handler.actor(req, requestID))
	handler.writeOfficialModerationMutation(wrt, http.StatusOK, view, err, requestID)
}

// unbanOfficialTopicMember 处理单个成员解封请求。
func (handler *adminHTTPHandler) unbanOfficialTopicMember(wrt http.ResponseWriter, req *http.Request,
	topicName, userID, requestID string) {
	expected, ok := handler.expectedVersion(wrt, req, requestID)
	if !ok {
		return
	}
	view, err := handler.official.unban(expected, topicName, userID,
		handler.actor(req, requestID))
	handler.writeOfficialModerationMutation(wrt, http.StatusOK, view, err, requestID)
}

// createOfficialTopic 解析并执行官方对象创建请求。
func (handler *adminHTTPHandler) createOfficialTopic(wrt http.ResponseWriter, req *http.Request,
	requestID string) {
	expected, ok := handler.expectedVersion(wrt, req, requestID)
	if !ok {
		return
	}
	var input officialTopicCreateInput
	if !handler.decode(wrt, req, &input, requestID) {
		return
	}
	view, err := handler.official.create(expected, input, handler.actor(req, requestID))
	handler.writeOfficialTopicMutation(wrt, http.StatusCreated, view, err, requestID)
}

// patchOfficialTopic 解析并执行官方策略更新请求。
func (handler *adminHTTPHandler) patchOfficialTopic(wrt http.ResponseWriter, req *http.Request,
	topicName, requestID string) {
	expected, ok := handler.expectedVersion(wrt, req, requestID)
	if !ok {
		return
	}
	var input officialTopicPatchInput
	if !handler.decode(wrt, req, &input, requestID) {
		return
	}
	view, err := handler.official.patch(expected, topicName, input, handler.actor(req, requestID))
	handler.writeOfficialTopicMutation(wrt, http.StatusOK, view, err, requestID)
}

// assignOfficialTopicRole 解析并执行平台成员角色分配。
func (handler *adminHTTPHandler) assignOfficialTopicRole(wrt http.ResponseWriter, req *http.Request,
	topicName, userID, requestID string) {
	expected, ok := handler.expectedVersion(wrt, req, requestID)
	if !ok {
		return
	}
	var input officialTopicRoleInput
	if !handler.decode(wrt, req, &input, requestID) {
		return
	}
	view, err := handler.official.assignRole(expected, topicName, userID, input.Role,
		handler.actor(req, requestID))
	handler.writeOfficialTopicMutation(wrt, http.StatusOK, view, err, requestID)
}

// writeOfficialTopicMutation 统一输出官方 Topic 变更响应和新版本 ETag。
func (handler *adminHTTPHandler) writeOfficialTopicMutation(wrt http.ResponseWriter, status int,
	view officialTopicMutationView, err error, requestID string) {
	if err != nil {
		if errors.Is(err, types.ErrDuplicate) {
			err = admincontrol.ErrConflict
		}
		handler.writeAdminError(wrt, err, requestID)
		return
	}
	wrt.Header().Set("ETag", strconv.Quote(strconv.FormatUint(view.Version, 10)))
	handler.writeData(wrt, status, view, requestID)
}
