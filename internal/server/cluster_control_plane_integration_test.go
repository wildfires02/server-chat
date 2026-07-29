package server

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestEtcdControlPlaneIntegration 使用真实 etcd 验证注册、Watch、多数派和租约注销。
//
// 默认跳过；设置 IM_TEST_ETCD_ENDPOINTS=http://127.0.0.1:2379 后执行。
func TestEtcdControlPlaneIntegration(t *testing.T) {
	endpointText := strings.TrimSpace(os.Getenv("IM_TEST_ETCD_ENDPOINTS"))
	if endpointText == "" {
		t.Skip("未配置 IM_TEST_ETCD_ENDPOINTS，跳过真实 etcd 集成测试")
	}

	expectedMembers := map[string]string{
		"im-0": "im-0:12000",
		"im-1": "im-1:12000",
		"im-2": "im-2:12000",
		"im-3": "im-3:12000",
		"im-4": "im-4:12000",
	}
	initialMembers := []string{"im-0", "im-1", "im-2"}
	config := clusterControlPlaneConfig{
		Provider:    controlPlaneProviderEtcd,
		Endpoints:   splitNonEmpty(endpointText),
		Namespace:   "/im/integration-" + strconv.FormatInt(time.Now().UnixNano(), 10),
		LeaseTTL:    "5s",
		DialTimeout: "5s",
	}

	controls := make([]*etcdControlPlane, 0, len(expectedMembers))
	for index := 0; index < len(initialMembers); index++ {
		control, err := newEtcdControlPlane(
			config,
			"integration",
			3,
			expectedMembers,
			initialMembers,
		)
		if err != nil {
			t.Fatal(err)
		}
		memberName := "im-" + strconv.Itoa(index)
		member := clusterMember{
			Name:            memberName,
			Address:         expectedMembers[memberName],
			InstanceID:      int64(index + 1),
			ProtocolVersion: clusterProtocolVersion,
			StartedAt:       time.Now().UTC(),
		}
		if err = control.Start(context.Background(), member, nil); err != nil {
			t.Fatal(err)
		}
		controls = append(controls, control)
	}
	t.Cleanup(func() {
		for _, control := range controls {
			_ = control.Close()
		}
	})

	waitForControlPlaneState(t, 10*time.Second, true, controls...)
	initialEpoch := waitForSameControlPlaneEpoch(t, 10*time.Second, 0, controls...)

	// 两个候选节点先以 Joining 状态注册：它们不 Ready，也不进入三节点 Ring。
	for index := 3; index < len(expectedMembers); index++ {
		control, err := newEtcdControlPlane(
			config,
			"integration",
			3,
			expectedMembers,
			initialMembers,
		)
		if err != nil {
			t.Fatal(err)
		}
		memberName := "im-" + strconv.Itoa(index)
		if err = control.Start(context.Background(), clusterMember{
			Name:            memberName,
			Address:         expectedMembers[memberName],
			InstanceID:      int64(index + 1),
			ProtocolVersion: clusterProtocolVersion,
			StartedAt:       time.Now().UTC(),
		}, nil); err != nil {
			t.Fatal(err)
		}
		controls = append(controls, control)
	}
	waitForControlPlaneState(t, 10*time.Second, false, controls[3], controls[4])
	if _, err := controls[0].ChangeTopology(
		context.Background(),
		[]string{"im-0", "im-1", "im-2", "im-3", "im-4"},
	); err != nil {
		t.Fatalf("3→5 在线扩容失败：%v", err)
	}
	waitForControlPlaneState(t, 10*time.Second, true, controls...)
	expandedEpoch := waitForSameControlPlaneEpoch(t, 10*time.Second, initialEpoch, controls...)

	// 缩容必须先 Drain 待移除节点，再由仍在活动拓扑内的节点提交 5→3。
	if err := controls[3].BeginDrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := controls[4].BeginDrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForControlPlaneState(t, 10*time.Second, false, controls[3], controls[4])
	if _, err := controls[0].ChangeTopology(
		context.Background(),
		[]string{"im-0", "im-1", "im-2"},
	); err != nil {
		t.Fatalf("5→3 在线缩容失败：%v", err)
	}
	waitForControlPlaneState(t, 10*time.Second, true, controls[0], controls[1], controls[2])
	initialEpoch = waitForSameControlPlaneEpoch(
		t,
		10*time.Second,
		expandedEpoch,
		controls[0],
		controls[1],
		controls[2],
	)

	// etcd 中与 IM 成员无关的写入不能推动 Cluster View epoch。
	if _, err := controls[0].client.Put(
		context.Background(),
		config.Namespace+"/unrelated",
		strconv.FormatInt(time.Now().UnixNano(), 10),
	); err != nil {
		t.Fatal(err)
	}
	for _, control := range controls {
		if err := control.refreshView(); err != nil {
			t.Fatal(err)
		}
		if epoch := control.View().Epoch; epoch != initialEpoch {
			t.Fatalf("无关 etcd 写入把 Cluster View epoch 从 %d 推进到 %d", initialEpoch, epoch)
		}
	}

	claimed, err := controls[0].ClaimTask(context.Background(), "scheduled-1", 2*time.Second)
	if err != nil || !claimed {
		t.Fatalf("首次定时任务领取 = %v, %v", claimed, err)
	}
	claimed, err = controls[1].ClaimTask(context.Background(), "scheduled-1", 2*time.Second)
	if err != nil || claimed {
		t.Fatalf("并发定时任务领取 = %v, %v，期望冲突", claimed, err)
	}
	time.Sleep(3 * time.Second)
	claimed, err = controls[1].ClaimTask(context.Background(), "scheduled-1", 2*time.Second)
	if err != nil || !claimed {
		t.Fatalf("领取租约过期后结果 = %v, %v", claimed, err)
	}

	if err := controls[2].BeginDrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForControlPlaneState(t, 10*time.Second, true, controls[0], controls[1])
	waitForControlPlaneState(t, 10*time.Second, false, controls[2])
	drainEpoch := waitForSameControlPlaneEpoch(t, 10*time.Second, initialEpoch, controls...)
	for _, control := range controls {
		names := control.View().memberNames()
		if len(names) != 2 || names[0] != "im-0" || names[1] != "im-1" {
			t.Fatalf("Drain 后 Owner Ring 成员 = %v", names)
		}
	}

	if err := controls[2].Close(); err != nil {
		t.Fatal(err)
	}
	waitForControlPlaneState(t, 10*time.Second, true, controls[0], controls[1])
	waitForSameControlPlaneEpoch(t, 10*time.Second, drainEpoch, controls[0], controls[1])

	if err := controls[1].Close(); err != nil {
		t.Fatal(err)
	}
	waitForControlPlaneState(t, 10*time.Second, false, controls[0])
}

// waitForSameControlPlaneEpoch 等待所有实例收敛到同一个、可选大于下限的 epoch。
func waitForSameControlPlaneEpoch(
	t *testing.T,
	timeout time.Duration,
	greaterThan int64,
	controls ...*etcdControlPlane,
) int64 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var epoch int64
		allMatched := true
		for index, control := range controls {
			current := control.View().Epoch
			if index == 0 {
				epoch = current
			}
			if current != epoch || current <= greaterThan {
				allMatched = false
				break
			}
		}
		if allMatched {
			return epoch
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("控制面未在 %s 内收敛到大于 %d 的相同 epoch", timeout, greaterThan)
	return 0
}

// splitNonEmpty 将逗号分隔的 etcd 地址转换为去除空白的列表。
func splitNonEmpty(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

// waitForControlPlaneState 等待所有控制面实例达到期望 Ready 状态。
func waitForControlPlaneState(
	t *testing.T,
	timeout time.Duration,
	expected bool,
	controls ...*etcdControlPlane,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		allMatched := true
		for _, control := range controls {
			if control.Ready() != expected {
				allMatched = false
				break
			}
		}
		if allMatched {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("控制面未在 %s 内达到 Ready=%t", timeout, expected)
}
