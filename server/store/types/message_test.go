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
