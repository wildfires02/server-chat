package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.etcd.io/etcd/api/v3/mvccpb"
)

// TestNormalizeControlPlaneConfig 验证 etcd 控制面默认值和安全边界。
func TestNormalizeControlPlaneConfig(t *testing.T) {
	tests := []struct {
		name      string
		config    clusterControlPlaneConfig
		wantError string
	}{
		{
			name: "填充默认时长",
			config: clusterControlPlaneConfig{
				Provider:  "ETCD",
				Endpoints: []string{"http://etcd-0:2379"},
				Namespace: "im/test",
			},
		},
		{
			name: "拒绝未知实现",
			config: clusterControlPlaneConfig{
				Provider:  "memory",
				Endpoints: []string{"http://etcd-0:2379"},
				Namespace: "/im/test",
			},
			wantError: "provider 必须是 etcd",
		},
		{
			name: "拒绝重复地址",
			config: clusterControlPlaneConfig{
				Provider:  "etcd",
				Endpoints: []string{"http://etcd-0:2379", "http://etcd-0:2379"},
				Namespace: "/im/test",
			},
			wantError: "endpoint",
		},
		{
			name: "拒绝过短租约",
			config: clusterControlPlaneConfig{
				Provider:  "etcd",
				Endpoints: []string{"http://etcd-0:2379"},
				Namespace: "/im/test",
				LeaseTTL:  "1s",
			},
			wantError: "lease_ttl",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config, err := normalizeControlPlaneConfig(test.config)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("normalizeControlPlaneConfig() 错误 = %v，期望包含 %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeControlPlaneConfig() 返回意外错误：%v", err)
			}
			if config.Provider != controlPlaneProviderEtcd ||
				config.Namespace != "/im/test" ||
				config.LeaseTTL != defaultControlPlaneLeaseTTL.String() ||
				config.DialTimeout != defaultControlPlaneDialTimeout.String() {
				t.Fatalf("normalizeControlPlaneConfig() = %+v", config)
			}
		})
	}
}

// TestBuildClusterView 验证成员校验、稳定排序和 etcd Revision 映射。
func TestBuildClusterView(t *testing.T) {
	expectedMembers := map[string]string{
		"im-0": "im-0:12000",
		"im-1": "im-1:12000",
		"im-2": "im-2:12000",
	}
	keyValues := []*mvccpb.KeyValue{
		memberKeyValue(t, clusterMember{Name: "im-2", Address: "im-2:12000", InstanceID: 3}),
		memberKeyValue(t, clusterMember{Name: "im-0", Address: "im-0:12000", InstanceID: 1}),
		memberKeyValue(t, clusterMember{Name: "im-1", Address: "im-1:12000", InstanceID: 2}),
	}
	topology := clusterTopology{
		ExpectedReplicas: 3,
		Members:          []string{"im-0", "im-1", "im-2"},
	}

	view, err := buildClusterView(42, keyValues, expectedMembers, topology)
	if err != nil {
		t.Fatal(err)
	}
	if view.Epoch != 42 || len(view.Members) != 3 {
		t.Fatalf("buildClusterView() = %+v", view)
	}
	if view.Members[0].Name != "im-0" || view.Members[2].Name != "im-2" {
		t.Fatalf("成员没有稳定排序：%+v", view.Members)
	}
	if !view.hasMember("im-1", 2) {
		t.Fatal("视图未识别已注册节点实例")
	}
	view.Members[2].Draining = true
	names := view.memberNames()
	if len(names) != 2 || names[0] != "im-0" || names[1] != "im-1" {
		t.Fatalf("Drain 节点未从 Owner Ring 排除：%v", names)
	}
}

// TestBuildClusterViewRejectsUnexpectedMember 验证控制面不能注入静态拓扑外的节点。
func TestBuildClusterViewRejectsUnexpectedMember(t *testing.T) {
	keyValues := []*mvccpb.KeyValue{
		memberKeyValue(t, clusterMember{Name: "im-9", Address: "im-9:12000", InstanceID: 9}),
	}
	expectedMembers := map[string]string{
		"im-0": "im-0:12000",
		"im-1": "im-1:12000",
		"im-2": "im-2:12000",
	}
	_, err := buildClusterView(1, keyValues, expectedMembers, clusterTopology{
		ExpectedReplicas: 3,
		Members:          []string{"im-0", "im-1", "im-2"},
	})
	if err == nil || !strings.Contains(err.Error(), "未配置节点") {
		t.Fatalf("buildClusterView() 错误 = %v，期望拒绝未知成员", err)
	}
}

// TestMaxMemberRevision 验证成员 epoch 只由 members 前缀自身的修改版本决定。
func TestMaxMemberRevision(t *testing.T) {
	keyValues := []*mvccpb.KeyValue{
		{ModRevision: 12},
		{ModRevision: 47},
		{ModRevision: 31},
	}
	if epoch := maxMemberRevision(keyValues); epoch != 47 {
		t.Fatalf("maxMemberRevision() = %d，期望 47", epoch)
	}
	if epoch := maxMemberRevision(nil); epoch != 0 {
		t.Fatalf("空成员集合 epoch = %d，期望 0", epoch)
	}
}

