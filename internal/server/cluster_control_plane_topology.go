package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.etcd.io/etcd/api/v3/mvccpb"
)

// clusterMember 描述注册到控制面的单个 IM 节点实例。
type clusterMember struct {
	Name            string    `json:"name"`
	Address         string    `json:"address"`
	InstanceID      int64     `json:"instance_id"`
	ProtocolVersion int       `json:"protocol_version"`
	StartedAt       time.Time `json:"started_at"`
	Draining        bool      `json:"draining,omitempty"`
	// Active 由已提交拓扑计算，不写入 etcd 成员租约。
	Active bool `json:"-"`
}

// clusterTopology 是 etcd 中经过 CAS 提交的活动成员集合。
type clusterTopology struct {
	ExpectedReplicas int       `json:"expected_replicas"`
	Members          []string  `json:"members"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// clusterView 是从 etcd 线性一致快照生成的不可变成员视图。
type clusterView struct {
	Epoch            int64
	ExpectedReplicas int
	Members          []clusterMember
}

func (view clusterView) memberNames() []string {
	names := make([]string, 0, len(view.Members))
	for _, member := range view.Members {
		if member.Active && !member.Draining {
			names = append(names, member.Name)
		}
	}
	return names
}

func (view clusterView) hasMember(name string, instanceID int64) bool {
	for _, member := range view.Members {
		if member.Name == name && member.InstanceID == instanceID {
			return true
		}
	}
	return false
}

func (view clusterView) hasServingMember(name string, instanceID int64) bool {
	for _, member := range view.Members {
		if member.Name == name &&
			member.InstanceID == instanceID &&
			member.Active &&
			!member.Draining {
			return true
		}
	}
	return false
}

func (view clusterView) servingMemberCount() int {
	count := 0
	for _, member := range view.Members {
		if member.Active && !member.Draining {
			count++
		}
	}
	return count
}

// maxMemberRevision 返回当前成员集合中最近一次注册或重启的 etcd Revision。
func maxMemberRevision(keyValues []*mvccpb.KeyValue) int64 {
	var epoch int64
	for _, keyValue := range keyValues {
		if keyValue.ModRevision > epoch {
			epoch = keyValue.ModRevision
		}
	}
	return epoch
}

// buildClusterView 校验成员数据并生成名称稳定排序的不可变视图。
func buildClusterView(
	epoch int64,
	keyValues []*mvccpb.KeyValue,
	expectedMembers map[string]string,
	topology clusterTopology,
) (clusterView, error) {
	topology, err := normalizeClusterTopology(topology, expectedMembers)
	if err != nil {
		return clusterView{}, err
	}
	activeMembers := stringSet(topology.Members)
	view := clusterView{
		Epoch:            epoch,
		ExpectedReplicas: topology.ExpectedReplicas,
		Members:          make([]clusterMember, 0, len(keyValues)),
	}
	seen := make(map[string]struct{}, len(keyValues))
	for _, keyValue := range keyValues {
		var member clusterMember
		if err := json.Unmarshal(keyValue.Value, &member); err != nil {
			return clusterView{}, fmt.Errorf("解析集群成员 %q 失败: %w", keyValue.Key, err)
		}
		expectedAddress, exists := expectedMembers[member.Name]
		if !exists {
			return clusterView{}, fmt.Errorf("控制面包含未配置节点 %q", member.Name)
		}
		if member.Address != expectedAddress {
			return clusterView{}, fmt.Errorf("节点 %q 广播地址 %q 与配置 %q 不一致",
				member.Name, member.Address, expectedAddress)
		}
		if _, exists := seen[member.Name]; exists {
			return clusterView{}, fmt.Errorf("控制面包含重复节点 %q", member.Name)
		}
		seen[member.Name] = struct{}{}
		_, member.Active = activeMembers[member.Name]
		view.Members = append(view.Members, member)
	}
	sort.Slice(view.Members, func(left, right int) bool {
		return view.Members[left].Name < view.Members[right].Name
	})
	return view, nil
}

// normalizeClusterTopology 校验奇数规模、成员唯一性和候选节点白名单。
func normalizeClusterTopology(
	topology clusterTopology,
	expectedMembers map[string]string,
) (clusterTopology, error) {
	if topology.ExpectedReplicas < 3 || topology.ExpectedReplicas%2 == 0 {
		return clusterTopology{}, errors.New("活动拓扑副本数必须是大于等于 3 的奇数")
	}
	if len(topology.Members) != topology.ExpectedReplicas {
		return clusterTopology{}, fmt.Errorf(
			"活动成员数量 %d 与 expected_replicas=%d 不一致",
			len(topology.Members),
			topology.ExpectedReplicas,
		)
	}
	normalized := clusterTopology{
		ExpectedReplicas: topology.ExpectedReplicas,
		Members:          make([]string, 0, len(topology.Members)),
		UpdatedAt:        topology.UpdatedAt.UTC(),
	}
	seen := make(map[string]struct{}, len(topology.Members))
	for _, rawName := range topology.Members {
		name := strings.TrimSpace(rawName)
		if _, exists := expectedMembers[name]; !exists {
			return clusterTopology{}, fmt.Errorf("活动成员 %q 不在候选节点白名单中", name)
		}
		if _, duplicated := seen[name]; duplicated {
			return clusterTopology{}, fmt.Errorf("活动拓扑包含重复节点 %q", name)
		}
		seen[name] = struct{}{}
		normalized.Members = append(normalized.Members, name)
	}
	sort.Strings(normalized.Members)
	if normalized.UpdatedAt.IsZero() {
		normalized.UpdatedAt = time.Now().UTC()
	}
	return normalized, nil
}

func decodeClusterTopology(
	payload []byte,
	expectedMembers map[string]string,
) (clusterTopology, error) {
	var topology clusterTopology
	if err := json.Unmarshal(payload, &topology); err != nil {
		return clusterTopology{}, fmt.Errorf("解析集群拓扑失败: %w", err)
	}
	return normalizeClusterTopology(topology, expectedMembers)
}

func decodeRegisteredMembers(
	keyValues []*mvccpb.KeyValue,
	expectedMembers map[string]string,
) (map[string]clusterMember, error) {
	members := make(map[string]clusterMember, len(keyValues))
	for _, keyValue := range keyValues {
		var member clusterMember
		if err := json.Unmarshal(keyValue.Value, &member); err != nil {
			return nil, fmt.Errorf("解析集群成员 %q 失败: %w", keyValue.Key, err)
		}
		expectedAddress, exists := expectedMembers[member.Name]
		if !exists {
			return nil, fmt.Errorf("控制面包含未配置节点 %q", member.Name)
		}
		if member.Address != expectedAddress {
			return nil, fmt.Errorf(
				"节点 %q 广播地址 %q 与配置 %q 不一致",
				member.Name,
				member.Address,
				expectedAddress,
			)
		}
		if _, duplicated := members[member.Name]; duplicated {
			return nil, fmt.Errorf("控制面包含重复节点 %q", member.Name)
		}
		members[member.Name] = member
	}
	return members, nil
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func isStringSubset(subset, superset []string) bool {
	supersetValues := stringSet(superset)
	for _, value := range subset {
		if _, exists := supersetValues[value]; !exists {
			return false
		}
	}
	return true
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
