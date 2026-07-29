// Package server 实现即时通信服务端的协议、路由和业务逻辑。
package server

import (
	"math/rand"
	"net/rpc"
	"sync"
	"time"

	"chat/server/logs"
)

// 集群 Leader 选举相关方法。基于 Raft 协议思想。
// Leader 节点向 Follower 节点发送心跳。如果 Follower 节点连续多次心跳失败，
// Leader 节点会宣布该节点失效并触发重新哈希：使用存活节点重新生成 RingHash，
// 并将新的节点列表通知给 Follower。Follower 使用新列表进行重新哈希。
// 当失效节点恢复时，会再次触发重新哈希。

// 故障转移配置。
type clusterFailover struct {
	// 当前 Leader 节点
	leader string
	// 当前选举任期
	term int
	// 心跳间隔
	heartBeat time.Duration
	// 投票超时：连续多少次心跳未收到后触发新选举
	voteTimeout int

	// Leader 认为活跃的节点列表
	activeNodes []string
	// activeNodesLock 保护集群Failover的并发读写。
	activeNodesLock sync.RWMutex
	// 节点连续失败多少次后被宣布失效
	nodeFailCountLimit int

	// 处理 Leader 健康检查的管道
	healthCheck chan *ClusterHealth
	// 处理选举投票的管道
	electionVote chan *ClusterVote
	// 停止故障转移协程的管道
	done chan bool
}

// clusterFailoverConfig 保存集群Failover配置的数据和运行状态。
type clusterFailoverConfig struct {
	// 是否启用故障转移
	Enabled bool `json:"enabled"`
	// 心跳间隔（毫秒）
	Heartbeat int `json:"heartbeat"`
	// 连续多少次心跳失败后触发 Leader 选举
	VoteAfter int `json:"vote_after"`
	// 节点连续失败多少次后被认定失效
	NodeFailAfter int `json:"node_fail_after"`
}

// ClusterHealth 是 Leader 节点健康检查的数据结构。
type ClusterHealth struct {
	// Leader 节点名称
	Leader string
	// 选举任期
	Term int
	// 表示集群的 RingHash 签名
	Signature string
	// 当前活跃的节点名称列表
	Nodes []string
}

// ClusterVoteRequest 是候选节点向其他节点发起的投票请求。
type ClusterVoteRequest struct {
	// 发起请求的候选节点
	Node string
	// 选举任期
	Term int
}

// ClusterVoteResponse 是节点的投票响应。
type ClusterVoteResponse struct {
	// 投票结果
	Result bool
	// 投票后的任期
	Term int
}

// ClusterVote 是 Leader 选举中的投票请求和响应。
type ClusterVote struct {
	// req 保存req。
	req *ClusterVoteRequest
	// resp 传递resp相关的异步事件。
	resp chan ClusterVoteResponse
}

// failoverInit 完成failoverInit所需的内部处理。
func (c *Cluster) failoverInit(config *clusterFailoverConfig) bool {
	if config == nil || !config.Enabled {
		return false
	}
	if len(c.nodes) < 2 {
		logs.Err.Printf("cluster: failover disabled; need at least 3 nodes, got %d", len(c.nodes)+1)
		return false
	}

	// 假设所有节点都存活且健康，生成 RingHash。
	// 这样可以最小化正常运行时的重新哈希次数。
	var activeNodes []string
	for _, node := range c.nodes {
		activeNodes = append(activeNodes, node.name)
	}
	activeNodes = append(activeNodes, c.thisNodeName)
	c.rehash(activeNodes)

	// 随机心跳间隔：0.75 * config.HeartBeat + random(0, 0.5 * config.HeartBeat)。
	// PRNG 在 main.go 中初始化。
	hb := time.Duration(config.Heartbeat) * time.Millisecond
	hb = (hb >> 1) + (hb >> 2) + time.Duration(rand.Intn(int(hb>>1)))

	c.fo = &clusterFailover{
		activeNodes:        activeNodes,
		heartBeat:          hb,
		voteTimeout:        config.VoteAfter,
		nodeFailCountLimit: config.NodeFailAfter,
		healthCheck:        make(chan *ClusterHealth, config.VoteAfter),
		electionVote:       make(chan *ClusterVote, len(c.nodes)),
		done:               make(chan bool, 1),
	}

	logs.Info.Println("cluster: failover mode enabled")

	return true
}

