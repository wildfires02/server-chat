package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	admincontrol "chat/server/admin"
	"chat/server/store"
	"chat/server/store/types"
)

type adminHTTPMemoryRepository struct {
	document *admincontrol.Document
}

func (repo *adminHTTPMemoryRepository) Load() (*admincontrol.Document, error) {
	if repo.document == nil {
		return nil, admincontrol.ErrNotFound
	}
	return repo.document, nil
}

func (repo *adminHTTPMemoryRepository) Save(document *admincontrol.Document) error {
	repo.document = document
	return nil
}

func newTestAdminHandler(t *testing.T) *adminHTTPHandler {
	t.Helper()
	control, err := admincontrol.NewControlPlane(&adminHTTPMemoryRepository{})
	if err != nil {
		t.Fatal(err)
	}
	return newAdminHTTPHandler("/internal/", adminAPIConfig{
		Enabled: true, BootstrapToken: "test-bootstrap-token",
		AllowedOrigins: []string{"http://localhost:4173"},
	}, configType{
		Runtime:        runtimeConfig{Environment: environmentTest, DeploymentMode: deploymentModeStandalone},
		MaxMessageSize: 1024, MaxSubscriberCount: 128,
	}, control)
}

func TestAdminHTTPAuthenticationAndBootstrap(t *testing.T) {
	handler := newTestAdminHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/internal/bootstrap", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/internal/bootstrap", nil)
	request.Header.Set("Authorization", "Bearer test-bootstrap-token")
	request.Header.Set("Origin", "http://localhost:4173")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Access-Control-Allow-Origin") != "http://localhost:4173" {
		t.Fatalf("missing CORS response: %v", response.Header())
	}
}

func TestAdminHTTPMutationRequiresVersion(t *testing.T) {
	handler := newTestAdminHandler(t)
	body, _ := json.Marshal(admincontrol.Role{
		ID: "support", Name: "Support", Permissions: []string{"assets.read"},
	})
	request := httptest.NewRequest(http.MethodPut, "/internal/roles/support", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer test-bootstrap-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPreconditionRequired {
		t.Fatalf("expected 428, got %d: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPut, "/internal/roles/support", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer test-bootstrap-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", `"1"`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("ETag") != `"2"` {
		t.Fatalf("unexpected ETag: %q", response.Header().Get("ETag"))
	}
}

func TestAdminHTTPRejectsUnknownOrigin(t *testing.T) {
	handler := newTestAdminHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/internal/bootstrap", nil)
	request.Header.Set("Authorization", "Bearer test-bootstrap-token")
	request.Header.Set("Origin", "https://attacker.invalid")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", response.Code)
	}
}

func TestAdminHTTPPushDeadLetterRoutes(t *testing.T) {
	previous := store.PCache
	store.PCache = &resumableTestCache{values: make(map[string]string)}
	t.Cleanup(func() { store.PCache = previous })
	handler := newTestAdminHandler(t)

	request := httptest.NewRequest(http.MethodGet, "/internal/push/dlq?provider=fcm", nil)
	request.Header.Set("Authorization", "Bearer test-bootstrap-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected DLQ list 200, got %d: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost,
		"/internal/push/dlq/fcm/missing/replay", nil)
	request.Header.Set("Authorization", "Bearer test-bootstrap-token")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected replay 404, got %d: %s", response.Code, response.Body.String())
	}
}

func TestAdminHTTPTranslationProviderTestReturnsNotFound(t *testing.T) {
	handler := newTestAdminHandler(t)
	body := bytes.NewBufferString(
		`{"text":"你好","source_language":"zh","target_language":"en"}`)
	request := httptest.NewRequest(http.MethodPost,
		"/internal/translation/providers/missing/test", body)
	request.Header.Set("Authorization", "Bearer test-bootstrap-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", response.Code, response.Body.String())
	}
}

func TestAdminBusinessMessageRejectsUntrustedPayload(t *testing.T) {
	handler := newTestAdminHandler(t)
	request := httptest.NewRequest(http.MethodPost, "/internal/business/messages",
		bytes.NewBufferString(`{"provider":"server","actor_external_id":"1","target_external_id":"2","action":"message","text":"hello","client_id":"client-123"}`))
	request.Header.Set("Authorization", "Bearer test-bootstrap-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", response.Code, response.Body.String())
	}
}

