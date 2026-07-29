package server

import "testing"

// TestNormalizeHTTPPath 验证 API 和静态资源挂载点使用相同的路径规则。
func TestNormalizeHTTPPath(t *testing.T) {
	tests := []struct {
		input    string
		fallback string
		expected string
	}{
		{input: "", fallback: "/", expected: "/"},
		{input: "api", fallback: "/", expected: "/api/"},
		{input: "/api", fallback: "/", expected: "/api/"},
		{input: "/api/", fallback: "/", expected: "/api/"},
	}
	for _, test := range tests {
		if actual := normalizeHTTPPath(test.input, test.fallback); actual != test.expected {
			t.Errorf("normalizeHTTPPath(%q) = %q, want %q",
				test.input, actual, test.expected)
		}
	}
}

// TestResolveServingEndpoint 验证公网地址、TCP 监听和 Unix Socket 回退。
func TestResolveServingEndpoint(t *testing.T) {
	tests := []struct {
		name         string
		externalURL  string
		listen       string
		apiPath      string
		tls          bool
		expected     string
		unixFallback bool
	}{
		{
			name:        "external URL",
			externalURL: "chat.example.com/base",
			listen:      ":6060",
			apiPath:     "/api/",
			tls:         true,
			expected:    "https://chat.example.com/base/",
		},
		{
			name:     "TCP wildcard",
			listen:   ":6060",
			apiPath:  "/api/",
			expected: "http://localhost:6060/api/",
		},
		{
			name:         "Unix Socket",
			listen:       "unix:/tmp/chat.sock",
			apiPath:      "/api/",
			tls:          true,
			expected:     "https://localhost/api/",
			unixFallback: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual, unixFallback := resolveServingEndpoint(
				test.externalURL,
				test.listen,
				test.apiPath,
				test.tls,
			)
			if actual != test.expected || unixFallback != test.unixFallback {
				t.Fatalf("resolveServingEndpoint() = (%q, %t), want (%q, %t)",
					actual, unixFallback, test.expected, test.unixFallback)
			}
		})
	}
}