// Health 由 Leader 节点调用，用于宣告领导权并检查 Follower 状态。
func (c *Cluster) Health(health *ClusterHealth, unused *bool) error {
	select {
	case c.fo.healthCheck <- health:
	default:
	}
	return nil
}

// Vote 处理候选节点的投票请求。
func (c *Cluster) Vote(vreq *ClusterVoteRequest, response *ClusterVoteResponse) error {
	respChan := make(chan ClusterVoteResponse, 1)

	c.fo.electionVote <- &ClusterVote{
		req:  vreq,
		resp: respChan,
	}

	*response = <-respChan

	return nil
}

// Leader 节点向 Follower 发送健康检查。
func (c *Cluster) sendHealthChecks() {
	rehash := false

	for _, node := range c.nodes {
		unused := false
		err := node.call("Cluster.Health",
			&ClusterHealth{
				Leader:    c.thisNodeName,
				Term:      c.fo.term,
				Signature: c.ringSignature(),
				Nodes:     c.fo.activeNodes,
			}, &unused)

		if err != nil {
			node.failCount++
			if node.failCount == c.fo.nodeFailCountLimit {
				// 节点连续失败次数超过阈值
				rehash = true
			}
		} else {
			if node.failCount >= c.fo.nodeFailCountLimit {
				// 节点已恢复
				rehash = true
			}
			node.failCount = 0
		}
	}

	if rehash {
		activeNodes := []string{c.thisNodeName}
		for _, node := range c.nodes {
			if node.failCount < c.fo.nodeFailCountLimit {
				activeNodes = append(activeNodes, node.name)
			}
		}
		c.fo.activeNodesLock.Lock()
		c.fo.activeNodes = activeNodes
		c.fo.activeNodesLock.Unlock()
		c.rehash(activeNodes)
		c.invalidateProxySubs("")
		c.gcProxySessions(activeNodes)

		logs.Info.Println("cluster: initiating failover rehash for nodes", activeNodes)
		globals.hub.rehash <- true
	}
}

// electLeader 完成electLeader所需的内部处理。
func (c *Cluster) electLeader() {
	// 增加任期（本节点在本任期中投票给自己）并清空 Leader
	c.fo.term++
	c.fo.leader = ""

	// 确保当前节点不报告自己为 Leader
	statsSet("ClusterLeader", 0)

	logs.Info.Println("cluster: leading new election for term", c.fo.term)

	nodeCount := len(c.nodes)
	// 当选 Leader 需要的票数
	expectVotes := (nodeCount+1)>>1 + 1
	done := make(chan *rpc.Call, nodeCount)

	// 向其他节点异步发送投票请求
	for _, node := range c.nodes {
		response := ClusterVoteResponse{}
		node.callAsync("Cluster.Vote",
			&ClusterVoteRequest{
				Node: c.thisNodeName,
				Term: c.fo.term,
			}, &response, done)
	}

	// 已收到的票数（1 票投给自己）
	voteCount := 1
	timeout := time.NewTimer(c.fo.heartBeat>>1 + c.fo.heartBeat)
	// 等待以下情况之一：
	// 1. 超过半数节点投赞成票
	// 2. 所有节点都已响应
	// 3. 超时
	for i := 0; i < nodeCount && voteCount < expectVotes; {
		select {
		case call := <-done:
			if call.Error == nil {
				if call.Reply.(*ClusterVoteResponse).Result {
					// 投了赞成票
					voteCount++
				} else if c.fo.term < call.Reply.(*ClusterVoteResponse).Term {
					// 投了反对票。放弃选举：本节点的任期落后于集群
					i = nodeCount
					voteCount = 0
				}
			}

			i++
		case <-timeout.C:
			// 跳出循环
			i = nodeCount
		}
	}

	if voteCount >= expectVotes {
		// 当前节点当选为 Leader
		c.fo.leader = c.thisNodeName
		statsSet("ClusterLeader", 1)
		logs.Info.Printf("'%s' elected self as a new leader", c.thisNodeName)
	}
}

