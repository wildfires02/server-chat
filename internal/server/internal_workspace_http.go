package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	admincontrol "chat/server/admin"
	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
)

const internalWorkspaceMaxBodySize = 16 << 10

var internalWorkspaceOrgPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

type internalWorkspaceHTTPHandler struct {
	basePath       string
	allowedOrigins map[string]struct{}
	control        *admincontrol.ControlPlane
}

type internalWorkspaceHTTPResponse struct {
	Data      any    `json:"data,omitempty"`
	Error     string `json:"error,omitempty"`
	RequestID string `json:"request_id"`
}

type internalWorkspacePinWrite struct {
	Rank            int64   `json:"rank"`
	ExpectedVersion *uint64 `json:"expected_version"`
}

func registerInternalWorkspaceHTTPRoutes(mux *http.ServeMux, apiPath string,
	config adminAPIConfig, control *admincontrol.ControlPlane) {
	basePath := apiPath + "v0/internal/"
	origins := make(map[string]struct{}, len(config.AllowedOrigins))
	for _, origin := range config.AllowedOrigins {
		if origin = strings.TrimRight(strings.TrimSpace(origin), "/"); origin != "" {
			origins[origin] = struct{}{}
		}
	}
	mux.Handle(basePath, &internalWorkspaceHTTPHandler{
		basePath: basePath, allowedOrigins: origins, control: control,
	})
	logs.Info.Printf("Internal workspace API served from '%s'", basePath)
}

func (handler *internalWorkspaceHTTPHandler) ServeHTTP(wrt http.ResponseWriter, req *http.Request) {
	requestID := strings.TrimSpace(req.Header.Get("X-Request-ID"))
	if requestID == "" || len(requestID) > 128 {
		requestID = fmt.Sprintf("workspace-%d", time.Now().UnixNano())
	}
	wrt.Header().Set("Cache-Control", "no-store")
	wrt.Header().Set("Content-Type", "application/json; charset=utf-8")
	wrt.Header().Set("X-Content-Type-Options", "nosniff")
	wrt.Header().Set("X-Request-ID", requestID)
	if !handler.applyCORS(wrt, req) {
		handler.writeError(wrt, http.StatusForbidden, "origin_not_allowed", requestID)
		return
	}
	if req.Method == http.MethodOptions {
		wrt.WriteHeader(http.StatusNoContent)
		return
	}

	uid, org, ok := handler.authorize(wrt, req, requestID)
	if !ok {
		return
	}
	resource := strings.Trim(strings.TrimPrefix(req.URL.Path, handler.basePath), "/")
	switch {
	case resource == "workspace" && req.Method == http.MethodGet:
		handler.sync(wrt, req, requestID, org, uid)
	case strings.HasPrefix(resource, "pins/") &&
		(req.Method == http.MethodPut || req.Method == http.MethodDelete):
		handler.mutate(wrt, req, requestID, org, uid, strings.TrimPrefix(resource, "pins/"))
	default:
		handler.writeError(wrt, http.StatusNotFound, "internal_workspace_route_not_found", requestID)
	}
}

func (handler *internalWorkspaceHTTPHandler) applyCORS(wrt http.ResponseWriter, req *http.Request) bool {
	origin := strings.TrimRight(strings.TrimSpace(req.Header.Get("Origin")), "/")
	if origin == "" {
		return true
	}
	if _, ok := handler.allowedOrigins[origin]; !ok {
		return false
	}
	wrt.Header().Set("Access-Control-Allow-Origin", origin)
	wrt.Header().Set("Access-Control-Allow-Headers",
		"Authorization, Content-Type, If-Match, X-IM-APIKey, X-IM-Auth, X-IM-Org, X-Request-ID")
	wrt.Header().Set("Access-Control-Allow-Methods", "GET, PUT, DELETE, OPTIONS")
	wrt.Header().Add("Vary", "Origin")
	return true
}

func (handler *internalWorkspaceHTTPHandler) authorize(wrt http.ResponseWriter, req *http.Request,
	requestID string) (types.Uid, string, bool) {
	if valid, _ := checkAPIKey(getAPIKey(req)); !valid {
		wrt.Header().Set("WWW-Authenticate", `IM realm="internal-workspace"`)
		handler.writeError(wrt, http.StatusUnauthorized, "invalid_api_key", requestID)
		return types.ZeroUid, "", false
	}
	method, secret := getHttpAuth(req)
	uid, challenge, err := authFileRequest(method, secret, req.URL.Query().Get("sid"), getRemoteAddr(req))
	if err != nil || uid.IsZero() || challenge != nil {
		wrt.Header().Set("WWW-Authenticate", `IM realm="internal-workspace"`)
		handler.writeError(wrt, http.StatusUnauthorized, "employee_auth_required", requestID)
		return types.ZeroUid, "", false
	}
	org := strings.TrimSpace(req.Header.Get("X-IM-Org"))
	if !internalWorkspaceOrgPattern.MatchString(org) {
		handler.writeError(wrt, http.StatusBadRequest, "invalid_organization", requestID)
		return types.ZeroUid, "", false
	}
	permission := "workspace.pins.read"
	if req.Method != http.MethodGet {
		permission = "workspace.pins.write"
	}
	if handler.control == nil {
		handler.writeError(wrt, http.StatusServiceUnavailable, "employee_policy_unavailable", requestID)
		return types.ZeroUid, "", false
	}
	evaluation, err := handler.control.Evaluate(
		"im:"+uid.UserId(), "channel:"+org, permission, time.Now().UTC())
	if err != nil {
		logs.Warn.Printf("internal workspace authorization failed: %v", err)
		handler.writeError(wrt, http.StatusInternalServerError, "employee_policy_failed", requestID)
		return types.ZeroUid, "", false
	}
	if !evaluation.Allowed {
		handler.writeError(wrt, http.StatusForbidden, "employee_permission_required", requestID)
		return types.ZeroUid, "", false
	}
	return uid, org, true
}

