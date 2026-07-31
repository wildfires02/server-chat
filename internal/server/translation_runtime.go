package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unicode"

	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
	translation "chat/server/translate"
)

const (
	translationCachePrefix = "translation:v1:"
	translationQueueSize   = 1024
	translationWorkerCount = 8
	translationMemoryLimit = 10000
)

type cachedTranslation struct {
	Text                   string `json:"text"`
	DetectedSourceLanguage string `json:"detected_source_language,omitempty"`
	TargetLanguage         string `json:"target_language"`
	Provider               string `json:"provider"`
}

type translationTask struct {
	key     string
	request translation.Request
	router  *translation.Router
}

type translationCompletion struct {
	entry cachedTranslation
	err   error
}

type translationRuntime struct {
	mu          sync.Mutex
	source      translationSettingsSource
	initialized bool
	version     uint64
	settings    translation.Settings
	router      *translation.Router
	routerErr   error
	cache       map[string]cachedTranslation
	inflight    map[string][]func(translationCompletion)
	queue       chan translationTask
}

type translationSettingsSnapshot struct {
	Version  uint64
	Settings translation.Settings
}

type translationSettingsSource interface {
	Snapshot() (translationSettingsSnapshot, error)
}

func newTranslationRuntime(source translationSettingsSource) *translationRuntime {
	runtime := &translationRuntime{
		source:   source,
		cache:    make(map[string]cachedTranslation),
		inflight: make(map[string][]func(translationCompletion)),
		queue:    make(chan translationTask, translationQueueSize),
	}
	for range translationWorkerCount {
		go runtime.worker()
	}
	return runtime
}

func (runtime *translationRuntime) configuration() (translation.Settings,
	*translation.Router, error) {
	if runtime.source == nil {
		return translation.Settings{}, nil, nil
	}
	snapshot, sourceErr := runtime.source.Snapshot()
	runtime.mu.Lock()
	if runtime.initialized && runtime.version == snapshot.Version {
		settings, router, err := runtime.settings, runtime.router, runtime.routerErr
		runtime.mu.Unlock()
		if sourceErr != nil {
			return settings, router, sourceErr
		}
		return settings, router, err
	}
	runtime.mu.Unlock()

	settings := snapshot.Settings
	var router *translation.Router
	var err error
	if settings.Enabled {
		if err = translation.ValidateSettings(settings); err == nil {
			router, err = translation.NewRouter(settings.RouterConfig(),
				translation.FileSecretResolver, nil)
		}
	}
	if sourceErr != nil {
		err = sourceErr
	}

	runtime.mu.Lock()
	if !runtime.initialized || snapshot.Version >= runtime.version {
		runtime.initialized = true
		runtime.version = snapshot.Version
		runtime.settings = settings
		runtime.router = router
		runtime.routerErr = err
	}
	settings, router, err = runtime.settings, runtime.router, runtime.routerErr
	runtime.mu.Unlock()
	return settings, router, err
}

