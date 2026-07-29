// Package server 实现即时通信服务端的协议、路由和业务逻辑。
package server

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"chat/server/auth"
	"chat/server/auth/mock_auth"
	"chat/server/store"
	"chat/server/store/mock_store"
	"chat/server/store/types"
	"github.com/golang/mock/gomock"
)

// test_makeSession 完成testmake会话所需的内部处理。
func test_makeSession(uid types.Uid) *Session {
	return &Session{
		send:         make(chan any, 10),
		uid:          uid,
		authLvl:      auth.LevelAuth,
		inflightReqs: newBoundedWaitGroup(1),
		ver:          22,
	}
}

// TestDispatchHello 验证 Dispatch Hello 相关行为。
func TestDispatchHello(t *testing.T) {
	s := &Session{
		send:    make(chan any, 10),
		uid:     types.Uid(1),
		authLvl: auth.LevelAuth,
	}
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)
	msg := &ClientComMessage{
		Hi: &MsgClientHi{
			Id:        "123",
			Version:   "1",
			UserAgent: "test-ua",
			Lang:      "en-GB",
		},
	}
	s.dispatch(msg)
	close(s.send)
	wg.Wait()
	if len(r.messages) != 1 {
		t.Errorf("responses: expected 1, received %d.", len(r.messages))
	}
	resp := r.messages[0].(*ServerComMessage)
	if resp == nil {
		t.Fatal("Response must be ServerComMessage")
	}
	if resp.Ctrl != nil {
		if resp.Ctrl.Code != 201 {
			t.Errorf("Response code: expected 201, got %d", resp.Ctrl.Code)
		}
		if resp.Ctrl.Params == nil {
			t.Error("Response is expected to contain params dict.")
		}
	} else {
		t.Error("Response must contain a ctrl message.")
	}

	if s.lang != "en-GB" {
		t.Errorf("Session language expected to be 'en-GB' vs '%s'", s.lang)
	}
	if s.userAgent != "test-ua" {
		t.Errorf("Session UA expected to be 'test-ua' vs '%s'", s.userAgent)
	}
	if s.countryCode != "GB" {
		t.Errorf("Country code expected to be 'GB' vs '%s'", s.countryCode)
	}
	if s.ver == 0 {
		t.Errorf("s.ver expected 0 vs found %d", s.ver)
	}
}

// verifyResponseCodes 校验响应Codes的输入和约束。
func verifyResponseCodes(r *responses, codes []int, t *testing.T) {
	if len(r.messages) != len(codes) {
		t.Errorf("responses: expected %d, received %d.", len(codes), len(r.messages))
	}
	for i := range codes {
		resp := r.messages[i].(*ServerComMessage)
		if resp == nil {
			t.Fatalf("Response %d must be ServerComMessage", i)
		}
		if resp.Ctrl == nil {
			t.Fatalf("Response %d must contain a ctrl message.", i)
		}
		if resp.Ctrl.Code != codes[i] {
			t.Errorf("Response code: expected %d, got %d", codes[i], resp.Ctrl.Code)
		}
	}
}

// TestDispatchInvalidVersion 验证 Dispatch Invalid Version 相关行为。
func TestDispatchInvalidVersion(t *testing.T) {
	s := &Session{
		send:    make(chan any, 10),
		uid:     types.Uid(1),
		authLvl: auth.LevelAuth,
	}
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)
	msg := &ClientComMessage{
		Hi: &MsgClientHi{
			Id: "123",
			// Invalid version string.
			Version: "INVALID VERSION STRING",
		},
	}
	s.dispatch(msg)
	close(s.send)
	wg.Wait()
	verifyResponseCodes(&r, []int{http.StatusBadRequest}, t)
}

// TestDispatchUnsupportedVersion 验证 Dispatch Unsupported Version 相关行为。
func TestDispatchUnsupportedVersion(t *testing.T) {
	s := &Session{
		send:    make(chan any, 10),
		uid:     types.Uid(1),
		authLvl: auth.LevelAuth,
	}
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)
	msg := &ClientComMessage{
		Hi: &MsgClientHi{
			Id: "123",
			// Invalid version string.
			Version: "0.1",
		},
	}
	s.dispatch(msg)
	close(s.send)
	wg.Wait()
	verifyResponseCodes(&r, []int{http.StatusHTTPVersionNotSupported}, t)
}

