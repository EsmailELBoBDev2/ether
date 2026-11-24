package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/extend"
	"github.com/braedonsaunders/ether/internal/plugins"
	"github.com/braedonsaunders/ether/internal/stremio"
)

type forceCatalogueSyncKey struct{}

func withForceCatalogueSync(ctx context.Context) context.Context {
	return context.WithValue(ctx, forceCatalogueSyncKey{}, true)
}

func isForceCatalogueSync(ctx context.Context) bool {
	v, _ := ctx.Value(forceCatalogueSyncKey{}).(bool)
	return v
}

func (a *App) catalogueSourceType(ctx context.Context, id string) (string, error) {
	ref, err := a.resolveCatalogueSource(ctx, id)
	return ref.typ, err
}

// catalogueSourceRef is what a catalogue source id resolves to: its media
// type and, for a plugin list, the list-source kind it syncs through and
// the name the owner knows it by.
type catalogueSourceRef struct {
	typ   string
	kind  string
	label string
}

// resolveCatalogueSource resolves a catalogue source id. A plugin list
// ("stremio:<plugin>:<catalog key>") resolves through the plugin's
// CatalogueLists, the same list the console offers, so a source the owner
// can attach always syncs through a list-source kind the plugin registers.
// Errors name the source and say why it can't be used.
func (a *App) resolveCatalogueSource(ctx context.Context, id string) (catalogueSourceRef, error) {
	if id == "tmdb-movies" {
		return catalogueSourceRef{typ: "movie"}, nil
	}
	if id == "tmdb-tv" {
		return catalogueSourceRef{typ: "show"}, nil
	}
	parts := strings.SplitN(id, ":", 3)
	if len(parts) != 3 || (parts[0] != "plugin" && parts[0] != "stremio") {
		return catalogueSourceRef{}, fmt.Errorf("unknown catalogue source %q", id)
	}
	if a.pluginsRT == nil || a.pluginsRT.mgr == nil {
		return catalogueSourceRef{}, fmt.Errorf("catalogue source %q can't be used because plugins are not running on this server", id)
	}
	rec, err := a.pluginsRT.mgr.Get(ctx, parts[1])
	if errors.Is(err, plugins.ErrNotFound) {
		return catalogueSourceRef{}, fmt.Errorf("the plugin %q that provided this catalogue source is not installed", parts[1])
	}
	if err != nil {
		return catalogueSourceRef{}, fmt.Errorf("couldn't read the plugin %q: %w", parts[1], err)
	}
	if !rec.Enabled {
		return catalogueSourceRef{}, fmt.Errorf("%s is turned off; turn it on in Plugins to use its catalogue sources", rec.Name)
	}
	if parts[0] == "plugin" {
		if e := rec.Manifest.EffectiveEther(); e != nil {
			for _, k := range e.Contributes.MediaKinds {
				if k.Key == parts[2] {
					return catalogueSourceRef{typ: "plugin:" + id}, nil
				}
			}
		}
		return catalogueSourceRef{}, fmt.Errorf("%s no longer offers the content kind %q; it may have changed in an update", rec.Name, parts[2])
	}
	cl, ok := rec.Manifest.CatalogueList(parts[2])
	if !ok {
		return catalogueSourceRef{}, fmt.Errorf("%s no longer offers this list; it may have changed in an update", rec.Name)
	}
	typ := "movie"
	if cl.Catalog.Type == stremio.TypeSeries {
		typ = "show"
	}
	name := cl.Catalog.Name
	if name == "" {
		name = cl.Label
		if key, ok := strings.CutPrefix(name, "$t:"); ok {
			name, _ = a.pluginsRT.mgr.ResolveText(ctx, rec.ID, key, "en")
		}
	}
	label := rec.Name
	if name = strings.TrimSpace(name); name != "" {
		label = fmt.Sprintf("%s (%s)", name, rec.Name)
	}
	return catalogueSourceRef{typ: typ, kind: extend.PluginID(rec.ID, cl.List), label: label}, nil
}
func (a *App) validateCatalogueSources(ctx context.Context, lib core.Library, ids []string) error {
	if len(ids) == 0 || len(ids) > 32 {
		return fmt.Errorf("choose between one and 32 catalogue sources")
	}
	if lib.Source == core.LibraryCollection {
		return fmt.Errorf("change the collection source from library settings")
	}
	if lib.Source == core.LibraryBuiltin {
		return fmt.Errorf("built-in catalogue sources cannot be changed")
	}
	typ := lib.MediaType
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return fmt.Errorf("duplicate catalogue source")
		}
		seen[id] = true
		sourceType, err := a.catalogueSourceType(ctx, id)
		if err != nil {
			return err
		}
		if lib.Source == core.LibraryPlugin {
			if len(ids) != 1 || id != "plugin:"+lib.PluginID+":"+lib.KindKey {
				return fmt.Errorf("plugin libraries use their own content kind")
			}
		} else if sourceType != string(typ) {
			return fmt.Errorf("choose sources with the same media type")
		}
	}
	return nil
}

