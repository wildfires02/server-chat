package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestNormalizeHealthConfig 验证默认路径、重复路径和 Drain 超时边界。
func TestNormalizeHealthConfig(t *testing.T) {
	config, err := normalizeHealthConfig(healthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if config.LivePath != defaultLivePath ||
		config.ReadyPath != defaultReadyPath ||
		config.DrainPath != defaultDrainPath ||
		config.TopologyPath != defaultTopologyPath ||
		config.DrainTimeout != defaultDrainTimeout {
		t.Fatalf("默认健康配置不正确：%+v", config)
	}

	_, err = normalizeHealthConfig(healthConfig{
		LivePath:  "/health",
		ReadyPath: "/health",
	})
	if err == nil || !strings.Contains(err.Error(), "不能重复") {
		t.Fatalf("重复路径校验结果 = %v", err)
	}
	_, err = normalizeHealthConfig(healthConfig{DrainTimeout: "500ms"})
	if err == nil || !strings.Contains(err.Error(), "1s") {
		t.Fatalf("过短 Drain 超时校验结果 = %v", err)
	}
}

// topologyTestControlPlane 为本机扩缩容 HTTP 接口返回可预测的新视图。
type topologyTestControlPlane struct {
	staticControlPlane
}

// ChangeTopology 返回请求中的成员集合，模拟已经由 etcd 提交并应用。
func (control *topologyTestControlPlane) ChangeTopology(
	_ context.Context,
	members []string,
) (clusterView, error) {
	view := clusterView{
		Epoch:            88,
		ExpectedReplicas: len(members),
		Members:          make([]clusterMember, 0, len(members)),
	}
	for index, name := range members {
		view.Members = append(view.Members, clusterMember{
			Name:       name,
			InstanceID: int64(index + 1),
			Active:     true,
		})
	}
	return view, nil
}

// TestServiceTopologyEndpoint 验证扩缩容入口只允许本机且返回已提交视图。
func TestServiceTopologyEndpoint(t *testing.T) {
	config, err := normalizeHealthConfig(healthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	health := newServiceHealth(config, func() error { return nil })
	originalCluster := globals.cluster
	globals.cluster = &Cluster{
		controlPlane: &topologyTestControlPlane{
			staticControlPlane{ready: true},
		},
	}
	t.Cleanup(func() {
		globals.cluster = originalCluster
	})
	mux := http.NewServeMux()
	registerServiceHealthHandlers(mux, health)

	body := `{"members":["im-0","im-1","im-2","im-3","im-4"]}`
	remoteRequest := httptest.NewRequest(http.MethodPost, config.TopologyPath, strings.NewReader(body))
	remoteRequest.RemoteAddr = "192.0.2.10:12000"
	remoteResponse := httptest.NewRecorder()
	mux.ServeHTTP(remoteResponse, remoteRequest)
	if remoteResponse.Code != http.StatusForbidden {
		t.Fatalf("远端扩缩容状态码 = %d，期望 403", remoteResponse.Code)
	}

	localRequest := httptest.NewRequest(http.MethodPost, config.TopologyPath, strings.NewReader(body))
	localRequest.RemoteAddr = "127.0.0.1:" + strconv.Itoa(12000)
	localResponse := httptest.NewRecorder()
	mux.ServeHTTP(localResponse, localRequest)
	if localResponse.Code != http.StatusAccepted ||
		!strings.Contains(localResponse.Body.String(), `"cluster_epoch":88`) ||
		!strings.Contains(localResponse.Body.String(), `"expected_replicas":5`) {
		t.Fatalf(
			"本机扩缩容结果 = %d, %s",
			localResponse.Code,
			localResponse.Body.String(),
		)
	}
}

// TestServiceHealthEndpoints 验证 Ready→Drain 状态转换和本机访问限制。
func TestServiceHealthEndpoints(t *testing.T) {
	config, err := normalizeHealthConfig(healthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	health := newServiceHealth(config, func() error { return nil })
	health.Start()
	t.Cleanup(health.Stop)
	health.MarkServing()

	originalCluster := globals.cluster
	globals.cluster = nil
	t.Cleanup(func() {
		globals.cluster = originalCluster
	})

	mux := http.NewServeMux()
	registerServiceHealthHandlers(mux, health)

	readyRequest := httptest.NewRequest(http.MethodGet, config.ReadyPath, nil)
	readyResponse := httptest.NewRecorder()
	mux.ServeHTTP(readyResponse, readyRequest)
	if readyResponse.Code != http.StatusOK {
		t.Fatalf("初始 Readiness = %d, %s", readyResponse.Code, readyResponse.Body.String())
	}

	remoteDrain := httptest.NewRequest(http.MethodPost, config.DrainPath, nil)
	remoteDrain.RemoteAddr = "192.0.2.10:12000"
	remoteResponse := httptest.NewRecorder()
	mux.ServeHTTP(remoteResponse, remoteDrain)
	if remoteResponse.Code != http.StatusForbidden {
		t.Fatalf("远端 Drain 状态码 = %d，期望 403", remoteResponse.Code)
	}

	localDrain := httptest.NewRequest(http.MethodPost, config.DrainPath, nil)
	localDrain.RemoteAddr = "127.0.0.1:12000"
	localResponse := httptest.NewRecorder()
	mux.ServeHTTP(localResponse, localDrain)
	if localResponse.Code != http.StatusAccepted {
		t.Fatalf("本机 Drain 状态码 = %d，期望 202", localResponse.Code)
	}

	readyAfterDrain := httptest.NewRecorder()
	mux.ServeHTTP(readyAfterDrain, readyRequest)
	if readyAfterDrain.Code != http.StatusServiceUnavailable ||
		!strings.Contains(readyAfterDrain.Body.String(), "draining") {
		t.Fatalf(
			"Drain 后 Readiness = %d, %s",
			readyAfterDrain.Code,
			readyAfterDrain.Body.String(),
		)
	}

	liveRequest := httptest.NewRequest(http.MethodGet, config.LivePath, nil)
	liveResponse := httptest.NewRecorder()
	mux.ServeHTTP(liveResponse, liveRequest)
	if liveResponse.Code != http.StatusOK {
		t.Fatalf("Drain 期间 Liveness = %d", liveResponse.Code)
	}
}

// TestServiceHealthReadinessReasons 验证数据库、控制面和可靠队列原因可观测。
func TestServiceHealthReadinessReasons(t *testing.T) {
	config, err := normalizeHealthConfig(healthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var databaseFailure atomic.Bool
	health := newServiceHealth(config, func() error {
		if databaseFailure.Load() {
			return errors.New("database offline")
		}
		return nil
	})
	health.Start()
	t.Cleanup(health.Stop)
	health.MarkServing()

	originalCluster := globals.cluster
	t.Cleanup(func() {
		globals.cluster = originalCluster
	})

	controlPlane := &staticControlPlane{ready: true}
	cluster := &Cluster{
		clusterID:       "cluster-health",
		thisNodeName:    "node-a",
		controlPlane:    controlPlane,
		transportConfig: &clusterTransportConfig{},
		nodes:           make(map[string]*ClusterNode),
	}
	cluster.rehash([]string{"node-a"})
	cluster.viewEpoch.Store(1)
	cluster.grpcTransport = &clusterGRPCTransport{
		peers: make(map[string]*clusterGRPCPeer),
	}
	globals.cluster = cluster

	if ready, reasons := health.Ready(); !ready {
		t.Fatalf("合法集群未就绪：%v", reasons)
	}

	controlPlane.ready = false
	if ready, reasons := health.Ready(); ready ||
		!containsHealthReason(reasons, "cluster_quorum_unavailable") {
		t.Fatalf("控制面异常的 Readiness = %v, %v", ready, reasons)
	}
	controlPlane.ready = true

	databaseFailure.Store(true)
	health.checkDatabase()
	if ready, reasons := health.Ready(); ready ||
		!containsHealthReason(reasons, "database_unavailable") {
		t.Fatalf("数据库异常的 Readiness = %v, %v", ready, reasons)
	}
	databaseFailure.Store(false)
	health.checkDatabase()

	queue := make(chan *clusterLaneRequest, 10)
	for range 8 {
		queue <- &clusterLaneRequest{}
	}
	cluster.grpcTransport.peers["node-b"] = &clusterGRPCPeer{
		lanes: []*clusterGRPCLane{
			{reliableQueue: queue},
		},
	}
	if ready, reasons := health.Ready(); ready ||
		!containsHealthReason(reasons, "cluster_reliable_queue_high") {
		t.Fatalf("可靠队列高水位的 Readiness = %v, %v", ready, reasons)
	}
}

// TestClientMessageRequiresWrite 验证 Drain 期间允许读请求并拒绝状态变更。
func TestClientMessageRequiresWrite(t *testing.T) {
	tests := []struct {
		// name 是测试场景。
		name string
		// message 是待分类的客户端命令。
		message *ClientComMessage
		// want 是是否需要写入许可。
		want bool
	}{
		{name: "读取元数据", message: &ClientComMessage{Get: &MsgClientGet{}}, want: false},
		{name: "发布消息", message: &ClientComMessage{Pub: &MsgClientPub{}}, want: true},
		{name: "修改 ACL", message: &ClientComMessage{Set: &MsgClientSet{}}, want: true},
		{name: "已读状态", message: &ClientComMessage{Note: &MsgClientNote{What: "read"}}, want: true},
		{name: "输入状态", message: &ClientComMessage{Note: &MsgClientNote{What: "kp"}}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if actual := clientMessageRequiresWrite(test.message); actual != test.want {
				t.Fatalf("clientMessageRequiresWrite() = %v，期望 %v", actual, test.want)
			}
		})
	}
}

// containsHealthReason 判断原因列表是否包含指定稳定前缀。
func containsHealthReason(reasons []string, expected string) bool {
	for _, reason := range reasons {
		if strings.HasPrefix(reason, expected) {
			return true
		}
	}
	return false
}

// TestClusterReliableDrain 验证 Drain 会等待可靠在途请求并在超时后失败。
func TestClusterReliableDrain(t *testing.T) {
	lane := &clusterGRPCLane{
		reliableQueue: make(chan *clusterLaneRequest, 1),
	}
	lane.inFlight.Store(1)
	cluster := &Cluster{
		grpcTransport: &clusterGRPCTransport{
			peers: map[string]*clusterGRPCPeer{
				"node-b": {lanes: []*clusterGRPCLane{lane}},
			},
		},
	}

	drainContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if cluster.waitForReliableDrain(drainContext) {
		t.Fatal("存在可靠在途请求时不应立即完成 Drain")
	}
	lane.inFlight.Store(0)
	if !cluster.waitForReliableDrain(context.Background()) {
		t.Fatal("可靠请求排空后 Drain 未完成")
	}
}
