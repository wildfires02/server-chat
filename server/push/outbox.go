package push

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
)

const (
	durableOutboxPrefix = "pushoutbox:"
	durableDLQPrefix    = "pushdlq:"
	durableJobMaxBytes  = 56 << 10
)

type durableOutboxConfig struct {
	BatchSize      int
	RecipientChunk int
	MaxAttempts    int
	PollInterval   time.Duration
	RetryBase      time.Duration
	MaxRetry       time.Duration
	Lease          time.Duration
}

var defaultDurableOutboxConfig = durableOutboxConfig{
	BatchSize:      100,
	RecipientChunk: 40,
	MaxAttempts:    8,
	PollInterval:   2 * time.Second,
	RetryBase:      2 * time.Second,
	MaxRetry:       time.Hour,
	Lease:          5 * time.Minute,
}

type durableOutboxJob struct {
	Version       int             `json:"version"`
	Provider      string          `json:"provider"`
	Receipt       *durableReceipt `json:"receipt"`
	Status        string          `json:"status"`
	Attempts      int             `json:"attempts"`
	CreatedAt     time.Time       `json:"created_at"`
	NextAttemptAt time.Time       `json:"next_attempt_at,omitempty"`
	LeaseOwner    string          `json:"lease_owner,omitempty"`
	LeaseUntil    time.Time       `json:"lease_until,omitempty"`
	LastError     string          `json:"last_error,omitempty"`
}

// durableReceipt 使用字符串 UID 作为键，避免 Uid 的 JSON Map 键无法往返解码。
type durableReceipt struct {
	To      map[string]Recipient `json:"to"`
	Channel string               `json:"channel"`
	Payload Payload              `json:"payload"`
}

func newDurableReceipt(receipt *Receipt) *durableReceipt {
	result := &durableReceipt{
		To:      make(map[string]Recipient, len(receipt.To)),
		Channel: receipt.Channel,
		Payload: receipt.Payload,
	}
	for uid, recipient := range receipt.To {
		result.To[uid.String()] = recipient
	}
	return result
}

func (receipt *durableReceipt) runtimeReceipt() (*Receipt, error) {
	result := &Receipt{
		To:      make(map[types.Uid]Recipient, len(receipt.To)),
		Channel: receipt.Channel,
		Payload: receipt.Payload,
	}
	for encoded, recipient := range receipt.To {
		var uid types.Uid
		if err := uid.UnmarshalText([]byte(encoded)); err != nil || uid.IsZero() {
			return nil, fmt.Errorf("invalid durable push uid %q", encoded)
		}
		result.To[uid] = recipient
	}
	return result, nil
}

// DurableEnqueuer 由需要可靠投递的推送插件实现。
type DurableEnqueuer interface {
	Enqueue(*Receipt) error
}

// DurableOutbox 将推送任务先写入数据库，再由后台 Worker 至少投递一次。
type DurableOutbox struct {
	provider string
	deliver  func(*Receipt) error
	config   durableOutboxConfig
	owner    string
	now      func() time.Time
	wake     chan struct{}
	stop     chan struct{}
	done     chan struct{}
	start    sync.Once
	close    sync.Once
}

