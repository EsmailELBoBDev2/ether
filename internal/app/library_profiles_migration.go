package app

import (
	"context"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

// applyLibraryProfileMigration is the libraries half of the one-time move
// off per-media release profiles (config.migrateMediaProfiles): each
// library takes the profile its media used, so every title keeps ranking
// with the profile it ranked with before the upgrade. Delete it, with
// config.LibraryProfileMigration, once v0.3.20 upgrades have ended.
//
//   - A library that had chosen a profile keeps it, renamed if its media's
//     profile of that name was kept apart as "<name> (TV)".
//   - A built-in library that had not chosen one takes its media's At home
//     profile when that is not the new Default.
//   - A library whose media's Away profile differs from the new Default
//     away takes it as its Away profile (the built-in library, and every
//     library that chose a profile, since the library's choice wins over
//     the built-in one).
//
// A library holding films and shows takes the movie mapping.
func (a *App) applyLibraryProfileMigration(ctx context.Context) error {
	q := a.cfg.Get().Quality
	mig := q.LibraryProfileMigration
	if mig == nil {
		return nil
	}
	libs, err := a.libraries.List(ctx)
	if err != nil {
		return err
	}
	for _, lib := range libs {
		if !lib.UsesReleaseProfiles() {
			continue
		}
		media := core.MediaMovie
		if lib.MediaType == core.TypeShow {
			media = core.MediaTV
		}
		home := lib.QualityProfile
		if home != "" {
			if to, ok := mig.Renames[media][home]; ok {
				home = to
			}
		} else if core.IsBuiltinLibrary(lib.ID) && mig.Home[media] != q.Default {
			home = mig.Home[media]
		}
		changed := home != lib.QualityProfile
		if changed {
			if err := a.libraries.SetQualityProfile(ctx, lib.ID, home); err != nil {
				return err
			}
		}
		away := lib.AwayQualityProfile
		if m := mig.Away[media]; away == "" && m != "" && m != q.Remote && (home != "" || core.IsBuiltinLibrary(lib.ID)) {
			if err := a.libraries.SetAwayQualityProfile(ctx, lib.ID, m); err != nil {
				return err
			}
			away, changed = m, true
		}
		if changed {
			a.log.Info("libraries: release profiles migrated", "library", lib.Name, "home", home, "away", away)
		}
	}
	return a.cfg.Update(func(c *config.Config) error {
		c.Quality.LibraryProfileMigration = nil
		return nil
	})
}
