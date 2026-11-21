package app

import (
	"context"
	"slices"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
)

// pluginKindVersions lists the playable version menu of a plugin kind item
// (Auto slot + remaining /resolve sources). It is the broker's KindVersions
// dep and the plugin branch of the Vela/Plex version surfaces; it reuses the
// KindResolver candidate cache so a warm list never re-runs the plugin.
func (a *App) pluginKindVersions(ctx context.Context, viewer *core.Viewer, key core.RatingKey) ([]core.ReleaseVariant, error) {
	if a.pluginsRT == nil || a.pluginsRT.kindResolver == nil || viewer == nil || !key.IsPluginKey() {
		return nil, core.ErrNotFound
	}
	return a.pluginsRT.kindResolver.KindVersions(ctx, viewer, key, kindAccept, true)
}

// kindAccept lists the retrieval patterns the transcode bridge can turn into
// bytes. "direct" is excluded: a redirect cannot be probed or encoded, so a
// kind source must be a real stream.
var kindAccept = []string{"range", "download", "container", "release"}

// kindSourceFunc is the broker's KindSource dep: it projects a plugin kind
// item and hands back an opener for its bytes (spec §15.1). The opener
// re-authorizes the viewer through KindResolver.OpenKindExcluding, so
// authorization is enforced at open time, not on the projection alone.
func (a *App) kindSourceFunc() func(context.Context, *core.Viewer, core.RatingKey) (*core.KindInput, error) {
	if a.pluginsRT == nil || a.pluginsRT.kindCatalog == nil || a.pluginsRT.kindResolver == nil {
		return nil
	}
	catalog := a.pluginsRT.kindCatalog
	resolver := a.pluginsRT.kindResolver
	return func(ctx context.Context, viewer *core.Viewer, key core.RatingKey) (*core.KindInput, error) {
		if viewer == nil || !key.IsPluginKey() {
			return nil, core.ErrNotFound
		}
		piv, err := catalog.Item(ctx, key)
		if err != nil {
			return nil, core.ErrNotFound
		}
		title := piv.Title
		if title.MediaClass == core.MediaClassBook || title.MBItemType == "Book" {
			return nil, core.ErrNoUsableFile
		}
		in := &core.KindInput{
			Title:         &title,
			SourceID:      key.String(),
			OpenCandidate: nil,
		}
		in.OpenCandidate = func(octx context.Context, exclude []string) (core.MediaSource, core.Release, error) {
			var media *frontend.KindMedia
			var err error
			if in.PreferSourceID != "" && in.PreferSourceID != core.KindAutoVersionID {
				if slices.Contains(exclude, in.PreferSourceID) {
					if in.Exact {
						return nil, core.Release{}, core.ErrNotAvailable
					}
					media, err = resolver.OpenKindExcluding(octx, viewer, key, kindAccept, exclude)
				} else {
					media, err = resolver.OpenKindSource(octx, viewer, key, in.PreferSourceID, kindAccept)
				}
			} else {
				media, err = resolver.OpenKindExcluding(octx, viewer, key, kindAccept, exclude)
			}
			if err != nil {
				return nil, core.Release{}, err
			}
			rel := media.Release
			if rel.ID == "" {
				rel.ID = "kind:" + key.String()
			}
			if rel.Size <= 0 {
				rel.Size = media.Size
			}
			if ms := media.MediaSource; ms != nil && ms.Size() == media.Size && (ms.ReleaseID() == "" || ms.ReleaseID() == rel.ID) {
				if ms.ReleaseID() == rel.ID {
					return ms, rel, nil
				}
			}
			return kindMediaSource{media: media, releaseID: rel.ID}, rel, nil
		}
		in.Open = func(octx context.Context) (core.MediaSource, error) {
			ms, _, err := in.OpenCandidate(octx, nil)
			return ms, err
		}
		return in, nil
	}
}

// kindMediaSource adapts a frontend.KindMedia (a byte-addressable plugin
// stream) into a core.MediaSource so the playback session pipeline can probe
// and encode it like any other source.
type kindMediaSource struct {
	media     *frontend.KindMedia
	releaseID string
}

func (k kindMediaSource) ReadAt(p []byte, off int64) (int, error) { return k.media.ReadAt(p, off) }
func (k kindMediaSource) ReadAtContext(ctx context.Context, p []byte, off int64) (int, error) {
	if cr, ok := k.media.ReaderAt.(core.ContextReaderAt); ok {
		return cr.ReadAtContext(ctx, p, off)
	}
	return k.media.ReadAt(p, off)
}
func (k kindMediaSource) Size() int64 { return k.media.Size }
func (k kindMediaSource) Name() string {
	if k.media.Name != "" {
		return k.media.Name
	}
	return k.releaseID
}
func (k kindMediaSource) ReleaseID() string { return k.releaseID }
func (k kindMediaSource) MIME() string      { return k.media.MIME }
func (k kindMediaSource) Close() error      { return k.media.Close() }
