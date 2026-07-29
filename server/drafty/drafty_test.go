// Package drafty 实现即时通信服务端的协议、路由和业务逻辑。
package drafty

import (
	"encoding/json"
	"testing"
)

// validInputs 保存validInputs的共享实例或运行状态。
var validInputs = []string{
	`"This is a plain text string."`,
	`{
		"txt":"This is a string with a line break.",
		"fmt":[{"at":9,"tp":"BR"}]
	}`,
	`{
		"ent":[{"data":{"mime":"image/jpeg","name":"hello.jpg","val":"<38992, bytes: ...>","width":100, "height":80},"tp":"EX"}],
		"fmt":[{"at":-1, "key":0}]
	}`,
	`{
		"ent":[{"data":{"url":"https://www.youtube.com/watch?v=dQw4w9WgXcQ"},"tp":"LN"}],
		"fmt":[{"len":22}],
		"txt":"https://api.tin-im.co/"
	}`,
	`{
		"ent":[{"data":{"url":"https://api.tin-im.co/"},"tp":"LN"}],
		"fmt":[{"len":22}],
		"txt":"https://api.tin-im.co/"
	}`,
	`{
		"ent":[{"data":{"url":"http://tin-im.co"},"tp":"LN"}],
		"fmt":[{"at":9,"len":3}, {"at":4,"len":3}],
		"txt":"Url one, two"
	}`,
	`{
		"ent":[{"data":{"height":213,"mime":"image/jpeg","name":"roses.jpg","val":"<38992, bytes: ...>","width":638},"tp":"IM"}],
		"fmt":[{"len":1}],
		"txt":" "
	}`,
	`{
		"txt":"This text has staggered formats",
		"fmt":[{"at":5,"len":8,"tp":"EM"},{"at":10,"len":13,"tp":"ST"}]
	}`,
	`{
		"txt":"This text is formatted and deleted too",
		"fmt":[{"at":5,"len":4,"tp":"ST"},{"at":13,"len":9,"tp":"EM"},{"at":35,"len":3,"tp":"ST"},{"at":27,"len":11,"tp":"DL"}]
	}`,
	`{
		"txt":"мультибайтовый юникод",
		"fmt":[{"len":14,"tp":"ST"},{"at":15,"len":6,"tp":"EM"}]
	}`,
	`{
		"txt":"Alice Johnson    This is a test",
		"fmt":[{"at":13,"len":1,"tp":"BR"},{"at":15,"len":1},{"len":13,"key":1},{"len":16,"tp":"QQ"},{"at":16,"len":1,"tp":"BR"}],
		"ent":[{"tp":"IM","data":{"mime":"image/jpeg","val":"<1292, bytes: /9j/4AAQSkZJ...rehH5o6D/9k=>","width":25,"height":14,"size":968}},{"tp":"MN","data":{"color":2}}]
	}`,
	`{
		"txt": "Hello 😀, o😀k https://google.com",
		"fmt":[{"at":9,"len":3,"tp":"ST"},{"at":13,"len":18}],
		"ent":[{"tp":"LN","data":{"url":"https://google.com"}}]
	}`,
	`{
		"txt": "Hi 🏴󠁧󠁢󠁳󠁣󠁴󠁿🏴󠁧󠁢󠁳󠁣󠁴󠁿🏴󠁧󠁢󠁳󠁣󠁴󠁿🏴󠁧󠁢󠁳󠁣󠁴󠁿 🏴󠁧󠁢󠁳󠁣󠁴󠁿🏴󠁧󠁢󠁳󠁣󠁴󠁿🏴󠁧󠁢󠁳󠁣󠁴󠁿🏴󠁧󠁢󠁳󠁣󠁴󠁿 🏴󠁧󠁢󠁳󠁣󠁴󠁿🏴󠁧󠁢󠁳󠁣󠁴󠁿🏴󠁧󠁢󠁳󠁣󠁴󠁿🏴󠁧󠁢󠁳󠁣󠁴󠁿",
		"fmt":[{"at":3,"len":4,"tp":"ST"},{"at":8,"len":4,"tp":"ST"}]
	}`,
}

// invalidInputs 保存invalidInputs的共享实例或运行状态。
var invalidInputs = []string{
	`{
		"txt":"This should fail",
		"fmt":[{"at":50,"len":-45,"tp":"ST"}]
	}`,
	`{
		"txt":"This should fail",
		"fmt":[{"at":0,"len":50,"tp":"ST"}]
	}`,
	`{
		"ent":[],
		"fmt":[{"at":0,"len":1,"tp":"ST","key":1}]
	}`,
	`{
		"ent":[{"xy": true, "tp": "XY"}],
		"fmt":[{"len":1,"key":-2}],
		"txt":" "
	}`,
	`{
		"ent":[{"data": true, "tp": "ST"}],
		"fmt":[{"len":1,"key":42, "at":"33"}],
		"txt":"123"
	}`,
	`{
		"txt":true
	}`,
	`{
		"invalid":[{"data": true, "tp": "ST"}],
		"content":[{"len":1, "key":42}]
	}`,
}

