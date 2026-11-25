package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

// The one-time move off per-media profiles: twins that rank alike merge
// (taking the episode-only settings from the TV one), twins the owner made
// different are both kept, and every reference keeps its profile.
func TestMigrateMediaProfiles(t *testing.T) {
	raw := `{"quality":{"default":"4K","remote":"","defaultMovie":"4K","defaultTv":"1080p","remoteMovie":"1080p","remoteTv":"1080p",
	"rules":[{"product":"Roku","profile":"1080p"},{"platform":"iOS","mediaType":"tv","profile":"1080p"},{"userId":3,"profile":"4K"}],
	"profiles":[
	 {"name":"4K","mediaType":"movie","maxHeight":2160,"minHeight":720,"customFormatScores":{"ether:remux":10}},
	 {"name":"1080p","mediaType":"movie","maxHeight":1080,"minHeight":720},
	 {"name":"4K","mediaType":"tv","maxHeight":2160,"minHeight":720,"episodeMaxSizeMbPerMin":80,"customFormatScores":{"ether:remux":10,"ether:series-year-differs":-10000}},
	 {"name":"1080p","mediaType":"tv","maxHeight":1080,"minHeight":480}
	]}}`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	q := store.Get().Quality
	var names []string
	for _, p := range q.Profiles {
		names = append(names, p.Name)
	}
	if !reflect.DeepEqual(names, []string{"4K", "1080p", "1080p (TV)"}) {
		t.Fatalf("profiles = %v", names)
	}
	uhd := q.Profiles[0]
	if uhd.EpisodeMaxSizeMBPerMin != 80 || uhd.CustomFormatScores[core.FormatSeriesYearDiffers] != -10000 || uhd.CustomFormatScores["ether:remux"] != 10 {
		t.Fatalf("merged 4K = %+v", uhd)
	}
	if q.Profiles[2].MinHeight != 480 {
		t.Fatalf("kept TV profile = %+v", q.Profiles[2])
	}
	if q.Default != "4K" || q.Remote != "" {
		t.Fatalf("defaults = %q / %q", q.Default, q.Remote)
	}
	wantRules := []QualityRule{
		{Product: "Roku", MediaType: core.MediaMovie, Profile: "1080p"},
		{Product: "Roku", MediaType: core.MediaTV, Profile: "1080p (TV)"},
		{Platform: "iOS", MediaType: core.MediaTV, Profile: "1080p (TV)"},
		{UserID: 3, Profile: "4K"},
	}
	if !reflect.DeepEqual(q.Rules, wantRules) {
		t.Fatalf("rules = %+v", q.Rules)
	}
	want := &LibraryProfileMigration{
		Home:    map[string]string{core.MediaMovie: "4K", core.MediaTV: "1080p (TV)"},
		Away:    map[string]string{core.MediaMovie: "1080p", core.MediaTV: "1080p (TV)"},
		Renames: map[string]map[string]string{core.MediaTV: {"1080p": "1080p (TV)"}},
	}
	if !reflect.DeepEqual(q.LibraryProfileMigration, want) {
		t.Fatalf("hand-off = %+v", q.LibraryProfileMigration)
	}
	// Settled: a second load changes nothing.
	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again.Get().Quality, q) {
		t.Fatalf("second load changed quality:\n%+v\n%+v", again.Get().Quality, q)
	}
}
