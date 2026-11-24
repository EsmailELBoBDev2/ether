package collections

import (
	"context"
	"strconv"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

// RetiredDiscoverySource is what one of the retired Ether-managed
// "Discovery sources" collections (ids 900001–900013, TMDB lists wrapped as
// collections so they could be placed) becomes on a Recommendations page:
// a built-in row source with parameters, drawing from the catalogue's
// movie (1) or TV (2) library.
type RetiredDiscoverySource struct {
	Source  core.RowSource
	Library core.LibraryID
}

// RetiredDiscovery maps each retired discovery collection's public id to
// its built-in source, for the one-time rows-pages-v1 migration. The kinds
// are the personalize row source kinds.
var RetiredDiscovery = map[string]RetiredDiscoverySource{
	"900001": {core.RowSource{Kind: "trending", Params: map[string]string{"window": "week"}}, 1},
	"900002": {core.RowSource{Kind: "trending", Params: map[string]string{"window": "week"}}, 2},
	"900003": {core.RowSource{Kind: "trending", Params: map[string]string{"window": "day"}}, 1},
	"900004": {core.RowSource{Kind: "trending", Params: map[string]string{"window": "day"}}, 2},
	"900005": {core.RowSource{Kind: "top-rated"}, 1},
	"900006": {core.RowSource{Kind: "top-rated"}, 2},
	"900007": {core.RowSource{Kind: "popular"}, 1},
	"900008": {core.RowSource{Kind: "popular"}, 2},
	"900009": {core.RowSource{Kind: "new-releases"}, 1},
	"900010": {core.RowSource{Kind: "upcoming"}, 1},
	"900011": {core.RowSource{Kind: "airing-today", Params: map[string]string{"window": "day"}}, 2},
	"900012": {core.RowSource{Kind: "airing-today", Params: map[string]string{"window": "week"}}, 2},
	"900013": {core.RowSource{Kind: "new-releases"}, 1},
}

// DeleteRetiredDiscovery removes the retired discovery collections and
// their members. The rows-pages-v1 migration calls it after mapping their
// placements to built-in sources; a collection the owner duplicated is an
// ordinary owner collection and stays.
func (s *Service) DeleteRetiredDiscovery(ctx context.Context) error {
	ids := make([]any, 0, len(RetiredDiscovery))
	for id := range RetiredDiscovery {
		n, _ := strconv.ParseInt(id, 10, 64)
		ids = append(ids, n)
	}
	return s.purgeSpecs(ctx, `id IN (`+store.Placeholders(len(ids))+`)`, ids...)
}

// purgeSpecs deletes the collections matching where, with their members.
func (s *Service) purgeSpecs(ctx context.Context, where string, args ...any) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM collections_specs WHERE `+where, args...)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(ids) == 0 {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM collections_members WHERE spec_id=?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM collections_specs WHERE id=?`, id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.forgetRowMemo(ctx)
	for _, id := range ids {
		s.changed(id)
	}
	return nil
}
