package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	translation "chat/server/translate"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
)

const (
	maxAuditEvents = 500
	casbinModel    = `
[request_definition]
r = sub, dom, obj, act

[policy_definition]
p = sub, dom, obj, act

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = r.sub == p.sub && (p.dom == "*" || r.dom == p.dom) && r.obj == p.obj && r.act == p.act
`
)

var (
	objectIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)
	clockPattern    = regexp.MustCompile(`^(?:[01][0-9]|2[0-3]):[0-5][0-9]$`)
)

// ControlPlane 管理持久化设置和 Casbin 策略快照。
type ControlPlane struct {
	mu       sync.RWMutex
	repo     Repository
	document *Document
	enforcer *casbin.Enforcer
}

// NewControlPlane 加载或初始化管理文档。
func NewControlPlane(repo Repository) (*ControlPlane, error) {
	if repo == nil {
		return nil, fmt.Errorf("%w: repository is required", ErrInvalid)
	}
	document, err := repo.Load()
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if document == nil {
		document = defaultDocument()
		if err = repo.Save(document); err != nil {
			return nil, err
		}
	}
	if err = normalizeDocument(document); err != nil {
		return nil, err
	}
	enforcer, err := buildEnforcer(document)
	if err != nil {
		return nil, err
	}
	return &ControlPlane{repo: repo, document: document, enforcer: enforcer}, nil
}

func defaultDocument() *Document {
	now := time.Now().UTC()
	all := permissionKeys("")
	readyAndFoundation := make([]string, 0, len(all))
	content := []string{"assets.read", "assets.write", "assets.publish", "official_topics.read",
		"official_topics.publish", "translation.manage", "notifications.manage"}
	auditor := []string{"system.settings.read", "system.roles.read", "system.audit.read",
		"official_topics.read", "moderation.read", "assets.read", "contacts.read"}
	for _, permission := range permissionCatalog {
		if permission.Stage != "integration" &&
			permission.Key != "system.roles.write" &&
			permission.Key != "system.settings.write" {
			readyAndFoundation = append(readyAndFoundation, permission.Key)
		}
	}
	return &Document{
		Version: 1,
		Roles: map[string]Role{
			"super_admin": {
				ID: "super_admin", Name: "超级管理员", Description: "拥有全部权限；仅用于平台最高管理员",
				Permissions: all, BuiltIn: true, CreatedAt: now, UpdatedAt: now,
			},
			"operations_admin": {
				ID: "operations_admin", Name: "运营管理员", Description: "管理官方会话、群组治理、素材和通知",
				Permissions: readyAndFoundation, BuiltIn: true, CreatedAt: now, UpdatedAt: now,
			},
			"content_editor": {
				ID: "content_editor", Name: "内容运营", Description: "维护素材、官方内容和翻译策略",
				Permissions: content, BuiltIn: true, CreatedAt: now, UpdatedAt: now,
			},
			"auditor": {
				ID: "auditor", Name: "审计员", Description: "只读查看配置、权限和治理记录",
				Permissions: auditor, BuiltIn: true, CreatedAt: now, UpdatedAt: now,
			},
			"employee": {
				ID: "employee", Name: "员工", Description: "使用员工个人工作区能力",
				Permissions: []string{"workspace.pins.read", "workspace.pins.write"},
				BuiltIn:     true, CreatedAt: now, UpdatedAt: now,
			},
		},
		Bindings:       make(map[string]Binding),
		OfficialTopics: make(map[string]OfficialTopic),
		Settings: ProductSettings{
			General: GeneralSettings{
				ProductName: "IM 管理中心", DefaultLocale: "zh-CN", TimeZone: "Asia/Shanghai",
			},
			Topics: TopicSettings{
				OfficialChannelsEnabled: true, OfficialLargeGroupsEnabled: true,
				MemberListPageSize: 100,
			},
			Moderation: ModerationSettings{
				RequireReason: true, DefaultMuteMinutes: 60, MaxMuteMinutes: 10080,
				AuditRetentionDays: 365,
			},
			Assets: AssetSettings{
				PublishingEnabled: true, ReviewRequired: true, MaxAssetsPerPack: 500,
				AllowedKinds: []string{"animated-emoji", "gif", "sticker"},
			},
			Translation: TranslationSettings{
				Enabled: false, StaffLanguage: "zh-CN", CustomerLanguage: "en", KeepOriginal: true,
				FailurePolicy: "hold", DefaultTimeoutMS: 1500, MaxAttempts: 3,
				Providers: []TranslationProviderSettings{}, Routes: []TranslationRouteSettings{},
			},
			Notification: NotificationSettings{
				PushEnabled: true, QuietHoursStart: "22:00", QuietHoursEnd: "08:00",
			},
		},
		UpdatedAt: now,
	}
}

