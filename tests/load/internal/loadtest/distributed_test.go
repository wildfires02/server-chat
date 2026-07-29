package loadtest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestControllerDistributesAndAggregatesWork(t *testing.T) {
	t.Parallel()

	workload := validTestWorkload()
	workload.Sessions = 5
	workload.Accounts = []Account{
		{Username: "a", Password: "a"},
		{Username: "b", Password: "b"},
		{Username: "c", Password: "c"},
	}
	config := ControllerConfig{
		Listen:          "127.0.0.1:0",
		ControlToken:    "shared-token",
		ExpectedWorkers: 2,
		StartDelay:      time.Second,
		SummaryInterval: time.Second,
		Workload:        workload,
	}
	controller := newControllerServer(config, nil)
	server := httptest.NewServer(controller.handler())
	defer server.Close()
	client := server.Client()

	registerTestWorker(t, client, server.URL, config.ControlToken, "worker-a")
	registerTestWorker(t, client, server.URL, config.ControlToken, "worker-b")

	first := getTestAssignment(t, client, server.URL, config.ControlToken, "worker-a")
	second := getTestAssignment(t, client, server.URL, config.ControlToken, "worker-b")
	if first.Sessions+second.Sessions != workload.Sessions {
		t.Fatalf("连接数分配合计=%d，期望=%d", first.Sessions+second.Sessions, workload.Sessions)
	}
	if !first.StartAt.Equal(second.StartAt) {
		t.Fatalf("执行节点开始时间不一致: %s 与 %s", first.StartAt, second.StartAt)
	}
	if len(first.Accounts)+len(second.Accounts) != len(workload.Accounts) {
		t.Fatalf("账号分片数量错误: %d 与 %d", len(first.Accounts), len(second.Accounts))
	}

	sendTestReport(t, client, server.URL, config.ControlToken, MetricsSnapshot{
		RunID:                workload.RunID,
		WorkerID:             "worker-a",
		Completed:            true,
		ConnectionsAttempted: 2,
	})
	sendTestReport(t, client, server.URL, config.ControlToken, MetricsSnapshot{
		RunID:                workload.RunID,
		WorkerID:             "worker-b",
		Completed:            true,
		ConnectionsAttempted: 3,
	})

	select {
	case <-controller.done:
	case <-time.After(time.Second):
		t.Fatal("全部执行节点完成后控制器未结束")
	}
	status := controller.status()
	if !status.Aggregate.Completed || status.Aggregate.ConnectionsAttempted != 5 {
		t.Fatalf("汇总指标错误: %#v", status.Aggregate)
	}
}

func TestWorkerConfigRejectsNonHTTPController(t *testing.T) {
	t.Parallel()

	config := WorkerConfig{
		ControllerURL:  "worker.internal",
		WorkerID:       "worker-a",
		PollInterval:   time.Second,
		ReportInterval: time.Second,
		RequestTimeout: time.Second,
	}
	if err := config.Validate(); err == nil {
		t.Fatal("非 HTTP 控制器地址未被拒绝")
	}
}

func TestRegisterWorkerStopsOnAuthenticationFailure(t *testing.T) {
	t.Parallel()

	workload := validTestWorkload()
	config := ControllerConfig{
		ControlToken:    "correct-token",
		ExpectedWorkers: 1,
		Workload:        workload,
	}
	controller := newControllerServer(config, nil)
	server := httptest.NewServer(controller.handler())
	defer server.Close()

	workerConfig := WorkerConfig{
		WorkerID:     "worker-a",
		ControlToken: "wrong-token",
		PollInterval: time.Hour,
	}
	startedAt := time.Now()
	err := registerWorker(
		context.Background(),
		server.Client(),
		server.URL,
		workerConfig,
	)
	if err == nil {
		t.Fatal("错误令牌未导致注册失败")
	}
	if time.Since(startedAt) > time.Second {
		t.Fatalf("认证失败后仍进行了重试: %s", time.Since(startedAt))
	}
}

func registerTestWorker(
	t *testing.T,
	client *http.Client,
	baseURL string,
	token string,
	workerID string,
) {
	t.Helper()
	var response registerResponse
	if err := controlRequest(
		context.Background(),
		client,
		http.MethodPost,
		baseURL+controlAPIPrefix+"/workers/register",
		token,
		registerRequest{WorkerID: workerID},
		&response,
	); err != nil {
		t.Fatalf("注册执行节点 %s 失败: %v", workerID, err)
	}
	if !response.Accepted {
		t.Fatalf("执行节点 %s 未被接受", workerID)
	}
}

func getTestAssignment(
	t *testing.T,
	client *http.Client,
	baseURL string,
	token string,
	workerID string,
) WorkloadConfig {
	t.Helper()
	var response assignmentResponse
	if err := controlRequest(
		context.Background(),
		client,
		http.MethodGet,
		baseURL+controlAPIPrefix+"/workers/assignment?worker_id="+workerID,
		token,
		nil,
		&response,
	); err != nil {
		t.Fatalf("获取执行节点 %s 任务失败: %v", workerID, err)
	}
	if !response.Ready || response.Assignment == nil {
		t.Fatalf("执行节点 %s 任务未就绪", workerID)
	}
	return *response.Assignment
}

func sendTestReport(
	t *testing.T,
	client *http.Client,
	baseURL string,
	token string,
	snapshot MetricsSnapshot,
) {
	t.Helper()
	var response map[string]bool
	if err := controlRequest(
		context.Background(),
		client,
		http.MethodPost,
		baseURL+controlAPIPrefix+"/workers/report",
		token,
		reportRequest{WorkerID: snapshot.WorkerID, Snapshot: snapshot},
		&response,
	); err != nil {
		t.Fatalf("执行节点 %s 报告失败: %v", snapshot.WorkerID, err)
	}
}
