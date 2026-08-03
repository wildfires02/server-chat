package store

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"chat/server/store/types"

	"github.com/google/uuid"
)

const (
	contactStatePrefix    = "contacts:"
	contactLockPrefix     = "contacts-lock:v1:"
	contactTxnPrefix      = "contacts-txn:v1:"
	contactTxnOwnerPrefix = "contacts-txn-owner:v1:"
	contactRatePrefix     = "contacts-rate:v1:"
	contactDismissPrefix  = "contacts-recommend-dismiss:v1:"
	maxContactEvents      = 1000
	contactLockTTL        = 15 * time.Second
	contactLockWait       = 2 * time.Second
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

type contactLease struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
}

type contactPairTransaction struct {
	ID         string `json:"id"`
	Owner      string `json:"owner"`
	Peer       string `json:"peer"`
	OwnerState string `json:"owner_state"`
	PeerState  string `json:"peer_state"`
	CreatedAt  int64  `json:"created_at"`
}

type contactRateCounter struct {
	Count     int   `json:"count"`
	ExpiresAt int64 `json:"expires_at"`
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

func contactLeaseKey(owner types.Uid) string {
	return contactLockPrefix + owner.String()
}

func contactTxnOwnerKey(owner types.Uid, transactionID string) string {
	return contactTxnOwnerPrefix + owner.String() + ":" + transactionID
}

// acquireContactLeases 使用持久缓存 CAS 创建跨节点用户租约。
// 所有联系人写入都先取得相关用户租约，因此多 Pod 不会覆盖彼此的版本。
func acquireContactLeases(owners ...types.Uid) (func(), error) {
	unique := make(map[types.Uid]struct{}, len(owners))
	ordered := make([]types.Uid, 0, len(owners))
	for _, owner := range owners {
		if owner.IsZero() {
			continue
		}
		if _, exists := unique[owner]; !exists {
			unique[owner] = struct{}{}
			ordered = append(ordered, owner)
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Compare(ordered[j]) < 0 })
	token := uuid.NewString()
	acquired := make(map[string]string, len(ordered))
	deadline := time.Now().Add(contactLockWait)
	release := func() {
		for key, ownedRaw := range acquired {
			expiredRaw, _ := json.Marshal(contactLease{ExpiresAt: 0})
			_, _ = PCache.CompareAndSwap(key, ownedRaw, string(expiredRaw))
		}
	}
	for _, owner := range ordered {
		key := contactLeaseKey(owner)
		for {
			now := time.Now()
			if now.After(deadline) {
				release()
				return nil, types.ErrInternal
			}
			leaseRaw, _ := json.Marshal(contactLease{Token: token, ExpiresAt: now.Add(contactLockTTL).UnixMilli()})
			oldRaw, err := PCache.Get(key)
			if errors.Is(err, types.ErrNotFound) {
				if err = PCache.Upsert(key, string(leaseRaw), true); err == nil {
					acquired[key] = string(leaseRaw)
					break
				}
				if !errors.Is(err, types.ErrDuplicate) {
					release()
					return nil, err
				}
			} else if err != nil {
				release()
				return nil, err
			} else {
				var current contactLease
				if json.Unmarshal([]byte(oldRaw), &current) != nil || current.ExpiresAt <= now.UnixMilli() {
					swapped, swapErr := PCache.CompareAndSwap(key, oldRaw, string(leaseRaw))
					if swapErr != nil {
						release()
						return nil, swapErr
					}
					if swapped {
						acquired[key] = string(leaseRaw)
						break
					}
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	return release, nil
}

func marshalContactState(state *contactState) (string, error) {
	return marshalPersistentJSON(state)
}

// recoverContactTransactions 补齐进程崩溃或数据库瞬时故障留下的双边联系人事务。
func recoverContactTransactions(owner types.Uid) error {
	entries, err := PCache.List(contactTxnOwnerPrefix+owner.String()+":", 100)
	if err != nil {
		return err
	}
	for indexKey, txnKey := range entries {
		raw, loadErr := PCache.Get(txnKey)
		if errors.Is(loadErr, types.ErrNotFound) {
			_ = PCache.Delete(indexKey)
			continue
		}
		if loadErr != nil {
			return loadErr
		}
		var transaction contactPairTransaction
		if json.Unmarshal([]byte(raw), &transaction) != nil {
			return types.ErrMalformed
		}
		first := types.ParseUserId(transaction.Owner)
		second := types.ParseUserId(transaction.Peer)
		if first.IsZero() || second.IsZero() {
			return types.ErrMalformed
		}
		release, lockErr := acquireContactLeases(first, second)
		if lockErr != nil {
			return lockErr
		}
		// 锁等待期间正常提交可能已经清理事务，因此需要再次确认。
		if _, checkErr := PCache.Get(txnKey); errors.Is(checkErr, types.ErrNotFound) {
			release()
			_ = PCache.Delete(indexKey)
			continue
		}
		if err = PCache.Upsert(contactStatePrefix+transaction.Peer, transaction.PeerState, false); err == nil {
			err = PCache.Upsert(contactStatePrefix+transaction.Owner, transaction.OwnerState, false)
		}
		if err == nil {
			_ = PCache.Delete(contactTxnOwnerKey(first, transaction.ID))
			_ = PCache.Delete(contactTxnOwnerKey(second, transaction.ID))
			_ = PCache.Delete(txnKey)
		}
		release()
		if err != nil {
			return err
		}
	}
	return nil
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
	raw, err := marshalContactState(state)
	if err != nil {
		return err
	}
	return PCache.Upsert(contactStatePrefix+owner.String(), raw, false)
}

func commitContactPair(owner, peer types.Uid, ownerState, peerState *contactState) error {
	ownerRaw, err := marshalContactState(ownerState)
	if err != nil {
		return err
	}
	peerRaw, err := marshalContactState(peerState)
	if err != nil {
		return err
	}
	transaction := contactPairTransaction{
		ID: uuid.NewString(), Owner: owner.UserId(), Peer: peer.UserId(),
		OwnerState: ownerRaw, PeerState: peerRaw, CreatedAt: time.Now().UnixMilli(),
	}
	transactionRaw, err := json.Marshal(transaction)
	if err != nil {
		return err
	}
	transactionKey := contactTxnPrefix + transaction.ID
	ownerIndex := contactTxnOwnerKey(owner, transaction.ID)
	peerIndex := contactTxnOwnerKey(peer, transaction.ID)
	if err = PCache.Upsert(transactionKey, string(transactionRaw), true); err != nil {
		return err
	}
	cleanupBeforeCommit := func() {
		_ = PCache.Delete(ownerIndex)
		_ = PCache.Delete(peerIndex)
		_ = PCache.Delete(transactionKey)
	}
	if err = PCache.Upsert(ownerIndex, transactionKey, true); err != nil {
		cleanupBeforeCommit()
		return err
	}
	if err = PCache.Upsert(peerIndex, transactionKey, true); err != nil {
		cleanupBeforeCommit()
		return err
	}
	// 事务意图与双方恢复索引都持久化后才开始修改业务状态。
	if err = PCache.Upsert(contactStatePrefix+peer.String(), peerRaw, false); err != nil {
		return err
	}
	if err = PCache.Upsert(contactStatePrefix+owner.String(), ownerRaw, false); err != nil {
		return err
	}
	_ = PCache.Delete(ownerIndex)
	_ = PCache.Delete(peerIndex)
	_ = PCache.Delete(transactionKey)
	return nil
}

func validContactStatus(status types.ContactStatus) bool {
	return status == types.ContactPending || status == types.ContactAccepted || status == types.ContactBlocked
}

func consumeContactRateCounter(key string, maximum int, expiresAt time.Time) error {
	for attempt := 0; attempt < 32; attempt++ {
		raw, err := PCache.Get(key)
		if errors.Is(err, types.ErrNotFound) {
			counterRaw, _ := json.Marshal(contactRateCounter{Count: 1, ExpiresAt: expiresAt.Unix()})
			if err = PCache.Upsert(key, string(counterRaw), true); err == nil {
				return nil
			}
			if errors.Is(err, types.ErrDuplicate) {
				continue
			}
			return err
		}
		if err != nil {
			return err
		}
		var counter contactRateCounter
		if json.Unmarshal([]byte(raw), &counter) != nil || counter.ExpiresAt <= time.Now().Unix() {
			counter = contactRateCounter{ExpiresAt: expiresAt.Unix()}
		}
		if counter.Count >= maximum {
			return types.ErrPolicy
		}
		counter.Count++
		updated, _ := json.Marshal(counter)
		swapped, swapErr := PCache.CompareAndSwap(key, raw, string(updated))
		if swapErr != nil {
			return swapErr
		}
		if swapped {
			return nil
		}
	}
	return types.ErrVersionConflict
}

func consumeFriendRequestQuota(owner, peer types.Uid, now time.Time) error {
	hour := now.UTC().Format("2006010215")
	day := now.UTC().Format("20060102")
	if err := consumeContactRateCounter(contactRatePrefix+owner.String()+":hour:"+hour,
		20, now.UTC().Truncate(time.Hour).Add(time.Hour)); err != nil {
		return err
	}
	if err := consumeContactRateCounter(contactRatePrefix+owner.String()+":day:"+day,
		100, now.UTC().Truncate(24*time.Hour).Add(24*time.Hour)); err != nil {
		return err
	}
	return consumeContactRateCounter(contactRatePrefix+owner.String()+":target:"+peer.String()+":"+day,
		3, now.UTC().Truncate(24*time.Hour).Add(24*time.Hour))
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
	if op == "request_friend" || op == "accept_friend" || op == "remove_friend" ||
		op == "reject_friend" || op == "block_contact" || op == "unblock_contact" ||
		op == "dismiss_recommendation" {
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
	if err := recoverContactTransactions(owner); err != nil {
		return nil, err
	}
	if !peer.IsZero() {
		if err := recoverContactTransactions(peer); err != nil {
			return nil, err
		}
	}
	firstLock := m.userLock(first)
	firstLock.Lock()
	defer firstLock.Unlock()
	if !second.IsZero() {
		secondLock := m.userLock(second)
		secondLock.Lock()
		defer secondLock.Unlock()
	}
	releaseLeases, err := acquireContactLeases(first, second)
	if err != nil {
		return nil, err
	}
	defer releaseLeases()

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
		if err = consumeFriendRequestQuota(owner, peer, now); err != nil {
			return nil, err
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

	case "reject_friend":
		peerState, err = loadContactState(peer)
		if err != nil {
			return nil, err
		}
		incoming, ok := state.Contacts[peer.UserId()]
		outgoing, peerOK := peerState.Contacts[owner.UserId()]
		if !ok || !peerOK || incoming.Status != types.ContactPending || incoming.Request != "incoming" ||
			outgoing.Status != types.ContactPending || outgoing.Request != "outgoing" {
			return nil, types.ErrPermissionDenied
		}
		peerState.Version++
		delete(state.Contacts, peer.UserId())
		delete(peerState.Contacts, owner.UserId())
		appendContactEvent(state, "friend.reject", peer.UserId(), now)
		appendContactEvent(peerState, "friend.rejected", owner.UserId(), now)

	case "block_contact":
		peerState, err = loadContactState(peer)
		if err != nil {
			return nil, err
		}
		peerState.Version++
		blocked := state.Contacts[peer.UserId()]
		blocked.User = peer.UserId()
		blocked.Status = types.ContactBlocked
		blocked.Request = ""
		if blocked.CreatedAt.IsZero() {
			blocked.CreatedAt = now
		}
		blocked.UpdatedAt = now
		blocked.Version = state.Version
		state.Contacts[peer.UserId()] = blocked
		delete(peerState.Contacts, owner.UserId())
		appendContactEvent(state, "contact.block", peer.UserId(), now)
		appendContactEvent(peerState, "friend.remove", owner.UserId(), now)

	case "unblock_contact":
		blocked, ok := state.Contacts[peer.UserId()]
		if !ok || blocked.Status != types.ContactBlocked {
			return nil, types.ErrNotFound
		}
		delete(state.Contacts, peer.UserId())
		appendContactEvent(state, "contact.unblock", peer.UserId(), now)

	case "dismiss_recommendation":
		if err = PCache.Upsert(contactDismissPrefix+owner.String()+":"+peer.String(),
			strconv.FormatInt(now.Add(30*24*time.Hour).Unix(), 10), false); err != nil {
			return nil, err
		}
		appendContactEvent(state, "recommendation.dismiss", peer.UserId(), now)

	default:
		return nil, types.ErrMalformed
	}

	if peerState != nil {
		if err = commitContactPair(owner, peer, state, peerState); err != nil {
			return nil, err
		}
	} else if err = saveContactState(owner, state); err != nil {
		return nil, err
	}
	return snapshotContactState(state, 0, 0), nil
}

// Get 返回全量快照，或返回指定版本之后的变更及受影响对象。
func (m *contactMapper) Get(owner types.Uid, query types.ContactQuery) (*types.ContactSnapshot, error) {
	if owner.IsZero() {
		return nil, types.ErrPermissionDenied
	}
	if err := recoverContactTransactions(owner); err != nil {
		return nil, err
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
			if strings.HasPrefix(event.Type, "contact.") || strings.HasPrefix(event.Type, "friend.") ||
				strings.HasPrefix(event.Type, "recommendation.") {
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
		candidate := types.ParseUserId(user)
		if candidate.IsZero() {
			continue
		}
		if raw, err := PCache.Get(contactDismissPrefix + owner.String() + ":" + candidate.String()); err == nil {
			expiresAt, _ := strconv.ParseInt(raw, 10, 64)
			if expiresAt > time.Now().Unix() {
				continue
			}
			_ = PCache.Delete(contactDismissPrefix + owner.String() + ":" + candidate.String())
		}
		candidateState, err := loadContactState(candidate)
		if err != nil {
			continue
		}
		if relation, exists := candidateState.Contacts[owner.UserId()]; exists && relation.Status == types.ContactBlocked {
			continue
		}
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
