package server

import (
	"testing"

	"chat/server/store/types"
)

func TestParseInternalWorkspaceTargets(t *testing.T) {
	actor := types.Uid(100)
	customer := types.Uid(200)

	mutation, err := parseInternalWorkspaceTarget(
		actor, "customer/"+customer.UserId())
	if err != nil || mutation.Kind != types.InternalPinCustomer ||
		mutation.CustomerUID != customer.UserId() {
		t.Fatalf("customer target mismatch: mutation=%#v err=%v", mutation, err)
	}
	mutation, err = parseInternalWorkspaceTarget(
		actor, "conversation/"+customer.UserId())
	if err != nil || mutation.Kind != types.InternalPinConversation ||
		mutation.Topic != actor.P2PName(customer) {
		t.Fatalf("peer conversation mismatch: mutation=%#v err=%v", mutation, err)
	}
	mutation, err = parseInternalWorkspaceTarget(actor, "conversation/chnExample")
	if err != nil || mutation.Topic != "grpExample" {
		t.Fatalf("channel canonicalization mismatch: mutation=%#v err=%v", mutation, err)
	}
	mutation, err = parseInternalWorkspaceTarget(actor, "message/grpExample/42")
	if err != nil || mutation.Kind != types.InternalPinMessage ||
		mutation.Topic != "grpExample" || mutation.SeqID != 42 {
		t.Fatalf("message target mismatch: mutation=%#v err=%v", mutation, err)
	}

	for _, invalid := range []string{
		"customer/" + actor.UserId(),
		"conversation/me",
		"message/grpExample/0",
		"message/grpExample/not-a-sequence",
	} {
		if _, err = parseInternalWorkspaceTarget(actor, invalid); err == nil {
			t.Fatalf("invalid target accepted: %q", invalid)
		}
	}
}

func TestParseInternalWorkspaceIfMatch(t *testing.T) {
	for _, input := range []string{`4`, `"4"`, `W/"4"`} {
		if value, ok := parseInternalWorkspaceIfMatch(input); !ok || value != 4 {
			t.Fatalf("If-Match %q rejected: value=%d ok=%v", input, value, ok)
		}
	}
	for _, input := range []string{"", "*", `"bad"`, `"4`, `4"`} {
		if _, ok := parseInternalWorkspaceIfMatch(input); ok {
			t.Fatalf("invalid If-Match %q accepted", input)
		}
	}
}
