package loadtest

import (
	"strings"
	"testing"
	"time"
)

func TestReadAccounts(t *testing.T) {
	t.Parallel()

	accounts, err := ReadAccounts(strings.NewReader(
		" username , password , token \n alice , secret , \n bob , , cached-token \n",
	))
	if err != nil {
		t.Fatalf("读取账号失败: %v", err)
	}
	if len(accounts) != 2 {
		t.Fatalf("账号数量=%d，期望=2", len(accounts))
	}
	if accounts[0].Username != "alice" || accounts[0].Password != "secret" {
		t.Fatalf("第一个账号内容错误: %#v", accounts[0])
	}
	if accounts[1].Username != "bob" || accounts[1].Token != "cached-token" {
		t.Fatalf("第二个账号内容错误: %#v", accounts[1])
	}
}

func TestReadAccountsRejectsInvalidRows(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		content string
	}{
		{name: "缺少密码列", content: "username\nalice\n"},
		{name: "用户名为空", content: "username,password\n,secret\n"},
		{name: "认证信息为空", content: "username,password\nalice,\n"},
		{name: "没有账号", content: "username,password\n"},
	}
	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ReadAccounts(strings.NewReader(testCase.content)); err == nil {
				t.Fatal("无效账号文件未返回错误")
			}
		})
	}
}

func TestPartitionHelpers(t *testing.T) {
	t.Parallel()

	accounts := []Account{
		{Username: "a"},
		{Username: "b"},
		{Username: "c"},
	}
	first := PartitionAccounts(accounts, 0, 2)
	second := PartitionAccounts(accounts, 1, 2)
	if len(first) != 2 || first[0].Username != "a" || first[1].Username != "c" {
		t.Fatalf("第一个账号分片错误: %#v", first)
	}
	if len(second) != 1 || second[0].Username != "b" {
		t.Fatalf("第二个账号分片错误: %#v", second)
	}
	if fallback := PartitionAccounts(accounts, 4, 5); len(fallback) != 1 {
		t.Fatalf("空分片没有复用账号: %#v", fallback)
	}

	total := 0
	for index := 0; index < 3; index++ {
		total += PartitionTotal(10, index, 3)
	}
	if total != 10 {
		t.Fatalf("连接数分片合计=%d，期望=10", total)
	}
}

func TestWorkloadConfigValidate(t *testing.T) {
	t.Parallel()

	config := validTestWorkload()
	if err := config.Validate(); err != nil {
		t.Fatalf("有效配置校验失败: %v", err)
	}
	config.Topic = ""
	if err := config.Validate(); err == nil {
		t.Fatal("热点主题为空时校验未失败")
	}

	config = validTestWorkload()
	config.WebSocketURL = "127.0.0.1:6060"
	if err := config.Validate(); err == nil {
		t.Fatal("没有协议的 WebSocket 地址未被拒绝")
	}

	config = validTestWorkload()
	config.Accounts[0].Password = ""
	if err := config.Validate(); err == nil {
		t.Fatal("没有认证信息的账号未被拒绝")
	}
}

func validTestWorkload() WorkloadConfig {
	return WorkloadConfig{
		RunID:           "run-test",
		WorkerID:        "worker-test",
		WebSocketURL:    "ws://127.0.0.1:6060/v0/channels",
		APIKey:          "test-key",
		ProtocolVersion: DefaultProtocolVersion,
		Scenario:        ScenarioHotTopic,
		Topic:           "grp-test",
		Sessions:        1,
		Duration:        100 * time.Millisecond,
		RequestTimeout:  time.Second,
		PublishCount:    1,
		MaxTopics:       1,
		StartAt:         time.Now().UTC(),
		Accounts: []Account{
			{Username: "alice", Password: "secret"},
		},
	}
}
