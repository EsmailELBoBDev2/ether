package experience

import (
	"context"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
)

// PluginLyrics is the plugin catalogue's lyrics source.
type PluginLyrics interface {
	Lyrics(ctx context.Context, key core.RatingKey) (core.Lyrics, error)
}

var _ frontend.TitleLyrics = (*Service)(nil)

// TitleLyrics reads lyrics for a title the viewer may see.
func (s *Service) TitleLyrics(ctx context.Context, viewer *core.Viewer, key core.RatingKey) (core.Lyrics, error) {
	ctx = core.WithCatalogViewer(ctx, viewer)
	v, err := s.viewer(ctx, viewer)
	if err != nil {
		return core.Lyrics{}, err
	}
	source, ok := s.d.PluginLibraries.(PluginLyrics)
	if !ok || !key.IsPluginKey() {
		return core.Lyrics{}, core.ErrNotFound
	}
	if _, err := s.pluginItem(core.WithNoContainerExpand(ctx), v, key); err != nil {
		return core.Lyrics{}, err
	}
	return source.Lyrics(ctx, key)
}

// PluginLyricsHint is the plugin catalogue's cheap lyrics hint.
type PluginLyricsHint interface {
	LyricsLikely(ctx context.Context, key core.RatingKey, library core.LibraryID) bool
}

var _ frontend.TitleLyricsHint = (*Service)(nil)

// TitleLyricsLikely reports whether a playable plugin title is worth
// offering lyrics for, from what Ether already knows; it never asks the
// plugin. The viewer's access is checked when the lyrics are read.
func (s *Service) TitleLyricsLikely(ctx context.Context, t core.Title) bool {
	if !t.Key.IsPluginKey() {
		return false
	}
	hint, ok := s.d.PluginLibraries.(PluginLyricsHint)
	return ok && hint.LyricsLikely(ctx, t.Key, t.LibraryID)
}

// LyricsAvailable reports whether a lyrics source is wired.
func (s *Service) LyricsAvailable(context.Context) bool {
	_, ok := s.d.PluginLibraries.(PluginLyrics)
	return ok
}
