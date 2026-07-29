package server

import (
	"fmt"

	rh "chat/server/ringhash"
	"chat/server/store"
	"chat/server/store/types"
)

// rehash 使用提供的节点列表或仅使用未故障状态的节点重新计算 ring hash
// 返回用于 ring hash 的节点列表
func (c *Cluster) rehash(nodes []string) []string {
	ring := rh.New(clusterHashReplicas, nil)

	var ringKeys []string

	if nodes == nil {
		for _, node := range c.nodes {
			ringKeys = append(ringKeys, node.name)
		}
		ringKeys = append(ringKeys, c.thisNodeName)
	} else {
		ringKeys = append(ringKeys, nodes...)
	}
	ring.Add(ringKeys...)

	c.ring.Store(ring)

	return ringKeys
}

// ringSnapshot 返回当前不可变 Ring；clusterInit 会在并发访问前完成首次初始化。
func (c *Cluster) ringSnapshot() *rh.Ring {
	return c.ring.Load()
}

// ringSignature 返回当前集群视图对应的一致性哈希签名。
func (c *Cluster) ringSignature() string {
	return c.ringSnapshot().Signature()
}

// topicOwner 返回当前 Cluster View 中指定 Topic 的 Owner 节点。
func (c *Cluster) topicOwner(topic string) string {
	return c.ringSnapshot().Get(topic)
}

// writeFenceToken 返回当前节点持久化 Topic 消息所需的数据库 fencing token。
func (c *Cluster) writeFenceToken(topic string) (string, int64, string, error) {
	if c == nil {
		return "", 0, "", nil
	}
	if globals.health != nil && !globals.health.AllowsWrites() {
		statsInc("ClusterFencingRejected", 1)
		return "", 0, c.topicOwner(topic), types.ErrClusterFenced
	}
	if c.controlPlane == nil {
		return "", 0, "", nil
	}
	if !c.controlPlane.Ready() {
		statsInc("ClusterFencingRejected", 1)
		return "", 0, "", types.ErrClusterFenced
	}
	owner := c.topicOwner(topic)
	epoch := c.viewEpoch.Load()
	if epoch <= 0 || owner != c.thisNodeName {
		statsInc("ClusterFencingRejected", 1)
		return "", 0, owner, types.ErrClusterFenced
	}
	return c.clusterID, epoch, owner, nil
}

// applyControlPlaneView 先推进共享数据库 fence，再切换 Ring 并通知 Hub 重新评估 Topic。
func (c *Cluster) applyControlPlaneView(view clusterView) error {
	if store.Store == nil || store.Store.GetAdapter() == nil {
		return fmt.Errorf("持久化适配器尚未初始化")
	}
	if err := store.Store.GetAdapter().ClusterFenceAdvance(c.clusterID, view.Epoch); err != nil {
		return fmt.Errorf("推进数据库 cluster fence 失败: %w", err)
	}
	nodes := view.memberNames()
	c.rehash(nodes)
	c.viewEpoch.Store(view.Epoch)
	statsSet("ClusterViewEpoch", view.Epoch)
	if globals.hub == nil {
		return nil
	}
	c.invalidateProxySubs("")
	c.gcProxySessions(nodes)
	globals.hub.rehash <- true
	return nil
}

// boolToInt64 将运行状态转换为 expvar 使用的整数值。
func boolToInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}
