// Package main 实现即时通信服务端的协议、路由和业务逻辑。
package main

import (
	"net/http"
	"time"
)

/****************************************************************
 * 服务端下行控制响应消息 {ctrl} 的生成器函数集合。
 ****************************************************************/

// 2xx 成功响应类型 (Success Responses)

// NoErr 表示操作成功执行 (200 OK)。
func NoErr(id, topic string, ts time.Time) *ServerComMessage {
	return NoErrParams(id, topic, ts, nil)
}

// NoErrExplicitTs 表示携带明确的服务端与请求时间戳的操作成功响应 (200 OK)。
func NoErrExplicitTs(id, topic string, serverTs, incomingReqTs time.Time) *ServerComMessage {
	return NoErrParamsExplicitTs(id, topic, serverTs, incomingReqTs, nil)
}

// NoErrReply 作为对客户端请求的回复，表示操作成功执行 (200 OK)。
func NoErrReply(msg *ClientComMessage, ts time.Time) *ServerComMessage {
	return NoErrExplicitTs(msg.Id, msg.Original, ts, msg.Timestamp)
}

// NoErrParams 表示携带附加参数的操作成功响应 (200 OK)。
func NoErrParams(id, topic string, ts time.Time, params any) *ServerComMessage {
	return NoErrParamsExplicitTs(id, topic, ts, ts, params)
}

// NoErrParamsExplicitTs 表示携带附加参数及明确时间戳的操作成功响应 (200 OK)。
func NoErrParamsExplicitTs(id, topic string, serverTs, incomingReqTs time.Time, params any) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusOK, // 200
			Text:      "ok",
			Topic:     topic,
			Params:    params,
			Timestamp: serverTs,
		},
		Id:        id,
		Timestamp: incomingReqTs,
	}
}

// NoErrParamsReply 作为对客户端请求的回复，表示携带附加参数的操作成功响应 (200 OK)。
func NoErrParamsReply(msg *ClientComMessage, ts time.Time, params any) *ServerComMessage {
	return NoErrParamsExplicitTs(msg.Id, msg.Original, ts, msg.Timestamp, params)
}

// NoErrCreated 表示对象（如用户、Topic 等）成功创建 (201 Created)。
func NoErrCreated(id, topic string, ts time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusCreated, // 201
			Text:      "created",
			Topic:     topic,
			Timestamp: ts,
		},
		Id:        id,
		Timestamp: ts,
	}
}

// NoErrAccepted 表示请求已被接受但尚未处理完毕 (202 Accepted)。
func NoErrAccepted(id, topic string, ts time.Time) *ServerComMessage {
	return NoErrAcceptedExplicitTs(id, topic, ts, ts)
}

// NoErrAcceptedExplicitTs 表示请求已被接受但尚未处理完毕（携带明确时间戳）(202 Accepted)。
func NoErrAcceptedExplicitTs(id, topic string, serverTs, incomingReqTs time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusAccepted, // 202
			Text:      "accepted",
			Topic:     topic,
			Timestamp: serverTs,
		}, Id: id,
		Timestamp: incomingReqTs,
	}
}

// NoContentParams 表示请求已处理但无返回内容 (204 No Content)。
func NoContentParams(id, topic string, serverTs, incomingReqTs time.Time, params any) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusNoContent, // 204
			Text:      "no content",
			Topic:     topic,
			Params:    params,
			Timestamp: serverTs,
		},
		Id:        id,
		Timestamp: incomingReqTs,
	}
}

// NoContentParamsReply 作为对客户端请求的回复，表示无返回内容 (204 No Content)。
func NoContentParamsReply(msg *ClientComMessage, ts time.Time, params any) *ServerComMessage {
	return NoContentParams(msg.Id, msg.Original, ts, msg.Timestamp, params)
}

// NoErrEvicted 表示用户非因自身过错而被断开 Topic 关联 (205 Reset Content)。
func NoErrEvicted(id, topic string, ts time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusResetContent, // 205
			Text:      "evicted",
			Topic:     topic,
			Timestamp: ts,
		}, Id: id,
	}
}

// NoErrShutdown 表示因系统正在关机而断开连接 (205 Reset Content)。
func NoErrShutdown(ts time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Code:      http.StatusResetContent, // 205
			Text:      "server shutdown",
			Timestamp: ts,
		},
	}
}

