package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/rpc"
	"sort"
	"time"

	"chat/server/concurrency"
	"chat/server/logs"
)

// 返回 snowflake worker id
func clusterInit(configString json.RawMessage, self *string, deploymentMode string) int {
	if globals.cluster != nil {
		logs.Err.Fatal("Cluster already initialized.")
	}

	// 即使是独立服务器也要注册这些变量，否则监控软件会报告变量缺失。
	clusterStatsOnce.Do(func() {
		// 如果本节点是集群 leader 则为 1，否则为 0。
		statsRegisterInt("ClusterLeader")
		// 配置的节点总数。
		statsRegisterInt("TotalClusterNodes")
		// 当前认为存活的节点数。
		statsRegisterInt("LiveClusterNodes")
		// 控制面是否持有有效租约和多数派视图。
		statsRegisterInt("ClusterControlPlaneReady")
		// 当前已应用的 etcd Cluster View Revision。
		statsRegisterInt("ClusterViewEpoch")
		// 数据库或路由层拒绝的过期 Owner 写入次数。
		statsRegisterInt("ClusterFencingRejected")
		// 控制面成功提交 Drain 成员视图的次数。
		statsRegisterInt("ClusterDrainTransitions")
		// 控制面成功提交在线扩容或缩容拓扑的次数。
		statsRegisterInt("ClusterTopologyTransitions")
		// 当前节点成功领取的定时任务数。
		statsRegisterInt("ClusterScheduledTaskClaims")
		// 因其他 Owner 已持有短租约而跳过的定时任务数。
		statsRegisterInt("ClusterScheduledTaskClaimConflicts")
		// 当前建立过有效发送的 gRPC Lane 数量。
		statsRegisterInt("ClusterConnectedLanes")
		// 正在每条可靠 Lane 中等待处理的请求总数。
		statsRegisterInt("ClusterLaneQueued")
		// 已完成的 gRPC Lane 请求数。
		statsRegisterInt("ClusterLaneRequests")
		// gRPC Lane 网络、协议或超时失败数。
		statsRegisterInt("ClusterLaneFailures")
		// 因可靠队列达到容量上限而拒绝的请求数。
		statsRegisterInt("ClusterLaneQueueRejected")
		// 当前等待发送的瞬态输入状态和 Presence 数量。
		statsRegisterInt("ClusterEphemeralQueued")
		// 因瞬态队列满或超时而主动丢弃的事件数。
		statsRegisterInt("ClusterEphemeralDropped")
		// 可靠 Lane 因断流等传输错误执行的重试次数。
		statsRegisterInt("ClusterLaneRetries")
		// 服务端命中 Request ID 去重窗口的次数。
		statsRegisterInt("ClusterDedupeHits")
		// 去重窗口达到容量时淘汰已完成记录的次数。
		statsRegisterInt("ClusterDedupeEvictions")
	})

	// 单机模式由已经通过启动门禁的显式 deployment_mode 决定，不再通过
	// cluster_config 或 self 是否为空推断。此分支不会解析节点、创建队列、
	// 注册 gRPC Lane、启动控制面或分配集群 Goroutine。
	if deploymentMode == deploymentModeStandalone {
		logs.Info.Println("Cluster: running as a standalone server.")
		return 1
	}
	if deploymentMode != deploymentModeCluster {
		logs.Err.Fatalf("Cluster: unknown deployment mode %q", deploymentMode)
	}
	if len(configString) == 0 {
		logs.Err.Fatal("Cluster: cluster mode requires cluster_config")
	}

	var config clusterConfig
	if err := json.Unmarshal(configString, &config); err != nil {
		logs.Err.Fatal(err)
	}

	thisName := *self
	if thisName == "" {
		thisName = config.ThisName
	}

	// cluster 模式已经过部署门禁，此处仍防御配置在校验后被意外修改。
	if thisName == "" {
		logs.Err.Fatal("Cluster: cluster mode requires self node identity")
	}

	if config.NumProxyEventGoRoutines != 0 {
		logs.Warn.Println("Cluster config: field num_proxy_event_goroutines is deprecated.")
	}

	globals.cluster = &Cluster{
		clusterID:          config.ClusterID,
		expectedReplicas:   config.ExpectedReplicas,
		initialMembers:     normalizedInitialMembers(config),
		thisNodeName:       thisName,
		fingerprint:        time.Now().UnixNano(),
		advertiseAddr:      config.AdvertiseAddr,
		controlPlaneConfig: config.ControlPlane,
		transportConfig:    config.Transport,
		tlsConfig:          config.TLS,
		nodes:              make(map[string]*ClusterNode),
		proxyEventQueue:    concurrency.NewGoRoutinePool(len(config.Nodes) * 5),
	}

	var nodeNames []string
	for _, host := range config.Nodes {
		nodeNames = append(nodeNames, host.Name)

		if host.Name == thisName {
			globals.cluster.listenOn = host.Addr
			if globals.cluster.advertiseAddr == "" {
				globals.cluster.advertiseAddr = host.Addr
			}
			// 不为本地实例创建集群成员
			continue
		}

		globals.cluster.nodes[host.Name] = &ClusterNode{
			address: host.Addr,
			name:    host.Name,
			done:    make(chan bool, 1),
			msess:   make(map[string]struct{}),
		}
	}

	if len(globals.cluster.nodes) == 0 {
		// 集群至少需要两个节点
		logs.Err.Fatal("Cluster: invalid cluster size: 1")
	}

	if len(globals.cluster.nodes)%2 == 1 {
		// 偶数个节点（自身 + 奇数个）
		logs.Warn.Println("Cluster: use odd number of cluster nodes")
	}

	if config.ControlPlane != nil {
		// etcd 控制面接管成员视图后，不再启动旧的内存选举。
		globals.cluster.rehash(nil)
	} else if !globals.cluster.failoverInit(config.Failover) {
		globals.cluster.rehash(nil)
	}

	sort.Strings(nodeNames)
	workerId := sort.SearchStrings(nodeNames, thisName) + 1

	statsSet("TotalClusterNodes", int64(len(globals.cluster.nodes)+1))

	return workerId
}