// TestPlainText 验证 Plain Text 相关行为。
func TestPlainText(t *testing.T) {
	expect := []string{
		"This is a plain text string.",
		"This is a\n string with a line break.",
		"[FILE 'hello.jpg']",
		"[https://api.tin-im.co/](https://www.youtube.com/watch?v=dQw4w9WgXcQ)",
		"https://api.tin-im.co/",
		"Url [one](http://tin-im.co), [two](http://tin-im.co)",
		"[IMAGE 'roses.jpg']",
		"This _text has_ staggered formats",
		"This *text* is _formatted_ and ~deleted *too*~",
		"*мультибайтовый* _юникод_",
		"This is a test",
		"Hello 😀, *o😀k* https://google.com",
		"Hi *🏴󠁧󠁢󠁳󠁣󠁴󠁿🏴󠁧󠁢󠁳󠁣󠁴󠁿🏴󠁧󠁢󠁳󠁣󠁴󠁿🏴󠁧󠁢󠁳󠁣󠁴󠁿* *🏴󠁧󠁢󠁳󠁣󠁴󠁿🏴󠁧󠁢󠁳󠁣󠁴󠁿🏴󠁧󠁢󠁳󠁣󠁴󠁿🏴󠁧󠁢󠁳󠁣󠁴󠁿* 🏴󠁧󠁢󠁳󠁣󠁴󠁿🏴󠁧󠁢󠁳󠁣󠁴󠁿🏴󠁧󠁢󠁳󠁣󠁴󠁿🏴󠁧󠁢󠁳󠁣󠁴󠁿",
	}

	for i := range validInputs {
		var val any
		if err := json.Unmarshal([]byte(validInputs[i]), &val); err != nil {
			t.Errorf("Failed to parse input %d '%s': %s", i, validInputs[i], err)
		}
		res, err := PlainText(val)
		if err != nil {
			t.Errorf("%d failed with error: %s", i, err)
		} else if res != expect[i] {
			t.Errorf("%d output '%s' does not match '%s'", i, res, expect[i])
		}
	}

	for i := range invalidInputs {
		var val any
		if err := json.Unmarshal([]byte(invalidInputs[i]), &val); err != nil {
			// 不要将其视为错误：我们不是在测试 json.Unmarshal 的有效性。
			t.Logf("Failed to parse input %d '%s': %s", i, invalidInputs[i], err)
		}
		res, err := PlainText(val)
		if err == nil {
			t.Errorf("invalid input %d '%s' did not cause an error '%s'", i, invalidInputs[i], res)
		}
	}
}

// TestSearchText 验证全文索引文本会归一化正文并保留可搜索的文件名。
func TestSearchText(t *testing.T) {
	content := map[string]any{
		"txt": "Ａ版本说明 ",
		"fmt": []any{map[string]any{"at": -1, "len": 0, "key": 0}},
		"ent": []any{map[string]any{
			"tp": "EX",
			"data": map[string]any{
				"name": "发布清单.pdf",
				"mime": "application/pdf",
				"ref":  "https://example.test/file",
				"size": float64(10),
			},
		}},
	}
	got, err := SearchText(content)
	if err != nil {
		t.Fatalf("SearchText failed: %v", err)
	}
	if want := "A版本说明  发布清单.pdf"; got != want {
		t.Fatalf("SearchText: want %q, got %q", want, got)
	}
}

