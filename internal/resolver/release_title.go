package resolver

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/flight"
	"github.com/braedonsaunders/ether/internal/releaseparse"
)

const releaseTitleTTL = 24 * time.Hour
const releaseTitleMissTTL = 10 * time.Minute
const maxReleaseTitleEntries = 4096
const maxReleaseTitleLookups = 4

type releaseTitleKey struct {
	kind            core.MediaType
	language, title string
}
type releaseTitleVerdict struct {
	ids     core.IDs
	expires time.Time
}
type releaseTitleCache struct {
	mu      sync.Mutex
	seen    map[releaseTitleKey]releaseTitleVerdict
	flights flight.Group[releaseTitleKey, core.IDs]
}

func (s *Service) releaseTitleKey(rel core.Release, req core.SearchRequest) releaseTitleKey {
	kind := core.TypeMovie
	if req.Type == core.TypeEpisode {
		kind = core.TypeShow
	}
	return releaseTitleKey{kind: kind, language: s.catalogLanguage(), title: releaseparse.NormalizeTitle(rel.Info.Title)}
}

func (s *Service) cachedReleaseTitle(key releaseTitleKey) (core.IDs, bool) {
	s.releaseTitles.mu.Lock()
	defer s.releaseTitles.mu.Unlock()
	v, ok := s.releaseTitles.seen[key]
	return v.ids, ok && s.now().Before(v.expires)
}

// prepareReleaseTitles spends a bounded metadata budget only on unmatched
// names. Reported IDs and existing aliases need no lookup. The cache stores
// the provider's answer independently of the requested title's identity.
func (s *Service) prepareReleaseTitles(ctx context.Context, t *core.Title, req core.SearchRequest, results []core.Release) {
	if s.releaseTitleIDs == nil || t == nil || req.IDs == (core.IDs{}) || (req.Type != core.TypeEpisode && req.Type != core.TypeMovie) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	seen := map[releaseTitleKey]bool{}
	lookups := 0
	for _, rel := range results {
		if ctx.Err() != nil {
			return
		}
		// Only the title mismatch gate may be relaxed by this evidence.
		if !strings.HasPrefix(rejectIdentity(rel, t, req, s.catalogLanguage()), "title mismatch") || releaseparse.IsObfuscated(rel.Info.Title) {
			continue
		}
		key := s.releaseTitleKey(rel, req)
		if key.title == "" || seen[key] {
			continue
		}
		seen[key] = true
		if _, ok := s.cachedReleaseTitle(key); ok {
			continue
		}
		if lookups >= maxReleaseTitleLookups {
			break
		}
		lookups++
		_, err := s.releaseTitles.flights.Do(ctx, key, func(ctx context.Context) (core.IDs, error) {
			if ids, ok := s.cachedReleaseTitle(key); ok {
				return ids, nil
			}
			ids, err := s.releaseTitleIDs(ctx, key.kind, rel.Info.Title)
			if err != nil {
				return core.IDs{}, err
			}
			if err := ctx.Err(); err != nil {
				return core.IDs{}, err
			}
			ttl := releaseTitleTTL
			if ids == (core.IDs{}) {
				ttl = releaseTitleMissTTL
			}
			now := s.now()
			s.releaseTitles.mu.Lock()
			defer s.releaseTitles.mu.Unlock()
			if s.releaseTitles.seen == nil {
				s.releaseTitles.seen = map[releaseTitleKey]releaseTitleVerdict{}
			}
			if len(s.releaseTitles.seen) >= maxReleaseTitleEntries {
				var oldest releaseTitleKey
				var expires time.Time
				for k, v := range s.releaseTitles.seen {
					if !now.Before(v.expires) {
						delete(s.releaseTitles.seen, k)
					} else if expires.IsZero() || v.expires.Before(expires) {
						oldest, expires = k, v.expires
					}
				}
				if len(s.releaseTitles.seen) >= maxReleaseTitleEntries {
					delete(s.releaseTitles.seen, oldest)
				}
			}
			s.releaseTitles.seen[key] = releaseTitleVerdict{ids: ids, expires: now.Add(ttl)}
			return ids, nil
		})
		if err != nil {
			s.log.Debug("release title metadata lookup failed; keeping title check", "title", rel.Info.Title, "err", err)
		}
	}
}

func (s *Service) metadataTitleMatches(rel core.Release, req core.SearchRequest) bool {
	ids, ok := s.cachedReleaseTitle(s.releaseTitleKey(rel, req))
	if !ok {
		return false
	}
	match, conflict := releaseparse.CompareTitleIDs(ids, req.IDs)
	return match && !conflict
}
