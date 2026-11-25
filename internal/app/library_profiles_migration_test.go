package app

import (
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

// The libraries half of the move off per-media profiles: the built-in
// Shows library takes the TV profile, a library that had picked a renamed
// profile follows the rename, and away profiles that differ from Default
// away are pinned. The hand-off is cleared once applied.
func TestApplyLibraryProfileMigration(t *testing.T) {
	a := setupTestAppConfigured(t, true, Options{}, func(c *config.Config) {
		c.Quality.Profiles = []core.QualityProfile{{Name: "4K"}, {Name: "1080p"}, {Name: "1080p (TV)"}}
		c.Quality.Default, c.Quality.Remote = "4K", ""
	})
	ctx := t.Context()
	anime, err := a.libraries.Create(ctx, "Anime", core.TypeShow, libraryRules(core.Filter{Type: core.TypeShow, Genres: []string{"Animation"}}), "hide", core.Presentations{})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.libraries.SetQualityProfile(ctx, anime.ID, "1080p"); err != nil {
		t.Fatal(err)
	}
	if err := a.cfg.Update(func(c *config.Config) error {
		c.Quality.LibraryProfileMigration = &config.LibraryProfileMigration{
			Home:    map[string]string{core.MediaMovie: "4K", core.MediaTV: "1080p (TV)"},
			Away:    map[string]string{core.MediaMovie: "1080p", core.MediaTV: "1080p (TV)"},
			Renames: map[string]map[string]string{core.MediaTV: {"1080p": "1080p (TV)"}},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.applyLibraryProfileMigration(ctx); err != nil {
		t.Fatal(err)
	}
	want := map[core.LibraryID][2]string{
		core.LibraryMovies: {"", "1080p"},
		core.LibraryShows:  {"1080p (TV)", "1080p (TV)"},
		anime.ID:           {"1080p (TV)", "1080p (TV)"},
	}
	for id, w := range want {
		lib, err := a.libraries.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got := [2]string{lib.QualityProfile, lib.AwayQualityProfile}; got != w {
			t.Fatalf("%s = %v, want %v", lib.Name, got, w)
		}
	}
	if a.cfg.Get().Quality.LibraryProfileMigration != nil {
		t.Fatal("hand-off not cleared")
	}
}
