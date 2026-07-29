package server

import (
	"fmt"
	"math"
	"testing"

	rh "chat/server/ringhash"
)

const clusterCapacityTopicSamples = 100_000

// TestClusterRingBalancesProductionTopologies 验证 3/5 节点的 Topic Owner
// 分布不会出现会显著浪费节点容量的哈希倾斜。
func TestClusterRingBalancesProductionTopologies(t *testing.T) {
	for _, nodeCount := range []int{3, 5} {
		t.Run(fmt.Sprintf("%d-nodes", nodeCount), func(t *testing.T) {
			nodes := clusterCapacityNodeNames(nodeCount)
			ring := clusterCapacityRing(nodes)
			counts := clusterCapacityDistribution(ring)
			expected := float64(clusterCapacityTopicSamples) / float64(nodeCount)
			t.Logf("%d 节点 Topic Owner 分布：%v", nodeCount, counts)

			for _, node := range nodes {
				deviation := math.Abs(float64(counts[node])-expected) / expected
				if deviation > 0.20 {
					t.Fatalf(
						"%d 节点 Topic 分布倾斜：%s=%d，均值=%.0f，偏差=%.2f%%，全部=%v",
						nodeCount,
						node,
						counts[node],
						expected,
						deviation*100,
						counts,
					)
				}
			}
		})
	}
}

// TestClusterRingMovesOnlyAffectedOwners 验证加节点时 Topic 只迁往新节点，
// 减节点时只有原故障节点持有的 Topic 发生迁移。
func TestClusterRingMovesOnlyAffectedOwners(t *testing.T) {
	threeNodes := clusterCapacityNodeNames(3)
	fiveNodes := clusterCapacityNodeNames(5)
	threeNodeRing := clusterCapacityRing(threeNodes)
	fiveNodeRing := clusterCapacityRing(fiveNodes)
	removedNodeRing := clusterCapacityRing(threeNodes[:2])

	addedNodeMoves := 0
	removedNodeMoves := 0
	for index := range clusterCapacityTopicSamples {
		topic := fmt.Sprintf("grp-capacity-%d", index)
		ownerBefore := threeNodeRing.Get(topic)

		ownerAfterAdd := fiveNodeRing.Get(topic)
		if ownerAfterAdd != ownerBefore {
			addedNodeMoves++
			if ownerAfterAdd != "im-3" && ownerAfterAdd != "im-4" {
				t.Fatalf(
					"扩容把 Topic %q 从 %q 迁往已有节点 %q",
					topic,
					ownerBefore,
					ownerAfterAdd,
				)
			}
		}

		ownerAfterRemove := removedNodeRing.Get(topic)
		if ownerAfterRemove != ownerBefore {
			removedNodeMoves++
			if ownerBefore != "im-2" {
				t.Fatalf(
					"缩容迁移了未受影响的 Topic %q：%q -> %q",
					topic,
					ownerBefore,
					ownerAfterRemove,
				)
			}
		}
	}
	if addedNodeMoves == 0 || removedNodeMoves == 0 {
		t.Fatalf(
			"扩缩容没有产生可验证的 Owner 迁移：add=%d remove=%d",
			addedNodeMoves,
			removedNodeMoves,
		)
	}
	t.Logf(
		"3→5 扩容迁移 %.2f%%，3→2 缩容迁移 %.2f%%",
		float64(addedNodeMoves)*100/clusterCapacityTopicSamples,
		float64(removedNodeMoves)*100/clusterCapacityTopicSamples,
	)
}

// BenchmarkClusterTopicOwner3Nodes 测量三节点生产 Ring 的 Topic Owner 查询成本。
func BenchmarkClusterTopicOwner3Nodes(b *testing.B) {
	benchmarkClusterTopicOwner(b, 3)
}

// BenchmarkClusterTopicOwner5Nodes 测量五节点生产 Ring 的 Topic Owner 查询成本。
func BenchmarkClusterTopicOwner5Nodes(b *testing.B) {
	benchmarkClusterTopicOwner(b, 5)
}

// benchmarkClusterTopicOwner 在固定 Topic 集合上执行 Owner 查询。
func benchmarkClusterTopicOwner(b *testing.B, nodeCount int) {
	ring := clusterCapacityRing(clusterCapacityNodeNames(nodeCount))
	topics := make([]string, 4096)
	for index := range topics {
		topics[index] = fmt.Sprintf("grp-benchmark-%d", index)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := range b.N {
		_ = ring.Get(topics[index&(len(topics)-1)])
	}
}

// clusterCapacityNodeNames 返回稳定的生产节点名称。
func clusterCapacityNodeNames(nodeCount int) []string {
	nodes := make([]string, nodeCount)
	for index := range nodes {
		nodes[index] = fmt.Sprintf("im-%d", index)
	}
	return nodes
}

// clusterCapacityRing 创建与生产路由相同副本数的一致性哈希环。
func clusterCapacityRing(nodes []string) *rh.Ring {
	ring := rh.New(clusterHashReplicas, nil)
	ring.Add(nodes...)
	return ring
}

// clusterCapacityDistribution 统计固定数量 Topic 的 Owner 分布。
func clusterCapacityDistribution(ring *rh.Ring) map[string]int {
	counts := make(map[string]int)
	for index := range clusterCapacityTopicSamples {
		counts[ring.Get(fmt.Sprintf("grp-capacity-%d", index))]++
	}
	return counts
}
