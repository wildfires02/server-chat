package loadtest

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

var latencyBounds = []time.Duration{
	time.Millisecond,
	2 * time.Millisecond,
	5 * time.Millisecond,
	10 * time.Millisecond,
	20 * time.Millisecond,
	50 * time.Millisecond,
	100 * time.Millisecond,
	200 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	30 * time.Second,
	60 * time.Second,
}

// 直方图快照保存可跨执行节点合并的固定桶延迟数据。
type HistogramSnapshot struct {
	BoundsMicros []int64  `json:"bounds_micros"`
	Counts       []uint64 `json:"counts"`
	Count        uint64   `json:"count"`
	SumMicros    uint64   `json:"sum_micros"`
	MaxMicros    int64    `json:"max_micros"`
}

// 分位值计算返回固定桶直方图中不低于目标分位的上界。
func (snapshot HistogramSnapshot) Percentile(quantile float64) time.Duration {
	if snapshot.Count == 0 {
		return 0
	}
	if quantile < 0 {
		quantile = 0
	}
	if quantile > 1 {
		quantile = 1
	}
	target := uint64(math.Ceil(float64(snapshot.Count) * quantile))
	if target == 0 {
		target = 1
	}
	var cumulative uint64
	for index, count := range snapshot.Counts {
		cumulative += count
		if cumulative < target {
			continue
		}
		if index < len(snapshot.BoundsMicros) {
			return time.Duration(snapshot.BoundsMicros[index]) * time.Microsecond
		}
		return time.Duration(snapshot.MaxMicros) * time.Microsecond
	}
	return time.Duration(snapshot.MaxMicros) * time.Microsecond
}

// 直方图合并会汇总多个执行节点使用相同边界的快照。
func MergeHistogramSnapshots(snapshots ...HistogramSnapshot) HistogramSnapshot {
	result := emptyHistogramSnapshot()
	for _, snapshot := range snapshots {
		if len(snapshot.Counts) != len(result.Counts) {
			continue
		}
		for index, count := range snapshot.Counts {
			result.Counts[index] += count
		}
		result.Count += snapshot.Count
		result.SumMicros += snapshot.SumMicros
		if snapshot.MaxMicros > result.MaxMicros {
			result.MaxMicros = snapshot.MaxMicros
		}
	}
	return result
}

func emptyHistogramSnapshot() HistogramSnapshot {
	bounds := make([]int64, len(latencyBounds))
	for index, bound := range latencyBounds {
		bounds[index] = bound.Microseconds()
	}
	return HistogramSnapshot{
		BoundsMicros: bounds,
		Counts:       make([]uint64, len(bounds)+1),
	}
}

type latencyHistogram struct {
	counts [16]atomic.Uint64
	count  atomic.Uint64
	sum    atomic.Uint64
	max    atomic.Int64
}

func (histogram *latencyHistogram) Observe(value time.Duration) {
	if value < 0 {
		return
	}
	microseconds := value.Microseconds()
	index := sort.Search(len(latencyBounds), func(index int) bool {
		return value <= latencyBounds[index]
	})
	histogram.counts[index].Add(1)
	histogram.count.Add(1)
	histogram.sum.Add(uint64(microseconds))
	for {
		current := histogram.max.Load()
		if microseconds <= current || histogram.max.CompareAndSwap(current, microseconds) {
			break
		}
	}
}

func (histogram *latencyHistogram) Snapshot() HistogramSnapshot {
	snapshot := emptyHistogramSnapshot()
	for index := range histogram.counts {
		snapshot.Counts[index] = histogram.counts[index].Load()
	}
	snapshot.Count = histogram.count.Load()
	snapshot.SumMicros = histogram.sum.Load()
	snapshot.MaxMicros = histogram.max.Load()
	return snapshot
}

