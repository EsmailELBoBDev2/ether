package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/personalize"
)

// migrateRowsPages runs the one-time rows-pages-v1 migration: the old Home
// layout, the collections' publication targets and the Ether-managed
// discovery collections become Recommendations pages; collections keep
// only "Browse in apps"; add-on imports and the Jellyfin/Emby Home settings
// drop their row placement. It runs once per database (a failed run is
// retried at the next start) and is deleted when upgrades from 0.3.20 end.
func (a *App) migrateRowsPages(ctx context.Context) error {
	if a.personalize == nil {
		return nil
	}
	state, err := a.personalize.MigrationState(ctx, personalize.RowsPagesMigration)
	if err != nil || state == "done" {
		return err
	}
	var placements []personalize.LegacyCollectionPlacement
	if a.collections != nil {
		legacy, err := a.collections.TakeLegacyPlacements(ctx)
		if err != nil {
			return fmt.Errorf("collections: read old placements: %w", err)
		}
		for _, p := range legacy {
			placements = append(placements, personalize.LegacyCollectionPlacement{ID: p.ID, MediaType: p.MediaType, Enabled: p.Enabled,
				HomeRow: p.HomeRow, SectionRow: p.SectionRow, Viewers: p.Viewers, When: p.When, Devices: p.Devices})
		}
	}
	n, err := a.personalize.MigrateRowsPages(ctx, placements)
	if err != nil {
		return err
	}
	// The Ether-managed discovery collections are built-in sources now.
	ids := personalize.RetiredDiscoveryIDs()
	if len(ids) > 0 && a.collections != nil {
		args := make([]any, len(ids))
		marks := make([]string, len(ids))
		for i, id := range ids {
			args[i], marks[i] = id, "?"
		}
		in := strings.Join(marks, ",")
		if _, err := a.db.ExecContext(ctx, `DELETE FROM collections_members WHERE spec_id IN (SELECT id FROM collections_specs WHERE builtin=1 AND slug IN (`+in+`))`, args...); err != nil {
			return fmt.Errorf("collections: delete discovery members: %w", err)
		}
		if _, err := a.db.ExecContext(ctx, `DELETE FROM collections_specs WHERE builtin=1 AND slug IN (`+in+`)`, args...); err != nil {
			return fmt.Errorf("collections: delete discovery collections: %w", err)
		}
	}
	// Add-on catalogue imports keep only "Browse in apps".
	if err := a.migrateAddonImportTargets(ctx); err != nil {
		return err
	}
	// Jellyfin and Emby rows come from Home now: their own row lists go.
	if err := a.cfg.Update(func(c *config.Config) error {
		for _, id := range []string{"jellyfin", "emby"} {
			fe, ok := c.Frontends[id]
			if !ok || fe.Settings == nil {
				continue
			}
			delete(fe.Settings, "homeRows")
			delete(fe.Settings, "homeRowKinds")
			c.Frontends[id] = fe
		}
		return nil
	}); err != nil {
		return fmt.Errorf("config: drop Jellyfin/Emby home row settings: %w", err)
	}
	if err := a.personalize.SetMigrationState(ctx, personalize.RowsPagesMigration, "done"); err != nil {
		return err
	}
	a.log.Info("recommendations: moved Home layout and collection rows onto Recommendations pages", "pages", n, "collections", len(placements))
	return nil
}

// migrateAddonImportTargets rewrites each add-on import's stored targets to
// {"browseInApps": <its old Collections tab switch>}.
func (a *App) migrateAddonImportTargets(ctx context.Context) error {
	rows, err := a.db.QueryContext(ctx, `SELECT addon_id, targets_json FROM collections_addon_imports`)
	if err != nil {
		// No add-on imports table on this database: nothing to rewrite.
		return nil
	}
	type imp struct{ id, raw string }
	var all []imp
	for rows.Next() {
		var i imp
		if err := rows.Scan(&i.id, &i.raw); err != nil {
			rows.Close()
			return err
		}
		all = append(all, i)
	}
	rows.Close()
	for _, i := range all {
		var old struct {
			CollectionsTab *bool `json:"collectionsTab"`
			BrowseInApps   *bool `json:"browseInApps"`
		}
		_ = json.Unmarshal([]byte(i.raw), &old)
		browse := true
		if old.BrowseInApps != nil {
			continue // already rewritten
		}
		if old.CollectionsTab != nil {
			browse = *old.CollectionsTab
		}
		b, _ := json.Marshal(map[string]bool{"browseInApps": browse})
		if _, err := a.db.ExecContext(ctx, `UPDATE collections_addon_imports SET targets_json=? WHERE addon_id=?`, string(b), i.id); err != nil {
			return err
		}
	}
	return nil
}
