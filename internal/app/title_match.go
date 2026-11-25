package app

import (
	"context"
	"time"

	"github.com/braedonsaunders/ether/internal/catalog"
	"github.com/braedonsaunders/ether/internal/collections"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/favorites"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/playback"
	"github.com/braedonsaunders/ether/internal/state"
	"github.com/braedonsaunders/ether/internal/store"
	"github.com/braedonsaunders/ether/internal/vault"
	"github.com/braedonsaunders/ether/internal/watchlist"
)

var _ frontend.TitleMatcher = (*App)(nil)

func (a *App) SearchTitleMatches(ctx context.Context, typ core.MediaType, query string, year int) ([]core.Title, error) {
	if a.catalog == nil {
		return nil, core.ErrNotAvailable
	}
	return a.catalog.SearchTitleMatches(ctx, typ, query, year)
}

func (a *App) RefreshCatalogTitle(ctx context.Context, key core.RatingKey) error {
	if a.catalog == nil {
		return core.ErrNotAvailable
	}
	if _, err := a.catalog.StoredTitle(ctx, key); err != nil {
		return err
	}
	return a.appTitleRefresher().run(ctx, key)
}

func (a *App) ApplyTitleMatch(ctx context.Context, from core.RatingKey, id int64) (core.RatingKey, error) {
	if a.catalog == nil || id <= 0 || id >= 100_000_000 {
		return 0, core.ErrNotFound
	}
	var to core.RatingKey
	switch from.Kind() {
	case core.KindMovie:
		to = core.MovieKey(id)
	case core.KindShow:
		to = core.ShowKey(id)
	default:
		return 0, core.ErrNotFound
	}
	old, err := a.catalog.StoredTitle(ctx, from)
	if err != nil {
		return 0, err
	}
	// Validate the candidate before publishing anything or moving bindings.
	next, err := a.catalog.RefreshTitle(ctx, to, false)
	if err != nil {
		return 0, err
	}
	if next.Adult {
		return 0, core.ErrNotFound
	}
	if err := a.appTitleRefresher().run(ctx, to); err != nil {
		return 0, err
	}
	if from == to {
		return to, nil
	}
	if err := a.moveTitleBindings(ctx, old, next); err != nil {
		return 0, err
	}
	a.catalog.TitleBindingsChanged(old.Key, next.Key)
	if a.availability != nil {
		a.availability.InvalidateTitleMatches()
	}
	if a.resolver != nil {
		a.resolver.InvalidateDetailVariants()
	}
	return to, nil
}

func (a *App) moveTitleBindings(ctx context.Context, old, next *core.Title) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	dialect := store.DialectOf(a.db)
	for _, key := range []core.RatingKey{min(old.Key, next.Key), max(old.Key, next.Key)} {
		if err := store.LockKey(ctx, dialect, tx, "title-match:"+key.String()); err != nil {
			return err
		}
	}

	ranges := core.TitleMatchRanges(old.Key, next.Key)
	changedCollections, err := collections.RekeyTitle(ctx, tx, old.Key, next.Key, ranges)
	if err != nil {
		return err
	}
	if err := state.RekeyTitle(ctx, tx, ranges); err != nil {
		return err
	}
	if err := favorites.RekeyTitle(ctx, tx, ranges); err != nil {
		return err
	}
	if err := a.availability.RekeyTitle(ctx, tx, ranges, next); err != nil {
		return err
	}
	var changedPins []core.RatingKey
	if a.resolver != nil {
		changedPins, err = a.resolver.RekeyTitle(ctx, tx, ranges, next.Title)
		if err != nil {
			return err
		}
	}
	if err := playback.RekeyTitle(ctx, tx, ranges); err != nil {
		return err
	}
	if err := watchlist.RekeyTitle(ctx, tx, old, next); err != nil {
		return err
	}
	if err := vault.RekeyTitle(ctx, tx, old, next); err != nil {
		return err
	}
	if err := catalog.RekeyTitle(ctx, tx, ranges, next.Key); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if a.resolver != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		a.resolver.TitlePinsRebound(ctx, changedPins)
	}
	if a.collections != nil {
		a.collections.TitleMatchesChanged(changedCollections)
	}
	return nil
}
