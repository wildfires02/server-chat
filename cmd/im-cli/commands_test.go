// Package main 实现 IM 命令行客户端的回归测试。
package main

import (
	"testing"

	"chat/api/pbx"
)

// TestParseSetMemberRoleCommand 验证 CLI 可以构造成员角色更新请求。
func TestParseSetMemberRoleCommand(t *testing.T) {
	cmd, err := ParseCommandLine(
		"set --user=usrTarget --role=readonly grpTest", 1, "usrOwner", "grpDefault", "")
	if err != nil {
		t.Fatalf("parse set role: %v", err)
	}
	set := cmd.Msg.GetSet()
	if set == nil || set.GetTopic() != "grpTest" || set.GetQuery().GetSub().GetUserId() != "usrTarget" ||
		set.GetQuery().GetSub().GetRole() != "readonly" {
		t.Fatalf("unexpected set role command: %#v", set)
	}
}

// TestParseGetChannelSubscribersCommand 验证 CLI 可以请求频道离线读者列表。
func TestParseGetChannelSubscribersCommand(t *testing.T) {
	cmd, err := ParseCommandLine(
		"get --sub --sub_topic=chnTest --limit=50 grpTest", 2, "usrOwner", "grpDefault", "")
	if err != nil {
		t.Fatalf("parse get subscribers: %v", err)
	}
	get := cmd.Msg.GetGet()
	if get == nil || get.GetQuery().GetSub().GetTopic() != "chnTest" ||
		get.GetQuery().GetSub().GetLimit() != 50 {
		t.Fatalf("unexpected get subscribers command: %#v", get)
	}
}

// TestParseDeleteMemberCommand 验证 CLI 把移出群成员编码为 del.sub。
func TestParseDeleteMemberCommand(t *testing.T) {
	cmd, err := ParseCommandLine(
		"del --member=usrTarget grpTest", 3, "usrOwner", "grpDefault", "")
	if err != nil {
		t.Fatalf("parse delete member: %v", err)
	}
	del := cmd.Msg.GetDel()
	if del == nil || del.GetTopic() != "grpTest" || del.GetWhat() != pbx.ClientDel_SUB ||
		del.GetUserId() != "usrTarget" {
		t.Fatalf("unexpected delete member command: %#v", del)
	}
}

// TestParseSearchCommand 验证 CLI 可以构造带过滤条件和游标的消息搜索请求。
func TestParseSearchCommand(t *testing.T) {
	cmd, err := ParseCommandLine(
		"get --search=版本 --scope=topic --from=usrSender --kinds=text,file --cursor=next --limit=25 grpTest",
		4, "usrOwner", "grpDefault", "")
	if err != nil {
		t.Fatalf("parse search: %v", err)
	}
	get := cmd.Msg.GetGet()
	search := get.GetQuery().GetSearch()
	if get.GetTopic() != "grpTest" || search.GetQuery() != "版本" || search.GetScope() != "topic" ||
		search.GetFromUserId() != "usrSender" || search.GetCursor() != "next" || search.GetLimit() != 25 ||
		len(search.GetKinds()) != 2 || search.GetKinds()[0] != "text" || search.GetKinds()[1] != "file" {
		t.Fatalf("unexpected search command: %#v", get)
	}
}

// TestParseAwaitCommand 验证等待策略不会在递归解析服务端命令时丢失。
func TestParseAwaitCommand(t *testing.T) {
	cmd, err := ParseCommandLine(
		".await $result get --desc grpTest",
		5,
		"usrOwner",
		"grpDefault",
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Msg == nil || cmd.Msg.GetGet() == nil ||
		!cmd.IsSynchronous || cmd.FailOnError || cmd.VarName != "$result" {
		t.Fatalf("unexpected await command: %#v", cmd)
	}
}

// TestParseMustCommand 验证 must 会等待响应并在服务端错误时终止脚本。
func TestParseMustCommand(t *testing.T) {
	cmd, err := ParseCommandLine(
		".must pub grpTest hello",
		6,
		"usrOwner",
		"grpDefault",
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Msg == nil || cmd.Msg.GetPub() == nil ||
		!cmd.IsSynchronous || !cmd.FailOnError {
		t.Fatalf("unexpected must command: %#v", cmd)
	}
}
