package server

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
	translation "chat/server/translate"
)

type persistentTranslationSettingsSource struct {
	mu              sync.Mutex
	refreshInterval time.Duration
	nextRefresh     time.Time
	last            translationSettingsSnapshot
}

func newPersistentTranslationSettingsSource(refreshInterval time.Duration) *persistentTranslationSettingsSource {
	if refreshInterval <= 0 {
		refreshInterval = 5 * time.Second
	}
	fallback := translation.Settings{
		Enabled: true, StaffLanguage: "zh-CN", CustomerLanguage: "en",
		FailurePolicy: "hold", KeepOriginal: true,
	}
	translation.NormalizeSettings(&fallback)
	return &persistentTranslationSettingsSource{
		refreshInterval: refreshInterval,
		last: translationSettingsSnapshot{
			Version: 0, Settings: fallback,
		},
	}
}

func (source *persistentTranslationSettingsSource) Snapshot() (translationSettingsSnapshot, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	now := time.Now()
	if now.Before(source.nextRefresh) {
		return source.last, nil
	}
	source.nextRefresh = now.Add(source.refreshInterval)

	raw, err := store.PCache.Get(adminDocumentKey)
	if err != nil {
		if !errors.Is(err, types.ErrNotFound) {
			logs.Warn.Printf("failed to refresh translation settings from im-admin: %v", err)
		}
		return source.last, nil
	}
	var document struct {
		Version  uint64 `json:"version"`
		Settings struct {
			Translation translation.Settings `json:"translation"`
		} `json:"settings"`
	}
	if err = json.Unmarshal([]byte(raw), &document); err != nil {
		logs.Warn.Printf("failed to decode translation settings from im-admin: %v", err)
		return source.last, nil
	}
	translation.NormalizeSettings(&document.Settings.Translation)
	if err = translation.ValidateSettings(document.Settings.Translation); err != nil {
		logs.Warn.Printf("ignored invalid translation settings from im-admin: %v", err)
		return source.last, nil
	}
	if document.Version >= source.last.Version {
		source.last = translationSettingsSnapshot{
			Version: document.Version, Settings: document.Settings.Translation,
		}
	}
	return source.last, nil
}