// TestDispatchLogin 验证 Dispatch Login 相关行为。
func TestDispatchLogin(t *testing.T) {
	ctrl := gomock.NewController(t)
	ss := mock_store.NewMockPersistentStorageInterface(ctrl)
	aa := mock_auth.NewMockAuthHandler(ctrl)

	uid := types.Uid(1)
	store.Store = ss
	defer func() {
		store.Store = nil
		ctrl.Finish()
	}()

	secret := "<==auth-secret==>"
	authRec := &auth.Rec{
		Uid:       uid,
		AuthLevel: auth.LevelAuth,
		Tags:      []string{"tag1", "tag2"},
		State:     types.StateOK,
	}
	ss.EXPECT().GetLogicalAuthHandler("basic").Return(aa)
	aa.EXPECT().Authenticate([]byte(secret), gomock.Any()).Return(authRec, nil, nil)
	// Token generation.
	ss.EXPECT().GetLogicalAuthHandler("token").Return(aa)
	token := "<==auth-token==>"
	expires, _ := time.Parse(time.RFC822, "01 Jan 50 00:00 UTC")
	aa.EXPECT().GenSecret(authRec).Return([]byte(token), expires, nil)

	s := &Session{
		send:    make(chan any, 10),
		authLvl: auth.LevelAuth,
		ver:     16,
	}
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)

	msg := &ClientComMessage{
		Login: &MsgClientLogin{
			Id:     "123",
			Scheme: "basic",
			Secret: []byte(secret),
		},
	}

	s.dispatch(msg)
	close(s.send)
	wg.Wait()

	if len(r.messages) != 1 {
		t.Errorf("responses: expected 1, received %d.", len(r.messages))
	}
	resp := r.messages[0].(*ServerComMessage)
	if resp == nil {
		t.Fatal("Response must be ServerComMessage")
	}
	if resp.Ctrl != nil {
		if resp.Ctrl.Id != "123" {
			t.Errorf("Response id: expected '123', found '%s'", resp.Ctrl.Id)
		}
		if resp.Ctrl.Code != 200 {
			t.Errorf("Response code: expected 200, got %d", resp.Ctrl.Code)
		}
		if resp.Ctrl.Params == nil {
			t.Error("Response is expected to contain params dict.")
		}
		p := resp.Ctrl.Params.(map[string]any)
		if authToken := string(p["token"].([]byte)); authToken != token {
			t.Errorf("Auth token: expected '%s', found '%s'.", token, authToken)
		}
		if exp := p["expires"].(time.Time); exp != expires {
			t.Errorf("Token expiration: expected '%s', found '%s'.", expires, exp)
		}
	} else {
		t.Error("Response must contain a ctrl message.")
	}
}

// TestDispatchSubscribe 验证 Dispatch Subscribe 相关行为。
func TestDispatchSubscribe(t *testing.T) {
	uid := types.Uid(1)
	s := test_makeSession(uid)
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)

	hub := &Hub{
		join: make(chan *ClientComMessage, 10),
	}
	globals.hub = hub

	defer func() {
		globals.hub = nil
	}()

	msg := &ClientComMessage{
		Sub: &MsgClientSub{
			Id:    "123",
			Topic: "me",
			Get: &MsgGetQuery{
				What: "sub desc tags cred",
			},
		},
	}

	s.dispatch(msg)
	close(s.send)
	wg.Wait()

	// Check we've routed the join 请求 via the hub.
	if len(r.messages) != 0 {
		t.Errorf("responses: expected 0, received %d.", len(r.messages))
	}
	if len(hub.join) == 1 {
		join := <-hub.join
		if join.sess != s {
			t.Error("Hub.join request: sess field expected to be the session under test.")
		}
		if join != msg {
			t.Error("Hub.join request: subscribe message expected to be the original subscribe message.")
		}
	} else {
		t.Errorf("Hub join messages: expected 1, received %d.", len(hub.join))
	}
	s.inflightReqs.Done()
}

