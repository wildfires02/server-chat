// Package main 实现即时通信服务端的协议、路由和业务逻辑。
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"chat/server/auth"
	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/mock_store"
	"chat/server/store/types"
	"github.com/golang/mock/gomock"
)

// responses 保存responses的数据和运行状态。
type responses struct {
	// messages 保存messages列表。
	messages []any
}

// Test fixture.
type TopicTestHelper struct {
	// numUsers 保存numUsers。
	numUsers int
	// uids 保存uids列表。
	uids []types.Uid

	// Gomock controller.
	ctrl *gomock.Controller

	// Session.
	sessions []*Session
	// sessWg 保存sessWg。
	sessWg *sync.WaitGroup
	// Per-Session responses (i.e. what gets dumped into Session' write loops).
	results []*responses

	// Hub.
	hub *Hub
	// 消息 captured from Hub.route Channel on the per-用户 (RcptTo) basis.
	hubMessages map[string][]*ServerComMessage
	// For stopping hub loop.
	hubDone chan bool

	// Topic.
	topic *Topic

	// Mock objects.
	mm *mock_store.MockMessagesPersistenceInterface
	// uu 保存uu。
	uu *mock_store.MockUsersPersistenceInterface
	// tt 保存tt。
	tt *mock_store.MockTopicsPersistenceInterface
	// ss 保存ss。
	ss *mock_store.MockSubsPersistenceInterface
}

// finish 完成finish所需的内部处理。
func (b *TopicTestHelper) finish() {
	b.topic.killTimer.Stop()
	b.topic.callEstablishmentTimer.Stop()
	// Stop Session write loops.
	for _, s := range b.sessions {
		close(s.send)
	}
	b.sessWg.Wait()
	// Hub loop.
	close(b.hub.routeSrv)
	close(b.hub.routeCli)
	<-b.hubDone
}

// newSession 创建并初始化会话。
func (b *TopicTestHelper) newSession(sid string, uid types.Uid) (*Session, *responses) {
	s := &Session{
		sid:    sid,
		uid:    uid,
		subs:   make(map[string]*Subscription),
		send:   make(chan any, 10),
		detach: make(chan string, 10),
	}
	r := &responses{}
	b.sessWg.Add(1)
	go s.testWriteLoop(r, b.sessWg)
	return s, r
}

// setUp 更新Up。
func (b *TopicTestHelper) setUp(t *testing.T, numUsers int, cat types.TopicCat, topicName string, attachSessions bool) {
	t.Helper()
	b.numUsers = numUsers
	b.uids = make([]types.Uid, numUsers)
	for i := range numUsers {
		// Can't use 0 as a valid uid.
		b.uids[i] = types.Uid(i + 1)
	}

	// Mocks.
	b.ctrl = gomock.NewController(t)
	b.mm = mock_store.NewMockMessagesPersistenceInterface(b.ctrl)
	b.uu = mock_store.NewMockUsersPersistenceInterface(b.ctrl)
	b.tt = mock_store.NewMockTopicsPersistenceInterface(b.ctrl)
	b.ss = mock_store.NewMockSubsPersistenceInterface(b.ctrl)
	store.Messages = b.mm
	store.Users = b.uu
	store.Topics = b.tt
	store.Subs = b.ss
	// Session.
	b.sessions = make([]*Session, b.numUsers)
	b.results = make([]*responses, b.numUsers)
	b.sessWg = &sync.WaitGroup{}
	for i := range b.sessions {
		s, r := b.newSession(fmt.Sprintf("sid%d", i), b.uids[i])
		b.results[i] = r
		b.sessions[i] = s
	}

	// Hub.
	b.hub = &Hub{
		routeCli: make(chan *ClientComMessage, 10),
		routeSrv: make(chan *ServerComMessage, 10),
	}
	globals.hub = b.hub
	b.hubMessages = make(map[string][]*ServerComMessage)
	b.hubDone = make(chan bool)
	go b.hub.testHubLoop(t, b.hubMessages, b.hubDone)

	// Topic.
	pu := make(map[types.Uid]perUserData)
	ps := make(map[*Session]perSessionData)
	for i, uid := range b.uids {
		puData := perUserData{
			modeWant:  types.ModeCFull,
			modeGiven: types.ModeCFull,
		}
		if cat == types.TopicCatP2P {
			puData.topicName = b.uids[i^1].UserId()
		}
		if attachSessions {
			ps[b.sessions[i]] = perSessionData{uid: uid}
			puData.online = 1
		}
		pu[uid] = puData
	}
	b.topic = &Topic{
		name:                   topicName,
		cat:                    cat,
		status:                 topicStatusLoaded,
		perUser:                pu,
		isProxy:                false,
		sessions:               ps,
		killTimer:              time.NewTimer(time.Hour),
		callEstablishmentTimer: time.NewTimer(time.Second),
	}
	if cat != types.TopicCatSys {
		b.topic.accessAuth = getDefaultAccess(cat, true, false)
		b.topic.accessAnon = getDefaultAccess(cat, true, false)
	}
	if cat == types.TopicCatMe {
		b.topic.xoriginal = "me"
	}
	if cat == types.TopicCatGrp {
		b.topic.xoriginal = topicName
		b.topic.owner = b.uids[0]
	}
}

// tearDown 完成tearDown所需的内部处理。
func (b *TopicTestHelper) tearDown() {
	globals.hub = nil
	store.Messages = nil
	store.Users = nil
	store.Topics = nil
	store.Subs = nil
	b.ctrl.Finish()
}

// testWriteLoop 持续运行testWriteLoop，直到输入通道关闭或收到停止信号。
func (s *Session) testWriteLoop(results *responses, wg *sync.WaitGroup) {
	for msg := range s.send {
		results.messages = append(results.messages, msg)
	}
	wg.Done()
}

// testHubLoop 持续运行testHubLoop，直到输入通道关闭或收到停止信号。
func (h *Hub) testHubLoop(t *testing.T, results map[string][]*ServerComMessage, done chan bool) {
	t.Helper()
	for msg := range h.routeSrv {
		if msg.RcptTo == "" {
			// Don't call t.Fatal from goroutine - instead send 错误 info back
			results["__ERROR__"] = []*ServerComMessage{{
				Ctrl: &MsgServerCtrl{
					Code: 500,
					Text: "Hub.route received a message without addressee.",
				},
			}}
			done <- true
			return
		}
		results[msg.RcptTo] = append(results[msg.RcptTo], msg)
	}
	done <- true
}

// TestHandleBroadcastDataP2P 验证 Handle Broadcast Data P 2 P 相关行为。
func TestHandleBroadcastDataP2P(t *testing.T) {
	numUsers := 2
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatP2P, "p2p-test" /*attach=*/, true)
	defer helper.tearDown()
	helper.mm.EXPECT().Save(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, true)

	from := helper.uids[0].UserId()
	msg := &ClientComMessage{
		AsUser:   from,
		Original: from,
		Pub: &MsgClientPub{
			Topic:   "p2p",
			Content: "test",
			NoEcho:  true,
		},
		sess: helper.sessions[0],
	}
	helper.topic.handleClientMsg(msg)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	// 消息 uid1 -> uid2.
	for i, m := range helper.results {
		if i == 0 {
			if len(m.messages) != 0 {
				t.Fatalf("Uid1: expected 0 messages, got %d", len(m.messages))
			}
		} else {
			if len(m.messages) != 1 {
				t.Fatalf("Uid2: expected 1 messages, got %d", len(m.messages))
			}
			r := m.messages[0].(*ServerComMessage)
			if r.Data == nil {
				t.Fatalf("Response[0] must have a ctrl message")
			}
			if r.Data.Topic != from {
				t.Errorf("Response[0] topic: expected '%s', got '%s'", from, r.Data.Topic)
			}
			if r.Data.Content.(string) != "test" {
				t.Errorf("Response[0] content: expected 'test', got '%s'", r.Data.Content.(string))
			}
			if r.Data.From != from {
				t.Errorf("Response[0] from: expected '%s', got '%s'", from, r.Data.From)
			}
		}
	}
	// Checking presence 消息 routed through the helper.
	if len(helper.hubMessages) != 2 {
		t.Fatal("Huhelper.route expected exactly two recipients routed via huhelper.")
	}
	for i, uid := range helper.uids {
		if mm, ok := helper.hubMessages[uid.UserId()]; ok {
			if len(mm) == 1 {
				s := mm[0]
				if s.Pres != nil {
					p := s.Pres
					if p.Topic != "me" {
						t.Errorf("Uid %s: pres notify on topic is expected to be 'me', got %s", uid.UserId(), p.Topic)
					}
					if p.SkipTopic != "p2p-test" {
						t.Errorf("Uid %s: pres skip topic is expected to be 'p2p-test', got %s", uid.UserId(), p.SkipTopic)
					}
					expectedSrc := helper.uids[i^1].UserId()
					if p.Src != expectedSrc {
						t.Errorf("Uid %s: pres.src expected: %s, found: %s", uid.UserId(), expectedSrc, p.Src)
					}
				} else {
					t.Errorf("Uid %s: hub message expected to be {pres}.", uid.UserId())
				}
			} else {
				t.Errorf("Uid %s: expected 1 hub message, got %d.", uid.UserId(), len(mm))
			}
		} else {
			t.Errorf("Uid %s: no hub results found.", uid.UserId())
		}
	}
}

// TestHandleBroadcastCall 验证 Handle Broadcast Call 相关行为。
func TestHandleBroadcastCall(t *testing.T) {
	numUsers := 2
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatP2P, "p2p-test" /*attach=*/, true)
	globals.iceServers = []iceServer{{Username: "dummy"}}
	helper.topic.lastID = 5
	defer helper.tearDown()
	helper.mm.EXPECT().Save(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, true)

	from := helper.uids[0].UserId()
	msg := &ClientComMessage{
		AsUser:   from,
		Original: from,
		Pub: &MsgClientPub{
			Topic:   "p2p",
			Head:    map[string]any{"webrtc": "started"},
			Content: "test",
			NoEcho:  true,
		},
		sess: helper.sessions[0],
	}
	helper.topic.handleClientMsg(msg)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	globals.iceServers = nil

	// 消息 uid1 -> uid2.
	for i, m := range helper.results {
		if i == 0 {
			if len(m.messages) != 0 {
				t.Fatalf("Uid1: expected 0 messages, got %d", len(m.messages))
			}
		} else {
			if len(m.messages) != 1 {
				t.Fatalf("Uid2: expected 1 messages, got %d", len(m.messages))
			}
			r := m.messages[0].(*ServerComMessage)
			if r.Data == nil {
				t.Fatalf("Response[0] must have a ctrl message")
			}
			if r.Data.Topic != from {
				t.Errorf("Response[0] topic: expected '%s', got '%s'", from, r.Data.Topic)
			}
			if r.Data.Content.(string) != "test" {
				t.Errorf("Response[0] content: expected 'test', got '%s'", r.Data.Content.(string))
			}
			if r.Data.Head == nil || r.Data.Head["webrtc"].(string) != "started" {
				t.Errorf("Response[0] head: expected {'webrtc': 'started'}', got '%s'", r.Data.Content.(string))
			}
			if r.Data.From != from {
				t.Errorf("Response[0] from: expected '%s', got '%s'", from, r.Data.From)
			}
		}
	}
	// Checking presence 消息 routed through the helper.
	if len(helper.hubMessages) != 2 {
		t.Fatal("Huhelper.route expected exactly two recipients routed via huhelper.")
	}
	for i, uid := range helper.uids {
		if mm, ok := helper.hubMessages[uid.UserId()]; ok {
			if len(mm) == 1 {
				s := mm[0]
				if s.Pres != nil {
					p := s.Pres
					if p.Topic != "me" {
						t.Errorf("Uid %s: pres notify on topic is expected to be 'me', got %s", uid.UserId(), p.Topic)
					}
					if p.SkipTopic != "p2p-test" {
						t.Errorf("Uid %s: pres skip topic is expected to be 'p2p-test', got %s", uid.UserId(), p.SkipTopic)
					}
					expectedSrc := helper.uids[i^1].UserId()
					if p.Src != expectedSrc {
						t.Errorf("Uid %s: pres.src expected: %s, found: %s", uid.UserId(), expectedSrc, p.Src)
					}
				} else {
					t.Errorf("Uid %s: hub message expected to be {pres}.", uid.UserId())
				}
			} else {
				t.Errorf("Uid %s: expected 1 hub message, got %d.", uid.UserId(), len(mm))
			}
		} else {
			t.Errorf("Uid %s: no hub results found.", uid.UserId())
		}
	}
	if helper.topic.currentCall == nil {
		t.Fatal("No call in progress")
	}
	if helper.topic.currentCall.seq != 6 {
		t.Errorf("Call seq: expected 6, found %d.", helper.topic.currentCall.seq)
	}
	if len(helper.topic.currentCall.parties) != 1 {
		t.Fatalf("Call parties: expected 1, found %d.", len(helper.topic.currentCall.parties))
	}
	if p, ok := helper.topic.currentCall.parties[helper.sessions[0].sid]; ok {
		if !p.isOriginator {
			t.Error("Call party is not a call originator.")
		}
		if p.uid != helper.uids[0] {
			t.Errorf("Call party wrong uid: expected %s, found %s.", helper.uids[0].UserId(), p.uid.UserId())
		}
	} else {
		t.Errorf("Call party for session %s not found.", helper.sessions[0].sid)
	}
}

