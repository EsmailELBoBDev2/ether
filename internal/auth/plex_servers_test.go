package auth

import (
	"context"
	"testing"

	"github.com/braedonsaunders/ether/internal/plextv"
)

// A viewer shared on an extra Plex server signs in with that server's
// token, keeps access while any server still shares with them, and each
// server's seats are counted apart.
func TestExtraPlexServerMembership(t *testing.T) {
	s, _, _ := setup(t)
	ctx := context.Background()
	if err := s.SetServerToken(ctx, "p2", "p2-server-token"); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncServerFriends(ctx, "p2", []plextv.Friend{
		{Owned: true, PlexID: 77, Token: "p2-owner-access"},
		{PlexID: 103, Username: "carol", Token: "carol-p2"},
		{PlexID: 100, Username: "alice", Token: "alice-p2"},
	}); err != nil {
		t.Fatal(err)
	}
	for tok, want := range map[string]string{"carol-p2": "carol", "alice-p2": "alice", "alice-access": "alice", "p2-server-token": "owner"} {
		u, err := s.Authenticate(ctx, tok)
		if err != nil || u.Username != want {
			t.Fatalf("%s: %+v %v", tok, u, err)
		}
	}
	if id, ok := s.TokenServer(ctx, "carol-p2"); !ok || id != "p2" {
		t.Fatalf("carol token server = %q %v", id, ok)
	}
	if id, ok := s.TokenServer(ctx, "p2-server-token"); !ok || id != "p2" {
		t.Fatalf("p2 server token = %q %v", id, ok)
	}
	if id, ok := s.TokenServer(ctx, "alice-access"); !ok || id != "" {
		t.Fatalf("main token server = %q %v", id, ok)
	}
	if _, ok := s.TokenServer(ctx, "nobody"); ok {
		t.Fatal("unknown token reported as known")
	}
	members, err := s.ServerMembers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if members[""] != 3 || members["p2"] != 2 {
		t.Fatalf("members = %v, want main 3 (alice, bob, dave) and p2 2", members)
	}

	// The main server stops sharing with alice: p2 still does, so she stays.
	if err := s.SyncFriends(ctx, []plextv.Friend{
		{Owned: true, Token: "owner-access", PlexID: 42},
		{PlexID: 101, Username: "bob", Token: "bob-access", AllLibs: true},
	}); err != nil {
		t.Fatal(err)
	}
	if u, err := s.Authenticate(ctx, "alice-p2"); err != nil || u.Username != "alice" {
		t.Fatalf("alice lost access another server still grants: %+v %v", u, err)
	}
	if _, err := s.Authenticate(ctx, "alice-access"); err == nil {
		t.Fatal("alice's main-server token still works after the main server unshared")
	}

	// p2 stops sharing with everyone: carol (only on p2) loses access.
	if err := s.SyncServerFriends(ctx, "p2", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, "carol-p2"); err == nil {
		t.Fatal("carol kept access after her only server unshared")
	}
	if u, err := s.Authenticate(ctx, "bob-access"); err != nil || u.Username != "bob" {
		t.Fatalf("an extra server's sync touched a main-server viewer: %+v %v", u, err)
	}
	members, _ = s.ServerMembers(ctx)
	if members["p2"] != 0 || members[""] != 1 {
		t.Fatalf("members after unshare = %v", members)
	}
}