// TestDispatchAlreadySubscribed 验证 Dispatch Already Subscribed 相关行为。
func TestDispatchAlreadySubscribed(t *testing.T) {
	uid := types.Uid(1)
	s := test_makeSession(uid)
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)

	msg := &ClientComMessage{
		Sub: &MsgClientSub{
			Id:    "123",
			Topic: "me",
			Get: &MsgGetQuery{
				What: "sub desc tags cred",
			},
		},
	}
	// Pretend the Session's already subscribed to Topic 'me'.
	s.subs = make(map[string]*Subscription)
	s.subs[uid.UserId()] = &Subscription{}

	s.dispatch(msg)
	close(s.send)
	wg.Wait()

	verifyResponseCodes(&r, []int{http.StatusNotModified}, t)
}

// TestDispatchSubscribeJoinChannelFull 验证 Dispatch Subscribe Join Channel Full 相关行为。
func TestDispatchSubscribeJoinChannelFull(t *testing.T) {
	uid := types.Uid(1)
	s := test_makeSession(uid)
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)

	hub := &Hub{
		// Make it unbuffered with no readers - so emit operation fails immediately.
		join: make(chan *ClientComMessage),
	}
	globals.hub = hub

	defer func() {
		globals.hub = nil
	}()

	msg := &ClientComMessage{
		Sub: &MsgClientSub{
			Id:    "123",
			Topic: "me",
			Get: &MsgGetQuery{
				What: "sub desc tags cred",
			},
		},
	}

	s.dispatch(msg)
	close(s.send)
	wg.Wait()

	verifyResponseCodes(&r, []int{http.StatusServiceUnavailable}, t)
}

// TestDispatchLeave 验证 Dispatch Leave 相关行为。
func TestDispatchLeave(t *testing.T) {
	uid := types.Uid(1)
	s := test_makeSession(uid)
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)

	destUid := types.Uid(2)
	topicName := uid.P2PName(destUid)
	leave := make(chan *ClientComMessage, 1)
	s.subs = make(map[string]*Subscription)
	s.subs[topicName] = &Subscription{
		done: leave,
	}

	msg := &ClientComMessage{
		Leave: &MsgClientLeave{
			Id:    "123",
			Topic: destUid.UserId(),
		},
	}

	s.dispatch(msg)
	close(s.send)
	wg.Wait()

	// Check we've routed the join 请求 via the leave Channel.
	if len(r.messages) != 0 {
		t.Errorf("responses: expected 0, received %d.", len(r.messages))
	}
	if len(leave) == 1 {
		req := <-leave
		if req.sess != s {
			t.Error("Leave request: sess field expected to be the session under test.")
		}
		if req != msg {
			t.Error("Leave request: leave message expected to be the original leave message.")
		}
		// leave 请求 handler is expected to clean up subs.
		s.delSub(topicName)
	} else {
		t.Errorf("Unsub messages: expected 1, received %d.", len(leave))
	}
	if len(s.subs) != 0 {
		t.Errorf("Session subs: expected to be empty, actual size: %d", len(s.subs))
	}
	s.inflightReqs.Done()
}

// TestDispatchLeaveUnsubMe 验证 Dispatch Leave Unsub Me 相关行为。
func TestDispatchLeaveUnsubMe(t *testing.T) {
	uid := types.Uid(1)
	s := test_makeSession(uid)
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)

	s.subs = make(map[string]*Subscription)
	s.subs[uid.UserId()] = &Subscription{}

	msg := &ClientComMessage{
		Leave: &MsgClientLeave{
			Id: "123",
			// Cannot unsubscribe from 'me'.
			Topic: "me",
			Unsub: true,
		},
	}

	s.dispatch(msg)
	close(s.send)
	wg.Wait()

	verifyResponseCodes(&r, []int{http.StatusForbidden}, t)
}

// TestDispatchLeaveUnknownTopic 验证 Dispatch Leave Unknown Topic 相关行为。
func TestDispatchLeaveUnknownTopic(t *testing.T) {
	uid := types.Uid(1)
	s := test_makeSession(uid)
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)

	// Session isn't subscribed to Topic 'me'.
	// And wants to leave it => no change.
	s.subs = make(map[string]*Subscription)

	msg := &ClientComMessage{
		Leave: &MsgClientLeave{
			Id:    "123",
			Topic: "me",
		},
	}

	s.dispatch(msg)
	close(s.send)
	wg.Wait()

	verifyResponseCodes(&r, []int{http.StatusNotModified}, t)
}

