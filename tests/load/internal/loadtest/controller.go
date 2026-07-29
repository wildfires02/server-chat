package loadtest

import (
	"context"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

func newControllerServer(config ControllerConfig, logf LogFunc) *controllerServer {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &controllerServer{
		config:  config,
		logf:    logf,
		workers: make(map[string]*controllerWorker),
		done:    make(chan struct{}),
	}
}

func (controller *controllerServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(controlAPIPrefix+"/workers/register", controller.handleRegister)
	mux.HandleFunc(controlAPIPrefix+"/workers/assignment", controller.handleAssignment)
	mux.HandleFunc(controlAPIPrefix+"/workers/report", controller.handleReport)
	mux.HandleFunc(controlAPIPrefix+"/status", controller.handleStatus)
	return mux
}

// 控制器运行函数会在所有执行节点完成后返回汇总结果。
func RunController(
	ctx context.Context,
	config ControllerConfig,
	logf LogFunc,
) (MetricsSnapshot, error) {
	if err := config.Validate(); err != nil {
		return MetricsSnapshot{}, err
	}
	controller := newControllerServer(config, logf)
	listener, err := net.Listen("tcp", config.Listen)
	if err != nil {
		return MetricsSnapshot{}, err
	}
	server := &http.Server{
		Handler:           controller.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serverErrors := make(chan error, 1)
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil &&
			!errors.Is(serveErr, http.ErrServerClosed) {
			serverErrors <- serveErr
		}
		close(serverErrors)
	}()
	controller.logf(
		"控制器监听 %s，等待 %d 个执行节点",
		listener.Addr(),
		config.ExpectedWorkers,
	)

	summaryTicker := time.NewTicker(config.SummaryInterval)
	defer summaryTicker.Stop()
	var runErr error
	for runErr == nil {
		select {
		case <-controller.done:
			goto completed
		case serveErr := <-serverErrors:
			if serveErr != nil {
				runErr = serveErr
			}
		case <-summaryTicker.C:
			status := controller.status()
			controller.logf(
				"执行节点=%d/%d 就绪=%t %s",
				status.Registered,
				status.ExpectedWorkers,
				status.Ready,
				FormatSummary(status.Aggregate),
			)
		case <-ctx.Done():
			runErr = ctx.Err()
		}
	}

completed:
	shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownContext)
	status := controller.status()
	status.Aggregate.Completed = controller.allWorkersCompleted()
	return status.Aggregate, runErr
}

