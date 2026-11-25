package plex

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/frontends/plex/model"
)

func (s *Server) handleMetadataMatch(w http.ResponseWriter, r *http.Request) {
	key, err := keyParam(r)
	if err != nil {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	pr := FromContext(r.Context())
	if pr.User.Delegated {
		writeHTTPError(w, http.StatusForbidden)
		return
	}
	matcher := s.getHost().TitleMatch
	if matcher == nil || (key.Kind() != frontend.KindMovie && key.Kind() != frontend.KindShow) {
		s.handlePlaylistMetadataRefusal(w, r)
		return
	}
	ctx := r.Context()
	title, err := s.metadataTitle(ctx, pr, key)
	if err != nil || title == nil || !s.visibleTitle(ctx, pr.User, title) {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	if strings.HasSuffix(r.URL.Path, "/match") {
		guid, ok := strings.CutPrefix(q.Get("guid"), "tmdb://")
		id, err := strconv.ParseInt(guid, 10, 64)
		if !ok || err != nil || id <= 0 {
			writeHTTPError(w, http.StatusBadRequest)
			return
		}
		next, err := matcher.ApplyTitleMatch(ctx, key, id)
		if err != nil {
			writeMatchError(w, err)
			return
		}
		w.Header().Set("Content-Location", "/library/metadata/"+next.String())
		w.WriteHeader(http.StatusOK)
		return
	}
	name := q.Get("title")
	if name == "" {
		name = title.Title
	}
	year, _ := strconv.Atoi(q.Get("year"))
	matches, err := matcher.SearchTitleMatches(ctx, title.Type, name, year)
	if err != nil {
		writeMatchError(w, err)
		return
	}
	mc := &model.MediaContainer{Size: len(matches), Identifier: model.LibraryIdentifier}
	for n, candidate := range matches {
		mc.SearchResult = append(mc.SearchResult, model.SearchResult{
			Name: candidate.Title, Year: candidate.Year,
			Lifespan: strconv.Itoa(candidate.Year),
			GUID:     "tmdb://" + strconv.FormatInt(candidate.IDs.TMDB, 10),
			Score:    float64(max(1, 100-n)),
		})
	}
	s.write(w, r, mc)
}

func writeMatchError(w http.ResponseWriter, err error) {
	if errors.Is(err, core.ErrNotFound) {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	writeHTTPError(w, http.StatusServiceUnavailable)
}