// NoErrDeliveredParams 表示请求的数据内容已被成功送达 (208 Already Reported)。
func NoErrDeliveredParams(id, topic string, ts time.Time, params any) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusAlreadyReported, // 208
			Text:      "delivered",
			Topic:     topic,
			Params:    params,
			Timestamp: ts,
		},
		Id: id,
	}
}

// 3xx 重定向与信息提示类响应 (Redirection / Information Responses)

// InfoValidateCredentials 要求用户在继续操作前验证账号凭证 (300 Multiple Choices)。
func InfoValidateCredentials(id string, ts time.Time) *ServerComMessage {
	return InfoValidateCredentialsExplicitTs(id, ts, ts)
}

// InfoValidateCredentialsExplicitTs 要求用户在继续操作前验证凭证（携带明确时间戳）(300)。
func InfoValidateCredentialsExplicitTs(id string, serverTs, incomingReqTs time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusMultipleChoices, // 300
			Text:      "validate credentials",
			Timestamp: serverTs,
		},
		Id:        id,
		Timestamp: incomingReqTs,
	}
}

// InfoChallenge 要求用户在完成登录前回应挑战凭证 (300 Multiple Choices)。
func InfoChallenge(id string, ts time.Time, challenge []byte) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusMultipleChoices, // 300
			Text:      "challenge",
			Params:    map[string]any{"challenge": challenge},
			Timestamp: ts,
		},
		Id:        id,
		Timestamp: ts,
	}
}

// InfoAuthReset 响应密码/密钥重置请求（当已重置但未执行自动登录时发送）(301 Moved Permanently)。
func InfoAuthReset(id string, ts time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusMovedPermanently, // 301
			Text:      "auth reset",
			Timestamp: ts,
		},
		Id:        id,
		Timestamp: ts,
	}
}

// InfoUseOther 响应订阅请求，重定向客户端至另一个 Topic (303 See Other)。
func InfoUseOther(id, topic, other string, serverTs, incomingReqTs time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusSeeOther, // 303
			Text:      "use other",
			Topic:     topic,
			Params:    map[string]string{"topic": other},
			Timestamp: serverTs,
		},
		Id:        id,
		Timestamp: incomingReqTs,
	}
}

// InfoUseOtherReply 响应订阅请求，重定向客户端至另一个 Topic (303)。
func InfoUseOtherReply(msg *ClientComMessage, other string, ts time.Time) *ServerComMessage {
	return InfoUseOther(msg.Id, msg.Original, other, ts, msg.Timestamp)
}

// InfoAlreadySubscribed 表示用户已处于订阅状态，订阅请求被忽略 (304 Not Modified)。
func InfoAlreadySubscribed(id, topic string, ts time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusNotModified, // 304
			Text:      "already subscribed",
			Topic:     topic,
			Timestamp: ts,
		},
		Id: id, Timestamp: ts,
	}
}

// InfoNotJoined 表示用户本未订阅该 Topic，离开请求被忽略 (304 Not Modified)。
func InfoNotJoined(id, topic string, ts time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusNotModified, // 304
			Text:      "not joined",
			Topic:     topic,
			Timestamp: ts,
		},
		Id:        id,
		Timestamp: ts,
	}
}

// InfoNoAction 表示目标已处于期望状态，请求被忽略 (304 Not Modified)。
func InfoNoAction(id, topic string, serverTs, incomingReqTs time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusNotModified, // 304
			Text:      "no action",
			Topic:     topic,
			Timestamp: serverTs,
		},
		Id:        id,
		Timestamp: incomingReqTs,
	}
}

// InfoNoActionReply 响应客户端请求，表示目标已处于期望状态 (304)。
func InfoNoActionReply(msg *ClientComMessage, ts time.Time) *ServerComMessage {
	return InfoNoAction(msg.Id, msg.Original, ts, msg.Timestamp)
}

// InfoNotModified 表示更新请求未产生任何变动 (304 Not Modified)。
func InfoNotModified(id, topic string, ts time.Time) *ServerComMessage {
	return InfoNotModifiedExplicitTs(id, topic, ts, ts)
}

// InfoNotModifiedReply 响应客户端更新请求，表示未产生任何变动 (304)。
func InfoNotModifiedReply(msg *ClientComMessage, ts time.Time) *ServerComMessage {
	return InfoNotModifiedExplicitTs(msg.Id, msg.Original, ts, msg.Timestamp)
}

