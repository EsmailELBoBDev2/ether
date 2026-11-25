package mediabrowser

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/frontend/kit/ids"
)

type remoteMatch struct {
	Name           string            `json:"Name"`
	ProductionYear int               `json:"ProductionYear,omitempty"`
	ProviderIDs    map[string]string `json:"ProviderIds"`
	SearchProvider string            `json:"SearchProviderName,omitempty"`
	Overview       string            `json:"Overview,omitempty"`
	ImageURL       string            `json:"ImageUrl,omitempty"`
}

func (i *instance) searchRemoteMatch(typ frontend.MediaType) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := i.requireAdmin(w, r, false); !ok {
			return
		}
		var body struct {
			SearchInfo remoteMatch `json:"SearchInfo"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if i.host.TitleMatch == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		query := body.SearchInfo.Name
		if id := body.SearchInfo.tmdbID(); id > 0 {
			query = "tmdb-" + strconv.FormatInt(id, 10)
		}
		titles, err := i.host.TitleMatch.SearchTitleMatches(i.identityContext(r), typ, query, body.SearchInfo.ProductionYear)
		if err != nil {
			writeRemoteMatchError(w, err)
			return
		}
		out := make([]remoteMatch, 0, len(titles))
		for _, t := range titles {
			match := remoteMatch{Name: t.Title, ProductionYear: t.Year, Overview: t.Summary,
				SearchProvider: "TheMovieDb", ProviderIDs: map[string]string{"Tmdb": strconv.FormatInt(t.IDs.TMDB, 10)}}
			if core.ValidArtworkPath(t.PosterPath) {
				match.ImageURL = "https://image.tmdb.org/t/p/w342" + t.PosterPath
			}
			out = append(out, match)
		}
		jsonReply(w, out)
	}
}

func (i *instance) applyRemoteMatch(w http.ResponseWriter, r *http.Request) {
	viewer, ok := i.requireAdmin(w, r, false)
	if !ok {
		return
	}
	key, err := ids.ParseRatingKeyGUID(r.PathValue("itemId"))
	if err != nil || (key.Kind() != frontend.KindMovie && key.Kind() != frontend.KindShow) {
		http.NotFound(w, r)
		return
	}
	if _, ok := i.authorizePlaybackItem(r, viewer, key); !ok {
		http.NotFound(w, r)
		return
	}
	var body remoteMatch
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	id := body.tmdbID()
	if id <= 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if i.host.TitleMatch == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	next, err := i.host.TitleMatch.ApplyTitleMatch(i.identityContext(r), key, id)
	if err != nil {
		writeRemoteMatchError(w, err)
		return
	}
	w.Header().Set("Content-Location", "/Items/"+ids.RatingKeyGUID(next))
	w.WriteHeader(http.StatusNoContent)
}

func (m remoteMatch) tmdbID() int64 {
	for provider, value := range m.ProviderIDs {
		if strings.EqualFold(provider, "tmdb") {
			id, _ := strconv.ParseInt(value, 10, 64)
			return id
		}
	}
	return 0
}

func writeRemoteMatchError(w http.ResponseWriter, err error) {
	if errors.Is(err, core.ErrNotFound) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
}
