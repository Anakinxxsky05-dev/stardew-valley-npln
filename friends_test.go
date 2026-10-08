package main

import "testing"

func TestFriendUserAllowsBidirectionalPresence(t *testing.T) {
	got := friendUser("u-me", nplnFriendData{
		UserID:     "u-friend",
		AccountHex: "0123456789abcdef",
	})

	if got.GetName() != nplnTenant+"/users/u-me/friendUsers/u-friend" {
		t.Fatalf("name = %q", got.GetName())
	}
	if got.GetFriendUser() != nplnTenant+"/users/u-friend" {
		t.Fatalf("friend_user = %q", got.GetFriendUser())
	}
	if got.GetNsaId() != "0123456789abcdef" {
		t.Fatalf("nsa_id = %q", got.GetNsaId())
	}
	relationship := got.GetRelationship()
	if relationship == nil {
		t.Fatal("relationship is nil")
	}
	if !relationship.GetPresenceDeliverable() {
		t.Fatal("presence_deliverable = false")
	}
	if !relationship.GetPresenceReceivable() {
		t.Fatal("presence_receivable = false")
	}
}

func TestAutoFriendsInPermissiveMode(t *testing.T) {
	t.Setenv("NPLN_ALLOW_UNVERIFIED", "1")
	recordKnownUser(1850577049, "u-1850577049", "2e40a6919d80da56", "Anakin_sky")
	recordKnownUser(1888560507, "u-1888560507", "81d8d5d2ec4609fa", "Player2")

	srv := newFriendsServer(newSessionRegistry())
	list := srv.getFriendsList(1888560507, "u-1888560507")
	if len(list) == 0 {
		t.Fatal("expected at least 1 auto-friend, got 0")
	}
	found := false
	for _, f := range list {
		if f.PID == 1850577049 {
			found = true
			if f.AccountHex != "2e40a6919d80da56" {
				t.Fatalf("expected NSA 2e40a6919d80da56, got %s", f.AccountHex)
			}
			if f.Name != "Anakin_sky" {
				t.Fatalf("expected Name Anakin_sky, got %s", f.Name)
			}
		}
	}
	if !found {
		t.Fatal("did not find host 1850577049 in friend list")
	}
}

