package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	admincontrol "chat/server/admin"
	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
	translation "chat/server/translate"
)

const (
	adminDocumentKey = "admin:control-plane:v1"
	adminMaxBodySize = 1 << 20
)

type persistentAdminRepository struct{}

func (persistentAdminRepository) Load() (*admincontrol.Document, error) {
	raw, err := store.PCache.Get(adminDocumentKey)
	if errors.Is(err, types.ErrNotFound) {
		return nil, admincontrol.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var document admincontrol.Document
	if err = json.Unmarshal([]byte(raw), &document); err != nil {
		return nil, err
	}
	return &document, nil
}

func (persistentAdminRepository) Save(document *admincontrol.Document) error {
	raw, err := json.Marshal(document)
	if err != nil {
		return err
	}
	return store.PCache.Upsert(adminDocumentKey, string(raw), false)
}

type adminRuntimeView struct {
	Environment             string `json:"environment"`
	DeploymentMode          string `json:"deployment_mode"`
	MaxMessageSize          int    `json:"max_message_size"`
	MaxSubscriberCount      int    `json:"max_subscriber_count"`
	MaxFileUploadSize       int64  `json:"max_file_upload_size"`
	MessageDeleteAgeSeconds int    `json:"message_delete_age_seconds"`
	P2PDeleteEnabled        bool   `json:"p2p_delete_enabled"`
	MediaProcessingEnabled  bool   `json:"media_processing_enabled"`
	PolicyProvider          string `json:"policy_provider"`
}

type adminIntegrationView struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Phase  string `json:"phase"`
	Detail string `json:"detail"`
}

type adminBootstrapView struct {
	ControlPlane admincontrol.Snapshot  `json:"control_plane"`
	Runtime      adminRuntimeView       `json:"runtime"`
	Integrations []adminIntegrationView `json:"integrations"`
}

type adminHTTPResponse struct {
	Data      any    `json:"data,omitempty"`
	Error     string `json:"error,omitempty"`
	RequestID string `json:"request_id"`
}

type adminHTTPHandler struct {
	basePath       string
	token          string
	allowedOrigins map[string]struct{}
	control        *admincontrol.ControlPlane
	official       *officialTopicManager
	runtime        adminRuntimeView
}

func registerAdminHTTPRoutes(mux *http.ServeMux, apiPath string, config configType) {
	if config.Admin == nil || !config.Admin.Enabled {
		logs.Info.Println("Admin API is disabled")
		return
	}
	control, err := admincontrol.NewControlPlane(persistentAdminRepository{})
	if err != nil {
		logs.Err.Fatalf("Failed to initialize admin control plane: %v", err)
	}
	globals.adminControl = control
	basePath := apiPath + "v0/admin/"
	handler := newAdminHTTPHandler(basePath, *config.Admin, config, control)
	mux.Handle(basePath, handler)
	registerInternalWorkspaceHTTPRoutes(mux, apiPath, *config.Admin, control)
	logs.Info.Printf("Admin API served from '%s'", basePath)
}

func newAdminHTTPHandler(basePath string, adminConfig adminAPIConfig, config configType,
	control *admincontrol.ControlPlane) *adminHTTPHandler {
	origins := make(map[string]struct{}, len(adminConfig.AllowedOrigins))
	for _, origin := range adminConfig.AllowedOrigins {
		origin = strings.TrimRight(strings.TrimSpace(origin), "/")
		if origin != "" {
			origins[origin] = struct{}{}
		}
	}
	runtime := adminRuntimeView{
		Environment: config.Runtime.Environment, DeploymentMode: config.Runtime.DeploymentMode,
		MaxMessageSize: config.MaxMessageSize, MaxSubscriberCount: config.MaxSubscriberCount,
		P2PDeleteEnabled: config.P2PDeleteEnabled, MessageDeleteAgeSeconds: config.MsgDeleteAge,
		PolicyProvider: "local-casbin",
	}
	if config.Media != nil {
		runtime.MaxFileUploadSize = config.Media.MaxFileUploadSize
		runtime.MediaProcessingEnabled = config.Media.Processing != nil && config.Media.Processing.Enabled
	}
	return &adminHTTPHandler{
		basePath: basePath, token: adminConfig.BootstrapToken,
		allowedOrigins: origins, control: control,
		official: newOfficialTopicManager(control), runtime: runtime,
	}
}

