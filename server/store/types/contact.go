package types

import "time"

// ContactStatus 是当前用户与联系人的关系状态。
type ContactStatus string

const (
	ContactPending  ContactStatus = "pending"
	ContactAccepted ContactStatus = "accepted"
	ContactBlocked  ContactStatus = "blocked"
)

// AddressBookContact 是用户私有通讯录中的一条记录。
type AddressBookContact struct {
	User   string        `json:"user"`
	Alias  string        `json:"alias,omitempty"`
	Groups []string      `json:"groups,omitempty"`
	Status ContactStatus `json:"status"`
	// Request 在 pending 状态下为 incoming 或 outgoing。
	Request   string    `json:"request,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Version   uint64    `json:"version"`
}

// ContactGroup 是用户自定义的联系人分组。
type ContactGroup struct {
	Id        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Version   uint64    `json:"version"`
}

// ContactEvent 是供其它设备增量同步的通讯录变更事件。
type ContactEvent struct {
	Version uint64    `json:"version"`
	Type    string    `json:"type"`
	Id      string    `json:"id"`
	At      time.Time `json:"at"`
}

// ContactMutation 表示一次原子通讯录变更。
type ContactMutation struct {
	Op      string              `json:"op"`
	Contact *AddressBookContact `json:"contact,omitempty"`
	Group   *ContactGroup       `json:"group,omitempty"`
	User    string              `json:"user,omitempty"`
	GroupId string              `json:"group_id,omitempty"`
}

// ContactQuery 是跨设备同步查询。
type ContactQuery struct {
	Since           uint64 `json:"since,omitempty"`
	Limit           int    `json:"limit,omitempty"`
	Recommendations bool   `json:"recommendations,omitempty"`
}

// ContactRecommendation 是服务端生成的好友推荐。
type ContactRecommendation struct {
	User          string `json:"user"`
	MutualFriends int    `json:"mutual_friends"`
}

// ContactSnapshot 是全量或增量通讯录同步结果。
type ContactSnapshot struct {
	Version         uint64                  `json:"version"`
	Reset           bool                    `json:"reset,omitempty"`
	Contacts        []AddressBookContact    `json:"contacts,omitempty"`
	Groups          []ContactGroup          `json:"groups,omitempty"`
	Events          []ContactEvent          `json:"events,omitempty"`
	Recommendations []ContactRecommendation `json:"recommendations,omitempty"`
}
