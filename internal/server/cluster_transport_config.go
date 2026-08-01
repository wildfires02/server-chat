package server

import (
	"fmt"
	"strings"
	"time"
)

const (
	// defaultClusterLaneCount 在吞吐和连接数量之间提供保守默认值。
	defaultClusterLaneCount = 8
	// defaultClusterReliableQueueCapacity 是每条 Lane 可等待的可靠请求数量。
	defaultClusterReliableQueueCapacity = 512
	// defaultClusterEphemeralQueueCapacity 预留给 CLUSTER-005 的瞬态事件队列。
	defaultClusterEphemeralQueueCapacity = 128
	// defaultClusterPipelineWindow 是单条 Lane 允许同时在途的请求数。
	defaultClusterPipelineWindow = 32
	// defaultClusterDialTimeout 是建立节点 gRPC 连接的最长等待时间。
	defaultClusterDialTimeout = 3 * time.Second
	// defaultClusterRequestTimeout 是一次节点间可靠请求的最长等待时间。
	defaultClusterRequestTimeout = 5 * time.Second
	// defaultClusterMaxRetries 是断流等可重试错误的额外尝试次数。
	defaultClusterMaxRetries = 2
	// defaultClusterRetryBackoff 是两次可靠投递之间的初始退避时间。
	defaultClusterRetryBackoff = 100 * time.Millisecond
	// defaultClusterDedupeCapacity 是本节点缓存的已处理 Request ID 上限。
	defaultClusterDedupeCapacity = 65536
	// defaultClusterDedupeTTL 覆盖正常重连和短暂网络抖动的去重时间窗。
	defaultClusterDedupeTTL = 2 * time.Minute
)

// clusterTransportConfig 保存节点间 gRPC 双向流式 Lane 参数。
type clusterTransportConfig struct {
	// Listen 是本节点数据面监听地址；为空时复用 cluster_config.nodes 中的本节点地址。
	Listen string `json:"listen"`
	// LaneCount 是每个远端节点的独立有序流数量，必须为 2 的幂。
	LaneCount int `json:"lane_count"`
	// ReliableQueueCapacity 是每条 Lane 的可靠请求等待队列容量。
	ReliableQueueCapacity int `json:"reliable_queue_capacity"`
	// EphemeralQueueCapacity 是后续瞬态事件队列容量，目前只做配置门禁。
	EphemeralQueueCapacity int `json:"ephemeral_queue_capacity"`
	// PipelineWindow 是每条双向流允许尚未收到响应的最大请求数。
	PipelineWindow int `json:"pipeline_window"`
	// DialTimeout 使用 Go duration 字符串，例如 3s。
	DialTimeout string `json:"dial_timeout"`
	// RequestTimeout 使用 Go duration 字符串，例如 5s。
	RequestTimeout string `json:"request_timeout"`
	// MaxRetries 是可靠请求发生可重试传输错误后的额外尝试次数。
	MaxRetries int `json:"max_retries"`
	// RetryBackoff 是可靠请求重试前的初始指数退避时间。
	RetryBackoff string `json:"retry_backoff"`
	// DedupeCapacity 是服务端 Request ID 去重窗口的最大记录数。
	DedupeCapacity int `json:"dedupe_capacity"`
	// DedupeTTL 是一个 Request ID 保留在去重窗口中的时长。
	DedupeTTL string `json:"dedupe_ttl"`
}

// normalizedClusterTransport 保存已经解析为强类型 duration 的运行时参数。
type normalizedClusterTransport struct {
	// Listen 是当前节点 gRPC 数据面监听地址。
	Listen string
	// LaneCount 是每个远端节点的独立有序流数量。
	LaneCount int
	// ReliableQueueCapacity 是每条 Lane 的可靠请求容量。
	ReliableQueueCapacity int
	// EphemeralQueueCapacity 是每条 Lane 的瞬态事件容量。
	EphemeralQueueCapacity int
	// PipelineWindow 是每条 Lane 的最大同时在途请求数。
	PipelineWindow int
	// DialTimeout 是连接远端节点的最长等待时间。
	DialTimeout time.Duration
	// RequestTimeout 是一次调用包含排队和重试的总超时。
	RequestTimeout time.Duration
	// MaxRetries 是一次可靠请求允许的额外尝试次数。
	MaxRetries int
	// RetryBackoff 是第一次可靠重试前的等待时间。
	RetryBackoff time.Duration
	// DedupeCapacity 是入站去重窗口的最大记录数。
	DedupeCapacity int
	// DedupeTTL 是已完成请求的去重保留时间。
	DedupeTTL time.Duration
}

