package screens

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/store"
)

// The owner's per-app switches for document screens and surfaces ("Show in
// apps"). A switch the owner has never touched follows the manifest's
// default; once set it is sticky across plugin updates and restarts.

func init() {
	store.Register("plugin_screen_publish", 1, `
CREATE TABLE plugin_screen_publish (
	plugin_id TEXT NOT NULL,
	kind TEXT NOT NULL,
	item_id TEXT NOT NULL,
	frontend TEXT NOT NULL,
	enabled INTEGER NOT NULL,
	updated_at BIGINT NOT NULL,
	PRIMARY KEY(plugin_id, kind, item_id, frontend)
);
`)
}

// PublishStore holds the switches in memory, backed by the database.
type PublishStore struct {
	db *sql.DB

	once   sync.Once
	mu     sync.RWMutex
	values map[publishKey]bool
}

type publishKey struct {
	plugin, kind, id, frontend string
}

// NewPublishStore returns a store over db (nil keeps it in memory).
func NewPublishStore(db *sql.DB) *PublishStore {
	return &PublishStore{db: db, values: map[publishKey]bool{}}
}

func (p *PublishStore) load(ctx context.Context) {
	p.once.Do(func() {
		if p.db == nil {
			return
		}
		rows, err := p.db.QueryContext(ctx, `SELECT plugin_id, kind, item_id, frontend, enabled FROM plugin_screen_publish`)
		if err != nil {
			return
		}
		defer rows.Close()
		p.mu.Lock()
		defer p.mu.Unlock()
		for rows.Next() {
			var k publishKey
			var on int
			if rows.Scan(&k.plugin, &k.kind, &k.id, &k.frontend, &on) == nil {
				p.values[k] = on == 1
			}
		}
	})
}

// Published reports the switch, or def when the owner never set it.
func (p *PublishStore) Published(ctx context.Context, pluginID, kind, id, frontendID string, def bool) bool {
	if p == nil {
		return def
	}
	p.load(ctx)
	p.mu.RLock()
	v, ok := p.values[publishKey{pluginID, kind, id, frontendID}]
	p.mu.RUnlock()
	if !ok {
		return def
	}
	return v
}

// Set records the owner's switch.
func (p *PublishStore) Set(ctx context.Context, pluginID, kind, id, frontendID string, enabled bool) error {
	p.load(ctx)
	if p.db != nil {
		on := 0
		if enabled {
			on = 1
		}
		if _, err := p.db.ExecContext(ctx, `INSERT INTO plugin_screen_publish(plugin_id,kind,item_id,frontend,enabled,updated_at) VALUES(?,?,?,?,?,?)
			ON CONFLICT(plugin_id,kind,item_id,frontend) DO UPDATE SET enabled=excluded.enabled, updated_at=excluded.updated_at`,
			pluginID, kind, id, frontendID, on, time.Now().UnixMilli()); err != nil {
			return err
		}
	}
	p.mu.Lock()
	p.values[publishKey{pluginID, kind, id, frontendID}] = enabled
	p.mu.Unlock()
	return nil
}
