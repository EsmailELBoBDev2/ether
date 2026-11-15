package catalog

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/releaseparse"
	"github.com/braedonsaunders/ether/internal/store"
	"github.com/braedonsaunders/ether/internal/tmdb"
)

func TestLocalizedNamesAcrossMetadataPaths(t *testing.T) {
	ctx := context.Background()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		want := "no-NO"
		if calls > 6 {
			want = "en-US"
		}
		if got := r.URL.Query().Get("language"); got != want {
			t.Errorf("language = %q, want %q", got, want)
		}
		appendices := strings.Split(r.URL.Query().Get("append_to_response"), ",")
		if !slices.Contains(appendices, "translations") || !slices.Contains(appendices, "alternative_titles") {
			t.Errorf("missing appended titles: %s", r.URL)
		}
		// Region-specific aliases come after enough unrelated aliases to fill
		// the storage cap. Primary translations are not alternative_titles.
		var aliases []string
		for i := range 30 {
			aliases = append(aliases, fmt.Sprintf(`{"iso_3166_1":"US","title":"Working Name %d"}`, i))
		}
		aliases = append(aliases, `{"iso_3166_1":"NO","title":"Superfamilien"}`)
		fmt.Fprintf(w, `{"id":508,"title":"The Incredibles","original_title":"The Incredibles","name":"The Incredibles","original_name":"The Incredibles","release_date":"2004-10-27","alternative_titles":{"titles":[%s]},"translations":{"translations":[{"iso_639_1":"fr","iso_3166_1":"FR","data":{"title":"Les Indestructibles","name":"Les Indestructibles"}},{"iso_639_1":"no","iso_3166_1":"NO","data":{"title":"De utrolige","name":"De utrolige"}},{"iso_639_1":"nb","iso_3166_1":"NO","data":{"title":"","name":""}}]}}`, strings.Join(aliases, ","))
	}))
	defer srv.Close()
	cfg := config.Catalog{TMDBKey: "test", Language: "nb-NO", Region: "NO"}
	client := tmdb.NewFromConfig(cfg, slog.Default(), tmdb.WithBaseURL(srv.URL), tmdb.WithCacheTTL(0))
	defer client.Close()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c, err := New(db, client, cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	check := func(title core.Title) {
		t.Helper()
		if title.Title != "De utrolige" || title.SortTitle != "de utrolige" || title.OriginalTitle != "The Incredibles" {
			t.Fatalf("localized identity: %+v", title)
		}
		if !slices.Contains(title.AlternativeTitles, "Superfamilien") || len(title.AlternativeTitles) > maxAltTitles || slices.Contains(title.AlternativeTitles, "Les Indestructibles") {
			t.Fatalf("aliases: %v", title.AlternativeTitles)
		}
		if err := c.upsert(ctx, title, upsertOpts{detail: true}); err != nil {
			t.Fatal(err)
		}
		stored, err := c.loadByKey(ctx, title.Key)
		if err != nil {
			t.Fatal(err)
		}
		if stored.t.Title != title.Title || !slices.Contains(stored.t.AlternativeTitles, "Superfamilien") {
			t.Fatalf("stored names: %+v", stored.t)
		}
		// The resolver passes these hydrated fields to TitleMatches.
		if !releaseparse.TitleMatches(releaseparse.Parse("De.Utrolige.2004.NORWEGIAN.DVDRip"), stored.t.Title) ||
			!slices.ContainsFunc(stored.t.AlternativeTitles, func(name string) bool {
				return releaseparse.TitleMatches(releaseparse.Parse("Superfamilien.2004.NORWEGIAN.DVDRip"), name)
			}) {
			t.Fatal("localized release title did not match")
		}
	}
	for _, fetch := range []func(context.Context, int64) (*tmdb.MovieDetails, error){client.MovieSummary, client.MovieDetails, client.MovieDetailsFresh} {
		d, err := fetch(ctx, 508)
		if err != nil {
			t.Fatal(err)
		}
		check(c.movieSummaryToTitle(ctx, d))
		check(c.movieDetailsToTitle(ctx, d))
	}
	for _, fetch := range []func(context.Context, int64) (*tmdb.TVDetails, error){client.TVSummary, client.TVDetails, client.TVDetailsFresh} {
		d, err := fetch(ctx, 508)
		if err != nil {
			t.Fatal(err)
		}
		check(c.tvSummaryToTitle(ctx, d))
		check(c.tvDetailsToTitle(ctx, d))
	}
	if calls != 6 {
		t.Fatalf("requests = %d, want one per existing metadata fetch", calls)
	}
	cfg.Language, cfg.Region = "en-US", "US"
	client.UpdateConfig(cfg)
	c.UpdateConfig(cfg)
	d, err := client.MovieDetails(ctx, 508)
	if err != nil {
		t.Fatal(err)
	}
	if title := c.movieDetailsToTitle(ctx, d); title.Title != "The Incredibles" || slices.Contains(title.AlternativeTitles, "De utrolige") {
		t.Fatalf("English metadata changed: %+v", title)
	}
}