// One loader per App; lock only remote refreshes. The persisted timestamp
// avoids network work after the first bounded load for an opened catalogue.
// Each list imports independently under its own timeout so one failing or huge
// list never blocks sibling lists in the same library.
func (a *App) wireCatalogueSources() {
	if a.catalog == nil {
		return
	}
	lock := make(chan struct{}, 1)
	type sourceFailure struct {
		at  time.Time
		err error
	}
	failures := map[string]sourceFailure{}
	recordError := func(parent context.Context, id string, err error) {
		if err == nil {
			return
		}
		errCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
		defer cancel()
		_, _ = a.db.ExecContext(errCtx, `INSERT INTO libraries_catalogue_state(source_id,refreshed,item_count,last_error) VALUES(?,0,0,?) ON CONFLICT(source_id) DO UPDATE SET refreshed=0,last_error=excluded.last_error`, id, err.Error())
	}
	a.catalog.SetCatalogueSourceLoader(func(ctx context.Context, ids []string) error {
		force := isForceCatalogueSync(ctx)
		succeeded := 0
		var firstErr error
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !strings.HasPrefix(id, "stremio:") {
				succeeded++
				continue
			}
			itemCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			ref, err := a.resolveCatalogueSource(itemCtx, id)
			typ := ref.typ
			if err != nil {
				cancel()
				recordError(ctx, id, err)
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			var refreshed int64
			_ = a.db.QueryRowContext(itemCtx, `SELECT refreshed FROM libraries_catalogue_state WHERE source_id=?`, id).Scan(&refreshed)
			if !force && time.Since(time.Unix(refreshed, 0)) < time.Hour {
				cancel()
				succeeded++
				continue
			}
			err = func() error {
				select {
				case lock <- struct{}{}:
				case <-itemCtx.Done():
					return itemCtx.Err()
				}
				defer func() { <-lock }()
				if !force {
					if failed, ok := failures[id]; ok && time.Since(failed.at) < time.Minute {
						return failed.err
					}
					_ = a.db.QueryRowContext(itemCtx, `SELECT refreshed FROM libraries_catalogue_state WHERE source_id=?`, id).Scan(&refreshed)
					if time.Since(time.Unix(refreshed, 0)) < time.Hour {
						return nil
					}
				}
				if a.collections == nil {
					return fmt.Errorf("catalogue sources unavailable")
				}
				titles, err := a.collections.GatherCatalogueSource(itemCtx, ref.kind, ref.label, typ)
				if err != nil {
					failures[id] = sourceFailure{at: time.Now(), err: err}
					return err
				}
				delete(failures, id)
				unique := make([]core.Title, 0, len(titles))
				seen := map[core.RatingKey]bool{}
				for _, title := range titles {
					if title.Type == core.MediaType(typ) && title.IDs.TMDB > 0 && !seen[title.Key] {
						seen[title.Key] = true
						unique = append(unique, title)
					}
				}
				titles = unique
				writeCtx, writeCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				defer writeCancel()
				if err := a.catalog.UpsertCollectionTitles(writeCtx, titles); err != nil {
					return err
				}
				tx, err := a.db.BeginTx(writeCtx, nil)
				if err != nil {
					return err
				}
				defer tx.Rollback()
				if _, err = tx.ExecContext(writeCtx, `DELETE FROM libraries_catalogue_members WHERE source_id=?`, id); err != nil {
					return err
				}
				for _, title := range titles {
					if _, err = tx.ExecContext(writeCtx, `INSERT INTO libraries_catalogue_members(source_id,key) VALUES(?,?) ON CONFLICT DO NOTHING`, id, int64(title.Key)); err != nil {
						return err
					}
				}
				if _, err = tx.ExecContext(writeCtx, `INSERT INTO libraries_catalogue_state(source_id,refreshed,item_count,last_error) VALUES(?,?,?,'') ON CONFLICT(source_id) DO UPDATE SET refreshed=excluded.refreshed,item_count=excluded.item_count,last_error=''`, id, time.Now().Unix(), len(titles)); err != nil {
					return err
				}
				if err = tx.Commit(); err == nil {
					a.catalog.CatalogueSourcesChanged()
				}
				return err
			}()
			cancel()
			if err != nil {
				recordError(ctx, id, err)
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			succeeded++
		}
		if firstErr != nil && succeeded == 0 {
			return firstErr
		}
		return nil
	})
}