func normalizeDocument(document *Document) error {
	if document.Version == 0 {
		document.Version = 1
	}
	if document.Roles == nil {
		document.Roles = make(map[string]Role)
	}
	if document.Bindings == nil {
		document.Bindings = make(map[string]Binding)
	}
	if document.OfficialTopics == nil {
		document.OfficialTopics = make(map[string]OfficialTopic)
	}
	//较旧的文档在提供商支持之前暴露了惰性启用的标志
	//存在。 保持升级可启动并关闭失败，直到管理员
	//明确地保存至少一个提供商。
	legacyTranslation := document.Settings.Translation.Enabled &&
		document.Settings.Translation.DefaultTimeoutMS == 0 &&
		document.Settings.Translation.Providers == nil
	now := time.Now().UTC()
	if role, ok := document.Roles["super_admin"]; ok && role.BuiltIn {
		// 内置超级管理员在服务升级后自动获得新增权限。
		allPermissions := permissionKeys("")
		currentPermissions := uniqueSorted(role.Permissions)
		same := len(currentPermissions) == len(allPermissions)
		for idx := 0; same && idx < len(allPermissions); idx++ {
			same = currentPermissions[idx] == allPermissions[idx]
		}
		if !same {
			role.Permissions = allPermissions
			role.UpdatedAt = now
			document.Roles["super_admin"] = role
		}
	}
	if _, ok := document.Roles["employee"]; !ok {
		document.Roles["employee"] = Role{
			ID: "employee", Name: "员工", Description: "使用员工个人工作区能力",
			Permissions: []string{"workspace.pins.read", "workspace.pins.write"},
			BuiltIn:     true, CreatedAt: now, UpdatedAt: now,
		}
	}
	normalizeTranslationSettings(&document.Settings.Translation)
	if legacyTranslation {
		document.Settings.Translation.Enabled = false
	}
	if err := validateSettings(document.Settings); err != nil {
		return err
	}
	for id, role := range document.Roles {
		if role.ID == "" {
			role.ID = id
		}
		if err := validateRole(role); err != nil {
			return err
		}
		document.Roles[id] = role
	}
	for id, binding := range document.Bindings {
		if binding.ID == "" {
			binding.ID = id
		}
		if err := validateBinding(binding, document.Roles); err != nil {
			return err
		}
		document.Bindings[id] = binding
	}
	for topic, officialTopic := range document.OfficialTopics {
		if officialTopic.Topic == "" {
			officialTopic.Topic = topic
		}
		if err := ValidateOfficialTopic(officialTopic); err != nil {
			return err
		}
		document.OfficialTopics[topic] = officialTopic
	}
	return nil
}

func buildEnforcer(document *Document) (*casbin.Enforcer, error) {
	parsedModel, err := model.NewModelFromString(casbinModel)
	if err != nil {
		return nil, err
	}
	enforcer, err := casbin.NewEnforcer(parsedModel)
	if err != nil {
		return nil, err
	}
	for _, role := range document.Roles {
		for _, key := range role.Permissions {
			permission, ok := permissionByKey(key)
			if !ok {
				return nil, fmt.Errorf("%w: unknown permission %q", ErrInvalid, key)
			}
			if _, err = enforcer.AddPolicy("role:"+role.ID, "*", permission.Object, permission.Action); err != nil {
				return nil, err
			}
		}
	}
	return enforcer, nil
}

func cloneDocument(document *Document) (*Document, error) {
	raw, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	var cloned Document
	if err = json.Unmarshal(raw, &cloned); err != nil {
		return nil, err
	}
	return &cloned, nil
}

// Snapshot 返回适合 JSON 响应的稳定副本。
func (control *ControlPlane) Snapshot() Snapshot {
	control.mu.RLock()
	defer control.mu.RUnlock()
	return snapshotOf(control.document)
}

func snapshotOf(document *Document) Snapshot {
	return Snapshot{
		Version: document.Version, Permissions: PermissionCatalog(),
		Roles: sortedRoles(document.Roles), Bindings: sortedBindings(document.Bindings),
		OfficialTopics: sortedOfficialTopics(document.OfficialTopics),
		Settings:       document.Settings, UpdatedAt: document.UpdatedAt,
	}
}

