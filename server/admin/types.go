// Package admin 提供即时通信管理控制面。
package admin

import (
	"errors"
	"sort"
	"strings"
	"time"

	translation "chat/server/translate"
)

var (
	// ErrNotFound 表示请求的控制面对象不存在。
	ErrNotFound = errors.New("admin: not found")
	// ErrConflict 表示调用方基于过期配置版本提交了修改。
	ErrConflict = errors.New("admin: configuration version conflict")
	// ErrInvalid 表示管理输入无效。
	ErrInvalid = errors.New("admin: invalid input")
	// ErrProtected 表示调用方尝试修改受保护的内置对象。
	ErrProtected = errors.New("admin: protected object")
)

// Permission 描述管理控制台公开的一项稳定权限。
type Permission struct {
	Key         string `json:"key"`
	Group       string `json:"group"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Object      string `json:"object"`
	Action      string `json:"action"`
	Risk        string `json:"risk"`
	Stage       string `json:"stage"`
}

// Role 是一组可复用的权限。
type Role struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Permissions []string  `json:"permissions"`
	BuiltIn     bool      `json:"built_in"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Binding 在指定域中为某类主体分配角色。
// 主体标识有意保持不透明，以便后续接入团购系统员工标识。
type Binding struct {
	ID        string     `json:"id"`
	Subject   string     `json:"subject"`
	RoleID    string     `json:"role_id"`
	Domain    string     `json:"domain"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// ProductSettings 包含无需修改部署配置文件即可调整的产品策略。
type ProductSettings struct {
	General      GeneralSettings      `json:"general"`
	Topics       TopicSettings        `json:"topics"`
	Moderation   ModerationSettings   `json:"moderation"`
	Assets       AssetSettings        `json:"assets"`
	Translation  TranslationSettings  `json:"translation"`
	Notification NotificationSettings `json:"notification"`
}

type GeneralSettings struct {
	ProductName   string `json:"product_name"`
	DefaultLocale string `json:"default_locale"`
	TimeZone      string `json:"time_zone"`
}

type TopicSettings struct {
	OfficialChannelsEnabled    bool `json:"official_channels_enabled"`
	OfficialLargeGroupsEnabled bool `json:"official_large_groups_enabled"`
	MemberListPageSize         int  `json:"member_list_page_size"`
}

type ModerationSettings struct {
	RequireReason      bool `json:"require_reason"`
	DefaultMuteMinutes int  `json:"default_mute_minutes"`
	MaxMuteMinutes     int  `json:"max_mute_minutes"`
	AuditRetentionDays int  `json:"audit_retention_days"`
}

type AssetSettings struct {
	PublishingEnabled bool     `json:"publishing_enabled"`
	ReviewRequired    bool     `json:"review_required"`
	MaxAssetsPerPack  int      `json:"max_assets_per_pack"`
	AllowedKinds      []string `json:"allowed_kinds"`
}

// 翻译配置由独立翻译模块定义。别名保持管理 API 兼容，同时避免聊天执行层依赖后台包。
type TranslationSettings = translation.Settings
type TranslationProviderSettings = translation.ProviderSettings
type TranslationRouteSettings = translation.RouteSettings

type NotificationSettings struct {
	PushEnabled     bool   `json:"push_enabled"`
	QuietHoursStart string `json:"quiet_hours_start"`
	QuietHoursEnd   string `json:"quiet_hours_end"`
}

// AuditEvent 是只追加、不覆盖的管理变更记录。
type AuditEvent struct {
	ID        string         `json:"id"`
	At        time.Time      `json:"at"`
	Actor     string         `json:"actor"`
	Action    string         `json:"action"`
	Target    string         `json:"target"`
	RequestID string         `json:"request_id,omitempty"`
	RemoteIP  string         `json:"remote_ip,omitempty"`
	Version   uint64         `json:"version"`
	Detail    map[string]any `json:"detail,omitempty"`
}

// OfficialTopic 描述由平台控制面认证的官方频道或官方大群。
// Topic 自身的 Aux 中也保存一份策略投影，聊天热路径不依赖管理控制面读取。
type OfficialTopic struct {
	Topic               string    `json:"topic"`
	OrganizationID      string    `json:"org_id"`
	Owner               string    `json:"owner"`
	Official            bool      `json:"official"`
	OfficialStatus      string    `json:"official_status"`
	ScaleClass          string    `json:"scale_class"`
	MemberLimit         int       `json:"member_limit"`
	JoinPolicy          string    `json:"join_policy"`
	AdminAssignPolicy   string    `json:"admin_assign_policy"`
	DirectMessagePolicy string    `json:"dm_policy"`
	AllMuted            bool      `json:"all_muted"`
	ReactionsEnabled    bool      `json:"reactions_enabled"`
	CreatedBy           string    `json:"created_by"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// Actor 描述对变更负责且已通过认证的管理员。
type Actor struct {
	Subject   string
	RequestID string
	RemoteIP  string
}

// Document 是持久化的控制面状态。
type Document struct {
	Version        uint64                   `json:"version"`
	Roles          map[string]Role          `json:"roles"`
	Bindings       map[string]Binding       `json:"bindings"`
	OfficialTopics map[string]OfficialTopic `json:"official_topics"`
	Settings       ProductSettings          `json:"settings"`
	Audit          []AuditEvent             `json:"audit"`
	UpdatedAt      time.Time                `json:"updated_at"`
}

// Snapshot 是 Document 对外提供的稳定接口表示。
type Snapshot struct {
	Version        uint64          `json:"version"`
	Permissions    []Permission    `json:"permissions"`
	Roles          []Role          `json:"roles"`
	Bindings       []Binding       `json:"bindings"`
	OfficialTopics []OfficialTopic `json:"official_topics"`
	Settings       ProductSettings `json:"settings"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

// Evaluation 是 Casbin 权限校验结果。
type Evaluation struct {
	Allowed    bool     `json:"allowed"`
	Subject    string   `json:"subject"`
	Domain     string   `json:"domain"`
	Permission string   `json:"permission"`
	Roles      []string `json:"roles"`
}

// Repository 持久化带版本号的管理文档。
type Repository interface {
	Load() (*Document, error)
	Save(*Document) error
}

func sortedRoles(roles map[string]Role) []Role {
	out := make([]Role, 0, len(roles))
	for _, role := range roles {
		role.Permissions = append([]string(nil), role.Permissions...)
		sort.Strings(role.Permissions)
		out = append(out, role)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].BuiltIn != out[j].BuiltIn {
			return out[i].BuiltIn
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

func sortedBindings(bindings map[string]Binding) []Binding {
	out := make([]Binding, 0, len(bindings))
	for _, binding := range bindings {
		out = append(out, binding)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

// sortedOfficialTopics 返回按 Topic 名称稳定排序的官方对象副本。
func sortedOfficialTopics(topics map[string]OfficialTopic) []OfficialTopic {
	out := make([]OfficialTopic, 0, len(topics))
	for _, topic := range topics {
		out = append(out, topic)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Topic < out[j].Topic
	})
	return out
}
