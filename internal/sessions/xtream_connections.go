package sessions

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/store"
)

func init() {
	store.RegisterAfter("xtream_connections", "sessions")
	store.Register("xtream_connections", 1, `CREATE TABLE sessions_xtream_connections (
 grant_reference TEXT PRIMARY KEY,
 viewer_id INTEGER NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
 target TEXT NOT NULL,
 peer TEXT NOT NULL,
 expires INTEGER NOT NULL
 ); CREATE INDEX sessions_xtream_connections_expiry ON sessions_xtream_connections(expires);`)
	store.Register("xtream_connections", 2, `CREATE TABLE sessions_xtream_connections_counted (
 grant_reference TEXT NOT NULL,
 viewer_id INTEGER NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
 target TEXT NOT NULL, peer TEXT NOT NULL, expires INTEGER NOT NULL,
 PRIMARY KEY(grant_reference,target)
 );
 INSERT INTO sessions_xtream_connections_counted SELECT grant_reference,viewer_id,target,peer,expires FROM sessions_xtream_connections;
 DROP TABLE sessions_xtream_connections;
 ALTER TABLE sessions_xtream_connections_counted RENAME TO sessions_xtream_connections;
 CREATE INDEX sessions_xtream_connections_expiry ON sessions_xtream_connections(expires);
 CREATE INDEX sessions_xtream_connections_viewer ON sessions_xtream_connections(viewer_id,expires);`)
	store.RegisterViewerTable("xtream_connections", "sessions_xtream_connections", "viewer_id", store.ViewerMergeDrop)
}

// Each login/peer/item has one renewable lease. Different items can play
// together, with admission serialized on the viewer row across cluster nodes.
func (s *lazyStreams) acquireXtreamConnection(r *http.Request, reference string, viewer int64, target string) (context.Context, func(), error) {
	peer, target, err := xtreamBinding(r, target)
	if err != nil {
		return nil, nil, err
	}
	const ttl = 30 * time.Second
	update := func(ctx context.Context) error {
		return s.admitXtream(ctx, reference, viewer, peer, target, ttl)
	}

	if err := update(r.Context()); err != nil {
		return nil, nil, err
	}
	// Renew long file responses as well as HLS segment activity. Cancellation
	// also stops the goroutine; Close waits for it so no process work is orphaned.
	ctx, cancel := context.WithCancel(r.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if update(ctx) != nil {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, func() { cancel(); <-done }, nil
}

func (s *lazyStreams) AppConnections(ctx context.Context, credential string) (int, error) {
	if s.protocol != "xtream" {
		return 0, fmt.Errorf("unavailable")
	}
	ref, viewer, err := s.grants.GrantReference(ctx, "xtream", credential)
	if err != nil || viewer == nil {
		return 0, fmt.Errorf("unauthorized")
	}
	var count int
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions_xtream_connections WHERE grant_reference=? AND viewer_id=? AND expires>?`, ref, viewer.ID, s.broker.deps.Now().UnixMilli()).Scan(&count)
	return count, err
}

func xtreamBinding(r *http.Request, target string) (string, string, error) {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if ip, ok := frontend.ClientIP(r.Context()); ok {
		peer = ip.String()
		err = nil
	}
	if err != nil {
		return "", "", fmt.Errorf("invalid stream peer")
	}
	return lazyHash(peer), lazyHash(peer + "/" + target), nil
}

// PrepareAppConnection reserves an item before source work begins. It never
// retires a different item on the same device: IPTV multi-view needs both.
func (s *lazyStreams) PrepareAppConnection(r *http.Request, credential string, req frontend.PlanRequest) error {
	if s.protocol != "xtream" || req.Viewer == nil || req.Protocol != s.protocol {
		return core.ErrUnauthorized
	}
	ref, v, err := s.grants.GrantReference(r.Context(), "xtream", credential)
	if err != nil || v == nil || v.ID != req.Viewer.ID {
		return core.ErrUnauthorized
	}
	peer, target, err := xtreamBinding(r, fmt.Sprint(req.Item)+"/"+req.Channel)
	if err != nil {
		return err
	}
	return s.admitXtream(r.Context(), ref, v.ID, peer, target, 30*time.Second)
}

// AppConnectionLimit exposes the same effective cap used by durable admission.
func (s *lazyStreams) AppConnectionLimit(ctx context.Context, credential string) (int, error) {
	if s.protocol != "xtream" {
		return 0, core.ErrUnauthorized
	}
	ref, v, err := s.grants.GrantReference(ctx, "xtream", credential)
	if err != nil || v == nil {
		return 0, core.ErrUnauthorized
	}
	viewerLimit, deviceLimit, err := s.xtreamLimits(ctx, s.db, ref, v.ID)
	if err != nil {
		return 0, err
	}
	if viewerLimit == 0 || deviceLimit > 0 && deviceLimit < viewerLimit {
		return deviceLimit, nil
	}
	return viewerLimit, nil
}

type xtreamQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *lazyStreams) xtreamLimits(ctx context.Context, q xtreamQuerier, ref string, id int64) (int, int, error) {
	var viewerLimit, deviceLimit int
	err := q.QueryRowContext(ctx, `SELECT u.stream_limit,g.xtream_stream_limit FROM auth_users u JOIN identity_grants g ON g.viewer_id=u.id WHERE u.id=? AND g.hash=? AND g.surface='xtream' AND g.revoked=0 AND g.expires>? AND u.active=1`, id, ref, s.broker.deps.Now().UnixMilli()).Scan(&viewerLimit, &deviceLimit)
	if err != nil {
		return 0, 0, err
	}
	viewer := &core.Viewer{ID: id, StreamLimit: viewerLimit}
	if s.broker.deps.StreamLimit != nil {
		viewerLimit = s.broker.deps.StreamLimit(viewer)
	} else if viewerLimit < 0 {
		viewerLimit = 0
	}
	return viewerLimit, deviceLimit, nil
}
func (s *lazyStreams) admitXtream(ctx context.Context, ref string, id int64, peer, target string, ttl time.Duration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A write locks this one viewer on PostgreSQL and serializes SQLite writers.
	// Thus two nodes cannot both see a free last slot and over-admit.
	if _, err = tx.ExecContext(ctx, `UPDATE auth_users SET id=id WHERE id=? AND active=1`, id); err != nil {
		return err
	}
	viewerLimit, deviceLimit, err := s.xtreamLimits(ctx, tx, ref, id)
	if err != nil {
		return err
	}
	now := s.broker.deps.Now().UnixMilli()
	var viewerCount, deviceCount int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN c.grant_reference=? THEN 1 ELSE 0 END),0) FROM sessions_xtream_connections c JOIN identity_grants g ON g.hash=c.grant_reference WHERE c.viewer_id=? AND c.expires>? AND g.revoked=0 AND g.expires>? AND NOT(c.grant_reference=? AND c.target=?)`, ref, id, now, now, ref, target).Scan(&viewerCount, &deviceCount)
	if err != nil {
		return err
	}
	if viewerLimit > 0 && viewerCount >= viewerLimit || deviceLimit > 0 && deviceCount >= deviceLimit {
		return core.ErrStreamLimit
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO sessions_xtream_connections(grant_reference,viewer_id,target,peer,expires) VALUES(?,?,?,?,?) ON CONFLICT(grant_reference,target) DO UPDATE SET expires=excluded.expires`, ref, id, target, peer, now+ttl.Milliseconds())
	if err != nil {
		return err
	}
	return tx.Commit()
}