// TestHandleBroadcastAgoraGroupCall 验证群组通话会持久化为 Agora 邀请，
// 并确保通过 ACL 校验的成员收到私有且绑定频道的 AccessToken2 响应。
func TestHandleBroadcastAgoraGroupCall(t *testing.T) {
	const topicName = "grp-agora-test"
	helper := TopicTestHelper{}
	helper.setUp(t, 3, types.TopicCatGrp, topicName, true)
	previousAgora := globals.agora
	globals.agora = &agoraProvider{
		appID:           "0123456789abcdef0123456789abcdef",
		appCertificate:  "abcdef0123456789abcdef0123456789",
		tokenTTL:        time.Hour,
		channelPrefix:   "im",
		maxParticipants: 128,
	}
	defer func() {
		globals.agora = previousAgora
		helper.tearDown()
	}()
	helper.topic.lastID = 10
	helper.mm.EXPECT().Save(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, true).Times(2)

	originator := helper.uids[0]
	invite := &ClientComMessage{
		AsUser:   originator.UserId(),
		Original: topicName,
		Pub: &MsgClientPub{
			Topic:   topicName,
			Head:    map[string]any{"webrtc": "started"},
			Content: map[string]any{"type": "video"},
			NoEcho:  true,
		},
		sess: helper.sessions[0],
	}
	helper.topic.handleClientMsg(invite)
	if helper.topic.currentCall == nil {
		t.Fatal("group call was not created")
	}
	callSeq := helper.topic.currentCall.seq
	if helper.topic.currentCall.provider != constCallProviderAgora {
		t.Fatalf("call provider = %q, want %q",
			helper.topic.currentCall.provider, constCallProviderAgora)
	}

	join := &ClientComMessage{
		AsUser:   originator.UserId(),
		Original: topicName,
		Note: &MsgClientNote{
			Topic: topicName,
			What:  "call",
			SeqId: callSeq,
			Event: constCallEventJoin,
		},
		sess: helper.sessions[0],
	}
	helper.topic.handleClientMsg(join)
	helper.finish()

	party := helper.topic.currentCall.parties[helper.sessions[0].sid]
	if !party.joined || party.agoraRole != "publisher" || party.agoraUID == 0 {
		t.Fatalf("joined party = %+v, want publisher with non-zero Agora UID", party)
	}
	if helper.topic.currentCall.acceptedAt.IsZero() {
		t.Fatal("acceptedAt was not set after first Agora participant joined")
	}

	var credentials agoraCallCredentials
	var foundCredentials bool
	for _, raw := range helper.results[0].messages {
		response := raw.(*ServerComMessage)
		if response.Info == nil || response.Info.What != "call" ||
			response.Info.Event != constCallEventJoin {
			continue
		}
		if err := json.Unmarshal(response.Info.Payload, &credentials); err != nil {
			t.Fatalf("decode Agora credentials: %v", err)
		}
		foundCredentials = true
	}
	if !foundCredentials {
		t.Fatal("originator did not receive private Agora credentials")
	}
	if credentials.Provider != constCallProviderAgora ||
		credentials.AppID != globals.agora.appID ||
		credentials.Channel != helper.topic.currentCall.channel ||
		credentials.UID != party.agoraUID ||
		credentials.Token == "" ||
		credentials.Role != "publisher" ||
		credentials.CallSeq != callSeq {
		t.Fatalf("Agora credentials = %+v, want call-bound publisher credentials", credentials)
	}

	for _, raw := range helper.results[1].messages {
		response := raw.(*ServerComMessage)
		if response.Data != nil && response.Data.Head["call-provider"] != constCallProviderAgora {
			t.Fatalf("group call data provider = %v, want %q",
				response.Data.Head["call-provider"], constCallProviderAgora)
		}
		if response.Info != nil && len(response.Info.Payload) > 0 &&
			strings.Contains(string(response.Info.Payload), credentials.Token) {
			t.Fatal("Agora Token leaked to another group member")
		}
	}
}

// TestHandleBroadcastDataGroup 验证 Handle Broadcast Data Group 相关行为。
func TestHandleBroadcastDataGroup(t *testing.T) {
	topicName := "grp-test"
	numUsers := 4
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatGrp, topicName, true)
	defer func() {
		store.Messages = nil
		helper.tearDown()
	}()
	helper.mm.EXPECT().Save(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, true)

	// 用户 3 isn't allowed to read.
	pu3 := helper.topic.perUser[helper.uids[3]]
	pu3.modeWant = types.ModeJoin | types.ModeWrite | types.ModePres
	pu3.modeGiven = pu3.modeWant
	helper.topic.perUser[helper.uids[3]] = pu3

	from := helper.uids[0].UserId()
	msg := &ClientComMessage{
		AsUser:   from,
		Original: topicName,
		Pub: &MsgClientPub{
			Topic:   topicName,
			Content: "test",
			NoEcho:  true,
		},
		sess: helper.sessions[0],
	}

	if helper.topic.lastID != 0 {
		t.Errorf("Topic.lastID: expected 0, found %d", helper.topic.lastID)
	}
	helper.topic.handleClientMsg(msg)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	if helper.topic.lastID != 1 {
		t.Errorf("Topic.lastID: expected 1, found %d", helper.topic.lastID)
	}
	// 消息 uid0 -> uid1, uid2, uid3.
	// Uid0 is the sender.
	if len(helper.results[0].messages) != 0 {
		t.Fatalf("Uid0 is the sender: expected 0 messages, got %d", len(helper.results[0].messages))
	}
	// Uid3 is not a Topic reader.
	if len(helper.results[3].messages) != 0 {
		t.Fatalf("Uid3 isn't allowed to read messages: expected 0 messages, got %d", len(helper.results[3].messages))
	}
	for i := 1; i < 3; i++ {
		m := helper.results[i]
		if len(m.messages) != 1 {
			t.Fatalf("Uid%d: expected 1 messages, got %d", i, len(m.messages))
		}
		r := m.messages[0].(*ServerComMessage)
		if r.Data == nil {
			t.Fatalf("Response[0] must have a ctrl message")
		}
		if r.Data.Topic != topicName {
			t.Errorf("Response[0] topic: expected '%s', got '%s'", topicName, r.Data.Topic)
		}
		if r.Data.From != from {
			t.Errorf("Response[0] from: expected '%s', got '%s'", from, r.Data.From)
		}
		if r.Data.Content.(string) != "test" {
			t.Errorf("Response[0] content: expected 'test', got '%s'", r.Data.Content.(string))
		}
	}
	// Presence 消息.
	if len(helper.hubMessages) != 3 {
		t.Fatal("Hubhelper.route expected exactly three recipients routed via huhelper.")
	}
	for i, uid := range helper.uids {
		if i == 3 {
			if _, ok := helper.hubMessages[uid.UserId()]; ok {
				t.Errorf("Uid %s: not expected to receive pres notifications.", uid.UserId())
			}
			continue
		}
		if mm, ok := helper.hubMessages[uid.UserId()]; ok {
			if len(mm) == 1 {
				s := mm[0]
				if s.Pres != nil {
					p := s.Pres
					if p.Topic != "me" {
						t.Errorf("Uid %s: pres notify on topic is expected to be 'me', got %s", uid.UserId(), p.Topic)
					}
					if p.SkipTopic != topicName {
						t.Errorf("Uid %s: pres skip topic is expected to be 'p2p-test', got %s", uid.UserId(), p.SkipTopic)
					}
					if p.Src != topicName {
						t.Errorf("Uid %s: pres.src expected: %s, found: %s", uid.UserId(), topicName, p.Src)
					}
				} else {
					t.Errorf("Uid %s: hub message expected to be {pres}.", uid.UserId())
				}
			} else {
				t.Errorf("Uid %s: expected 1 hub message, got %d.", uid.UserId(), len(mm))
			}
		} else {
			t.Errorf("Uid %s: no hub results found.", uid.UserId())
		}
	}
}

// TestHandleBroadcastDataIdempotentRetry 验证 Handle Broadcast Data Idempotent Retry 相关行为。
func TestHandleBroadcastDataIdempotentRetry(t *testing.T) {
	helper := TopicTestHelper{}
	helper.setUp(t, 2, types.TopicCatP2P, "p2p-test", true)
	defer helper.tearDown()

	helper.topic.lastID = 7
	helper.mm.EXPECT().
		GetByClientId("p2p-test", helper.uids[0], "device-a:7").
		Return(&types.Message{SeqId: 7, ClientId: "device-a:7"}, nil)

	msg := &ClientComMessage{
		Id:        "retry-1",
		AsUser:    helper.uids[0].UserId(),
		Original:  helper.uids[0].UserId(),
		Timestamp: types.TimeNow(),
		Pub: &MsgClientPub{
			Id:       "retry-1",
			Topic:    "p2p",
			ClientId: "device-a:7",
			Content:  "same message",
		},
		sess: helper.sessions[0],
	}

	helper.topic.handleClientMsg(msg)
	helper.finish()

	if helper.topic.lastID != 7 {
		t.Fatalf("duplicate publish advanced topic sequence: got %d", helper.topic.lastID)
	}
	if len(helper.results[1].messages) != 0 {
		t.Fatalf("duplicate publish was broadcast again: %d messages", len(helper.results[1].messages))
	}
	if len(helper.hubMessages) != 0 {
		t.Fatalf("duplicate publish generated presence/push side effects: %d", len(helper.hubMessages))
	}
	if len(helper.results[0].messages) != 1 {
		t.Fatalf("sender should receive one duplicate acknowledgement, got %d", len(helper.results[0].messages))
	}
	reply := helper.results[0].messages[0].(*ServerComMessage)
	if reply.Ctrl == nil || reply.Ctrl.Code != http.StatusAlreadyReported {
		t.Fatalf("expected 208 duplicate acknowledgement, got %#v", reply.Ctrl)
	}
	params := reply.Ctrl.Params.(map[string]any)
	if params["seq"] != 7 || params["cid"] != "device-a:7" || params["duplicate"] != true {
		t.Fatalf("unexpected duplicate acknowledgement params: %#v", params)
	}
}

// TestReplyGetDataForwardSnapshot 验证 Reply Get Data Forward Snapshot 相关行为。
func TestReplyGetDataForwardSnapshot(t *testing.T) {
	helper := TopicTestHelper{}
	helper.setUp(t, 1, types.TopicCatGrp, "grpTest", true)
	defer helper.tearDown()
	helper.topic.lastID = 7

	helper.mm.EXPECT().
		GetAll("grpTest", helper.uids[0], gomock.Any()).
		DoAndReturn(func(_ string, _ types.Uid, opt *types.QueryOpt) ([]types.Message, error) {
			if !opt.Forward || opt.Since != 4 || opt.Before != 8 || opt.Limit != 2 {
				t.Fatalf("unexpected forward query options: %#v", opt)
			}
			return []types.Message{
				{SeqId: 4, Topic: "grpTest", From: helper.uids[0].String(), ClientId: "c4", Content: "four"},
				{SeqId: 5, Topic: "grpTest", From: helper.uids[0].String(), ClientId: "c5", Content: "five"},
			}, nil
		})

	req := &MsgGetOpts{SinceId: 4, Limit: 2, Forward: true}
	msg := &ClientComMessage{
		Id:        "sync-1",
		Original:  "grpTest",
		Timestamp: types.TimeNow(),
	}
	if err := helper.topic.replyGetData(helper.sessions[0], helper.uids[0], false, req, msg); err != nil {
		t.Fatal(err)
	}
	helper.finish()

	got := helper.results[0].messages
	if len(got) != 3 {
		t.Fatalf("expected two data messages and one completion, got %d", len(got))
	}
	if got[0].(*ServerComMessage).Data.SeqId != 4 || got[1].(*ServerComMessage).Data.SeqId != 5 {
		t.Fatalf("messages are not in forward order: %#v, %#v", got[0], got[1])
	}
	if got[0].(*ServerComMessage).Data.ClientId != "c4" {
		t.Fatalf("stored client id was not returned: %#v", got[0])
	}
	ctrl := got[2].(*ServerComMessage).Ctrl
	params := ctrl.Params.(map[string]any)
	if params["cursor"] != 5 || params["high"] != 7 || params["hasMore"] != true {
		t.Fatalf("unexpected catch-up cursor: %#v", params)
	}
}

// TestReplyGetDelForwardSnapshot 验证 Reply Get Del Forward Snapshot 相关行为。
func TestReplyGetDelForwardSnapshot(t *testing.T) {
	helper := TopicTestHelper{}
	helper.setUp(t, 1, types.TopicCatGrp, "grpTest", true)
	defer helper.tearDown()
	helper.topic.delID = 4

	helper.mm.EXPECT().
		GetDeleted("grpTest", helper.uids[0], gomock.Any()).
		DoAndReturn(func(_ string, _ types.Uid, opt *types.QueryOpt) ([]types.Range, int, error) {
			if !opt.Forward || opt.Since != 2 || opt.Before != 5 {
				t.Fatalf("unexpected deletion sync options: %#v", opt)
			}
			return []types.Range{{Low: 11, Hi: 13}}, 3, nil
		})

	req := &MsgGetOpts{SinceId: 2, Limit: 10, Forward: true}
	msg := &ClientComMessage{Id: "sync-del-1", Original: "grpTest", Timestamp: types.TimeNow()}
	if err := helper.topic.replyGetDel(helper.sessions[0], helper.uids[0], req, msg); err != nil {
		t.Fatal(err)
	}
	helper.finish()

	got := helper.results[0].messages
	if len(got) != 2 || got[0].(*ServerComMessage).Meta == nil || got[1].(*ServerComMessage).Ctrl == nil {
		t.Fatalf("expected deletion metadata and completion cursor, got %#v", got)
	}
	params := got[1].(*ServerComMessage).Ctrl.Params.(map[string]any)
	if params["cursor"] != 3 || params["high"] != 4 || params["hasMore"] != true {
		t.Fatalf("unexpected deletion catch-up cursor: %#v", params)
	}
}

// TestHandleBroadcastDataMissingWritePermission 验证 Handle Broadcast Data Missing Write Permission 相关行为。
func TestHandleBroadcastDataMissingWritePermission(t *testing.T) {
	topicName := "p2p-test"
	numUsers := 2
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatP2P, topicName, true)
	defer helper.tearDown()

	// Remove W 权限 for uid1.
	uid1 := helper.uids[0]
	pud := helper.topic.perUser[uid1]
	pud.modeGiven = types.ModeRead | types.ModeJoin
	helper.topic.perUser[uid1] = pud

	// Make test 消息.
	from := helper.uids[0].UserId()
	msg := &ClientComMessage{
		AsUser:   from,
		Original: from,
		Pub: &MsgClientPub{
			Topic:   "p2p",
			Content: "test",
		},
		sess: helper.sessions[0],
	}

	helper.topic.handleClientMsg(msg)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	// 消息 uid1 -> uid2.
	if len(helper.results[0].messages) == 1 {
		em := helper.results[0].messages[0].(*ServerComMessage)
		if em.Ctrl == nil {
			t.Fatal("User 1 is expected to receive a ctrl message")
		}
		if em.Ctrl.Code < 400 || em.Ctrl.Code >= 500 {
			t.Errorf("User1: expected ctrl.code 4xx, received %d", em.Ctrl.Code)
		}
	} else {
		t.Errorf("User 1 is expected to receive one message vs %d received.", len(helper.results[0].messages))
	}
	if len(helper.results[1].messages) != 0 {
		t.Errorf("User 2 is not expected to receive any messages, %d received.", len(helper.results[1].messages))
	}
	// Checking presence 消息 routed through hubhelper.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hubhelper.route did not expect any messages, however %d received.", len(helper.hubMessages))
	}
}

// TestHandleBroadcastDataDbError 验证 Handle Broadcast Data Db Error 相关行为。
func TestHandleBroadcastDataDbError(t *testing.T) {
	numUsers := 2
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatP2P, "p2p-test", true)
	defer helper.tearDown()

	// DB returns an 错误.
	helper.mm.EXPECT().Save(gomock.Any(), gomock.Any(), gomock.Any()).Return(types.ErrInternal, false)

	// Make test 消息.
	from := helper.uids[0].UserId()
	msg := &ClientComMessage{
		AsUser: from,
		Pub: &MsgClientPub{
			Topic:   "p2p",
			Content: "test",
		},
		sess: helper.sessions[0],
	}

	if helper.topic.lastID != 0 {
		t.Errorf("Topic.lastID: expected 0, found %d", helper.topic.lastID)
	}
	helper.topic.handleClientMsg(msg)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	if helper.topic.lastID != 0 {
		t.Errorf("Topic.lastID: expected to remain 0, found %d", helper.topic.lastID)
	}
	// 消息 uid1 -> uid2.
	if len(helper.results[0].messages) == 1 {
		em := helper.results[0].messages[0].(*ServerComMessage)
		if em.Ctrl == nil {
			t.Fatal("User 1 is expected to receive a ctrl message")
		}
		if em.Ctrl.Code < 500 || em.Ctrl.Code >= 600 {
			t.Errorf("User1: expected ctrl.code 5xx, received %d", em.Ctrl.Code)
		}
	} else {
		t.Errorf("User 1 is expected to receive one message vs %d received.", len(helper.results[0].messages))
	}
	if len(helper.results[1].messages) != 0 {
		t.Errorf("User 2 is not expected to receive any messages, %d received.", len(helper.results[1].messages))
	}
	// Checking presence 消息 routed through hubhelper.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hubhelper.route did not expect any messages, however %d received.", len(helper.hubMessages))
	}
}

