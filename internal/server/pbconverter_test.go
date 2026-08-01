// Package server 实现即时通信服务端的协议、路由和业务逻辑。
package server

import (
	"reflect"
	"testing"
	"time"

	"chat/server/store/types"
)

func TestPbContactsAndAssetsRoundTrip(t *testing.T) {
	now := time.Now().UTC().Round(time.Millisecond)
	client := &ClientComMessage{Set: &MsgClientSet{
		Id:    "catalog-1",
		Topic: "me",
		MsgSetQuery: MsgSetQuery{
			Contact: &types.ContactMutation{
				Op: "upsert_contact",
				Contact: &types.AddressBookContact{
					User: "usrPeer", Alias: "Peer", Status: types.ContactAccepted,
				},
			},
			Asset: &types.AssetMutation{
				Op:   "upsert_pack",
				Pack: &types.AssetPack{Id: "pack", Name: "Pack", Published: true},
			},
		},
	}}
	gotClient := pbCliDeserialize(pbCliSerialize(client))
	if gotClient.Set == nil || gotClient.Set.Contact == nil ||
		gotClient.Set.Contact.Contact == nil || gotClient.Set.Contact.Contact.Alias != "Peer" ||
		gotClient.Set.Asset == nil || gotClient.Set.Asset.Pack == nil ||
		gotClient.Set.Asset.Pack.Id != "pack" {
		t.Fatalf("contact/asset client round trip mismatch: %#v", gotClient.Set)
	}

	server := &ServerComMessage{Meta: &MsgServerMeta{
		Contacts: &types.ContactSnapshot{
			Version: 4,
			Contacts: []types.AddressBookContact{{
				User: "usrPeer", Status: types.ContactAccepted, UpdatedAt: now,
			}},
		},
		Assets: &types.AssetCatalog{
			Version: 2,
			Assets: []types.MediaAsset{{
				Id: "wave", PackId: "pack", Kind: "sticker", URL: "/asset", UpdatedAt: now,
				Alt: "👋", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Size: 42, Revision: 3,
				Variants: []types.AssetVariant{{
					Name: "webp", URL: "/asset.webp", MimeType: "image/webp",
					Width: 64, Height: 64, Size: 21,
					SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				}},
			}},
		},
		Readers: &MsgReadParticipants{
			SeqId: 42,
			Users: []MsgReadParticipant{
				{User: "usrPeer", Date: &now},
				{User: "usrLegacy"},
			},
		},
		Previews: []*MsgServerData{{
			Topic: "grpPreview", From: "usrPeer", SeqId: 9, Timestamp: now, Content: "preview",
		}},
	}}
	gotServer := pbServDeserialize(pbServSerialize(server))
	if gotServer.Meta == nil || gotServer.Meta.Contacts == nil ||
		gotServer.Meta.Contacts.Version != 4 || len(gotServer.Meta.Contacts.Contacts) != 1 ||
		gotServer.Meta.Assets == nil || gotServer.Meta.Assets.Version != 2 ||
		len(gotServer.Meta.Assets.Assets) != 1 || gotServer.Meta.Assets.Assets[0].Kind != "sticker" ||
		gotServer.Meta.Assets.Assets[0].Alt != "👋" ||
		len(gotServer.Meta.Assets.Assets[0].Variants) != 1 ||
		gotServer.Meta.Assets.Assets[0].Variants[0].Name != "webp" ||
		gotServer.Meta.Readers == nil || gotServer.Meta.Readers.SeqId != 42 ||
		len(gotServer.Meta.Readers.Users) != 2 ||
		gotServer.Meta.Readers.Users[0].Date == nil ||
		!gotServer.Meta.Readers.Users[0].Date.Equal(now) ||
		gotServer.Meta.Readers.Users[1].Date != nil ||
		len(gotServer.Meta.Previews) != 1 || gotServer.Meta.Previews[0].Topic != "grpPreview" ||
		gotServer.Meta.Previews[0].SeqId != 9 || gotServer.Meta.Previews[0].Content != "preview" {
		t.Fatalf("contact/asset server round trip mismatch: %#v", gotServer.Meta)
	}
}

