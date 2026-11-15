package sessions

import (
	"context"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
	"net/http/httptest"
	"testing"
	"time"
)

func TestXtreamConnectionLimitSharedAcrossNodes(t *testing.T) {
	db := store.OpenTest(t)
	ctx := context.Background()
	now := time.Now()
	if _, err := db.ExecContext(ctx, `INSERT INTO auth_users(id,username,title,active,created_at,updated_at,stream_limit) VALUES(1,'viewer','Viewer',1,0,0,3)`); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"grant", "second"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO identity_grants(hash,viewer_id,surface,device_id,client,product,version,created,last_used,expires,xtream_stream_limit) VALUES(?,1,'xtream','tv','','','',0,0,?,?)`, ref, now.Add(time.Hour).UnixMilli(), map[string]int{"grant": 2}[ref]); err != nil {
			t.Fatal(err)
		}
	}
	b := &Broker{deps: BrokerDeps{SurfacePolicy: testSessionPolicy, Now: func() time.Time { return now }}}
	one := &lazyStreams{db: db, broker: b, protocol: "xtream", grants: xtreamGrants{}}
	two := &lazyStreams{db: db, broker: b, protocol: "xtream", grants: xtreamGrants{}}
	r := httptest.NewRequest("GET", "http://ether.test/file", nil)
	r.RemoteAddr = "192.0.2.1:1234"
	admit := func(s *lazyStreams, ref, item string, want bool) {
		t.Helper()
		_, release, err := s.acquireXtreamConnection(r, ref, 1, item)
		if err == nil {
			release()
		}
		if (err == nil) != want {
			t.Fatalf("%s/%s admission=%v, want %v", ref, item, err, want)
		}
	}
	admit(one, "grant", "movie/1", true)
	admit(two, "grant", "movie/1", true)
	admit(two, "grant", "movie/2", true)
	admit(one, "grant", "movie/3", false) // device cap 2
	admit(two, "second", "movie/3", true)
	admit(one, "second", "movie/4", false) // shared viewer cap 3
	if limit, err := one.AppConnectionLimit(ctx, "password"); err != nil || limit != 2 {
		t.Fatal("effective min", limit, err)
	}
	if active, err := one.AppConnections(ctx, "password"); err != nil || active != 2 {
		t.Fatal("active count", active, err)
	}
	// Lowering the viewer cap changes the advertised min immediately.
	if _, err := db.ExecContext(ctx, `UPDATE auth_users SET stream_limit=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if limit, err := one.AppConnectionLimit(ctx, "password"); err != nil || limit != 1 {
		t.Fatal("viewer min", limit, err)
	}
	now = now.Add(31 * time.Second)
	admit(two, "second", "movie/4", true)
	// Switching at the viewer cap can reclaim a stopped item on the same
	// login/peer before the full lease expires, but never a freshly renewed one.
	admit(one, "second", "live/next", false)
	now = now.Add(xtreamConnectionIdle)
	admit(one, "second", "live/next", false) // exactly the window is still protected
	now = now.Add(time.Millisecond)
	admit(one, "grant", "live/next", false) // another login cannot steal the slot
	r.RemoteAddr = "192.0.2.2:1234"
	admit(one, "second", "live/next", false) // neither can another peer
	r.RemoteAddr = "192.0.2.1:1234"
	admit(one, "second", "live/next", true)
	var oldCount int
	_, oldTarget, _ := xtreamBinding(r, "movie/4")
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions_xtream_connections WHERE grant_reference='second' AND target=?`, oldTarget).Scan(&oldCount); err != nil || oldCount != 0 {
		t.Fatal("old viewer-cap lease retained", oldCount, err)
	}

	// With viewer capacity available, the per-device cap follows the same rule.
	if _, err := db.ExecContext(ctx, `UPDATE auth_users SET stream_limit=3 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE identity_grants SET xtream_stream_limit=1 WHERE hash='second'`); err != nil {
		t.Fatal(err)
	}
	now = now.Add(xtreamConnectionIdle - time.Second)
	admit(two, "second", "live/next", true) // live HLS/range traffic renews the item
	now = now.Add(2 * time.Second)
	admit(one, "second", "live/new", false) // original age is old; renewal is fresh
	now = now.Add(xtreamConnectionIdle)
	admit(one, "second", "live/new", true)
	var activeCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions_xtream_connections WHERE grant_reference='second' AND expires>?`, now.UnixMilli()).Scan(&activeCount); err != nil || activeCount != 1 {
		t.Fatal("device switch did not replace exactly one slot", activeCount, err)
	}

	// Multi-view reclamation chooses the oldest eligible lease, retaining the
	// other item even when both have become idle.
	if _, err := db.ExecContext(ctx, `UPDATE identity_grants SET xtream_stream_limit=2 WHERE hash='second'`); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	admit(one, "second", "live/other", true)
	now = now.Add(xtreamConnectionIdle + time.Millisecond)
	admit(two, "second", "live/third", true)
	_, oldest, _ := xtreamBinding(r, "live/new")
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions_xtream_connections WHERE grant_reference='second' AND target=?`, oldest).Scan(&oldCount); err != nil || oldCount != 0 {
		t.Fatal("oldest idle item not reclaimed", oldCount, err)
	}
	_, remaining, _ := xtreamBinding(r, "live/other")
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions_xtream_connections WHERE grant_reference='second' AND target=?`, remaining).Scan(&oldCount); err != nil || oldCount != 1 {
		t.Fatal("other multi-view item reclaimed", oldCount, err)
	}
}

type xtreamGrants struct{}

func (xtreamGrants) GrantReference(context.Context, string, string) (string, *core.Viewer, error) {
	return "grant", &core.Viewer{ID: 1}, nil
}
func (xtreamGrants) ValidateGrantReference(context.Context, string, string, int64) error { return nil }
