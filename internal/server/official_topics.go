package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	admincontrol "chat/server/admin"
	"chat/server/store"
	"chat/server/store/types"
)

const (
	// officialTopicAuxKey 是 Topic.Aux 中由管理 API 独占维护的系统键。
	officialTopicAuxKey = "__official_topic"
	// officialModerationKeyPrefix 是单人禁言状态在持久缓存中的键前缀。
	officialModerationKeyPrefix = "official-topic:moderation:"
	// officialPolicyRefreshInterval 限制热路径刷新官方策略的最大频率。
	officialPolicyRefreshInterval = 2 * time.Second
	// officialModerationCacheTTL 限制跨节点禁言变更的最大本地可见延迟。
	officialModerationCacheTTL = time.Second
	// officialSlowModeKeyPrefix 保存官方群成员最近一次被接受的发布时间。
	officialSlowModeKeyPrefix = "official-topic:slow-mode:"
)

var errOfficialSlowMode = errors.New("official topic slow mode is active")

// officialTopicPolicy 是聊天运行时需要的官方 Topic 策略投影。
type officialTopicPolicy struct {
	OrganizationID      string `json:"org_id"`
	Owner               string `json:"owner"`
	Official            bool   `json:"official"`
	OfficialStatus      string `json:"official_status"`
	ScaleClass          string `json:"scale_class"`
	MemberLimit         int    `json:"member_limit"`
	JoinPolicy          string `json:"join_policy"`
	AdminAssignPolicy   string `json:"admin_assign_policy"`
	DirectMessagePolicy string `json:"dm_policy"`
	AllMuted            bool   `json:"all_muted"`
	SlowModeSeconds     int    `json:"slow_mode_seconds"`
	ReactionsEnabled    bool   `json:"reactions_enabled"`
	CreatedBy           string `json:"created_by"`
}