func (handler *adminHTTPHandler) ServeHTTP(wrt http.ResponseWriter, req *http.Request) {
	requestID := strings.TrimSpace(req.Header.Get("X-Request-ID"))
	if requestID == "" {
		requestID = fmt.Sprintf("admin-%d", time.Now().UnixNano())
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
	if !handler.authenticated(req) {
		wrt.Header().Set("WWW-Authenticate", `Bearer realm="im-admin"`)
		handler.writeError(wrt, http.StatusUnauthorized, "admin_auth_required", requestID)
		return
	}

	resource := strings.Trim(strings.TrimPrefix(req.URL.Path, handler.basePath), "/")
	switch {
	case resource == "health" && req.Method == http.MethodGet:
		handler.writeData(wrt, http.StatusOK, map[string]any{
			"status": "ok", "version": handler.control.Snapshot().Version,
		}, requestID)
	case resource == "bootstrap" && req.Method == http.MethodGet:
		handler.writeBootstrap(wrt, requestID)
	case resource == "audit" && req.Method == http.MethodGet:
		limit, _ := strconv.Atoi(req.URL.Query().Get("limit"))
		handler.writeData(wrt, http.StatusOK, handler.control.Audit(limit), requestID)
	case resource == "evaluate" && req.Method == http.MethodPost:
		handler.evaluate(wrt, req, requestID)
	case resource == "settings" && req.Method == http.MethodPut:
		handler.updateSettings(wrt, req, requestID)
	case resource == "identities/session" && req.Method == http.MethodPost:
		handler.createIdentitySession(wrt, req, requestID)
	case strings.HasPrefix(resource, "translation/providers/") &&
		strings.HasSuffix(resource, "/test") && req.Method == http.MethodPost:
		handler.testTranslationProvider(wrt, req, resource, requestID)
	case resource == "official-topics" || strings.HasPrefix(resource, "official-topics/"):
		handler.officialTopics(wrt, req, resource, requestID)
	case strings.HasPrefix(resource, "roles/"):
		handler.role(wrt, req, strings.TrimPrefix(resource, "roles/"), requestID)
	case strings.HasPrefix(resource, "bindings/"):
		handler.binding(wrt, req, strings.TrimPrefix(resource, "bindings/"), requestID)
	default:
		handler.writeError(wrt, http.StatusNotFound, "admin_route_not_found", requestID)
	}
}

func (handler *adminHTTPHandler) applyCORS(wrt http.ResponseWriter, req *http.Request) bool {
	origin := strings.TrimRight(strings.TrimSpace(req.Header.Get("Origin")), "/")
	if origin == "" {
		return true
	}
	if _, ok := handler.allowedOrigins[origin]; !ok {
		return false
	}
	wrt.Header().Set("Access-Control-Allow-Origin", origin)
	wrt.Header().Set("Access-Control-Allow-Headers",
		"Authorization, Content-Type, If-Match, X-Request-ID")
	wrt.Header().Set("Access-Control-Allow-Methods", "GET, PUT, POST, PATCH, DELETE, OPTIONS")
	wrt.Header().Add("Vary", "Origin")
	return true
}

func (handler *adminHTTPHandler) authenticated(req *http.Request) bool {
	parts := strings.Fields(req.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || handler.token == "" {
		return false
	}
	left, right := []byte(parts[1]), []byte(handler.token)
	return len(left) == len(right) && subtle.ConstantTimeCompare(left, right) == 1
}

func (handler *adminHTTPHandler) writeBootstrap(wrt http.ResponseWriter, requestID string) {
	handler.writeData(wrt, http.StatusOK, adminBootstrapView{
		ControlPlane: handler.control.Snapshot(),
		Runtime:      handler.runtime,
		Integrations: []adminIntegrationView{
			{
				Name: "Groupbuying 身份与 Casbin 同步", Status: "deferred", Phase: "final",
				Detail: "本阶段使用本地管理员令牌和本地策略；跨系统身份、客户关系及资金能力最后接入。",
			},
			{
				Name: "Groupbuying 红包与转账", Status: "deferred", Phase: "final",
				Detail: "IM 当前不保存钱包或余额，仅预留权限点。",
			},
		},
	}, requestID)
}

func (handler *adminHTTPHandler) role(wrt http.ResponseWriter, req *http.Request, id, requestID string) {
	if id == "" || strings.Contains(id, "/") {
		handler.writeError(wrt, http.StatusBadRequest, "invalid_role_id", requestID)
		return
	}
	expected, ok := handler.expectedVersion(wrt, req, requestID)
	if !ok {
		return
	}
	actor := handler.actor(req, requestID)
	var (
		snapshot admincontrol.Snapshot
		err      error
	)
	switch req.Method {
	case http.MethodPut:
		var role admincontrol.Role
		if !handler.decode(wrt, req, &role, requestID) {
			return
		}
		if role.ID != "" && role.ID != id {
			handler.writeError(wrt, http.StatusBadRequest, "role_id_mismatch", requestID)
			return
		}
		role.ID = id
		snapshot, err = handler.control.UpsertRole(expected, role, actor)
	case http.MethodDelete:
		snapshot, err = handler.control.DeleteRole(expected, id, actor)
	default:
		handler.writeError(wrt, http.StatusMethodNotAllowed, "method_not_allowed", requestID)
		return
	}
	handler.writeMutation(wrt, snapshot, err, requestID)
}

func (handler *adminHTTPHandler) binding(wrt http.ResponseWriter, req *http.Request, id, requestID string) {
	if id == "" || strings.Contains(id, "/") {
		handler.writeError(wrt, http.StatusBadRequest, "invalid_binding_id", requestID)
		return
	}
	expected, ok := handler.expectedVersion(wrt, req, requestID)
	if !ok {
		return
	}
	actor := handler.actor(req, requestID)
	var (
		snapshot admincontrol.Snapshot
		err      error
	)
	switch req.Method {
	case http.MethodPut:
		var binding admincontrol.Binding
		if !handler.decode(wrt, req, &binding, requestID) {
			return
		}
		if binding.ID != "" && binding.ID != id {
			handler.writeError(wrt, http.StatusBadRequest, "binding_id_mismatch", requestID)
			return
		}
		binding.ID = id
		snapshot, err = handler.control.UpsertBinding(expected, binding, actor)
	case http.MethodDelete:
		snapshot, err = handler.control.DeleteBinding(expected, id, actor)
	default:
		handler.writeError(wrt, http.StatusMethodNotAllowed, "method_not_allowed", requestID)
		return
	}
	handler.writeMutation(wrt, snapshot, err, requestID)
}

func (handler *adminHTTPHandler) updateSettings(wrt http.ResponseWriter, req *http.Request, requestID string) {
	expected, ok := handler.expectedVersion(wrt, req, requestID)
	if !ok {
		return
	}
	var settings admincontrol.ProductSettings
	if !handler.decode(wrt, req, &settings, requestID) {
		return
	}
	snapshot, err := handler.control.UpdateSettings(expected, settings, handler.actor(req, requestID))
	handler.writeMutation(wrt, snapshot, err, requestID)
}

func (handler *adminHTTPHandler) testTranslationProvider(wrt http.ResponseWriter, req *http.Request,
	resource, requestID string) {
	id := strings.TrimSuffix(strings.TrimPrefix(resource, "translation/providers/"), "/test")
	if id == "" || strings.Contains(id, "/") {
		handler.writeError(wrt, http.StatusBadRequest, "invalid_translation_provider_id", requestID)
		return
	}
	var input struct {
		Text           string `json:"text"`
		SourceLanguage string `json:"source_language"`
		TargetLanguage string `json:"target_language"`
	}
	if !handler.decode(wrt, req, &input, requestID) {
		return
	}
	if strings.TrimSpace(input.Text) == "" || len([]rune(input.Text)) > 10000 ||
		strings.TrimSpace(input.TargetLanguage) == "" {
		handler.writeError(wrt, http.StatusBadRequest, "invalid_translation_test", requestID)
		return
	}
	started := time.Now()
	result, err := testConfiguredTranslationProvider(req.Context(),
		handler.control.Snapshot().Settings.Translation, id, translation.Request{
			Text: input.Text, SourceLanguage: input.SourceLanguage, TargetLanguage: input.TargetLanguage,
		})
	if errors.Is(err, admincontrol.ErrNotFound) || errors.Is(err, translation.ErrNoProvider) {
		handler.writeError(wrt, http.StatusNotFound, "translation_provider_not_found", requestID)
		return
	}
	if err != nil {
		logs.Warn.Printf("translation provider test %s failed: %v", id, err)
		handler.writeError(wrt, http.StatusBadGateway, "translation_provider_test_failed", requestID)
		return
	}
	handler.writeData(wrt, http.StatusOK, map[string]any{
		"provider": result.Provider, "text": result.Text,
		"detected_source_language": result.DetectedSourceLanguage,
		"latency_ms":               time.Since(started).Milliseconds(),
	}, requestID)
}

func (handler *adminHTTPHandler) evaluate(wrt http.ResponseWriter, req *http.Request, requestID string) {
	var input struct {
		Subject    string `json:"subject"`
		Domain     string `json:"domain"`
		Permission string `json:"permission"`
	}
	if !handler.decode(wrt, req, &input, requestID) {
		return
	}
	result, err := handler.control.Evaluate(input.Subject, input.Domain, input.Permission, time.Now().UTC())
	if err != nil {
		handler.writeAdminError(wrt, err, requestID)
		return
	}
	handler.writeData(wrt, http.StatusOK, result, requestID)
}

func (handler *adminHTTPHandler) expectedVersion(wrt http.ResponseWriter, req *http.Request,
	requestID string) (uint64, bool) {
	raw := strings.Trim(strings.TrimSpace(req.Header.Get("If-Match")), `"`)
	version, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || version == 0 {
		handler.writeError(wrt, http.StatusPreconditionRequired, "if_match_version_required", requestID)
		return 0, false
	}
	return version, true
}

func (handler *adminHTTPHandler) decode(wrt http.ResponseWriter, req *http.Request, target any,
	requestID string) bool {
	if contentType := req.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		handler.writeError(wrt, http.StatusUnsupportedMediaType, "json_content_type_required", requestID)
		return false
	}
	reader := http.MaxBytesReader(wrt, req.Body, adminMaxBodySize)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		handler.writeError(wrt, http.StatusBadRequest, "invalid_json", requestID)
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		handler.writeError(wrt, http.StatusBadRequest, "single_json_value_required", requestID)
		return false
	}
	return true
}