func (control *ControlPlane) mutate(expected uint64, actor Actor, action, target string,
	apply func(*Document) error) (Snapshot, error) {
	return control.mutateDetailed(expected, actor, action, target, nil, apply)
}

// mutateDetailed 以乐观锁更新控制面，并把业务详情写入不可变审计事件。
func (control *ControlPlane) mutateDetailed(expected uint64, actor Actor, action, target string,
	detail map[string]any, apply func(*Document) error) (Snapshot, error) {
	control.mu.Lock()
	defer control.mu.Unlock()
	if expected != control.document.Version {
		return Snapshot{}, ErrConflict
	}
	next, err := cloneDocument(control.document)
	if err != nil {
		return Snapshot{}, err
	}
	if err = apply(next); err != nil {
		return Snapshot{}, err
	}
	next.Version++
	next.UpdatedAt = time.Now().UTC()
	next.Audit = append(next.Audit, AuditEvent{
		ID: fmt.Sprintf("audit-%d-%d", next.UpdatedAt.UnixNano(), next.Version),
		At: next.UpdatedAt, Actor: actor.Subject, Action: action, Target: target,
		RequestID: actor.RequestID, RemoteIP: actor.RemoteIP, Version: next.Version,
		Detail: detail,
	})
	if len(next.Audit) > maxAuditEvents {
		next.Audit = append([]AuditEvent(nil), next.Audit[len(next.Audit)-maxAuditEvents:]...)
	}
	enforcer, err := buildEnforcer(next)
	if err != nil {
		return Snapshot{}, err
	}
	if err = control.repo.Save(next); err != nil {
		return Snapshot{}, err
	}
	control.document = next
	control.enforcer = enforcer
	return snapshotOf(next), nil
}

// UpsertOfficialTopic 创建或更新官方 Topic 的控制面记录。
// 首次创建时间由控制面填写，调用方不能通过更新覆盖。
func (control *ControlPlane) UpsertOfficialTopic(expected uint64, topic OfficialTopic,
	actor Actor) (Snapshot, error) {
	return control.mutateDetailed(expected, actor, "official_topic.upsert",
		"official-topic:"+topic.Topic, map[string]any{
			"org_id": topic.OrganizationID, "status": topic.OfficialStatus,
			"scale_class": topic.ScaleClass, "all_muted": topic.AllMuted,
		}, func(document *Document) error {
			if err := ValidateOfficialTopic(topic); err != nil {
				return err
			}
			now := time.Now().UTC()
			if existing, ok := document.OfficialTopics[topic.Topic]; ok {
				topic.CreatedAt = existing.CreatedAt
				topic.CreatedBy = existing.CreatedBy
				topic.Owner = existing.Owner
			} else {
				topic.CreatedAt = now
			}
			topic.UpdatedAt = now
			document.OfficialTopics[topic.Topic] = topic
			return nil
		})
}

// RecordOfficialAction 写入不改变策略快照的成员治理审计。
func (control *ControlPlane) RecordOfficialAction(expected uint64, actor Actor, topic, action,
	target string, detail map[string]any) (Snapshot, error) {
	auditTarget := "official-topic:" + topic
	if target != "" {
		auditTarget += "/member:" + target
	}
	return control.mutateDetailed(expected, actor, action, auditTarget, detail,
		func(*Document) error { return nil })
}

// OfficialTopic 返回官方 Topic 的控制面快照。
func (control *ControlPlane) OfficialTopic(topic string) (OfficialTopic, error) {
	control.mu.RLock()
	defer control.mu.RUnlock()
	value, ok := control.document.OfficialTopics[topic]
	if !ok {
		return OfficialTopic{}, ErrNotFound
	}
	return value, nil
}

// OfficialTopicAudit 返回指定官方 Topic 的最新治理事件。
func (control *ControlPlane) OfficialTopicAudit(topic string, limit int) []AuditEvent {
	control.mu.RLock()
	defer control.mu.RUnlock()
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	prefix := "official-topic:" + topic
	out := make([]AuditEvent, 0, limit)
	for idx := len(control.document.Audit) - 1; idx >= 0 && len(out) < limit; idx-- {
		event := control.document.Audit[idx]
		if event.Target == prefix || strings.HasPrefix(event.Target, prefix+"/") {
			out = append(out, event)
		}
	}
	return out
}