func (handler *internalWorkspaceHTTPHandler) sync(wrt http.ResponseWriter, req *http.Request,
	requestID, org string, uid types.Uid) {
	since, err := parseInternalWorkspaceUint(req.URL.Query().Get("since"), 0)
	if err != nil {
		handler.writeError(wrt, http.StatusBadRequest, "invalid_since", requestID)
		return
	}
	limit64, err := parseInternalWorkspaceUint(req.URL.Query().Get("limit"), 0)
	if err != nil || limit64 > uint64(^uint(0)>>1) {
		handler.writeError(wrt, http.StatusBadRequest, "invalid_limit", requestID)
		return
	}
	snapshot, err := store.InternalPins.Query(org, uid, types.InternalPinQuery{
		Since: since, Limit: int(limit64),
	})
	if err != nil {
		handler.writeStoreError(wrt, err, requestID)
		return
	}
	handler.writeData(wrt, http.StatusOK, snapshot, requestID)
}

func (handler *internalWorkspaceHTTPHandler) mutate(wrt http.ResponseWriter, req *http.Request,
	requestID, org string, uid types.Uid, resource string) {
	mutation, err := parseInternalWorkspaceTarget(uid, resource)
	if err != nil {
		handler.writeError(wrt, http.StatusBadRequest, "invalid_pin_target", requestID)
		return
	}
	mutation.Actor = uid.UserId()
	mutation.RequestID = requestID
	if req.Method == http.MethodPut {
		var input internalWorkspacePinWrite
		reader := http.MaxBytesReader(wrt, req.Body, internalWorkspaceMaxBodySize)
		decoder := json.NewDecoder(reader)
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&input); err != nil || input.ExpectedVersion == nil {
			handler.writeError(wrt, http.StatusBadRequest, "invalid_pin_payload", requestID)
			return
		}
		mutation.Op = types.InternalPinUpsert
		mutation.Rank = input.Rank
		mutation.ExpectedVersion = *input.ExpectedVersion
		if err = authorizeInternalWorkspaceTarget(uid, mutation); err != nil {
			handler.writeStoreError(wrt, err, requestID)
			return
		}
	} else {
		expected, ok := parseInternalWorkspaceIfMatch(req.Header.Get("If-Match"))
		if !ok {
			handler.writeError(wrt, http.StatusPreconditionRequired, "if_match_required", requestID)
			return
		}
		mutation.Op = types.InternalPinDelete
		mutation.ExpectedVersion = expected
	}

	pin, changed, err := store.InternalPins.Apply(org, uid, mutation)
	if err != nil {
		handler.writeStoreError(wrt, err, requestID)
		return
	}
	if changed {
		notifyInternalWorkspaceChanged(uid)
	}
	handler.writeData(wrt, http.StatusOK, pin, requestID)
}

func parseInternalWorkspaceTarget(actor types.Uid, resource string) (types.InternalPinMutation, error) {
	parts := strings.Split(resource, "/")
	for idx := range parts {
		unescaped, err := url.PathUnescape(parts[idx])
		if err != nil {
			return types.InternalPinMutation{}, types.ErrMalformed
		}
		parts[idx] = strings.TrimSpace(unescaped)
	}
	if len(parts) == 2 && parts[0] == string(types.InternalPinCustomer) {
		customer := types.ParseUserId(parts[1])
		if customer.IsZero() || customer == actor {
			return types.InternalPinMutation{}, types.ErrMalformed
		}
		return types.InternalPinMutation{
			Kind: types.InternalPinCustomer, CustomerUID: customer.UserId(),
		}, nil
	}
	if len(parts) == 2 && parts[0] == string(types.InternalPinConversation) {
		topic, err := canonicalInternalWorkspaceTopic(actor, parts[1])
		if err != nil {
			return types.InternalPinMutation{}, err
		}
		return types.InternalPinMutation{Kind: types.InternalPinConversation, Topic: topic}, nil
	}
	if len(parts) == 3 && parts[0] == string(types.InternalPinMessage) {
		topic, err := canonicalInternalWorkspaceTopic(actor, parts[1])
		if err != nil {
			return types.InternalPinMutation{}, err
		}
		seqID, err := strconv.Atoi(parts[2])
		if err != nil || seqID <= 0 {
			return types.InternalPinMutation{}, types.ErrMalformed
		}
		return types.InternalPinMutation{Kind: types.InternalPinMessage, Topic: topic, SeqID: seqID}, nil
	}
	return types.InternalPinMutation{}, types.ErrMalformed
}