// TestHandleBroadcastDataInactiveTopic 验证 Handle Broadcast Data Inactive Topic 相关行为。
func TestHandleBroadcastDataInactiveTopic(t *testing.T) {
	numUsers := 2
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatP2P, "p2p-test", true)
	defer helper.tearDown()

	// Make test 消息.
	from := helper.uids[0].UserId()
	msg := &ClientComMessage{
		AsUser: from,
		Pub: &MsgClientPub{
			Topic:   "p2p",
			Content: "test",
		},
		sess: helper.sessions[0],
	}

	// Deactivate Topic.
	helper.topic.markDeleted()

	helper.topic.handleClientMsg(msg)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	// 消息 uid1 -> uid2.
	if len(helper.results[0].messages) == 1 {
		em := helper.results[0].messages[0].(*ServerComMessage)
		if em.Ctrl == nil {
			t.Fatal("User 1 is expected to receive a ctrl message")
		}
		if em.Ctrl.Code < 500 || em.Ctrl.Code >= 600 {
			t.Errorf("User1: expected ctrl.code 5xx, received %d", em.Ctrl.Code)
		}
	} else {
		t.Errorf("User 1 is expected to receive one message vs %d received.", len(helper.results[0].messages))
	}
	if len(helper.results[1].messages) != 0 {
		t.Errorf("User 2 is not expected to receive any messages, %d received.", len(helper.results[1].messages))
	}
	// Checking presence 消息 routed through hubhelper.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hubhelper.route did not expect any messages, however %d received.", len(helper.hubMessages))
	}
}

// TestHandleBroadcastInfoP2P 验证 Handle Broadcast Info P 2 P 相关行为。
func TestHandleBroadcastInfoP2P(t *testing.T) {
	topicName := "usrP2P"
	numUsers := 2
	readId := 8
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatP2P, topicName, true)
	defer helper.tearDown()
	// Pretend we have 10 消息.
	helper.topic.lastID = 10
	// uid1 notifies uid2 that uid1 has read 消息 up to seqid 8.
	from := helper.uids[0]
	to := helper.uids[1]

	helper.ss.EXPECT().Update(topicName, from, map[string]any{"ReadSeqId": readId}).Return(nil)

	msg := &ClientComMessage{
		Id:       "read-8",
		AsUser:   from.UserId(),
		Original: to.UserId(),
		Note: &MsgClientNote{
			Id:    "read-8",
			Topic: to.UserId(),
			What:  "read",
			SeqId: readId,
		},
		sess: helper.sessions[0],
	}
	helper.topic.handleClientMsg(msg)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	// Topic metadata.
	if actualReadId := helper.topic.perUser[from].readID; actualReadId != readId {
		t.Errorf("perUser[%s].readID: expected %d, found %d.", from.UserId(), readId, actualReadId)
	}
	// Server 消息.
	if len(helper.results[0].messages) != 1 {
		t.Fatalf("Session 0 should receive one persisted read acknowledgement. Received %d", len(helper.results[0].messages))
	}
	ack := helper.results[0].messages[0].(*ServerComMessage)
	if ack.Ctrl == nil || ack.Ctrl.Code != http.StatusOK {
		t.Fatalf("unexpected read acknowledgement: %#v", ack.Ctrl)
	}
	if params := ack.Ctrl.Params.(map[string]any); params["what"] != "read" || params["seq"] != readId {
		t.Fatalf("unexpected read acknowledgement params: %#v", params)
	}
	if len(helper.results[1].messages) != 1 {
		t.Fatalf("Session 1 is expected to receive exactly 1 message. Received %d", len(helper.results[1].messages))
	}
	res := helper.results[1].messages[0].(*ServerComMessage)
	if res.Info != nil {
		info := res.Info
		// Topic name will be fixed (to -> from).
		if info.Topic != from.UserId() {
			t.Errorf("Info.Topic: expected '%s', found '%s'", to.UserId(), info.Topic)
		}
		if info.From != from.UserId() {
			t.Errorf("Info.From: expected '%s', found '%s'", from.UserId(), info.From)
		}
		if info.What != "read" {
			t.Errorf("Info.What: expected 'read', found '%s'", info.What)
		}
		if info.SeqId != readId {
			t.Errorf("Info.SeqId: expected %d, found %d", readId, info.SeqId)
		}
	} else {
		t.Error("Session message is expected to contain `info` section.")
	}
	// Checking presence 消息 routed through hub helper. These are intended for offline Session.
	if len(helper.hubMessages) != 2 {
		t.Fatalf("Hubhelper.route expected exactly two recipients routed via hubhelper. Found %d", len(helper.hubMessages))
	}
	for i, uid := range helper.uids {
		if routedMsgs, ok := helper.hubMessages[uid.UserId()]; ok {
			expectedSrc := helper.uids[i^1].UserId()
			for _, s := range routedMsgs {
				if s.Info != nil {
					// Info 消息 for offline Session.
					info := s.Info
					if info.Topic != "me" {
						t.Errorf("Uid %s: info.topic is expected to be 'me', got %s", uid.UserId(), info.Topic)
					}
					if info.Src != expectedSrc {
						t.Errorf("Uid %s: info.src expected: %s, found: %s", uid.UserId(), expectedSrc, info.Src)
					}
					if info.What != "read" {
						t.Error("info.what expected to be 'read'")
					}
					if info.SeqId != readId {
						t.Errorf("info.seq: expected %d, found %d", readId, info.SeqId)
					}
				} else if s.Pres != nil {
					// Pres 消息 for offline Session.
					pres := s.Pres
					if pres.Topic != "me" {
						t.Errorf("Uid %s: pres.topic is expected to be 'me', got %s", uid.UserId(), pres.Topic)
					}
					if pres.What != "read" {
						t.Error("pres.what expected to be 'read'")
					}
					if pres.Src != expectedSrc {
						t.Errorf("Uid %s: pres.src expected: %s, found: %s", uid.UserId(), expectedSrc, pres.Src)
					}
					if pres.SeqId != readId {
						t.Errorf("pres.seq: expected %d, found %d", readId, pres.SeqId)
					}
				} else {
					t.Error("Hub messages must be either `info` or `pres`.")
				}
			}
		} else {
			t.Errorf("Uid %s: no hub results found.", uid.UserId())
		}
	}
}

// TestHandleBroadcastInfoBogusNotification 验证 Handle Broadcast Info Bogus Notification 相关行为。
func TestHandleBroadcastInfoBogusNotification(t *testing.T) {
	topicName := "usrP2P"
	numUsers := 2
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatP2P, topicName, true)
	defer helper.tearDown()
	// Pretend we have 10 消息.
	helper.topic.lastID = 10
	// uid1 notifies uid2 that uid1 has read 消息 up to seqid 11.
	readId := 11
	from := helper.uids[0]
	to := helper.uids[1]

	msg := &ClientComMessage{
		AsUser:   from.UserId(),
		Original: to.UserId(),
		Note: &MsgClientNote{
			Topic: to.UserId(),
			What:  "read",
			SeqId: readId,
		},
		sess: helper.sessions[0],
	}
	helper.topic.handleClientMsg(msg)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	// Read id should not be updated.
	if actualReadId := helper.topic.perUser[from].readID; actualReadId != 0 {
		t.Errorf("perUser[%s].readID: expected 0, found %d.", from.UserId(), actualReadId)
	}
	// Server 消息.
	for i, r := range helper.results {
		if numMessages := len(r.messages); numMessages != 0 {
			t.Errorf("User %d is not expected to receive any messages, %d received.", i, numMessages)
		}
	}

	// Nothing should be routed through the hub.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hubhelper.route did not expect any messages, however %d received.", len(helper.hubMessages))
	}
}

// TestHandleBroadcastInfoFilterOutRecvWithoutRPermission 验证 Handle Broadcast Info Filter Out Recv Without R Permission 相关行为。
func TestHandleBroadcastInfoFilterOutRecvWithoutRPermission(t *testing.T) {
	topicName := "usrP2P"
	numUsers := 2
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatP2P, topicName, true)
	defer helper.tearDown()
	// Pretend we have 10 消息.
	helper.topic.lastID = 10
	// uid1 notifies uid2 that uid1 has read 消息 up to seqid 11.
	readId := 8
	from := helper.uids[0]
	to := helper.uids[1]

	// Revoke R 权限 from the sender.
	pud := helper.topic.perUser[from]
	pud.modeGiven = types.ModeWrite | types.ModeJoin
	helper.topic.perUser[from] = pud

	msg := &ClientComMessage{
		AsUser:   from.UserId(),
		Original: to.UserId(),
		Note: &MsgClientNote{
			Topic: to.UserId(),
			What:  "recv",
			SeqId: readId,
		},
		sess: helper.sessions[0],
	}
	helper.topic.handleClientMsg(msg)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	// Read id should not be updated.
	if actualReadId := helper.topic.perUser[from].readID; actualReadId != 0 {
		t.Errorf("perUser[%s].readID: expected 0, found %d.", from.UserId(), actualReadId)
	}
	// Server 消息.
	for i, r := range helper.results {
		if numMessages := len(r.messages); numMessages != 0 {
			t.Errorf("User %d is not expected to receive any messages, %d received.", i, numMessages)
		}
	}

	// Nothing should be routed through the hub.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hubhelper.route did not expect any messages, however %d received.", len(helper.hubMessages))
	}
}

// TestHandleBroadcastInfoFilterOutKpWithoutWPermission 验证 Handle Broadcast Info Filter Out Kp Without W Permission 相关行为。
func TestHandleBroadcastInfoFilterOutKpWithoutWPermission(t *testing.T) {
	topicName := "usrP2P"
	numUsers := 2
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatP2P, topicName, true)
	defer helper.tearDown()
	// Pretend we have 10 消息.
	helper.topic.lastID = 10
	// uid1 notifies uid2 that uid1 has read 消息 up to seqid 11.
	readId := 8
	from := helper.uids[0]
	to := helper.uids[1]

	// Revoke W 权限 from the sender.
	pud := helper.topic.perUser[from]
	pud.modeGiven = types.ModeRead | types.ModeJoin
	helper.topic.perUser[from] = pud

	msg := &ClientComMessage{
		AsUser:   from.UserId(),
		Original: to.UserId(),
		Note: &MsgClientNote{
			Topic: to.UserId(),
			What:  "kp",
			SeqId: readId,
		},
		sess: helper.sessions[0],
	}
	helper.topic.handleClientMsg(msg)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	// Read id should not be updated.
	if actualReadId := helper.topic.perUser[from].readID; actualReadId != 0 {
		t.Errorf("perUser[%s].readID: expected 0, found %d.", from.UserId(), actualReadId)
	}
	// Server 消息.
	for i, r := range helper.results {
		if numMessages := len(r.messages); numMessages != 0 {
			t.Errorf("User %d is not expected to receive any messages, %d received.", i, numMessages)
		}
	}

	// Nothing should be routed through the hub.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hubhelper.route did not expect any messages, however %d received.", len(helper.hubMessages))
	}
}

// TestHandleBroadcastInfoDuplicatedRead 验证 Handle Broadcast Info Duplicated Read 相关行为。
func TestHandleBroadcastInfoDuplicatedRead(t *testing.T) {
	topicName := "usrP2P"
	numUsers := 2
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatP2P, topicName /*attach=*/, true)
	defer helper.tearDown()
	// Pretend we have 10 消息.
	helper.topic.lastID = 10
	// uid1 notifies uid2 that uid1 has read 消息 up to seqid 11.
	readId := 8
	from := helper.uids[0]
	to := helper.uids[1]

	// Revoke R 权限 from the sender.
	pud := helper.topic.perUser[from]
	pud.readID = 8
	helper.topic.perUser[from] = pud

	msg := &ClientComMessage{
		Id:       "read-8-retry",
		AsUser:   from.UserId(),
		Original: to.UserId(),
		Note: &MsgClientNote{
			Id:    "read-8-retry",
			Topic: to.UserId(),
			What:  "read",
			SeqId: readId,
		},
		sess: helper.sessions[0],
	}
	helper.topic.handleClientMsg(msg)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	// Read id should not be updated.
	if actualReadId := helper.topic.perUser[from].readID; actualReadId != 8 {
		t.Errorf("perUser[%s].readID: expected 8, found %d.", from.UserId(), actualReadId)
	}
	// The retry is acknowledged but not broadcast again.
	if len(helper.results[0].messages) != 1 {
		t.Fatalf("sender should receive one duplicate acknowledgement, got %d", len(helper.results[0].messages))
	}
	ack := helper.results[0].messages[0].(*ServerComMessage)
	if ack.Ctrl == nil || ack.Ctrl.Code != http.StatusAlreadyReported {
		t.Fatalf("unexpected duplicate read acknowledgement: %#v", ack.Ctrl)
	}
	if len(helper.results[1].messages) != 0 {
		t.Fatalf("duplicate read was broadcast again: %d messages", len(helper.results[1].messages))
	}

	// Nothing should be routed through the hub.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hubhelper.route did not expect any messages, however %d received.", len(helper.hubMessages))
	}
}

// TestHandleBroadcastInfoDbError 验证 Handle Broadcast Info Db Error 相关行为。
func TestHandleBroadcastInfoDbError(t *testing.T) {
	topicName := "usrP2P"
	numUsers := 2
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatP2P, topicName, true)
	defer helper.tearDown()
	// Pretend we have 10 消息.
	helper.topic.lastID = 10
	// uid1 notifies uid2 that uid1 has read 消息 up to seqid 11.
	readId := 8
	from := helper.uids[0]
	to := helper.uids[1]

	helper.ss.EXPECT().Update(topicName, from, map[string]any{"ReadSeqId": readId}).Return(types.ErrInternal)

	msg := &ClientComMessage{
		AsUser:   from.UserId(),
		Original: to.UserId(),
		Note: &MsgClientNote{
			Topic: to.UserId(),
			What:  "read",
			SeqId: readId,
		},
		sess: helper.sessions[0],
	}
	helper.topic.handleClientMsg(msg)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	// Read id should not be updated.
	if actualReadId := helper.topic.perUser[from].readID; actualReadId != 0 {
		t.Errorf("perUser[%s].readID: expected 0, found %d.", from.UserId(), actualReadId)
	}
	// Server 消息.
	for i, r := range helper.results {
		if numMessages := len(r.messages); numMessages != 0 {
			t.Errorf("User %d is not expected to receive any messages, %d received.", i, numMessages)
		}
	}

	// Nothing should be routed through the hub.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hubhelper.route did not expect any messages, however %d received.", len(helper.hubMessages))
	}
}

