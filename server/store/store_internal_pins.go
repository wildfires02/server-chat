package store

import (
	"encoding/base64"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"chat/server/store/types"
)

const (
	internalPinCachePrefix      = "workspace:pins:v1:"
	internalPinCASAttempts      = 64
	internalPinDefaultSyncLimit = 200
	internalPinMaxSyncLimit     = 500
	internalPinMaxTombstones    = 300
)

var internalPinKindLimits = map[types.InternalPinKind]int{
	types.InternalPinCustomer:     100,
	types.InternalPinConversation: 100,
	types.InternalPinMessage:      300,
}

//InternalPinPersistenceInterface獨立地持續員工私人工作區
// 来自公共主题和消息元数据。
type InternalPinPersistenceInterface interface {
	Apply(org string, owner types.Uid, mutation types.InternalPinMutation) (*types.InternalPin, bool, error)
	Query(org string, owner types.Uid, query types.InternalPinQuery) (*types.InternalPinSnapshot, error)
}

type internalPinMapper struct{}

type internalPinWorkspace struct {
	Version     uint64                       `json:"version"`
	ResetBefore uint64                       `json:"reset_before,omitempty"`
	Pins        map[string]types.InternalPin `json:"pins"`
}

//InternalPins是全流程专用工作区映射器。
var InternalPins InternalPinPersistenceInterface

func internalPinWorkspaceKey(org string, owner types.Uid) string {
	encodedOrg := base64.RawURLEncoding.EncodeToString([]byte(org))
	return internalPinCachePrefix + encodedOrg + ":" + owner.String()
}

func validateInternalPinScope(org string, owner types.Uid) error {
	if owner.IsZero() {
		return types.ErrMalformed
	}
	org = strings.TrimSpace(org)
	if org == "" || len(org) > 128 {
		return types.ErrMalformed
	}
	return nil
}

func internalPinTarget(mutation types.InternalPinMutation) (string, error) {
	if mutation.Rank < -1_000_000_000 || mutation.Rank > 1_000_000_000 {
		return "", types.ErrPolicy
	}
	switch mutation.Kind {
	case types.InternalPinCustomer:
		if types.ParseUserId(mutation.CustomerUID).IsZero() {
			return "", types.ErrMalformed
		}
		return string(mutation.Kind) + ":" + mutation.CustomerUID, nil
	case types.InternalPinConversation:
		if mutation.Topic == "" || len(mutation.Topic) > 160 {
			return "", types.ErrMalformed
		}
		return string(mutation.Kind) + ":" + mutation.Topic, nil
	case types.InternalPinMessage:
		if mutation.Topic == "" || len(mutation.Topic) > 160 || mutation.SeqID <= 0 {
			return "", types.ErrMalformed
		}
		return string(mutation.Kind) + ":" + mutation.Topic + ":" + strconv.Itoa(mutation.SeqID), nil
	default:
		return "", types.ErrMalformed
	}
}

func loadInternalPinWorkspace(key string) (internalPinWorkspace, string, bool, error) {
	raw, err := PCache.Get(key)
	if errors.Is(err, types.ErrNotFound) {
		return internalPinWorkspace{Pins: make(map[string]types.InternalPin)}, "", false, nil
	}
	if err != nil {
		return internalPinWorkspace{}, "", false, err
	}
	var workspace internalPinWorkspace
	if err = unmarshalPersistentJSON(raw, &workspace); err != nil {
		return internalPinWorkspace{}, "", false, err
	}
	if workspace.Pins == nil {
		workspace.Pins = make(map[string]types.InternalPin)
	}
	return workspace, raw, true, nil
}

func (internalPinMapper) Apply(org string, owner types.Uid,
	mutation types.InternalPinMutation) (*types.InternalPin, bool, error) {
	org = strings.TrimSpace(org)
	if err := validateInternalPinScope(org, owner); err != nil {
		return nil, false, err
	}
	if mutation.Op != types.InternalPinUpsert && mutation.Op != types.InternalPinDelete {
		return nil, false, types.ErrMalformed
	}
	if len(mutation.Actor) > 160 || len(mutation.RequestID) > 128 {
		return nil, false, types.ErrMalformed
	}
	targetKey, err := internalPinTarget(mutation)
	if err != nil {
		return nil, false, err
	}
	key := internalPinWorkspaceKey(org, owner)

	for attempt := 0; attempt < internalPinCASAttempts; attempt++ {
		workspace, oldRaw, exists, err := loadInternalPinWorkspace(key)
		if err != nil {
			return nil, false, err
		}
		current, found := workspace.Pins[targetKey]

		//语义无操作使重試安全，即使呼叫者仍然有
		//突变前版本。
		if mutation.Op == types.InternalPinDelete && (!found || current.State == types.InternalPinDeleted) {
			if !found {
				current = pinFromMutation(targetKey, mutation)
				current.State = types.InternalPinDeleted
			}
			return &current, false, nil
		}
		if mutation.Op == types.InternalPinUpsert && found &&
			current.State == types.InternalPinActive && current.Rank == mutation.Rank {
			return &current, false, nil
		}

		currentVersion := uint64(0)
		if found {
			currentVersion = current.Version
		}
		if currentVersion != mutation.ExpectedVersion {
			return nil, false, types.ErrVersionConflict
		}
		if mutation.Op == types.InternalPinUpsert && (!found || current.State != types.InternalPinActive) &&
			activeInternalPinCount(workspace.Pins, mutation.Kind) >= internalPinKindLimits[mutation.Kind] {
			return nil, false, types.ErrPolicy
		}

		now := time.Now().UTC()
		next := pinFromMutation(targetKey, mutation)
		workspace.Version++
		next.Version = workspace.Version
		next.UpdatedAt = now
		if mutation.Op == types.InternalPinDelete {
			next.State = types.InternalPinDeleted
			next.PinnedAt = current.PinnedAt
			next.DeletedAt = &now
		} else {
			next.State = types.InternalPinActive
			if found && current.State == types.InternalPinActive {
				next.PinnedAt = current.PinnedAt
			} else {
				next.PinnedAt = now
			}
		}
		workspace.Pins[targetKey] = next
		pruneInternalPinTombstones(&workspace)

		newRaw, err := marshalInternalPinWorkspace(&workspace)
		if err != nil {
			return nil, false, err
		}
		if !exists {
			err = PCache.Upsert(key, newRaw, true)
			if errors.Is(err, types.ErrDuplicate) {
				continue
			}
			if err != nil {
				return nil, false, err
			}
			return &next, true, nil
		}
		swapped, err := PCache.CompareAndSwap(key, oldRaw, newRaw)
		if err != nil {
			return nil, false, err
		}
		if swapped {
			return &next, true, nil
		}
	}
	return nil, false, types.ErrVersionConflict
}

