package server

import (
	"testing"
	"time"

	"chat/server/auth"
	"chat/server/store"
	mockstore "chat/server/store/mock_store"
	"chat/server/store/types"

	"go.uber.org/mock/gomock"
)

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