func (handler *adminHTTPHandler) actor(req *http.Request, requestID string) admincontrol.Actor {
	return admincontrol.Actor{
		Subject: "bootstrap-admin", RequestID: requestID, RemoteIP: getRemoteAddr(req),
	}
}

func (handler *adminHTTPHandler) writeMutation(wrt http.ResponseWriter,
	snapshot admincontrol.Snapshot, err error, requestID string) {
	if err != nil {
		handler.writeAdminError(wrt, err, requestID)
		return
	}
	wrt.Header().Set("ETag", fmt.Sprintf(`"%d"`, snapshot.Version))
	handler.writeData(wrt, http.StatusOK, snapshot, requestID)
}

func (handler *adminHTTPHandler) writeAdminError(wrt http.ResponseWriter, err error, requestID string) {
	switch {
	case errors.Is(err, admincontrol.ErrInvalid):
		handler.writeError(wrt, http.StatusBadRequest, "invalid_admin_input", requestID)
	case errors.Is(err, admincontrol.ErrNotFound):
		handler.writeError(wrt, http.StatusNotFound, "admin_object_not_found", requestID)
	case errors.Is(err, admincontrol.ErrConflict):
		handler.writeError(wrt, http.StatusConflict, "admin_version_conflict", requestID)
	case errors.Is(err, admincontrol.ErrProtected):
		handler.writeError(wrt, http.StatusForbidden, "protected_admin_object", requestID)
	default:
		logs.Warn.Printf("admin API request %s failed: %v", requestID, err)
		handler.writeError(wrt, http.StatusInternalServerError, "admin_internal_error", requestID)
	}
}

func (handler *adminHTTPHandler) writeData(wrt http.ResponseWriter, status int, data any,
	requestID string) {
	wrt.WriteHeader(status)
	_ = json.NewEncoder(wrt).Encode(adminHTTPResponse{Data: data, RequestID: requestID})
}

func (handler *adminHTTPHandler) writeError(wrt http.ResponseWriter, status int, code, requestID string) {
	wrt.WriteHeader(status)
	_ = json.NewEncoder(wrt).Encode(adminHTTPResponse{Error: code, RequestID: requestID})
}