// UpsertRole 创建或替换角色。
func (control *ControlPlane) UpsertRole(expected uint64, role Role, actor Actor) (Snapshot, error) {
	return control.mutate(expected, actor, "role.upsert", "role:"+role.ID, func(document *Document) error {
		if err := validateRole(role); err != nil {
			return err
		}
		if role.ID == "super_admin" {
			return ErrProtected
		}
		now := time.Now().UTC()
		if existing, ok := document.Roles[role.ID]; ok {
			role.CreatedAt = existing.CreatedAt
			role.BuiltIn = existing.BuiltIn
		} else {
			role.CreatedAt = now
			role.BuiltIn = false
		}
		role.UpdatedAt = now
		role.Permissions = uniqueSorted(role.Permissions)
		document.Roles[role.ID] = role
		return nil
	})
}

// DeleteRole 删除没有绑定关系的自定义角色。
func (control *ControlPlane) DeleteRole(expected uint64, id string, actor Actor) (Snapshot, error) {
	return control.mutate(expected, actor, "role.delete", "role:"+id, func(document *Document) error {
		role, ok := document.Roles[id]
		if !ok {
			return ErrNotFound
		}
		if role.BuiltIn {
			return ErrProtected
		}
		for _, binding := range document.Bindings {
			if binding.RoleID == id {
				return ErrConflict
			}
		}
		delete(document.Roles, id)
		return nil
	})
}

// UpsertBinding 创建或替换主体到角色的绑定关系。
func (control *ControlPlane) UpsertBinding(expected uint64, binding Binding, actor Actor) (Snapshot, error) {
	return control.mutate(expected, actor, "binding.upsert", "binding:"+binding.ID, func(document *Document) error {
		if err := validateBinding(binding, document.Roles); err != nil {
			return err
		}
		now := time.Now().UTC()
		if existing, ok := document.Bindings[binding.ID]; ok {
			binding.CreatedAt = existing.CreatedAt
		} else {
			binding.CreatedAt = now
		}
		binding.UpdatedAt = now
		document.Bindings[binding.ID] = binding
		return nil
	})
}

// DeleteBinding 删除角色绑定关系。
func (control *ControlPlane) DeleteBinding(expected uint64, id string, actor Actor) (Snapshot, error) {
	return control.mutate(expected, actor, "binding.delete", "binding:"+id, func(document *Document) error {
		if _, ok := document.Bindings[id]; !ok {
			return ErrNotFound
		}
		delete(document.Bindings, id)
		return nil
	})
}

// UpdateSettings 替换全部支持热加载的产品设置。
func (control *ControlPlane) UpdateSettings(expected uint64, settings ProductSettings, actor Actor) (Snapshot, error) {
	return control.mutate(expected, actor, "settings.update", "settings", func(document *Document) error {
		normalizeTranslationSettings(&settings.Translation)
		if err := validateSettings(settings); err != nil {
			return err
		}
		settings.Assets.AllowedKinds = uniqueSorted(settings.Assets.AllowedKinds)
		document.Settings = settings
		return nil
	})
}

// Audit 按从新到旧的顺序返回审计事件。
func (control *ControlPlane) Audit(limit int) []AuditEvent {
	control.mu.RLock()
	defer control.mu.RUnlock()
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	count := len(control.document.Audit)
	if limit > count {
		limit = count
	}
	out := make([]AuditEvent, 0, limit)
	for idx := count - 1; idx >= count-limit; idx-- {
		out = append(out, control.document.Audit[idx])
	}
	return out
}

// Evaluate 解析有效绑定，并使用 Casbin 执行角色策略校验。
func (control *ControlPlane) Evaluate(subject, domain, permissionKey string, now time.Time) (Evaluation, error) {
	control.mu.RLock()
	defer control.mu.RUnlock()
	permission, ok := permissionByKey(permissionKey)
	if !ok || strings.TrimSpace(subject) == "" || strings.TrimSpace(domain) == "" {
		return Evaluation{}, ErrInvalid
	}
	result := Evaluation{Subject: subject, Domain: domain, Permission: permissionKey}
	for _, binding := range control.document.Bindings {
		if binding.Subject != subject || (binding.Domain != "*" && binding.Domain != domain) ||
			(binding.ExpiresAt != nil && !binding.ExpiresAt.After(now)) {
			continue
		}
		roleSubject := "role:" + binding.RoleID
		allowed, err := control.enforcer.Enforce(roleSubject, domain, permission.Object, permission.Action)
		if err != nil {
			return Evaluation{}, err
		}
		result.Roles = append(result.Roles, binding.RoleID)
		result.Allowed = result.Allowed || allowed
	}
	sort.Strings(result.Roles)
	return result, nil
}

