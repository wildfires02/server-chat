package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"chat/server/store"
)

const (
	// defaultLivePath 是容器或进程管理器使用的存活探针。
	defaultLivePath = "/livez"
	// defaultReadyPath 是负载均衡器使用的接流探针。
	defaultReadyPath = "/readyz"
	// defaultDrainPath 是仅允许本机触发的摘流入口。
	defaultDrainPath = "/drainz"
	// defaultTopologyPath 是仅允许本机提交在线扩缩容拓扑的运维入口。
	defaultTopologyPath = "/clusterz"
	// defaultDrainTimeout 是终止信号到来后等待可靠队列排空的最长时间。
	defaultDrainTimeout = 15 * time.Second
	// serviceDatabaseCheckInterval 控制数据库主动健康检查频率。
	serviceDatabaseCheckInterval = 2 * time.Second
	// serviceReliableQueueHighWatermark 是 Readiness 拒绝接流的可靠队列水位。
	serviceReliableQueueHighWatermark = 0.8
)

// serviceState 描述进程接流生命周期。
type serviceState int32

const (
	// serviceStateStarting 表示依赖仍在初始化。
	serviceStateStarting serviceState = iota
	// serviceStateServing 表示允许根据动态依赖状态接流。
	serviceStateServing
	// serviceStateDraining 表示已经摘流并拒绝新的写请求。
	serviceStateDraining
	// serviceStateStopped 表示进程服务组件已经停止。
	serviceStateStopped
)

// String 返回健康接口使用的稳定状态名称。
func (state serviceState) String() string {
	switch state {
	case serviceStateStarting:
		return "starting"
	case serviceStateServing:
		return "serving"
	case serviceStateDraining:
		return "draining"
	case serviceStateStopped:
		return "stopped"
	default:
		return "unknown"
	}
}

// healthConfig 保存 HTTP 探针和优雅摘流参数。
type healthConfig struct {
	// LivePath 是存活探针路径。
	LivePath string `json:"live_path"`
	// ReadyPath 是接流探针路径。
	ReadyPath string `json:"ready_path"`
	// DrainPath 是本机摘流入口路径。
	DrainPath string `json:"drain_path"`
	// TopologyPath 是本机在线扩缩容入口路径。
	TopologyPath string `json:"topology_path"`
	// DrainTimeout 是收到终止信号后等待可靠队列排空的时长。
	DrainTimeout string `json:"drain_timeout"`
}

// normalizedHealthConfig 保存校验后的探针路径和强类型超时。
type normalizedHealthConfig struct {
	// LivePath 是规范化后的存活探针路径。
	LivePath string
	// ReadyPath 是规范化后的就绪探针路径。
	ReadyPath string
	// DrainPath 是规范化后的摘流路径。
	DrainPath string
	// TopologyPath 是规范化后的在线扩缩容路径。
	TopologyPath string
	// DrainTimeout 是可靠队列最大排空时间。
	DrainTimeout time.Duration
}

// serviceHealth 聚合进程状态、数据库、控制面和数据面健康状态。
type serviceHealth struct {
	// state 是当前接流生命周期。
	state atomic.Int32
	// databaseHealthy 保存最近一次主动检查结果。
	databaseHealthy atomic.Bool
	// databaseError 保存最近一次数据库错误文本。
	databaseError atomic.Value
	// databaseCheck 执行不会修改业务数据的数据库版本查询。
	databaseCheck func() error
	// config 保存路径和 Drain 超时。
	config normalizedHealthConfig
	// context 控制数据库检查协程。
	context context.Context
	// cancel 停止数据库检查协程。
	cancel context.CancelFunc
	// waitGroup 等待数据库检查协程结束。
	waitGroup sync.WaitGroup
	// stopOnce 保证健康管理器只停止一次。
	stopOnce sync.Once
}