// TestDispatchLeaveUnsubFromUnknownTopic 验证 Dispatch Leave Unsub From Unknown Topic 相关行为。
func TestDispatchLeaveUnsubFromUnknownTopic(t *testing.T) {
	uid := types.Uid(1)
	s := test_makeSession(uid)
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)

	// Session isn't subscribed to Topic 'me'.
	// And wants to leave & unsubscribe from it.
	s.subs = make(map[string]*Subscription)

	msg := &ClientComMessage{
		Leave: &MsgClientLeave{
			Id:    "123",
			Topic: "me",
			Unsub: true,
		},
	}

	s.dispatch(msg)
	close(s.send)
	wg.Wait()

	verifyResponseCodes(&r, []int{http.StatusConflict}, t)
}

// TestDispatchPublish 验证 Dispatch Publish 相关行为。
func TestDispatchPublish(t *testing.T) {
	uid := types.Uid(1)
	s := test_makeSession(uid)
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)

	destUid := types.Uid(2)
	topicName := uid.P2PName(destUid)

	brdcst := make(chan *ClientComMessage, 1)
	s.subs = make(map[string]*Subscription)
	s.subs[topicName] = &Subscription{
		broadcast: brdcst,
	}

	testMessage := "test content"
	msg := &ClientComMessage{
		Pub: &MsgClientPub{
			Id:      "123",
			Topic:   destUid.UserId(),
			Content: testMessage,
		},
	}

	s.dispatch(msg)
	close(s.send)
	wg.Wait()

	// Check we've routed the join 请求 via the broadcast Channel.
	if len(r.messages) != 0 {
		t.Errorf("responses: expected 0, received %d.", len(r.messages))
	}
	if len(brdcst) == 1 {
		req := <-brdcst
		if req.sess != s {
			t.Error("Pub request: sess field expected to be the session under test.")
		}
		if req.Pub.Content != testMessage {
			t.Errorf("Pub request content: expected '%s' vs '%s'.", testMessage, req.Pub.Content)
		}
		if req.Pub.Topic != destUid.UserId() {
			t.Errorf("Pub request topic: expected '%s' vs '%s'.", destUid.UserId(), req.Pub.Topic)
		}
	} else {
		t.Errorf("Pub messages: expected 1, received %d.", len(brdcst))
	}
}

// TestDispatchPublishBroadcastChannelFull 验证 Dispatch Publish Broadcast Channel Full 相关行为。
func TestDispatchPublishBroadcastChannelFull(t *testing.T) {
	uid := types.Uid(1)
	s := test_makeSession(uid)
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)

	destUid := types.Uid(2)
	topicName := uid.P2PName(destUid)

	// Make broadcast Channel unbuffered with no reader -
	// emit op will fail.
	brdcst := make(chan *ClientComMessage)
	s.subs = make(map[string]*Subscription)
	s.subs[topicName] = &Subscription{
		broadcast: brdcst,
	}

	testMessage := "test content"
	msg := &ClientComMessage{
		Pub: &MsgClientPub{
			Id:      "123",
			Topic:   destUid.UserId(),
			Content: testMessage,
		},
	}

	s.dispatch(msg)
	close(s.send)
	wg.Wait()

	verifyResponseCodes(&r, []int{http.StatusServiceUnavailable}, t)
}

// TestDispatchPublishMissingSubcription 验证 Dispatch Publish Missing Subcription 相关行为。
func TestDispatchPublishMissingSubcription(t *testing.T) {
	uid := types.Uid(1)
	s := test_makeSession(uid)
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)

	destUid := types.Uid(2)

	// 订阅 to Topic missing.
	s.subs = make(map[string]*Subscription)

	testMessage := "test content"
	msg := &ClientComMessage{
		Pub: &MsgClientPub{
			Id:      "123",
			Topic:   destUid.UserId(),
			Content: testMessage,
		},
	}

	s.dispatch(msg)
	close(s.send)
	wg.Wait()

	verifyResponseCodes(&r, []int{http.StatusConflict}, t)
}