func validateRole(role Role) error {
	if !objectIDPattern.MatchString(role.ID) || strings.TrimSpace(role.Name) == "" ||
		len(role.Name) > 80 || len(role.Description) > 500 || len(role.Permissions) > len(permissionCatalog) {
		return ErrInvalid
	}
	for _, key := range role.Permissions {
		if _, ok := permissionByKey(key); !ok {
			return ErrInvalid
		}
	}
	return nil
}

func validateBinding(binding Binding, roles map[string]Role) error {
	if !objectIDPattern.MatchString(binding.ID) || strings.TrimSpace(binding.Subject) == "" ||
		len(binding.Subject) > 160 || len(binding.Domain) > 128 ||
		(binding.Domain != "*" && !strings.Contains(binding.Domain, ":")) {
		return ErrInvalid
	}
	if _, ok := roles[binding.RoleID]; !ok {
		return ErrInvalid
	}
	return nil
}

func validateSettings(settings ProductSettings) error {
	if strings.TrimSpace(settings.General.ProductName) == "" || len(settings.General.ProductName) > 80 ||
		settings.General.DefaultLocale == "" || len(settings.General.DefaultLocale) > 16 ||
		settings.General.TimeZone == "" || len(settings.General.TimeZone) > 64 ||
		settings.Topics.MemberListPageSize < 20 || settings.Topics.MemberListPageSize > 500 ||
		settings.Moderation.DefaultMuteMinutes < 1 ||
		settings.Moderation.MaxMuteMinutes < settings.Moderation.DefaultMuteMinutes ||
		settings.Moderation.MaxMuteMinutes > 525600 ||
		settings.Moderation.AuditRetentionDays < 30 || settings.Moderation.AuditRetentionDays > 3650 ||
		settings.Assets.MaxAssetsPerPack < 1 || settings.Assets.MaxAssetsPerPack > 10000 ||
		len(settings.Assets.AllowedKinds) == 0 ||
		len(settings.Translation.StaffLanguage) > 16 || len(settings.Translation.CustomerLanguage) > 16 ||
		!clockPattern.MatchString(settings.Notification.QuietHoursStart) ||
		!clockPattern.MatchString(settings.Notification.QuietHoursEnd) {
		return ErrInvalid
	}
	for _, kind := range settings.Assets.AllowedKinds {
		if kind != "sticker" && kind != "animated-emoji" && kind != "gif" {
			return ErrInvalid
		}
	}
	return validateTranslationSettings(settings.Translation)
}

func normalizeTranslationSettings(settings *TranslationSettings) {
	translation.NormalizeSettings(settings)
}

func validateTranslationSettings(settings TranslationSettings) error {
	if err := translation.ValidateSettings(settings); err != nil {
		return ErrInvalid
	}
	return nil
}

// ValidateOfficialTopic 校验官方 Topic 的不可变产品约束。
func ValidateOfficialTopic(topic OfficialTopic) error {
	if !strings.HasPrefix(topic.Topic, "grp") || len(topic.Topic) <= 3 ||
		strings.TrimSpace(topic.OrganizationID) == "" || len(topic.OrganizationID) > 128 ||
		!strings.HasPrefix(topic.Owner, "usr") || len(topic.Owner) <= 3 ||
		!topic.Official || topic.MemberLimit != 0 ||
		topic.AdminAssignPolicy != "platform" ||
		strings.TrimSpace(topic.CreatedBy) == "" || len(topic.CreatedBy) > 160 {
		return ErrInvalid
	}
	switch topic.OfficialStatus {
	case "pending", "verified", "suspended", "revoked":
	default:
		return ErrInvalid
	}
	switch topic.ScaleClass {
	case "normal", "large":
	default:
		return ErrInvalid
	}
	switch topic.JoinPolicy {
	case "open", "invite", "approval", "closed":
	default:
		return ErrInvalid
	}
	switch topic.DirectMessagePolicy {
	case "open", "contact", "customer_assignment", "disabled":
	default:
		return ErrInvalid
	}
	if topic.SlowModeSeconds < 0 || topic.SlowModeSeconds > 24*60*60 {
		return ErrInvalid
	}
	return nil
}

func uniqueSorted(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
