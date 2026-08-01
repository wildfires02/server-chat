package server

import "testing"

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