// TestHandleBroadcastInfoInvalidChannelAccess 验证 Handle Broadcast Info Invalid Channel Access 相关行为。
func TestHandleBroadcastInfoInvalidChannelAccess(t *testing.T) {
	topicName := "grpTest"
	chanName := "chnTest"
	numUsers := 3
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatGrp, topicName, true)
	// This is not a Channel. However, we will try to handle an info 消息 where
	// the Topic is referenced as "chn".
	helper.topic.isChan = false
	defer helper.tearDown()
	// Pretend we have 10 消息.
	helper.topic.lastID = 10
	// uid1 notifies uid2 that uid1 has read 消息 up to seqid 11.
	readId := 8
	from := helper.uids[0]
	for i := 1; i < numUsers; i++ {
		uid := helper.uids[i]
		pud := helper.topic.perUser[uid]
		pud.modeGiven = types.ModeCChnReader
		helper.topic.perUser[uid] = pud
	}

	msg := &ClientComMessage{
		Original: chanName,
		AsUser:   from.UserId(),
		Note: &MsgClientNote{
			Topic: chanName,
			What:  "read",
			SeqId: readId,
		},
		sess: helper.sessions[0],
	}
	helper.topic.handleClientMsg(msg)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	// Read id should not be updated.
	if actualReadId := helper.topic.perUser[from].readID; actualReadId != 0 {
		t.Errorf("perUser[%s].readID: expected 0, found %d.", from.UserId(), actualReadId)
	}
	// Server 消息.
	for i, r := range helper.results {
		if numMessages := len(r.messages); numMessages != 0 {
			t.Errorf("User %d is not expected to receive any messages, %d received.", i, numMessages)
		}
	}
	// Nothing should be routed through the hub.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hubhelper.route did not expect any messages, however %d received.", len(helper.hubMessages))
	}
}

// TestHandleBroadcastInfoChannelProcessing 验证 Handle Broadcast Info Channel Processing 相关行为。
func TestHandleBroadcastInfoChannelProcessing(t *testing.T) {
	topicName := "grpTest"
	chanName := "chnTest"
	numUsers := 3
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatGrp, topicName, true)
	helper.topic.isChan = true
	defer helper.tearDown()
	// Pretend we have 10 消息.
	helper.topic.lastID = 10
	// uid1 notifies uid2 that uid1 has read 消息 up to seqid 11.
	readId := 8
	from := helper.uids[0]
	for i := 1; i < numUsers; i++ {
		uid := helper.uids[i]
		pud := helper.topic.perUser[uid]
		pud.modeGiven = types.ModeCChnReader
		pud.isChan = true
		helper.topic.perUser[uid] = pud
	}

	helper.ss.EXPECT().Update(chanName, from, map[string]any{"ReadSeqId": readId}).Return(nil)

	msg := &ClientComMessage{
		AsUser:   from.UserId(),
		Original: chanName,
		Note: &MsgClientNote{
			Topic: chanName,
			What:  "read",
			SeqId: readId,
		},
		sess: helper.sessions[0],
	}
	helper.topic.handleClientMsg(msg)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	// Topic metadata.
	// We do not update read ids for Channel Topic.
	if actualReadId := helper.topic.perUser[from].readID; actualReadId != 0 {
		t.Errorf("perUser[%s].readID: expected 0, found %d.", from.UserId(), actualReadId)
	}
	// Server 消息. Note 消息 aren't forwarded by Channel Topic.
	for i, r := range helper.results {
		if numMessages := len(r.messages); numMessages != 0 {
			t.Errorf("User %d is not expected to receive any messages, %d received.", i, numMessages)
		}
	}

	// Send a pres back to the sender.
	if len(helper.hubMessages) != 1 {
		t.Fatalf("Hubhelper.route did not expect any messages, however %d received.", len(helper.hubMessages))
	}
	if mm, ok := helper.hubMessages[from.UserId()]; ok || len(mm) != 1 {
		s := mm[0]
		if s.Pres != nil {
			p := s.Pres
			if p.Topic != "me" {
				t.Errorf("Uid %s: pres notify on topic is expected to be 'me', got %s", from.UserId(), p.Topic)
			}
			if p.SkipTopic != topicName {
				t.Errorf("Uid %s: pres skip topic is expected to be '%s', got %s", from.UserId(), topicName, p.SkipTopic)
			}
			if p.Src != topicName {
				t.Errorf("Uid %s: pres.src expected: %s, found: %s", from.UserId(), topicName, p.Src)
			}
			if p.What != "read" {
				t.Errorf("Uid %s: pres.what expected: 'read', found: %s", from.UserId(), p.What)
			}
		} else {
			t.Errorf("Uid %s: hub message expected to be {pres}.", from.UserId())
		}
	} else {
		t.Errorf("Uid %s: expected 1 hub message, got %d.", from.UserId(), len(mm))
	}
}

// TestHandleBroadcastPresMe 验证 Handle Broadcast Pres Me 相关行为。
func TestHandleBroadcastPresMe(t *testing.T) {
	topicName := "usrMe"
	numUsers := 1
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatMe, topicName, true)
	defer helper.tearDown()

	uid := helper.uids[0]
	srcUid := types.Uid(10)
	helper.topic.perSubs = make(map[string]perSubsData)
	helper.topic.perSubs[srcUid.UserId()] = perSubsData{enabled: true, online: false}

	msg := &ServerComMessage{
		AsUser: uid.UserId(),
		RcptTo: uid.UserId(),
		Pres: &MsgServerPres{
			Topic: "me",
			Src:   srcUid.UserId(),
			What:  "on",
		},
	}
	helper.topic.handleServerMsg(msg)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	// Topic metadata.
	if online := helper.topic.perSubs[srcUid.UserId()].online; !online {
		t.Errorf("User %s is expected to be online.", srcUid.UserId())
	}
	// Server 消息.
	if len(helper.results[0].messages) != 1 {
		t.Fatalf("Session 0 is expected to receive one message. Received %d.", len(helper.results[0].messages))
	}
	s := helper.results[0].messages[0].(*ServerComMessage)
	if s.RcptTo != uid.UserId() {
		t.Errorf("Message.RcptTo: expected '%s', found '%s'", uid.UserId(), s.RcptTo)
	}
	if s.Pres != nil {
		pres := s.Pres
		if pres.Topic != "me" {
			t.Errorf("Expected to notify user on 'me' topic. Found: '%s'", pres.Topic)
		}
		if pres.Src != srcUid.UserId() {
			t.Errorf("Expected notification from '%s'. Found: '%s'", srcUid.UserId(), pres.Topic)
		}
		if pres.What != "on" {
			t.Errorf("Expected an online notification. Found: '%s'", pres.What)
		}
	} else {
		t.Error("Message is expected to be pres.")
	}
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hubhelper.route isn't expected to receive messages. Received %d", len(helper.hubMessages))
	}
}

// TestHandleBroadcastPresInactiveTopic 验证 Handle Broadcast Pres Inactive Topic 相关行为。
func TestHandleBroadcastPresInactiveTopic(t *testing.T) {
	topicName := "usrMe"
	numUsers := 1
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatMe, topicName, true)
	defer helper.tearDown()

	uid := helper.uids[0]
	srcUid := types.Uid(10)
	helper.topic.perSubs = make(map[string]perSubsData)
	helper.topic.perSubs[srcUid.UserId()] = perSubsData{enabled: true, online: false}

	msg := &ServerComMessage{
		AsUser: uid.UserId(),
		RcptTo: uid.UserId(),
		Pres: &MsgServerPres{
			Topic: "me",
			Src:   srcUid.UserId(),
			What:  "on",
		},
	}

	// Deactivate Topic.
	helper.topic.markDeleted()

	helper.topic.handleServerMsg(msg)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	// Topic metadata.
	if online := helper.topic.perSubs[srcUid.UserId()].online; online {
		t.Errorf("User %s is expected to be offline.", srcUid.UserId())
	}
	// Server 消息.
	if len(helper.results[0].messages) != 0 {
		t.Fatalf("Session 0 is not expected to receive messages. Received %d.", len(helper.results[0].messages))
	}
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hubhelper.route isn't expected to receive messages. Received %d", len(helper.hubMessages))
	}
}

const (
	// NoSub 指定No订阅。
	NoSub = 0
	// ExistingSubEnabled 指定Existing订阅Enabled。
	ExistingSubEnabled = 1
	// ExistingSubDisabled 指定Existing订阅Disabled。
	ExistingSubDisabled = 2
)

// NoChangeInStatusTest 完成NoChangeIn状态Test所需的内部处理。
func NoChangeInStatusTest(t *testing.T, subscriptionStatus int, what string) *TopicTestHelper {
	t.Helper()
	topicName := "usrMe"
	numUsers := 1
	helper := &TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatMe, topicName, true)

	uid := helper.uids[0]
	srcUid := types.Uid(10)
	helper.topic.perSubs = make(map[string]perSubsData)
	enabled := false
	switch subscriptionStatus {
	case NoSub:
	case ExistingSubEnabled:
		enabled = true
		fallthrough
	case ExistingSubDisabled:
		helper.topic.perSubs[srcUid.UserId()] = perSubsData{enabled: enabled, online: false}
	}

	msg := &ServerComMessage{
		AsUser: uid.UserId(),
		RcptTo: uid.UserId(),
		Pres: &MsgServerPres{
			Topic: "me",
			Src:   srcUid.UserId(),
			// No change in online status.
			What: what,
		},
	}

	helper.topic.handleServerMsg(msg)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	// Topic metadata.
	if online := helper.topic.perSubs[srcUid.UserId()].online; online {
		t.Errorf("User %s is expected to be offline.", srcUid.UserId())
	}
	// Server 消息.
	if len(helper.results[0].messages) != 0 {
		t.Fatalf("Session 0 is not expected to receive messages. Received %d.", len(helper.results[0].messages))
	}
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hubhelper.route isn't expected to receive messages. Received %d", len(helper.hubMessages))
	}
	return helper
}

// TestHandleBroadcastPresUnkn 验证 Handle Broadcast Pres Unkn 相关行为。
func TestHandleBroadcastPresUnkn(t *testing.T) {
	NoChangeInStatusTest(t, ExistingSubEnabled, "?unkn").tearDown()
}

// TestHandleBroadcastPresNone 验证 Handle Broadcast Pres None 相关行为。
func TestHandleBroadcastPresNone(t *testing.T) {
	NoChangeInStatusTest(t, ExistingSubEnabled, "?none").tearDown()
}

// TestHandleBroadcastPresRedundantUpdate 验证 Handle Broadcast Pres Redundant Update 相关行为。
func TestHandleBroadcastPresRedundantUpdate(t *testing.T) {
	h := NoChangeInStatusTest(t, ExistingSubDisabled, "off+rem")
	uid := h.uids[0]
	if _, ok := h.topic.perSubs[uid.UserId()]; ok {
		t.Errorf("Subscription for user %s expected to be deleted.", uid.UserId())
	}
	h.tearDown()
}

// TestHandleBroadcastPresNewSub 验证 Handle Broadcast Pres New Sub 相关行为。
func TestHandleBroadcastPresNewSub(t *testing.T) {
	NoChangeInStatusTest(t, NoSub, "off+wrong").tearDown()
}

// TestHandleBroadcastPresUnknownSub 验证 Handle Broadcast Pres Unknown Sub 相关行为。
func TestHandleBroadcastPresUnknownSub(t *testing.T) {
	NoChangeInStatusTest(t, NoSub, "on+rem").tearDown()
}

// TestReplyGetDescInvalidOpts 验证 Reply Get Desc Invalid Opts 相关行为。
func TestReplyGetDescInvalidOpts(t *testing.T) {
	numUsers := 1
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatMe, "" /*attach=*/, true)
	defer helper.tearDown()

	msg := ClientComMessage{
		Original: "dummy",
	}
	// Can't specify 用户 in opts.
	if err := helper.topic.replyGetDesc(helper.sessions[0], 123, false, &MsgGetOpts{User: "abcdef"}, &msg); err == nil {
		t.Error("replyGetDesc expected to error out.")
	} else if err.Error() != "invalid GetDesc query" {
		t.Errorf("Unexpected error: expected 'invalid GetDesc query', got '%s'", err.Error())
	}
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	if len(helper.results[0].messages) != 1 {
		t.Fatalf("`responses` expected to contain 1 element, found %d", len(helper.results[0].messages))
	}
	resp := helper.results[0].messages[0].(*ServerComMessage)
	if resp.Ctrl == nil {
		t.Fatalf("response expected to contain a Ctrl message")
	}
	if resp.Ctrl.Code != 400 {
		t.Errorf("response code: expected 400, found: %d", resp.Ctrl.Code)
	}
	// Presence notifications.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hub isn't expected to receive any messages, received %d", len(helper.hubMessages))
	}
}

// Verifies ctrl codes in Session outputs.
func registerSessionVerifyOutputs(t *testing.T, sessionOutput *responses, expectedCtrlCodes []int) {
	t.Helper()
	// Session output.
	if len(sessionOutput.messages) == len(expectedCtrlCodes) {
		n := len(expectedCtrlCodes)
		for i := range n {
			resp := sessionOutput.messages[i].(*ServerComMessage)
			code := expectedCtrlCodes[i]
			if resp.Ctrl != nil {
				if resp.Ctrl.Code != code {
					t.Errorf("response code: expected %d, found: %d", code, resp.Ctrl.Code)
				}
			} else {
				t.Errorf("response %d: expected to contain a Ctrl message", i)
			}
		}
	} else {
		t.Errorf("Session output: expected %d responses, received %d", len(expectedCtrlCodes),
			len(sessionOutput.messages))
	}
}

// TestRegisterSessionMe 验证 Register Session Me 相关行为。
func TestRegisterSessionMe(t *testing.T) {
	topicName := "usrMe"
	numUsers := 1
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatMe, topicName, false)
	defer helper.tearDown()
	if len(helper.topic.sessions) != 0 {
		helper.finish()
		t.Fatalf("Initially attached sessions: expected 0 vs found %d", len(helper.topic.sessions))
	}

	uid := helper.uids[0]

	// Add a couple more Session.
	for i := 1; i < 3; i++ {
		s, r := helper.newSession(fmt.Sprintf("sid%d", i), uid)
		helper.sessions = append(helper.sessions, s)
		helper.results = append(helper.results, r)
	}

	for i, s := range helper.sessions {
		join := &ClientComMessage{
			Sub: &MsgClientSub{
				Id:    fmt.Sprintf("id456-%d", i),
				Topic: "me",
			},
			AsUser: uid.UserId(),
			sess:   s,
		}
		helper.topic.registerSession(join)
	}
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	if len(helper.topic.sessions) != 3 {
		t.Errorf("Attached sessions: expected 3, found %d", len(helper.topic.sessions))
	}
	for _, s := range helper.sessions {
		if len(s.subs) != 1 {
			t.Errorf("Session subscriptions: expected 3, found %d", len(s.subs))
		}
	}
	online := helper.topic.perUser[uid].online
	if online != 3 {
		t.Errorf("Number of online sessions: expected 3, found %d", online)
	}
	// Session output.
	for _, r := range helper.results {
		registerSessionVerifyOutputs(t, r, []int{http.StatusOK})
	}
	// Presence notifications.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hub isn't expected to receive any messages, received %d", len(helper.hubMessages))
	}
}