// TestDispatchGet 验证 Dispatch Get 相关行为。
func TestDispatchGet(t *testing.T) {
	uid := types.Uid(1)
	s := test_makeSession(uid)
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)

	destUid := types.Uid(2)
	topicName := uid.P2PName(destUid)

	meta := make(chan *ClientComMessage, 1)
	s.subs = make(map[string]*Subscription)
	s.subs[topicName] = &Subscription{
		meta: meta,
	}

	msg := &ClientComMessage{
		Get: &MsgClientGet{
			Id:    "123",
			Topic: destUid.UserId(),
			MsgGetQuery: MsgGetQuery{
				What: "desc sub del cred",
			},
		},
	}

	s.dispatch(msg)
	close(s.send)
	wg.Wait()

	// Check we've routed the join 请求 via the meta Channel.
	if len(r.messages) != 0 {
		t.Errorf("responses: expected 0, received %d.", len(r.messages))
	}
	if len(meta) == 1 {
		req := <-meta
		if req.sess != s {
			t.Error("Get request: sess field expected to be the session under test.")
		}
	} else {
		t.Errorf("Get messages: expected 1, received %d.", len(meta))
	}
}

// TestDispatchGetMalformedWhat 验证 Dispatch Get Malformed What 相关行为。
func TestDispatchGetMalformedWhat(t *testing.T) {
	uid := types.Uid(1)
	s := test_makeSession(uid)
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)

	destUid := types.Uid(2)
	msg := &ClientComMessage{
		Get: &MsgClientGet{
			Id:    "123",
			Topic: destUid.UserId(),
			MsgGetQuery: MsgGetQuery{
				// Empty 'what'. This will produce an 错误.
				What: "",
			},
		},
	}

	s.dispatch(msg)
	close(s.send)
	wg.Wait()

	verifyResponseCodes(&r, []int{http.StatusBadRequest}, t)
}

// TestDispatchGetMetaChannelFull 验证 Dispatch Get Meta Channel Full 相关行为。
func TestDispatchGetMetaChannelFull(t *testing.T) {
	uid := types.Uid(1)
	s := test_makeSession(uid)
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)

	destUid := types.Uid(2)
	topicName := uid.P2PName(destUid)

	// Unbuffered chan with no readers - emit will fail.
	meta := make(chan *ClientComMessage)
	s.subs = make(map[string]*Subscription)
	s.subs[topicName] = &Subscription{
		meta: meta,
	}

	msg := &ClientComMessage{
		Get: &MsgClientGet{
			Id:    "123",
			Topic: destUid.UserId(),
			MsgGetQuery: MsgGetQuery{
				What: "desc sub",
			},
		},
	}

	s.dispatch(msg)
	close(s.send)
	wg.Wait()

	verifyResponseCodes(&r, []int{http.StatusServiceUnavailable}, t)
}

// TestDispatchSet 验证 Dispatch Set 相关行为。
func TestDispatchSet(t *testing.T) {
	uid := types.Uid(1)
	s := test_makeSession(uid)
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)

	destUid := types.Uid(2)
	topicName := uid.P2PName(destUid)

	meta := make(chan *ClientComMessage, 1)
	s.subs = make(map[string]*Subscription)
	s.subs[topicName] = &Subscription{
		meta: meta,
	}

	msg := &ClientComMessage{
		Set: &MsgClientSet{
			Id:    "123",
			Topic: destUid.UserId(),
			MsgSetQuery: MsgSetQuery{
				Desc: &MsgSetDesc{},
				Sub:  &MsgSetSub{},
				Tags: []string{"abc"},
				Cred: &MsgCredClient{},
			},
		},
	}

	s.dispatch(msg)
	close(s.send)
	wg.Wait()

	// Check we've routed the join 请求 via the meta Channel.
	if len(r.messages) != 0 {
		t.Errorf("responses: expected 0, received %d.", len(r.messages))
	}
	if len(meta) == 1 {
		req := <-meta
		if req.sess != s {
			t.Error("Set request: sess field expected to be the session under test.")
		}
		expectedWhat := constMsgMetaDesc | constMsgMetaSub | constMsgMetaTags | constMsgMetaCred
		if msg.MetaWhat != expectedWhat {
			t.Errorf("Set request what: expected %d vs %d", expectedWhat, msg.MetaWhat)
		}
	} else {
		t.Errorf("Set messages: expected 1, received %d.", len(meta))
	}
}

// TestDispatchSetMalformedWhat 验证 Dispatch Set Malformed What 相关行为。
func TestDispatchSetMalformedWhat(t *testing.T) {
	uid := types.Uid(1)
	s := test_makeSession(uid)
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)

	destUid := types.Uid(2)
	msg := &ClientComMessage{
		Set: &MsgClientSet{
			Id:          "123",
			Topic:       destUid.UserId(),
			MsgSetQuery: MsgSetQuery{
				// No meta requests.
			},
		},
	}

	s.dispatch(msg)
	close(s.send)
	wg.Wait()

	verifyResponseCodes(&r, []int{http.StatusBadRequest}, t)
}

