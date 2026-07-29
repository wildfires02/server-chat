package server

import (
	"errors"
	"net"
	"net/rpc"
	"time"

	"chat/server/logs"
)

// asyncRpcLoop 持续运行asyncRpcLoop，直到输入通道关闭或收到停止信号。
func (n *ClusterNode) asyncRpcLoop() {
	for call := range n.rpcDone {
		n.handleRpcResponse(call)
	}
}

// p2mSenderLoop 持续运行p2mSenderLoop，直到输入通道关闭或收到停止信号。
func (n *ClusterNode) p2mSenderLoop() {
	for req := range n.p2mSender {
		if req == nil {
			// 退出循环
			return
		}

		if err := n.proxyToMaster(req); err != nil {
			logs.Warn.Println("p2mSenderLoop: 调用失败", n.name, err)
		}
	}
}

// Handle 出站节点通信：重联及维护从 Channel 读取消息并转发到远程节点。
func (n *ClusterNode) reconnect() {
	var reconnTicker *time.Ticker

	// 避免并行重连线程
	n.lock.Lock()
	if n.reconnecting {
		n.lock.Unlock()
		return
	}
	n.reconnecting = true
	n.lock.Unlock()

	count := 0
	for {
		// 立即尝试重连
		if conn, err := net.DialTimeout("tcp", n.address, clusterNetworkTimeout); err == nil {
			if reconnTicker != nil {
				reconnTicker.Stop()
			}
			n.lock.Lock()
			n.endpoint = rpc.NewClient(conn)
			n.connected.Store(true)
			n.reconnecting = false
			n.lock.Unlock()
			statsInc("LiveClusterNodes", 1)
			logs.Info.Println("cluster: connected to", n.name)

			// 向新节点发送本节点的凭证
			var unused bool
			n.call("Cluster.Ping",
				&ClusterPing{
					Node:        globals.cluster.thisNodeName,
					Fingerprint: globals.cluster.fingerprint,
				},
				&unused)
			return
		} else if count == 0 {
			reconnTicker = time.NewTicker(clusterDefaultReconnectTime)
		}

		count++

		select {
		case <-reconnTicker.C:
			// 等待定时器以重试重连。定时器未激活时不执行任何操作
		case <-n.done:
			// 正在关闭
			logs.Info.Println("cluster: shutdown started at node", n.name)
			reconnTicker.Stop()
			if n.endpoint != nil {
				n.endpoint.Close()
			}
			n.lock.Lock()
			n.connected.Store(false)
			n.reconnecting = false
			n.lock.Unlock()
			logs.Info.Println("cluster: shut down completed at node", n.name)
			return
		}
	}
}

// call 完成通话所需的内部处理。
func (n *ClusterNode) call(proc string, req, resp any) error {
	if n.grpcPeer != nil {
		return n.grpcPeer.Call(proc, req, resp)
	}
	if !n.connected.Load() {
		return errors.New("cluster: node '" + n.name + "' not connected")
	}

	if err := n.endpoint.Call(proc, req, resp); err != nil {
		logs.Warn.Println("cluster: call failed", n.name, err)

		n.lock.Lock()
		if n.connected.Load() {
			n.endpoint.Close()
			n.connected.Store(false)
			statsInc("LiveClusterNodes", -1)
			go n.reconnect()
		}
		n.lock.Unlock()
		return err
	}

	return nil
}

// handleRpcResponse 处理Rpc响应消息或事件。
func (n *ClusterNode) handleRpcResponse(call *rpc.Call) {
	if call.Error != nil {
		logs.Warn.Printf("cluster: %s call failed: %s", call.ServiceMethod, call.Error)
		if n.grpcPeer != nil {
			return
		}
		n.lock.Lock()
		if n.connected.Load() {
			n.endpoint.Close()
			n.connected.Store(false)
			statsInc("LiveClusterNodes", -1)
			go n.reconnect()
		}
		n.lock.Unlock()
	}
}

// callAsync 完成通话Async所需的内部处理。
func (n *ClusterNode) callAsync(proc string, req, resp any, done chan *rpc.Call) *rpc.Call {
	if done != nil && cap(done) == 0 {
		logs.Err.Panic("cluster: RPC done channel is unbuffered")
	}
	if n.grpcPeer != nil {
		call := &rpc.Call{
			ServiceMethod: proc,
			Args:          req,
			Reply:         resp,
			Done:          done,
		}
		n.asyncCalls.Add(1)
		go func() {
			defer n.asyncCalls.Done()
			call.Error = n.call(proc, req, resp)
			if done != nil {
				n.handleRpcResponse(call)
				done <- call
				return
			}
			select {
			case n.rpcDone <- call:
			case <-n.grpcPeer.context.Done():
			}
		}()
		return call
	}

	if !n.connected.Load() {
		call := &rpc.Call{
			ServiceMethod: proc,
			Args:          req,
			Reply:         resp,
			Error:         errors.New("cluster: node '" + n.name + "' not connected"),
			Done:          done,
		}
		if done != nil {
			done <- call
		}
		return call
	}

	var responseChan chan *rpc.Call
	if done != nil {
		// 如果需要通知调用方，创建单独的响应回调
		myDone := make(chan *rpc.Call, 1)
		go func() {
			call := <-myDone
			n.handleRpcResponse(call)
			if done != nil {
				done <- call
			}
		}()
		responseChan = myDone
	} else {
		responseChan = n.rpcDone
	}

	call := n.endpoint.Go(proc, req, resp, responseChan)

	return call
}

// proxyToMaster 将请求从 Topic 代理转发到 Topic 主节点
func (n *ClusterNode) proxyToMaster(msg *ClusterReq) error {
	msg.Node = globals.cluster.thisNodeName
	var rejected bool
	err := n.call("Cluster.TopicMaster", msg, &rejected)
	if err == nil && rejected {
		err = errors.New("cluster: topic master node out of sync")
	}
	return err
}

// proxyToMaster 将请求从 Topic 代理转发到 Topic 主节点
func (n *ClusterNode) proxyToMasterAsync(msg *ClusterReq) error {
	if n.grpcPeer != nil {
		// gRPC 数据面已经按 Topic 分 Lane；直接等待明确结果，避免外层单队列再次串行化。
		return n.proxyToMaster(msg)
	}
	select {
	case n.p2mSender <- msg:
		return nil
	default:
	}
	// 缓冲已满。短暂等待后放弃
	timer := time.NewTimer(clusterP2MTimeout)
	defer timer.Stop()
	select {
	case n.p2mSender <- msg:
		return nil
	case <-timer.C:
		return errors.New("cluster: load exceeded")
	}
}

// route 在集群内路由服务器消息
func (n *ClusterNode) route(msg *ClusterRoute) error {
	var unused bool
	return n.call("Cluster.Route", msg, &unused)
}

// stopMultiplexingSession 停止Multiplexing会话并释放相关资源。
func (n *ClusterNode) stopMultiplexingSession(msess *Session) {
	if msess == nil {
		return
	}
	msess.stopSession(nil)
	n.lock.Lock()
	delete(n.msess, msess.sid)
	n.lock.Unlock()
}