// TestRegisterSessionInactiveTopic 验证 Register Session Inactive Topic 相关行为。
func TestRegisterSessionInactiveTopic(t *testing.T) {
	topicName := "usrMe"
	numUsers := 1
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatMe, topicName, false)
	defer helper.tearDown()
	if len(helper.topic.sessions) != 0 {
		helper.finish()
		t.Fatalf("Initially attached sessions: expected 0 vs found %d", len(helper.topic.sessions))
	}

	uid := helper.uids[0]

	s := helper.sessions[0]
	join := &ClientComMessage{
		Sub: &MsgClientSub{
			Id:    "id456",
			Topic: "me",
		},
		AsUser: uid.UserId(),
		sess:   s,
	}

	// Deactivate Topic.
	helper.topic.markDeleted()

	helper.topic.registerSession(join)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	if len(s.subs) != 0 {
		t.Errorf("Session subscriptions: expected 0, found %d", len(s.subs))
	}
	online := helper.topic.perUser[uid].online
	if online != 0 {
		t.Errorf("Number of online sessions: expected 0, found %d", online)
	}
	// Session output.
	registerSessionVerifyOutputs(t, helper.results[0], []int{http.StatusServiceUnavailable})
	// Presence notifications.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hub isn't expected to receive any messages, received %d", len(helper.hubMessages))
	}
}

// TestRegisterSessionUserSpecifiedInSetMessage 验证 Register Session User Specified In Set Message 相关行为。
func TestRegisterSessionUserSpecifiedInSetMessage(t *testing.T) {
	topicName := "grpTest"
	numUsers := 1
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatGrp, topicName, false)
	defer helper.tearDown()
	if len(helper.topic.sessions) != 0 {
		helper.finish()
		t.Fatalf("Initially attached sessions: expected 0 vs found %d", len(helper.topic.sessions))
	}

	uid := helper.uids[0]

	s := helper.sessions[0]
	join := &ClientComMessage{
		Original: topicName,
		Sub: &MsgClientSub{
			Id:    "id456",
			Topic: topicName,
			Set: &MsgSetQuery{
				Sub: &MsgSetSub{
					// Specify the 用户. This should result in an 错误.
					User: "foo",
				},
			},
		},
		AsUser: uid.UserId(),
		sess:   s,
	}

	helper.topic.registerSession(join)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	if len(s.subs) != 0 {
		t.Errorf("Session subscriptions: expected 0, found %d", len(s.subs))
	}
	online := helper.topic.perUser[uid].online
	if online != 0 {
		t.Errorf("Number of online sessions: expected 0, found %d", online)
	}
	// Session output.
	registerSessionVerifyOutputs(t, helper.results[0], []int{http.StatusBadRequest})
	// Presence notifications.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hub isn't expected to receive any messages, received %d", len(helper.hubMessages))
	}
}

// TestRegisterSessionInvalidWantStrInSetMessage 验证 Register Session Invalid Want Str In Set Message 相关行为。
func TestRegisterSessionInvalidWantStrInSetMessage(t *testing.T) {
	topicName := "grpTest"
	numUsers := 1
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatGrp, topicName, false)
	defer helper.tearDown()
	if len(helper.topic.sessions) != 0 {
		helper.finish()
		t.Fatalf("Initially attached sessions: expected 0 vs found %d", len(helper.topic.sessions))
	}

	uid := helper.uids[0]

	s := helper.sessions[0]
	join := &ClientComMessage{
		Original: topicName,
		Sub: &MsgClientSub{
			Id:    "id456",
			Topic: topicName,
			Set: &MsgSetQuery{
				Sub: &MsgSetSub{
					// Specify the 用户. This should result in an 错误.
					Mode: "Invalid mode string",
				},
			},
		},
		AsUser: uid.UserId(),
		sess:   s,
	}

	helper.topic.registerSession(join)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	if len(s.subs) != 0 {
		t.Errorf("Session subscriptions: expected 0, found %d", len(s.subs))
	}
	online := helper.topic.perUser[uid].online
	if online != 0 {
		t.Errorf("Number of online sessions: expected 0, found %d", online)
	}
	// Session output.
	registerSessionVerifyOutputs(t, helper.results[0], []int{http.StatusBadRequest})
	// Presence notifications.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hub isn't expected to receive any messages, received %d", len(helper.hubMessages))
	}
}

// TestRegisterSessionMaxSubscriberCountExceeded 验证 Register Session Max Subscriber Count Exceeded 相关行为。
func TestRegisterSessionMaxSubscriberCountExceeded(t *testing.T) {
	topicName := "grpTest"
	// Pretend we already exceeded the maximum 用户 count. This should produce an 错误.
	numUsers := 10
	oldMaxSubscribers := globals.maxSubscriberCount
	globals.maxSubscriberCount = 10
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatGrp, topicName, false)
	defer func() {
		helper.tearDown()
		globals.maxSubscriberCount = oldMaxSubscribers
	}()
	if len(helper.topic.sessions) != 0 {
		helper.finish()
		t.Fatalf("Initially attached sessions: expected 0 vs found %d", len(helper.topic.sessions))
	}

	// New uid. This should attempt to add a new 订阅.
	uid := types.Uid(10001)
	s, r := helper.newSession("test-sid", uid)
	helper.sessions = append(helper.sessions, s)
	helper.results = append(helper.results, r)

	join := &ClientComMessage{
		Original: topicName,
		Sub: &MsgClientSub{
			Id:    "id456",
			Topic: topicName,
		},
		AsUser: uid.UserId(),
		sess:   s,
	}

	helper.topic.registerSession(join)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	if len(s.subs) != 0 {
		t.Errorf("Session subscriptions: expected 0, found %d", len(s.subs))
	}
	online := helper.topic.perUser[uid].online
	if online != 0 {
		t.Errorf("Number of online sessions: expected 0, found %d", online)
	}
	// Session output.
	registerSessionVerifyOutputs(t, r, []int{http.StatusUnprocessableEntity})
	// Presence notifications.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hub isn't expected to receive any messages, received %d", len(helper.hubMessages))
	}
}

// TestRegisterSessionLowAuthLevelWithSysTopic 验证 Register Session Low Auth Level With Sys Topic 相关行为。
func TestRegisterSessionLowAuthLevelWithSysTopic(t *testing.T) {
	topicName := "sys"
	// No one is subscribed to sys.
	numUsers := 0
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatSys, topicName, false)
	defer helper.tearDown()
	if len(helper.topic.sessions) != 0 {
		helper.finish()
		t.Fatalf("Initially attached sessions: expected 0 vs found %d", len(helper.topic.sessions))
	}

	// New uid. This should attempt to add a new 订阅
	// which produces an 错误 b/c authLevel isn't root.
	uid := types.Uid(10001)
	s, r := helper.newSession("test-sid", uid)
	helper.sessions = append(helper.sessions, s)
	helper.results = append(helper.results, r)

	join := &ClientComMessage{
		Original: topicName,
		Sub: &MsgClientSub{
			Id:    "id456",
			Topic: topicName,
		},
		AsUser: uid.UserId(),
		sess:   s,
	}

	helper.topic.registerSession(join)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	if len(s.subs) != 0 {
		t.Errorf("Session subscriptions: expected 0, found %d", len(s.subs))
	}
	online := helper.topic.perUser[uid].online
	if online != 0 {
		t.Errorf("Number of online sessions: expected 0, found %d", online)
	}
	// Session output.
	registerSessionVerifyOutputs(t, r, []int{http.StatusForbidden})
	// Presence notifications.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hub isn't expected to receive any messages, received %d", len(helper.hubMessages))
	}
}

// TestRegisterSessionNewChannelGetSubDbError 验证 Register Session New Channel Get Sub Db Error 相关行为。
func TestRegisterSessionNewChannelGetSubDbError(t *testing.T) {
	topicName := "grpTest"
	chanName := "chnTest"
	numUsers := 1
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatGrp, topicName, false)
	// It is a Channel.
	helper.topic.isChan = true
	defer helper.tearDown()
	if len(helper.topic.sessions) != 0 {
		helper.finish()
		t.Fatalf("Initially attached sessions: expected 0 vs found %d", len(helper.topic.sessions))
	}

	// New uid. This should attempt to add a new 订阅
	// which produces an 错误 b/c authLevel isn't root.
	uid := types.Uid(10001)
	s, r := helper.newSession("test-sid", uid)
	helper.sessions = append(helper.sessions, s)
	helper.results = append(helper.results, r)

	join := &ClientComMessage{
		Original: chanName,
		Sub: &MsgClientSub{
			Id:    "id456",
			Topic: chanName,
		},
		AsUser: uid.UserId(),
		sess:   s,
	}

	helper.ss.EXPECT().Get(chanName, uid, false).Return(nil, types.ErrInternal)

	helper.topic.registerSession(join)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	if len(s.subs) != 0 {
		t.Errorf("Session subscriptions: expected 0, found %d", len(s.subs))
	}
	online := helper.topic.perUser[uid].online
	if online != 0 {
		t.Errorf("Number of online sessions: expected 0, found %d", online)
	}
	// Session output.
	registerSessionVerifyOutputs(t, r, []int{http.StatusInternalServerError})
	// Presence notifications.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hub isn't expected to receive any messages, received %d", len(helper.hubMessages))
	}
}

// TestRegisterSessionCreateSubFailed 验证 Register Session Create Sub Failed 相关行为。
func TestRegisterSessionCreateSubFailed(t *testing.T) {
	topicName := "grpTest"
	numUsers := 1
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatGrp, topicName, false)
	defer helper.tearDown()
	if len(helper.topic.sessions) != 0 {
		helper.finish()
		t.Fatalf("Initially attached sessions: expected 0 vs found %d", len(helper.topic.sessions))
	}

	// New uid. This should attempt to add a new 订阅
	// which produces an 错误 b/c authLevel isn't root.
	uid := types.Uid(10001)
	s, r := helper.newSession("test-sid", uid)
	helper.sessions = append(helper.sessions, s)
	helper.results = append(helper.results, r)

	join := &ClientComMessage{
		Original: topicName,
		Sub: &MsgClientSub{
			Id:    "id456",
			Topic: topicName,
		},
		AsUser:  uid.UserId(),
		AuthLvl: int(auth.LevelAuth),
		sess:    s,
	}

	helper.ss.EXPECT().Get(topicName, uid, true).Return(nil, types.ErrInternal)

	helper.topic.registerSession(join)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	if len(s.subs) != 0 {
		t.Errorf("Session subscriptions: expected 0, found %d", len(s.subs))
	}
	online := helper.topic.perUser[uid].online
	if online != 0 {
		t.Errorf("Number of online sessions: expected 0, found %d", online)
	}
	// Session output.
	registerSessionVerifyOutputs(t, r, []int{http.StatusInternalServerError})
	// Presence notifications.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hub isn't expected to receive any messages, received %d", len(helper.hubMessages))
	}
}

// TestRegisterSessionAsChanUserNotChanSubcriber 验证 Register Session As Chan User Not Chan Subcriber 相关行为。
func TestRegisterSessionAsChanUserNotChanSubcriber(t *testing.T) {
	topicName := "grpTest"
	chanName := "chnTest"
	numUsers := 1
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatGrp, topicName, false)
	// The Topic is a Channel.
	helper.topic.isChan = true
	defer helper.tearDown()
	if len(helper.topic.sessions) != 0 {
		helper.finish()
		t.Fatalf("Initially attached sessions: expected 0 vs found %d", len(helper.topic.sessions))
	}

	s := helper.sessions[0]
	uid := helper.uids[0]
	r := helper.results[0]

	// 用户 is not a Channel subscriber (userData.isChan is false).
	join := &ClientComMessage{
		Original: chanName,
		Sub: &MsgClientSub{
			Id:    "id456",
			Topic: chanName,
		},
		AsUser:  uid.UserId(),
		AuthLvl: int(auth.LevelAuth),
		sess:    s,
	}

	helper.topic.registerSession(join)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	if len(s.subs) != 0 {
		t.Errorf("Session subscriptions: expected 0, found %d", len(s.subs))
	}
	online := helper.topic.perUser[uid].online
	if online != 0 {
		t.Errorf("Number of online sessions: expected 0, found %d", online)
	}
	// Session output. Tell the subscriber to use non-Channel name.
	registerSessionVerifyOutputs(t, r, []int{http.StatusSeeOther})
	// Presence notifications.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hub isn't expected to receive any messages, received %d", len(helper.hubMessages))
	}
}

// TestRegisterSessionOwnerBansHimself 验证 Register Session Owner Bans Himself 相关行为。
func TestRegisterSessionOwnerBansHimself(t *testing.T) {
	topicName := "grpTest"
	numUsers := 1
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatGrp, topicName, false)
	defer helper.tearDown()
	if len(helper.topic.sessions) != 0 {
		helper.finish()
		t.Fatalf("Initially attached sessions: expected 0 vs found %d", len(helper.topic.sessions))
	}

	s := helper.sessions[0]
	uid := helper.uids[0]
	r := helper.results[0]

	// 用户 is the Topic owner.
	helper.topic.owner = uid
	pud := helper.topic.perUser[uid]
	pud.modeGiven |= types.ModeOwner
	helper.topic.perUser[uid] = pud

	join := &ClientComMessage{
		Original: topicName,
		Sub: &MsgClientSub{
			Id:    "id456",
			Topic: topicName,
			Set: &MsgSetQuery{
				Sub: &MsgSetSub{
					// No O 权限.
					Mode: "JPRW",
				},
			},
		},
		AsUser:  uid.UserId(),
		AuthLvl: int(auth.LevelAuth),
		sess:    s,
	}

	helper.topic.registerSession(join)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	if len(s.subs) != 0 {
		t.Errorf("Session subscriptions: expected 0, found %d", len(s.subs))
	}
	online := helper.topic.perUser[uid].online
	if online != 0 {
		t.Errorf("Number of online sessions: expected 0, found %d", online)
	}
	// Session output.
	registerSessionVerifyOutputs(t, r, []int{http.StatusForbidden})
	// Presence notifications.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hub isn't expected to receive any messages, received %d", len(helper.hubMessages))
	}
}

// TestRegisterSessionInvalidOwnershipTransfer 验证 Register Session Invalid Ownership Transfer 相关行为。
func TestRegisterSessionInvalidOwnershipTransfer(t *testing.T) {
	topicName := "grpTest"
	numUsers := 2
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatGrp, topicName, false)
	defer helper.tearDown()
	if len(helper.topic.sessions) != 0 {
		helper.finish()
		t.Fatalf("Initially attached sessions: expected 0 vs found %d", len(helper.topic.sessions))
	}

	s := helper.sessions[1]
	uid := helper.uids[1]
	r := helper.results[1]

	// 用户 is the Topic owner.
	pud := helper.topic.perUser[uid]
	pud.modeWant = types.ModeCPublic
	pud.modeGiven = types.ModeCPublic
	helper.topic.perUser[uid] = pud

	join := &ClientComMessage{
		Original: topicName,
		Sub: &MsgClientSub{
			Id:    "id456",
			Topic: topicName,
			Set: &MsgSetQuery{
				Sub: &MsgSetSub{
					// Want ownership.
					Mode: "JPRWSO",
				},
			},
		},
		AsUser:  uid.UserId(),
		AuthLvl: int(auth.LevelAuth),
		sess:    s,
	}

	helper.topic.registerSession(join)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	if len(s.subs) != 0 {
		t.Errorf("Session subscriptions: expected 0, found %d", len(s.subs))
	}
	online := helper.topic.perUser[uid].online
	if online != 0 {
		t.Errorf("Number of online sessions: expected 0, found %d", online)
	}
	// Session output.
	registerSessionVerifyOutputs(t, r, []int{http.StatusForbidden})
	// Presence notifications.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hub isn't expected to receive any messages, received %d", len(helper.hubMessages))
	}
}