// TestDispatchSetMetaChannelFull 验证 Dispatch Set Meta Channel Full 相关行为。
func TestDispatchSetMetaChannelFull(t *testing.T) {
	uid := types.Uid(1)
	s := test_makeSession(uid)
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)

	destUid := types.Uid(2)
	topicName := uid.P2PName(destUid)

	// Unbuffered meta Channel w/ no readers - emit will fail.
	meta := make(chan *ClientComMessage)
	s.subs = make(map[string]*Subscription)
	s.subs[topicName] = &Subscription{
		meta: meta,
	}

	msg := &ClientComMessage{
		Set: &MsgClientSet{
			Id:    "123",
			Topic: destUid.UserId(),
			MsgSetQuery: MsgSetQuery{
				// No meta requests.
				Desc: &MsgSetDesc{},
				Sub:  &MsgSetSub{},
				Tags: []string{"abc"},
				Cred: &MsgCredClient{},
			},
		},
	}

	s.dispatch(msg)
	close(s.send)
	wg.Wait()

	verifyResponseCodes(&r, []int{http.StatusServiceUnavailable}, t)
}

// TestDispatchDelMsg 验证 Dispatch Del Msg 相关行为。
func TestDispatchDelMsg(t *testing.T) {
	uid := types.Uid(1)
	s := test_makeSession(uid)
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)

	destUid := types.Uid(2)
	topicName := uid.P2PName(destUid)

	meta := make(chan *ClientComMessage, 1)
	s.subs = make(map[string]*Subscription)
	s.subs[topicName] = &Subscription{
		meta: meta,
	}

	msg := &ClientComMessage{
		Del: &MsgClientDel{
			Id:     "123",
			Topic:  destUid.UserId(),
			What:   "msg",
			DelSeq: []MsgRange{{LowId: 3, HiId: 4}},
			Hard:   true,
		},
	}

	s.dispatch(msg)
	close(s.send)
	wg.Wait()

	// Check we've routed the join 请求 via the meta Channel.
	if len(r.messages) != 0 {
		t.Errorf("responses: expected 0, received %d.", len(r.messages))
	}
	if len(meta) == 1 {
		req := <-meta
		if req.sess != s {
			t.Error("Del request: sess field expected to be the session under test.")
		}
	} else {
		t.Errorf("Del messages: expected 1, received %d.", len(meta))
	}
}

// TestDispatchDelMalformedWhat 验证 Dispatch Del Malformed What 相关行为。
func TestDispatchDelMalformedWhat(t *testing.T) {
	uid := types.Uid(1)
	s := test_makeSession(uid)
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)

	destUid := types.Uid(2)
	msg := &ClientComMessage{
		Del: &MsgClientDel{
			Id:    "123",
			Topic: destUid.UserId(),
			// Invalid 'what' - this will produce an 错误.
			What: "INVALID",
		},
	}

	s.dispatch(msg)
	close(s.send)
	wg.Wait()

	verifyResponseCodes(&r, []int{http.StatusBadRequest}, t)
}

// TestDispatchDelMetaChanFull 验证 Dispatch Del Meta Chan Full 相关行为。
func TestDispatchDelMetaChanFull(t *testing.T) {
	uid := types.Uid(1)
	s := test_makeSession(uid)
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)

	destUid := types.Uid(2)
	topicName := uid.P2PName(destUid)

	// Unbuffered chan - to simulate a full buffered chan.
	meta := make(chan *ClientComMessage)
	s.subs = make(map[string]*Subscription)
	s.subs[topicName] = &Subscription{
		meta: meta,
	}

	msg := &ClientComMessage{
		Del: &MsgClientDel{
			Id:     "123",
			Topic:  destUid.UserId(),
			What:   "msg",
			DelSeq: []MsgRange{{LowId: 3, HiId: 4}},
			Hard:   true,
		},
	}

	s.dispatch(msg)
	close(s.send)
	wg.Wait()

	verifyResponseCodes(&r, []int{http.StatusServiceUnavailable}, t)
}