// TestPreview 验证 Preview 相关行为。
func TestPreview(t *testing.T) {
	expect := []string{
		`{"txt":"This is a plain"}`,
		`{"txt":"This is a strin","fmt":[{"tp":"BR","at":9}]}`,
		`{"fmt":[{"at":-1}],"ent":[{"tp":"EX","data":{"height":80,"mime":"image/jpeg","name":"hello.jpg","width":100}}]}`,
		`{"txt":"https://api.tin","fmt":[{"len":15}],"ent":[{"tp":"LN","data":{"url":"https://www.youtube.com/watch?v=dQw4w9WgXcQ"}}]}`,
		`{"txt":"https://api.tin","fmt":[{"len":15}],"ent":[{"tp":"LN","data":{"url":"https://api.tin-im.co/"}}]}`,
		`{"txt":"Url one, two","fmt":[{"at":4,"len":3},{"at":9,"len":3}],"ent":[{"tp":"LN","data":{"url":"http://tin-im.co"}}]}`,
		`{"txt":" ","fmt":[{"len":1}],"ent":[{"tp":"IM","data":{"height":213,"mime":"image/jpeg","name":"roses.jpg","width":638}}]}`,
		`{"txt":"This text has s","fmt":[{"tp":"EM","at":5,"len":8}]}`,
		`{"txt":"This text is fo","fmt":[{"tp":"ST","at":5,"len":4},{"tp":"EM","at":13,"len":2}]}`,
		`{"txt":"мультибайтовый ","fmt":[{"tp":"ST","len":14}]}`,
		`{"txt":"This is a test"}`,
		`{"txt":"Hello 😀, o😀k ht","fmt":[{"tp":"ST","at":9,"len":3},{"at":13,"len":2}],"ent":[{"tp":"LN","data":{"url":"https://google.com"}}]}`,
		`{"txt":"Hi 🏴󠁧󠁢󠁳󠁣󠁴󠁿🏴󠁧󠁢󠁳󠁣󠁴󠁿🏴󠁧󠁢󠁳󠁣󠁴󠁿🏴󠁧󠁢󠁳󠁣󠁴󠁿 🏴󠁧󠁢󠁳󠁣󠁴󠁿🏴󠁧󠁢󠁳󠁣󠁴󠁿🏴󠁧󠁢󠁳󠁣󠁴󠁿🏴󠁧󠁢󠁳󠁣󠁴󠁿 🏴󠁧󠁢󠁳󠁣󠁴󠁿🏴󠁧󠁢󠁳󠁣󠁴󠁿","fmt":[{"tp":"ST","at":3,"len":4},{"tp":"ST","at":8,"len":4}]}`,
	}
	for i := range validInputs {
		var val any
		if err := json.Unmarshal([]byte(validInputs[i]), &val); err != nil {
			t.Errorf("Failed to parse input %d '%s': %s", i, validInputs[i], err)
		}
		res, err := Preview(val, 15)
		if err != nil {
			t.Errorf("%d failed with error: %s", i, err)
		} else if res != expect[i] {
			t.Errorf("%d output '%s' does not match '%s'", i, res, expect[i])
		}
	}

	// 只有部分无效输入会导致这些测试失败。
	testsToFail := []int{3, 4, 5, 6}
	for _, i := range testsToFail {
		var val any
		if err := json.Unmarshal([]byte(invalidInputs[i]), &val); err != nil {
			// 不要将其视为错误：我们不是在测试 json.Unmarshal 的有效性。
			t.Logf("Failed to parse input %d '%s': %s", i, invalidInputs[i], err)
		}
		res, err := Preview(val, 15)
		if err == nil {
			t.Errorf("invalid input %d did not cause an error '%s'", i, res)
		}
	}
}

// TestAnalyzeMessageKindsAndAttachments 验证 Analyze Message Kinds And Attachments 相关行为。
func TestAnalyzeMessageKindsAndAttachments(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		kind        string
		attachments []string
	}{
		{name: "text", input: `"hello"`, kind: "text"},
		{name: "rich text", input: `{"txt":"hello","fmt":[{"at":0,"len":5,"tp":"ST"}]}`, kind: "drafty"},
		{
			name: "image",
			input: `{"txt":" ","fmt":[{"at":0,"len":1,"key":0}],
				"ent":[{"tp":"IM","data":{"mime":"image/jpeg","ref":"/v0/file/s/a.jpg","width":10,"height":10}}]}`,
			kind: "image", attachments: []string{"/v0/file/s/a.jpg"},
		},
		{
			name: "voice",
			input: `{"fmt":[{"at":-1,"key":0}],
				"ent":[{"tp":"AU","data":{"mime":"audio/ogg","ref":"/v0/file/s/a.ogg","voice":true,"duration":1200}}]}`,
			kind: "voice", attachments: []string{"/v0/file/s/a.ogg"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var input any
			if err := json.Unmarshal([]byte(tc.input), &input); err != nil {
				t.Fatal(err)
			}
			info, err := Analyze(input)
			if err != nil {
				t.Fatal(err)
			}
			if info.Kind != tc.kind {
				t.Fatalf("kind: want %q, got %q", tc.kind, info.Kind)
			}
			if len(info.Attachments) != len(tc.attachments) {
				t.Fatalf("attachments: want %#v, got %#v", tc.attachments, info.Attachments)
			}
			for i := range tc.attachments {
				if info.Attachments[i] != tc.attachments[i] {
					t.Fatalf("attachments: want %#v, got %#v", tc.attachments, info.Attachments)
				}
			}
		})
	}
}

// TestAnalyzeRejectsInvalidMedia 验证 Analyze Rejects Invalid Media 相关行为。
func TestAnalyzeRejectsInvalidMedia(t *testing.T) {
	inputs := []string{
		`""`,
		`{"txt":"","ent":[]}`,
		`{"fmt":[{"at":-1,"key":0}],"ent":[{"tp":"IM","data":{"mime":"video/mp4","ref":"/bad"}}]}`,
		`{"txt":"x","ent":[{"tp":"LN","data":{"url":"https://example.com"}}]}`,
	}
	for _, encoded := range inputs {
		var input any
		_ = json.Unmarshal([]byte(encoded), &input)
		if _, err := Analyze(input); err == nil {
			t.Fatalf("invalid content was accepted: %s", encoded)
		}
	}
}
