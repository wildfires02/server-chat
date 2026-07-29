// Package types 提供领域模型及持久化访问层。
package types

import "testing"

// TestMessageClientKey 验证 Message Client Key 相关行为。
func TestMessageClientKey(t *testing.T) {
	first := MessageClientKey(Uid(1), "device-a:42")
	if len(first) != 43 {
		t.Fatalf("unexpected client key length: %d", len(first))
	}
	if first != MessageClientKey(Uid(1), "device-a:42") {
		t.Fatal("client key is not deterministic")
	}
	if first == MessageClientKey(Uid(2), "device-a:42") {
		t.Fatal("client key is not scoped by sender")
	}
	if first == MessageClientKey(Uid(1), "device-a:43") {
		t.Fatal("client key is not scoped by client id")
	}
	if MessageClientKey(Uid(1), "") != "" || MessageClientKey(ZeroUid, "device-a:42") != "" {
		t.Fatal("empty sender or client id must not create an idempotency key")
	}
}

// TestClusterFenceKey 验证数据库 fence 键稳定、定长且按逻辑集群隔离。
func TestClusterFenceKey(t *testing.T) {
	first := ClusterFenceKey("im-production")
	if len(first) > 64 {
		t.Fatalf("cluster fence key 超过 kvmeta 键长度：%d", len(first))
	}
	if first != ClusterFenceKey("im-production") {
		t.Fatal("cluster fence key 必须稳定")
	}
	if first == ClusterFenceKey("im-staging") {
		t.Fatal("不同逻辑集群不能共享 fence key")
	}
}

// TestMessageClusterFence 验证完整令牌与损坏的部分令牌能够被区分。
func TestMessageClusterFence(t *testing.T) {
	standalone := &Message{}
	if standalone.HasClusterFence() || standalone.HasAnyClusterFenceField() {
		t.Fatal("standalone 消息不应携带 cluster fence")
	}

	partial := &Message{ClusterId: "im-production"}
	if partial.HasClusterFence() || !partial.HasAnyClusterFenceField() {
		t.Fatal("部分 cluster fence 字段必须被识别为损坏令牌")
	}

	complete := &Message{
		ClusterId:    "im-production",
		ClusterEpoch: 42,
		ClusterOwner: "im-1",
	}
	if !complete.HasClusterFence() || !complete.HasAnyClusterFenceField() {
		t.Fatal("完整 cluster fence 令牌未被识别")
	}
}
