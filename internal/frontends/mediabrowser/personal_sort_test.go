package mediabrowser

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/frontend"
)

// A personal row opened as a folder keeps its own order for the default
// SortName-ascending open, and follows any other choice from the sort menu.
func TestPersonalRowFolderHonoursSortMenu(t *testing.T) {
	rows := func() []frontend.Title {
		return []frontend.Title{
			{Title: "Beta", Rating: 7.1, Released: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)},
			{Title: "Alpha", Rating: 9.4, Released: time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC)},
			{Title: "Gamma", Rating: 8.2, Released: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)},
		}
	}
	names := func(titles []frontend.Title) string {
		out := ""
		for _, title := range titles {
			out += title.Title[:1]
		}
		return out
	}
	for _, tc := range []struct {
		query string
		asked bool
		want  string
	}{
		{"SortBy=SortName&SortOrder=Ascending", false, "BAG"},
		{"", false, "BAG"},
		{"sortBy=CommunityRating&sortBy=SortName&sortOrder=Descending", true, "AGB"},
		{"SortBy=CommunityRating&SortOrder=Ascending", true, "BGA"},
		{"SortBy=PremiereDate&SortOrder=Descending", true, "GBA"},
		{"SortBy=SortName&SortOrder=Descending", true, "GBA"},
	} {
		r := httptest.NewRequest(http.MethodGet, "/Items?"+tc.query, nil)
		q := itemQuery(r)
		titles := rows()
		asked := personalSortAsked(r, q)
		if asked {
			sortPersonalTitles(titles, q)
		}
		if asked != tc.asked || names(titles) != tc.want {
			t.Fatalf("%q: asked %v order %s, want %v %s", tc.query, asked, names(titles), tc.asked, tc.want)
		}
	}
}