// TestDispatchDelUnsubscribedSession 验证 Dispatch Del Unsubscribed Session 相关行为。
func TestDispatchDelUnsubscribedSession(t *testing.T) {
	uid := types.Uid(1)
	s := test_makeSession(uid)
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)

	destUid := types.Uid(2)
	// Session isn't subscribed.
	s.subs = make(map[string]*Subscription)
	msg := &ClientComMessage{
		Del: &MsgClientDel{
			Id:     "123",
			Topic:  destUid.UserId(),
			What:   "msg",
			DelSeq: []MsgRange{{LowId: 3, HiId: 4}},
			Hard:   true,
		},
	}

	s.dispatch(msg)
	close(s.send)
	wg.Wait()

	verifyResponseCodes(&r, []int{http.StatusConflict}, t)
}

// TestDispatchNote 验证 Dispatch Note 相关行为。
func TestDispatchNote(t *testing.T) {
	uid := types.Uid(1)
	s := test_makeSession(uid)
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)

	destUid := types.Uid(2)
	topicName := uid.P2PName(destUid)

	brdcst := make(chan *ClientComMessage, 1)
	s.subs = make(map[string]*Subscription)
	s.subs[topicName] = &Subscription{
		broadcast: brdcst,
	}

	msg := &ClientComMessage{
		Note: &MsgClientNote{
			Topic: destUid.UserId(),
			What:  "recv",
			SeqId: 5,
		},
	}

	s.dispatch(msg)
	close(s.send)
	wg.Wait()

	// Check we've routed the join 请求 via the broadcast Channel.
	if len(r.messages) != 0 {
		t.Errorf("responses: expected 0, received %d.", len(r.messages))
	}
	if len(brdcst) == 1 {
		req := <-brdcst
		if req.sess != s {
			t.Error("Pub request: sess field expected to be the session under test.")
		}
		if req.Note.What != msg.Note.What {
			t.Errorf("Note request what: expected '%s' vs '%s'.", msg.Note.What, req.Note.What)
		}
		if req.Note.SeqId != msg.Note.SeqId {
			t.Errorf("Note request seqId: expected %d vs %d.", msg.Note.SeqId, req.Note.SeqId)
		}
		if req.Note.Topic != destUid.UserId() {
			t.Errorf("Note request topic: expected '%s' vs '%s'.", destUid.UserId(), req.Note.Topic)
		}
	} else {
		t.Errorf("Note messages: expected 1, received %d.", len(brdcst))
	}
}

// TestDispatchNoteBroadcastChanFull 验证 Dispatch Note Broadcast Chan Full 相关行为。
func TestDispatchNoteBroadcastChanFull(t *testing.T) {
	uid := types.Uid(1)
	s := test_makeSession(uid)
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)

	destUid := types.Uid(2)
	topicName := uid.P2PName(destUid)

	// Unbuffered chan - to simulate a full buffered chan.
	brdcst := make(chan *ClientComMessage)
	s.subs = make(map[string]*Subscription)
	s.subs[topicName] = &Subscription{
		broadcast: brdcst,
	}

	msg := &ClientComMessage{
		Note: &MsgClientNote{
			Topic: destUid.UserId(),
			What:  "recv",
			SeqId: 5,
		},
	}

	s.dispatch(msg)
	close(s.send)
	wg.Wait()

	verifyResponseCodes(&r, []int{http.StatusServiceUnavailable}, t)
}

// TestDispatchNoteOnNonSubscribedTopic 验证 Dispatch Note On Non Subscribed Topic 相关行为。
func TestDispatchNoteOnNonSubscribedTopic(t *testing.T) {
	uid := types.Uid(1)
	s := test_makeSession(uid)
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)

	destUid := types.Uid(2)
	s.subs = make(map[string]*Subscription)

	msg := &ClientComMessage{
		Note: &MsgClientNote{
			Topic: destUid.UserId(),
			What:  "read",
			SeqId: 5,
		},
	}

	s.dispatch(msg)
	close(s.send)
	wg.Wait()

	verifyResponseCodes(&r, []int{http.StatusConflict}, t)
}

