package server

import (
	"strings"
	"testing"
)

// TestNormalizeClusterTransportConfig 验证默认值和生产配置边界。
func TestNormalizeClusterTransportConfig(t *testing.T) {
	config, err := normalizeClusterTransportConfig(clusterTransportConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if config.LaneCount != defaultClusterLaneCount ||
		config.ReliableQueueCapacity != defaultClusterReliableQueueCapacity ||
		config.RequestTimeout != defaultClusterRequestTimeout ||
		config.MaxRetries != defaultClusterMaxRetries ||
		config.DedupeCapacity != defaultClusterDedupeCapacity ||
		config.DedupeTTL != defaultClusterDedupeTTL {
		t.Fatalf("默认 cluster transport 配置不正确：%+v", config)
	}

	tests := []struct {
		name      string
		config    clusterTransportConfig
		wantError string
	}{
		{
			name:      "Lane 数必须为二次幂",
			config:    clusterTransportConfig{LaneCount: 3},
			wantError: "2 的幂",
		},
		{
			name: "可靠队列不能过小",
			config: clusterTransportConfig{
				LaneCount:             4,
				ReliableQueueCapacity: 8,
			},
			wantError: "reliable_queue_capacity",
		},
		{
			name: "请求超时不能短于连接超时",
			config: clusterTransportConfig{
				DialTimeout:    "5s",
				RequestTimeout: "1s",
			},
			wantError: "不能小于",
		},
		{
			name: "重试次数不能无界增长",
			config: clusterTransportConfig{
				MaxRetries: 9,
			},
			wantError: "max_retries",
		},
		{
			name: "去重窗口容量不能过小",
			config: clusterTransportConfig{
				DedupeCapacity: 16,
			},
			wantError: "dedupe_capacity",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeClusterTransportConfig(test.config)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("错误 = %v，期望包含 %q", err, test.wantError)
			}
		})
	}
}
