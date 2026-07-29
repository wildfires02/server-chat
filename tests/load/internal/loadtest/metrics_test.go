package loadtest

import (
	"testing"
	"time"
)

func TestLatencyHistogramAndMerge(t *testing.T) {
	t.Parallel()

	var first latencyHistogram
	first.Observe(time.Millisecond)
	first.Observe(10 * time.Millisecond)

	var second latencyHistogram
	second.Observe(70 * time.Second)

	merged := MergeHistogramSnapshots(first.Snapshot(), second.Snapshot())
	if merged.Count != 3 {
		t.Fatalf("样本数=%d，期望=3", merged.Count)
	}
	if percentile := merged.Percentile(0.50); percentile != 10*time.Millisecond {
		t.Fatalf("P50=%s，期望=10ms", percentile)
	}
	if percentile := merged.Percentile(0.99); percentile != 70*time.Second {
		t.Fatalf("P99=%s，期望=70s", percentile)
	}
}

func TestMergeMetricsSnapshots(t *testing.T) {
	t.Parallel()

	first := MetricsSnapshot{
		RunID:                "run-test",
		WorkerID:             "worker-a",
		Completed:            true,
		ConnectionsAttempted: 2,
		Errors:               map[string]uint64{"connect": 1},
		AckLatency:           emptyHistogramSnapshot(),
		DeliveryLatency:      emptyHistogramSnapshot(),
	}
	second := MetricsSnapshot{
		RunID:                "run-test",
		WorkerID:             "worker-b",
		Completed:            false,
		ConnectionsAttempted: 3,
		Errors:               map[string]uint64{"connect": 2, "login": 1},
		AckLatency:           emptyHistogramSnapshot(),
		DeliveryLatency:      emptyHistogramSnapshot(),
	}

	merged := MergeMetricsSnapshots(first, second)
	if merged.Completed {
		t.Fatal("存在未完成节点时汇总结果不应完成")
	}
	if merged.ConnectionsAttempted != 5 {
		t.Fatalf("连接尝试数=%d，期望=5", merged.ConnectionsAttempted)
	}
	if merged.Errors["connect"] != 3 || merged.Errors["login"] != 1 {
		t.Fatalf("错误汇总不正确: %#v", merged.Errors)
	}
}
