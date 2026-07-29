// 本文件提供本机和多机分布式即时通信负载测试命令。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"chat/tests/load/internal/loadtest"
)

type commandOptions struct {
	mode                  string
	runID                 string
	scenario              string
	webSocketURL          string
	apiKey                string
	protocolVersion       string
	accountsPath          string
	topic                 string
	sessions              int
	ramp                  time.Duration
	duration              time.Duration
	requestTimeout        time.Duration
	publishCount          int
	publishInterval       time.Duration
	maxTopics             int
	controlListen         string
	controllerURL         string
	controlToken          string
	expectedWorkers       int
	workerID              string
	startDelay            time.Duration
	pollInterval          time.Duration
	reportInterval        time.Duration
	summaryInterval       time.Duration
	controlRequestTimeout time.Duration
	outputPath            string
}

func main() {
	options := parseFlags()
	logger := log.New(os.Stderr, "im-load: ", log.LstdFlags|log.Lmicroseconds)
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	var (
		snapshot loadtest.MetricsSnapshot
		err      error
	)
	switch options.mode {
	case "local":
		snapshot, err = runLocal(ctx, options, logger)
	case "controller":
		snapshot, err = runController(ctx, options, logger)
	case "worker":
		snapshot, err = runWorker(ctx, options, logger)
	default:
		err = fmt.Errorf("不支持的运行模式 %q", options.mode)
	}
	if reportErr := writeReport(options.outputPath, snapshot); reportErr != nil && err == nil {
		err = reportErr
	}
	if err == nil && snapshot.FatalError != "" {
		err = errors.New(snapshot.FatalError)
	}
	if err != nil {
		logger.Fatal(err)
	}
}

