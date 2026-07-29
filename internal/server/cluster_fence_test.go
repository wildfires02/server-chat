package server

import (
	"context"
	"testing"

	rh "chat/server/ringhash"
	"chat/server/store/types"
)

// staticControlPlane 为 writeFenceToken 单元测试提供不访问网络的控制面状态。
type staticControlPlane struct {
	ready bool
}

// Start 满足 clusterControlPlane；本测试不需要实际启动。
func (control *staticControlPlane) Start(
	context.Context,
	clusterMember,
	func(clusterView) error,
) error {
	return nil
}

// Ready 返回测试指定的多数派状态。
func (control *staticControlPlane) Ready() bool {
	return control.ready
}

// View 返回空视图；writeFenceToken 使用已经提交到 Cluster 的 viewEpoch。
func (control *staticControlPlane) View() clusterView {
	return clusterView{}
}

// Close 满足 clusterControlPlane；本测试没有待释放资源。
func (control *staticControlPlane) Close() error {
	return nil
}

// TestClusterWriteFenceToken 验证仅 Ready 的本地 Topic Owner 可以获得写入令牌。
func TestClusterWriteFenceToken(t *testing.T) {
	ring := rh.New(clusterHashReplicas, nil)
	ring.Add("im-0")
	cluster := &Cluster{
		clusterID:        "im-production",
		thisNodeName:     "im-0",
		controlPlane:     &staticControlPlane{ready: true},
		expectedReplicas: 3,
	}
	cluster.ring.Store(ring)
	cluster.viewEpoch.Store(42)

	clusterID, epoch, owner, err := cluster.writeFenceToken("grp-test")
	if err != nil {
		t.Fatalf("writeFenceToken() 返回意外错误：%v", err)
	}
	if clusterID != "im-production" || epoch != 42 || owner != "im-0" {
		t.Fatalf("writeFenceToken() = (%q,%d,%q)，结果不符合预期", clusterID, epoch, owner)
	}

	cluster.controlPlane = &staticControlPlane{ready: false}
	if _, _, _, err = cluster.writeFenceToken("grp-test"); err != types.ErrClusterFenced {
		t.Fatalf("非 Ready 控制面错误 = %v，期望 %v", err, types.ErrClusterFenced)
	}

	cluster.controlPlane = &staticControlPlane{ready: true}
	cluster.thisNodeName = "im-1"
	if _, _, _, err = cluster.writeFenceToken("grp-test"); err != types.ErrClusterFenced {
		t.Fatalf("非 Owner 节点错误 = %v，期望 %v", err, types.ErrClusterFenced)
	}
}
