package server

import "testing"

// TestGCProxySessionsIgnoresSelfPlaceholder 验证 Drain 后活动拓扑不再包含
// 本节点时，self 的 nil 占位不会被当成失联远端节点处理。
func TestGCProxySessionsIgnoresSelfPlaceholder(t *testing.T) {
	cluster := &Cluster{
		thisNodeName: "im0",
		nodes: map[string]*ClusterNode{
			"im0": nil,
			"im1": {
				name:  "im1",
				msess: make(map[string]struct{}),
			},
		},
	}

	// im0 已退出活动拓扑，im1 仍正常；调用必须安全返回且不修改 im1。
	cluster.gcProxySessions([]string{"im1"})
	if cluster.nodes["im1"].msess == nil {
		t.Fatal("活动远端节点的代理 Session 集合被意外清空")
	}
}

// TestGCProxySessionsForUnknownNode 验证成员视图与静态候选列表短暂不同步时，
// 未知节点不会触发空指针异常。
func TestGCProxySessionsForUnknownNode(t *testing.T) {
	cluster := &Cluster{nodes: map[string]*ClusterNode{}}
	cluster.gcProxySessionsForNode("missing")
}
