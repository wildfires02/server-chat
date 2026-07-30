package store

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"chat/server/store/types"
)

const (
	contactStatePrefix = "contacts:"
	maxContactEvents   = 1000
)

type contactState struct {
	Version  uint64                              `json:"version"`
	Contacts map[string]types.AddressBookContact `json:"contacts"`
	Groups   map[string]types.ContactGroup       `json:"groups"`
	Events   []types.ContactEvent                `json:"events"`
}

// ContactPersistenceInterface 管理联系人 CRUD、分组和多设备增量同步。
type ContactPersistenceInterface interface {
	Get(owner types.Uid, query types.ContactQuery) (*types.ContactSnapshot, error)
	Apply(owner types.Uid, mutation types.ContactMutation) (*types.ContactSnapshot, error)
}

type contactMapper struct {
	locks sync.Map
}

// Contacts 是通讯录持久化入口。
var Contacts ContactPersistenceInterface

func (m *contactMapper) userLock(owner types.Uid) *sync.Mutex {
	value, _ := m.locks.LoadOrStore(owner.String(), &sync.Mutex{})
	return value.(*sync.Mutex)
}

func emptyContactState() *contactState {
	return &contactState{
		Contacts: make(map[string]types.AddressBookContact),
		Groups:   make(map[string]types.ContactGroup),
	}
}

func loadContactState(owner types.Uid) (*contactState, error) {
	raw, err := PCache.Get(contactStatePrefix + owner.String())
	if errors.Is(err, types.ErrNotFound) {
		return emptyContactState(), nil
	}
	if err != nil {
		return nil, err
	}
	state := emptyContactState()
	if err = unmarshalPersistentJSON(raw, state); err != nil {
		return nil, err
	}
	if state.Contacts == nil {
		state.Contacts = make(map[string]types.AddressBookContact)
	}
	if state.Groups == nil {
		state.Groups = make(map[string]types.ContactGroup)
	}
	return state, nil
}

func saveContactState(owner types.Uid, state *contactState) error {
	raw, err := marshalPersistentJSON(state)
	if err != nil {
		return err
	}
	return PCache.Upsert(contactStatePrefix+owner.String(), raw, false)
}

func validContactStatus(status types.ContactStatus) bool {
	return status == types.ContactPending || status == types.ContactAccepted || status == types.ContactBlocked
}

func validContactGroupID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, ch := range id {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '-' || ch == '_') {
			return false
		}
	}
	return true
}

func normalizeContactGroups(groups []string, known map[string]types.ContactGroup) ([]string, error) {
	seen := make(map[string]bool, len(groups))
	out := make([]string, 0, len(groups))
	for _, group := range groups {
		if _, ok := known[group]; !ok {
			return nil, types.ErrNotFound
		}
		if !seen[group] {
			seen[group] = true
			out = append(out, group)
		}
	}
	sort.Strings(out)
	return out, nil
}

func appendContactEvent(state *contactState, eventType, id string, now time.Time) {
	state.Events = append(state.Events, types.ContactEvent{
		Version: state.Version,
		Type:    eventType,
		Id:      id,
		At:      now,
	})
	if len(state.Events) > maxContactEvents {
		state.Events = append([]types.ContactEvent(nil), state.Events[len(state.Events)-maxContactEvents:]...)
	}
}

