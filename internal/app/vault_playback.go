package app

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/debrid"
	"github.com/braedonsaunders/ether/internal/nzb"
	"github.com/braedonsaunders/ether/internal/stream"
	"github.com/braedonsaunders/ether/internal/vault"
)

const vaultStreamPrefix = "vault-record:"

func (a *App) vaultStreamOptions(cfg config.Config) stream.Options {
	o := stream.OptionsFromConfig(cfg)
	o.WantLayout = func(id string) bool { return strings.HasPrefix(id, vaultStreamPrefix) }
	o.OnLayout = func(id string, l vault.Layout) {
		if k, ok := strings.CutPrefix(id, vaultStreamPrefix); ok && a.vault != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = a.vault.SetLayout(ctx, vault.Key(k), l)
		}
	}
	mark := func(id string, health vault.Health) {
		if k, ok := strings.CutPrefix(id, vaultStreamPrefix); ok && a.vault != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = a.vault.MarkHealth(ctx, vault.Key(k), health)
		}
	}
	o.OnMissing = func(id string) { mark(id, vault.HealthDead) }
	o.OnHealthy = func(id string) { mark(id, vault.HealthOK) }
	return o
}

func (a *App) openVaultResolution(ctx context.Context, res *core.Resolution) (core.MediaSource, error) {
	backend := playbackEngine(a)
	fallback := func() (core.MediaSource, error) { return backend.Open(ctx, res.Release.ID, res.NZB) }
	r := res.Release
	if debrid.IsPayload(res.NZB) || a.vault == nil || a.engine == nil || backend != a.media || r.Protocol != core.ProtocolUsenet {
		return fallback()
	}
	key := vault.KeyOf(r)
	if key == "" {
		id := r.ID
		if r.Target != nil && r.Target.PackID != "" {
			id = r.Target.PackID
		}
		var err error
		key, _, err = a.vault.ByAlias(ctx, r.IndexerID, id)
		if key == "" && err == nil && id != r.ID {
			key, _, err = a.vault.ByAlias(ctx, r.IndexerID, r.ID)
		}
		if err != nil {
			return nil, err
		}
	}
	if key == "" {
		return fallback()
	}
	rec, err := a.vault.Get(ctx, key)
	if errors.Is(err, vault.ErrNotFound) || errors.Is(err, vault.ErrPayloadMissing) {
		return fallback()
	}
	if err != nil {
		return nil, err
	}
	if rec == nil || rec.NZB == nil {
		return fallback()
	}
	copyRec := *rec
	doc := *rec.NZB
	if t := r.Target; t != nil {
		doc.Meta = maps.Clone(doc.Meta)
		if doc.Meta == nil {
			doc.Meta = map[string][]string{}
		}
		if t.Episode > 0 {
			h := nzb.EpisodeHint{Season: t.Season, Episode: t.Episode, Absolute: t.AbsoluteEpisode}
			for _, v := range t.Aliases {
				h.Aliases = append(h.Aliases, [2]int{v.Season, v.Episode})
			}
			doc.Meta[nzb.MetaEpisode] = []string{h.String()}
		} else if len(t.MovieTitles)+len(t.WantedLanguages) > 0 {
			h := nzb.MovieHint{Titles: t.MovieTitles, Year: t.MovieYear, WantedLanguages: t.WantedLanguages, OriginalLanguage: t.OriginalLanguage}
			doc.Meta[nzb.MetaMovie] = []string{h.String()}
		}
	}
	copyRec.NZB = &doc
	var target []byte
	if len(res.NZB) == 0 && r.Info.FullSeason && r.Target != nil && r.Target.Episode > 0 {
		// A record-only resume has no resolver payload. Recreate the same
		// episode selection as FetchNZB so subsequent warmers see its slots.
		target, err = debrid.EpisodeNZB(rec.XML, r.Target)
		if err != nil {
			return nil, err
		}
	} else if !bytes.Equal(res.NZB, rec.XML) {
		target = res.NZB
	}
	src, err := a.engine.OpenRecordAlias(ctx, r.ID, vaultStreamPrefix+string(key), &copyRec, target)
	if errors.Is(err, stream.ErrInvalidNZB) && len(target) > 0 {
		return fallback()
	}
	// A resolver may have isolated an episode by its outer archive filename.
	// Its narrowed payload remains authoritative when the inner names are opaque.
	if errors.Is(err, stream.ErrNoPlayableFile) && r.Target != nil && len(res.NZB) > 0 {
		return fallback()
	}
	return src, err
}
