package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/libraries"
	"github.com/braedonsaunders/ether/internal/plugins"
)

type fakePluginLiveCaller struct {
	liveCalls    int
	resolveCalls int
	liveStatus   int
	liveBody     string
	resolveMap   map[string]plugins.Response
}

func (f *fakePluginLiveCaller) Call(_ context.Context, _ string, req plugins.Request) (plugins.Response, error) {
	if strings.HasSuffix(req.Path, "/live") {
		f.liveCalls++
		status := f.liveStatus
		if status == 0 {
			status = http.StatusOK
		}
		return plugins.Response{Status: status, Body: json.RawMessage(f.liveBody)}, nil
	}
	if strings.HasSuffix(req.Path, "/live/resolve") {
		f.resolveCalls++
		var in plugins.KindLiveResolveRequest
		_ = json.Unmarshal(req.Body, &in)
		if resp, ok := f.resolveMap[in.ID]; ok {
			return resp, nil
		}
		return plugins.Response{Status: http.StatusNotFound}, nil
	}
	return plugins.Response{Status: http.StatusNotFound}, nil
}

type fakeURLImages struct {
	data map[string][]byte
}

func (f fakeURLImages) GetUntrustedURL(_ context.Context, rawURL string) ([]byte, string, error) {
	b, ok := f.data[rawURL]
	if !ok {
		return nil, "", core.ErrNotFound
	}
	return b, "image/jpeg", nil
}

