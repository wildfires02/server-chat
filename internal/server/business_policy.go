package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"chat/server/store"
	"chat/server/store/types"

	"golang.org/x/sync/singleflight"
)

const businessPolicyMaxResponseSize = 64 << 10

var (
	errBusinessPolicyUnavailable = errors.New("business policy unavailable")
	errBusinessPolicyRateLimited = errors.New("business policy rate limited")
)

type businessPolicyConfig struct {
	Enabled          bool   `json:"enabled"`
	Endpoint         string `json:"endpoint"`
	BearerToken      string `json:"bearer_token"`
	TimeoutMS        int    `json:"timeout_ms"`
	CacheTTLSeconds  int    `json:"cache_ttl_seconds"`
	DenyTTLSeconds   int    `json:"deny_ttl_seconds"`
	CacheMaxEntries  int    `json:"cache_max_entries"`
	AuditEndpoint    string `json:"audit_endpoint"`
	AuditPollSeconds int    `json:"audit_poll_seconds"`
}

type businessPolicyDecision struct {
	Allowed    bool   `json:"allowed"`
	Reason     string `json:"reason"`
	ActorRole  string `json:"actor_role"`
	TargetRole string `json:"target_role"`
}

type businessPolicyRequest struct {
	Provider         string `json:"provider"`
	ActorExternalID  string `json:"actor_external_id"`
	TargetExternalID string `json:"target_external_id"`
	Action           string `json:"action"`
	Topic            string `json:"topic,omitempty"`
}

type cachedBusinessPolicyDecision struct {
	decision businessPolicyDecision
	expires  time.Time
}

type businessPolicyClient struct {
	endpoint string
	token    string
	allowTTL time.Duration
	denyTTL  time.Duration
	http     *http.Client

	mu              sync.Mutex
	cache           map[string]cachedBusinessPolicyDecision
	flight          singleflight.Group
	cacheMaxEntries int
	auditEndpoint   string
	auditPoll       time.Duration
}

func newBusinessPolicyClient(config businessPolicyConfig) (*businessPolicyClient, error) {
	if !config.Enabled {
		return nil, nil
	}
	config.Endpoint = strings.TrimSpace(config.Endpoint)
	config.BearerToken = strings.TrimSpace(config.BearerToken)
	if config.Endpoint == "" || config.BearerToken == "" {
		return nil, errors.New("business_policy.endpoint 和 bearer_token 不能为空")
	}
	if config.TimeoutMS == 0 {
		config.TimeoutMS = 1500
	}
	if config.CacheTTLSeconds == 0 {
		config.CacheTTLSeconds = 30
	}
	if config.DenyTTLSeconds == 0 {
		config.DenyTTLSeconds = 5
	}
	if config.CacheMaxEntries == 0 {
		config.CacheMaxEntries = 20000
	}
	if config.AuditPollSeconds == 0 {
		config.AuditPollSeconds = 2
	}
	if config.TimeoutMS < 100 || config.TimeoutMS > 10000 ||
		config.CacheTTLSeconds < 1 || config.CacheTTLSeconds > 300 ||
		config.DenyTTLSeconds < 1 || config.DenyTTLSeconds > 60 ||
		config.CacheMaxEntries < 100 || config.CacheMaxEntries > 1000000 ||
		config.AuditPollSeconds < 1 || config.AuditPollSeconds > 60 {
		return nil, errors.New("business_policy 超时或缓存参数超出允许范围")
	}
	return &businessPolicyClient{
		endpoint:        config.Endpoint,
		token:           config.BearerToken,
		allowTTL:        time.Duration(config.CacheTTLSeconds) * time.Second,
		denyTTL:         time.Duration(config.DenyTTLSeconds) * time.Second,
		http:            &http.Client{Timeout: time.Duration(config.TimeoutMS) * time.Millisecond},
		cache:           make(map[string]cachedBusinessPolicyDecision),
		cacheMaxEntries: config.CacheMaxEntries,
		auditEndpoint:   strings.TrimSpace(config.AuditEndpoint),
		auditPoll:       time.Duration(config.AuditPollSeconds) * time.Second,
	}, nil
}

const (
	businessAuditOutboxPrefix    = "business:audit:text:v1:"
	businessAuditOutboxHashBytes = 20
)

// businessAuditOutboxKey 将完整事件 ID 再散列为固定长度键。
// MySQL kvmeta.key 最长 64 字节，前缀加 20 字节摘要的十六进制形式共 63 字节。
// 事件正文仍保留完整 EventID，不影响下游审计服务的幂等判断。
func businessAuditOutboxKey(eventID string) string {
	digest := sha256.Sum256([]byte(eventID))
	return fmt.Sprintf("%s%x", businessAuditOutboxPrefix, digest[:businessAuditOutboxHashBytes])
}