func TestPbInternalWorkspacePresenceRoundTrip(t *testing.T) {
	message := &ServerComMessage{Pres: &MsgServerPres{Topic: "me", What: "workspace"}}
	decoded := pbServDeserialize(pbServSerialize(message))
	if decoded.Pres == nil || decoded.Pres.Topic != "me" || decoded.Pres.What != "workspace" {
		t.Fatalf("workspace presence round trip mismatch: %#v", decoded.Pres)
	}
}

func TestPbResumeAndInviteRoundTrip(t *testing.T) {
	client := &ClientComMessage{Resume: &MsgClientResume{
		Id: "resume-1", Token: []byte("signed-token"),
		Topics: []MsgResumeTopic{
			{Topic: "me"},
			{Topic: "grpResume", SeqId: 41, DelId: 6, Active: true},
		},
	}}
	got := pbCliDeserialize(pbCliSerialize(client))
	if !reflect.DeepEqual(got.Resume, client.Resume) {
		t.Fatalf("resume protobuf round trip mismatch:\nwant: %#v\ngot:  %#v", client.Resume, got.Resume)
	}

	invite := &ClientComMessage{Sub: &MsgClientSub{
		Id: "sub-1", Topic: "grpResume", Invite: "invite-token",
	}}
	gotInvite := pbCliDeserialize(pbCliSerialize(invite))
	if gotInvite.Sub == nil || gotInvite.Sub.Invite != invite.Sub.Invite {
		t.Fatalf("subscription invite lost in protobuf round trip: %#v", gotInvite.Sub)
	}

	note := &ClientComMessage{Note: &MsgClientNote{
		Topic: "grpResume", What: "data", SeqId: 42,
	}}
	gotNote := pbCliDeserialize(pbCliSerialize(note))
	if gotNote.Note == nil || gotNote.Note.What != "data" {
		t.Fatalf("data note lost in protobuf round trip: %#v", gotNote.Note)
	}
}

func TestPbControlAndMetaTimestampsRoundTrip(t *testing.T) {
	now := time.Now().UTC().Round(time.Millisecond)
	control := pbServDeserialize(pbServSerialize(&ServerComMessage{
		Ctrl: &MsgServerCtrl{Id: "ctrl-1", Code: 200, Timestamp: now},
	}))
	if control.Ctrl == nil || !control.Ctrl.Timestamp.Equal(now) {
		t.Fatalf("control timestamp lost in protobuf round trip: %#v", control.Ctrl)
	}
	meta := pbServDeserialize(pbServSerialize(&ServerComMessage{
		Meta: &MsgServerMeta{Id: "meta-1", Topic: "me", Timestamp: &now},
	}))
	if meta.Meta == nil || meta.Meta.Timestamp == nil || !meta.Meta.Timestamp.Equal(now) {
		t.Fatalf("meta timestamp lost in protobuf round trip: %#v", meta.Meta)
	}
}

// TestPbGetQueryRoundTripIncludesSyncFields 验证 Pb Get Query Round Trip Includes Sync Fields 相关行为。
func TestPbGetQueryRoundTripIncludesSyncFields(t *testing.T) {
	in := &MsgGetQuery{
		What: "desc sub data del readers previews",
		Desc: &MsgGetOpts{User: "usrA", Topic: "grpA", Limit: 7},
		Sub:  &MsgGetOpts{User: "usrB", Topic: "grpB", Limit: 8, Cursor: "usrCursor"},
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
		Assets: &types.AssetQuery{
			PackId: "official", Kind: "sticker", Since: 9, Limit: 20,
			AssetIds: []string{"wave", "party"},
		},
		Readers:  &MsgGetReaders{SeqId: 42},
		Previews: &MsgPreviewQuery{Topics: []string{"usrPeer", "grpPreview"}},
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
		Next: "usrNextMember",
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
		gotServer.Meta.Desc.SubCnt != 99 || gotServer.Meta.Next != "usrNextMember" {
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
		Translation: &MsgTranslation{
			Status: "completed", SourceLanguage: "zh", TargetLanguage: "en",
			Provider: "azure-primary", Original: "你好",
		},
		Content: map[string]any{"text": "hello"},
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
		gotServer.Data.Translation == nil ||
		gotServer.Data.Translation.Provider != "azure-primary" ||
		gotServer.Data.Translation.Original != "你好" ||
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
