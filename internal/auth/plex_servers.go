package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/braedonsaunders/ether/internal/store"
)

// Extra Plex servers (config.PlexExtraServer) share this viewer table with
// the main server. Each server's access tokens carry its own source
// ("friend:p2", "server:p2"), and auth_server_members records which server
// shares with which viewer, so one server's friend sync never takes access
// away that another server still grants.
func init() {
	store.Register("auth", 10, `CREATE TABLE auth_server_members (
	server_id TEXT NOT NULL,
	user_id INTEGER NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
	PRIMARY KEY (server_id, user_id)
);`)
}

// mainServerMember is the main server's key in auth_server_members.
const mainServerMember = "main"

func friendSource(serverID string) string {
	if serverID == "" {
		return sourceFriend
	}
	return sourceFriend + ":" + serverID
}

func memberServer(serverID string) string {
	if serverID == "" {
		return mainServerMember
	}
	return serverID
}

// SetServerToken records the extra server id's own plex.tv token as an owner
// credential: Plex apps signed in to that server's account present it, and it
// belongs to the Ether owner, who added the account. The main owner is not
// replaced (SetOwner is for the main server only).
func (s *Service) SetServerToken(ctx context.Context, id, token string) error {
	if id == "" {
		return errors.New("auth: extra plex server without id")
	}
	src := sourceServer + ":" + id
	now := s.o.Now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_tokens WHERE source=?`, src); err != nil {
		return err
	}
	if token != "" {
		var owner int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM auth_users WHERE is_owner=1`).Scan(&owner); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errors.New("auth: no owner yet")
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO auth_tokens (token_hash, user_id, source, created_at) VALUES (?, ?, ?, ?)
			ON CONFLICT(token_hash) DO UPDATE SET user_id=excluded.user_id, source=excluded.source, created_at=excluded.created_at`,
			HashToken(token), owner, src, now); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.mu.Lock()
	s.pos = map[string]cacheEntry{}
	s.neg = map[string]negEntry{}
	s.mu.Unlock()
	err = s.reload(ctx)
	s.notify("caches")
	return err
}

// RemoveServer forgets an extra server's tokens and members. Viewers it alone
// shared with lose access at the main server's next friend sync.
func (s *Service) RemoveServer(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("auth: extra plex server without id")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_tokens WHERE source=? OR source=?`, friendSource(id), sourceServer+":"+id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_server_members WHERE server_id=?`, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.mu.Lock()
	s.pos = map[string]cacheEntry{}
	s.neg = map[string]negEntry{}
	s.mu.Unlock()
	err = s.reload(ctx)
	s.notify("caches")
	return err
}

// TokenServer reports which Plex server a token was issued for: "" for the
// main server, an extra server's id, or ok=false for an unknown token.
func (s *Service) TokenServer(ctx context.Context, token string) (string, bool) {
	if token == "" || s.db == nil {
		return "", false
	}
	var src string
	if err := s.db.QueryRowContext(ctx, `SELECT source FROM auth_tokens WHERE token_hash=?`, HashToken(token)).Scan(&src); err != nil {
		return "", false
	}
	if _, id, ok := strings.Cut(src, ":"); ok && (strings.HasPrefix(src, sourceFriend+":") || strings.HasPrefix(src, sourceServer+":")) {
		return id, true
	}
	return "", true
}

// ServerMembers counts the viewers each Plex server shares with ("" is the
// main server).
func (s *Service) ServerMembers(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT m.server_id, COUNT(*) FROM auth_server_members m JOIN auth_users u ON u.id=m.user_id WHERE u.active=1 GROUP BY m.server_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		if id == mainServerMember {
			id = ""
		}
		out[id] = n
	}
	return out, rows.Err()
}
