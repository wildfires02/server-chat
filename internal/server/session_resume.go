package server

import (
	"strconv"

	"chat/server/auth"
	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
)

const (
	maxResumeTopics       = 8
	resumeCatchupPageSize = 100
	maxResumeCursor       = 1<<31 - 1
)

// resume 在一次请求中验证现有 Token、恢复 Session 身份，并重新订阅客户端声明的
// 少量实时 Topic。Token 仍由现有 HMAC 认证器校验，因此该流程天然支持跨节点恢复。
func (s *Session) resume(msg *ClientComMessage) {
	if versionCompare(s.ver, minProtobufWebSocketVersionValue) < 0 {
		statsInc("SessionResumeFailed", 1)
		s.queueOut(ErrVersionNotSupported(msg.Id, msg.Timestamp))
		return
	}
	if !s.uid.IsZero() {
		s.queueOut(ErrAlreadyAuthenticated(msg.Id, "", msg.Timestamp))
		return
	}
	if len(msg.Resume.Token) == 0 || len(msg.Resume.Topics) > maxResumeTopics {
		statsInc("SessionResumeFailed", 1)
		s.queueOut(ErrMalformedReply(msg, msg.Timestamp))
		return
	}
	if !validateResumeTopics(msg.Resume.Topics) {
		statsInc("SessionResumeFailed", 1)
		s.queueOut(ErrMalformedReply(msg, msg.Timestamp))
		return
	}

	handler := store.Store.GetLogicalAuthHandler("token")
	if handler == nil {
		statsInc("SessionResumeFailed", 1)
		s.queueOut(ErrAuthUnknownScheme(msg.Id, "", msg.Timestamp))
		return
	}
	rec, challenge, err := handler.Authenticate(msg.Resume.Token, s.remoteAddr)
	if err == nil && challenge != nil {
		err = types.ErrPermissionDenied
	}
	if err == nil && rec.Features&auth.FeatureNoLogin != 0 {
		err = types.ErrPermissionDenied
	}
	if err == nil && rec.State == types.StateUndefined {
		rec.State, err = userGetState(rec.Uid)
	}
	if err == nil && rec.State != types.StateOK {
		err = types.ErrPermissionDenied
	}
	if err != nil {
		statsInc("SessionResumeFailed", 1)
		logs.Warn.Println("s.resume: token validation failed", err, s.sid)
		s.queueOut(decodeStoreError(err, msg.Id, msg.Timestamp, nil))
		return
	}

	reply := s.onLogin(msg.Id, msg.Timestamp, rec, nil)
	params, ok := reply.Ctrl.Params.(map[string]any)
	if !ok {
		params = make(map[string]any)
		reply.Ctrl.Params = params
	}
	restored := s.resumeTopics(msg.Id, msg.Resume.Topics)

	params["resumed"] = true
	params["resumeTopics"] = len(msg.Resume.Topics)
	params["restoredTopics"] = restored
	params["resumeSeqLimit"] = resumeCatchupPageSize
	reply.Ctrl.Text = "resumed"
	s.queueOut(reply)
	statsInc("SessionResumeSucceeded", 1)
}

func validateResumeTopics(topics []MsgResumeTopic) bool {
	seen := make(map[string]struct{}, len(topics))
	for _, cursor := range topics {
		if cursor.Topic == "" || cursor.SeqId < 0 || cursor.SeqId > maxResumeCursor ||
			cursor.DelId < 0 || cursor.DelId > maxResumeCursor {
			return false
		}
		if _, duplicate := seen[cursor.Topic]; duplicate {
			return false
		}
		seen[cursor.Topic] = struct{}{}
	}
	return true
}

func (s *Session) resumeTopics(requestID string, cursors []MsgResumeTopic) []string {
	requests := make([]*ClientComMessage, 0, len(cursors))
	for index, cursor := range cursors {
		query := buildResumeGetQuery(cursor)
		if query == nil {
			continue
		}
		requests = append(requests, &ClientComMessage{Sub: &MsgClientSub{
			Id:    requestID + "-resume-" + strconv.Itoa(index),
			Topic: cursor.Topic,
			Get:   query,
		}})
	}
	if len(requests) == 0 {
		return nil
	}

	// Normal client mutations remain serialized with a capacity-one wait group.
	// Resume is processed synchronously by the socket read loop and contains only
	// independent subscriptions, so it can safely fan them out to their Topic
	// actors and join once before returning the atomic resume ACK.
	serialInflight := s.inflightReqs
	parallelInflight := newBoundedWaitGroup(len(requests))
	s.inflightReqs = parallelInflight
	defer func() { s.inflightReqs = serialInflight }()

	for _, request := range requests {
		s.dispatch(request)
	}
	parallelInflight.Wait()

	restored := make([]string, 0, len(requests))
	for _, request := range requests {
		if request.RcptTo != "" && s.getSub(request.RcptTo) != nil {
			restored = append(restored, request.Sub.Topic)
		}
	}
	return restored
}

func buildResumeGetQuery(cursor MsgResumeTopic) *MsgGetQuery {
	query := &MsgGetQuery{}
	if cursor.Topic == "me" {
		query.What = "desc sub"
		query.Desc = &MsgGetOpts{}
		query.Sub = &MsgGetOpts{}
	} else if cursor.Active {
		query.What = "desc sub data del aux"
		query.Desc = &MsgGetOpts{}
		query.Sub = &MsgGetOpts{}
		query.Data = &MsgGetOpts{Limit: resumeCatchupPageSize}
		if cursor.SeqId > 0 {
			query.Data.SinceId = cursor.SeqId + 1
			query.Data.Forward = true
		}
		query.Del = &MsgGetOpts{Limit: resumeCatchupPageSize}
		query.Del.Forward = true
		if cursor.DelId > 0 {
			query.Del.SinceId = cursor.DelId + 1
		}
	} else {
		// 非活动 Topic 只需要 Presence；消息缺口由 me Topic 的 seq/read 投影发现。
		query.What = "desc"
		query.Desc = &MsgGetOpts{}
	}

	if query.What == "" {
		return nil
	}
	return query
}
