package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/braedonsaunders/ether/internal/blobcache"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/identity"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/libraries"
	"github.com/braedonsaunders/ether/internal/plugins"
	"github.com/braedonsaunders/ether/internal/plugins/frontendadapter"
)

// TestAdapterCatalogResolvesKindLibraries proves an adapter's declared media
// kind keys resolve to the library ids those kinds created, and that a key
// with no library yet is skipped rather than guessed.
func TestAdapterCatalogResolvesKindLibraries(t *testing.T) {
	a := setupTestApp(t, true)
	ctx := context.Background()
	if a.libraries == nil {
		t.Skip("no libraries service in this build")
	}
	created, err := a.libraries.UpsertPluginLibrary(ctx, "plug.records", "recording", libraries.PluginLibraryDecl{Name: "Field recordings"})
	if err != nil {
		t.Fatal(err)
	}
	cat := adapterCatalog{app: a}
	ids := cat.resolveLibraries(ctx, "plug.records", []string{"recording"})
	if len(ids) != 1 || ids[0] != created.ID {
		t.Fatalf("resolved %v, want [%d]", ids, created.ID)
	}
	// Unknown kind key contributes nothing.
	if ids := cat.resolveLibraries(ctx, "plug.records", []string{"nope"}); len(ids) != 0 {
		t.Fatalf("unknown kind key resolved to %v", ids)
	}
	// A different plugin's library is not reachable by key alone.
	if ids := cat.resolveLibraries(ctx, "other.plugin", []string{"recording"}); len(ids) != 0 {
		t.Fatalf("cross-plugin key resolved to %v", ids)
	}
	if got := core.PluginLibraryKey("plug.records", "recording"); got != created.Key {
		t.Fatalf("library key = %q, want %q", got, created.Key)
	}
}

// TestAdapterStreamURLRefusesForeignAndUnknown proves the StreamURL backend
// rejects items that do not belong to the calling plugin and items that are
// not stored, before any ticket is minted.
func TestAdapterStreamURLRefusesForeignAndUnknown(t *testing.T) {
	a := setupTestApp(t, true)
	ctx := context.Background()
	issuer := frontendadapter.NewTicketIssuer()
	fn := a.adapterStreamURL(issuer)
	if fn == nil {
		t.Skip("no plugin runtime in this build")
	}

	// An item ref from another plugin is refused.
	if _, err := fn(ctx, "plug.mine", 7, "pk:other:kind:item-1"); !errors.Is(err, plugins.ErrPermissionDenied) {
		t.Fatalf("foreign item ref: %v", err)
	}
	// A malformed ref is not found.
	if _, err := fn(ctx, "plug.mine", 7, "not-a-ref"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("malformed ref: %v", err)
	}
	// A missing viewer is refused.
	if _, err := fn(ctx, "plug.mine", 0, "pk:plug.mine:kind:item-1"); !errors.Is(err, plugins.ErrPermissionDenied) {
		t.Fatalf("no viewer: %v", err)
	}
}

// Exercise the real controller, registry, listener owner and HTTP mux across
// version/prefix replacement, repeated sync, disable, and re-enable.
func TestAdapterListenerUpgrade(t *testing.T) {
	ctx := context.Background()
	reg := frontend.NewRegistry(func(frontend.Protocol) frontend.Host {
		return frontend.Host{LANViewer: func(context.Context, *http.Request) (*core.Viewer, error) { return &core.Viewer{ID: 7}, nil }}
	})
	control := newFrontendControl(reg, nil)
	a := &App{log: slog.Default(), frontends: &frontendRuntime{control: control}}
	cat := &adapterLifecycleCatalog{decl: frontendadapter.Declaration{PluginID: "upgrade", Version: "1", Meta: plugins.FrontendAdapterMeta{Key: "test", PathPrefix: "/old", Auth: frontendadapter.AuthNoneLAN}}}
	ctrl, err := frontendadapter.NewController(adapterRegistry{Registry: reg, app: a}, lanAdapterManager{}, cat)
	if err != nil {
		t.Fatal(err)
	}
	id := frontendadapter.AdapterID("upgrade", "test")
	if err := ctrl.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	control.Reconcile(ctx, config.Config{Frontends: map[string]config.Frontend{string(id): {Enabled: true}}})
	t.Cleanup(func() { _ = control.listeners.close(ctx) })
	serve := func(path string) int {
		w := httptest.NewRecorder()
		h := control.listeners.sharedHandler(path)
		if h == nil {
			return 404
		}
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w.Code
	}
	if got := serve("/old/file"); got != 200 {
		t.Fatal(got)
	}
	cat.decl.Version = "2"
	cat.decl.Meta.PathPrefix = "/new"
	if err := ctrl.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	control.Reconcile(ctx, config.Config{Frontends: map[string]config.Frontend{string(id): {Enabled: true}}})
	if serve("/old/file") != 404 || serve("/new/file") != 200 {
		t.Fatal("upgrade routes incorrect")
	}
	if err := ctrl.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if serve("/new/file") != 200 {
		t.Fatal("idempotent sync lost mount")
	}
	cat.disabled = true
	if err := ctrl.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if serve("/new/file") != 404 {
		t.Fatal("disabled adapter still mounted")
	}
	cat.disabled = false
	if err := ctrl.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	control.Reconcile(ctx, config.Config{Frontends: map[string]config.Frontend{string(id): {Enabled: true}}})
	if serve("/new/file") != 200 {
		t.Fatal("enable failed")
	}
}