func (controller *controllerServer) handleRegister(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeControlError(writer, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	if !controller.authorized(request) {
		writeControlError(writer, http.StatusUnauthorized, "控制令牌无效")
		return
	}
	var registration registerRequest
	if err := decodeControlJSON(request.Body, &registration); err != nil {
		writeControlError(writer, http.StatusBadRequest, err.Error())
		return
	}
	registration.WorkerID = strings.TrimSpace(registration.WorkerID)
	if registration.WorkerID == "" || len(registration.WorkerID) > 128 {
		writeControlError(writer, http.StatusBadRequest, "执行节点标识无效")
		return
	}

	controller.lock.Lock()
	if _, exists := controller.workers[registration.WorkerID]; !exists {
		if len(controller.workers) >= controller.config.ExpectedWorkers {
			controller.lock.Unlock()
			writeControlError(writer, http.StatusConflict, "执行节点数量已达到配置上限")
			return
		}
		controller.workers[registration.WorkerID] = &controllerWorker{
			index: len(controller.workers),
		}
		controller.logf("执行节点 %s 已注册", registration.WorkerID)
	}
	if len(controller.workers) == controller.config.ExpectedWorkers &&
		!controller.assignmentsReady {
		controller.finalizeAssignmentsLocked()
	}
	response := registerResponse{
		Accepted:        true,
		Registered:      len(controller.workers),
		ExpectedWorkers: controller.config.ExpectedWorkers,
	}
	controller.lock.Unlock()
	writeControlJSON(writer, http.StatusOK, response)
}

func (controller *controllerServer) finalizeAssignmentsLocked() {
	startAt := time.Now().UTC().Add(controller.config.StartDelay)
	workerIDs := make([]string, 0, len(controller.workers))
	for workerID := range controller.workers {
		workerIDs = append(workerIDs, workerID)
	}
	sort.Slice(workerIDs, func(left, right int) bool {
		return controller.workers[workerIDs[left]].index <
			controller.workers[workerIDs[right]].index
	})
	for _, workerID := range workerIDs {
		worker := controller.workers[workerID]
		assignment := controller.config.Workload
		assignment.WorkerID = workerID
		assignment.Sessions = PartitionTotal(
			controller.config.Workload.Sessions,
			worker.index,
			controller.config.ExpectedWorkers,
		)
		assignment.Accounts = PartitionAccounts(
			controller.config.Workload.Accounts,
			worker.index,
			controller.config.ExpectedWorkers,
		)
		assignment.StartAt = startAt
		worker.assignment = &assignment
	}
	controller.assignmentsReady = true
	controller.logf("任务已分配，统一开始时间 %s", startAt.Format(time.RFC3339Nano))
}

func (controller *controllerServer) handleAssignment(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeControlError(writer, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	if !controller.authorized(request) {
		writeControlError(writer, http.StatusUnauthorized, "控制令牌无效")
		return
	}
	workerID := strings.TrimSpace(request.URL.Query().Get("worker_id"))
	controller.lock.RLock()
	worker := controller.workers[workerID]
	if worker == nil {
		controller.lock.RUnlock()
		writeControlError(writer, http.StatusNotFound, "执行节点尚未注册")
		return
	}
	response := assignmentResponse{Ready: worker.assignment != nil}
	if worker.assignment != nil {
		assignmentCopy := *worker.assignment
		response.Assignment = &assignmentCopy
	}
	controller.lock.RUnlock()
	writeControlJSON(writer, http.StatusOK, response)
}

func (controller *controllerServer) handleReport(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeControlError(writer, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	if !controller.authorized(request) {
		writeControlError(writer, http.StatusUnauthorized, "控制令牌无效")
		return
	}
	var report reportRequest
	if err := decodeControlJSON(request.Body, &report); err != nil {
		writeControlError(writer, http.StatusBadRequest, err.Error())
		return
	}

	controller.lock.Lock()
	worker := controller.workers[report.WorkerID]
	if worker == nil {
		controller.lock.Unlock()
		writeControlError(writer, http.StatusNotFound, "执行节点尚未注册")
		return
	}
	if report.Snapshot.WorkerID != report.WorkerID ||
		report.Snapshot.RunID != controller.config.Workload.RunID {
		controller.lock.Unlock()
		writeControlError(writer, http.StatusConflict, "报告身份或运行标识不匹配")
		return
	}
	worker.report = report.Snapshot
	worker.lastSeen = time.Now().UTC()
	allCompleted := controller.allWorkersCompletedLocked()
	controller.lock.Unlock()
	if allCompleted {
		controller.doneOnce.Do(func() { close(controller.done) })
	}
	writeControlJSON(writer, http.StatusOK, map[string]bool{"accepted": true})
}

func (controller *controllerServer) handleStatus(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeControlError(writer, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	if !controller.authorized(request) {
		writeControlError(writer, http.StatusUnauthorized, "控制令牌无效")
		return
	}
	writeControlJSON(writer, http.StatusOK, controller.status())
}

func (controller *controllerServer) status() controllerStatus {
	controller.lock.RLock()
	defer controller.lock.RUnlock()
	workers := make(map[string]workerStatusView, len(controller.workers))
	snapshots := make([]MetricsSnapshot, 0, len(controller.workers))
	for workerID, worker := range controller.workers {
		workers[workerID] = workerStatusView{
			Index:    worker.index,
			Assigned: worker.assignment != nil,
			LastSeen: worker.lastSeen,
			Report:   worker.report,
		}
		if worker.report.WorkerID != "" {
			snapshots = append(snapshots, worker.report)
		}
	}
	aggregate := MergeMetricsSnapshots(snapshots...)
	aggregate.RunID = controller.config.Workload.RunID
	aggregate.Completed = controller.allWorkersCompletedLocked()
	return controllerStatus{
		RunID:           controller.config.Workload.RunID,
		Ready:           controller.assignmentsReady,
		Registered:      len(controller.workers),
		ExpectedWorkers: controller.config.ExpectedWorkers,
		Workers:         workers,
		Aggregate:       aggregate,
	}
}

func (controller *controllerServer) allWorkersCompleted() bool {
	controller.lock.RLock()
	defer controller.lock.RUnlock()
	return controller.allWorkersCompletedLocked()
}

func (controller *controllerServer) allWorkersCompletedLocked() bool {
	if len(controller.workers) != controller.config.ExpectedWorkers {
		return false
	}
	for _, worker := range controller.workers {
		if !worker.report.Completed {
			return false
		}
	}
	return true
}

func (controller *controllerServer) authorized(request *http.Request) bool {
	if controller.config.ControlToken == "" {
		return true
	}
	const prefix = "Bearer "
	header := request.Header.Get("Authorization")
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	provided := strings.TrimPrefix(header, prefix)
	return subtle.ConstantTimeCompare(
		[]byte(provided),
		[]byte(controller.config.ControlToken),
	) == 1
}
