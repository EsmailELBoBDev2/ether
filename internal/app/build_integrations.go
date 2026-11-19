package app

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/admin"
	"github.com/braedonsaunders/ether/internal/cachefill"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/episodeorder"
	"github.com/braedonsaunders/ether/internal/playback"
	"github.com/braedonsaunders/ether/internal/spike"
	"github.com/braedonsaunders/ether/internal/transcode"
	"github.com/braedonsaunders/ether/internal/trickplay"
)

func (a *App) buildIntegrations(ctx context.Context, cfg config.Config) error {
	var err error
	var sharedShelf *cachefill.SharedStore
	if a.cache.Shared() {
		sharedShelf = cachefill.NewSharedStore(a.db, a.cacheNodeID())
	}
	a.cachefill, err = cachefill.New(a.cache.Directory(), cachefill.Deps{
		Shared:            sharedShelf,
		Allowed:           a.safety.Allowed,
		WatchlistPosition: a.cacheWatchlistPosition,
		WatchlistIntents:  a.cacheWatchlistIntents, WatchlistDone: a.cacheWatchlistDone,
		Catalog: a.catalog, Resolver: a.resolver, Engine: a.media, Cache: a.cache,
		State: a.state, Household: a.cacheHousehold,
		Config: a.runningCacheConfig, Owner: a.users.Owner,
		Active: func() bool {
			if len(a.playback.Sessions()) > 0 {
				return true
			}
			if a.cache.Shared() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				return len(a.playback.RemoteSessions(ctx)) > 0
			}
			return false
		},
		FreeBytes: func() (int64, error) { return admin.DiskFree(a.cache.Directory()) },
		Remember:  a.playback.Remember, Logger: a.log,
	})
	if err != nil {
		return fmt.Errorf("managed cache: %w", err)
	}
	a.buildInsights()
	a.buildDiscovery()
	a.episodeorder = episodeorder.New(a.db, a.tmdb, a.catalog)
	if err := a.buildLiveTV(); err != nil {
		return fmt.Errorf("live TV: %w", err)
	}
	a.buildCalendar()
	a.cfg.Subscribe(a.applyConfig)

	if err := a.buildAvailability(); err != nil {
		return err
	}
	a.buildExtras()
	if err := a.buildPrerolls(); err != nil {
		return fmt.Errorf("pre-rolls: %w", err)
	}
	if err := a.buildExtensibility(ctx, cfg); err != nil {
		return err
	}
	if a.transcoder != nil {
		if a.subtitles != nil {
			a.subtitles.SetFFmpeg(a.transcoder.FFmpegPath())
		}
		a.trickplay = trickplay.New(transcode.NewThumbnails(transcode.ThumbOptions{FFmpeg: a.transcoder.FFmpegPath(), Logger: a.log}))
		a.closers = append(a.closers, func() { _ = a.trickplay.Close() })
	}
	a.frontendClients = newFrontendClients(a.db, a.log.With("component", "frontend-clients"), nil)
	a.closers = append(a.closers, a.frontendClients.Close)
	if cfg.Spike.Enabled {
		a.spikeLibrary, err = spike.New(ctx, a.cfg.Read, spike.Options{Logger: a.log.With("component", "spike")})
		if err != nil {
			return fmt.Errorf("local library: %w", err)
		}
		if mgr, ok := a.spikeLibrary.Playback().(*playback.Manager); ok && mgr != nil {
			mgr.Subscribe(func(playback.Event) {
				a.notifySessionsChanged()
			})
		}
		a.closers = append(a.closers, func() { _ = a.spikeLibrary.Close() })
	}
	if a.transcoder != nil && !cfg.Spike.Enabled {
		if download, e := a.transcoder.NewDownloadRemuxer(cfg.Downloads); e == nil {
			a.downloadRemuxer = download
		} else {
			a.log.Warn("offline downloads unavailable", "err", e)
		}
	}
	a.frontends, err = newFrontendRuntime(a, cfg)
	if err != nil {
		return fmt.Errorf("front-end registry: %w", err)
	}
	a.closers = append(a.closers, a.frontends.close)
	a.cfg.Subscribe(a.frontends.reconcile)
	mux := http.NewServeMux()
	a.mountAdapterMedia(mux)
	mux.Handle("/ether/", a.perf.wrap("console", lanOnly(a.buildAdmin(), a.cfg.TrustedProxyCIDRs, a.cfg.AllowRemoteConsole)))
	a.mountExtensibility(mux)
	mux.HandleFunc("/ether", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ether/", http.StatusFound)
	})
	mux.Handle("/", browserRootRedirect(a.frontends.plexHandler()))
	a.handler = mux
	return nil
}

func browserRootRedirect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isBrowserRootRequest(r) {
			http.Redirect(w, r, "/ether/", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isBrowserRootRequest(r *http.Request) bool {
	if r.URL.Path != "/" || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		return false
	}
	if !acceptsHTML(r.Header) {
		return false
	}
	for k := range r.Header {
		if len(k) >= len("X-Plex-") && strings.EqualFold(k[:len("X-Plex-")], "X-Plex-") {
			return false
		}
	}
	return !hasPlexTokenQuery(r.URL.RawQuery)
}

func acceptsHTML(h http.Header) bool {
	for k, vs := range h {
		if !strings.EqualFold(k, "Accept") {
			continue
		}
		for _, v := range vs {
			for _, part := range strings.Split(v, ",") {
				mt, _, _ := strings.Cut(part, ";")
				if strings.EqualFold(strings.TrimSpace(mt), "text/html") {
					return true
				}
			}
		}
	}
	return false
}

func hasPlexTokenQuery(raw string) bool {
	for raw != "" {
		var pair string
		pair, raw, _ = strings.Cut(raw, "&")
		if pair == "" {
			continue
		}
		key, _, _ := strings.Cut(pair, "=")
		if unescaped, err := url.QueryUnescape(key); err == nil {
			key = unescaped
		}
		if strings.EqualFold(key, "X-Plex-Token") {
			return true
		}
	}
	return false
}
