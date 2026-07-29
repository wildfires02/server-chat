package server

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chat/api/pbx"
)

// TestClusterDedupeCacheReplaysResult 验证重复 Request ID 不会再次执行业务函数。
func TestClusterDedupeCacheReplaysResult(t *testing.T) {
	cache := newClusterDedupeCache(4, time.Minute)
	var executions atomic.Int32
	dispatch := func() ([]byte, error) {
		executions.Add(1)
		return []byte("ack"), nil
	}
	for range 2 {
		response, err := cache.Execute(
			context.Background(),
			"node-a/1/request-1",
			pbx.ClusterFrameKind_CLUSTER_FRAME_TOPIC_MASTER,
			[]byte("payload"),
			dispatch,
		)
		if err != nil || string(response) != "ack" {
			t.Fatalf("去重执行结果 = %q, %v", response, err)
		}
	}
	if executions.Load() != 1 {
		t.Fatalf("业务函数执行了 %d 次，期望 1 次", executions.Load())
	}

	_, err := cache.Execute(
		context.Background(),
		"node-a/1/request-1",
		pbx.ClusterFrameKind_CLUSTER_FRAME_TOPIC_MASTER,
		[]byte("different-payload"),
		dispatch,
	)
	if !errors.Is(err, errClusterRequestIDCollision) {
		t.Fatalf("Request ID 碰撞返回 %v", err)
	}
}

// TestClusterDedupeCacheCoalescesConcurrentRetry 验证在途重试等待首次执行结果。
func TestClusterDedupeCacheCoalescesConcurrentRetry(t *testing.T) {
	cache := newClusterDedupeCache(4, time.Minute)
	started := make(chan struct{})
	release := make(chan struct{})
	var executions atomic.Int32
	dispatch := func() ([]byte, error) {
		executions.Add(1)
		close(started)
		<-release
		return []byte("done"), nil
	}

	var waitGroup sync.WaitGroup
	results := make(chan string, 2)
	execute := func() {
		defer waitGroup.Done()
		response, err := cache.Execute(
			context.Background(),
			"node-a/1/request-concurrent",
			pbx.ClusterFrameKind_CLUSTER_FRAME_ROUTE,
			[]byte("payload"),
			dispatch,
		)
		if err != nil {
			results <- err.Error()
			return
		}
		results <- string(response)
	}

	// 先确认首次请求已经进入业务函数，再发起相同 Request ID 的在途重试。
	waitGroup.Add(1)
	go execute()
	<-started
	waitGroup.Add(1)
	go execute()

	close(release)
	waitGroup.Wait()
	close(results)
	for result := range results {
		if result != "done" {
			t.Fatalf("并发去重结果 = %q", result)
		}
	}
	if executions.Load() != 1 {
		t.Fatalf("并发业务函数执行了 %d 次，期望 1 次", executions.Load())
	}
}