func TestAdminConfigValidation(t *testing.T) {
	config := configType{
		Runtime: runtimeConfig{
			Environment: environmentDevelopment, DeploymentMode: deploymentModeStandalone,
		},
		Store: json.RawMessage(`{"use_adapter":"mysql"}`),
		Admin: &adminAPIConfig{Enabled: true},
	}
	if err := validateAdminServiceConfig(&config); err == nil {
		t.Fatal("expected missing bootstrap token to fail")
	}
	config.Admin.BootstrapToken = "development-token"
	config.Admin.AllowedOrigins = []string{"*"}
	if err := validateAdminServiceConfig(&config); err == nil {
		t.Fatal("expected wildcard origin to fail")
	}
	config.Admin.AllowedOrigins = []string{"http://localhost:4173"}
	if err := validateAdminServiceConfig(&config); err != nil {
		t.Fatalf("expected local admin config to pass: %v", err)
	}
}

func TestAdminRouteRegistrationUsesInternalPrefix(t *testing.T) {
	previousCache := store.PCache
	store.PCache = &resumableTestCache{values: make(map[string]string)}
	t.Cleanup(func() { store.PCache = previousCache })

	mux := http.NewServeMux()
	config := configType{
		Runtime: runtimeConfig{
			Environment: environmentTest, DeploymentMode: deploymentModeStandalone,
		},
		Admin: &adminAPIConfig{
			Enabled: true, BootstrapToken: "test-bootstrap-token",
		},
	}
	registerAdminHTTPRoutes(mux, "/", config)

	request := httptest.NewRequest(http.MethodGet, "/internal/health", nil)
	request.Header.Set("Authorization", "Bearer test-bootstrap-token")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected /internal/health to be registered, got %d: %s",
			response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/v0/health", nil)
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("legacy admin /v0 route is still exposed: status=%d", response.Code)
	}
}

func TestChatHTTPRoutesDoNotExposeAdminAPI(t *testing.T) {
	mux := http.NewServeMux()
	config := configType{
		Listen: ":6060", ApiPath: "/",
		Admin: &adminAPIConfig{
			Enabled: true, BootstrapToken: "must-still-be-ignored",
		},
	}
	config.StaticData = "-"
	config.ServerStatusPath = "-"
	registerServerHTTPRoutes(mux, "", &config, nil)

	request := httptest.NewRequest(http.MethodGet, "/internal/health", nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("im-server exposed an admin route: status=%d", response.Code)
	}
}

func TestChatHTTPRoutesExposeWorkspaceWithoutInternalSegment(t *testing.T) {
	mux := http.NewServeMux()
	config := configType{Listen: ":6060", ApiPath: "/", StaticData: "-", ServerStatusPath: "-"}
	previousControl := globals.adminControl
	globals.adminControl = nil
	t.Cleanup(func() { globals.adminControl = previousControl })
	registerServerHTTPRoutes(mux, "", &config, nil)

	request := httptest.NewRequest(http.MethodGet, "/v0/workspace", nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected client workspace route to exist, got %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/v0/internal/workspace", nil)
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("legacy internal client route is still exposed: status=%d", response.Code)
	}
}

type officialTopicTestData struct {
	topics map[string]*types.Topic
	subs   map[string]map[types.Uid]*types.Subscription
	users  map[types.Uid]*types.User
}

type officialTopicTestTopics struct {
	data *officialTopicTestData
}

func (store *officialTopicTestTopics) Create(topic *types.Topic, owner types.Uid, private any) error {
	if store.data.topics[topic.Id] != nil {
		return types.ErrDuplicate
	}
	topic.InitTimes()
	topic.Owner = owner.String()
	store.data.topics[topic.Id] = topic
	return (&officialTopicTestSubs{data: store.data}).Create(&types.Subscription{
		User: owner.String(), Topic: topic.Id,
		ModeWant: types.ModeCFull, ModeGiven: types.ModeCFull, Private: private,
	})
}

func (store *officialTopicTestTopics) Get(topic string) (*types.Topic, error) {
	return store.data.topics[topic], nil
}

func (store *officialTopicTestTopics) Update(topic string, update map[string]any) error {
	current := store.data.topics[topic]
	if current == nil {
		return types.ErrNotFound
	}
	if aux, ok := update["Aux"].(map[string]any); ok {
		current.Aux = aux
	}
	if public, ok := update["Public"]; ok {
		current.Public = public
	}
	return nil
}

func (store *officialTopicTestTopics) Delete(topic string, _ bool, _ bool) error {
	delete(store.data.topics, topic)
	delete(store.data.subs, topic)
	delete(store.data.subs, types.GrpToChn(topic))
	return nil
}

type officialTopicTestSubs struct {
	data *officialTopicTestData
}

func (store *officialTopicTestSubs) Create(subs ...*types.Subscription) error {
	for _, sub := range subs {
		uid := types.ParseUid(sub.User)
		if store.data.subs[sub.Topic] == nil {
			store.data.subs[sub.Topic] = make(map[types.Uid]*types.Subscription)
		}
		if store.data.subs[sub.Topic][uid] != nil {
			return types.ErrDuplicate
		}
		copyOfSub := *sub
		store.data.subs[sub.Topic][uid] = &copyOfSub
	}
	return nil
}