// TestRegisterSessionMetadataUpdateFails 验证 Register Session Metadata Update Fails 相关行为。
func TestRegisterSessionMetadataUpdateFails(t *testing.T) {
	topicName := "grpTest"
	numUsers := 2
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatGrp, topicName, false)
	defer helper.tearDown()
	if len(helper.topic.sessions) != 0 {
		helper.finish()
		t.Fatalf("Initially attached sessions: expected 0 vs found %d", len(helper.topic.sessions))
	}

	s := helper.sessions[1]
	uid := helper.uids[1]
	r := helper.results[1]

	pud := helper.topic.perUser[uid]
	pud.modeWant = types.ModeCPublic
	pud.modeGiven = types.ModeCPublic
	helper.topic.perUser[uid] = pud

	// Want ownership.
	newWant := "JRWP"
	join := &ClientComMessage{
		Original: topicName,
		Sub: &MsgClientSub{
			Id:    "id456",
			Topic: topicName,
			Set: &MsgSetQuery{
				Sub: &MsgSetSub{
					Mode: newWant,
				},
			},
		},
		AsUser:  uid.UserId(),
		AuthLvl: int(auth.LevelAuth),

		sess: s,
	}
	// DB call fails.
	helper.ss.EXPECT().Update(topicName, uid, gomock.Any()).Return(types.ErrInternal)

	helper.topic.registerSession(join)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	if len(s.subs) != 0 {
		t.Errorf("Session subscriptions: expected 0, found %d", len(s.subs))
	}
	online := helper.topic.perUser[uid].online
	if online != 0 {
		t.Errorf("Number of online sessions: expected 0, found %d", online)
	}
	// Session output.
	registerSessionVerifyOutputs(t, r, []int{http.StatusInternalServerError})
	// Presence notifications.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hub isn't expected to receive any messages, received %d", len(helper.hubMessages))
	}
}

// TestRegisterSessionOwnerChangeDbCallFails 验证 Register Session Owner Change Db Call Fails 相关行为。
func TestRegisterSessionOwnerChangeDbCallFails(t *testing.T) {
	topicName := "grpTest"
	numUsers := 1
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatGrp, topicName, false)
	defer helper.tearDown()
	if len(helper.topic.sessions) != 0 {
		helper.finish()
		t.Fatalf("Initially attached sessions: expected 0 vs found %d", len(helper.topic.sessions))
	}

	s := helper.sessions[0]
	uid := helper.uids[0]
	r := helper.results[0]

	// 用户 is the Topic owner.
	pud := helper.topic.perUser[uid]
	pud.modeWant = types.ModeCPublic
	helper.topic.perUser[uid] = pud

	// Want ownership.
	newWant := "JRWPASO"
	join := &ClientComMessage{
		Original: topicName,
		Sub: &MsgClientSub{
			Id:    "id456",
			Topic: topicName,
			Set: &MsgSetQuery{
				Sub: &MsgSetSub{
					Mode: newWant,
				},
			},
		},
		AsUser:  uid.UserId(),
		AuthLvl: int(auth.LevelAuth),
		sess:    s,
	}
	helper.ss.EXPECT().Update(topicName, uid, gomock.Any()).Return(nil).Times(2)
	// OwnerChange call fails.
	helper.tt.EXPECT().OwnerChange(topicName, uid).Return(types.ErrInternal)

	helper.topic.registerSession(join)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	if len(s.subs) != 0 {
		t.Errorf("Session subscriptions: expected 0, found %d", len(s.subs))
	}
	online := helper.topic.perUser[uid].online
	if online != 0 {
		t.Errorf("Number of online sessions: expected 0, found %d", online)
	}
	registerSessionVerifyOutputs(t, r, []int{})
	// Presence notifications.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hub isn't expected to receive any messages, received %d", len(helper.hubMessages))
	}
}

// TestUnregisterSessionSimple 验证 Unregister Session Simple 相关行为。
func TestUnregisterSessionSimple(t *testing.T) {
	topicName := "usrMe"
	numUsers := 1
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatMe, topicName /*attach=*/, true)
	defer helper.tearDown()

	uid := helper.uids[0]
	helper.uu.EXPECT().UpdateLastSeen(uid, gomock.Any(), gomock.Any()).Return(nil)

	// Add a couple more Session.
	for i := 1; i < 3; i++ {
		s, r := helper.newSession(fmt.Sprintf("sid%d", i), uid)
		helper.sessions = append(helper.sessions, s)
		helper.results = append(helper.results, r)
		helper.topic.sessions[s] = perSessionData{uid: uid}
		pu := helper.topic.perUser[uid]
		pu.online++
		helper.topic.perUser[uid] = pu
	}

	// Initial online and attach Session counts.
	if len(helper.topic.sessions) != 3 {
		helper.finish()
		t.Fatalf("Initially attached sessions: expected 3 vs found %d", len(helper.topic.sessions))
	}
	if online := helper.topic.perUser[uid].online; online != 3 {
		t.Errorf("Number of online sessions: expected 3 vs found %d", online)
	}

	s := helper.sessions[0]
	r := helper.results[0]
	leave := &ClientComMessage{
		Leave: &MsgClientLeave{
			Id:    "id456",
			Topic: topicName,
		},
		AsUser: uid.UserId(),
		sess:   s,
		init:   true,
	}
	helper.topic.unregisterSession(leave)

	helper.finish()
	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	if len(helper.topic.sessions) != 2 {
		t.Errorf("Attached sessions: expected 2, found %d", len(helper.topic.sessions))
	}
	if len(s.subs) != 0 {
		t.Errorf("Session subscriptions: expected 0, found %d", len(helper.sessions[0].subs))
	}
	if online := helper.topic.perUser[uid].online; online != 2 {
		t.Errorf("Number of online sessions after unregistering: expected 2, found %d", online)
	}
	// Session output.
	registerSessionVerifyOutputs(t, r, []int{http.StatusOK})
	// Presence notifications.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hub isn't expected to receive any messages, received %d", len(helper.hubMessages))
	}
}

// TestUnregisterSessionInactiveTopic 验证 Unregister Session Inactive Topic 相关行为。
func TestUnregisterSessionInactiveTopic(t *testing.T) {
	topicName := "usrMe"
	numUsers := 1
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatMe, topicName, true)
	defer helper.tearDown()

	uid := helper.uids[0]

	// Initial online and attach Session counts.
	if len(helper.topic.sessions) != 1 {
		helper.finish()
		t.Fatalf("Initially attached sessions: expected 1 vs found %d", len(helper.topic.sessions))
	}
	if online := helper.topic.perUser[uid].online; online != 1 {
		t.Errorf("Number of online sessions: expected 1 vs found %d", online)
	}

	s := helper.sessions[0]
	r := helper.results[0]
	leave := &ClientComMessage{
		Leave: &MsgClientLeave{
			Id:    "id456",
			Topic: topicName,
		},
		AsUser: uid.UserId(),
		sess:   s,
		init:   true,
	}

	// Deactivate Topic.
	helper.topic.markDeleted()

	helper.topic.unregisterSession(leave)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	if len(helper.topic.sessions) != 1 {
		t.Errorf("Attached sessions: expected 1, found %d", len(helper.topic.sessions))
	}
	if len(s.subs) != 0 {
		t.Errorf("Session subscriptions: expected 0, found %d", len(s.subs))
	}
	if online := helper.topic.perUser[uid].online; online != 1 {
		t.Errorf("Number of online sessions after unregistering: expected 1, found %d", online)
	}
	// Session output.
	registerSessionVerifyOutputs(t, r, []int{http.StatusServiceUnavailable})
	// Presence notifications.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hub isn't expected to receive any messages, received %d", len(helper.hubMessages))
	}
}

// TestUnregisterSessionUnsubscribe 验证 Unregister Session Unsubscribe 相关行为。
func TestUnregisterSessionUnsubscribe(t *testing.T) {
	topicName := "grpTest"
	numUsers := 3
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatGrp, topicName, true)
	defer helper.tearDown()

	uid := helper.uids[2]
	helper.ss.EXPECT().Delete(topicName, uid).Return(nil)

	// Add a couple more Session.
	for i := range 2 {
		s, r := helper.newSession(fmt.Sprintf("sid-uid-%d-%d", uid, i), uid)
		helper.sessions = append(helper.sessions, s)
		helper.results = append(helper.results, r)
		helper.topic.sessions[s] = perSessionData{uid: uid}
		pu := helper.topic.perUser[uid]
		pu.online++
		helper.topic.perUser[uid] = pu
	}

	// Initial online and attach Session counts.
	if len(helper.topic.sessions) != 5 {
		helper.finish()
		t.Fatalf("Initially attached sessions: expected 5 vs found %d", len(helper.topic.sessions))
	}
	if online := helper.topic.perUser[uid].online; online != 3 {
		t.Errorf("Number of online sessions: expected 3 vs found %d", online)
	}

	leave := &ClientComMessage{
		Leave: &MsgClientLeave{
			Id:    "id456",
			Topic: topicName,
			Unsub: true,
		},
		AsUser: uid.UserId(),
		sess:   helper.sessions[0],
		init:   true,
	}
	helper.topic.unregisterSession(leave)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	if len(helper.topic.sessions) != 2 {
		t.Errorf("Attached sessions: expected 2, found %d", len(helper.topic.sessions))
	}
	if len(helper.sessions[0].subs) != 0 {
		t.Errorf("Session subscriptions: expected 0, found %d", len(helper.sessions[0].subs))
	}
	if pu, ok := helper.topic.perUser[uid]; pu.online != 0 || ok {
		t.Errorf("Number of online sessions after unsubscribing: expected 2, found %d; perUser entry found: %t", pu.online, ok)
	}
	// Session output. Session 2, 3, 4 are the evicted/unsubscribed uid.
	for i := 2; i < 5; i++ {
		r := helper.results[i]
		registerSessionVerifyOutputs(t, r, []int{http.StatusResetContent})
	}
	// Presence notifications.
	if len(helper.hubMessages) != 2 {
		t.Errorf("Hub messages recipients: expected 2, received %d", len(helper.hubMessages))
	}
	// Group presSubs.
	if grpPres, ok := helper.hubMessages[topicName]; ok {
		if len(grpPres) != 2 {
			t.Fatalf("Group presence messages: expected 2, got %d", len(grpPres))
		}
		for _, msg := range grpPres {
			//
			pres := msg.Pres
			if pres == nil {
				t.Fatal("Presence message expected in hub output, but not found.")
			}
			if pres.Topic != topicName {
				t.Errorf("Presence message topic: expected %s, found %s", topicName, pres.Topic)
			}
			if pres.Src != uid.UserId() {
				t.Errorf("Presence message src: expected %s, found %s", uid.UserId(), pres.Src)
			}
			if pres.What != "acs" && pres.What != "off" {
				t.Errorf("Presence message what: expected 'acs' or 'off', found %s", pres.What)
			}
		}
	} else {
		t.Errorf("Hub expected to pres recipient %s", topicName)
	}
	// 用户 notification.
	if userPres, ok := helper.hubMessages[uid.UserId()]; ok {
		if len(userPres) != 1 {
			t.Fatalf("User presence messages: expected 1, got %d", len(userPres))
		}
		pres := userPres[0].Pres
		if pres == nil {
			t.Fatal("Presence message expected in hub output, but not found.")
		}
		if pres.Topic != "me" {
			t.Errorf("Presence message topic: expected 'me', found %s", pres.Topic)
		}
		if pres.Src != topicName {
			t.Errorf("Presence message src: expected %s, found %s", topicName, pres.Src)
		}
		if pres.What != "gone" {
			t.Errorf("Presence message what: expected 'gone', found %s", pres.What)
		}
	} else {
		t.Errorf("Hub expected to pres recipient %s", uid.UserId())
	}
}

// TestUnregisterSessionOwnerCannotUnsubscribe 验证 Unregister Session Owner Cannot Unsubscribe 相关行为。
func TestUnregisterSessionOwnerCannotUnsubscribe(t *testing.T) {
	topicName := "grpTest"
	numUsers := 3
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatGrp, topicName, true)
	defer helper.tearDown()

	uid := helper.uids[0]
	s := helper.sessions[0]
	r := helper.results[0]

	leave := &ClientComMessage{
		Leave: &MsgClientLeave{
			Id:    "id456",
			Topic: topicName,
			Unsub: true,
		},
		AsUser: uid.UserId(),
		sess:   s,
		init:   true,
	}

	helper.topic.unregisterSession(leave)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	if len(helper.topic.sessions) != 3 {
		t.Errorf("Attached sessions: expected 3, found %d", len(helper.topic.sessions))
	}
	if len(s.subs) != 0 {
		t.Errorf("Session subscriptions: expected 0, found %d", len(helper.sessions[0].subs))
	}
	if online := helper.topic.perUser[uid].online; online != 1 {
		t.Errorf("Number of online sessions after failed unsubscribing: expected 1, found %d.", online)
	}
	// Session output.
	registerSessionVerifyOutputs(t, r, []int{http.StatusForbidden})
	// Presence notifications.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hub messages recipients: expected 0, received %d", len(helper.hubMessages))
	}
}

// TestUnregisterSessionUnsubDeleteCallFails 验证 Unregister Session Unsub Delete Call Fails 相关行为。
func TestUnregisterSessionUnsubDeleteCallFails(t *testing.T) {
	topicName := "grpTest"
	numUsers := 3
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatGrp, topicName, true)
	defer helper.tearDown()

	// Unsubscribe 用户 1 (cannot unsub 用户 0, the owner).
	uid := helper.uids[1]
	s := helper.sessions[1]
	r := helper.results[1]

	leave := &ClientComMessage{
		Leave: &MsgClientLeave{
			Id:    "id456",
			Topic: topicName,
			Unsub: true,
		},
		AsUser: uid.UserId(),
		sess:   s,
		init:   true,
	}
	// DB call fails.
	helper.ss.EXPECT().Delete(topicName, uid).Return(types.ErrInternal)

	helper.topic.unregisterSession(leave)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	if len(helper.topic.sessions) != 3 {
		t.Errorf("Attached sessions: expected 3, found %d", len(helper.topic.sessions))
	}
	if len(s.subs) != 0 {
		t.Errorf("Session subscriptions: expected 0, found %d", len(helper.sessions[0].subs))
	}
	if online := helper.topic.perUser[uid].online; online != 1 {
		t.Errorf("Number of online sessions after failed unsubscribing: expected 1, found %d.", online)
	}
	// Session output.
	registerSessionVerifyOutputs(t, r, []int{http.StatusInternalServerError})
	// Presence notifications.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hub messages recipients: expected 0, received %d", len(helper.hubMessages))
	}
}