type adapterLifecycleCatalog struct {
	decl     frontendadapter.Declaration
	disabled bool
}

func (c *adapterLifecycleCatalog) EnabledAdapters(context.Context) ([]frontendadapter.Declaration, error) {
	if c.disabled {
		return nil, nil
	}
	return []frontendadapter.Declaration{c.decl}, nil
}

func TestAdapterHostedBlobAuthorization(t *testing.T) {
	a := setupTestApp(t, true)
	ctx := context.Background()
	local, err := identity.NewLocal(a.db)
	if err != nil {
		t.Fatal(err)
	}
	viewerID, err := local.CreateViewer(ctx, "blob-viewer", "password-1234")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.users.RefreshViewers(ctx); err != nil {
		t.Fatal(err)
	}
	const pluginID = "community.example.blob"
	libraryKey := core.PluginLibraryKey(pluginID, "file")
	lib, err := a.libraries.UpsertPluginLibrary(ctx, pluginID, "file", libraries.PluginLibraryDecl{Name: "Files"})
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	entry, _ := z.Create("firmware.bin")
	_, _ = io.WriteString(entry, "0123456789")
	_ = z.Close()
	ref, err := a.pluginsRT.blobcache.Namespace(libraryKey, blobcache.Quota{}).Put(ctx, "fixture", bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"id":"community.example.blob","version":"1.0.0","name":"Files","description":"Serves test files","ether":{"protocol":1,"kind":"content","summary":"Serves test files","runtime":{"kind":"hosted","entry":"dist/plugin.js"},"capabilities":["catalog_provider","kind_resolver","frontend_adapter"],"contributes":{"mediaKinds":[{"key":"file","label":"Files","library":{"name":"Files"},"levels":[{"key":"file","label":"File","playable":true}],"present":{"plex":{"base":"movie"}},"sync":{"mode":"push"}}],"frontendAdapters":[{"key":"files","label":"Files","pathPrefix":"/files","protocolHint":"custom","auth":"none_lan","libraries":["file"]}]}}}`)
	spec := plugins.FrontendBlob{LibraryKey: libraryKey, Ref: ref.String(), MemberIndex: new(int)}
	response, _ := json.Marshal(plugins.Response{Status: 200, Blob: &spec})
	script := []byte(fmt.Sprintf(`function handle(req) { if(req.path === "/ether/health") return {status:200,body:{status:"ok"}}; if (!req.path.startsWith("/ether/v1/frontend/")) return {status:200,body:{}}; return %s; }`, response))
	preview, err := a.pluginsRT.mgr.PreviewInstall(ctx, plugins.Source{Kind: "studio", Manifest: raw, Files: map[string][]byte{"dist/plugin.js": script}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.pluginsRT.mgr.Install(ctx, preview, plugins.InstallOptions{AcceptAddedPermissions: true}); err != nil {
		t.Fatal(err)
	}
	mgr := adapterManager{Manager: a.pluginsRT.mgr, app: a}
	decl := frontendadapter.Declaration{PluginID: pluginID, Libraries: []core.LibraryID{lib.ID}, Meta: plugins.FrontendAdapterMeta{Key: "files", PathPrefix: "/files", Auth: frontendadapter.AuthNoneLAN}}
	f, err := frontendadapter.New(decl, mgr)
	if err != nil {
		t.Fatal(err)
	}
	inst, err := f.Mount(ctx, frontend.Host{LANViewer: func(context.Context, *http.Request) (*core.Viewer, error) { return a.users.PolicyUser(ctx, viewerID) }})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/files/firmware", nil)
	req.Header.Set("Range", "bytes=2-5")
	w := httptest.NewRecorder()
	inst.Handler().ServeHTTP(w, req)
	if w.Code != 206 || w.Body.String() != "2345" {
		t.Fatalf("hosted archive response: %d %q", w.Code, w.Body.String())
	}
	assertDenied := func(viewer int64, allowed []core.LibraryID, body plugins.FrontendBlob) {
		t.Helper()
		r, _, err := mgr.OpenFrontendBlob(ctx, pluginID, viewer, allowed, body)
		if err == nil {
			r.Close()
			t.Fatal("unauthorized blob accepted")
		}
	}
	if err := a.libraries.SetHidden(ctx, lib.ID, true); err != nil {
		t.Fatal(err)
	}
	assertDenied(viewerID, []core.LibraryID{lib.ID}, spec)
	if err := a.libraries.SetHidden(ctx, lib.ID, false); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	reader, _, err := mgr.OpenFrontendBlob(cancelled, pluginID, viewerID, []core.LibraryID{lib.ID}, spec)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_, readErr := reader.Read(make([]byte, 1))
	reader.Close()
	if !errors.Is(readErr, context.Canceled) {
		t.Fatalf("cancelled read: %v", readErr)
	}
	assertDenied(viewerID, nil, spec)
	assertDenied(999999, []core.LibraryID{lib.ID}, spec)
	foreign := spec
	foreign.LibraryKey = core.PluginLibraryKey("other.plugin", "file")
	assertDenied(viewerID, []core.LibraryID{lib.ID}, foreign)
	if err := a.pluginsRT.mgr.SetEnabled(ctx, pluginID, false); err != nil {
		t.Fatal(err)
	}
	assertDenied(viewerID, []core.LibraryID{lib.ID}, spec)
}