// 指标快照保存执行节点的累计压测结果。
type MetricsSnapshot struct {
	RunID                 string            `json:"run_id"`
	WorkerID              string            `json:"worker_id"`
	StartedAt             time.Time         `json:"started_at"`
	UpdatedAt             time.Time         `json:"updated_at"`
	Completed             bool              `json:"completed"`
	FatalError            string            `json:"fatal_error,omitempty"`
	ConnectionsAttempted  uint64            `json:"connections_attempted"`
	ConnectionsSucceeded  uint64            `json:"connections_succeeded"`
	ActiveConnections     int64             `json:"active_connections"`
	LoginsSucceeded       uint64            `json:"logins_succeeded"`
	Subscriptions         uint64            `json:"subscriptions"`
	PublishesAttempted    uint64            `json:"publishes_attempted"`
	PublishesAcknowledged uint64            `json:"publishes_acknowledged"`
	Deliveries            uint64            `json:"deliveries"`
	Errors                map[string]uint64 `json:"errors,omitempty"`
	AckLatency            HistogramSnapshot `json:"ack_latency"`
	DeliveryLatency       HistogramSnapshot `json:"delivery_latency"`
}

// 指标集合保存压测热路径使用的并发安全累计值。
type Metrics struct {
	runID                 string
	workerID              string
	startedAt             time.Time
	connectionsAttempted  atomic.Uint64
	connectionsSucceeded  atomic.Uint64
	activeConnections     atomic.Int64
	loginsSucceeded       atomic.Uint64
	subscriptions         atomic.Uint64
	publishesAttempted    atomic.Uint64
	publishesAcknowledged atomic.Uint64
	deliveries            atomic.Uint64
	ackLatency            latencyHistogram
	deliveryLatency       latencyHistogram
	errorLock             sync.Mutex
	errors                map[string]uint64
}

// 指标集合创建函数会初始化一个执行节点的累计指标。
func NewMetrics(runID, workerID string) *Metrics {
	return &Metrics{
		runID:     runID,
		workerID:  workerID,
		startedAt: time.Now().UTC(),
		errors:    make(map[string]uint64),
	}
}

func (metrics *Metrics) RecordError(category string) {
	metrics.errorLock.Lock()
	metrics.errors[category]++
	metrics.errorLock.Unlock()
}

func (metrics *Metrics) Snapshot(completed bool, fatalError string) MetricsSnapshot {
	errorsCopy := make(map[string]uint64)
	metrics.errorLock.Lock()
	for category, count := range metrics.errors {
		errorsCopy[category] = count
	}
	metrics.errorLock.Unlock()

	return MetricsSnapshot{
		RunID:                 metrics.runID,
		WorkerID:              metrics.workerID,
		StartedAt:             metrics.startedAt,
		UpdatedAt:             time.Now().UTC(),
		Completed:             completed,
		FatalError:            fatalError,
		ConnectionsAttempted:  metrics.connectionsAttempted.Load(),
		ConnectionsSucceeded:  metrics.connectionsSucceeded.Load(),
		ActiveConnections:     metrics.activeConnections.Load(),
		LoginsSucceeded:       metrics.loginsSucceeded.Load(),
		Subscriptions:         metrics.subscriptions.Load(),
		PublishesAttempted:    metrics.publishesAttempted.Load(),
		PublishesAcknowledged: metrics.publishesAcknowledged.Load(),
		Deliveries:            metrics.deliveries.Load(),
		Errors:                errorsCopy,
		AckLatency:            metrics.ackLatency.Snapshot(),
		DeliveryLatency:       metrics.deliveryLatency.Snapshot(),
	}
}