// 主节点上的代理 Session 正在关闭
func (sess *Session) closeRPC() {
	if sess.isMultiplex() {
		logs.Info.Println("cluster: session proxy closed", sess.sid)
	}
}

// start 启动控制面、节点连接和集群内部监听器。
func (c *Cluster) start() error {
	// err 贯穿控制面、gRPC 数据面和旧版开发数据面的启动流程。
	var err error
	if c.controlPlaneConfig != nil {
		expectedMembers := make(map[string]string, len(c.nodes)+1)
		expectedMembers[c.thisNodeName] = c.advertiseAddr
		for name, node := range c.nodes {
			expectedMembers[name] = node.address
		}
		controlPlane, err := newEtcdControlPlane(
			*c.controlPlaneConfig,
			c.clusterID,
			c.expectedReplicas,
			expectedMembers,
			c.initialMembers,
		)
		if err != nil {
			return fmt.Errorf("初始化集群控制面失败: %w", err)
		}
		member := clusterMember{
			Name:            c.thisNodeName,
			Address:         c.advertiseAddr,
			InstanceID:      c.fingerprint,
			ProtocolVersion: clusterProtocolVersion,
			StartedAt:       time.Now().UTC(),
		}
		if err = controlPlane.Start(context.Background(), member, c.applyControlPlaneView); err != nil {
			return fmt.Errorf("启动集群控制面失败: %w", err)
		}
		c.controlPlane = controlPlane
		statsSet("ClusterControlPlaneReady", boolToInt64(controlPlane.Ready()))
	}

	var bufferSize = clusterProxyToMasterBuffer
	if len(c.nodes) > 2 {
		// 为大型 (>3 节点) 集群扩展缓冲区
		bufferSize += clusterProxyToMasterBufferPerNode * (len(c.nodes) - 2)
	}
	for _, n := range c.nodes {
		n.rpcDone = make(chan *rpc.Call, len(c.nodes)*clusterRpcCompletionBuffer)
		n.p2mSender = make(chan *ClusterReq, bufferSize)
	}

	if c.transportConfig != nil {
		transportConfig, configErr := normalizeClusterTransportConfig(*c.transportConfig)
		if configErr != nil {
			if c.controlPlane != nil {
				_ = c.controlPlane.Close()
			}
			return configErr
		}
		if c.tlsConfig != nil {
			tlsConfig, tlsConfigErr := normalizeClusterTLSConfig(*c.tlsConfig)
			if tlsConfigErr != nil {
				if c.controlPlane != nil {
					_ = c.controlPlane.Close()
				}
				return tlsConfigErr
			}
			c.tlsMaterial, configErr = loadClusterTLSMaterial(tlsConfig, c.thisNodeName)
			if configErr != nil {
				if c.controlPlane != nil {
					_ = c.controlPlane.Close()
				}
				return configErr
			}
		}
		transport := newClusterGRPCTransport(c, transportConfig)
		if err = transport.Start(); err != nil {
			if c.controlPlane != nil {
				_ = c.controlPlane.Close()
			}
			return err
		}
		c.grpcTransport = transport
	} else {
		addr, resolveErr := net.ResolveTCPAddr("tcp", c.listenOn)
		if resolveErr != nil {
			return fmt.Errorf("解析集群监听地址失败: %w", resolveErr)
		}
		c.inbound, err = net.ListenTCP("tcp", addr)
		if err != nil {
			if c.controlPlane != nil {
				_ = c.controlPlane.Close()
			}
			return fmt.Errorf("监听集群地址失败: %w", err)
		}
		if err = rpc.Register(c); err != nil {
			_ = c.inbound.Close()
			if c.controlPlane != nil {
				_ = c.controlPlane.Close()
			}
			return fmt.Errorf("注册集群 RPC 服务失败: %w", err)
		}
		go rpc.Accept(c.inbound)
		for _, n := range c.nodes {
			go n.reconnect()
		}
	}

	for _, n := range c.nodes {
		n.workers.Add(2)
		go func(node *ClusterNode) {
			defer node.workers.Done()
			node.asyncRpcLoop()
		}(n)
		go func(node *ClusterNode) {
			defer node.workers.Done()
			node.p2mSenderLoop()
		}(n)
	}

	if c.fo != nil {
		go c.run()
	}

	logs.Info.Printf("Cluster of %d nodes initialized, node '%s' is listening on [%s]",
		len(globals.cluster.nodes)+1, globals.cluster.thisNodeName, c.listenOn)
	return nil
}