// 处理 Leader 选举和维护相关调用的协程。
func (c *Cluster) run() {
	ticker := time.NewTicker(c.fo.heartBeat)

	// 从 Leader 收到的连续未响应健康检查次数
	missed := 0
	// 第一次未收到健康检查时不立即触发重新哈希。如果本节点刚上线，Leader 会在下次检查时将其纳入。
	// 否则会导致重复哈希。
	rehashSkipped := false

	for {
		select {
		case <-ticker.C:
			if c.fo.leader == c.thisNodeName {
				// 我是 Leader，向 Follower 发送健康检查
				c.sendHealthChecks()
			} else {
				// 增加未收到 Leader 健康检查的次数
				// 收到健康检查时该计数器会被重置为 0
				missed++
				if missed >= c.fo.voteTimeout {
					// Leader 已失效，发起新选举
					missed = 0
					c.electLeader()
				}
			}
		case health := <-c.fo.healthCheck:
			// 收到 Leader 的健康检查

			if health.Term < c.fo.term {
				// 来自过期 Leader 的健康检查，忽略
				logs.Warn.Println("cluster: health check from a stale leader", health.Term, c.fo.term, health.Leader, c.fo.leader)
				continue
			}

			if health.Term > c.fo.term {
				c.fo.term = health.Term
				c.fo.leader = health.Leader
				logs.Info.Printf("cluster: leader '%s' elected", c.fo.leader)
			} else if health.Leader != c.fo.leader {
				if c.fo.leader != "" {
					// 错误的 Leader。这是 Bug，不应发生！
					logs.Err.Printf("cluster: wrong leader '%s' while expecting '%s'; term %d",
						health.Leader, c.fo.leader, health.Term)
				} else {
					logs.Info.Printf("cluster: leader set to '%s'", health.Leader)
				}
				c.fo.leader = health.Leader
			}

			// 收到 Leader 的健康检查，说明本节点不是 Leader
			statsSet("ClusterLeader", 0)

			missed = 0
			if health.Signature != c.ringSignature() {
				if rehashSkipped {
					logs.Info.Println("cluster: rehashing at a request of",
						health.Leader, health.Nodes, health.Signature, c.ringSignature())
					c.rehash(health.Nodes)
					c.invalidateProxySubs("")
					c.gcProxySessions(health.Nodes)
					rehashSkipped = false

					globals.hub.rehash <- true
				} else {
					rehashSkipped = true
				}
			}

		case vreq := <-c.fo.electionVote:
			if c.fo.term < vreq.req.Term {
				// 新的选举。本节点尚未投票。投票给请求方并清空当前 Leader
				logs.Info.Printf("Voting YES for %s, my term %d, vote term %d", vreq.req.Node, c.fo.term, vreq.req.Term)
				c.fo.term = vreq.req.Term
				c.fo.leader = ""
				// 选举中意味着尚无 Leader
				statsSet("ClusterLeader", 0)
				vreq.resp <- ClusterVoteResponse{Result: true, Term: c.fo.term}
			} else {
				// 本节点已投票或选举已过期，拒绝
				logs.Info.Printf("Voting NO for %s, my term %d, vote term %d", vreq.req.Node, c.fo.term, vreq.req.Term)
				vreq.resp <- ClusterVoteResponse{Result: false, Term: c.fo.term}
			}
		case <-c.fo.done:
			return
		}
	}
}