func (store *officialTopicTestSubs) Get(topic string, user types.Uid,
	_ bool) (*types.Subscription, error) {
	return store.data.subs[topic][user], nil
}

func (store *officialTopicTestSubs) Update(topic string, user types.Uid,
	update map[string]any) error {
	sub := store.data.subs[topic][user]
	if sub == nil {
		return types.ErrNotFound
	}
	if mode, ok := update["ModeWant"].(types.AccessMode); ok {
		sub.ModeWant = mode
	}
	if mode, ok := update["ModeGiven"].(types.AccessMode); ok {
		sub.ModeGiven = mode
	}
	return nil
}

func (store *officialTopicTestSubs) Delete(topic string, user types.Uid) error {
	delete(store.data.subs[topic], user)
	return nil
}

type officialTopicTestUsers struct {
	data *officialTopicTestData
}

func (store *officialTopicTestUsers) Get(uid types.Uid) (*types.User, error) {
	return store.data.users[uid], nil
}

func TestAdminOfficialTopicCreateAndRoleAssignment(t *testing.T) {
	handler := newTestAdminHandler(t)
	owner, publisher := types.Uid(10), types.Uid(11)
	data := &officialTopicTestData{
		topics: make(map[string]*types.Topic),
		subs:   make(map[string]map[types.Uid]*types.Subscription),
		users: map[types.Uid]*types.User{
			owner:     {State: types.StateOK},
			publisher: {State: types.StateOK},
		},
	}
	handler.official = &officialTopicManager{
		control: handler.control, topics: &officialTopicTestTopics{data: data},
		subs: &officialTopicTestSubs{data: data}, users: &officialTopicTestUsers{data: data},
		newTopic: func() string { return "grpOfficial01" },
	}

	createBody := []byte(`{
		"org_id":"org-main",
		"owner":"` + owner.UserId() + `",
		"public":{"fn":"官方公告"}
	}`)
	request := httptest.NewRequest(http.MethodPost, "/internal/official-topics",
		bytes.NewReader(createBody))
	request.Header.Set("Authorization", "Bearer test-bootstrap-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", `"1"`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("创建官方频道失败：code=%d body=%s", response.Code, response.Body.String())
	}
	storedTopic := data.topics["grpOfficial01"]
	if storedTopic == nil || !storedTopic.UseBt {
		t.Fatalf("官方频道未创建：%+v", storedTopic)
	}
	if storedTopic.Access.Auth != types.ModeNone {
		t.Fatalf("官方频道不应允许从底层群组地址自行加入：%v", storedTopic.Access.Auth)
	}
	policy, err := officialPolicyFromAux("grpOfficial01", storedTopic.Aux)
	if err != nil || policy == nil || policy.Owner != owner.UserId() ||
		policy.OfficialStatus != "verified" {
		t.Fatalf("官方认证投影不正确：policy=%+v err=%v", policy, err)
	}

	roleBody := []byte(`{"role":"publisher"}`)
	request = httptest.NewRequest(http.MethodPut,
		"/internal/official-topics/grpOfficial01/members/"+publisher.UserId()+"/role",
		bytes.NewReader(roleBody))
	request.Header.Set("Authorization", "Bearer test-bootstrap-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", `"2"`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"3"` {
		t.Fatalf("分配发布者失败：code=%d body=%s", response.Code, response.Body.String())
	}
	publisherSub := data.subs["grpOfficial01"][publisher]
	if publisherSub == nil || !(publisherSub.ModeGiven & publisherSub.ModeWant).IsWriter() {
		t.Fatalf("发布者 ACL 不正确：%+v", publisherSub)
	}

	roleBody = []byte(`{"role":"subscriber"}`)
	request = httptest.NewRequest(http.MethodPut,
		"/internal/official-topics/grpOfficial01/members/"+publisher.UserId()+"/role",
		bytes.NewReader(roleBody))
	request.Header.Set("Authorization", "Bearer test-bootstrap-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", `"3"`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"4"` {
		t.Fatalf("降级订阅者失败：code=%d body=%s", response.Code, response.Body.String())
	}
	if data.subs["grpOfficial01"][publisher] != nil {
		t.Fatal("订阅者仍残留在发布者命名空间")
	}
	subscriberSub := data.subs["chnOfficial01"][publisher]
	if subscriberSub == nil || (subscriberSub.ModeGiven & subscriberSub.ModeWant).IsWriter() {
		t.Fatalf("订阅者 ACL 不正确：%+v", subscriberSub)
	}
}
