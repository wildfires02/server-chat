package loadtest

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

const controlAPIPrefix = "/v1"

// 日志输出函数接收控制器和执行节点的运行摘要。
type LogFunc func(format string, arguments ...any)

// 控制器配置包含监听参数和全局负载配置。
type ControllerConfig struct {
	Listen          string
	ControlToken    string
	ExpectedWorkers int
	StartDelay      time.Duration
	SummaryInterval time.Duration
	Workload        WorkloadConfig
}

// 控制器配置校验会检查多机运行参数。
func (config ControllerConfig) Validate() error {
	if config.Listen == "" {
		return errors.New("控制器监听地址不能为空")
	}
	if config.ExpectedWorkers <= 0 {
		return errors.New("执行节点数量必须大于零")
	}
	if config.Workload.Sessions < config.ExpectedWorkers {
		return errors.New("全局连接数不能小于执行节点数量")
	}
	if config.StartDelay <= 0 {
		return errors.New("统一开始延迟必须大于零")
	}
	if config.SummaryInterval <= 0 {
		return errors.New("摘要输出间隔必须大于零")
	}
	workload := config.Workload
	workload.WorkerID = "controller-validation"
	if workload.StartAt.IsZero() {
		workload.StartAt = time.Now()
	}
	return workload.Validate()
}

// 执行节点配置包含连接控制器所需的参数。
type WorkerConfig struct {
	ControllerURL  string
	WorkerID       string
	ControlToken   string
	PollInterval   time.Duration
	ReportInterval time.Duration
	RequestTimeout time.Duration
}

// 执行节点配置校验会检查控制面参数。
func (config WorkerConfig) Validate() error {
	if config.ControllerURL == "" {
		return errors.New("控制器地址不能为空")
	}
	endpoint, err := url.ParseRequestURI(config.ControllerURL)
	if err != nil {
		return fmt.Errorf("控制器地址无效: %w", err)
	}
	if (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" {
		return errors.New("控制器地址必须是包含主机名的 HTTP 或 HTTPS 地址")
	}
	if strings.TrimSpace(config.WorkerID) == "" {
		return errors.New("执行节点标识不能为空")
	}
	if config.PollInterval <= 0 || config.ReportInterval <= 0 || config.RequestTimeout <= 0 {
		return errors.New("轮询、报告和请求超时时间必须大于零")
	}
	return nil
}

type registerRequest struct {
	WorkerID string `json:"worker_id"`
}

type registerResponse struct {
	Accepted        bool `json:"accepted"`
	Registered      int  `json:"registered"`
	ExpectedWorkers int  `json:"expected_workers"`
}

type assignmentResponse struct {
	Ready      bool            `json:"ready"`
	Assignment *WorkloadConfig `json:"assignment,omitempty"`
}

type reportRequest struct {
	WorkerID string          `json:"worker_id"`
	Snapshot MetricsSnapshot `json:"snapshot"`
}

type controllerStatus struct {
	RunID           string                      `json:"run_id"`
	Ready           bool                        `json:"ready"`
	Registered      int                         `json:"registered"`
	ExpectedWorkers int                         `json:"expected_workers"`
	Workers         map[string]workerStatusView `json:"workers"`
	Aggregate       MetricsSnapshot             `json:"aggregate"`
}

type workerStatusView struct {
	Index    int             `json:"index"`
	Assigned bool            `json:"assigned"`
	LastSeen time.Time       `json:"last_seen,omitempty"`
	Report   MetricsSnapshot `json:"report"`
}

type controllerWorker struct {
	index      int
	assignment *WorkloadConfig
	lastSeen   time.Time
	report     MetricsSnapshot
}

type controllerServer struct {
	config           ControllerConfig
	logf             LogFunc
	lock             sync.RWMutex
	workers          map[string]*controllerWorker
	assignmentsReady bool
	done             chan struct{}
	doneOnce         sync.Once
}