// healthHTTPResponse 是探针返回的稳定 JSON 结构。
type healthHTTPResponse struct {
	// Status 是 ok 或 unavailable。
	Status string `json:"status"`
	// State 是 starting、serving、draining 或 stopped。
	State string `json:"state"`
	// Node 是当前集群节点名称，单机模式为空。
	Node string `json:"node,omitempty"`
	// ClusterEpoch 是当前已提交的 Cluster View 版本。
	ClusterEpoch int64 `json:"cluster_epoch,omitempty"`
	// RingSignature 是当前一致性哈希视图签名。
	RingSignature string `json:"ring_signature,omitempty"`
	// Reasons 列出未就绪原因；就绪时省略。
	Reasons []string `json:"reasons,omitempty"`
	// Timestamp 是探针生成时间。
	Timestamp time.Time `json:"timestamp"`
}

// topologyChangeRequest 是本机扩缩容入口接受的最小请求结构。
type topologyChangeRequest struct {
	// Members 是希望进入活动拓扑的完整节点名称集合。
	Members []string `json:"members"`
}

// topologyChangeResponse 返回已经提交并在本机应用的拓扑版本。
type topologyChangeResponse struct {
	// Status 是 accepted 或 rejected。
	Status string `json:"status"`
	// ClusterEpoch 是完成数据库 fence 后的视图版本。
	ClusterEpoch int64 `json:"cluster_epoch,omitempty"`
	// ExpectedReplicas 是新活动拓扑副本数。
	ExpectedReplicas int `json:"expected_replicas,omitempty"`
	// Members 是新 Owner Ring 的稳定排序节点名称。
	Members []string `json:"members,omitempty"`
	// Error 是拒绝原因。
	Error string `json:"error,omitempty"`
	// Timestamp 是响应生成时间。
	Timestamp time.Time `json:"timestamp"`
}

// normalizeHealthConfig 校验并补齐健康接口配置。
func normalizeHealthConfig(config healthConfig) (normalizedHealthConfig, error) {
	normalized := normalizedHealthConfig{
		LivePath:     strings.TrimSpace(config.LivePath),
		ReadyPath:    strings.TrimSpace(config.ReadyPath),
		DrainPath:    strings.TrimSpace(config.DrainPath),
		TopologyPath: strings.TrimSpace(config.TopologyPath),
	}
	if normalized.LivePath == "" {
		normalized.LivePath = defaultLivePath
	}
	if normalized.ReadyPath == "" {
		normalized.ReadyPath = defaultReadyPath
	}
	if normalized.DrainPath == "" {
		normalized.DrainPath = defaultDrainPath
	}
	if normalized.TopologyPath == "" {
		normalized.TopologyPath = defaultTopologyPath
	}
	paths := []string{
		normalized.LivePath,
		normalized.ReadyPath,
		normalized.DrainPath,
		normalized.TopologyPath,
	}
	seen := make(map[string]struct{}, len(paths))
	for _, endpoint := range paths {
		if !strings.HasPrefix(endpoint, "/") || path.Clean(endpoint) != endpoint {
			return normalizedHealthConfig{}, fmt.Errorf(
				"health 探针路径 %q 必须是规范化的绝对 HTTP 路径",
				endpoint,
			)
		}
		if _, duplicated := seen[endpoint]; duplicated {
			return normalizedHealthConfig{}, fmt.Errorf("health 探针路径不能重复: %s", endpoint)
		}
		seen[endpoint] = struct{}{}
	}

	normalized.DrainTimeout = defaultDrainTimeout
	if strings.TrimSpace(config.DrainTimeout) != "" {
		parsed, err := time.ParseDuration(config.DrainTimeout)
		if err != nil {
			return normalizedHealthConfig{}, fmt.Errorf("解析 health.drain_timeout 失败: %w", err)
		}
		if parsed < time.Second || parsed > 5*time.Minute {
			return normalizedHealthConfig{}, fmt.Errorf(
				"health.drain_timeout 必须在 1s～5m 之间")
		}
		normalized.DrainTimeout = parsed
	}
	return normalized, nil
}

// newServiceHealth 创建初始状态为 starting 的健康管理器。
func newServiceHealth(
	config normalizedHealthConfig,
	databaseCheck func() error,
) *serviceHealth {
	healthContext, cancel := context.WithCancel(context.Background())
	health := &serviceHealth{
		config:        config,
		databaseCheck: databaseCheck,
		context:       healthContext,
		cancel:        cancel,
	}
	health.state.Store(int32(serviceStateStarting))
	health.databaseError.Store("")
	return health
}

