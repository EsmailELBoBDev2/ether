package plex

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/frontends/plex/model"
)

type collectionCardHubSource interface {
	CollectionCardHubs(context.Context, *frontend.User, frontend.HubContext, int) []frontend.Hub
}

func (s *Server) renderCollectionCardHub(ctx context.Context, pr *Request, hub frontend.Hub) model.Hub {
	out := model.Hub{Title: hub.Title, Type: "collection", HubIdentifier: hub.ID, Style: "shelf", Promoted: model.Ptr(true)}
	vis := s.visibility()
	for _, collection := range hub.Collections {
		if vis.CollectionAllowed(pr.User, &collection) {
			dir := s.collectionDirectory(ctx, &collection)
			dir.Extra = append(dir.Extra, model.A("guid", "collection://"+collection.Key.String()), model.A("summary", collection.Summary))
			out.Directory = append(out.Directory, dir)
		}
	}
	out.Size = len(out.Directory)
	out.More = hub.More && out.Size > 0
	if out.More {
		out.Key = "/hubs/items?identifier=" + url.QueryEscape(hub.ID)
	}
	return out
}

func (s *Server) writeCollectionCardPage(w http.ResponseWriter, r *http.Request, pr *Request, id string) bool {
	if !strings.HasPrefix(id, "home.") || !core.ValidRowID(strings.TrimPrefix(id, "home.")) {
		return false
	}
	source, ok := s.recommender().(collectionCardHubSource)
	if !ok {
		return false
	}
	ctx := r.Context()
	hc := s.hubContext(pr, 0)
	for _, hub := range source.CollectionCardHubs(ctx, pr.User, hc, core.MaxCollectionCards) {
		if hub.ID != id {
			continue
		}
		row := s.renderCollectionCardHub(ctx, pr, hub)
		facts, neutral := homeLayoutFacts(row)
		rows := s.arrangeHomeRows(ctx, pr, hc, "home", []model.Hub{row}, []frontend.Hub{facts}, []frontend.NeutralHomeRow{neutral})
		if len(rows) == 0 {
			return false
		}
		mc := hubContainer()
		dirs := rows[0].Directory
		start, end := pr.window(len(dirs))
		mc.Directory = dirs[start:end]
		mc.Size = len(mc.Directory)
		mc.SetPage(start, len(dirs))
		s.write(w, r, mc)
		return true
	}
	return false
}
