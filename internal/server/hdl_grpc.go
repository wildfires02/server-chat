/******************************************************************************
 *
 *  描述 :
 *
 *    gRPC 连接处理器。另见 hdl_websock.go（WebSocket）和
 *    hdl_longpoll.go（长轮询）。
 *
 *****************************************************************************/

// Package server 实现即时通信服务端的协议、路由和业务逻辑。
package server

import (
	"crypto/tls"
	"io"
	"time"

	"chat/api/pbx"
	"chat/server/logs"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// grpcNodeServer 保存gRPC节点服务端的数据和运行状态。
type grpcNodeServer struct {
	// Embedded 嵌入公共状态或行为，供当前结构直接复用。
	pbx.UnimplementedNodeServer
}

// closeGrpc 停止gRPC并释放相关资源。
func (sess *Session) closeGrpc() {
	if sess.proto == GRPC {
		sess.lock.Lock()
		sess.grpcnode = nil
		sess.lock.Unlock()
	}
}

// 相当于同时启动新 Session 和读取循环。
func (*grpcNodeServer) MessageLoop(stream pbx.Node_MessageLoopServer) error {
	if !serviceAcceptingConnections() {
		return status.Error(codes.Unavailable, "服务未就绪或正在 Drain")
	}
	sess, count := globals.sessionStore.NewSession(stream, "")
	if p, ok := peer.FromContext(stream.Context()); ok {
		sess.remoteAddr = p.Addr.String()
	}
	logs.Info.Println("grpc: session started", sess.sid, sess.remoteAddr, count)

	defer func() {
		sess.closeGrpc()
		sess.cleanUp(false)
	}()

	go sess.writeGrpcLoop()

	for {
		in, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			logs.Err.Println("grpc: recv", sess.sid, err)
			return err
		}
		statsInc("IncomingMessagesGrpcTotal", 1)
		sess.dispatch(pbCliDeserialize(in))

		sess.lock.Lock()
		if sess.grpcnode == nil {
			sess.lock.Unlock()
			break
		}
		sess.lock.Unlock()
	}

	return nil
}

// sendMessageGrpc 处理消息gRPC消息或事件。
func (sess *Session) sendMessageGrpc(msg any) bool {
	if len(sess.send) > sendQueueLimit {
		logs.Err.Println("grpc: outbound queue limit exceeded", sess.sid)
		return false
	}
	statsInc("OutgoingMessagesGrpcTotal", 1)
	if err := grpcWrite(sess, msg); err != nil {
		logs.Err.Println("grpc: write", sess.sid, err)
		return false
	}
	return true
}

// writeGrpcLoop 保存gRPCLoop。
func (sess *Session) writeGrpcLoop() {
	defer func() {
		sess.closeGrpc() //退出MessageLoop
	}()

	for {
		select {
		case msg, ok := <-sess.send:
			if !ok {
				// Channel 已关闭
				return
			}
			sess.releaseOutbound(msg)
			switch v := msg.(type) {
			case []*ServerComMessage: // 批量未序列化的消息
				for _, msg := range v {
					w := sess.serializeAndUpdateStats(msg)
					if !sess.sendMessageGrpc(w) {
						return
					}
				}
			case *ServerComMessage: // 单个未序列化的消息
				w := sess.serializeAndUpdateStats(v)
				if !sess.sendMessageGrpc(w) {
					return
				}
			default: // 已序列化的消息
				if !sess.sendMessageGrpc(v) {
					return
				}
			}

		case <-sess.bkgTimer.C:
			if sess.background {
				sess.background = false
				sess.onBackgroundTimer()
			}

		case msg := <-sess.stop:
			// 请求关闭，不关心消息是否已送达
			if msg != nil {
				grpcWrite(sess, msg)
			}
			return

		case topic := <-sess.detach:
			sess.delSub(topic)
		}
	}
}

// grpcWrite 完成gRPCWrite所需的内部处理。
func grpcWrite(sess *Session, msg any) error {
	if out := sess.grpcnode; out != nil {
		// 如果 msg 不是 *pbx.ServerMsg 类型将会 panic。这是有意为之的 panic。
		return out.Send(msg.(*pbx.ServerMsg))
	}
	return nil
}

// serveGrpc 处理gRPC消息或事件。
func serveGrpc(addr string, kaEnabled bool, tlsConf *tls.Config) (*grpc.Server, error) {
	if addr == "" {
		return nil, nil
	}

	lis, err := netListener(addr)
	if err != nil {
		return nil, err
	}

	secure := ""
	var opts []grpc.ServerOption
	opts = append(opts, grpc.MaxRecvMsgSize(int(globals.maxMessageSize)))
	if tlsConf != nil {
		opts = append(opts, grpc.Creds(credentials.NewTLS(tlsConf)))
		secure = " secure"
	}

	if kaEnabled {
		kepConfig := keepalive.EnforcementPolicy{
			MinTime:             1 * time.Second, // 如果客户端每秒 ping 超过一次，则终止连接
			PermitWithoutStream: true,            // 即使没有活跃流也允许 ping
		}
		opts = append(opts, grpc.KeepaliveEnforcementPolicy(kepConfig))

		kpConfig := keepalive.ServerParameters{
			Time:    60 * time.Second, // 客户端空闲 60 秒后 ping 以确认连接仍然活跃
			Timeout: 20 * time.Second, // 等待 ping 确认 20 秒，超时则认定连接已断开
		}
		opts = append(opts, grpc.KeepaliveParams(kpConfig))
	}

	srv := grpc.NewServer(opts...)
	pbx.RegisterNodeServer(srv, &grpcNodeServer{})
	logs.Info.Printf("gRPC/%s%s server is registered at [%s]", grpc.Version, secure, addr)

	go func() {
		if err := srv.Serve(lis); err != nil {
			logs.Err.Println("gRPC server failed:", err)
		}
	}()

	return srv, nil
}