// project 为每个接收者生成翻译视图。缓存未命中时立即返回处理中数据包，
// 并在翻译完成后通过 deliver 回调发送最终数据包。
func (runtime *translationRuntime) project(topic string, data *MsgServerData, receiverLanguage string,
	receiverIsSender bool, deliver func(*MsgServerData)) (*MsgServerData, func()) {
	if runtime == nil || data == nil {
		return data, nil
	}
	settings, router, err := runtime.configuration()
	if !settings.Enabled {
		return data, nil
	}
	text, ok := data.Content.(string)
	if !ok || strings.TrimSpace(text) == "" || (data.Kind != "" && data.Kind != "text") {
		return data, nil
	}
	source := likelyLanguage(text)
	target := translationTarget(settings, source, receiverLanguage)
	if receiverIsSender || sameLanguage(source, target) || target == "" {
		projected := data.copy()
		projected.Translation = &MsgTranslation{
			Status: "original", SourceLanguage: source, TargetLanguage: target,
		}
		return projected, nil
	}
	if router == nil || err != nil {
		if err != nil {
			logs.Warn.Printf("translation configuration unavailable: %v", err)
		}
		return failedTranslationData(data, text, source, target, settings), nil
	}

	key := translationCacheKey(topic, data.SeqId, target, translationPolicyFingerprint(settings), text)
	if entry, found := runtime.loadCache(key); found {
		return completedTranslationData(data, text, settings.KeepOriginal, entry), nil
	}
	pending := data.copy()
	pending.Content = nil
	pending.Translation = &MsgTranslation{
		Status: "pending", SourceLanguage: source, TargetLanguage: target,
	}
	callback := func(completion translationCompletion) {
		if deliver == nil {
			return
		}
		if completion.err != nil {
			deliver(failedTranslationData(data, text, source, target, settings))
			return
		}
		deliver(completedTranslationData(data, text, settings.KeepOriginal, completion.entry))
	}
	start := func() {
		if !runtime.submit(translationTask{
			key: key,
			request: translation.Request{
				Text: text, SourceLanguage: source, TargetLanguage: target,
			},
			router: router,
		}, callback) && deliver != nil {
			deliver(failedTranslationData(data, text, source, target, settings))
		}
	}
	return pending, start
}

func (runtime *translationRuntime) submit(task translationTask,
	callback func(translationCompletion)) bool {
	runtime.mu.Lock()
	if entry, found := runtime.cache[task.key]; found {
		runtime.mu.Unlock()
		if callback != nil {
			callback(translationCompletion{entry: entry})
		}
		return true
	}
	if callbacks, found := runtime.inflight[task.key]; found {
		runtime.inflight[task.key] = append(callbacks, callback)
		runtime.mu.Unlock()
		return true
	}
	runtime.inflight[task.key] = []func(translationCompletion){callback}
	runtime.mu.Unlock()
	select {
	case runtime.queue <- task:
		return true
	default:
		runtime.mu.Lock()
		delete(runtime.inflight, task.key)
		runtime.mu.Unlock()
		return false
	}
}

func (runtime *translationRuntime) worker() {
	for task := range runtime.queue {
		result, err := task.router.Translate(context.Background(), task.request)
		completion := translationCompletion{err: err}
		if err == nil {
			completion.entry = cachedTranslation{
				Text: result.Text, DetectedSourceLanguage: result.DetectedSourceLanguage,
				TargetLanguage: task.request.TargetLanguage, Provider: result.Provider,
			}
			runtime.saveCache(task.key, completion.entry)
		} else {
			logs.Warn.Printf("automatic translation failed: %v", err)
		}
		runtime.mu.Lock()
		callbacks := runtime.inflight[task.key]
		delete(runtime.inflight, task.key)
		runtime.mu.Unlock()
		for _, callback := range callbacks {
			callback(completion)
		}
	}
}

func (runtime *translationRuntime) loadCache(key string) (cachedTranslation, bool) {
	runtime.mu.Lock()
	entry, found := runtime.cache[key]
	runtime.mu.Unlock()
	if found {
		return entry, true
	}
	raw, err := store.PCache.Get(key)
	if err != nil {
		return cachedTranslation{}, false
	}
	if json.Unmarshal([]byte(raw), &entry) != nil || entry.Text == "" {
		return cachedTranslation{}, false
	}
	runtime.mu.Lock()
	runtime.putMemoryCache(key, entry)
	runtime.mu.Unlock()
	return entry, true
}

func (runtime *translationRuntime) saveCache(key string, entry cachedTranslation) {
	runtime.mu.Lock()
	runtime.putMemoryCache(key, entry)
	runtime.mu.Unlock()
	raw, err := json.Marshal(entry)
	if err == nil {
		if err = store.PCache.Upsert(key, string(raw), false); err != nil {
			logs.Warn.Printf("failed to persist translation cache: %v", err)
		}
	}
}

func (runtime *translationRuntime) putMemoryCache(key string, entry cachedTranslation) {
	if _, found := runtime.cache[key]; !found && len(runtime.cache) >= translationMemoryLimit {
		for oldest := range runtime.cache {
			delete(runtime.cache, oldest)
			break
		}
	}
	runtime.cache[key] = entry
}

