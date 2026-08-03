package server

import (
	"strings"
	"testing"
)

func TestBusinessPolicyAction(t *testing.T) {
	tests := []struct {
		name        string
		head        map[string]any
		content     any
		attachments []string
		want        string
	}{
		{name: "批量打招呼", head: map[string]any{"x-business-action": "batch_greeting"}, want: "batch_greeting"},
		{name: "通话", head: map[string]any{"webrtc": "started"}, want: "call"},
		{name: "文件", head: map[string]any{headMessageKind: "file"}, want: "document"},
		{name: "媒体附件", attachments: []string{"file-id"}, want: "media"},
		{name: "普通文字", content: "hello", want: "message"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := businessPolicyAction(test.head, test.content, test.attachments); got != test.want {
				t.Fatalf("businessPolicyAction() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAuditPlainText(t *testing.T) {
	if got, ok := auditPlainText(map[string]any{"x-business-action": "batch_greeting"}, "  hello  "); !ok || got != "hello" {
		t.Fatalf("批量打招呼审计内容 = %q, %v", got, ok)
	}
	if _, ok := auditPlainText(map[string]any{"mime": "image/jpeg"}, "ignored"); ok {
		t.Fatal("媒体正文不应进入文字审计")
	}
}

func TestBusinessAuditOutboxKeyFitsPersistentCache(t *testing.T) {
	eventID := strings.Repeat("a", 64)
	key := businessAuditOutboxKey(eventID)
	if len(key) > 64 {
		t.Fatalf("审计 outbox 键长度 = %d，超过 kvmeta.key 的 64 字节限制", len(key))
	}
	if !strings.HasPrefix(key, businessAuditOutboxPrefix) {
		t.Fatalf("审计 outbox 键缺少前缀：%q", key)
	}
	if key != businessAuditOutboxKey(eventID) {
		t.Fatal("同一个事件 ID 必须生成稳定的 outbox 键")
	}
	if key == businessAuditOutboxKey(strings.Repeat("b", 64)) {
		t.Fatal("不同事件 ID 不应生成相同的 outbox 键")
	}
}