// Apply 创建、更新或删除联系人及分组。
func (m *contactMapper) Apply(owner types.Uid, mutation types.ContactMutation) (*types.ContactSnapshot, error) {
	if owner.IsZero() {
		return nil, types.ErrPermissionDenied
	}
	op := strings.ToLower(mutation.Op)
	peer := types.ZeroUid
	if op == "request_friend" || op == "accept_friend" || op == "remove_friend" {
		peer = types.ParseUserId(mutation.User)
		if peer.IsZero() && mutation.Contact != nil {
			peer = types.ParseUserId(mutation.Contact.User)
		}
		if peer.IsZero() || peer == owner {
			return nil, types.ErrMalformed
		}
	}
	first, second := owner, peer
	if !second.IsZero() && first.Compare(second) > 0 {
		first, second = second, first
	}
	firstLock := m.userLock(first)
	firstLock.Lock()
	defer firstLock.Unlock()
	if !second.IsZero() {
		secondLock := m.userLock(second)
		secondLock.Lock()
		defer secondLock.Unlock()
	}

	state, err := loadContactState(owner)
	if err != nil {
		return nil, err
	}
	now := types.TimeNow()
	state.Version++
	var peerState *contactState

	switch op {
	case "upsert_contact":
		if mutation.Contact == nil || types.ParseUserId(mutation.Contact.User).IsZero() ||
			mutation.Contact.User == owner.UserId() || !validContactStatus(mutation.Contact.Status) ||
			len(mutation.Contact.Alias) > 256 || !utf8.ValidString(mutation.Contact.Alias) ||
			len(mutation.Contact.Groups) > 32 {
			return nil, types.ErrMalformed
		}
		groups, err := normalizeContactGroups(mutation.Contact.Groups, state.Groups)
		if err != nil {
			return nil, err
		}
		contact := *mutation.Contact
		if old, ok := state.Contacts[contact.User]; ok {
			contact.CreatedAt = old.CreatedAt
		} else {
			contact.CreatedAt = now
		}
		contact.Groups = groups
		contact.Alias = strings.TrimSpace(contact.Alias)
		contact.Request = ""
		contact.UpdatedAt = now
		contact.Version = state.Version
		state.Contacts[contact.User] = contact
		appendContactEvent(state, "contact.upsert", contact.User, now)

	case "delete_contact":
		if mutation.User == "" {
			return nil, types.ErrMalformed
		}
		if _, ok := state.Contacts[mutation.User]; !ok {
			return nil, types.ErrNotFound
		}
		delete(state.Contacts, mutation.User)
		appendContactEvent(state, "contact.delete", mutation.User, now)

	case "upsert_group":
		if mutation.Group == nil || !validContactGroupID(mutation.Group.Id) ||
			strings.TrimSpace(mutation.Group.Name) == "" || len(mutation.Group.Name) > 256 ||
			!utf8.ValidString(mutation.Group.Name) ||
			(len(state.Groups) >= 100 && state.Groups[mutation.Group.Id].Id == "") {
			return nil, types.ErrMalformed
		}
		group := *mutation.Group
		if old, ok := state.Groups[group.Id]; ok {
			group.CreatedAt = old.CreatedAt
		} else {
			group.CreatedAt = now
		}
		group.Name = strings.TrimSpace(group.Name)
		group.UpdatedAt = now
		group.Version = state.Version
		state.Groups[group.Id] = group
		appendContactEvent(state, "group.upsert", group.Id, now)

	case "delete_group":
		if !validContactGroupID(mutation.GroupId) {
			return nil, types.ErrMalformed
		}
		if _, ok := state.Groups[mutation.GroupId]; !ok {
			return nil, types.ErrNotFound
		}
		delete(state.Groups, mutation.GroupId)
		for user, contact := range state.Contacts {
			filtered := contact.Groups[:0]
			for _, group := range contact.Groups {
				if group != mutation.GroupId {
					filtered = append(filtered, group)
				}
			}
			if len(filtered) != len(contact.Groups) {
				contact.Groups = append([]string(nil), filtered...)
				contact.UpdatedAt = now
				contact.Version = state.Version
				state.Contacts[user] = contact
			}
		}
		appendContactEvent(state, "group.delete", mutation.GroupId, now)

	case "request_friend":
		peerState, err = loadContactState(peer)
		if err != nil {
			return nil, err
		}
		if existing, ok := state.Contacts[peer.UserId()]; ok && existing.Status == types.ContactBlocked {
			return nil, types.ErrPermissionDenied
		}
		if existing, ok := peerState.Contacts[owner.UserId()]; ok && existing.Status == types.ContactBlocked {
			return nil, types.ErrPermissionDenied
		}
		peerState.Version++
		outgoing := state.Contacts[peer.UserId()]
		outgoing.User = peer.UserId()
		outgoing.Status = types.ContactPending
		outgoing.Request = "outgoing"
		if outgoing.CreatedAt.IsZero() {
			outgoing.CreatedAt = now
		}
		outgoing.UpdatedAt = now
		outgoing.Version = state.Version
		state.Contacts[outgoing.User] = outgoing

		incoming := peerState.Contacts[owner.UserId()]
		incoming.User = owner.UserId()
		incoming.Status = types.ContactPending
		incoming.Request = "incoming"
		if incoming.CreatedAt.IsZero() {
			incoming.CreatedAt = now
		}
		incoming.UpdatedAt = now
		incoming.Version = peerState.Version
		peerState.Contacts[incoming.User] = incoming
		appendContactEvent(state, "friend.request", outgoing.User, now)
		appendContactEvent(peerState, "friend.incoming", incoming.User, now)

	case "accept_friend":
		peerState, err = loadContactState(peer)
		if err != nil {
			return nil, err
		}
		incoming, ok := state.Contacts[peer.UserId()]
		outgoing, peerOK := peerState.Contacts[owner.UserId()]
		if !ok || !peerOK || incoming.Status != types.ContactPending ||
			incoming.Request != "incoming" || outgoing.Status != types.ContactPending ||
			outgoing.Request != "outgoing" {
			return nil, types.ErrPermissionDenied
		}
		peerState.Version++
		incoming.Status, incoming.Request = types.ContactAccepted, ""
		incoming.UpdatedAt, incoming.Version = now, state.Version
		state.Contacts[incoming.User] = incoming
		outgoing.Status, outgoing.Request = types.ContactAccepted, ""
		outgoing.UpdatedAt, outgoing.Version = now, peerState.Version
		peerState.Contacts[outgoing.User] = outgoing
		appendContactEvent(state, "friend.accept", incoming.User, now)
		appendContactEvent(peerState, "friend.accept", outgoing.User, now)

	case "remove_friend":
		peerState, err = loadContactState(peer)
		if err != nil {
			return nil, err
		}
		if _, ok := state.Contacts[peer.UserId()]; !ok {
			return nil, types.ErrNotFound
		}
		peerState.Version++
		delete(state.Contacts, peer.UserId())
		delete(peerState.Contacts, owner.UserId())
		appendContactEvent(state, "friend.remove", peer.UserId(), now)
		appendContactEvent(peerState, "friend.remove", owner.UserId(), now)

	default:
		return nil, types.ErrMalformed
	}

	if peerState != nil {
		if err = saveContactState(peer, peerState); err != nil {
			return nil, err
		}
	}
	if err = saveContactState(owner, state); err != nil {
		return nil, err
	}
	return snapshotContactState(state, 0, 0), nil
}