// officialModerationState 是一个成员当前生效的治理状态。
// 完整历史写入控制面审计；此对象只服务于低延迟权限判断。
type officialModerationState struct {
	Topic        string     `json:"topic"`
	Target       string     `json:"target"`
	Action       string     `json:"action"`
	Scope        string     `json:"scope"`
	ReasonCode   string     `json:"reason_code,omitempty"`
	Note         string     `json:"note,omitempty"`
	StartsAt     time.Time  `json:"starts_at"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	Operator     string     `json:"operator"`
	OperatorRole string     `json:"operator_role"`
	RequestID    string     `json:"request_id,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// officialModerationCache 保存治理状态及其最近加载时间。
type officialModerationCache struct {
	state    *officialModerationState
	loadedAt time.Time
}

// officialPolicyFromAdmin 把控制面对象转换成聊天运行时策略。
func officialPolicyFromAdmin(topic admincontrol.OfficialTopic) officialTopicPolicy {
	return officialTopicPolicy{
		OrganizationID: topic.OrganizationID, Owner: topic.Owner, Official: topic.Official,
		OfficialStatus: topic.OfficialStatus, ScaleClass: topic.ScaleClass,
		MemberLimit: topic.MemberLimit, JoinPolicy: topic.JoinPolicy,
		AdminAssignPolicy:   topic.AdminAssignPolicy,
		DirectMessagePolicy: topic.DirectMessagePolicy, AllMuted: topic.AllMuted,
		SlowModeSeconds:  topic.SlowModeSeconds,
		ReactionsEnabled: topic.ReactionsEnabled, CreatedBy: topic.CreatedBy,
	}
}

// officialPolicyToAuxValue 生成可由全部数据库适配器保存的 JSON 兼容对象。
func officialPolicyToAuxValue(policy officialTopicPolicy) (map[string]any, error) {
	raw, err := json.Marshal(policy)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if err = json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}

// officialPolicyFromAux 从 Topic.Aux 读取并严格校验平台策略。
func officialPolicyFromAux(topic string, aux map[string]any) (*officialTopicPolicy, error) {
	value, ok := aux[officialTopicAuxKey]
	if !ok || value == nil {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode official policy for %s: %w", topic, err)
	}
	var policy officialTopicPolicy
	if err = json.Unmarshal(raw, &policy); err != nil {
		return nil, fmt.Errorf("decode official policy for %s: %w", topic, err)
	}
	adminTopic := admincontrol.OfficialTopic{
		Topic: topic, OrganizationID: policy.OrganizationID, Owner: policy.Owner,
		Official:       policy.Official,
		OfficialStatus: policy.OfficialStatus, ScaleClass: policy.ScaleClass,
		MemberLimit: policy.MemberLimit, JoinPolicy: policy.JoinPolicy,
		AdminAssignPolicy:   policy.AdminAssignPolicy,
		DirectMessagePolicy: policy.DirectMessagePolicy, AllMuted: policy.AllMuted,
		SlowModeSeconds:  policy.SlowModeSeconds,
		ReactionsEnabled: policy.ReactionsEnabled, CreatedBy: policy.CreatedBy,
	}
	if err = admincontrol.ValidateOfficialTopic(adminTopic); err != nil {
		return nil, fmt.Errorf("invalid official policy for %s: %w", topic, err)
	}
	return &policy, nil
}

// officialPolicyIntoAux 把平台策略写入客户端不可修改、不可见的保留键。
func officialPolicyIntoAux(aux map[string]any, topic admincontrol.OfficialTopic) (map[string]any, error) {
	value, err := officialPolicyToAuxValue(officialPolicyFromAdmin(topic))
	if err != nil {
		return nil, err
	}
	updated := copyMap(aux)
	if updated == nil {
		updated = make(map[string]any)
	}
	updated[officialTopicAuxKey] = value
	return updated, nil
}

// isOfficialTopic 判断 Topic 是否由平台控制面认证。
func (t *Topic) isOfficialTopic() bool {
	return t != nil && t.official != nil && t.official.Official
}

// isOfficialReadOnlyChannel 判断 Topic 是否为官方只读频道。
func (t *Topic) isOfficialReadOnlyChannel() bool {
	return t.isOfficialTopic() && t.isChan && t.official.ScaleClass == "normal"
}

// refreshOfficialChannelMember 每次敏感操作前从数据库刷新频道角色。
// 这样平台撤销发布者后，不会被任一节点的旧 Topic Actor 缓存继续放行。
func (t *Topic) refreshOfficialChannelMember(uid types.Uid) error {
	if !t.isOfficialReadOnlyChannel() {
		return nil
	}
	t.officialRefreshedAt = time.Time{}
	if err := t.refreshOfficialPolicy(types.TimeNow()); err != nil {
		return err
	}
	if t.official == nil || t.official.OfficialStatus != "verified" {
		return types.ErrPermissionDenied
	}

	subscription, err := store.Subs.Get(t.name, uid, false)
	isChannelSubscriber := false
	if err == nil && subscription == nil {
		subscription, err = store.Subs.Get(types.GrpToChn(t.name), uid, false)
		isChannelSubscriber = subscription != nil
	}
	if err != nil {
		return err
	}
	userData := t.perUser[uid]
	if subscription == nil {
		userData.modeWant = types.ModeNone
		userData.modeGiven = types.ModeNone
		userData.isChan = true
		t.perUser[uid] = userData
		return types.ErrPermissionDenied
	}
	userData.modeWant = subscription.ModeWant
	userData.modeGiven = subscription.ModeGiven
	userData.isChan = isChannelSubscriber
	userData.deleted = false
	t.perUser[uid] = userData
	return nil
}

// isOfficialLargeGroup 判断 Topic 是否采用官方大群冷成员模型。
func (t *Topic) isOfficialLargeGroup() bool {
	return t != nil && t.official != nil && t.official.Official &&
		t.official.ScaleClass == "large" && !t.isChan
}

// loadSubscriber 按需把一个持久订阅加载到 Topic Actor。
func (t *Topic) loadSubscriber(uid types.Uid) (bool, error) {
	if uid.IsZero() {
		return false, nil
	}
	if pud, ok := t.perUser[uid]; ok && !pud.deleted {
		return true, nil
	}
	sub, err := store.Subs.Get(t.name, uid, false)
	if err != nil || sub == nil {
		return false, err
	}
	t.cacheSubscriber(sub)
	if t.isOfficialLargeGroup() {
		if t.officialMemberRefreshed == nil {
			t.officialMemberRefreshed = make(map[types.Uid]time.Time)
		}
		t.officialMemberRefreshed[uid] = types.TimeNow()
	}
	return true, nil
}

// cacheSubscriber 把订阅行转换为 Actor 内部的轻量成员快照。
func (t *Topic) cacheSubscriber(sub *types.Subscription) {
	if sub == nil {
		return
	}
	uid := types.ParseUid(sub.User)
	if uid.IsZero() {
		return
	}
	t.perUser[uid] = perUserData{
		delID: sub.DelId, readID: sub.ReadSeqId, recvID: sub.RecvSeqId,
		readHistory: append(types.ReadHistory(nil), sub.ReadHistory...),
		private:     sub.Private, modeWant: sub.ModeWant, modeGiven: sub.ModeGiven,
	}
	if (sub.ModeGiven & sub.ModeWant).IsOwner() {
		t.owner = uid
	}
}

// evictColdSubscriber 在成员最后一个 Session 离开后释放普通成员快照。
// 所有者和管理员继续驻留，便于管理操作；完整订阅仍保存在数据库中。
func (t *Topic) evictColdSubscriber(uid types.Uid) {
	if !t.isOfficialLargeGroup() || uid.IsZero() {
		return
	}
	pud, ok := t.perUser[uid]
	if !ok || pud.online > 0 {
		return
	}
	mode := pud.modeWant & pud.modeGiven
	if mode.IsOwner() || mode.IsAdmin() {
		return
	}
	delete(t.perUser, uid)
	delete(t.officialMemberRefreshed, uid)
	t.computePerUserAcsUnion()
	usersRegisterUser(uid, false)
}

// refreshOfficialLargeMember 按短 TTL 刷新成员 ACL。
// 平台在任意集群节点执行移出或封禁后，Topic 所有者节点无需重启即可快速收敛。
func (t *Topic) refreshOfficialLargeMember(uid types.Uid, now time.Time) error {
	if !t.isOfficialLargeGroup() {
		return nil
	}
	if t.officialMemberRefreshed == nil {
		t.officialMemberRefreshed = make(map[types.Uid]time.Time)
	}
	if refreshedAt, ok := t.officialMemberRefreshed[uid]; ok &&
		now.Sub(refreshedAt) < officialModerationCacheTTL {
		return nil
	}
	sub, err := store.Subs.Get(t.name, uid, false)
	if err != nil {
		return err
	}
	if sub == nil {
		if pud, ok := t.perUser[uid]; ok {
			pud.modeWant = types.ModeNone
			pud.modeGiven = types.ModeNone
			pud.deleted = true
			t.perUser[uid] = pud
		}
		t.officialMemberRefreshed[uid] = now
		return types.ErrPermissionDenied
	}
	old := t.perUser[uid]
	online := old.online
	t.cacheSubscriber(sub)
	updated := t.perUser[uid]
	updated.online = online
	t.perUser[uid] = updated
	t.officialMemberRefreshed[uid] = now
	return nil
}

// refreshOfficialPolicy 定期从 Topic 主记录刷新全员禁言、冻结等热策略。
func (t *Topic) refreshOfficialPolicy(now time.Time) error {
	if t.official == nil || now.Sub(t.officialRefreshedAt) < officialPolicyRefreshInterval {
		return nil
	}
	stored, err := store.Topics.Get(t.name)
	if err != nil {
		return err
	}
	if stored == nil {
		return types.ErrTopicNotFound
	}
	policy, err := officialPolicyFromAux(t.name, stored.Aux)
	if err != nil {
		return err
	}
	t.official = policy
	t.officialRefreshedAt = now
	return nil
}

// officialModerationKey 返回一个 Topic/用户唯一的当前治理状态键。
func officialModerationKey(topic string, uid types.Uid) string {
	return officialModerationKeyPrefix + topic + ":" + uid.String()
}

// loadOfficialModeration 读取单人治理状态，并使用短 TTL 抑制数据库放大。
func (t *Topic) loadOfficialModeration(uid types.Uid, now time.Time) (*officialModerationState, error) {
	if t.moderationCache == nil {
		t.moderationCache = make(map[types.Uid]officialModerationCache)
	}
	if cached, ok := t.moderationCache[uid]; ok &&
		now.Sub(cached.loadedAt) < officialModerationCacheTTL {
		return cached.state, nil
	}
	raw, err := store.PCache.Get(officialModerationKey(t.name, uid))
	if errors.Is(err, types.ErrNotFound) {
		t.moderationCache[uid] = officialModerationCache{loadedAt: now}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state officialModerationState
	if err = json.Unmarshal([]byte(raw), &state); err != nil {
		return nil, err
	}
	t.moderationCache[uid] = officialModerationCache{state: &state, loadedAt: now}
	return &state, nil
}

// moderationApplies 判断治理动作是否覆盖当前发布类型。
func moderationApplies(state *officialModerationState, scope string, now time.Time) bool {
	if state == nil || state.Action != "mute" || now.Before(state.StartsAt) ||
		(state.ExpiresAt != nil && !state.ExpiresAt.After(now)) {
		return false
	}
	return state.Scope == "all" || state.Scope == scope
}

// checkOfficialPublish 在官方 Topic 每次写入前校验最新角色、冻结和禁言策略。
func (t *Topic) checkOfficialPublish(uid types.Uid, scope string, now time.Time) error {
	if t.isOfficialReadOnlyChannel() {
		if err := t.refreshOfficialChannelMember(uid); err != nil {
			return err
		}
		pud := t.perUser[uid]
		mode := pud.modeWant & pud.modeGiven
		if pud.isChan || !mode.IsJoiner() || !mode.IsWriter() {
			return types.ErrPermissionDenied
		}
		return nil
	}
	if !t.isOfficialLargeGroup() {
		return nil
	}
	if err := t.refreshOfficialPolicy(now); err != nil {
		// 策略存储不可用时关闭写入，避免集群节点在治理状态不确定时放行。
		return err
	}
	if t.official == nil || t.official.OfficialStatus != "verified" {
		return types.ErrPermissionDenied
	}
	if err := t.refreshOfficialLargeMember(uid, now); err != nil {
		return err
	}
	pud, ok := t.perUser[uid]
	if !ok || pud.isChan {
		return types.ErrPermissionDenied
	}
	mode := pud.modeWant & pud.modeGiven
	if !mode.IsJoiner() || !mode.IsWriter() {
		return types.ErrPermissionDenied
	}
	if t.official.AllMuted && !mode.IsOwner() && !mode.IsAdmin() {
		return types.ErrPermissionDenied
	}
	state, err := t.loadOfficialModeration(uid, now)
	if err != nil {
		return err
	}
	if moderationApplies(state, scope, now) {
		return types.ErrPermissionDenied
	}
	return nil
}

// enforceOfficialSlowMode 使用持久缓存 CAS 在多个 Topic 节点之间原子保留发布窗口。
// 所有者和管理员不受慢速模式影响；通话信令不计入消息频率。
func (t *Topic) enforceOfficialSlowMode(uid types.Uid, scope string, now time.Time) (time.Duration, error) {
	if !t.isOfficialLargeGroup() || t.official == nil || t.official.SlowModeSeconds <= 0 || scope == "call" {
		return 0, nil
	}
	pud, ok := t.perUser[uid]
	if !ok {
		return 0, types.ErrPermissionDenied
	}
	mode := pud.modeWant & pud.modeGiven
	if mode.IsOwner() || mode.IsAdmin() {
		return 0, nil
	}
	window := time.Duration(t.official.SlowModeSeconds) * time.Second
	key := officialSlowModeKeyPrefix + t.name + ":" + uid.String()
	newValue := now.UTC().Format(time.RFC3339Nano)
	for attempt := 0; attempt < 8; attempt++ {
		oldValue, err := store.PCache.Get(key)
		if errors.Is(err, types.ErrNotFound) {
			if err = store.PCache.Upsert(key, newValue, true); err == nil {
				return 0, nil
			}
			continue
		}
		if err != nil {
			return 0, err
		}
		last, err := time.Parse(time.RFC3339Nano, oldValue)
		if err != nil {
			return 0, err
		}
		availableAt := last.Add(window)
		if now.Before(availableAt) {
			return availableAt.Sub(now), errOfficialSlowMode
		}
		swapped, err := store.PCache.CompareAndSwap(key, oldValue, newValue)
		if err != nil {
			return 0, err
		}
		if swapped {
			return 0, nil
		}
	}
	return 0, errors.New("slow mode reservation conflicted")
}

// saveOfficialModeration 保存当前生效的成员治理状态。
func saveOfficialModeration(state officialModerationState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return store.PCache.Upsert(
		officialModerationKey(state.Topic, types.ParseUserId(state.Target)),
		string(raw),
		false,
	)
}

// clearOfficialModeration 清除成员的当前治理状态。
func clearOfficialModeration(topic string, uid types.Uid) error {
	err := store.PCache.Delete(officialModerationKey(topic, uid))
	if errors.Is(err, types.ErrNotFound) {
		return nil
	}
	return err
}

// validModerationScope 校验 API 可接受的禁言范围。
func validModerationScope(scope string) bool {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "message", "media", "call", "all":
		return true
	default:
		return false
	}
}

// clientVisibleAux 返回移除平台保留键后的 Aux 副本。
func clientVisibleAux(aux map[string]any) map[string]any {
	if len(aux) == 0 {
		return nil
	}
	visible := copyMap(aux)
	delete(visible, officialTopicAuxKey)
	if len(visible) == 0 {
		return nil
	}
	return visible
}