// InfoNotModifiedExplicitTs 携带明确时间戳，表示更新请求未产生任何变动 (304)。
func InfoNotModifiedExplicitTs(id, topic string, serverTs, incomingReqTs time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusNotModified, // 304
			Text:      "not modified",
			Topic:     topic,
			Timestamp: serverTs,
		},
		Id:        id,
		Timestamp: incomingReqTs,
	}
}

// InfoFound 重定向至新资源 (307 Temporary Redirect)。
func InfoFound(id, topic string, ts time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusTemporaryRedirect, // 307
			Text:      "found",
			Topic:     topic,
			Timestamp: ts,
		},
		Id:        id,
		Timestamp: ts,
	}
}

// 4xx 客户端错误响应 (Client Error Responses)

// ErrMalformed 表示客户端请求格式错误 (400 Bad Request)。
func ErrMalformed(id, topic string, ts time.Time) *ServerComMessage {
	return ErrMalformedExplicitTs(id, topic, ts, ts)
}

// ErrMalformedReply 响应客户端请求，表示请求格式错误 (400 Bad Request)。
func ErrMalformedReply(msg *ClientComMessage, ts time.Time) *ServerComMessage {
	return ErrMalformedExplicitTs(msg.Id, msg.Original, ts, msg.Timestamp)
}

// ErrMalformedExplicitTs 携带明确时间戳，表示请求格式错误 (400)。
func ErrMalformedExplicitTs(id, topic string, serverTs, incomingReqTs time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusBadRequest, // 400
			Text:      "malformed",
			Topic:     topic,
			Timestamp: serverTs,
		},
		Id:        id,
		Timestamp: incomingReqTs,
	}
}

// ErrAuthRequired 表示必须先进行身份验证 (401 Unauthorized)。
func ErrAuthRequired(id, topic string, serverTs, incomingReqTs time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusUnauthorized, // 401
			Text:      "authentication required",
			Topic:     topic,
			Timestamp: serverTs,
		},
		Id:        id,
		Timestamp: incomingReqTs,
	}
}

// ErrAuthRequiredReply 响应客户端请求，表示必须先进行身份验证 (401)。
func ErrAuthRequiredReply(msg *ClientComMessage, ts time.Time) *ServerComMessage {
	return ErrAuthRequired(msg.Id, msg.Original, ts, msg.Timestamp)
}

// ErrAuthFailed 表示身份验证失败（密钥错误等）(401 Unauthorized)。
func ErrAuthFailed(id, topic string, serverTs, incomingReqTs time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusUnauthorized, // 401
			Text:      "authentication failed",
			Topic:     topic,
			Timestamp: serverTs,
		},
		Id:        id,
		Timestamp: incomingReqTs,
	}
}

// ErrAuthUnknownScheme 表示无法识别的身份认证方案 (401 Unauthorized)。
func ErrAuthUnknownScheme(id, topic string, ts time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusUnauthorized, // 401
			Text:      "unknown authentication scheme",
			Topic:     topic,
			Timestamp: ts,
		},
		Id:        id,
		Timestamp: ts,
	}
}

// ErrPermissionDenied 表示已验证身份但无权限执行当前操作 (403 Forbidden)。
func ErrPermissionDenied(id, topic string, ts time.Time) *ServerComMessage {
	return ErrPermissionDeniedExplicitTs(id, topic, ts, ts)
}

// ErrPermissionDeniedExplicitTs 携带明确时间戳，表示无权限执行操作 (403)。
func ErrPermissionDeniedExplicitTs(id, topic string, serverTs, incomingReqTs time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusForbidden, // 403
			Text:      "permission denied",
			Topic:     topic,
			Timestamp: serverTs,
		},
		Id:        id,
		Timestamp: incomingReqTs,
	}
}

// ErrPermissionDeniedReply 响应客户端请求，表示无权限执行操作 (403)。
func ErrPermissionDeniedReply(msg *ClientComMessage, ts time.Time) *ServerComMessage {
	return ErrPermissionDeniedExplicitTs(msg.Id, msg.Original, ts, msg.Timestamp)
}

// ErrAPIKeyRequired 表示请求需要有效的 API Key (403 Forbidden)。
func ErrAPIKeyRequired(ts time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Code:      http.StatusForbidden,
			Text:      "valid API key required",
			Timestamp: ts,
		},
	}
}

