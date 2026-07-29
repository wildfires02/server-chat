package server

import (
	"testing"

	"chat/server/store/types"
)

// TestClusterBroadcastFansOutOncePerEdgeNode 验证 Master 不按远端 Session 数重复跨节点发送。
func TestClusterBroadcastFansOutOncePerEdgeNode(t *testing.T) {
	originalCluster := globals.cluster
	globals.cluster = nil
	t.Cleanup(func() {
		globals.cluster = originalCluster
	})

	edgeOne := &Session{
		proto: MULTIPLEX,
		sid:   "grp-test-node-1",
		send:  make(chan any, 8),
		subs:  make(map[string]*Subscription),
	}
	edgeTwo := &Session{
		proto: MULTIPLEX,
		sid:   "grp-test-node-2",
		send:  make(chan any, 8),
		subs:  make(map[string]*Subscription),
	}
	topic := &Topic{
		cat:      types.TopicCatGrp,
		sessions: make(map[*Session]perSessionData),
	}

	// 每个边缘节点各模拟两个终端 Session，它们在 Master 上共享一个 Multiplex Session。
	topic.addSession(&Session{multi: edgeOne}, types.Uid(1), false)
	topic.addSession(&Session{multi: edgeOne}, types.Uid(2), false)
	topic.addSession(&Session{multi: edgeTwo}, types.Uid(3), false)
	topic.addSession(&Session{multi: edgeTwo}, types.Uid(4), false)
	if len(topic.sessions) != 2 {
		t.Fatalf("Master 保存了 %d 个远端投递目标，期望每个节点一个", len(topic.sessions))
	}

	topic.broadcastToSessions(&ServerComMessage{
		Data: &MsgServerData{Topic: "grp-test"},
	})
	if len(edgeOne.send) != 1 || len(edgeTwo.send) != 1 {
		t.Fatalf(
			"跨节点 Fanout 次数 = node-1:%d node-2:%d，期望各 1 次",
			len(edgeOne.send),
			len(edgeTwo.send),
		)
	}
}
