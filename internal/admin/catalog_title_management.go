package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
)

type CatalogTitleManagement interface {
	frontend.TitleMatcher
	RefreshCatalogTitle(context.Context, core.RatingKey) error
}

func (h *Handler) handleCatalogTitleManagement(w http.ResponseWriter, r *http.Request) {
	key, err := core.ParseRatingKey(r.PathValue("key"))
	if err != nil || (key.Kind() != core.KindMovie && key.Kind() != core.KindShow) {
		writeError(w, http.StatusNotFound, "not_found", http.StatusText(http.StatusNotFound))
		return
	}
	mgmt := h.deps.TitleManagement
	if mgmt == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", http.StatusText(http.StatusServiceUnavailable))
		return
	}
	switch {
	case r.Method == http.MethodGet:
		typ := core.TypeMovie
		if key.Kind() == core.KindShow {
			typ = core.TypeShow
		}
		q := r.URL.Query()
		year, _ := strconv.Atoi(q.Get("year"))
		matches, err := mgmt.SearchTitleMatches(r.Context(), typ, q.Get("q"), year)
		if err != nil {
			writeTitleManagementError(w, err)
			return
		}
		type matchCard struct {
			TitleCard
			TMDB int64 `json:"tmdbId"`
		}
		out := make([]matchCard, 0, len(matches))
		for _, t := range matches {
			out = append(out, matchCard{TitleCard: CardFor(t), TMDB: t.IDs.TMDB})
		}
		writeJSON(w, http.StatusOK, out)
	case r.Method == http.MethodPut:
		var body struct {
			TMDB int64 `json:"tmdbId"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil || body.TMDB <= 0 {
			writeError(w, http.StatusBadRequest, "validation", http.StatusText(http.StatusBadRequest))
			return
		}
		next, err := mgmt.ApplyTitleMatch(r.Context(), key, body.TMDB)
		if err != nil {
			writeTitleManagementError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"key": next.String()})
	default:
		if err := mgmt.RefreshCatalogTitle(r.Context(), key); err != nil {
			writeTitleManagementError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func writeTitleManagementError(w http.ResponseWriter, err error) {
	if errors.Is(err, core.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", http.StatusText(http.StatusNotFound))
		return
	}
	writeError(w, http.StatusServiceUnavailable, "unavailable", http.StatusText(http.StatusServiceUnavailable))
}