// ErrSessionNotFound 表示无效或已过期的会话 (403 Forbidden)。
func ErrSessionNotFound(ts time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Code:      http.StatusForbidden,
			Text:      "invalid or expired session",
			Timestamp: ts,
		},
	}
}

// ErrTopicNotFound 表示请求的目标 Topic 不存在 (404 Not Found)。
func ErrTopicNotFound(id, topic string, serverTs, incomingReqTs time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusNotFound, // 404
			Text:      "topic not found",
			Topic:     topic,
			Timestamp: serverTs,
		},
		Id:        id,
		Timestamp: incomingReqTs,
	}
}

// ErrTopicNotFoundReply 响应客户端请求，表示目标 Topic 不存在 (404)。
func ErrTopicNotFoundReply(msg *ClientComMessage, ts time.Time) *ServerComMessage {
	return ErrTopicNotFound(msg.Id, msg.Original, ts, msg.Timestamp)
}

// ErrUserNotFound 表示请求的目标用户不存在 (404 Not Found)。
func ErrUserNotFound(id, topic string, serverTs, incomingReqTs time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusNotFound, // 404
			Text:      "user not found",
			Topic:     topic,
			Timestamp: serverTs,
		},
		Id:        id,
		Timestamp: incomingReqTs,
	}
}

// ErrUserNotFoundReply 响应客户端请求，表示目标用户不存在 (404)。
func ErrUserNotFoundReply(msg *ClientComMessage, ts time.Time) *ServerComMessage {
	return ErrUserNotFound(msg.Id, msg.Original, ts, msg.Timestamp)
}

// ErrNotFound 表示除用户与 Topic 以外的其他对象未找到 (404 Not Found)。
func ErrNotFound(id, topic string, ts time.Time) *ServerComMessage {
	return ErrNotFoundExplicitTs(id, topic, ts, ts)
}

// ErrNotFoundExplicitTs 携带明确时间戳，表示对象未找到 (404)。
func ErrNotFoundExplicitTs(id, topic string, serverTs, incomingReqTs time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusNotFound, // 404
			Text:      "not found",
			Topic:     topic,
			Timestamp: serverTs,
		},
		Id:        id,
		Timestamp: incomingReqTs,
	}
}

// ErrNotFoundReply 响应客户端请求，表示对象未找到 (404)。
func ErrNotFoundReply(msg *ClientComMessage, ts time.Time) *ServerComMessage {
	return ErrNotFoundExplicitTs(msg.Id, msg.Original, ts, msg.Timestamp)
}

// ErrOperationNotAllowed 表示在当前上下文中不允许此有效操作 (405 Method Not Allowed)。
func ErrOperationNotAllowed(id, topic string, ts time.Time) *ServerComMessage {
	return ErrOperationNotAllowedExplicitTs(id, topic, ts, ts)
}

// ErrOperationNotAllowedExplicitTs 携带明确时间戳，表示操作受限 (405)。
func ErrOperationNotAllowedExplicitTs(id, topic string, serverTs, incomingReqTs time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusMethodNotAllowed, // 405
			Text:      "operation or method not allowed",
			Topic:     topic,
			Timestamp: serverTs,
		},
		Id:        id,
		Timestamp: incomingReqTs,
	}
}

// ErrOperationNotAllowedReply 响应客户端请求，表示操作受限 (405)。
func ErrOperationNotAllowedReply(msg *ClientComMessage, ts time.Time) *ServerComMessage {
	return ErrOperationNotAllowedExplicitTs(msg.Id, msg.Original, ts, msg.Timestamp)
}

// ErrInvalidResponse 表示客户端的响应结果无效 (406 Not Acceptable)。
func ErrInvalidResponse(id, topic string, serverTs, incomingReqTs time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusNotAcceptable, // 406
			Text:      "invalid response",
			Topic:     topic,
			Timestamp: serverTs,
		},
		Id:        id,
		Timestamp: incomingReqTs,
	}
}

// ErrDisconnected 表示客户端断开连接或超时未能及时传输数据 (408 Request Timeout)。
func ErrDisconnected(id, topic string, ts time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusRequestTimeout, // 408
			Text:      "disconnected",
			Topic:     topic,
			Timestamp: ts,
		},
		Id:        id,
		Timestamp: ts,
	}
}

