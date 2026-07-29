// Package server 实现即时通信服务端的协议、路由和业务逻辑。
package server

import (
	"reflect"
	"testing"
	"time"
)

// TestPbGetQueryRoundTripIncludesSyncFields 验证 Pb Get Query Round Trip Includes Sync Fields 相关行为。
func TestPbGetQueryRoundTripIncludesSyncFields(t *testing.T) {
	in := &MsgGetQuery{
		What: "desc sub data del",
		Desc: &MsgGetOpts{User: "usrA", Topic: "grpA", Limit: 7},
		Sub:  &MsgGetOpts{User: "usrB", Topic: "grpB", Limit: 8},
		Data: &MsgGetOpts{
			SinceId:  10,
			BeforeId: 30,
			Limit:    5,
			Forward:  true,
			IdRanges: []MsgRange{{LowId: 10, HiId: 15}},
		},
		Del: &MsgGetOpts{
			SinceId:  3,
			BeforeId: 20,
			Limit:    4,
			Forward:  true,
			IdRanges: []MsgRange{{LowId: 3, HiId: 6}},
		},
	}

	got := pbGetQueryDeserialize(pbGetQuerySerialize(in))
	if !reflect.DeepEqual(in, got) {
		t.Fatalf("gRPC get-query round trip mismatch:\nwant: %#v\ngot:  %#v", in, got)
	}
}

// TestPbSearchRoundTrip 验证搜索条件、Peer、消息和下一页游标在 gRPC 中完整保留。
func TestPbSearchRoundTrip(t *testing.T) {
	minDate := time.Now().UTC().Add(-time.Hour).Round(time.Millisecond)
	maxDate := minDate.Add(30 * time.Minute)
	query := &MsgGetQuery{
		What: "search",
		Search: &MsgSearchOpts{
			Query:   "版本",
			Scope:   "topic",
			From:    "usrSender",
			Kinds:   []string{"text", "file"},
			MinDate: &minDate,
			MaxDate: &maxDate,
			Cursor:  "next-page",
			Limit:   25,
		},
	}
	gotQuery := pbGetQueryDeserialize(pbGetQuerySerialize(query))
	if !reflect.DeepEqual(query, gotQuery) {
		t.Fatalf("gRPC search query round trip mismatch:\nwant: %#v\ngot:  %#v", query, gotQuery)
	}

	server := &ServerComMessage{Meta: &MsgServerMeta{
		Search: &MsgSearchResult{
			Scope: "topic",
			Peers: []MsgTopicSub{{Topic: "grpResult", Public: map[string]any{"fn": "版本群"}}},
			Messages: []*MsgServerData{{
				Topic: "grpResult", From: "usrSender", SeqId: 12, Kind: "file",
				Timestamp: minDate, Content: "版本说明",
			}},
			Next: "next-page",
		},
	}}
	gotServer := pbServDeserialize(pbServSerialize(server))
	if gotServer.Meta == nil || gotServer.Meta.Search == nil ||
		gotServer.Meta.Search.Scope != "topic" || gotServer.Meta.Search.Next != "next-page" ||
		len(gotServer.Meta.Search.Peers) != 1 || gotServer.Meta.Search.Peers[0].Topic != "grpResult" ||
		len(gotServer.Meta.Search.Messages) != 1 || gotServer.Meta.Search.Messages[0].SeqId != 12 ||
		gotServer.Meta.Search.Messages[0].Content != "版本说明" {
		t.Fatalf("gRPC search result round trip mismatch: %#v", gotServer.Meta)
	}
}

// TestPbMemberRoleRoundTrip 验证角色在 gRPC 上下行协议中不会丢失。
func TestPbMemberRoleRoundTrip(t *testing.T) {
	client := &ClientComMessage{Set: &MsgClientSet{
		Id:    "role-1",
		Topic: "grpA",
		MsgSetQuery: MsgSetQuery{Sub: &MsgSetSub{
			User: "usrTarget",
			Role: "readonly",
		}},
	}}
	gotClient := pbCliDeserialize(pbCliSerialize(client))
	if gotClient.Set == nil || gotClient.Set.Sub == nil ||
		gotClient.Set.Sub.Role != "readonly" {
		t.Fatalf("member role lost in client gRPC round trip: %#v", gotClient.Set)
	}

	server := &ServerComMessage{Meta: &MsgServerMeta{
		Desc: &MsgTopicDesc{IsChan: true, SubCnt: 99},
		Sub: []MsgTopicSub{{
			User:   "usrTarget",
			SubCnt: 42,
			Acs: MsgAccessMode{
				Want: "JRP", Given: "JRP", Mode: "JRP", Role: "readonly",
			},
		}},
	}}
	gotServer := pbServDeserialize(pbServSerialize(server))
	if gotServer.Meta == nil || len(gotServer.Meta.Sub) != 1 ||
		gotServer.Meta.Sub[0].Acs.Role != "readonly" ||
		gotServer.Meta.Sub[0].SubCnt != 42 ||
		gotServer.Meta.Desc == nil || !gotServer.Meta.Desc.IsChan ||
		gotServer.Meta.Desc.SubCnt != 99 {
		t.Fatalf("member role lost in server gRPC round trip: %#v", gotServer.Meta)
	}
}

