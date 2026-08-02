package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"chat/server/auth"
	"chat/server/store"
	mockstore "chat/server/store/mock_store"
	"chat/server/store/types"

	"go.uber.org/mock/gomock"
)

type externalIdentityProfileTestStore struct {
	user       *types.User
	update     map[string]any
	updateCall int
}

func (store *externalIdentityProfileTestStore) Get(types.Uid) (*types.User, error) {
	return store.user, nil
}

func (store *externalIdentityProfileTestStore) Update(_ types.Uid, update map[string]any) error {
	store.updateCall++
	store.update = update
	return nil
}

func TestExternalIdentityUnique(t *testing.T) {
	first := externalIdentityUnique("server", "12345678")
	if got := len("external:" + first); got > 32 {
		t.Fatalf("persisted auth identity is too long: %d", got)
	}
	if first != externalIdentityUnique("server", "12345678") {
		t.Fatal("external identity mapping is not deterministic")
	}
	if first == externalIdentityUnique("server", "87654321") {
		t.Fatal("different external users resolved to the same test identity")
	}
	if first == externalIdentityUnique("another", "12345678") {
		t.Fatal("provider is not part of the external identity mapping")
	}
}

func TestEnsureExternalIdentityCreatesUserWhenLookupReturnsZeroUID(t *testing.T) {
	controller := gomock.NewController(t)
	users := mockstore.NewMockUsersPersistenceInterface(controller)
	originalUsers := store.Users
	store.Users = users
	defer func() { store.Users = originalUsers }()

	input := adminIdentitySessionRequest{
		Provider: "server", ExternalID: "77890",
		ProfileVersion: 1, Profile: adminIdentityProfile{Name: "Test user"},
	}
	expectedUID := types.Uid(77890)
	users.EXPECT().GetAuthUniqueRecord(externalIdentityAuthScheme, gomock.Any()).
		Return(types.ZeroUid, auth.LevelNone, nil, time.Time{}, nil)
	users.EXPECT().Create(gomock.Any(), nil).
		DoAndReturn(func(user *types.User, _ any) (*types.User, error) {
			user.SetUid(expectedUID)
			return user, nil
		})
	users.EXPECT().AddAuthRecord(expectedUID, auth.LevelAuth, externalIdentityAuthScheme,
		gomock.Any(), []byte{0}, time.Time{}).Return(nil)

	uid, err := ensureExternalIdentity(input)
	if err != nil {
		t.Fatalf("ensure external identity: %v", err)
	}
	if uid != expectedUID {
		t.Fatalf("created uid=%v, want %v", uid, expectedUID)
	}
}

func TestNormalizeAdminIdentityDeviceInput(t *testing.T) {
	input := adminIdentityDeviceRequest{
		Provider: " Server ", ExternalID: " 45885384 ", DeviceToken: " token ",
		OldToken: " old-token ", Platform: " iPhone ", Lang: " EN-US ",
	}
	if !normalizeAdminIdentityDeviceInput(&input) {
		t.Fatal("expected valid native device input")
	}
	if input.Provider != "server" || input.ExternalID != "45885384" ||
		input.DeviceToken != "token" || input.Platform != "ios" || input.Lang != "en-us" {
		t.Fatalf("unexpected normalized device input: %#v", input)
	}
	input.Platform = "web"
	if normalizeAdminIdentityDeviceInput(&input) {
		t.Fatal("browser push device must be rejected")
	}
}

func TestExternalIdentityProfilePreservesUnmanagedFields(t *testing.T) {
	users := &externalIdentityProfileTestStore{user: &types.User{
		Public:  map[string]any{"fn": "Old", "photo": "old.jpg", "country": "VN"},
		Trusted: map[string]any{"profile_version": int64(10), "role": "customer"},
	}}
	input := adminIdentitySessionRequest{
		Provider: "server", ExternalID: "45885384", ProfileVersion: 11,
		Profile: adminIdentityProfile{Name: "New Name", Avatar: "new.jpg"},
	}
	if err := updateExternalIdentityProfileWithStore(users, types.Uid(1), input); err != nil {
		t.Fatal(err)
	}
	public := users.update["Public"].(map[string]any)
	trusted := users.update["Trusted"].(map[string]any)
	if public["fn"] != "New Name" || public["photo"] != "new.jpg" ||
		public["external_id"] != "45885384" || public["country"] != "VN" {
		t.Fatalf("unexpected public profile: %#v", public)
	}
	if trusted["profile_version"] != int64(11) || trusted["role"] != "customer" {
		t.Fatalf("unexpected trusted profile: %#v", trusted)
	}
}

func TestExternalIdentityProfileRejectsStaleVersion(t *testing.T) {
	users := &externalIdentityProfileTestStore{user: &types.User{
		Public:  map[string]any{"fn": "Current", "photo": "current.jpg"},
		Trusted: map[string]any{"profile_version": float64(20)},
	}}
	input := adminIdentitySessionRequest{
		Provider: "server", ExternalID: "45885384", ProfileVersion: 19,
		Profile: adminIdentityProfile{Name: "Stale", Avatar: "stale.jpg"},
	}
	if err := updateExternalIdentityProfileWithStore(users, types.Uid(1), input); err != nil {
		t.Fatal(err)
	}
	if users.updateCall != 0 {
		t.Fatal("stale profile update must not reach persistence")
	}
}

func TestExternalIdentityProfileCanClearAvatar(t *testing.T) {
	users := &externalIdentityProfileTestStore{user: &types.User{
		Public:  map[string]any{"fn": "Current", "photo": "current.jpg"},
		Trusted: map[string]any{"profile_version": int64(20)},
	}}
	input := adminIdentitySessionRequest{
		Provider: "server", ExternalID: "45885384", ProfileVersion: 21,
		Profile: adminIdentityProfile{Name: "Current"},
	}
	if err := updateExternalIdentityProfileWithStore(users, types.Uid(1), input); err != nil {
		t.Fatal(err)
	}
	public := users.update["Public"].(map[string]any)
	if _, exists := public["photo"]; exists {
		t.Fatalf("avatar was not cleared: %#v", public)
	}
}

func TestAdminIdentityProfileRouteValidatesInput(t *testing.T) {
	handler := newTestAdminHandler(t)
	request := httptest.NewRequest(http.MethodPut, "/v0/identities/profile",
		bytes.NewBufferString(`{"provider":"","external_id":""}`))
	request.Header.Set("Authorization", "Bearer test-bootstrap-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", response.Code, response.Body.String())
	}
}
