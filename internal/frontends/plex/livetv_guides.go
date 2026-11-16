package plex

import (
	"context"
	"encoding/base64"
	"net/url"
	"strings"

	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/frontends/plex/model"
)

// Provider categories are identities, not English classification heuristics.
func liveGenreID(genre string) string {
	return "genre-" + base64.RawURLEncoding.EncodeToString([]byte(strings.ToLower(strings.TrimSpace(genre))))
}

func normalizeLiveCategory(raw string) (string, bool) {
	switch raw {
	case "movies":
		raw = "movie"
	case "tv-shows":
		raw = "series"
	}
	switch raw {
	case "movie", "series", "sports", "news", "kids":
		return raw, true
	}
	if strings.HasPrefix(raw, "genre-") {
		b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(raw, "genre-"))
		return raw, err == nil && len(b) > 0 && len(b) <= 256
	}
	return "", false
}

func matchesLiveCategory(genres []string, category, mediaType string) bool {
	if strings.HasPrefix(category, "genre-") {
		for _, genre := range genres {
			if liveGenreID(genre) == category {
				return true
			}
		}
		return false
	}
	return frontend.LiveCategory(genres, category, mediaType)
}

func liveCategoryGuideFilters() []model.GridChannelFilter {
	return liveCategoryGuideFiltersFor(liveProviderPath)
}
func liveCategoryGuideFiltersFor(providerPath string) []model.GridChannelFilter {
	out := []model.GridChannelFilter{}
	for _, c := range []struct{ id, title string }{{"movie", "Movies"}, {"series", "TV Shows"}, {"sports", "Sports"}, {"news", "News"}, {"kids", "Kids"}} {
		out = append(out, liveCategoryFilterFor(providerPath, c.id, c.title))
	}
	return out
}
func liveCategoryFilter(id, title string) model.GridChannelFilter {
	return liveCategoryFilterFor(liveProviderPath, id, title)
}
func liveCategoryFilterFor(providerPath, id, title string) model.GridChannelFilter {
	return model.GridChannelFilter{Identifier: id, Key: providerPath + "/lineups/dvr/channels?genre=" + url.QueryEscape(id), GenreRatingKey: "ether.category." + id, Title: title}
}

func (s *Server) liveGuideFilters(ctx context.Context, u *frontend.User) []model.GridChannelFilter {
	return s.liveGuideFiltersFor(ctx, u, liveProviderPath, true)
}

// Group providers carry only category pills (scoped to their own path); the
// group pills live on the main provider, where they switch between groups.
func (s *Server) liveGuideFiltersFor(ctx context.Context, u *frontend.User, providerPath string, includeGroups bool) []model.GridChannelFilter {
	out := []model.GridChannelFilter{}
	if includeGroups {
		if g, ok := s.svc().liveTV.(frontend.LiveTVGroups); ok {
			groups, err := g.Groups(ctx, u)
			if err != nil {
				s.log.Warn("live groups unavailable", "error", err)
			}
			for _, g := range groups {
				out = append(out, model.GridChannelFilter{Identifier: "guide-" + g.ID, Key: providerPath + "/guides/" + url.PathEscape(g.ID) + "/channels", GenreRatingKey: "ether.guide." + g.ID, Title: g.Name})
			}
		}
	}
	out = append(out, liveCategoryGuideFiltersFor(providerPath)...)
	if snapshot := s.liveDiscoverySnapshot(ctx, u); snapshot != nil {
		for _, bucket := range snapshot.buckets {
			if strings.HasPrefix(bucket.id, "genre-") {
				out = append(out, liveCategoryFilterFor(providerPath, bucket.id, bucket.title))
			}
		}
	}
	return out
}
