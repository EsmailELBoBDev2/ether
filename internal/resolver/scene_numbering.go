package resolver

import (
	"context"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/releaseparse"
)

func (s *Service) numberedRequest(ctx context.Context, req core.SearchRequest, cachedOnly bool) core.SearchRequest {
	if s.episodeNumbering == nil || req.Type != core.TypeEpisode || req.IDs.TMDB <= 0 {
		return req
	}
	if season, episode, err := s.episodeNumbering(ctx, req, cachedOnly); err == nil && season > 0 && episode > 0 && (season != req.Season || episode != req.Episode) {
		req.AltSeason, req.AltEpisode = season, episode
	}
	return req
}

// Canonical discovery always gets the first chance to settle the episode.
// Neither background/detail discovery nor a matching canonical release loads XEM.
func (s *Service) searchWithNumbering(ctx context.Context, req *core.SearchRequest, fresh bool) ([]core.Release, bool, error) {
	results, cached, err := s.searchCached(ctx, *req, fresh)
	if err != nil || req.Type != core.TypeEpisode || core.SpeculativeIndexerWork(ctx) || core.IsBackgroundCheck(ctx) {
		return results, cached, err
	}
	for _, r := range results {
		if numberingMatch(r, *req) {
			return results, cached, nil
		}
	}
	mapped := s.numberedRequest(ctx, *req, false)
	if mapped.AltEpisode == 0 {
		return results, cached, nil
	}
	*req = mapped
	// Reuse already returned releases before spending any further indexer calls.
	results = s.normalizeSearchResults(mapped, append([]core.Release(nil), results...))
	for _, r := range results {
		if numberingMatch(r, mapped) {
			return results, cached, nil
		}
	}
	// Keep canonical identity and the existing both-numbering indexer plumbing.
	// Progressive preparation belongs to the canonical search and must not
	// validate mapped candidates using its original episode numbers.
	ctx = context.WithValue(ctx, searchPreparationKey{}, (*searchPreparation)(nil))
	extra, extraCached, extraErr := s.searchCached(ctx, mapped, fresh)
	if extraErr != nil {
		return results, cached, nil
	}
	return append(results, novelReleases(results, extra)...), cached && extraCached, nil
}

// A different show's same-numbered result cannot settle this show's lookup.
// Season packs count too: a matching canonical pack needs no XEM fetch.
func numberingMatch(rel core.Release, req core.SearchRequest) bool {
	match, conflict := releaseparse.CompareTitleIDs(rel.ReportedTitleIDs(), req.IDs)
	if conflict {
		return false
	}
	if !match && !releaseparse.TitleMatchesAny(rel.Info, req.Title, req.AlternativeTitles) && (req.OriginalTitle == "" || !releaseparse.TitleMatches(rel.Info, req.OriginalTitle)) {
		return false
	}
	return episodeMatchesRequest(rel.Info, req) || (rel.Info.FullSeason && packCoversSeasonCour(rel.Info, req))
}