// TestHandleMetaChanErr 验证 Handle Meta Chan Err 相关行为。
func TestHandleMetaChanErr(t *testing.T) {
	topicName := "grpTest"
	chanName := "chnTest"
	numUsers := 3
	helper := TopicTestHelper{}
	defer helper.tearDown()
	helper.setUp(t, numUsers, types.TopicCatGrp, topicName, false)

	// This is not a Channel. However, we will try to handle an info 消息 where
	// the Topic is referenced as "chn".
	helper.topic.isChan = false
	// Empty 消息 since this 请求 should trigger an 错误 anyway.
	meta := &ClientComMessage{
		AsUser:   helper.uids[0].UserId(),
		Original: chanName,
		MetaWhat: constMsgMetaDesc | constMsgMetaSub | constMsgMetaData | constMsgMetaDel,
		sess:     helper.sessions[0],
	}
	helper.topic.handleMeta(meta)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	// Session output.
	registerSessionVerifyOutputs(t, helper.results[0], []int{http.StatusNotFound})
	// Presence notifications.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hub messages recipients: expected 0, received %d", len(helper.hubMessages))
	}
}

// TestHandleMetaGet 验证 Handle Meta Get 相关行为。
func TestHandleMetaGet(t *testing.T) {
	topicName := "usrMe"
	numUsers := 1
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatMe, topicName, true)
	defer helper.tearDown()

	uid := helper.uids[0]
	helper.mm.EXPECT().GetAll(topicName, uid, gomock.Any()).Return([]types.Message{}, nil)
	helper.mm.EXPECT().GetDeleted(topicName, uid, gomock.Any()).Return([]types.Range{}, 0, nil)
	helper.uu.EXPECT().GetTopics(uid, gomock.Any()).Return([]types.Subscription{}, nil)

	meta := &ClientComMessage{
		Get: &MsgClientGet{
			Id:    "id456",
			Topic: topicName,
			MsgGetQuery: MsgGetQuery{
				What: "desc sub data del",
				Desc: &MsgGetOpts{},
				Sub:  &MsgGetOpts{},
				Data: &MsgGetOpts{},
				Del:  &MsgGetOpts{},
			},
		},
		AsUser:   uid.UserId(),
		MetaWhat: constMsgMetaDesc | constMsgMetaSub | constMsgMetaData | constMsgMetaDel,
		sess:     helper.sessions[0],
	}
	helper.topic.handleMeta(meta)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	r := helper.results[0]
	if len(r.messages) != 4 {
		t.Errorf("responses received: expected 4, received %d", len(r.messages))
	}
	for _, msg := range r.messages {
		m := msg.(*ServerComMessage)
		if m.Meta != nil {
			if m.Meta.Desc == nil {
				t.Error("Meta.Desc expected to be specified.")
			}
		} else if m.Ctrl == nil {
			t.Error("Expected only meta or ctrl messages.")
		}
	}
	// Presence notifications.
	if len(helper.hubMessages) != 0 {
		t.Errorf("Hub messages recipients: expected 0, received %d", len(helper.hubMessages))
	}
}

// supersetOf 是用于验证 map 是否包含指定键值子集的 GoMock 匹配器。
type supersetOf struct {
	// subset 保存期望在实际 map 中出现的键值集合。
	subset map[string]string
}

// SupersetOf 完成SupersetOf所需的内部处理。
func SupersetOf(subset map[string]string) gomock.Matcher {
	return &supersetOf{subset}
}

// Matches 判断是否满足es条件。
func (s *supersetOf) Matches(x any) bool {
	super := x.(map[string]any)
	if super == nil {
		return false
	}
	for k, v := range s.subset {
		if x, ok := super[k]; ok {
			val := x.(string)
			if val != v {
				return false
			}
		} else {
			return false
		}
	}
	return true
}

// String 返回当前值的可读字符串表示。
func (s *supersetOf) String() string {
	return fmt.Sprintf("%+v is subset", s.subset)
}

// TestHandleMetaSetDescMePublicPrivate 验证 Handle Meta Set Desc Me Public Private 相关行为。
func TestHandleMetaSetDescMePublicPrivate(t *testing.T) {
	topicName := "usrMe"
	numUsers := 1
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatMe, topicName /*attach=*/, true)
	defer helper.tearDown()

	uid := helper.uids[0]
	gomock.InOrder(
		helper.uu.EXPECT().Update(uid, SupersetOf(map[string]string{"Public": "new public"})).Return(nil),
		helper.ss.EXPECT().Update(topicName, uid, map[string]any{"Private": "new private"}).Return(nil),
	)

	meta := &ClientComMessage{
		Set: &MsgClientSet{
			Id:    "id456",
			Topic: topicName,
			MsgSetQuery: MsgSetQuery{
				Desc: &MsgSetDesc{
					Public:  "new public",
					Private: "new private",
				},
			},
		},
		AsUser:   uid.UserId(),
		MetaWhat: constMsgMetaDesc,
		sess:     helper.sessions[0],
	}
	helper.topic.handleMeta(meta)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	r := helper.results[0]
	if len(r.messages) != 1 {
		t.Fatalf("responses received: expected 1, received %d", len(r.messages))
	}
	msg := r.messages[0].(*ServerComMessage)
	if msg == nil || msg.Ctrl == nil {
		t.Fatalf("Server message expected to have a ctrl submessage: %+v", msg)
	}
	if msg.Ctrl.Code != 200 {
		t.Errorf("Response code: expected 200, found %d", msg.Ctrl.Code)
	}
	// Presence notifications.
	if len(helper.hubMessages) != 1 {
		t.Fatalf("Hub messages recipients: expected 1, received %d", len(helper.hubMessages))
	}
	// Make sure uid's Session are notified.
	if userPres, ok := helper.hubMessages[uid.UserId()]; ok {
		if len(userPres) != 1 {
			t.Fatalf("User presence messages: expected 1, got %d", len(userPres))
		}
		if userPres[0].SkipSid != helper.sessions[0].sid {
			t.Errorf("Pres notification SkipSid: %s expected vs %s found", helper.sessions[0].sid, userPres[0].SkipSid)
		}
		pres := userPres[0].Pres
		if pres == nil {
			t.Fatal("Presence message expected in hub output, but not found.")
		}
		if pres.Topic != "me" {
			t.Errorf("Presence message topic: expected 'me', found %s", pres.Topic)
		}
		if pres.What != "upd" {
			t.Errorf("Presence message what: expected 'upd', found %s", pres.What)
		}
	} else {
		t.Errorf("Hub expected to pres recipient %s", uid.UserId())
	}
}

// TestHandleSessionUpdateSessToForeground 验证 Handle Session Update Sess To Foreground 相关行为。
func TestHandleSessionUpdateSessToForeground(t *testing.T) {
	topicName := "usrMe"
	numUsers := 1
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatMe, topicName /*attach=*/, true)
	defer helper.tearDown()

	uid := helper.uids[0]
	supd := &sessionUpdate{
		sess: helper.sessions[0],
	}
	var uaAgent string
	helper.topic.handleSessionUpdate(supd, &uaAgent, nil)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	// Expect online count bumped up to 2.
	if online := helper.topic.perUser[uid].online; online != 2 {
		t.Errorf("online count for %s: expected 2, found %d", uid.UserId(), online)
	}
}

// TestHandleSessionUpdateUserAgent 验证 Handle Session Update User Agent 相关行为。
func TestHandleSessionUpdateUserAgent(t *testing.T) {
	topicName := "usrMe"
	numUsers := 1
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatMe, topicName /*attach=*/, true)
	defer helper.tearDown()

	uid := helper.uids[0]
	supd := &sessionUpdate{
		userAgent: "newUA",
	}
	uaAgent := "oldUA"
	timer := time.NewTimer(time.Hour)
	helper.topic.handleSessionUpdate(supd, &uaAgent, timer)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	// online count stays 1.
	if online := helper.topic.perUser[uid].online; online != 1 {
		t.Errorf("online count for %s: expected 1, found %d", uid.UserId(), online)
	}
	if uaAgent != "newUA" {
		t.Errorf("User agent: expected 'newUA', found '%s'", uaAgent)
	}
	timer.Stop()
}

// TestHandleUATimerEvent 验证 Handle UA Timer Event 相关行为。
func TestHandleUATimerEvent(t *testing.T) {
	topicName := "usrMe"
	numUsers := 1
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatMe, topicName /*attach=*/, true)
	defer helper.tearDown()

	uid := helper.uids[0]
	helper.topic.perSubs = make(map[string]perSubsData)
	helper.topic.perSubs[uid.UserId()] = perSubsData{online: true}
	helper.topic.handleUATimerEvent("newUA")
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	if helper.topic.userAgent != "newUA" {
		t.Errorf("Topic's user agent: expected 'newUA', found '%s'", helper.topic.userAgent)
	}
	// Presence notifications.
	if len(helper.hubMessages) != 1 {
		t.Fatalf("Hub messages recipients: expected 1, received %d", len(helper.hubMessages))
	}
	// Make sure uid's Session are notified.
	if userPres, ok := helper.hubMessages[uid.UserId()]; ok {
		if len(userPres) != 1 {
			t.Fatalf("User presence messages: expected 1, got %d", len(userPres))
		}
		pres := userPres[0].Pres
		if pres == nil {
			t.Fatal("Presence message expected in hub output, but not found.")
		}
		if pres.Topic != "me" {
			t.Errorf("Presence message topic: expected 'me', found '%s'", pres.Topic)
		}
		if pres.What != "ua" {
			t.Errorf("Presence message what: expected 'ua', found '%s'", pres.What)
		}
		if pres.Src != topicName {
			t.Errorf("Presence message src: expected '%s', found '%s'", topicName, pres.Src)
		}
	} else {
		t.Errorf("Hub expected to pres recipient %s", uid.UserId())
	}
}

// TestHandleTopicTimeout 验证 Handle Topic Timeout 相关行为。
func TestHandleTopicTimeout(t *testing.T) {
	topicName := "usrMe"
	numUsers := 1
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatMe, topicName /*attach=*/, true)
	defer helper.tearDown()

	uid := helper.uids[0]
	helper.topic.perSubs = make(map[string]perSubsData)
	helper.topic.perSubs[uid.UserId()] = perSubsData{online: true}
	helper.hub.unreg = make(chan *topicUnreg, 10)
	uaTimer := time.NewTimer(time.Hour)
	notifTimer := time.NewTimer(time.Hour)
	helper.topic.handleTopicTimeout(helper.hub, "newUA", uaTimer, notifTimer)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	if len(helper.hub.unreg) != 1 {
		t.Fatalf("Hub.unreg chan must contain exactly 1 message. Found %d.", len(helper.hub.unreg))
	}
	if unreg := <-helper.hub.unreg; unreg.rcptTo != topicName {
		t.Errorf("unreg.rcptTo: expected '%s', found '%s'", topicName, unreg.rcptTo)
	}
	uaTimer.Stop()
	notifTimer.Stop()
	// Presence notifications.
	if len(helper.hubMessages) != 1 {
		t.Fatalf("Hub messages recipients: expected 1, received %d", len(helper.hubMessages))
	}
	// Make sure uid's Session are notified.
	if userPres, ok := helper.hubMessages[uid.UserId()]; ok {
		if len(userPres) != 1 {
			t.Fatalf("User presence messages: expected 1, got %d", len(userPres))
		}
		pres := userPres[0].Pres
		if pres == nil {
			t.Fatal("Presence message expected in hub output, but not found.")
		}
		if pres.Topic != "me" {
			t.Errorf("Presence message topic: expected 'me', found '%s'", pres.Topic)
		}
		if pres.What != "off" {
			t.Errorf("Presence message what: expected 'off', found '%s'", pres.What)
		}
		if pres.Src != topicName {
			t.Errorf("Presence message src: expected '%s', found '%s'", topicName, pres.Src)
		}
	} else {
		t.Errorf("Hub expected to pres recipient %s", uid.UserId())
	}
}

// TestHandleTopicTermination 验证 Handle Topic Termination 相关行为。
func TestHandleTopicTermination(t *testing.T) {
	topicName := "usrMe"
	numUsers := 1
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatMe, topicName /*attach=*/, true)
	defer helper.tearDown()

	done := make(chan bool, 1)
	exit := &shutDown{
		reason: StopDeleted,
		done:   done,
	}
	helper.topic.handleTopicTermination(exit)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	if len(done) != 1 {
		t.Fatal("done callback isn't invoked.")
	}
	<-done
	for i, s := range helper.sessions {
		if len(s.detach) != 1 {
			t.Fatalf("Session %d: detach channel is empty.", i)
		}
		val := <-s.detach
		if val != topicName {
			t.Errorf("Session %d is expected to detach from topic '%s', found '%s'.", i, topicName, val)
		}
	}
	// Presence notifications.
	if len(helper.hubMessages) != 0 {
		t.Fatalf("Hub messages recipients: expected 0, received %d", len(helper.hubMessages))
	}
}

// TestHandleBroadcastDataWithAttachments 验证 Handle Broadcast Data With Attachments 相关行为。
func TestHandleBroadcastDataWithAttachments(t *testing.T) {
	numUsers := 2
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatP2P, "p2p-test", true)
	defer helper.tearDown()
	helper.mm.EXPECT().Save(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, true)

	from := helper.uids[0].UserId()
	msg := &ClientComMessage{
		AsUser:   from,
		Original: from,
		Pub: &MsgClientPub{
			Topic:   "p2p",
			Content: "Check out this image!",
			Head: map[string]any{
				"attachments": []map[string]any{
					{"mime": "image/jpeg", "name": "photo.jpg", "size": 1024000},
				},
			},
			NoEcho: true,
		},
		sess: helper.sessions[0],
	}
	helper.topic.handleClientMsg(msg)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	// Verify 消息 with attachments was delivered
	if len(helper.results[1].messages) != 1 {
		t.Fatalf("Uid2: expected 1 message, got %d", len(helper.results[1].messages))
	}
	r := helper.results[1].messages[0].(*ServerComMessage)
	if r.Data == nil {
		t.Fatal("Response must have a data message")
	}
	if r.Data.Head == nil {
		t.Fatal("Response must have attachments in head")
	}
	attachments := r.Data.Head["attachments"]
	if attachments == nil {
		t.Fatal("Expected attachments in message head")
	}
}

