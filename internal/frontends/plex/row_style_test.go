package plex

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontends/plex/model"
	"github.com/braedonsaunders/ether/internal/personalize"
)

// The owner's row style and Hide watched, set in the Home designer, reach
// the Plex hubs: Hero rows go out as style=hero (a shelf while no title has
// hero art), Wide is a landscape shelf, and watched titles leave the row.
func TestOwnerRowStyleAndHideWatchedReachPlexHubs(t *testing.T) {
	f := newFixture()
	defer f.srv.Close()
	e, err := personalize.New(personalize.Options{DB: discoveryDB(t), Catalog: f.catalog, State: f.srv.state(), RefreshEvery: -1, FlushEvery: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	on := true
	if _, err := e.SetHomeLayout(context.Background(), core.HomeLayout{Global: []core.LayoutPage{{Section: "home", Rows: []core.RowPolicy{
		{Row: "row.lead", Status: "always", Slot: 1, HideWatched: &on},
		{Row: "row.hero", Status: "always", Slot: 2, Style: core.RowStyleHero},
		{Row: "row.cold", Status: "always", Slot: 3, Style: core.RowStyleHero},
		{Row: "row.wide", Status: "always", Slot: 4, Style: core.RowStyleWide},
	}}}}); err != nil {
		t.Fatal(err)
	}
	f.srv.setRecommender(e)

	art := func(key string) model.Metadata {
		return model.Metadata{RatingKey: key, Type: "movie", Title: key, Thumb: "/t/" + key, Art: "/a/" + key}
	}
	watched := art("1")
	watched.ViewCount = 1
	cold := model.Metadata{RatingKey: "5", Type: "movie", Title: "5", Thumb: "/t/5"}
	rows := []model.Hub{
		{HubIdentifier: "row.wide", Style: "shelf", Size: 1, Metadata: []model.Metadata{art("6")}},
		{HubIdentifier: "row.cold", Style: "shelf", Size: 1, Metadata: []model.Metadata{cold}},
		{HubIdentifier: "row.hero", Style: "shelf", Size: 1, Metadata: []model.Metadata{art("3")}},
		{HubIdentifier: "row.lead", Style: "shelf", Size: 2, Metadata: []model.Metadata{watched, art("2")}},
	}
	got := heroReadyHomeHubs(stableHomeHubs(nil, f.srv.finalizeHomeRows(context.Background(), &Request{User: &ownerUser}, core.HubContext{Now: fixedNow}, "home", rows)))
	byID := map[string]model.Hub{}
	order := []string{}
	for _, h := range got {
		byID[h.HubIdentifier] = h
		order = append(order, h.HubIdentifier)
	}
	if len(order) != 4 || order[0] != "row.lead" || order[1] != "row.hero" {
		t.Fatalf("order = %v", order)
	}
	if lead := byID["row.lead"]; lead.Style != "hero" || len(lead.Metadata) != 1 || lead.Metadata[0].RatingKey != "2" || lead.Size != 1 {
		t.Fatalf("Hide watched lead = %+v", lead)
	}
	if h := byID["row.hero"]; h.Style != "hero" {
		t.Fatalf("owner Hero row style = %q", h.Style)
	}
	if h := byID["row.cold"]; h.Style != "shelf" || len(h.Metadata) != 1 {
		t.Fatalf("artless owner Hero row should fall back to a shelf: %+v", h)
	}
	if h := byID["row.wide"]; h.Style != "shelf" || h.Metadata[0].Thumb != "/a/6" {
		t.Fatalf("Wide row should be a landscape shelf: %+v", h)
	}
}