func testConfiguredTranslationProvider(ctx context.Context,
	settings translation.Settings, providerID string,
	request translation.Request) (translation.Result, error) {
	settings.Providers = append([]translation.ProviderSettings(nil), settings.Providers...)
	settings.Routes = append([]translation.RouteSettings(nil), settings.Routes...)
	found := false
	for index := range settings.Providers {
		settings.Providers[index].Enabled = settings.Providers[index].ID == providerID
		found = found || settings.Providers[index].ID == providerID
	}
	if !found {
		return translation.Result{}, translation.ErrNoProvider
	}
	settings.Routes = nil
	router, err := translation.NewRouter(settings.RouterConfig(),
		translation.FileSecretResolver, nil)
	if err != nil {
		return translation.Result{}, err
	}
	return router.TranslateWithProvider(ctx, providerID, request)
}

func translationTarget(settings translation.Settings, source, receiver string) string {
	switch {
	case sameLanguage(source, settings.StaffLanguage):
		return settings.CustomerLanguage
	case sameLanguage(source, settings.CustomerLanguage):
		return settings.StaffLanguage
	case receiver != "":
		return receiver
	default:
		return settings.CustomerLanguage
	}
}

func likelyLanguage(text string) string {
	han, latin := 0, 0
	for _, char := range text {
		switch {
		case unicode.Is(unicode.Han, char):
			han++
		case unicode.Is(unicode.Latin, char):
			latin++
		}
	}
	if han > 0 && han >= latin {
		return "zh"
	}
	if latin > 0 {
		return "en"
	}
	return "auto"
}

func sameLanguage(left, right string) bool {
	left = primaryLanguage(left)
	right = primaryLanguage(right)
	return left != "" && left != "auto" && left == right
}

func primaryLanguage(language string) string {
	language = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(language), "_", "-"))
	if index := strings.IndexByte(language, '-'); index >= 0 {
		return language[:index]
	}
	return language
}

func translationPolicyFingerprint(settings translation.Settings) string {
	raw, _ := json.Marshal(settings)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:8])
}

func translationCacheKey(topic string, seq int, target, policy, text string) string {
	digest := sha256.Sum256(fmt.Appendf(nil,
		"%s\x00%d\x00%s\x00%s\x00%s", topic, seq, target, policy, text))
	return translationCachePrefix + hex.EncodeToString(digest[:16])
}

func completedTranslationData(data *MsgServerData, original string, keepOriginal bool,
	entry cachedTranslation) *MsgServerData {
	projected := data.copy()
	projected.Content = entry.Text
	projected.Translation = &MsgTranslation{
		Status: "completed", SourceLanguage: entry.DetectedSourceLanguage,
		TargetLanguage: entry.TargetLanguage, Provider: entry.Provider,
	}
	if projected.Translation.SourceLanguage == "" {
		projected.Translation.SourceLanguage = likelyLanguage(original)
	}
	if keepOriginal {
		projected.Translation.Original = original
	}
	return projected
}

func failedTranslationData(data *MsgServerData, original, source, target string,
	settings translation.Settings) *MsgServerData {
	projected := data.copy()
	projected.Content = nil
	if settings.FailurePolicy == "original" {
		projected.Content = original
	}
	projected.Translation = &MsgTranslation{
		Status: "failed", SourceLanguage: source, TargetLanguage: target,
	}
	if settings.KeepOriginal && settings.FailurePolicy == "original" {
		projected.Translation.Original = original
	}
	return projected
}

// projectHistoricalData 对历史消息应用与实时投递相同的策略。
// 翻译完成后，回调使用相同序号发送替换数据包。
func (runtime *translationRuntime) projectHistoricalData(topic string, data *MsgServerData,
	sess *Session, asUid types.Uid) (*MsgServerData, func()) {
	return runtime.project(topic, data, sess.lang, data.From == asUid.UserId(),
		func(translated *MsgServerData) {
			sess.queueOut(&ServerComMessage{Data: translated})
		})
}