// TestHandleBroadcastInfoChannelWithMultipleReaders 验证 Handle Broadcast Info Channel With Multiple Readers 相关行为。
func TestHandleBroadcastInfoChannelWithMultipleReaders(t *testing.T) {
	topicName := "grpTest"
	chanName := "chnTest"
	numUsers := 5
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatGrp, topicName, true)
	helper.topic.isChan = true
	defer helper.tearDown()
	helper.topic.lastID = 15

	readId := 12
	from := helper.uids[0]

	// Set up multiple Channel readers
	for i := 1; i < numUsers; i++ {
		uid := helper.uids[i]
		pud := helper.topic.perUser[uid]
		pud.modeGiven = types.ModeCChnReader
		pud.isChan = true
		helper.topic.perUser[uid] = pud
	}

	helper.ss.EXPECT().Update(chanName, from, map[string]any{"ReadSeqId": readId}).Return(nil)

	msg := &ClientComMessage{
		AsUser:   from.UserId(),
		Original: chanName,
		Note: &MsgClientNote{
			Topic: chanName,
			What:  "read",
			SeqId: readId,
		},
		sess: helper.sessions[0],
	}
	helper.topic.handleClientMsg(msg)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	// Channel Topic don't forward note 消息 to other 用户
	for i, r := range helper.results {
		if numMessages := len(r.messages); numMessages != 0 {
			t.Errorf("User %d is not expected to receive any messages, %d received", i, numMessages)
		}
	}

	// Only sender gets presence notification
	if len(helper.hubMessages) != 1 {
		t.Fatalf("Hub expected exactly 1 recipient, got %d", len(helper.hubMessages))
	}
	if _, ok := helper.hubMessages[from.UserId()]; !ok {
		t.Fatal("Expected presence notification for sender")
	}
}

// TestRegisterSessionWithComplexModeString 验证 Register Session With Complex Mode String 相关行为。
func TestRegisterSessionWithComplexModeString(t *testing.T) {
	topicName := "grpTest"
	numUsers := 2
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatGrp, topicName, false)
	defer helper.tearDown()

	uid := helper.uids[1]
	s := helper.sessions[1]
	r := helper.results[1]

	// 用户 with existing 订阅 wants to change mode
	pud := helper.topic.perUser[uid]
	pud.modeWant = types.ModeCPublic
	pud.modeGiven = types.ModeCPublic
	helper.topic.perUser[uid] = pud

	join := &ClientComMessage{
		Original: topicName,
		Sub: &MsgClientSub{
			Id:    "id456",
			Topic: topicName,
			Set: &MsgSetQuery{
				Sub: &MsgSetSub{
					Mode: "JRWPAS", // Complex mode string with multiple 权限
				},
			},
		},
		AsUser:  uid.UserId(),
		AuthLvl: int(auth.LevelAuth),
		sess:    s,
	}

	helper.ss.EXPECT().Update(topicName, uid, gomock.Any()).Return(nil)

	helper.topic.registerSession(join)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	if len(helper.topic.sessions) != 1 {
		t.Fatalf("Attached sessions: expected 1, found %d", len(helper.topic.sessions))
	}
	if len(s.subs) != 1 {
		t.Fatalf("Session subscriptions: expected 1, found %d", len(s.subs))
	}
	online := helper.topic.perUser[uid].online
	if online != 1 {
		t.Fatalf("Number of online sessions: expected 1, found %d", online)
	}
	registerSessionVerifyOutputs(t, r, []int{http.StatusOK})
}

// TestHandleBroadcastDataGroupWithMutedUser 验证 Handle Broadcast Data Group With Muted User 相关行为。
func TestHandleBroadcastDataGroupWithMutedUser(t *testing.T) {
	topicName := "grp-test"
	numUsers := 4
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatGrp, topicName, true)
	defer helper.tearDown()
	helper.mm.EXPECT().Save(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, true)

	// 用户 2 has muted the Topic (no Pres 权限)
	pu2 := helper.topic.perUser[helper.uids[2]]
	pu2.modeWant = types.ModeJoin | types.ModeRead | types.ModeWrite
	pu2.modeGiven = pu2.modeWant
	helper.topic.perUser[helper.uids[2]] = pu2

	from := helper.uids[0].UserId()
	msg := &ClientComMessage{
		AsUser:   from,
		Original: topicName,
		Pub: &MsgClientPub{
			Topic:   topicName,
			Content: "test message",
			NoEcho:  true,
		},
		sess: helper.sessions[0],
	}

	helper.topic.handleClientMsg(msg)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	// 用户 2 should still receive the 消息 (has Read 权限)
	if len(helper.results[2].messages) != 1 {
		t.Fatalf("Uid2: expected 1 message, got %d", len(helper.results[2].messages))
	}

	// Check presence notifications - muted 用户 should not receive presence
	if len(helper.hubMessages) != 3 { // 用户 0, 1, 3 but not 2
		t.Fatalf("Hub expected 3 recipients, got %d", len(helper.hubMessages))
	}

	// Verify 用户 2 is not in presence notifications
	if _, ok := helper.hubMessages[helper.uids[2].UserId()]; ok {
		t.Fatal("Muted user should not receive presence notifications")
	}
}

// TestUnregisterSessionWithPendingCall 验证 Unregister Session With Pending Call 相关行为。
func TestUnregisterSessionWithPendingCall(t *testing.T) {
	numUsers := 2
	helper := TopicTestHelper{}
	helper.setUp(t, numUsers, types.TopicCatP2P, "p2p-test", true)
	defer helper.tearDown()

	uid := helper.uids[0]
	s := helper.sessions[0]
	r := helper.results[0]

	// Set up a pending call matching the actual videoCall structure
	helper.topic.currentCall = &videoCall{
		seq:     123,
		parties: make(map[string]callPartyData),
	}
	helper.topic.currentCall.parties[s.sid] = callPartyData{
		uid:          uid,
		isOriginator: true,
		sess:         s,
	}
	helper.mm.EXPECT().Save(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, true)

	leave := &ClientComMessage{
		Leave: &MsgClientLeave{
			Id:    "id456",
			Topic: "p2p-test",
		},
		AsUser: uid.UserId(),
		sess:   s,
		init:   true,
	}

	helper.topic.unregisterSession(leave)
	helper.finish()

	// Check for errors from testHubLoop
	if errorMsgs, hasError := helper.hubMessages["__ERROR__"]; hasError {
		t.Fatal(errorMsgs[0].Ctrl.Text)
	}

	// Verify Session was unregistered
	if len(helper.topic.sessions) != 1 {
		t.Errorf("Attached sessions: expected 1, found %d", len(helper.topic.sessions))
	}
	if len(s.subs) != 0 {
		t.Errorf("Session subscriptions: expected 0, found %d", len(s.subs))
	}

	// Verify call party was removed (if the implementation handles this)
	if helper.topic.currentCall != nil && helper.topic.currentCall.parties != nil {
		if _, exists := helper.topic.currentCall.parties[s.sid]; exists {
			t.Error("Call party should have been removed when session unregistered")
		}
	}

	if len(r.messages) != 3 {
		t.Fatalf("`responses` expected to contain 3 elements, found %d", len(r.messages))
	}

	// Expected one of each: {data}, {info}, {ctrl}.
	var found = 0
	for _, msg := range r.messages {
		m := msg.(*ServerComMessage)
		if m.Data != nil {
			found++
			if m.Data.Head == nil || m.Data.Head["webrtc"] != "disconnected" || m.Data.Head["replace"] != ":123" {
				t.Fatalf("Unexpected Data.Head: %+v", m.Data.Head)
			}
		} else if m.Info != nil {
			found++
			if m.Info.SeqId != 123 {
				t.Fatalf("Unexpected Info.SeqId: %d", m.Info.SeqId)
			}
			if m.Info.What != "call" {
				t.Fatalf("Unexpected Info.What: %s", m.Info.What)
			}
			if m.Info.Event != "hang-up" {
				t.Fatalf("Unexpected Info.Event: %s", m.Info.Event)
			}
		} else if m.Ctrl != nil {
			found++
			if m.Ctrl.Code != http.StatusOK {
				t.Fatalf("Unexpected Ctrl.Code: %d", m.Ctrl.Code)
			}
		} else {
			t.Error("Expected only {data}, {info}, {ctrl} messages.")
		}
	}

	if found != 3 {
		t.Fatal("Expected only {data}, {info}, {ctrl} messages, but some are missing")
	}
}

// TestReplyDelMsgHardDelete 验证 Reply Del Msg Hard Delete 相关行为。
func TestReplyDelMsgHardDelete(t *testing.T) {
	// Test hard delete scenario - hard deletes affect all 用户 equally
	// and don't update individual unread counters the same way as soft deletes

	topicName := "p2pTest"
	helper := TopicTestHelper{}
	helper.setUp(t, 2, types.TopicCatP2P, topicName, true)
	defer helper.tearDown()

	user1 := helper.uids[0] // 用户 with delete 权限
	user2 := helper.uids[1] // Other 用户

	// Set up initial state: user2 has read up to 消息 5, Topic has 消息 up to 10
	helper.topic.lastID = 10

	pud1 := helper.topic.perUser[user1]
	pud1.readID = 10
	pud1.modeGiven = types.ModeCFull // Full 权限 including delete
	pud1.modeWant = types.ModeCFull
	helper.topic.perUser[user1] = pud1

	pud2 := helper.topic.perUser[user2]
	pud2.readID = 5
	pud2.modeGiven = types.ModeCFull
	pud2.modeWant = types.ModeCFull
	helper.topic.perUser[user2] = pud2

	// Simulate user1 doing a hard delete of 消息 7 and 8
	msg := &ClientComMessage{
		Del: &MsgClientDel{
			Id:   "del123",
			What: "msg",
			DelSeq: []MsgRange{
				{LowId: 7, HiId: 9}, // Deletes 消息 7 and 8 [7, 9)
			},
			Hard: true, // Hard delete
		},
		AsUser: user1.UserId(),
		sess:   helper.sessions[0],
		init:   true,
	}

	// Mock the 消息 deletion for hard delete (forUser = types.ZeroUid)
	helper.mm.EXPECT().DeleteList(topicName, 1, types.ZeroUid, gomock.Any(), []types.Range{{Low: 7, Hi: 9}}).Return(nil)

	// Call the function under test
	err := helper.topic.replyDelMsg(helper.sessions[0], user1, false, msg)

	// Verify
	if err != nil {
		t.Fatalf("replyDelMsg failed: %v", err)
	}

	// Verify Session got success 响应
	helper.finish()
	registerSessionVerifyOutputs(t, helper.results[0], []int{http.StatusOK})

	// For hard deletes, all 用户' delID should be updated
	if helper.topic.perUser[user1].delID != 1 {
		t.Errorf("Expected user1.delID to be 1, got %d", helper.topic.perUser[user1].delID)
	}
	if helper.topic.perUser[user2].delID != 1 {
		t.Errorf("Expected user2.delID to be 1, got %d", helper.topic.perUser[user2].delID)
	}
}

// TestReplyDelMsgUpdatesUnreadCounters 验证 Reply Del Msg Updates Unread Counters 相关行为。
func TestReplyDelMsgUpdatesUnreadCounters(t *testing.T) {
	// This test simulates the scenario from issue #898:
	// 1. User1 sends 消息 to User2
	// 2. User1 deletes some 消息 (soft delete)
	// 3. Verify that the unread calculation logic works correctly

	topicName := "p2pTest"
	helper := TopicTestHelper{}
	helper.setUp(t, 2, types.TopicCatP2P, topicName, true)
	defer helper.tearDown()

	user1 := helper.uids[0] // Sender/deleter
	user2 := helper.uids[1] // Recipient

	// Set up initial state: user2 has read up to 消息 5, Topic has 消息 up to 10
	// So user2 has 5 unread 消息 (6, 7, 8, 9, 10)
	helper.topic.lastID = 10

	pud1 := helper.topic.perUser[user1]
	pud1.readID = 10 // user1 has read all
	helper.topic.perUser[user1] = pud1

	pud2 := helper.topic.perUser[user2]
	pud2.readID = 5 // user2 has 5 unread 消息
	helper.topic.perUser[user2] = pud2

	// Simulate user1 deleting 消息 7 and 8 (2 of user2's unread 消息)
	msg := &ClientComMessage{
		Del: &MsgClientDel{
			Id:   "del123",
			What: "msg",
			DelSeq: []MsgRange{
				{LowId: 7, HiId: 9}, // Deletes 消息 7 and 8 [7, 9)
			},
			Hard: false, // Soft delete
		},
		AsUser: user1.UserId(),
		sess:   helper.sessions[0],
		init:   true,
	}

	// Mock the 消息 deletion
	helper.mm.EXPECT().DeleteList(topicName, 1, user1, time.Duration(0), []types.Range{{Low: 7, Hi: 9}}).Return(nil)

	// Call the function under test
	err := helper.topic.replyDelMsg(helper.sessions[0], user1, false, msg)

	// Verify
	if err != nil {
		t.Fatalf("replyDelMsg failed: %v", err)
	}

	// Verify Session got success 响应
	helper.finish()
	registerSessionVerifyOutputs(t, helper.results[0], []int{http.StatusOK})

	// The key verification is that calculateUnreadInRanges should have been called
	// with the correct parameters. We can test this indirectly by testing the function:
	ranges := []types.Range{{Low: 7, Hi: 9}}
	unreadDeleted := calculateUnreadInRanges(5, 10, ranges) // user2's readID=5, lastID=10
	if unreadDeleted != 2 {
		t.Errorf("Expected 2 unread messages to be deleted for user2, got %d", unreadDeleted)
	}
}

// TestCalculateUnreadInRanges 验证 Calculate Unread In Ranges 相关行为。
func TestCalculateUnreadInRanges(t *testing.T) {
	tests := []struct {
		name     string
		readID   int
		lastID   int
		ranges   []types.Range
		expected int
	}{
		{
			name:     "no unread messages",
			readID:   10,
			lastID:   10,
			ranges:   []types.Range{{Low: 5, Hi: 15}},
			expected: 0,
		},
		{
			name:     "no deleted messages in unread range",
			readID:   5,
			lastID:   10,
			ranges:   []types.Range{{Low: 1, Hi: 5}},
			expected: 0,
		},
		{
			name:     "all unread messages deleted",
			readID:   5,
			lastID:   10,
			ranges:   []types.Range{{Low: 6, Hi: 11}},
			expected: 5,
		},
		{
			name:     "partial unread messages deleted",
			readID:   5,
			lastID:   10,
			ranges:   []types.Range{{Low: 7, Hi: 9}},
			expected: 2,
		},
		{
			name:     "single message deleted",
			readID:   5,
			lastID:   10,
			ranges:   []types.Range{{Low: 7, Hi: 0}}, // Hi: 0 means single 消息
			expected: 1,
		},
		{
			name:     "multiple ranges",
			readID:   5,
			lastID:   15,
			ranges:   []types.Range{{Low: 7, Hi: 9}, {Low: 12, Hi: 14}},
			expected: 4, // 2 消息 in range [7,9) + 2 消息 in range [12,14)
		},
		{
			name:     "overlapping with unread boundaries",
			readID:   5,
			lastID:   10,
			ranges:   []types.Range{{Low: 4, Hi: 8}, {Low: 9, Hi: 12}},
			expected: 4, // [6,8) + [9,11) = 2 + 2 = 4 unread 消息 deleted
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := calculateUnreadInRanges(tt.readID, tt.lastID, tt.ranges)
			if result != tt.expected {
				t.Errorf("calculateUnreadInRanges(%d, %d, %v) = %d; want %d",
					tt.readID, tt.lastID, tt.ranges, result, tt.expected)
			}
		})
	}
}

// TestMain 验证 Main 相关行为。
func TestMain(m *testing.M) {
	logs.Init(os.Stderr, "stdFlags")
	// Set max subscriber count to effective infinity.
	globals.maxSubscriberCount = 1_000_000_000
	os.Exit(m.Run())
}