type businessTextAuditEvent struct {
	EventID             string `json:"event_id"`
	Topic               string `json:"topic"`
	Sequence            int    `json:"sequence"`
	SenderExternalID    string `json:"sender_external_id"`
	RecipientExternalID string `json:"recipient_external_id"`
	Text                string `json:"text"`
	SentAt              int64  `json:"sent_at"`
}

func (client *businessPolicyClient) startAuditWorker() {
	if client == nil || client.auditEndpoint == "" {
		return
	}
	go func() {
		ticker := time.NewTicker(client.auditPoll)
		defer ticker.Stop()
		for {
			client.flushAuditOutbox()
			<-ticker.C
		}
	}()
}

func (client *businessPolicyClient) archiveTextMessage(stored *types.Message,
	recipient types.Uid) error {
	if client == nil || client.auditEndpoint == "" || stored == nil || store.PCache == nil {
		return nil
	}
	text, ok := auditPlainText(stored.Head, stored.Content)
	if !ok {
		return nil
	}
	sender := types.ParseUid(stored.From)
	provider, senderExternalID, err := externalIdentityForPolicy(sender)
	if err != nil {
		return err
	}
	targetProvider, recipientExternalID, err := externalIdentityForPolicy(recipient)
	if err != nil || targetProvider != provider {
		return types.ErrPolicy
	}
	identity := fmt.Sprintf("%s\x00%d\x00%s\x00%s\x00%d", stored.Topic, stored.SeqId,
		senderExternalID, recipientExternalID, stored.UpdatedAt.UnixMilli())
	digest := sha256.Sum256([]byte(identity))
	event := businessTextAuditEvent{
		EventID: fmt.Sprintf("%x", digest[:]), Topic: stored.Topic, Sequence: stored.SeqId,
		SenderExternalID: senderExternalID, RecipientExternalID: recipientExternalID,
		Text: text, SentAt: stored.CreatedAt.UnixMilli(),
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return store.PCache.Upsert(businessAuditOutboxKey(event.EventID), string(raw), false)
}

func auditPlainText(head map[string]any, content any) (string, bool) {
	if head != nil {
		if mime, _ := head["mime"].(string); mime != "" && mime != "text/plain" && mime != "text/x-drafty" {
			return "", false
		}
	}
	if draftyCustomMIME(content) == "application/x-chat-payment" {
		return "", false
	}
	var text string
	switch typed := content.(type) {
	case string:
		text = typed
	case map[string]any:
		text, _ = typed["txt"].(string)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	runes := []rune(text)
	if len(runes) > 10000 {
		text = string(runes[:10000])
	}
	return text, true
}

func (client *businessPolicyClient) flushAuditOutbox() {
	items, err := store.PCache.List(businessAuditOutboxPrefix, 100)
	if err != nil {
		return
	}
	for key, raw := range items {
		var event businessTextAuditEvent
		if json.Unmarshal([]byte(raw), &event) != nil {
			_ = store.PCache.Delete(key)
			continue
		}
		body, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			continue
		}
		req, requestErr := http.NewRequest(http.MethodPost, client.auditEndpoint, bytes.NewReader(body))
		if requestErr != nil {
			continue
		}
		req.Header.Set("Authorization", "Bearer "+client.token)
		req.Header.Set("Content-Type", "application/json")
		res, requestErr := client.http.Do(req)
		if requestErr != nil {
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, businessPolicyMaxResponseSize))
		_ = res.Body.Close()
		if res.StatusCode >= http.StatusOK && res.StatusCode < http.StatusMultipleChoices {
			_ = store.PCache.Delete(key)
		}
	}
}

func (client *businessPolicyClient) authorizeUIDs(actor, target types.Uid, action, topic string) error {
	if client == nil {
		return nil
	}
	actorProvider, actorExternalID, err := externalIdentityForPolicy(actor)
	if err != nil {
		return err
	}
	targetProvider, targetExternalID, err := externalIdentityForPolicy(target)
	if err != nil {
		return err
	}
	if actorProvider != targetProvider {
		return types.ErrPolicy
	}
	request := businessPolicyRequest{
		Provider: actorProvider, ActorExternalID: actorExternalID,
		TargetExternalID: targetExternalID, Action: action, Topic: topic,
	}
	decision, err := client.evaluate(context.Background(), request)
	if err != nil {
		return errBusinessPolicyUnavailable
	}
	if !decision.Allowed {
		if request.Action == "batch_greeting" && decision.Reason == "batch_greeting_rate_limited" {
			return errBusinessPolicyRateLimited
		}
		return types.ErrPolicy
	}
	return nil
}

