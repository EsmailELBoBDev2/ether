package adapters

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/kindstream"
	"github.com/braedonsaunders/ether/internal/libraries"
	"github.com/braedonsaunders/ether/internal/plugins"
)

// HostKind is a media kind Ether provides itself, with no plugin process:
// its items are pushed by host code and its sources are files under Root.
// The library, browsing, artwork, playback (probe, direct play, remux,
// transcode, seek) and watch state are the same as for a plugin kind.
type HostKind struct {
	// PluginID is a reserved owner ID no installable plugin uses
	// (for example "ether.dvr").
	PluginID string
	Kind     plugins.MediaKindMeta
	// Root is the only folder file sources may be opened from.
	Root func() string
	// Resolve returns an item's playable sources.
	Resolve func(context.Context, core.KindItem) ([]core.PlayableSource, error)
}

var hostKinds = struct {
	mu sync.RWMutex
	m  map[string]*HostKind
}{m: map[string]*HostKind{}}

func hostKindKey(pluginID, kindKey string) string { return pluginID + "\x00" + kindKey }

// lookupHostKind returns the registered host kind, or nil.
func lookupHostKind(pluginID, kindKey string) *HostKind {
	hostKinds.mu.RLock()
	defer hostKinds.mu.RUnlock()
	return hostKinds.m[hostKindKey(pluginID, kindKey)]
}

// RegisterHostKind creates (or reconciles) the kind's library and record
// and makes it active without a plugin record. It returns the library.
func (c *KindCatalog) RegisterHostKind(ctx context.Context, hk HostKind) (core.Library, error) {
	if c == nil || c.libs == nil || c.items == nil {
		return core.Library{}, errors.New("adapters: kind catalog unavailable")
	}
	if hk.PluginID == "" || !strings.Contains(hk.PluginID, ".") || hk.Kind.Key == "" || hk.Resolve == nil || hk.Root == nil {
		return core.Library{}, errors.New("adapters: a host kind needs a reserved ID, a kind, a root and a resolver")
	}
	if c.mgr != nil {
		if _, err := c.mgr.Get(ctx, hk.PluginID); err == nil {
			return core.Library{}, fmt.Errorf("adapters: %s is an installed plugin", hk.PluginID)
		}
	}
	copy := hk
	hostKinds.mu.Lock()
	hostKinds.m[hostKindKey(hk.PluginID, hk.Kind.Key)] = &copy
	hostKinds.mu.Unlock()

	presInput := kindPresentationInput(hk.Kind)
	initial := libraries.ResolveKindPresentations(presInput, core.PresentationOverrides{})
	lib, err := c.libs.UpsertPluginLibrary(ctx, hk.PluginID, hk.Kind.Key, libraries.PluginLibraryDecl{
		Name:    hk.Kind.Library.Name,
		Present: initial.Effective,
	})
	if err != nil {
		// The owner may have deleted the library; the kind stays registered
		// so nothing else breaks, but there is nothing to fill.
		return core.Library{}, err
	}
	reconciled := libraries.ResolveKindPresentations(presInput, lib.PresentOverrides)
	if !presentationsEqual(lib.Present, reconciled.Effective) {
		if err := c.libs.SetPresent(ctx, lib.ID, reconciled.Effective); err == nil {
			lib.Present = reconciled.Effective
		}
	}
	if _, err := c.items.UpsertKind(ctx, hk.PluginID, lib.ID, hk.Kind, false); err != nil {
		return core.Library{}, err
	}
	return lib, nil
}

// openHostFile opens a file source of a host kind, confined to its root.
func openHostFile(hk *HostKind, src core.PlayableSource) (kindstream.ByteSource, error) {
	root := hk.Root()
	if root == "" || !filepath.IsAbs(root) || src.Path == "" {
		return nil, core.ErrSourceUnavailable
	}
	rel, err := filepath.Rel(root, src.Path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return nil, core.ErrSourceUnavailable
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, core.ErrSourceUnavailable
	}
	defer r.Close()
	f, err := r.Open(rel)
	if err != nil {
		return nil, core.ErrSourceUnavailable
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, core.ErrSourceUnavailable
	}
	mime := src.MIME
	if mime == "" {
		mime = "video/mp2t"
	}
	return &kindLocalFile{File: f, size: info.Size(), mime: mime}, nil
}
