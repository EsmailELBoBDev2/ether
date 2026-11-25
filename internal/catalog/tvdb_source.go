package catalog

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
	"golang.org/x/sync/errgroup"
)

// TVDBSourceTitles resolves a bounded upstream page through exact TVDB IDs.
// Only unambiguous TMDB show matches are admitted by the current title store.
func (c *Catalog) TVDBSourceTitles(ctx context.Context, ids []int64) ([]core.Title, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	local, err := c.queryTitlesUnfiltered(ctx, `SELECT `+titleCols+` FROM catalog_titles
		WHERE type='show' AND tvdb IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64][]core.Title, len(local))
	for _, t := range local {
		if t.IDs.TMDB > 0 {
			byID[t.IDs.TVDB] = append(byID[t.IDs.TVDB], t)
		}
	}
	remote := c.tmdb()
	results := make([]core.Title, len(ids))
	matches, matchCtx := errgroup.WithContext(ctx)
	matches.SetLimit(8)
	for i, id := range ids {
		if len(byID[id]) > 0 {
			if len(byID[id]) == 1 {
				results[i] = byID[id][0]
			}
			continue
		}
		matches.Go(func() error {
			if remote == nil {
				return errors.New("catalog: TMDB client unavailable")
			}
			found, err := remote.FindByExternalID(matchCtx, "tvdb_id", strconv.FormatInt(id, 10))
			if err != nil {
				return err
			}
			if len(found.TVResults) == 1 && found.TVResults[0].ID > 0 && !found.TVResults[0].Adult {
				t := c.tvToTitle(matchCtx, found.TVResults[0])
				t.IDs.TVDB = id
				results[i] = t
			}
			return nil
		})
	}
	if err := matches.Wait(); err != nil {
		return nil, err
	}
	titles := results[:0]
	for _, t := range results {
		if t.Key != 0 {
			titles = append(titles, t)
		}
	}
	return titles, nil
}