func canonicalInternalWorkspaceTopic(actor types.Uid, topic string) (string, error) {
	if peer := types.ParseUserId(topic); !peer.IsZero() && peer != actor {
		return actor.P2PName(peer), nil
	}
	if strings.HasPrefix(topic, "chn") {
		topic = types.ChnToGrp(topic)
	}
	if len(topic) < 4 || len(topic) > 160 ||
		(!strings.HasPrefix(topic, "grp") && !strings.HasPrefix(topic, "p2p")) {
		return "", types.ErrMalformed
	}
	return topic, nil
}

func authorizeInternalWorkspaceTarget(actor types.Uid, mutation types.InternalPinMutation) error {
	switch mutation.Kind {
	case types.InternalPinCustomer:
		user, err := store.Users.Get(types.ParseUserId(mutation.CustomerUID))
		if err != nil {
			return err
		}
		if user == nil || user.State != types.StateOK {
			return types.ErrUserNotFound
		}
	case types.InternalPinConversation, types.InternalPinMessage:
		sub, err := store.Subs.Get(mutation.Topic, actor, false)
		if err != nil {
			return err
		}
		if sub == nil || !(sub.ModeWant & sub.ModeGiven).IsReader() {
			return types.ErrPermissionDenied
		}
		if mutation.Kind == types.InternalPinMessage {
			message, err := store.Messages.Get(mutation.Topic, mutation.SeqID)
			if err != nil {
				return err
			}
			if message == nil {
				return types.ErrNotFound
			}
		}
	}
	return nil
}

func parseInternalWorkspaceUint(raw string, fallback uint64) (uint64, error) {
	if raw == "" {
		return fallback, nil
	}
	return strconv.ParseUint(raw, 10, 64)
}

func parseInternalWorkspaceIfMatch(raw string) (uint64, bool) {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "W/"))
	if strings.HasPrefix(raw, `"`) || strings.HasSuffix(raw, `"`) {
		if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
			return 0, false
		}
		raw = raw[1 : len(raw)-1]
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	return value, err == nil
}

func notifyInternalWorkspaceChanged(uid types.Uid) {
	if globals.hub == nil {
		return
	}
	message := &ServerComMessage{
		Pres:   &MsgServerPres{Topic: "me", What: "workspace"},
		RcptTo: uid.UserId(),
	}
	select {
	case globals.hub.routeSrv <- message:
	default:
		logs.Warn.Printf("internal workspace notification queue is full for %s", uid.UserId())
	}
}

func (handler *internalWorkspaceHTTPHandler) writeStoreError(wrt http.ResponseWriter, err error,
	requestID string) {
	switch {
	case errors.Is(err, types.ErrMalformed):
		handler.writeError(wrt, http.StatusBadRequest, "malformed", requestID)
	case errors.Is(err, types.ErrPermissionDenied):
		handler.writeError(wrt, http.StatusForbidden, "target_permission_denied", requestID)
	case errors.Is(err, types.ErrUserNotFound), errors.Is(err, types.ErrTopicNotFound),
		errors.Is(err, types.ErrNotFound):
		handler.writeError(wrt, http.StatusNotFound, "target_not_found", requestID)
	case errors.Is(err, types.ErrVersionConflict):
		handler.writeError(wrt, http.StatusConflict, "pin_version_conflict", requestID)
	case errors.Is(err, types.ErrPolicy):
		handler.writeError(wrt, http.StatusUnprocessableEntity, "pin_policy_rejected", requestID)
	default:
		logs.Warn.Printf("internal workspace request failed: %v", err)
		handler.writeError(wrt, http.StatusInternalServerError, "internal_workspace_failed", requestID)
	}
}

func (handler *internalWorkspaceHTTPHandler) writeData(wrt http.ResponseWriter, status int,
	data any, requestID string) {
	wrt.WriteHeader(status)
	if err := json.NewEncoder(wrt).Encode(internalWorkspaceHTTPResponse{
		Data: data, RequestID: requestID,
	}); err != nil {
		logs.Warn.Printf("failed to encode internal workspace response: %v", err)
	}
}

func (handler *internalWorkspaceHTTPHandler) writeError(wrt http.ResponseWriter, status int,
	code, requestID string) {
	wrt.WriteHeader(status)
	if err := json.NewEncoder(wrt).Encode(internalWorkspaceHTTPResponse{
		Error: code, RequestID: requestID,
	}); err != nil {
		logs.Warn.Printf("failed to encode internal workspace error: %v", err)
	}
}
