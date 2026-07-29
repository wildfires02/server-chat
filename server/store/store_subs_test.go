// Package store 提供订阅持久化映射的单元测试。
package store

import "testing"

// TestSubscriptionCounterTopic 验证频道订阅计数归属父群，临时 Topic 不参与计数。
func TestSubscriptionCounterTopic(t *testing.T) {
	tests := map[string]string{
		"grpExample": "grpExample",
		"chnExample": "grpExample",
		"usrExample": "",
		"fnd":        "",
	}
	for input, want := range tests {
		if got := subscriptionCounterTopic(input); got != want {
			t.Errorf("subscriptionCounterTopic(%q): want %q, got %q", input, want, got)
		}
	}
}
