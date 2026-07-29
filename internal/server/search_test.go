// Package server 提供搜索协议、权限和分页行为的回归测试。
package server

import (
	"testing"
	"time"

	"chat/server/auth"
	"chat/server/store/types"
	"github.com/golang/mock/gomock"
)

// TestSearchCursorIsBoundToQuery 验证游标不能跨关键词或搜索范围复用。
func TestSearchCursorIsBoundToQuery(t *testing.T) {
	opts := &MsgSearchOpts{Query: "版本", Scope: types.SearchScopeTopic, Limit: 20}
	key := searchQueryKey("grpTest", opts)
	encoded := encodeSearchCursor(searchCursor{
		Version:   1,
		Scope:     types.SearchScopeTopic,
		Key:       key,
		BeforeSeq: 42,
	})
	cursor, err := decodeSearchCursor(encoded, types.SearchScopeTopic, key)
	if err != nil || cursor.BeforeSeq != 42 {
		t.Fatalf("decode valid cursor: cursor=%#v err=%v", cursor, err)
	}
	if _, err = decodeSearchCursor(encoded, types.SearchScopeTopic, searchQueryKey("grpOther", opts)); err == nil {
		t.Fatal("cursor reused for a different topic was accepted")
	}
}

// TestReplySearchTopicAppliesLimitAndReturnsCursor 验证会话内搜索执行 ACL、过滤参数和分页游标。
func TestReplySearchTopicAppliesLimitAndReturnsCursor(t *testing.T) {
	helper := TopicTestHelper{}
	helper.setUp(t, 1, types.TopicCatGrp, "grpTest", true)
	defer helper.tearDown()

	now := types.TimeNow()
	helper.mm.EXPECT().
		Search("grpTest", helper.uids[0], gomock.Any()).
		DoAndReturn(func(_ string, _ types.Uid, query *types.MessageSearchQuery) ([]types.Message, error) {
			if query.Query != "版本" || query.Limit != 3 || query.BeforeSeq != 0 ||
				len(query.Kinds) != 1 || query.Kinds[0] != "file" {
				t.Fatalf("unexpected search query: %#v", query)
			}
			return []types.Message{
				{ObjHeader: types.ObjHeader{CreatedAt: now}, SeqId: 8, Topic: "grpTest",
					From: helper.uids[0].String(), Head: types.KVMap{"x-kind": "file"}, Content: "版本一"},
				{ObjHeader: types.ObjHeader{CreatedAt: now.Add(-time.Minute)}, SeqId: 7, Topic: "grpTest",
					From: helper.uids[0].String(), Head: types.KVMap{"x-kind": "file"}, Content: "版本二"},
				{ObjHeader: types.ObjHeader{CreatedAt: now.Add(-2 * time.Minute)}, SeqId: 6, Topic: "grpTest",
					From: helper.uids[0].String(), Head: types.KVMap{"x-kind": "file"}, Content: "版本三"},
			}, nil
		})

	msg := &ClientComMessage{
		Id:       "search-1",
		Original: "grpTest",
		Get: &MsgClientGet{MsgGetQuery: MsgGetQuery{Search: &MsgSearchOpts{
			Query: "版本",
			Scope: types.SearchScopeTopic,
			Kinds: []string{"file"},
			Limit: 2,
		}}},
	}
	if err := helper.topic.replySearch(helper.sessions[0], helper.uids[0], false,
		auth.LevelAuth, msg); err != nil {
		t.Fatal(err)
	}
	helper.finish()

	if len(helper.results[0].messages) != 1 {
		t.Fatalf("expected one search response, got %#v", helper.results[0].messages)
	}
	response := helper.results[0].messages[0].(*ServerComMessage)
	if response.Meta == nil || response.Meta.Search == nil ||
		len(response.Meta.Search.Messages) != 2 || response.Meta.Search.Messages[0].SeqId != 8 ||
		response.Meta.Search.Messages[1].SeqId != 7 || response.Meta.Search.Next == "" {
		t.Fatalf("unexpected search response: %#v", response.Meta)
	}
	key := searchQueryKey("grpTest", msg.Get.Search)
	cursor, err := decodeSearchCursor(response.Meta.Search.Next, types.SearchScopeTopic, key)
	if err != nil || cursor.BeforeSeq != 7 {
		t.Fatalf("unexpected next cursor: cursor=%#v err=%v", cursor, err)
	}
}

// TestReplySearchTopicRequiresReadPermission 验证没有读取 ACL 的成员不能搜索历史消息。
func TestReplySearchTopicRequiresReadPermission(t *testing.T) {
	helper := TopicTestHelper{}
	helper.setUp(t, 1, types.TopicCatGrp, "grpTest", true)
	defer helper.tearDown()
	userData := helper.topic.perUser[helper.uids[0]]
	userData.modeGiven = types.ModeJoin
	userData.modeWant = types.ModeJoin
	helper.topic.perUser[helper.uids[0]] = userData

	msg := &ClientComMessage{
		Id:       "search-denied",
		Original: "grpTest",
		Get: &MsgClientGet{MsgGetQuery: MsgGetQuery{Search: &MsgSearchOpts{
			Query: "版本",
			Scope: types.SearchScopeTopic,
		}}},
	}
	if err := helper.topic.replySearch(helper.sessions[0], helper.uids[0], false,
		auth.LevelAuth, msg); err == nil {
		t.Fatal("search without read permission was accepted")
	}
	helper.finish()
	response := helper.results[0].messages[0].(*ServerComMessage)
	if response.Ctrl == nil || response.Ctrl.Code != 403 {
		t.Fatalf("expected permission denied response, got %#v", response)
	}
}