// Start 执行首次数据库检查并启动周期检查协程。
func (health *serviceHealth) Start() {
	if health == nil {
		return
	}
	health.checkDatabase()
	health.waitGroup.Add(1)
	go func() {
		defer health.waitGroup.Done()
		ticker := time.NewTicker(serviceDatabaseCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				health.checkDatabase()
			case <-health.context.Done():
				return
			}
		}
	}()
}

// Stop 终止检查协程并把 Liveness 切换为 stopped。
func (health *serviceHealth) Stop() {
	if health == nil {
		return
	}
	health.stopOnce.Do(func() {
		health.state.Store(int32(serviceStateStopped))
		health.cancel()
		health.waitGroup.Wait()
	})
}

// MarkServing 表示启动组件已完成，动态 Readiness 可以开始接流。
func (health *serviceHealth) MarkServing() {
	if health != nil {
		health.state.CompareAndSwap(int32(serviceStateStarting), int32(serviceStateServing))
	}
}

// BeginDrain 原子摘除 Readiness，并保持幂等。
func (health *serviceHealth) BeginDrain() bool {
	if health == nil {
		return false
	}
	return health.state.CompareAndSwap(int32(serviceStateServing), int32(serviceStateDraining))
}

// State 返回当前生命周期状态。
func (health *serviceHealth) State() serviceState {
	if health == nil {
		return serviceStateServing
	}
	return serviceState(health.state.Load())
}

// Live 只判断进程是否仍应保持运行。
func (health *serviceHealth) Live() bool {
	return health != nil && health.State() != serviceStateStopped
}

// Ready 汇总数据库、控制面、Ring 和可靠队列状态。
func (health *serviceHealth) Ready() (bool, []string) {
	if health == nil {
		return true, nil
	}
	var reasons []string
	state := health.State()
	if state != serviceStateServing {
		reasons = append(reasons, "service_state="+state.String())
	}
	if !health.databaseHealthy.Load() {
		reason := "database_unavailable"
		if message, _ := health.databaseError.Load().(string); message != "" {
			reason += ": " + message
		}
		reasons = append(reasons, reason)
	}

	cluster := globals.cluster
	if cluster != nil {
		if cluster.ringSnapshot() == nil {
			reasons = append(reasons, "cluster_ring_unavailable")
		}
		if cluster.controlPlane != nil {
			if !cluster.controlPlane.Ready() {
				reasons = append(reasons, "cluster_quorum_unavailable")
			}
			if cluster.viewEpoch.Load() <= 0 {
				reasons = append(reasons, "cluster_view_uncommitted")
			}
		} else if cluster.isPartitioned() {
			reasons = append(reasons, "cluster_partitioned")
		}
		if cluster.transportConfig != nil {
			if cluster.grpcTransport == nil {
				reasons = append(reasons, "cluster_transport_unavailable")
			} else if cluster.grpcTransport.maxReliableQueueUtilization() >=
				serviceReliableQueueHighWatermark {
				reasons = append(reasons, "cluster_reliable_queue_high")
			}
		}
		if cluster.tlsConfig != nil {
			if cluster.tlsMaterial == nil {
				reasons = append(reasons, "cluster_tls_unavailable")
			} else if err := cluster.tlsMaterial.readinessError(); err != nil {
				reasons = append(reasons, "cluster_tls_unhealthy: "+err.Error())
			}
		}
	}
	sort.Strings(reasons)
	return len(reasons) == 0, reasons
}

// AcceptingConnections 表示允许创建新的 WebSocket、Long Polling 或 gRPC Session。
func (health *serviceHealth) AcceptingConnections() bool {
	ready, _ := health.Ready()
	return ready
}

// AllowsWrites 表示已有连接当前可以执行状态变更。
func (health *serviceHealth) AllowsWrites() bool {
	ready, _ := health.Ready()
	return ready
}

