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
}

type xtreamGrants struct{}

func (xtreamGrants) GrantReference(context.Context, string, string) (string, *core.Viewer, error) {
	return "grant", &core.Viewer{ID: 1}, nil
}
func (xtreamGrants) ValidateGrantReference(context.Context, string, string, int64) error { return nil }