func pinFromMutation(targetKey string, mutation types.InternalPinMutation) types.InternalPin {
	return types.InternalPin{
		TargetKey: targetKey, Kind: mutation.Kind, CustomerUID: mutation.CustomerUID,
		Topic: mutation.Topic, SeqID: mutation.SeqID, Rank: mutation.Rank,
		Actor: mutation.Actor, RequestID: mutation.RequestID,
	}
}

func activeInternalPinCount(pins map[string]types.InternalPin, kind types.InternalPinKind) int {
	count := 0
	for _, pin := range pins {
		if pin.Kind == kind && pin.State == types.InternalPinActive {
			count++
		}
	}
	return count
}

func pruneInternalPinTombstones(workspace *internalPinWorkspace) {
	var tombstones []types.InternalPin
	for _, pin := range workspace.Pins {
		if pin.State == types.InternalPinDeleted {
			tombstones = append(tombstones, pin)
		}
	}
	sort.Slice(tombstones, func(i, j int) bool {
		return tombstones[i].Version < tombstones[j].Version
	})
	for len(tombstones) > internalPinMaxTombstones {
		pruned := tombstones[0]
		delete(workspace.Pins, pruned.TargetKey)
		if pruned.Version > workspace.ResetBefore {
			workspace.ResetBefore = pruned.Version
		}
		tombstones = tombstones[1:]
	}
}

func marshalInternalPinWorkspace(workspace *internalPinWorkspace) (string, error) {
	for {
		raw, err := marshalPersistentJSON(workspace)
		if !errors.Is(err, types.ErrPolicy) {
			return raw, err
		}
		var oldest *types.InternalPin
		for _, candidate := range workspace.Pins {
			if candidate.State == types.InternalPinDeleted &&
				(oldest == nil || candidate.Version < oldest.Version) {
				copy := candidate
				oldest = &copy
			}
		}
		if oldest == nil {
			return "", types.ErrPolicy
		}
		delete(workspace.Pins, oldest.TargetKey)
		if oldest.Version > workspace.ResetBefore {
			workspace.ResetBefore = oldest.Version
		}
	}
}

func (internalPinMapper) Query(org string, owner types.Uid,
	query types.InternalPinQuery) (*types.InternalPinSnapshot, error) {
	org = strings.TrimSpace(org)
	if err := validateInternalPinScope(org, owner); err != nil {
		return nil, err
	}
	workspace, _, _, err := loadInternalPinWorkspace(internalPinWorkspaceKey(org, owner))
	if err != nil {
		return nil, err
	}
	limit := query.Limit
	if limit <= 0 {
		limit = internalPinDefaultSyncLimit
	} else if limit > internalPinMaxSyncLimit {
		limit = internalPinMaxSyncLimit
	}
	reset := query.Since == 0 || query.Since < workspace.ResetBefore || query.Since > workspace.Version
	snapshot := &types.InternalPinSnapshot{
		Version: workspace.Version, NextSince: workspace.Version, Reset: reset,
		Pins: make([]types.InternalPin, 0),
	}
	if reset {
		for _, pin := range workspace.Pins {
			if pin.State == types.InternalPinActive {
				snapshot.Pins = append(snapshot.Pins, pin)
			}
		}
		sort.Slice(snapshot.Pins, func(i, j int) bool {
			if snapshot.Pins[i].Kind != snapshot.Pins[j].Kind {
				return snapshot.Pins[i].Kind < snapshot.Pins[j].Kind
			}
			if snapshot.Pins[i].Rank != snapshot.Pins[j].Rank {
				return snapshot.Pins[i].Rank < snapshot.Pins[j].Rank
			}
			return snapshot.Pins[i].PinnedAt.After(snapshot.Pins[j].PinnedAt)
		})
		return snapshot, nil
	}

	for _, pin := range workspace.Pins {
		if pin.Version > query.Since {
			snapshot.Pins = append(snapshot.Pins, pin)
		}
	}
	sort.Slice(snapshot.Pins, func(i, j int) bool {
		return snapshot.Pins[i].Version < snapshot.Pins[j].Version
	})
	if len(snapshot.Pins) > limit {
		snapshot.Pins = snapshot.Pins[:limit]
		snapshot.HasMore = true
	}
	if snapshot.HasMore {
		snapshot.NextSince = snapshot.Pins[len(snapshot.Pins)-1].Version
	}
	return snapshot, nil
}