func parseFlags() commandOptions {
	var options commandOptions
	flag.StringVar(&options.mode, "mode", "local", "运行模式：local、controller 或 worker")
	flag.StringVar(&options.runID, "run-id", "", "本次压测的唯一运行标识")
	flag.StringVar(&options.scenario, "scenario", loadtest.ScenarioMixed, "压测场景：mixed、me 或 hot-topic")
	flag.StringVar(&options.webSocketURL, "ws-url", "ws://127.0.0.1:6060/v0/channels", "服务端 WebSocket 地址")
	flag.StringVar(&options.apiKey, "api-key", loadtest.DefaultAPIKey, "测试环境接口密钥")
	flag.StringVar(&options.protocolVersion, "protocol", loadtest.DefaultProtocolVersion, "客户端协议版本")
	flag.StringVar(&options.accountsPath, "accounts", "tests/load/users.csv", "压测账号 CSV 文件")
	flag.StringVar(&options.topic, "topic", "", "热点主题场景使用的目标主题")
	flag.IntVar(&options.sessions, "sessions", 100, "全局并发连接数")
	flag.DurationVar(&options.ramp, "ramp", 10*time.Second, "连接升压时间")
	flag.DurationVar(&options.duration, "duration", time.Minute, "升压完成后的运行时间")
	flag.DurationVar(&options.requestTimeout, "request-timeout", 15*time.Second, "业务请求超时时间")
	flag.IntVar(&options.publishCount, "publish-count", 1, "每个连接在每个主题中的发布次数")
	flag.DurationVar(&options.publishInterval, "publish-interval", time.Second, "相邻发布之间的最大随机间隔")
	flag.IntVar(&options.maxTopics, "max-topics", 10, "综合场景每个连接最多访问的已有主题数，零表示不限")
	flag.StringVar(&options.controlListen, "control-listen", ":19090", "控制器监听地址")
	flag.StringVar(&options.controllerURL, "controller", "http://127.0.0.1:19090", "执行节点连接的控制器地址")
	flag.StringVar(&options.controlToken, "control-token", "", "控制器与执行节点共享的控制令牌")
	flag.IntVar(&options.expectedWorkers, "workers", 1, "控制器等待的执行节点数量")
	flag.StringVar(&options.workerID, "worker-id", defaultWorkerID(), "执行节点唯一标识")
	flag.DurationVar(&options.startDelay, "start-delay", 10*time.Second, "全部执行节点注册后的统一开始延迟")
	flag.DurationVar(&options.pollInterval, "poll-interval", time.Second, "执行节点等待任务的轮询间隔")
	flag.DurationVar(&options.reportInterval, "report-interval", 2*time.Second, "执行节点指标报告间隔")
	flag.DurationVar(&options.summaryInterval, "summary-interval", 5*time.Second, "本机或控制器摘要输出间隔")
	flag.DurationVar(&options.controlRequestTimeout, "control-timeout", 10*time.Second, "控制面 HTTP 请求超时时间")
	flag.StringVar(&options.outputPath, "output", "", "最终 JSON 报告文件，留空时只输出到标准输出")
	flag.Parse()
	if options.runID == "" {
		options.runID = "load-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	}
	return options
}

func runLocal(
	ctx context.Context,
	options commandOptions,
	logger *log.Logger,
) (loadtest.MetricsSnapshot, error) {
	if options.summaryInterval <= 0 {
		return loadtest.MetricsSnapshot{}, errors.New("摘要输出间隔必须大于零")
	}
	accounts, err := loadtest.LoadAccounts(options.accountsPath)
	if err != nil {
		return loadtest.MetricsSnapshot{}, err
	}
	config := workloadConfig(options, accounts)
	config.WorkerID = options.workerID
	config.StartAt = time.Now().UTC()
	metrics := loadtest.NewMetrics(config.RunID, config.WorkerID)
	completed := make(chan error, 1)
	go func() {
		completed <- loadtest.RunWorkload(ctx, config, metrics)
	}()

	ticker := time.NewTicker(options.summaryInterval)
	defer ticker.Stop()
	for {
		select {
		case runErr := <-completed:
			fatalError := ""
			if runErr != nil {
				fatalError = runErr.Error()
			}
			snapshot := metrics.Snapshot(true, fatalError)
			logger.Printf("%s", loadtest.FormatSummary(snapshot))
			return snapshot, runErr
		case <-ticker.C:
			logger.Printf("%s", loadtest.FormatSummary(metrics.Snapshot(false, "")))
		case <-ctx.Done():
			runErr := <-completed
			fatalError := ctx.Err().Error()
			if runErr != nil && !errors.Is(runErr, ctx.Err()) {
				fatalError = runErr.Error()
			}
			return metrics.Snapshot(true, fatalError), ctx.Err()
		}
	}
}

func runController(
	ctx context.Context,
	options commandOptions,
	logger *log.Logger,
) (loadtest.MetricsSnapshot, error) {
	accounts, err := loadtest.LoadAccounts(options.accountsPath)
	if err != nil {
		return loadtest.MetricsSnapshot{}, err
	}
	config := loadtest.ControllerConfig{
		Listen:          options.controlListen,
		ControlToken:    options.controlToken,
		ExpectedWorkers: options.expectedWorkers,
		StartDelay:      options.startDelay,
		SummaryInterval: options.summaryInterval,
		Workload:        workloadConfig(options, accounts),
	}
	return loadtest.RunController(ctx, config, logger.Printf)
}

func runWorker(
	ctx context.Context,
	options commandOptions,
	logger *log.Logger,
) (loadtest.MetricsSnapshot, error) {
	return loadtest.RunWorker(ctx, loadtest.WorkerConfig{
		ControllerURL:  options.controllerURL,
		WorkerID:       options.workerID,
		ControlToken:   options.controlToken,
		PollInterval:   options.pollInterval,
		ReportInterval: options.reportInterval,
		RequestTimeout: options.controlRequestTimeout,
	}, logger.Printf)
}

func workloadConfig(options commandOptions, accounts []loadtest.Account) loadtest.WorkloadConfig {
	return loadtest.WorkloadConfig{
		RunID:           options.runID,
		WebSocketURL:    options.webSocketURL,
		APIKey:          options.apiKey,
		ProtocolVersion: options.protocolVersion,
		Scenario:        options.scenario,
		Topic:           options.topic,
		Sessions:        options.sessions,
		Ramp:            options.ramp,
		Duration:        options.duration,
		RequestTimeout:  options.requestTimeout,
		PublishCount:    options.publishCount,
		PublishInterval: options.publishInterval,
		MaxTopics:       options.maxTopics,
		Accounts:        accounts,
	}
}

func writeReport(path string, snapshot loadtest.MetricsSnapshot) error {
	if snapshot.RunID == "" {
		return nil
	}
	report, err := loadtest.MarshalReport(snapshot)
	if err != nil {
		return err
	}
	if _, err = os.Stdout.Write(append(report, '\n')); err != nil {
		return err
	}
	if path == "" || path == "-" {
		return nil
	}
	if directory := filepath.Dir(path); directory != "." {
		if err = os.MkdirAll(directory, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, append(report, '\n'), 0o600)
}

func defaultWorkerID() string {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "worker"
	}
	return fmt.Sprintf("%s-%d", hostname, os.Getpid())
}