// DrainTimeout 返回收到终止信号后的可靠队列排空上限。
func (health *serviceHealth) DrainTimeout() time.Duration {
	if health == nil {
		return defaultDrainTimeout
	}
	return health.config.DrainTimeout
}

// checkDatabase 更新最近一次数据库主动检查结果。
func (health *serviceHealth) checkDatabase() {
	if health.databaseCheck == nil {
		health.databaseHealthy.Store(true)
		health.databaseError.Store("")
		return
	}
	if err := health.databaseCheck(); err != nil {
		health.databaseHealthy.Store(false)
		health.databaseError.Store(err.Error())
		return
	}
	health.databaseHealthy.Store(true)
	health.databaseError.Store("")
}

// defaultDatabaseHealthCheck 使用适配器的版本查询验证连接和 Schema 可用。
func defaultDatabaseHealthCheck() error {
	if store.Store == nil || !store.Store.IsOpen() || store.Store.GetAdapter() == nil {
		return fmt.Errorf("数据库适配器未打开")
	}
	return store.Store.GetAdapter().CheckDbVersion()
}

// registerServiceHealthHandlers 注册固定用途的 Liveness、Readiness 和 Drain 接口。
func registerServiceHealthHandlers(mux *http.ServeMux, health *serviceHealth) {
	mux.HandleFunc(health.config.LivePath, health.serveLive)
	mux.HandleFunc(health.config.ReadyPath, health.serveReady)
	mux.HandleFunc(health.config.DrainPath, health.serveDrain)
	mux.HandleFunc(health.config.TopologyPath, health.serveTopology)
}

// serveLive 返回进程存活状态。
func (health *serviceHealth) serveLive(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		writeHealthResponse(writer, http.StatusMethodNotAllowed, health.response(
			false,
			[]string{"method_not_allowed"},
		))
		return
	}
	live := health.Live()
	statusCode := http.StatusOK
	if !live {
		statusCode = http.StatusServiceUnavailable
	}
	writeHealthResponse(writer, statusCode, health.response(live, nil))
}

// serveReady 返回是否允许负载均衡器继续转发新连接。
func (health *serviceHealth) serveReady(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		writeHealthResponse(writer, http.StatusMethodNotAllowed, health.response(
			false,
			[]string{"method_not_allowed"},
		))
		return
	}
	ready, reasons := health.Ready()
	statusCode := http.StatusOK
	if !ready {
		statusCode = http.StatusServiceUnavailable
	}
	writeHealthResponse(writer, statusCode, health.response(ready, reasons))
}

// serveDrain 仅允许本机 POST 请求把节点切换为 draining。
func (health *serviceHealth) serveDrain(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", "POST")
		writeHealthResponse(writer, http.StatusMethodNotAllowed, health.response(
			false,
			[]string{"method_not_allowed"},
		))
		return
	}
	if !isLoopbackRemoteAddress(request.RemoteAddr) {
		writeHealthResponse(writer, http.StatusForbidden, health.response(
			false,
			[]string{"drain_requires_loopback"},
		))
		return
	}
	health.BeginDrain()
	drainContext, cancel := context.WithTimeout(
		request.Context(),
		defaultControlPlaneDialTimeout,
	)
	err := globals.cluster.beginDrain(drainContext)
	cancel()
	if err != nil {
		writeHealthResponse(writer, http.StatusServiceUnavailable, health.response(
			false,
			[]string{"drain_control_plane_failed: " + err.Error()},
		))
		return
	}
	writeHealthResponse(writer, http.StatusAccepted, health.response(
		true,
		[]string{"drain_started"},
	))
}

