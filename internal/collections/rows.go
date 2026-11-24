package collections

import (
	"context"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// rowItems is how many titles a collection row carries per media type.
const rowItems = 24

// rowPageKeys caps keys for a Home or section shelf. Mixed collections keep up
// to rowItems movies and up to rowItems shows so neither section's shelf is
// starved when the other media type clusters near the front of the list.
func rowPageKeys(keys []core.RatingKey, mediaType string) ([]core.RatingKey, bool) {
	if mediaType != MediaMixed {
		if len(keys) <= rowItems {
			return keys, false
		}
		return keys[:rowItems], true
	}
	var movies, shows int
	var dropped bool
	out := make([]core.RatingKey, 0, min(len(keys), rowItems*2))
	for _, k := range keys {
		switch k.Kind() {
		case core.KindMovie:
			if movies >= rowItems {
				dropped = true
				continue
			}
			movies++
		case core.KindShow:
			if shows >= rowItems {
				dropped = true
				continue
			}
			shows++
		default:
			if len(out) >= rowItems*2 {
				dropped = true
				continue
			}
		}
		out = append(out, k)
	}
	return out, dropped
}

// homeRowWait bounds how long a single collection row may block a page's
// row assembly before its build finishes in the background.
const homeRowWait = 500 * time.Millisecond

// LayoutRow serves a collection or plugin list a Recommendations page
// places as a row. Private ownership and viewer content limits apply; the
// page row supplies placement and schedule.
func (s *Service) LayoutRow(ctx context.Context, user *core.User, hc core.HubContext, id string) (*core.CollectionRow, error) {
	var row *specRow
	var err error
	pluginRow := strings.HasPrefix(strings.TrimSpace(id), "plugin:")
	if pluginRow {
		row, err = s.ensurePluginLayoutRow(ctx, strings.TrimSpace(id))
	} else {
		row, err = s.rowByID(ctx, id)
	}
	if err != nil {
		return nil, err
	}
	if !row.enabled || row.owner != 0 && (user == nil || row.owner != user.ID) {
		return nil, core.ErrNotFound
	}
	rctx, rcancel := context.WithTimeout(ctx, homeRowWait)
	keys, err := s.serve(rctx, row, user, true)
	rcancel()
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, nil
	}
	count := len(keys)
	page, dropped := rowPageKeys(keys, row.spec.MediaType)
	items, err := s.mirror.Titles(ctx, page)
	if err != nil {
		return nil, err
	}
	hubID := "collection." + row.publicID()
	title := row.spec.Name
	if pluginRow {
		hubID = row.slug
		if kind, ok := s.kindRegistry().Lookup(row.slug); ok {
			if lbl := strings.TrimSpace(kind.Describe().Label.Resolved); lbl != "" {
				title = lbl
			}
		}
	}
	hub := core.Hub{ID: hubID, Title: title, Style: "shelf", PreviewOnFocus: row.spec.PreviewOnFocus, RankedTop10: row.spec.RankedTop10, Promoted: true, Items: items, More: dropped || count > len(items), Key: row.key(), Context: "hub.home.collection"}
	switch row.spec.MediaType {
	case MediaMovie:
		hub.Type = core.TypeMovie
	case MediaShow:
		hub.Type = core.TypeShow
	}
	return &core.CollectionRow{Hub: hub, CollectionID: row.publicID()}, nil
}