// normalizeClusterTransportConfig 校验并补齐 gRPC Lane 配置默认值。
func normalizeClusterTransportConfig(config clusterTransportConfig) (normalizedClusterTransport, error) {
	normalized := normalizedClusterTransport{
		Listen:                 strings.TrimSpace(config.Listen),
		LaneCount:              config.LaneCount,
		ReliableQueueCapacity:  config.ReliableQueueCapacity,
		EphemeralQueueCapacity: config.EphemeralQueueCapacity,
		PipelineWindow:         config.PipelineWindow,
		MaxRetries:             config.MaxRetries,
		DedupeCapacity:         config.DedupeCapacity,
	}
	if normalized.LaneCount == 0 {
		normalized.LaneCount = defaultClusterLaneCount
	}
	if normalized.LaneCount < 1 || normalized.LaneCount > 64 ||
		normalized.LaneCount&(normalized.LaneCount-1) != 0 {
		return normalizedClusterTransport{}, fmt.Errorf("cluster transport lane_count 必须是 1～64 之间的 2 的幂")
	}
	if normalized.ReliableQueueCapacity == 0 {
		normalized.ReliableQueueCapacity = defaultClusterReliableQueueCapacity
	}
	if normalized.ReliableQueueCapacity < 16 || normalized.ReliableQueueCapacity > 65536 {
		return normalizedClusterTransport{}, fmt.Errorf(
			"cluster transport reliable_queue_capacity 必须在 16～65536 之间")
	}
	if normalized.EphemeralQueueCapacity == 0 {
		normalized.EphemeralQueueCapacity = defaultClusterEphemeralQueueCapacity
	}
	if normalized.EphemeralQueueCapacity < 16 || normalized.EphemeralQueueCapacity > 65536 {
		return normalizedClusterTransport{}, fmt.Errorf(
			"cluster transport ephemeral_queue_capacity 必须在 16～65536 之间")
	}
	if normalized.PipelineWindow == 0 {
		normalized.PipelineWindow = defaultClusterPipelineWindow
	}
	if normalized.PipelineWindow < 2 || normalized.PipelineWindow > 1024 {
		return normalizedClusterTransport{}, fmt.Errorf(
			"cluster transport pipeline_window 必须在 2～1024 之间")
	}

	var err error
	normalized.DialTimeout, err = parseClusterTransportDuration(
		config.DialTimeout, defaultClusterDialTimeout, "dial_timeout")
	if err != nil {
		return normalizedClusterTransport{}, err
	}
	normalized.RequestTimeout, err = parseClusterTransportDuration(
		config.RequestTimeout, defaultClusterRequestTimeout, "request_timeout")
	if err != nil {
		return normalizedClusterTransport{}, err
	}
	if normalized.RequestTimeout < normalized.DialTimeout {
		return normalizedClusterTransport{}, fmt.Errorf(
			"cluster transport request_timeout 不能小于 dial_timeout")
	}
	if normalized.MaxRetries == 0 {
		normalized.MaxRetries = defaultClusterMaxRetries
	}
	if normalized.MaxRetries < 1 || normalized.MaxRetries > 8 {
		return normalizedClusterTransport{}, fmt.Errorf(
			"cluster transport max_retries 必须在 1～8 之间，0 表示使用默认值")
	}
	normalized.RetryBackoff, err = parseClusterTransportDuration(
		config.RetryBackoff, defaultClusterRetryBackoff, "retry_backoff")
	if err != nil {
		return normalizedClusterTransport{}, err
	}
	if normalized.RetryBackoff >= normalized.RequestTimeout {
		return normalizedClusterTransport{}, fmt.Errorf(
			"cluster transport retry_backoff 必须小于 request_timeout")
	}
	if normalized.DedupeCapacity == 0 {
		normalized.DedupeCapacity = defaultClusterDedupeCapacity
	}
	if normalized.DedupeCapacity < 1024 || normalized.DedupeCapacity > 1048576 {
		return normalizedClusterTransport{}, fmt.Errorf(
			"cluster transport dedupe_capacity 必须在 1024～1048576 之间")
	}
	normalized.DedupeTTL, err = parseClusterDedupeTTL(config.DedupeTTL)
	if err != nil {
		return normalizedClusterTransport{}, err
	}
	if normalized.DedupeTTL < normalized.RequestTimeout {
		return normalizedClusterTransport{}, fmt.Errorf(
			"cluster transport dedupe_ttl 不能小于 request_timeout")
	}
	return normalized, nil
}

// parseClusterTransportDuration 解析并限制节点间网络超时时间。
func parseClusterTransportDuration(value string, fallback time.Duration, field string) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("解析 cluster transport %s 失败: %w", field, err)
	}
	if parsed < 100*time.Millisecond || parsed > time.Minute {
		return 0, fmt.Errorf("cluster transport %s 必须在 100ms～1m 之间", field)
	}
	return parsed, nil
}

// parseClusterDedupeTTL 解析服务端去重窗口时长。
func parseClusterDedupeTTL(value string) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return defaultClusterDedupeTTL, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("解析 cluster transport dedupe_ttl 失败: %w", err)
	}
	if parsed < 10*time.Second || parsed > 10*time.Minute {
		return 0, fmt.Errorf("cluster transport dedupe_ttl 必须在 10s～10m 之间")
	}
	return parsed, nil
}