// Get 返回全量快照，或返回指定版本之后的变更及受影响对象。
func (m *contactMapper) Get(owner types.Uid, query types.ContactQuery) (*types.ContactSnapshot, error) {
	if owner.IsZero() {
		return nil, types.ErrPermissionDenied
	}
	lock := m.userLock(owner)
	lock.Lock()
	state, err := loadContactState(owner)
	lock.Unlock()
	if err != nil {
		return nil, err
	}
	result := snapshotContactState(state, query.Since, query.Limit)
	if query.Recommendations {
		result.Recommendations = m.recommend(owner, state)
	}
	return result, nil
}

func snapshotContactState(state *contactState, since uint64, limit int) *types.ContactSnapshot {
	out := &types.ContactSnapshot{Version: state.Version}
	if since == 0 || since > state.Version ||
		(len(state.Events) > 0 && since < state.Events[0].Version-1) {
		out.Reset = since != 0
		for _, contact := range state.Contacts {
			out.Contacts = append(out.Contacts, contact)
		}
		for _, group := range state.Groups {
			out.Groups = append(out.Groups, group)
		}
	} else {
		changedContacts := make(map[string]bool)
		changedGroups := make(map[string]bool)
		for _, event := range state.Events {
			if event.Version <= since {
				continue
			}
			if limit > 0 && len(out.Events) >= limit {
				break
			}
			out.Events = append(out.Events, event)
			if strings.HasPrefix(event.Type, "contact.") {
				changedContacts[event.Id] = true
			} else {
				changedGroups[event.Id] = true
			}
		}
		if len(out.Events) > 0 {
			out.Version = out.Events[len(out.Events)-1].Version
		}
		for id := range changedContacts {
			if contact, ok := state.Contacts[id]; ok {
				out.Contacts = append(out.Contacts, contact)
			}
		}
		for id := range changedGroups {
			if group, ok := state.Groups[id]; ok {
				out.Groups = append(out.Groups, group)
			}
		}
	}
	sort.Slice(out.Contacts, func(i, j int) bool { return out.Contacts[i].User < out.Contacts[j].User })
	sort.Slice(out.Groups, func(i, j int) bool { return out.Groups[i].Id < out.Groups[j].Id })
	return out
}

func (m *contactMapper) recommend(owner types.Uid, state *contactState) []types.ContactRecommendation {
	direct := map[string]bool{owner.UserId(): true}
	for user := range state.Contacts {
		direct[user] = true
	}
	counts := make(map[string]int)
	var friends []string
	for user, contact := range state.Contacts {
		if contact.Status == types.ContactAccepted {
			friends = append(friends, user)
		}
	}
	sort.Strings(friends)
	if len(friends) > 100 {
		friends = friends[:100]
	}
	for _, user := range friends {
		friend, err := loadContactState(types.ParseUserId(user))
		if err != nil {
			continue
		}
		for candidate, relation := range friend.Contacts {
			if relation.Status == types.ContactAccepted && !direct[candidate] {
				counts[candidate]++
			}
		}
	}
	out := make([]types.ContactRecommendation, 0, len(counts))
	for user, count := range counts {
		out = append(out, types.ContactRecommendation{User: user, MutualFriends: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MutualFriends == out[j].MutualFriends {
			return out[i].User < out[j].User
		}
		return out[i].MutualFriends > out[j].MutualFriends
	})
	if len(out) > 20 {
		out = out[:20]
	}
	return out
}