// DurableOutboxStats 是管理员查看积压与死信时使用的统计快照。
type DurableOutboxStats struct {
	Provider  string `json:"provider"`
	Queued    int    `json:"queued"`
	Leased    int    `json:"leased"`
	Dead      int    `json:"dead"`
	Total     int    `json:"total"`
	DLQ       int    `json:"dlq"`
	Alert     string `json:"alert"`
	OldestDLQ string `json:"oldest_dlq_at,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// DurableDeadLetter 是管理员可见的死信摘要，不返回消息正文或设备 Token。
type DurableDeadLetter struct {
	ID         string    `json:"id"`
	Provider   string    `json:"provider"`
	Status     string    `json:"status"`
	Attempts   int       `json:"attempts"`
	CreatedAt  time.Time `json:"created_at"`
	LastError  string    `json:"last_error"`
	What       string    `json:"what"`
	Topic      string    `json:"topic"`
	Channel    string    `json:"channel,omitempty"`
	Recipients int       `json:"recipients"`
}

// PermanentDeliveryError 表示重试无法修复的供应商或配置错误。
type PermanentDeliveryError struct {
	err error
}

func (err *PermanentDeliveryError) Error() string { return err.err.Error() }
func (err *PermanentDeliveryError) Unwrap() error { return err.err }

// Permanent 将错误标记为不可重试，任务会直接进入死信队列。
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &PermanentDeliveryError{err: err}
}

// NewDurableOutbox 创建一个使用持久缓存表的推送发件箱。
func NewDurableOutbox(provider string, deliver func(*Receipt) error) *DurableOutbox {
	return newDurableOutbox(provider, deliver, defaultDurableOutboxConfig)
}

func newDurableOutbox(provider string, deliver func(*Receipt) error,
	config durableOutboxConfig) *DurableOutbox {
	provider = strings.ToLower(strings.TrimSpace(provider))
	return &DurableOutbox{
		provider: provider,
		deliver:  deliver,
		config:   config,
		owner:    provider + "-" + durableRandomID(),
		now:      func() time.Time { return time.Now().UTC() },
		wake:     make(chan struct{}, 1),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Start 启动持久队列扫描。重复调用不会创建额外 Worker。
func (outbox *DurableOutbox) Start() {
	if outbox == nil {
		return
	}
	outbox.start.Do(func() { go outbox.worker() })
}

// Stop 停止当前节点的 Worker；数据库中的未完成任务仍会保留。
func (outbox *DurableOutbox) Stop() {
	if outbox == nil {
		return
	}
	outbox.close.Do(func() { close(outbox.stop) })
	select {
	case <-outbox.done:
	case <-time.After(5 * time.Second):
		logs.Warn.Printf("push outbox stop timed out, provider=%s", outbox.provider)
	}
}

// Enqueue 按接收人和数据库值大小拆分任务，避免单个缓存值过大。
func (outbox *DurableOutbox) Enqueue(receipt *Receipt) error {
	if outbox == nil || outbox.provider == "" || outbox.deliver == nil || receipt == nil {
		return errors.New("push outbox is not initialized")
	}
	if store.PCache == nil {
		return errors.New("persistent cache is unavailable")
	}
	if len(receipt.To) == 0 && receipt.Channel == "" {
		return nil
	}
	if len(receipt.To) == 0 {
		if _, err := outbox.persistReceipt(cloneReceiptForUsers(receipt, nil)); err != nil {
			return err
		}
		outbox.signal()
		return nil
	}

	uids := make([]types.Uid, 0, len(receipt.To))
	for uid := range receipt.To {
		uids = append(uids, uid)
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	chunkSize := outbox.config.RecipientChunk
	if chunkSize <= 0 {
		chunkSize = 40
	}
	created := make([]string, 0, (len(uids)+chunkSize-1)/chunkSize)
	for offset := 0; offset < len(uids); offset += chunkSize {
		upper := min(offset+chunkSize, len(uids))
		chunk := cloneReceiptForUsers(receipt, uids[offset:upper])
		keys, err := outbox.persistReceipt(chunk)
		created = append(created, keys...)
		if err != nil {
			for _, key := range created {
				_ = store.PCache.Delete(key)
			}
			return err
		}
	}
	outbox.signal()
	return nil
}

func (outbox *DurableOutbox) persistReceipt(receipt *Receipt) ([]string, error) {
	now := outbox.now()
	job := &durableOutboxJob{
		Version: 1, Provider: outbox.provider, Receipt: newDurableReceipt(receipt),
		Status: "queued", CreatedAt: now, NextAttemptAt: now,
	}
	raw, err := json.Marshal(job)
	if err != nil {
		return nil, err
	}
	if len(raw) > durableJobMaxBytes {
		job.Receipt.Payload.Content = nil
		raw, err = json.Marshal(job)
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > durableJobMaxBytes && len(receipt.To) > 1 {
		uids := make([]types.Uid, 0, len(receipt.To))
		for uid := range receipt.To {
			uids = append(uids, uid)
		}
		sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
		middle := len(uids) / 2
		left, leftErr := outbox.persistReceipt(cloneReceiptForUsers(receipt, uids[:middle]))
		if leftErr != nil {
			return left, leftErr
		}
		right, rightErr := outbox.persistReceipt(cloneReceiptForUsers(receipt, uids[middle:]))
		if rightErr != nil {
			for _, key := range left {
				_ = store.PCache.Delete(key)
			}
			return right, rightErr
		}
		return append(left, right...), nil
	}
	if len(raw) > durableJobMaxBytes {
		return nil, fmt.Errorf("push outbox job exceeds %d bytes", durableJobMaxBytes)
	}
	key := fmt.Sprintf("%s%s:%020d:%s", durableOutboxPrefix, outbox.provider,
		now.UnixNano(), durableRandomID())
	if err = store.PCache.Upsert(key, string(raw), true); err != nil {
		return nil, err
	}
	return []string{key}, nil
}

func cloneReceiptForUsers(source *Receipt, uids []types.Uid) *Receipt {
	copyReceipt := &Receipt{
		To:      make(map[types.Uid]Recipient, len(uids)),
		Channel: source.Channel,
		Payload: source.Payload,
	}
	for _, uid := range uids {
		copyReceipt.To[uid] = source.To[uid]
	}
	return copyReceipt
}

func (outbox *DurableOutbox) worker() {
	defer close(outbox.done)
	ticker := time.NewTicker(outbox.config.PollInterval)
	defer ticker.Stop()
	for {
		if _, err := outbox.processAvailable(outbox.now()); err != nil {
			logs.Warn.Printf("push outbox scan failed, provider=%s: %v", outbox.provider, err)
		}
		select {
		case <-outbox.wake:
		case <-ticker.C:
		case <-outbox.stop:
			return
		}
	}
}

func (outbox *DurableOutbox) signal() {
	select {
	case outbox.wake <- struct{}{}:
	default:
	}
}

func (outbox *DurableOutbox) processAvailable(now time.Time) (int, error) {
	entries, err := store.PCache.List(durableOutboxPrefix+outbox.provider+":", outbox.config.BatchSize)
	if err != nil {
		return 0, err
	}
	processed := 0
	for key, raw := range entries {
		didProcess, processErr := outbox.processEntry(key, raw, now)
		if processErr != nil {
			logs.Warn.Printf("push outbox entry failed, provider=%s key=%s: %v",
				outbox.provider, key, processErr)
			continue
		}
		if didProcess {
			processed++
		}
	}
	return processed, nil
}

func (outbox *DurableOutbox) processEntry(key, raw string, now time.Time) (bool, error) {
	var job durableOutboxJob
	if err := json.Unmarshal([]byte(raw), &job); err != nil || job.Receipt == nil {
		if moveErr := movePushDeadLetter(outbox.provider, key, raw); moveErr != nil {
			return false, moveErr
		}
		return true, nil
	}
	if job.Status == "completed" {
		return true, store.PCache.Delete(key)
	}
	if job.Status == "dead" {
		return true, movePushDeadLetter(outbox.provider, key, raw)
	}
	if job.NextAttemptAt.After(now) || (job.Status == "leased" && job.LeaseUntil.After(now)) {
		return false, nil
	}

	claimed := job
	claimed.Status = "leased"
	claimed.Attempts++
	claimed.LeaseOwner = outbox.owner
	claimed.LeaseUntil = now.Add(outbox.config.Lease)
	claimedRaw, err := json.Marshal(&claimed)
	if err != nil {
		return false, err
	}
	swapped, err := store.PCache.CompareAndSwap(key, raw, string(claimedRaw))
	if err != nil || !swapped {
		return false, err
	}

	deliveryReceipt, receiptErr := claimed.Receipt.runtimeReceipt()
	if receiptErr != nil {
		deliveryErr := pushPermanentReceiptError(receiptErr)
		return outbox.finishDelivery(key, string(claimedRaw), claimed, deliveryErr, now)
	}
	deliveryErr := outbox.deliver(deliveryReceipt)
	return outbox.finishDelivery(key, string(claimedRaw), claimed, deliveryErr, now)
}

func pushPermanentReceiptError(err error) error {
	return Permanent(err)
}

func (outbox *DurableOutbox) finishDelivery(key, claimedRaw string,
	claimed durableOutboxJob, deliveryErr error, now time.Time) (bool, error) {
	if deliveryErr == nil {
		completed := claimed
		completed.Status = "completed"
		completed.LeaseOwner = ""
		completed.LeaseUntil = time.Time{}
		completedRaw, marshalErr := json.Marshal(&completed)
		if marshalErr != nil {
			return true, marshalErr
		}
		swapped, err := store.PCache.CompareAndSwap(key, claimedRaw, string(completedRaw))
		if err != nil || !swapped {
			return true, err
		}
		return true, store.PCache.Delete(key)
	}

	claimed.LastError = truncatePushError(deliveryErr.Error())
	claimed.LeaseOwner = ""
	claimed.LeaseUntil = time.Time{}
	var permanent *PermanentDeliveryError
	if errors.As(deliveryErr, &permanent) || claimed.Attempts >= outbox.config.MaxAttempts {
		claimed.Status = "dead"
		deadRaw, marshalErr := json.Marshal(&claimed)
		if marshalErr != nil {
			return true, marshalErr
		}
		swapped, err := store.PCache.CompareAndSwap(key, claimedRaw, string(deadRaw))
		if err != nil || !swapped {
			return true, err
		}
		return true, movePushDeadLetter(outbox.provider, key, string(deadRaw))
	}
	claimed.Status = "queued"
	claimed.NextAttemptAt = now.Add(outbox.retryDelay(claimed.Attempts))
	retryRaw, marshalErr := json.Marshal(&claimed)
	if marshalErr != nil {
		return true, marshalErr
	}
	_, err := store.PCache.CompareAndSwap(key, claimedRaw, string(retryRaw))
	return true, err
}

func (outbox *DurableOutbox) retryDelay(attempt int) time.Duration {
	delay := outbox.config.RetryBase
	for current := 1; current < attempt && delay < outbox.config.MaxRetry; current++ {
		delay *= 2
	}
	if delay > outbox.config.MaxRetry {
		return outbox.config.MaxRetry
	}
	return delay
}

func movePushDeadLetter(provider, key, raw string) error {
	suffix := strings.TrimPrefix(key, durableOutboxPrefix+provider+":")
	dlqKey := durableDLQPrefix + provider + ":" + suffix
	if err := store.PCache.Upsert(dlqKey, raw, false); err != nil {
		return err
	}
	if err := store.PCache.Delete(key); err != nil {
		return err
	}
	// 统一使用结构化前缀，生产日志平台可直接为 PUSH_DLQ_ALERT 配置告警规则。
	logs.Err.Printf("PUSH_DLQ_ALERT provider=%s id=%s", provider, suffix)
	return nil
}

func truncatePushError(value string) string {
	const maxLength = 2048
	if len(value) <= maxLength {
		return value
	}
	return value[:maxLength]
}

func durableRandomID() string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer)
}

// GetDurableOutboxStats 返回指定供应商当前的排队与死信数量。
func GetDurableOutboxStats(provider string) (DurableOutboxStats, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	stats := DurableOutboxStats{Provider: provider}
	const limit = 10000
	entries, err := store.PCache.List(durableOutboxPrefix+provider+":", limit)
	if err != nil {
		return stats, err
	}
	stats.Total = len(entries)
	stats.Truncated = len(entries) == limit
	for _, raw := range entries {
		var job durableOutboxJob
		if json.Unmarshal([]byte(raw), &job) != nil {
			stats.Dead++
			continue
		}
		switch job.Status {
		case "leased":
			stats.Leased++
		case "dead":
			stats.Dead++
		default:
			stats.Queued++
		}
	}
	dlq, err := store.PCache.List(durableDLQPrefix+provider+":", limit)
	if err != nil {
		return stats, err
	}
	stats.DLQ = len(dlq)
	oldest := time.Time{}
	for _, raw := range dlq {
		var job durableOutboxJob
		if json.Unmarshal([]byte(raw), &job) == nil &&
			!job.CreatedAt.IsZero() && (oldest.IsZero() || job.CreatedAt.Before(oldest)) {
			oldest = job.CreatedAt
		}
	}
	if !oldest.IsZero() {
		stats.OldestDLQ = oldest.UTC().Format(time.RFC3339)
	}
	switch {
	case stats.DLQ >= 100:
		stats.Alert = "critical"
	case stats.DLQ > 0:
		stats.Alert = "warning"
	default:
		stats.Alert = "ok"
	}
	stats.Truncated = stats.Truncated || len(dlq) == limit
	return stats, nil
}

// ListDurableDeadLetters 按创建时间倒序返回死信摘要。
func ListDurableDeadLetters(provider string, limit int) ([]DurableDeadLetter, error) {
	provider, err := normalizeDurableProvider(provider)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	entries, err := store.PCache.List(durableDLQPrefix+provider+":", limit)
	if err != nil {
		return nil, err
	}
	result := make([]DurableDeadLetter, 0, len(entries))
	for key, raw := range entries {
		letter, parseErr := durableDeadLetterFromRaw(provider, key, raw)
		if parseErr != nil {
			letter = DurableDeadLetter{
				ID:       strings.TrimPrefix(key, durableDLQPrefix+provider+":"),
				Provider: provider, Status: "corrupt", LastError: truncatePushError(parseErr.Error()),
			}
		}
		result = append(result, letter)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID > result[j].ID
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result, nil
}

// ReplayDurableDeadLetter 将死信重新放回 outbox。重复请求不会创建第二份任务。
func ReplayDurableDeadLetter(provider, id string) (DurableDeadLetter, error) {
	provider, err := normalizeDurableProvider(provider)
	if err != nil {
		return DurableDeadLetter{}, err
	}
	dlqKey, outboxKey, err := durableDeadLetterKeys(provider, id)
	if err != nil {
		return DurableDeadLetter{}, err
	}
	raw, err := store.PCache.Get(dlqKey)
	if err != nil {
		return DurableDeadLetter{}, err
	}
	letter, err := durableDeadLetterFromRaw(provider, dlqKey, raw)
	if err != nil {
		return DurableDeadLetter{}, err
	}
	var job durableOutboxJob
	if err = json.Unmarshal([]byte(raw), &job); err != nil || job.Receipt == nil {
		return DurableDeadLetter{}, errors.New("corrupt push dead letter")
	}
	job.Status = "queued"
	job.Attempts = 0
	job.NextAttemptAt = time.Now().UTC()
	job.LeaseOwner = ""
	job.LeaseUntil = time.Time{}
	job.LastError = ""
	replayed, err := json.Marshal(&job)
	if err != nil {
		return DurableDeadLetter{}, err
	}
	if err = store.PCache.Upsert(outboxKey, string(replayed), true); err != nil &&
		!errors.Is(err, types.ErrDuplicate) {
		return DurableDeadLetter{}, err
	}
	if err = store.PCache.Delete(dlqKey); err != nil {
		return DurableDeadLetter{}, err
	}
	logs.Warn.Printf("push DLQ manually replayed provider=%s id=%s", provider, id)
	return letter, nil
}

// DeleteDurableDeadLetter 永久删除指定死信，调用前由管理端二次确认。
func DeleteDurableDeadLetter(provider, id string) error {
	provider, err := normalizeDurableProvider(provider)
	if err != nil {
		return err
	}
	dlqKey, _, err := durableDeadLetterKeys(provider, id)
	if err != nil {
		return err
	}
	if _, err = store.PCache.Get(dlqKey); err != nil {
		return err
	}
	if err = store.PCache.Delete(dlqKey); err == nil {
		logs.Warn.Printf("push DLQ manually deleted provider=%s id=%s", provider, id)
	}
	return err
}

func durableDeadLetterFromRaw(provider, key, raw string) (DurableDeadLetter, error) {
	var job durableOutboxJob
	if err := json.Unmarshal([]byte(raw), &job); err != nil || job.Receipt == nil {
		return DurableDeadLetter{}, errors.New("corrupt push dead letter")
	}
	return DurableDeadLetter{
		ID: strings.TrimPrefix(key, durableDLQPrefix+provider+":"), Provider: provider,
		Status: job.Status, Attempts: job.Attempts, CreatedAt: job.CreatedAt,
		LastError: job.LastError, What: job.Receipt.Payload.What,
		Topic: job.Receipt.Payload.Topic, Channel: job.Receipt.Channel,
		Recipients: len(job.Receipt.To),
	}, nil
}

func durableDeadLetterKeys(provider, id string) (string, string, error) {
	id = strings.TrimSpace(id)
	if id == "" || strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return "", "", errors.New("invalid push dead letter id")
	}
	return durableDLQPrefix + provider + ":" + id,
		durableOutboxPrefix + provider + ":" + id, nil
}

func normalizeDurableProvider(provider string) (string, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" || len(provider) > 32 {
		return "", errors.New("invalid push provider")
	}
	for _, char := range provider {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' && char != '_' {
			return "", errors.New("invalid push provider")
		}
	}
	return provider, nil
}
