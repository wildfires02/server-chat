package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"chat/server/logs"
)

// DLQAlertConfig 配置通用 HTTPS Webhook，可接入飞书、PagerDuty 或内部告警网关。
type DLQAlertConfig struct {
	Enabled            bool   `json:"enabled"`
	WebhookURL         string `json:"webhook_url"`
	BearerToken        string `json:"bearer_token,omitempty"`
	TimeoutSeconds     int    `json:"timeout_seconds,omitempty"`
	MaxAttempts        int    `json:"max_attempts,omitempty"`
	MinIntervalSeconds int    `json:"min_interval_seconds,omitempty"`
	AllowInsecure      bool   `json:"allow_insecure,omitempty"`
}

type dlqAlertEvent struct {
	Event      string    `json:"event"`
	Severity   string    `json:"severity"`
	Provider   string    `json:"provider"`
	ID         string    `json:"id,omitempty"`
	Count      int       `json:"count,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
}

type dlqAlertRuntime struct {
	config DLQAlertConfig
	client *http.Client
	queue  chan dlqAlertEvent
	stop   chan struct{}
	done   chan struct{}
}

var dlqAlerts struct {
	sync.RWMutex
	runtime *dlqAlertRuntime
}

func normalizeDLQAlertConfig(config *DLQAlertConfig) (DLQAlertConfig, error) {
	if config == nil || !config.Enabled {
		return DLQAlertConfig{}, nil
	}
	result := *config
	result.WebhookURL = strings.TrimSpace(result.WebhookURL)
	parsed, err := url.Parse(result.WebhookURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && !(result.AllowInsecure && parsed.Scheme == "http")) {
		return DLQAlertConfig{}, errors.New("push DLQ webhook must be an absolute HTTPS URL")
	}
	if result.TimeoutSeconds <= 0 || result.TimeoutSeconds > 30 {
		result.TimeoutSeconds = 5
	}
	if result.MaxAttempts <= 0 || result.MaxAttempts > 10 {
		result.MaxAttempts = 5
	}
	if result.MinIntervalSeconds <= 0 {
		result.MinIntervalSeconds = 30
	}
	return result, nil
}

// StartDLQAlerts 启动独立告警 Worker；Webhook 故障不会阻塞 Push Outbox。
func StartDLQAlerts(config *DLQAlertConfig) (func(), error) {
	normalized, err := normalizeDLQAlertConfig(config)
	if err != nil {
		return nil, err
	}
	if !normalized.Enabled {
		return func() {}, nil
	}
	runtime := &dlqAlertRuntime{
		config: normalized,
		client: &http.Client{Timeout: time.Duration(normalized.TimeoutSeconds) * time.Second},
		queue:  make(chan dlqAlertEvent, 256), stop: make(chan struct{}), done: make(chan struct{}),
	}
	dlqAlerts.Lock()
	if dlqAlerts.runtime != nil {
		dlqAlerts.Unlock()
		return nil, errors.New("push DLQ alert worker already started")
	}
	dlqAlerts.runtime = runtime
	dlqAlerts.Unlock()
	go runtime.run()
	for _, provider := range []string{"fcm"} {
		if stats, statsErr := GetDurableOutboxStats(provider); statsErr == nil && stats.DLQ > 0 {
			runtime.enqueue(dlqAlertEvent{
				Event: "push_dlq_startup", Severity: stats.Alert, Provider: provider,
				Count: stats.DLQ, OccurredAt: time.Now().UTC(),
			})
		}
	}
	return func() {
		dlqAlerts.Lock()
		if dlqAlerts.runtime == runtime {
			dlqAlerts.runtime = nil
		}
		dlqAlerts.Unlock()
		close(runtime.stop)
		<-runtime.done
	}, nil
}

func notifyPushDeadLetter(provider, id string) {
	dlqAlerts.RLock()
	runtime := dlqAlerts.runtime
	dlqAlerts.RUnlock()
	if runtime == nil {
		return
	}
	runtime.enqueue(dlqAlertEvent{
		Event: "push_dlq_created", Severity: "critical", Provider: provider,
		ID: id, Count: 1, OccurredAt: time.Now().UTC(),
	})
}

func (runtime *dlqAlertRuntime) enqueue(event dlqAlertEvent) {
	select {
	case runtime.queue <- event:
	default:
		logs.Err.Printf("PUSH_DLQ_WEBHOOK_QUEUE_FULL provider=%s id=%s", event.Provider, event.ID)
	}
}

func (runtime *dlqAlertRuntime) run() {
	defer close(runtime.done)
	lastSent := make(map[string]time.Time)
	pending := make(map[string]dlqAlertEvent)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	flush := func(now time.Time) {
		minimum := time.Duration(runtime.config.MinIntervalSeconds) * time.Second
		for provider, event := range pending {
			if now.Sub(lastSent[provider]) < minimum {
				continue
			}
			if err := runtime.deliver(event); err != nil {
				logs.Err.Printf("PUSH_DLQ_WEBHOOK_FAILED provider=%s count=%d error=%v", provider, event.Count, err)
			} else {
				lastSent[provider] = now
			}
			delete(pending, provider)
		}
	}
	for {
		select {
		case event := <-runtime.queue:
			if current, exists := pending[event.Provider]; exists {
				current.Count += event.Count
				current.ID = event.ID
				current.OccurredAt = event.OccurredAt
				pending[event.Provider] = current
			} else {
				pending[event.Provider] = event
			}
			flush(time.Now())
		case now := <-ticker.C:
			flush(now)
		case <-runtime.stop:
			flush(time.Now().Add(24 * time.Hour))
			return
		}
	}
}

func (runtime *dlqAlertRuntime) deliver(event dlqAlertEvent) error {
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 1; attempt <= runtime.config.MaxAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(runtime.config.TimeoutSeconds)*time.Second)
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, runtime.config.WebhookURL, bytes.NewReader(body))
		if reqErr != nil {
			cancel()
			return reqErr
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "server-chat-push-alert/1")
		if runtime.config.BearerToken != "" {
			req.Header.Set("Authorization", "Bearer "+runtime.config.BearerToken)
		}
		response, requestErr := runtime.client.Do(req)
		cancel()
		if requestErr == nil && response != nil {
			_ = response.Body.Close()
			if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
				return nil
			}
			lastErr = errors.New("webhook returned " + response.Status)
		} else {
			lastErr = requestErr
		}
		if attempt < runtime.config.MaxAttempts {
			time.Sleep(time.Duration(1<<(attempt-1)) * 250 * time.Millisecond)
		}
	}
	return lastErr
}