// ErrAlreadyAuthenticated 表示会话已完成认证，不允许在当前 Session 中中途切换用户 (409 Conflict)。
func ErrAlreadyAuthenticated(id, topic string, ts time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusConflict, // 409
			Text:      "already authenticated",
			Topic:     topic,
			Timestamp: ts,
		},
		Id:        id,
		Timestamp: ts,
	}
}

// ErrDuplicateCredential 表示尝试创建重复的账号凭证 (409 Conflict)。
func ErrDuplicateCredential(id, topic string, serverTs, incomingReqTs time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusConflict, // 409
			Text:      "duplicate credential",
			Topic:     topic,
			Timestamp: serverTs,
		},
		Id:        id,
		Timestamp: incomingReqTs,
	}
}

// ErrAttachFirst 响应客户端消息，要求必须先订阅/加入 Topic 才能执行后续操作 (409 Conflict)。
func ErrAttachFirst(msg *ClientComMessage, ts time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        msg.Id,
			Code:      http.StatusConflict, // 409
			Text:      "must attach first",
			Topic:     msg.Original,
			Timestamp: ts,
		},
		Id:        msg.Id,
		Timestamp: msg.Timestamp,
	}
}

// ErrAlreadyExists 表示尝试创建的目标对象已存在 (409 Conflict)。
func ErrAlreadyExists(id, topic string, ts time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusConflict, // 409
			Text:      "already exists",
			Topic:     topic,
			Timestamp: ts,
		},
		Id:        id,
		Timestamp: ts,
	}
}

// ErrCommandOutOfSequence 表示指令发送顺序错误（如未发送 {hi} 握手即发送 {sub}）(409 Conflict)。
func ErrCommandOutOfSequence(id, unused string, ts time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusConflict, // 409
			Text:      "command out of sequence",
			Timestamp: ts,
		},
		Id:        id,
		Timestamp: ts,
	}
}

// ErrGone 表示 Topic 已被物理删除或用户已被封禁 (410 Gone)。
func ErrGone(id, topic string, ts time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusGone, // 410
			Text:      "gone",
			Topic:     topic,
			Timestamp: ts,
		},
		Id:        id,
		Timestamp: ts,
	}
}

// ErrTooLarge 表示数据包或请求体大小超出系统上限 (413 Request Entity Too Large)。
func ErrTooLarge(id, topic string, ts time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusRequestEntityTooLarge, // 413
			Text:      "too large",
			Topic:     topic,
			Timestamp: ts,
		},
		Id:        id,
		Timestamp: ts,
	}
}

// ErrPolicy 表示请求违反了系统安全策略（如密码太弱、订阅者超限等）(422 Unprocessable Entity)。
func ErrPolicy(id, topic string, ts time.Time) *ServerComMessage {
	return ErrPolicyExplicitTs(id, topic, ts, ts)
}

// ErrPolicyExplicitTs 携带明确时间戳，表示请求违反安全策略 (422)。
func ErrPolicyExplicitTs(id, topic string, serverTs, incomingReqTs time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusUnprocessableEntity, // 422
			Text:      "policy violation",
			Topic:     topic,
			Timestamp: serverTs,
		},
		Id:        id,
		Timestamp: incomingReqTs,
	}
}

// ErrPolicyReply 响应客户端请求，表示请求违反安全策略 (422)。
func ErrPolicyReply(msg *ClientComMessage, ts time.Time) *ServerComMessage {
	return ErrPolicyExplicitTs(msg.Id, msg.Original, ts, msg.Timestamp)
}

// ErrCallBusyExplicitTs 响应音视频呼叫请求，表示目标用户正忙 (486 Busy Here)。
func ErrCallBusyExplicitTs(id, topic string, serverTs, incomingReqTs time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      486, // Busy here.
			Text:      "busy here",
			Topic:     topic,
			Timestamp: serverTs,
		},
		Id:        id,
		Timestamp: incomingReqTs,
	}
}

// ErrCallBusyReply 响应音视频呼叫请求，表示目标用户正忙 (486)。
func ErrCallBusyReply(msg *ClientComMessage, ts time.Time) *ServerComMessage {
	return ErrCallBusyExplicitTs(msg.Id, msg.Original, ts, msg.Timestamp)
}

// 5xx 服务端错误响应 (Server Error Responses)

// ErrUnknown 表示数据库或其他内部未知错误 (500 Internal Server Error)。
func ErrUnknown(id, topic string, ts time.Time) *ServerComMessage {
	return ErrUnknownExplicitTs(id, topic, ts, ts)
}

