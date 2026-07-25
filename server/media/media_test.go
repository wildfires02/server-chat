package media

import (
	"strings"
	"testing"
)

// TestMatchCORSOrigin 测试 CORS 跨域源匹配与解析逻辑
func TestMatchCORSOrigin(t *testing.T) {
	cases := []struct {
		allowed      []string
		origin       string
		expected     string
		expectError  bool
		errorMessage string
	}{
		{
			allowed:  []string{"https://example.com"},
			origin:   "https://example.com",
			expected: "https://example.com",
		},
		{
			allowed:  []string{"https://example2.com", "https://example.com"},
			origin:   "https://example.com",
			expected: "https://example.com",
		},
		{
			allowed:  []string{"*"},
			origin:   "https://example.com",
			expected: "https://example.com",
		},
		{
			allowed:  []string{""},
			origin:   "https://example.com",
			expected: "",
		},
		{
			allowed:  []string{},
			origin:   "",
			expected: "",
		},
		{
			allowed:  []string{"https://example.com"},
			origin:   "",
			expected: "",
		},
		{
			allowed:  []string{},
			origin:   "https://example.com",
			expected: "",
		},
		{
			allowed:  []string{"http://example.com"},
			origin:   "https://example.com",
			expected: "",
		},
		{
			allowed:  []string{"https://example.com"},
			origin:   "http://example.com",
			expected: "",
		},
		{
			allowed:  []string{"http://example.com:8000"},
			origin:   "http://example.com:8000",
			expected: "http://example.com:8000",
		},
		{
			allowed:  []string{"http://localhost:8090"},
			origin:   "http://localhost:8090",
			expected: "http://localhost:8090",
		},
		{
			allowed:  []string{"http://localhost:8090"},
			origin:   "http://localhost:8080",
			expected: "",
		},
		{
			allowed:  []string{"https://example.com"},
			origin:   "https://sub.example.com",
			expected: "",
		},
		{
			allowed:  []string{"https://*.example.com"},
			origin:   "https://sub.example.com",
			expected: "https://sub.example.com",
		},
		{
			allowed:  []string{"https://*.example.com"},
			origin:   "https://pre.sub.example.com",
			expected: "",
		},
		{
			allowed:  []string{"https://*.example.com", "https://*.sub.example.com"},
			origin:   "https://pre.sub.example.com",
			expected: "https://pre.sub.example.com",
		},
		{
			allowed:  []string{"https://*.*.example.com"},
			origin:   "https://pre.sub.example.com",
			expected: "https://pre.sub.example.com",
		},
		{
			allowed:  []string{"https://*.sub.example.com"},
			origin:   "https://pre.asd.example.com",
			expected: "",
		},
		{
			allowed:  []string{"https://pre.*.example.com"},
			origin:   "https://pre.sub.example.com",
			expected: "https://pre.sub.example.com",
		},
		{
			allowed:  []string{"https://*.*.*.example.com"},
			origin:   "https://www.pre.sub.example.com",
			expected: "https://www.pre.sub.example.com",
		},
		{
			allowed:  []string{"*"},
			origin:   "",
			expected: "*", // 允许任何来源，包括空来源
		},
		// 错误测试用例 - 应该在 ParseCORSAllow 阶段报错失败
		{
			allowed:      []string{"*", "https://example.com"},
			origin:       "https://example.com",
			expectError:  true,
			errorMessage: "wildcard origin '*' must be the only entry",
		},
		{
			allowed:      []string{"not-a-valid-url"},
			origin:       "https://example.com",
			expectError:  true,
			errorMessage: "invalid URI for request",
		},
		{
			allowed:      []string{"://invalid-url"},
			origin:       "https://example.com",
			expectError:  true,
			errorMessage: "missing protocol scheme",
		},
		{
			allowed:      []string{"https://", "example.com"},
			origin:       "https://example.com",
			expectError:  true,
			errorMessage: "invalid URI for request",
		},
		// 合法测试用例 - 不应产生错误
		{
			allowed:      []string{"", "https://example.com"},
			origin:       "https://example.com",
			expectError:  true,
			errorMessage: "empty allowed origin '' must be the only entry", // 空字符串配置使所有 Origin 不被允许
		},
		{
			allowed:  []string{"https://Example.com"},
			origin:   "https://example.com",
			expected: "", // 因大小写敏感而不应匹配
		},
		{
			allowed:  []string{"https://example.com/"},
			origin:   "https://example.com",
			expected: "", // 因结尾斜杠而不应匹配
		},
		{
			allowed:  []string{"https://example.com"},
			origin:   "not-a-valid-url",
			expected: "", // 应对格式错误的 Origin 优雅处理
		},
		{
			allowed:  []string{"https://example.*.com"},
			origin:   "https://example.sub.com",
			expected: "https://example.sub.com",
		},
		{
			allowed:  []string{"https://*.com"},
			origin:   "https://example.com",
			expected: "https://example.com",
		},
		{
			allowed:  []string{"http://*.example.com"},
			origin:   "https://sub.example.com",
			expected: "", // 因协议差异 (http vs https) 不应匹配
		},
		{
			allowed:  []string{"https://example.com", "https://example.com"},
			origin:   "https://example.com",
			expected: "https://example.com", // 重复项仍应正常匹配
		},
	}

	for i, tc := range cases {
		allowedOrigins, err := ParseCORSAllow(tc.allowed)

		if tc.expectError {
			if err == nil {
				t.Errorf("测试用例 %d: 预期产生错误但未产生。Allowed: %v", i, tc.allowed)
				continue
			}
			if tc.errorMessage != "" && !containsSubstring(err.Error(), tc.errorMessage) {
				t.Errorf("测试用例 %d: 预期错误包含 '%s'，实际得到 '%s'", i, tc.errorMessage, err.Error())
			}
			// 对于预期错误的用例，无需测试匹配逻辑
			continue
		}

		if err != nil {
			t.Errorf("测试用例 %d: 解析允许源发生未预期错误: %v. Allowed: %v", i, err, tc.allowed)
			continue
		}

		result := matchCORSOrigin(allowedOrigins, tc.origin)
		if result != tc.expected {
			t.Errorf("测试用例 %d: CORS 源匹配结果错误。预期 '%s'，实际得到 '%s'。Allowed: %v, Origin: '%s'",
				i, tc.expected, result, tc.allowed, tc.origin)
		}
	}
}

// 检查字符串是否包含子串的辅助函数（不区分大小写）
func containsSubstring(str, substr string) bool {
	return strings.Contains(strings.ToLower(str), strings.ToLower(substr))
}