// serveTopology 仅允许本机 POST 一份完整活动成员列表。
// 入口依赖 Pod exec 或主机本地权限，不应由 Service、Ingress 或公网暴露。
func (health *serviceHealth) serveTopology(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", "POST")
		writeTopologyResponse(writer, http.StatusMethodNotAllowed, topologyChangeResponse{
			Status:    "rejected",
			Error:     "method_not_allowed",
			Timestamp: time.Now().UTC().Round(time.Millisecond),
		})
		return
	}
	if !isLoopbackRemoteAddress(request.RemoteAddr) {
		writeTopologyResponse(writer, http.StatusForbidden, topologyChangeResponse{
			Status:    "rejected",
			Error:     "topology_change_requires_loopback",
			Timestamp: time.Now().UTC().Round(time.Millisecond),
		})
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 8<<10))
	decoder.DisallowUnknownFields()
	var payload topologyChangeRequest
	if err := decoder.Decode(&payload); err != nil {
		writeTopologyResponse(writer, http.StatusBadRequest, topologyChangeResponse{
			Status:    "rejected",
			Error:     "invalid_request: " + err.Error(),
			Timestamp: time.Now().UTC().Round(time.Millisecond),
		})
		return
	}
	if len(payload.Members) == 0 {
		writeTopologyResponse(writer, http.StatusBadRequest, topologyChangeResponse{
			Status:    "rejected",
			Error:     "members_required",
			Timestamp: time.Now().UTC().Round(time.Millisecond),
		})
		return
	}
	changeContext, cancel := context.WithTimeout(
		request.Context(),
		defaultControlPlaneDialTimeout,
	)
	view, err := globals.cluster.changeTopology(changeContext, payload.Members)
	cancel()
	if err != nil {
		writeTopologyResponse(writer, http.StatusConflict, topologyChangeResponse{
			Status:    "rejected",
			Error:     err.Error(),
			Timestamp: time.Now().UTC().Round(time.Millisecond),
		})
		return
	}
	writeTopologyResponse(writer, http.StatusAccepted, topologyChangeResponse{
		Status:           "accepted",
		ClusterEpoch:     view.Epoch,
		ExpectedReplicas: view.ExpectedReplicas,
		Members:          view.memberNames(),
		Timestamp:        time.Now().UTC().Round(time.Millisecond),
	})
}

// response 生成包含节点和 Cluster View 的探针响应。
func (health *serviceHealth) response(ok bool, reasons []string) healthHTTPResponse {
	response := healthHTTPResponse{
		Status:    "unavailable",
		State:     health.State().String(),
		Reasons:   reasons,
		Timestamp: time.Now().UTC().Round(time.Millisecond),
	}
	if ok {
		response.Status = "ok"
	}
	if cluster := globals.cluster; cluster != nil {
		response.Node = cluster.thisNodeName
		response.ClusterEpoch = cluster.viewEpoch.Load()
		if cluster.ringSnapshot() != nil {
			response.RingSignature = cluster.ringSignature()
		}
	}
	return response
}

// writeHealthResponse 写入禁止缓存的 JSON 探针响应。
func writeHealthResponse(
	writer http.ResponseWriter,
	statusCode int,
	response healthHTTPResponse,
) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(statusCode)
	_ = json.NewEncoder(writer).Encode(response)
}

// writeTopologyResponse 写入禁止缓存的扩缩容 JSON 响应。
func writeTopologyResponse(
	writer http.ResponseWriter,
	statusCode int,
	response topologyChangeResponse,
) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(statusCode)
	_ = json.NewEncoder(writer).Encode(response)
}

// isLoopbackRemoteAddress 验证 Drain 调用来自 IPv4 或 IPv6 回环地址。
func isLoopbackRemoteAddress(remoteAddress string) bool {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		host = remoteAddress
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// serviceAcceptingConnections 兼容尚未初始化健康管理器的单元测试路径。
func serviceAcceptingConnections() bool {
	return globals.health == nil || globals.health.AcceptingConnections()
}

// serviceAllowsWrites 兼容旧开发模式，同时对生产健康状态执行 fail-closed。
func serviceAllowsWrites() bool {
	if globals.health != nil {
		return globals.health.AllowsWrites()
	}
	return !globals.cluster.isPartitioned()
}

// clientMessageRequiresWrite 判断客户端命令是否会改变持久化或集群会话状态。
func clientMessageRequiresWrite(message *ClientComMessage) bool {
	if message == nil {
		return false
	}
	if message.Pub != nil ||
		message.Sub != nil ||
		message.Leave != nil ||
		message.Set != nil ||
		message.Del != nil ||
		message.Acc != nil {
		return true
	}
	return message.Note != nil && message.Note.What != "kp"
}