// ErrUnknownExplicitTs 携带明确时间戳，表示服务端内部未知错误 (500)。
func ErrUnknownExplicitTs(id, topic string, serverTs, incomingReqTs time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusInternalServerError, // 500
			Text:      "internal error",
			Topic:     topic,
			Timestamp: serverTs,
		},
		Id:        id,
		Timestamp: incomingReqTs,
	}
}

// ErrUnknownReply 响应客户端请求，表示服务端内部未知错误 (500)。
func ErrUnknownReply(msg *ClientComMessage, ts time.Time) *ServerComMessage {
	return ErrUnknownExplicitTs(msg.Id, msg.Original, ts, msg.Timestamp)
}

// ErrNotImplemented 表示请求的功能尚未实现 (501 Not Implemented)。
func ErrNotImplemented(id, topic string, serverTs, incomingReqTs time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusNotImplemented, // 501
			Text:      "not implemented",
			Topic:     topic,
			Timestamp: serverTs,
		},
		Id:        id,
		Timestamp: incomingReqTs,
	}
}

// ErrNotImplementedReply 响应客户端请求，表示请求功能未实现 (501)。
func ErrNotImplementedReply(msg *ClientComMessage, ts time.Time) *ServerComMessage {
	return ErrNotImplemented(msg.Id, msg.Original, ts, msg.Timestamp)
}

// ErrClusterUnreachableReply 响应客户端请求，表示集群内部节点网络不可达 (502 Bad Gateway)。
func ErrClusterUnreachableReply(msg *ClientComMessage, ts time.Time) *ServerComMessage {
	return ErrClusterUnreachableExplicitTs(msg.Id, msg.Original, ts, msg.Timestamp)
}

// ErrClusterUnreachableExplicitTs 携带明确时间戳，表示集群内部节点不可达 (502)。
func ErrClusterUnreachableExplicitTs(id, topic string, serverTs, incomingReqTs time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusBadGateway, // 502
			Text:      "cluster unreachable",
			Topic:     topic,
			Timestamp: serverTs,
		},
		Id:        id,
		Timestamp: incomingReqTs,
	}
}

// ErrServiceUnavailableReply 响应客户端请求，表示服务器超载或服务暂不可用 (503 Service Unavailable)。
func ErrServiceUnavailableReply(msg *ClientComMessage, ts time.Time) *ServerComMessage {
	return ErrServiceUnavailableExplicitTs(msg.Id, msg.Original, ts, msg.Timestamp)
}

// ErrServiceUnavailableExplicitTs 携带明确时间戳，表示服务器超载或服务暂不可用 (503)。
func ErrServiceUnavailableExplicitTs(id, topic string, serverTs, incomingReqTs time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusServiceUnavailable, // 503
			Text:      "service unavailable",
			Topic:     topic,
			Timestamp: serverTs,
		},
		Id:        id,
		Timestamp: incomingReqTs,
	}
}

// ErrLocked 表示目标 Topic 正在被注销或物理删除，拒绝进一步操作 (503 Service Unavailable)。
func ErrLocked(id, topic string, ts time.Time) *ServerComMessage {
	return ErrLockedExplicitTs(id, topic, ts, ts)
}

// ErrLockedReply 响应客户端请求，表示目标 Topic 处于冻结/删除流程中 (503)。
func ErrLockedReply(msg *ClientComMessage, ts time.Time) *ServerComMessage {
	return ErrLockedExplicitTs(msg.Id, msg.Original, ts, msg.Timestamp)
}

// ErrLockedExplicitTs 携带明确时间戳，表示目标 Topic 正在被删除 (503)。
func ErrLockedExplicitTs(id, topic string, serverTs, incomingReqTs time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusServiceUnavailable, // 503
			Text:      "locked",
			Topic:     topic,
			Timestamp: serverTs,
		},
		Id:        id,
		Timestamp: incomingReqTs,
	}
}

// ErrVersionNotSupported 表示客户端使用的协议版本号过低或不受支持 (505 HTTP Version Not Supported)。
func ErrVersionNotSupported(id string, ts time.Time) *ServerComMessage {
	return &ServerComMessage{
		Ctrl: &MsgServerCtrl{
			Id:        id,
			Code:      http.StatusHTTPVersionNotSupported, // 505
			Text:      "version not supported",
			Timestamp: ts,
		},
		Id:        id,
		Timestamp: ts,
	}
}
