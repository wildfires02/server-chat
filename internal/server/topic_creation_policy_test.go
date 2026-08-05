package server

import "testing"

func TestTopicCreationAllowedByTrusted(t *testing.T) {
	tests := []struct {
		name    string
		trusted any
		allowed bool
	}{
		{name: "native identity remains allowed", trusted: nil, allowed: true},
		{
			name: "managed customer is denied",
			trusted: map[string]any{
				"identity_provider": "server",
				"external_id":       "42",
				"staff":             false,
				"agent_verified":    false,
			},
			allowed: false,
		},
		{
			name: "managed staff is allowed",
			trusted: map[string]any{
				"identity_provider": "server",
				"external_id":       "43",
				"staff":             true,
			},
			allowed: true,
		},
		{
			name:    "verified agent is allowed",
			trusted: `{"identity_provider":"server","external_id":"44","agent_verified":true}`,
			allowed: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if allowed := topicCreationAllowedByTrusted(test.trusted); allowed != test.allowed {
				t.Fatalf("topicCreationAllowedByTrusted() = %t, want %t", allowed, test.allowed)
			}
		})
	}
}

func TestTopicCreationRequestIncludesGroupsAndChannels(t *testing.T) {
	for _, topic := range []string{"new", "new123", "nch", "nch123"} {
		if !isTopicCreationRequest(topic) {
			t.Fatalf("%q 应被识别为 Topic 创建请求", topic)
		}
	}
	for _, topic := range []string{"grpExisting", "chnExisting", "usrExisting"} {
		if isTopicCreationRequest(topic) {
			t.Fatalf("%q 不应被识别为 Topic 创建请求", topic)
		}
	}
}