// TestDispatchAccNew 验证 Dispatch Acc New 相关行为。
func TestDispatchAccNew(t *testing.T) {
	ctrl := gomock.NewController(t)
	ss := mock_store.NewMockPersistentStorageInterface(ctrl)
	uu := mock_store.NewMockUsersPersistenceInterface(ctrl)
	aa := mock_auth.NewMockAuthHandler(ctrl)

	uid := types.Uid(1)
	store.Store = ss
	store.Users = uu
	defer func() {
		store.Store = nil
		store.Users = nil
		ctrl.Finish()
	}()

	remoteAddr := "192.168.0.1"
	secret := "<==auth-secret==>"
	tags := []string{"tag1", "tag2"}
	authRec := &auth.Rec{
		Uid:       uid,
		AuthLevel: auth.LevelAuth,
		Tags:      tags,
		State:     types.StateOK,
	}
	ss.EXPECT().GetLogicalAuthHandler("basic").Return(aa)
	// This login is available.
	aa.EXPECT().IsUnique([]byte(secret), remoteAddr).Return(true, nil)
	uu.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
		func(user *types.User, private any) (*types.User, error) {
			user.SetUid(uid)
			return user, nil
		})
	aa.EXPECT().AddRecord(gomock.Any(), []byte(secret), remoteAddr).Return(authRec, nil)

	// Token generation.
	ss.EXPECT().GetLogicalAuthHandler("token").Return(aa)
	token := "<==auth-token==>"
	aa.EXPECT().GenSecret(gomock.Any()).Return([]byte(token), time.Now(), nil)
	uu.EXPECT().UpdateTags(uid, tags, nil, nil).Return(tags, nil)

	s := &Session{
		send:       make(chan any, 10),
		authLvl:    auth.LevelAuth,
		ver:        16,
		remoteAddr: remoteAddr,
	}
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)

	public := "public name"
	msg := &ClientComMessage{
		Acc: &MsgClientAcc{
			Id:     "123",
			User:   "newXYZ",
			Scheme: "basic",
			Secret: []byte(secret),
			Tags:   []string{"abc", "123"},
			Desc:   &MsgSetDesc{Public: public},
		},
	}

	s.dispatch(msg)
	close(s.send)
	wg.Wait()

	if len(r.messages) != 1 {
		t.Errorf("responses: expected 1, received %d.", len(r.messages))
	}
	resp := r.messages[0].(*ServerComMessage)
	if resp == nil {
		t.Fatal("Response must be ServerComMessage")
	}
	if resp.Ctrl != nil {
		if resp.Ctrl.Id != "123" {
			t.Errorf("Response id: expected '123', found '%s'", resp.Ctrl.Id)
		}
		if resp.Ctrl.Code != 201 {
			t.Errorf("Response code: expected 201, got %d", resp.Ctrl.Code)
		}
		if resp.Ctrl.Params == nil {
			t.Error("Response is expected to contain params dict.")
		}
		p := resp.Ctrl.Params.(map[string]any)
		if respUid := string(p["user"].(string)); respUid != uid.UserId() {
			t.Errorf("Response uid: expected '%s', found '%s'.", uid.UserId(), respUid)
		}
		if lvl := p["authlvl"].(string); lvl != auth.LevelAuth.String() {
			t.Errorf("Auth level: expected '%s', found '%s'.", auth.LevelAuth.String(), lvl)
		}
		if desc := p["desc"].(*MsgTopicDesc); desc.Public.(string) != public {
			t.Errorf("Public: expected '%s', found '%s'.", public, desc.Public.(string))
		}
	} else {
		t.Error("Response must contain a ctrl message.")
	}
}

// TestDispatchNoMessage 验证 Dispatch No Message 相关行为。
func TestDispatchNoMessage(t *testing.T) {
	remoteAddr := "192.168.0.1"
	s := &Session{
		send:       make(chan any, 10),
		authLvl:    auth.LevelAuth,
		ver:        16,
		remoteAddr: remoteAddr,
	}
	wg := sync.WaitGroup{}
	r := responses{}
	wg.Add(1)
	go s.testWriteLoop(&r, &wg)

	msg := &ClientComMessage{}

	s.dispatch(msg)
	close(s.send)
	wg.Wait()

	if len(r.messages) != 1 {
		t.Errorf("responses: expected 1, received %d.", len(r.messages))
	}
	resp := r.messages[0].(*ServerComMessage)
	if resp == nil {
		t.Fatal("Response must be ServerComMessage")
	}
	if resp.Ctrl == nil {
		t.Fatal("Response must contain a ctrl message.")
	}
	if resp.Ctrl.Code != 400 {
		t.Errorf("Response code: expected 400, got %d", resp.Ctrl.Code)
	}
}