func TestPluginLiveServiceCacheThumbsResolveAndConsole(t *testing.T) {
	a := setupTestApp(t, true)
	ctx := context.Background()

	lib, err := a.libraries.UpsertPluginLibrary(ctx, "esports", "matches", libraries.PluginLibraryDecl{
		Name: "Esports",
		Present: core.Presentations{
			Vela: &struct {
				Type string `json:"type"`
			}{Type: "movies"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.libraries.SetTabs(ctx, lib.ID, []core.LibraryTab{
		{Key: "live", Label: "Live", Kind: core.TabLive, Card: core.CardLandscape},
		{Key: "library", Label: "VODs", Kind: core.TabLibrary},
	}, nil); err != nil {
		t.Fatal(err)
	}
	lib, err = a.libraries.Get(ctx, lib.ID)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	caller := &fakePluginLiveCaller{
		liveBody: `{"items":[{"id":"m1","title":"Grand Finals","subtitle":"Arena","badge":"1080p","viewers":900,"tags":["EN"],"thumb":"https://thumbs.example.invalid/live/m1.jpg"}],"filters":[{"key":"all","label":"All"}],"nextCursor":"c2"}`,
		resolveMap: map[string]plugins.Response{
			"m1":  {Status: 200, Body: json.RawMessage(`{"url":"https://edge.example.invalid/live/m1.m3u8?token=secret","headers":{"Referer":"https://esports.example.invalid","Bad\r\nKey":"drop"},"title":"Grand Finals"}`)},
			"bad": {Status: 200, Body: json.RawMessage(`{"url":"file:///etc/passwd"}`)},
		},
	}
	svc := NewPluginLiveService(a)
	svc.caller = caller
	svc.now = func() time.Time { return now }
	a.pluginLive = svc
	a.opts.urlImages = fakeURLImages{data: map[string][]byte{"https://thumbs.example.invalid/live/m1.jpg": []byte("JPEGDATA")}}

	// 1. List caches within 45s TTL and registers opaque library-scoped thumb ref.
	page1, err := svc.List(ctx, lib, frontend.LibraryLiveRequest{Tab: "live", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(page1.Items) != 1 || !strings.HasPrefix(page1.Items[0].ThumbRef, frontend.LibraryLiveThumbPrefix) || strings.Contains(page1.Items[0].ThumbRef, "thumbs.example.invalid") {
		t.Fatalf("thumb must be opaque library-live ref, got %+v", page1.Items)
	}
	if !page1.Items[0].Live || page1.NextCursor != "c2" || caller.liveCalls != 1 {
		t.Fatalf("unexpected page1: %+v calls=%d", page1, caller.liveCalls)
	}

	// Second call within TTL hits cache.
	if _, err := svc.List(ctx, lib, frontend.LibraryLiveRequest{Tab: "live", Limit: 20}); err != nil {
		t.Fatal(err)
	}
	if caller.liveCalls != 1 {
		t.Fatalf("expected cache hit within TTL, got %d calls", caller.liveCalls)
	}

	// Advance clock past TTL -> refreshes.
	now = now.Add(pluginLiveCacheTTL + time.Second)
	if _, err := svc.List(ctx, lib, frontend.LibraryLiveRequest{Tab: "live", Limit: 20}); err != nil {
		t.Fatal(err)
	}
	if caller.liveCalls != 2 {
		t.Fatalf("expected cache refresh after TTL, got %d calls", caller.liveCalls)
	}

	// Cross-library thumb resolution must fail.
	if _, err := svc.ResolveThumb(lib.ID+1, page1.Items[0].ThumbRef); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("cross-library thumb resolve err = %v, want ErrNotFound", err)
	}
	resolvedThumb, err := svc.ResolveThumb(lib.ID, page1.Items[0].ThumbRef)
	if err != nil || resolvedThumb != "https://thumbs.example.invalid/live/m1.jpg" {
		t.Fatalf("thumb resolve = %q, err=%v", resolvedThumb, err)
	}

	// 2. Resolve is NEVER cached, rejects non-http(s) URLs, sanitizes headers, maps 404 to ErrNotFound.
	s1, err := svc.Resolve(ctx, lib, "m1")
	if err != nil {
		t.Fatal(err)
	}
	s2, err := svc.Resolve(ctx, lib, "m1")
	if err != nil || caller.resolveCalls != 2 || s1.URL != s2.URL {
		t.Fatalf("resolve must not be cached: calls=%d err=%v", caller.resolveCalls, err)
	}
	if len(s1.Headers) != 1 || s1.Headers["Referer"] != "https://esports.example.invalid" {
		t.Fatalf("headers not sanitized: %+v", s1.Headers)
	}
	if _, err := svc.Resolve(ctx, lib, "bad"); !errors.Is(err, core.ErrNotAvailable) {
		t.Fatalf("non-http stream URL err = %v, want ErrNotAvailable", err)
	}
	if _, err := svc.Resolve(ctx, lib, "offline"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing item err = %v, want ErrNotFound", err)
	}

	// 3. AllowViewer checks active plugin, viewer access, protocol publication, and visible live tab.
	owner := &core.Viewer{ID: 0, IsOwner: true}
	// With pluginsRT present and plugin not installed/active, AllowViewer rejects.
	if a.pluginsRT != nil && a.pluginsRT.kindCatalog != nil {
		if _, _, err := svc.AllowViewer(core.WithLiveApp(ctx, "vela"), owner, lib.ID, ""); !errors.Is(err, core.ErrNotFound) {
			t.Fatalf("inactive plugin must fail: %v", err)
		}
		a.pluginsRT = nil
	}
	if _, tab, err := svc.AllowViewer(core.WithLiveApp(ctx, "vela"), owner, lib.ID, ""); err != nil || tab != "live" {
		t.Fatalf("AllowViewer vela failed: tab=%q err=%v", tab, err)
	}
	if _, _, err := svc.AllowViewer(core.WithLiveApp(ctx, "plex"), owner, lib.ID, ""); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("unpublished protocol must fail: %v", err)
	}
	restricted := &core.Viewer{ID: 0, AllowedLibraries: []core.LibraryID{core.LibraryMovies}}
	if _, _, err := svc.AllowViewer(core.WithLiveApp(ctx, "vela"), restricted, lib.ID, ""); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("disallowed library must fail: %v", err)
	}

	// 4. Console live list & thumb proxy endpoints work and never expose raw thumb URLs.
	c := &librariesConsole{app: a}
	h := c.handler()
	idStr := strconv.FormatInt(int64(lib.ID), 10)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/"+idStr+"/live?tab=live", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("console GET /live status = %d body = %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "thumbs.example.invalid") {
		t.Fatalf("console /live leaked raw upstream thumb URL: %s", w.Body.String())
	}
	var dto consoleLivePageDTO
	if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if len(dto.Items) != 1 || !strings.HasPrefix(dto.Items[0].Thumb, "/ether/api/libraries/"+idStr+"/live/thumb/") {
		t.Fatalf("unexpected console live dto: %+v", dto)
	}
	thumbPath := strings.TrimPrefix(dto.Items[0].Thumb, "/ether/api/libraries")
	wThumb := httptest.NewRecorder()
	h.ServeHTTP(wThumb, httptest.NewRequest(http.MethodGet, thumbPath, nil))
	if wThumb.Code != http.StatusOK || wThumb.Body.String() != "JPEGDATA" {
		t.Fatalf("thumb proxy status=%d body=%q", wThumb.Code, wThumb.Body.String())
	}

	// 5. Console tab preview accepts kind="live".
	wPrev := httptest.NewRecorder()
	h.ServeHTTP(wPrev, httptest.NewRequest(http.MethodPost, "/"+idStr+"/tabs/preview", strings.NewReader(`{"tab":{"key":"live","label":"Live","kind":"live","card":"landscape"}}`)))
	if wPrev.Code != http.StatusOK || !strings.Contains(wPrev.Body.String(), `"kind":"live"`) {
		t.Fatalf("tab preview live status=%d body=%s", wPrev.Code, wPrev.Body.String())
	}
}
