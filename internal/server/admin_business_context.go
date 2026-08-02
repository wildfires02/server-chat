package server

import (
	"net/http"
	"strings"

	"chat/server/store"
	"chat/server/store/types"
)

type adminTopicMemberInput struct {
	Provider   string `json:"provider"`
	ExternalID string `json:"external_id"`
	Topic      string `json:"topic"`
}

// validateBusinessTopicMember 为资金服务校验官方或普通群的真实成员关系。
// 浏览器无法调用该接口，红包领取资格不能由消息正文或前端参数声明。
func (handler *adminHTTPHandler) validateBusinessTopicMember(
	wrt http.ResponseWriter,
	req *http.Request,
	requestID string,
) {
	var input adminTopicMemberInput
	if !handler.decode(wrt, req, &input, requestID) {
		return
	}
	input.Provider = strings.ToLower(strings.TrimSpace(input.Provider))
	input.ExternalID = strings.TrimSpace(input.ExternalID)
	input.Topic = strings.TrimSpace(input.Topic)
	if input.Provider == "" || input.ExternalID == "" || len(input.Topic) < 4 || len(input.Topic) > 160 {
		handler.writeError(wrt, http.StatusBadRequest, "invalid_business_context", requestID)
		return
	}
	uid, _, _, _, err := store.Users.GetAuthUniqueRecord(
		externalIdentityAuthScheme,
		externalIdentityUnique(input.Provider, input.ExternalID),
	)
	if err != nil || uid.IsZero() {
		handler.writeError(wrt, http.StatusNotFound, "identity_user_unavailable", requestID)
		return
	}
	topicName := input.Topic
	if strings.HasPrefix(topicName, "chn") {
		topicName = types.ChnToGrp(topicName)
	}
	if !strings.HasPrefix(topicName, "grp") {
		handler.writeError(wrt, http.StatusBadRequest, "invalid_business_context", requestID)
		return
	}
	topic, err := store.Topics.Get(topicName)
	if err != nil || topic == nil || topic.UseBt {
		handler.writeError(wrt, http.StatusNotFound, "topic_not_found", requestID)
		return
	}
	sub, err := store.Subs.Get(topicName, uid, false)
	if err != nil || sub == nil || !(sub.ModeWant & sub.ModeGiven).IsJoiner() {
		handler.writeError(wrt, http.StatusForbidden, "topic_membership_required", requestID)
		return
	}
	handler.writeData(wrt, http.StatusOK, map[string]any{
		"member": true,
		"topic":  topicName,
		"im_uid": uid.UserId(),
	}, requestID)
}