// 指标快照合并会汇总所有执行节点的最新累计值。
func MergeMetricsSnapshots(snapshots ...MetricsSnapshot) MetricsSnapshot {
	result := MetricsSnapshot{
		WorkerID:  "aggregate",
		Errors:    make(map[string]uint64),
		Completed: len(snapshots) > 0,
	}
	ackSnapshots := make([]HistogramSnapshot, 0, len(snapshots))
	deliverySnapshots := make([]HistogramSnapshot, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if result.RunID == "" {
			result.RunID = snapshot.RunID
		}
		if result.StartedAt.IsZero() || (!snapshot.StartedAt.IsZero() && snapshot.StartedAt.Before(result.StartedAt)) {
			result.StartedAt = snapshot.StartedAt
		}
		if snapshot.UpdatedAt.After(result.UpdatedAt) {
			result.UpdatedAt = snapshot.UpdatedAt
		}
		result.Completed = result.Completed && snapshot.Completed
		if snapshot.FatalError != "" {
			if result.FatalError != "" {
				result.FatalError += "; "
			}
			result.FatalError += snapshot.WorkerID + ": " + snapshot.FatalError
		}
		result.ConnectionsAttempted += snapshot.ConnectionsAttempted
		result.ConnectionsSucceeded += snapshot.ConnectionsSucceeded
		result.ActiveConnections += snapshot.ActiveConnections
		result.LoginsSucceeded += snapshot.LoginsSucceeded
		result.Subscriptions += snapshot.Subscriptions
		result.PublishesAttempted += snapshot.PublishesAttempted
		result.PublishesAcknowledged += snapshot.PublishesAcknowledged
		result.Deliveries += snapshot.Deliveries
		for category, count := range snapshot.Errors {
			result.Errors[category] += count
		}
		ackSnapshots = append(ackSnapshots, snapshot.AckLatency)
		deliverySnapshots = append(deliverySnapshots, snapshot.DeliveryLatency)
	}
	result.AckLatency = MergeHistogramSnapshots(ackSnapshots...)
	result.DeliveryLatency = MergeHistogramSnapshots(deliverySnapshots...)
	return result
}

// 报告编码会生成适合保存和持续集成处理的缩进结构化数据。
func MarshalReport(snapshot MetricsSnapshot) ([]byte, error) {
	return json.MarshalIndent(struct {
		MetricsSnapshot
		AckP50      string `json:"ack_p50"`
		AckP95      string `json:"ack_p95"`
		AckP99      string `json:"ack_p99"`
		DeliveryP50 string `json:"delivery_p50"`
		DeliveryP95 string `json:"delivery_p95"`
		DeliveryP99 string `json:"delivery_p99"`
	}{
		MetricsSnapshot: snapshot,
		AckP50:          snapshot.AckLatency.Percentile(0.50).String(),
		AckP95:          snapshot.AckLatency.Percentile(0.95).String(),
		AckP99:          snapshot.AckLatency.Percentile(0.99).String(),
		DeliveryP50:     snapshot.DeliveryLatency.Percentile(0.50).String(),
		DeliveryP95:     snapshot.DeliveryLatency.Percentile(0.95).String(),
		DeliveryP99:     snapshot.DeliveryLatency.Percentile(0.99).String(),
	}, "", "  ")
}

// 摘要格式化会生成运行期间便于阅读的单行文本。
func FormatSummary(snapshot MetricsSnapshot) string {
	return fmt.Sprintf(
		"连接=%d/%d 活跃=%d 登录=%d 订阅=%d 发布=%d/%d 投递=%d 错误=%d ACK-P99=%s 投递-P99=%s",
		snapshot.ConnectionsSucceeded,
		snapshot.ConnectionsAttempted,
		snapshot.ActiveConnections,
		snapshot.LoginsSucceeded,
		snapshot.Subscriptions,
		snapshot.PublishesAcknowledged,
		snapshot.PublishesAttempted,
		snapshot.Deliveries,
		totalErrors(snapshot.Errors),
		snapshot.AckLatency.Percentile(0.99),
		snapshot.DeliveryLatency.Percentile(0.99),
	)
}

func totalErrors(errorsByCategory map[string]uint64) uint64 {
	var total uint64
	for _, count := range errorsByCategory {
		total += count
	}
	return total
}