// TestPbClientAndServerDataRoundTripClientId 验证 Pb Client And Server Data Round Trip Client Id 相关行为。
func TestPbClientAndServerDataRoundTripClientId(t *testing.T) {
	scheduleAt := time.Now().UTC().Add(time.Hour).Round(time.Millisecond)
	client := &ClientComMessage{Pub: &MsgClientPub{
		Id:         "tx-1",
		Topic:      "grpA",
		ClientId:   "device-a:42",
		Kind:       "image",
		ReplyTo:    40,
		ReplaceSeq: 41,
		Forward:    &MsgMessageRef{Topic: "grpB", SeqId: 39},
		GroupId:    "album-a",
		ScheduleAt: &scheduleAt,
		Content:    map[string]any{"text": "hello"},
	}}
	gotClient := pbCliDeserialize(pbCliSerialize(client))
	if gotClient.Pub == nil || gotClient.Pub.Id != client.Pub.Id ||
		gotClient.Pub.Topic != client.Pub.Topic || gotClient.Pub.ClientId != client.Pub.ClientId ||
		gotClient.Pub.Kind != client.Pub.Kind || gotClient.Pub.ReplyTo != client.Pub.ReplyTo ||
		gotClient.Pub.ReplaceSeq != client.Pub.ReplaceSeq ||
		!reflect.DeepEqual(gotClient.Pub.Forward, client.Pub.Forward) ||
		gotClient.Pub.GroupId != client.Pub.GroupId ||
		!gotClient.Pub.ScheduleAt.Equal(scheduleAt) ||
		!reflect.DeepEqual(gotClient.Pub.Content, client.Pub.Content) {
		t.Fatalf("ClientPub gRPC round trip mismatch:\nwant: %#v\ngot:  %#v", client.Pub, gotClient.Pub)
	}

	editedAt := scheduleAt.Add(time.Minute)
	server := &ServerComMessage{Data: &MsgServerData{
		Topic:     "grpA",
		From:      "usrA",
		ClientId:  "device-a:42",
		SeqId:     42,
		Kind:      "image",
		ReplyTo:   40,
		GroupId:   "album-a",
		EditedAt:  &editedAt,
		Forwarded: &MsgForwardedMessage{Topic: "grpB", SeqId: 39, From: "usrB", Timestamp: scheduleAt},
		Reactions: []MsgReaction{{Reaction: "👍", Count: 2}},
		Content:   map[string]any{"text": "hello"},
	}}
	gotServer := pbServDeserialize(pbServSerialize(server))
	if gotServer.Data == nil || gotServer.Data.ClientId != server.Data.ClientId {
		t.Fatalf("client id lost in ServerData gRPC round trip: %#v", gotServer.Data)
	}
	if !reflect.DeepEqual(gotServer.Data.Content, server.Data.Content) {
		t.Fatalf("server content round trip mismatch: want %#v, got %#v",
			server.Data.Content, gotServer.Data.Content)
	}
	if gotServer.Data.Kind != server.Data.Kind || gotServer.Data.ReplyTo != server.Data.ReplyTo ||
		gotServer.Data.GroupId != server.Data.GroupId || gotServer.Data.Forwarded.SeqId != 39 ||
		len(gotServer.Data.Reactions) != 1 || gotServer.Data.Reactions[0].Count != 2 ||
		!gotServer.Data.EditedAt.Equal(editedAt) {
		t.Fatalf("server message metadata lost in gRPC round trip: %#v", gotServer.Data)
	}

	note := &ClientComMessage{Note: &MsgClientNote{
		Id: "read-ack-1", Topic: "grpA", What: "read", SeqId: 42,
	}}
	gotNote := pbCliDeserialize(pbCliSerialize(note))
	if gotNote.Note == nil || gotNote.Note.Id != note.Note.Id {
		t.Fatalf("request id lost in ClientNote gRPC round trip: %#v", gotNote.Note)
	}

	for _, what := range []string{"kp", "kpa", "kpv"} {
		typing := &ClientComMessage{Note: &MsgClientNote{Topic: "grpA", What: what}}
		gotTyping := pbCliDeserialize(pbCliSerialize(typing))
		if gotTyping.Note == nil || gotTyping.Note.What != what {
			t.Fatalf("typing state %q lost in gRPC round trip: %#v", what, gotTyping.Note)
		}
	}

	reaction := &ClientComMessage{Note: &MsgClientNote{
		Id: "react-1", Topic: "grpA", What: "react", SeqId: 42, Reaction: "👍", Remove: true,
	}}
	gotReaction := pbCliDeserialize(pbCliSerialize(reaction))
	if !reflect.DeepEqual(gotReaction.Note, reaction.Note) {
		t.Fatalf("reaction note gRPC round trip mismatch: %#v", gotReaction.Note)
	}
}
