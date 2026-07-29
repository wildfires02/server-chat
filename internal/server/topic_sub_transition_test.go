package server

import (
	"testing"

	"chat/server/store/types"
)

func TestPlanExistingSelfSubscriptionRejectsInvalidOwnerChange(t *testing.T) {
	input := selfSubscriptionTransitionInput{
		category:      types.TopicCatGrp,
		isTopicOwner:  true,
		oldWant:       types.ModeCFull,
		oldGiven:      types.ModeCFull,
		requested:     types.ModeJoin | types.ModeRead,
		defaultAccess: types.ModeCPublic,
	}

	if _, err := planExistingSelfSubscription(input); err == nil {
		t.Fatal("expected topic owner to be prevented from dropping owner access")
	}

	input.requested = types.ModeCFull &^ types.ModeJoin
	if _, err := planExistingSelfSubscription(input); err == nil {
		t.Fatal("expected topic owner to be prevented from dropping join access")
	}
}

func TestPlanExistingSelfSubscriptionRejectsOwnerAccessForNonOwner(t *testing.T) {
	_, err := planExistingSelfSubscription(selfSubscriptionTransitionInput{
		category:  types.TopicCatGrp,
		oldWant:   types.ModeCPublic,
		oldGiven:  types.ModeCPublic,
		requested: types.ModeCPublic | types.ModeOwner,
	})
	if err == nil {
		t.Fatal("expected non-owner owner-access request to fail")
	}
}

func TestPlanExistingSelfSubscriptionTransfersOwnerAndRestoresGrant(t *testing.T) {
	requested := types.ModeCFull
	transition, err := planExistingSelfSubscription(selfSubscriptionTransitionInput{
		category:  types.TopicCatGrp,
		oldWant:   types.ModeCPublic,
		oldGiven:  types.ModeOwner | types.ModeJoin,
		requested: requested,
	})
	if err != nil {
		t.Fatalf("planExistingSelfSubscription() error = %v", err)
	}
	if !transition.ownerChange {
		t.Fatal("expected owner transfer to be detected")
	}
	if transition.want != requested {
		t.Fatalf("want = %v, expected %v", transition.want, requested)
	}
	if transition.given != requested {
		t.Fatalf("given = %v, expected %v", transition.given, requested)
	}
}

func TestPlanExistingSelfSubscriptionAdminSelfGrantExcludesDelete(t *testing.T) {
	oldGiven := types.ModeJoin | types.ModeApprove
	requested := oldGiven | types.ModeRead | types.ModeDelete
	transition, err := planExistingSelfSubscription(selfSubscriptionTransitionInput{
		category:  types.TopicCatGrp,
		oldWant:   oldGiven,
		oldGiven:  oldGiven,
		requested: requested,
	})
	if err != nil {
		t.Fatalf("planExistingSelfSubscription() error = %v", err)
	}

	expectedGiven := oldGiven | types.ModeRead
	if transition.given != expectedGiven {
		t.Fatalf("given = %v, expected %v", transition.given, expectedGiven)
	}
	if transition.want != requested {
		t.Fatalf("want = %v, expected %v", transition.want, requested)
	}
}

func TestPlanExistingSelfSubscriptionClampsSpecialTopics(t *testing.T) {
	tests := []struct {
		name     string
		category types.TopicCat
		request  types.AccessMode
		p2p      types.AccessMode
		want     types.AccessMode
	}{
		{
			name:     "p2p",
			category: types.TopicCatP2P,
			request:  types.ModeCP2P | types.ModeShare | types.ModeDelete,
			p2p:      types.ModeCP2P,
			want:     types.ModeCP2P,
		},
		{
			name:     "sys",
			category: types.TopicCatSys,
			request:  types.ModeCSys | types.ModeApprove | types.ModeShare,
			want:     types.ModeCSys,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transition, err := planExistingSelfSubscription(selfSubscriptionTransitionInput{
				category:  test.category,
				oldWant:   types.ModeJoin,
				oldGiven:  types.ModeJoin,
				requested: test.request,
				p2pAccess: test.p2p,
			})
			if err != nil {
				t.Fatalf("planExistingSelfSubscription() error = %v", err)
			}
			if transition.want != test.want {
				t.Fatalf("want = %v, expected %v", transition.want, test.want)
			}
		})
	}
}

func TestPlanExistingSelfSubscriptionRestoresDefaultAccessWhenRejoining(t *testing.T) {
	oldGiven := types.ModeRead | types.ModeApprove
	defaultAccess := types.ModeJoin | types.ModeWrite
	transition, err := planExistingSelfSubscription(selfSubscriptionTransitionInput{
		category:      types.TopicCatGrp,
		oldWant:       types.ModeRead,
		oldGiven:      oldGiven,
		requested:     types.ModeUnset,
		defaultAccess: defaultAccess,
	})
	if err != nil {
		t.Fatalf("planExistingSelfSubscription() error = %v", err)
	}

	expected := oldGiven | defaultAccess
	if transition.want != expected {
		t.Fatalf("want = %v, expected %v", transition.want, expected)
	}
	if transition.given != oldGiven {
		t.Fatalf("given = %v, expected %v", transition.given, oldGiven)
	}
}

func TestPlanExistingSelfSubscriptionLeavesJoinedModeUnchangedWhenUnset(t *testing.T) {
	oldWant := types.ModeJoin | types.ModeRead
	transition, err := planExistingSelfSubscription(selfSubscriptionTransitionInput{
		category:      types.TopicCatGrp,
		oldWant:       oldWant,
		oldGiven:      types.ModeCFull,
		requested:     types.ModeUnset,
		defaultAccess: types.ModeCPublic,
	})
	if err != nil {
		t.Fatalf("planExistingSelfSubscription() error = %v", err)
	}
	if transition.want != oldWant {
		t.Fatalf("want = %v, expected %v", transition.want, oldWant)
	}
}