// changeTopology 通过 etcd CAS 提交新的奇数活动成员集合。
// HTTP 运维入口只负责鉴权和解码，扩缩容的安全约束全部在控制面实现。
func (c *Cluster) changeTopology(
	callContext context.Context,
	members []string,
) (clusterView, error) {
	if c == nil || c.controlPlane == nil {
		return clusterView{}, errors.New("当前进程未启用集群控制面")
	}
	controller, supported := c.controlPlane.(interface {
		ChangeTopology(context.Context, []string) (clusterView, error)
	})
	if !supported {
		return clusterView{}, errors.New("当前集群控制面不支持在线扩缩容")
	}
	view, err := controller.ChangeTopology(callContext, members)
	if err == nil {
		statsInc("ClusterTopologyTransitions", 1)
	}
	return view, err
}

// shutdown 停止shutdown并释放相关资源。
func (c *Cluster) shutdown() {
	if globals.cluster == nil {
		return
	}
	if c.grpcTransport != nil {
		c.grpcTransport.Close()
	}
	if c.inbound != nil {
		_ = c.inbound.Close()
	}
	for _, n := range c.nodes {
		select {
		case n.done <- true:
		default:
		}
		close(n.p2mSender)
		n.asyncCalls.Wait()
		close(n.rpcDone)
	}
	for _, n := range c.nodes {
		n.workers.Wait()
	}
	if c.controlPlane != nil {
		if err := c.controlPlane.Close(); err != nil {
			logs.Warn.Printf("cluster: 关闭控制面失败: %v", err)
		}
	}

	globals.cluster.proxyEventQueue.Stop()

	if c.fo != nil {
		c.fo.done <- true
	}
	globals.cluster = nil

	logs.Info.Println("Cluster shut down")
}

// waitForReliableDrain 等待生产 gRPC 数据面的可靠请求全部完成。
func (c *Cluster) waitForReliableDrain(context context.Context) bool {
	if c == nil || c.grpcTransport == nil {
		return true
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if c.grpcTransport.pendingReliableRequests() == 0 {
			return true
		}
		select {
		case <-ticker.C:
		case <-context.Done():
			return false
		}
	}
}

// beginDrain 请求支持 Drain 的控制面提交新成员视图并迁移 Topic Owner。
func (c *Cluster) beginDrain(callContext context.Context) error {
	if c == nil || c.controlPlane == nil {
		return nil
	}
	controlPlane, supported := c.controlPlane.(interface {
		BeginDrain(context.Context) error
	})
	if !supported {
		return errors.New("当前集群控制面不支持 Drain 成员状态")
	}
	if err := controlPlane.BeginDrain(callContext); err != nil {
		return err
	}
	statsInc("ClusterDrainTransitions", 1)
	return nil
}

// claimScheduledTask 通过控制面短租约领取当前 Owner 的一条定时消息。
func (c *Cluster) claimScheduledTask(
	callContext context.Context,
	taskID string,
) (bool, error) {
	if c == nil || c.controlPlane == nil {
		return true, nil
	}
	claimer, supported := c.controlPlane.(interface {
		ClaimTask(context.Context, string, time.Duration) (bool, error)
	})
	if !supported {
		return false, errors.New("当前集群控制面不支持定时任务领取")
	}
	claimed, err := claimer.ClaimTask(callContext, taskID, defaultScheduledTaskClaimTTL)
	if claimed {
		statsInc("ClusterScheduledTaskClaims", 1)
	} else if err == nil {
		statsInc("ClusterScheduledTaskClaimConflicts", 1)
	}
	return claimed, err
}