// TestEtcdControlPlaneReady 验证租约、联系时间、本节点实例和多数派共同决定 Ready。
func TestEtcdControlPlaneReady(t *testing.T) {
	control := &etcdControlPlane{
		config: clusterControlPlaneConfig{
			LeaseTTL: defaultControlPlaneLeaseTTL.String(),
		},
		expectedReplicas: 3,
		member: clusterMember{
			Name:       "im-0",
			InstanceID: 1,
		},
	}
	control.leaseAlive.Store(true)
	control.viewApplied.Store(true)
	control.lastViewRefresh.Store(time.Now().UnixNano())
	control.view.Store(&clusterView{
		Epoch:            7,
		ExpectedReplicas: 3,
		Members: []clusterMember{
			{Name: "im-0", InstanceID: 1, Active: true},
			{Name: "im-1", InstanceID: 2, Active: true},
		},
	})
	if !control.Ready() {
		t.Fatal("两个存活节点应满足三副本集群多数派")
	}

	control.view.Store(&clusterView{
		Epoch:            8,
		ExpectedReplicas: 3,
		Members: []clusterMember{
			{Name: "im-0", InstanceID: 1, Active: true},
			{Name: "im-1", InstanceID: 2, Draining: true, Active: true},
		},
	})
	if control.Ready() {
		t.Fatal("Drain 节点不能计入可写多数派")
	}

	control.viewApplied.Store(false)
	if control.Ready() {
		t.Fatal("数据库 fence 尚未应用时不应保持 Ready")
	}
	control.viewApplied.Store(true)

	control.view.Store(&clusterView{
		Epoch:            9,
		ExpectedReplicas: 3,
		Members:          []clusterMember{{Name: "im-0", InstanceID: 1, Active: true}},
	})
	if control.Ready() {
		t.Fatal("单节点不应满足三副本集群多数派")
	}

	control.view.Store(&clusterView{
		Epoch:            10,
		ExpectedReplicas: 3,
		Members: []clusterMember{
			{Name: "im-0", InstanceID: 1, Active: true},
			{Name: "im-1", InstanceID: 2, Active: true},
		},
	})
	control.lastViewRefresh.Store(time.Now().Add(-2 * defaultControlPlaneLeaseTTL).UnixNano())
	if control.Ready() {
		t.Fatal("超过租约窗口的旧视图不应保持 Ready")
	}
}

// TestNewEtcdControlPlaneValidatesTopology 验证控制面构造阶段拒绝非法集群身份和副本数。
func TestNewEtcdControlPlaneValidatesTopology(t *testing.T) {
	config := clusterControlPlaneConfig{
		Provider:  "etcd",
		Endpoints: []string{"http://etcd-0:2379"},
		Namespace: "/im/test",
	}
	_, err := newEtcdControlPlane(
		config,
		"invalid/id",
		3,
		map[string]string{
			"im-0": "im-0:12000",
			"im-1": "im-1:12000",
			"im-2": "im-2:12000",
		},
		[]string{"im-0", "im-1", "im-2"},
	)
	if err == nil || !strings.Contains(err.Error(), "cluster_id") {
		t.Fatalf("newEtcdControlPlane() 错误 = %v，期望拒绝非法 cluster_id", err)
	}
}

// TestBuildClusterViewKeepsJoiningNodeOutOfRing 验证候选节点注册后必须等待拓扑提交。
func TestBuildClusterViewKeepsJoiningNodeOutOfRing(t *testing.T) {
	expectedMembers := map[string]string{
		"im-0": "im-0:12000",
		"im-1": "im-1:12000",
		"im-2": "im-2:12000",
		"im-3": "im-3:12000",
		"im-4": "im-4:12000",
	}
	keyValues := make([]*mvccpb.KeyValue, 0, len(expectedMembers))
	for index := 0; index < 5; index++ {
		name := "im-" + string(rune('0'+index))
		keyValues = append(keyValues, memberKeyValue(t, clusterMember{
			Name:       name,
			Address:    expectedMembers[name],
			InstanceID: int64(index + 1),
		}))
	}
	view, err := buildClusterView(100, keyValues, expectedMembers, clusterTopology{
		ExpectedReplicas: 3,
		Members:          []string{"im-0", "im-1", "im-2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	names := view.memberNames()
	if strings.Join(names, ",") != "im-0,im-1,im-2" {
		t.Fatalf("Joining 节点错误进入 Owner Ring：%v", names)
	}
	if view.hasServingMember("im-3", 4) {
		t.Fatal("Joining 节点不应通过服务成员检查")
	}
}

// TestNormalizeClusterTopology 验证活动拓扑必须使用白名单中的唯一奇数成员。
func TestNormalizeClusterTopology(t *testing.T) {
	allowed := map[string]string{
		"im-0": "im-0:12000",
		"im-1": "im-1:12000",
		"im-2": "im-2:12000",
	}
	topology, err := normalizeClusterTopology(clusterTopology{
		ExpectedReplicas: 3,
		Members:          []string{"im-2", "im-0", "im-1"},
	}, allowed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(topology.Members, ",") != "im-0,im-1,im-2" {
		t.Fatalf("拓扑未稳定排序：%v", topology.Members)
	}
	if _, err = normalizeClusterTopology(clusterTopology{
		ExpectedReplicas: 3,
		Members:          []string{"im-0", "im-1", "im-9"},
	}, allowed); err == nil {
		t.Fatal("未知候选节点不应通过拓扑校验")
	}
}

// memberKeyValue 把成员编码成 buildClusterView 使用的 etcd 键值。
func memberKeyValue(t *testing.T, member clusterMember) *mvccpb.KeyValue {
	t.Helper()
	payload, err := json.Marshal(member)
	if err != nil {
		t.Fatal(err)
	}
	return &mvccpb.KeyValue{
		Key:   []byte("/members/" + member.Name),
		Value: payload,
	}
}
