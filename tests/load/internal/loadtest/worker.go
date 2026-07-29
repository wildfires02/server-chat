package loadtest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 执行节点运行函数负责注册、获取任务、运行负载并持续上报指标。
func RunWorker(
	ctx context.Context,
	config WorkerConfig,
	logf LogFunc,
) (MetricsSnapshot, error) {
	if err := config.Validate(); err != nil {
		return MetricsSnapshot{}, err
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	client := &http.Client{Timeout: config.RequestTimeout}
	baseURL := strings.TrimRight(config.ControllerURL, "/")
	if err := registerWorker(ctx, client, baseURL, config); err != nil {
		return MetricsSnapshot{}, err
	}
	assignment, err := waitForAssignment(ctx, client, baseURL, config)
	if err != nil {
		return MetricsSnapshot{}, err
	}
	logf(
		"收到任务：场景=%s 连接=%d 开始=%s",
		assignment.Scenario,
		assignment.Sessions,
		assignment.StartAt.Format(time.RFC3339Nano),
	)

	metrics := NewMetrics(assignment.RunID, config.WorkerID)
	workloadCompleted := make(chan error, 1)
	go func() {
		workloadCompleted <- RunWorkload(ctx, assignment, metrics)
	}()

	reportTicker := time.NewTicker(config.ReportInterval)
	defer reportTicker.Stop()
	for {
		select {
		case workloadErr := <-workloadCompleted:
			fatalError := ""
			if workloadErr != nil {
				fatalError = workloadErr.Error()
			}
			finalSnapshot := metrics.Snapshot(true, fatalError)
			reportContext, cancel := context.WithTimeout(
				context.Background(),
				config.RequestTimeout,
			)
			if reportErr := sendWorkerReport(
				reportContext,
				client,
				baseURL,
				config,
				finalSnapshot,
			); reportErr != nil {
				cancel()
				return finalSnapshot, reportErr
			}
			cancel()
			return finalSnapshot, workloadErr
		case <-reportTicker.C:
			snapshot := metrics.Snapshot(false, "")
			if reportErr := sendWorkerReport(
				ctx,
				client,
				baseURL,
				config,
				snapshot,
			); reportErr != nil {
				metrics.RecordError("control_report")
				logf("指标报告失败: %v", reportErr)
			} else {
				logf("%s", FormatSummary(snapshot))
			}
		case <-ctx.Done():
			workloadErr := <-workloadCompleted
			fatalError := ctx.Err().Error()
			if workloadErr != nil && !errors.Is(workloadErr, ctx.Err()) {
				fatalError = workloadErr.Error()
			}
			finalSnapshot := metrics.Snapshot(true, fatalError)
			reportContext, cancel := context.WithTimeout(context.Background(), config.RequestTimeout)
			_ = sendWorkerReport(
				reportContext,
				client,
				baseURL,
				config,
				finalSnapshot,
			)
			cancel()
			return finalSnapshot, ctx.Err()
		}
	}
}

func registerWorker(
	ctx context.Context,
	client *http.Client,
	baseURL string,
	config WorkerConfig,
) error {
	for {
		var response registerResponse
		err := controlRequest(
			ctx,
			client,
			http.MethodPost,
			baseURL+controlAPIPrefix+"/workers/register",
			config.ControlToken,
			registerRequest{WorkerID: config.WorkerID},
			&response,
		)
		if err == nil && response.Accepted {
			return nil
		}
		if isPermanentControlError(err) {
			return fmt.Errorf("注册执行节点失败: %w", err)
		}
		timer := time.NewTimer(config.PollInterval)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			if err != nil {
				return fmt.Errorf("注册执行节点失败: %w", err)
			}
			return ctx.Err()
		}
	}
}

func waitForAssignment(
	ctx context.Context,
	client *http.Client,
	baseURL string,
	config WorkerConfig,
) (WorkloadConfig, error) {
	for {
		var response assignmentResponse
		endpoint := baseURL + controlAPIPrefix + "/workers/assignment?worker_id=" +
			url.QueryEscape(config.WorkerID)
		err := controlRequest(
			ctx,
			client,
			http.MethodGet,
			endpoint,
			config.ControlToken,
			nil,
			&response,
		)
		if err == nil && response.Ready && response.Assignment != nil {
			if validateErr := response.Assignment.Validate(); validateErr != nil {
				return WorkloadConfig{}, validateErr
			}
			return *response.Assignment, nil
		}
		if isPermanentControlError(err) {
			return WorkloadConfig{}, fmt.Errorf("获取任务失败: %w", err)
		}
		timer := time.NewTimer(config.PollInterval)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			if err != nil {
				return WorkloadConfig{}, fmt.Errorf("获取任务失败: %w", err)
			}
			return WorkloadConfig{}, ctx.Err()
		}
	}
}

func sendWorkerReport(
	ctx context.Context,
	client *http.Client,
	baseURL string,
	config WorkerConfig,
	snapshot MetricsSnapshot,
) error {
	var response map[string]bool
	return controlRequest(
		ctx,
		client,
		http.MethodPost,
		baseURL+controlAPIPrefix+"/workers/report",
		config.ControlToken,
		reportRequest{WorkerID: config.WorkerID, Snapshot: snapshot},
		&response,
	)
}
