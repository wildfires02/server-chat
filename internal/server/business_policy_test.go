package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"chat/server/store"
	"chat/server/store/mock_store"
	"chat/server/store/types"
	"go.uber.org/mock/gomock"
)

type businessPolicyRoundTripFunc func(*http.Request) (*http.Response, error)

func (function businessPolicyRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

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

func TestAuthorizeActorUsesSelfTargetForGroupCapability(t *testing.T) {
	controller := gomock.NewController(t)
	users := mock_store.NewMockUsersPersistenceInterface(controller)
	previous := store.Users
	store.Users = users
	t.Cleanup(func() { store.Users = previous })

	uid := types.Uid(7)
	managed := &types.User{Trusted: map[string]any{
		"identity_provider": "server",
		"external_id":       "700",
	}}
	users.EXPECT().Get(uid).Return(managed, nil).Times(2)

	transport := businessPolicyRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		var input businessPolicyRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.ActorExternalID != "700" || input.TargetExternalID != "700" ||
			input.Action != "document" || input.Topic != "grpTest" {
			t.Fatalf("意外的群能力请求：%+v", input)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"allowed":true}`)),
			Header:     make(http.Header),
		}, nil
	})

	client, err := newBusinessPolicyClient(businessPolicyConfig{
		Enabled: true, Endpoint: "http://business-policy.test", BearerToken: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = transport
	if err = client.authorizeActor(uid, "document", "grpTest"); err != nil {
		t.Fatalf("群文档发送者能力校验失败：%v", err)
	}
}
