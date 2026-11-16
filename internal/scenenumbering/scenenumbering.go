// Package scenenumbering lazily reads untrusted XEM episode mappings.
// Only a TVDB absolute-number match establishes a TMDB episode's identity.
package scenenumbering

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/egress"
	"github.com/braedonsaunders/ether/internal/httpclient"
	"github.com/braedonsaunders/ether/internal/safefetch"
)

const DefaultURL = "https://thexem.info/map/all"

type Options struct {
	URL    string
	Client *http.Client
	Now    func() time.Time
}
type number struct {
	Season   int `json:"season"`
	Episode  int `json:"episode"`
	Absolute int `json:"absolute"`
}
type entry struct {
	numbers map[int]number
	expires time.Time
	loading chan struct{}
}
type Service struct {
	opts  Options
	mu    sync.Mutex
	shows map[int64]*entry
}

func New(opts Options) *Service {
	if opts.URL == "" {
		opts.URL = DefaultURL
	}
	if opts.Client == nil {
		opts.Client = (&safefetch.Guard{}).Client(&http.Client{Transport: egress.Transport(egress.CapMetadata)})
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Service{opts: opts, shows: map[int64]*entry{}}
}

// Lookup never guesses that TMDB and TVDB seasons have the same layout.
func (s *Service) Lookup(ctx context.Context, req core.SearchRequest) (int, int, error) {
	if req.Type != core.TypeEpisode || req.IDs.TVDB <= 0 || req.Season <= 0 || req.Episode <= 0 {
		return 0, 0, nil
	}
	absolute := req.AbsoluteEpisode
	if absolute <= 0 && req.Season == 1 {
		absolute = req.Episode
	}
	if absolute <= 0 {
		return 0, 0, nil
	}
	numbers, err := s.load(ctx, req.IDs.TVDB)
	n := numbers[absolute]
	return n.Season, n.Episode, err
}

func (s *Service) load(ctx context.Context, id int64) (map[int]number, error) {
	for {
		s.mu.Lock()
		e := s.shows[id]
		if e != nil && s.opts.Now().Before(e.expires) {
			n := e.numbers
			s.mu.Unlock()
			return n, nil
		}
		if e != nil && e.loading != nil {
			wait := e.loading
			s.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		// Bound memory even when many different shows are resolved. No background sweeper.
		if len(s.shows) >= 512 {
			for k, v := range s.shows {
				if v.loading == nil {
					delete(s.shows, k)
					break
				}
			}
		}
		e = &entry{loading: make(chan struct{})}
		s.shows[id] = e
		s.mu.Unlock()
		numbers, err := s.fetch(id)
		ttl := 7 * 24 * time.Hour
		if err != nil {
			ttl = time.Hour
		}
		s.mu.Lock()
		e.numbers, e.expires = numbers, s.opts.Now().Add(ttl)
		close(e.loading)
		e.loading = nil
		s.mu.Unlock()
		return numbers, err
	}
}

func (s *Service) fetch(id int64) (map[int]number, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.opts.URL+"?origin=tvdb&id="+strconv.FormatInt(id, 10), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", httpclient.UserAgent)
	resp, err := s.opts.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	const limit = 4 << 20
	if resp.StatusCode != http.StatusOK || resp.ContentLength > limit {
		return nil, fmt.Errorf("scenenumbering: invalid HTTP response %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if len(body) > limit {
		return nil, fmt.Errorf("scenenumbering: oversized mapping")
	}
	var result struct {
		Result string          `json:"result"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	if result.Result == "failure" {
		// Missing shows and upstream failures both back off without guessing.
		return nil, fmt.Errorf("scenenumbering: no mapping available")
	}
	if result.Result != "success" {
		return nil, fmt.Errorf("scenenumbering: invalid result")
	}
	var rows []struct {
		TVDB   number  `json:"tvdb"`
		Scene  *number `json:"scene"`
		Scene2 *number `json:"scene_2"`
	}
	if err := json.Unmarshal(result.Data, &rows); err != nil {
		return nil, err
	}
	out := map[int]number{}
	ambiguous := map[int]bool{}
	reverse := map[number]int{}
	for _, row := range rows {
		n := row.TVDB
		if row.Scene != nil {
			n = *row.Scene
		}
		abs := row.TVDB.Absolute
		if row.TVDB.Season <= 0 || row.TVDB.Episode <= 0 || abs <= 0 || abs > 1000000 || n.Season <= 0 || n.Season > 10000 || n.Episode <= 0 || n.Episode > 1000000 {
			continue
		}
		n.Absolute = 0
		if row.Scene2 != nil && (row.Scene2.Season != n.Season || row.Scene2.Episode != n.Episode) {
			ambiguous[abs] = true
		}
		if other, ok := reverse[n]; ok && other != abs {
			ambiguous[abs], ambiguous[other] = true, true
		}
		reverse[n] = abs
		if old, ok := out[abs]; ok && old != n {
			ambiguous[abs] = true
		}
		out[abs] = n
	}
	for abs := range ambiguous {
		delete(out, abs)
	}
	return out, nil
}

// LookupCached supports identity checks on browse/detail paths without fetching.
func (s *Service) LookupCached(req core.SearchRequest) (int, int, error) {
	if req.Type != core.TypeEpisode || req.IDs.TVDB <= 0 || req.Season <= 0 {
		return 0, 0, nil
	}
	absolute := req.AbsoluteEpisode
	if absolute <= 0 && req.Season == 1 {
		absolute = req.Episode
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.shows[req.IDs.TVDB]; e != nil {
		n := e.numbers[absolute]
		return n.Season, n.Episode, nil
	}
	return 0, 0, nil
}