func (client *businessPolicyClient) evaluate(ctx context.Context,
	request businessPolicyRequest) (businessPolicyDecision, error) {
	key := request.Provider + "\x00" + request.ActorExternalID + "\x00" +
		request.TargetExternalID + "\x00" + request.Action
	// 批量打招呼必须逐条到业务服务执行全局频控，不能命中本地权限缓存。
	if request.Action == "batch_greeting" {
		return client.evaluateUncached(ctx, key, request)
	}
	now := time.Now()
	client.mu.Lock()
	if cached, ok := client.cache[key]; ok && now.Before(cached.expires) {
		client.mu.Unlock()
		return cached.decision, nil
	}
	client.mu.Unlock()

	value, err, _ := client.flight.Do(key, func() (any, error) {
		client.mu.Lock()
		if cached, ok := client.cache[key]; ok && time.Now().Before(cached.expires) {
			client.mu.Unlock()
			return cached.decision, nil
		}
		client.mu.Unlock()
		return client.evaluateUncached(ctx, key, request)
	})
	if err != nil {
		return businessPolicyDecision{}, err
	}
	return value.(businessPolicyDecision), nil
}

func (client *businessPolicyClient) evaluateUncached(ctx context.Context, key string,
	request businessPolicyRequest) (businessPolicyDecision, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return businessPolicyDecision{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint, bytes.NewReader(body))
	if err != nil {
		return businessPolicyDecision{}, err
	}
	req.Header.Set("Authorization", "Bearer "+client.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := client.http.Do(req)
	if err != nil {
		return businessPolicyDecision{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, businessPolicyMaxResponseSize))
		return businessPolicyDecision{}, fmt.Errorf("business policy returned HTTP %d", res.StatusCode)
	}
	var decision businessPolicyDecision
	if err = json.NewDecoder(io.LimitReader(res.Body, businessPolicyMaxResponseSize)).Decode(&decision); err != nil {
		return businessPolicyDecision{}, err
	}
	if request.Action == "batch_greeting" {
		return decision, nil
	}
	ttl := client.allowTTL
	if !decision.Allowed {
		ttl = client.denyTTL
	}
	now := time.Now()
	client.mu.Lock()
	if len(client.cache) >= client.cacheMaxEntries {
		for cacheKey, cached := range client.cache {
			if now.After(cached.expires) {
				delete(client.cache, cacheKey)
			}
		}
		if len(client.cache) >= client.cacheMaxEntries {
			// 到达硬上限时不缓存新条目，避免热点攻击造成常驻内存增长。
			client.mu.Unlock()
			return decision, nil
		}
	}
	client.cache[key] = cachedBusinessPolicyDecision{decision: decision, expires: now.Add(ttl)}
	client.mu.Unlock()
	return decision, nil
}

func externalIdentityForPolicy(uid types.Uid) (string, string, error) {
	user, err := store.Users.Get(uid)
	if err != nil || user == nil {
		return "", "", types.ErrUserNotFound
	}
	trusted := externalIdentityObject(user.Trusted)
	provider, _ := trusted["identity_provider"].(string)
	externalID, _ := trusted["external_id"].(string)
	provider, externalID = strings.TrimSpace(provider), strings.TrimSpace(externalID)
	if provider == "" || externalID == "" {
		return "", "", types.ErrPolicy
	}
	return provider, externalID, nil
}

func businessPolicyAction(head map[string]any, content any, attachments []string) string {
	if head != nil {
		if action, _ := head["x-business-action"].(string); action == "batch_greeting" {
			return "batch_greeting"
		}
		if head["webrtc"] != nil {
			return "call"
		}
		if mime, _ := head["mime"].(string); mime == "application/x-chat-payment" {
			return "payment"
		}
		if kind, _ := head[headMessageKind].(string); kind == "file" {
			return "document"
		}
	}
	if draftyCustomMIME(content) == "application/x-chat-payment" {
		return "payment"
	}
	if len(attachments) > 0 {
		return "media"
	}
	return "message"
}

func draftyCustomMIME(content any) string {
	var document map[string]any
	switch typed := content.(type) {
	case map[string]any:
		document = typed
	case types.KVMap:
		document = map[string]any(typed)
	default:
		return ""
	}
	entities, ok := document["ent"].([]any)
	if !ok {
		return ""
	}
	for _, raw := range entities {
		entity, ok := raw.(map[string]any)
		if !ok || entity["tp"] != "CE" {
			continue
		}
		data, _ := entity["data"].(map[string]any)
		if mime, _ := data["mime"].(string); mime != "" {
			return strings.ToLower(strings.TrimSpace(mime))
		}
	}
	return ""
}
