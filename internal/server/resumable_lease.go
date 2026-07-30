package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"chat/server/store"
	"chat/server/store/types"
)

const (
	resumableLeasePrefix = "uploadlease:"
	resumableLeaseTTL    = 2 * time.Minute
)

var errResumableLeaseBusy = errors.New("resumable upload is locked by another node")

type resumableUploadLease struct {
	Owner     string    `json:"owner,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
}

func acquireResumableUploadLease(id string, ttl time.Duration) (string, error) {
	if types.ParseUid(id).IsZero() || ttl <= 0 {
		return "", types.ErrMalformed
	}
	next := resumableUploadLease{Owner: newResumableLeaseOwner(), ExpiresAt: types.TimeNow().Add(ttl)}
	nextRaw, err := json.Marshal(next)
	if err != nil {
		return "", err
	}
	key := resumableLeasePrefix + id
	if err = store.PCache.Upsert(key, string(nextRaw), true); err == nil {
		return string(nextRaw), nil
	} else if !errors.Is(err, types.ErrDuplicate) {
		return "", err
	}

	for attempt := 0; attempt < 4; attempt++ {
		currentRaw, getErr := store.PCache.Get(key)
		if getErr != nil {
			return "", getErr
		}
		var current resumableUploadLease
		if jsonErr := json.Unmarshal([]byte(currentRaw), &current); jsonErr == nil &&
			current.Owner != "" && current.ExpiresAt.After(types.TimeNow()) {
			return "", errResumableLeaseBusy
		}
		swapped, swapErr := store.PCache.CompareAndSwap(key, currentRaw, string(nextRaw))
		if swapErr != nil {
			return "", swapErr
		}
		if swapped {
			return string(nextRaw), nil
		}
	}
	return "", errResumableLeaseBusy
}

func releaseResumableUploadLease(id, ownedRaw string) {
	if id == "" || ownedRaw == "" {
		return
	}
	releasedRaw, err := json.Marshal(resumableUploadLease{ExpiresAt: types.TimeNow()})
	if err != nil {
		return
	}
	_, _ = store.PCache.CompareAndSwap(resumableLeasePrefix+id, ownedRaw, string(releasedRaw))
}

func newResumableLeaseOwner() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	return store.Store.GetUidString()
}
