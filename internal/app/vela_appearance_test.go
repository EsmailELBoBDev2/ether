package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/admin"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/frontends/vela"
	"github.com/braedonsaunders/ether/internal/safefetch"
	"github.com/braedonsaunders/ether/internal/store"
)

func buildTestSkinArchive(t *testing.T, id, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	skinJSON := `{"schema":1,"id":"` + id + `","version":"` + version + `","name":"Harbor","engine":"cinematic","palette":{"canvas":"#0b1320","ink":"#eef4fb"},"images":{"preview":"assets/preview.svg"},"swatches":["#0b1320","#4cc9f0"]}`
	w, err := zw.Create("skin.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(skinJSON)); err != nil {
		t.Fatal(err)
	}
	aw, err := zw.Create("assets/preview.svg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aw.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 9"><rect width="16" height="9" fill="#0b1320"/></svg>`)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The revision and owner choice must commit together and survive restarts.
func TestVelaAppearanceRevision(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	registry := frontend.NewRegistry(func(frontend.Protocol) frontend.Host { return frontend.Host{} })
	if err := registry.Register(vela.Frontend{}); err != nil {
		t.Fatal(err)
	}
	control := newFrontendControl(registry, cfg)
	for _, tc := range []struct {
		skin     string
		force    bool
		revision uint64
	}{
		{"cinematic", false, 0},
		{"lounge", false, 1},
		{"lounge", false, 1},
		{"lounge", true, 2},
		{"lounge", true, 2},
		{"cinematic-doc", true, 3},
		{"cinematic", false, 4},
	} {
		if err := control.PutSettings(context.Background(), "vela", frontend.Settings{"defaultSkinId": tc.skin, "force": tc.force}); err != nil {
			t.Fatal(err)
		}
		reopened, err := config.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		got := reopened.Read().Frontends["vela"]
		if got.AppearanceRevision != tc.revision || got.Settings["defaultSkinId"] != tc.skin || got.Settings["force"] != tc.force {
			t.Fatalf("persisted: %#v", got)
		}
	}
	if err := control.PutSettings(context.Background(), "vela", frontend.Settings{"defaultSkinId": "../secret"}); err == nil {
		t.Fatal("invalid skin accepted")
	}
}

func TestVelaSkinsAdminAndSSRF(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	db := store.OpenTest(t)
	a := &App{
		opts:         Options{DataDir: dir, Version: "0.3.16-test"},
		cfg:          cfg,
		db:           db,
		velaSkinHTTP: (&safefetch.Guard{}).Client(&http.Client{}),
	}
	registry := frontend.NewRegistry(func(frontend.Protocol) frontend.Host { return frontend.Host{} })
	if err := registry.Register(vela.Frontend{DB: db, Skins: a.velaSkinsStore()}); err != nil {
		t.Fatal(err)
	}
	control := newFrontendControl(registry, cfg)
	h := admin.NewHandler(admin.Deps{
		Config:    cfg,
		Frontends: control,
		VelaSkins: a.velaSkinsAdmin(),
		IsOwnerToken: func(_ context.Context, tok string) bool {
			return tok == "owner-token"
		},
	})

	// Unauthenticated request is rejected.
	unauth := httptest.NewRecorder()
	h.ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, "/api/vela/skins", nil))
	if unauth.Code != http.StatusForbidden {
		t.Fatalf("unauth GET /api/vela/skins=%d, want 403", unauth.Code)
	}

	// Authenticated list returns the 9 non-removable built-in skin documents.
	listReq := httptest.NewRequest(http.MethodGet, "/api/vela/skins", nil)
	listReq.Header.Set("X-Plex-Token", "owner-token")
	listRec := httptest.NewRecorder()
	h.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("GET /api/vela/skins=%d: %s", listRec.Code, listRec.Body.String())
	}
	var listed struct {
		Skins []admin.VelaSkin `json:"skins"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Skins) != 9 || listed.Skins[0].ID != "cinematic-doc" || !listed.Skins[0].Builtin {
		t.Fatalf("initial skins=%+v", listed.Skins)
	}

	// Multipart upload installs harbor and bumps AppearanceRevision.
	archive := buildTestSkinArchive(t, "harbor", "1.0.0")
	var mpBuf bytes.Buffer
	mw := multipart.NewWriter(&mpBuf)
	fw, err := mw.CreateFormFile("file", "harbor.vskin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(archive); err != nil {
		t.Fatal(err)
	}
	_ = mw.Close()

	upReq := httptest.NewRequest(http.MethodPost, "/api/vela/skins", &mpBuf)
	upReq.Header.Set("Content-Type", mw.FormDataContentType())
	upReq.Header.Set("X-Ether-Request", "1")
	upReq.Header.Set("X-Plex-Token", "owner-token")
	upRec := httptest.NewRecorder()
	h.ServeHTTP(upRec, upReq)
	if upRec.Code != http.StatusCreated {
		t.Fatalf("POST multipart /api/vela/skins=%d: %s", upRec.Code, upRec.Body.String())
	}
	if got := cfg.Read().Frontends["vela"].AppearanceRevision; got != 1 {
		t.Fatalf("AppearanceRevision=%d, want 1", got)
	}

	// Owner can select harbor as defaultSkinId with force=true.
	if err := control.PutSettings(context.Background(), "vela", frontend.Settings{"defaultSkinId": "harbor", "force": true}); err != nil {
		t.Fatalf("PutSettings(harbor): %v", err)
	}
	if got := cfg.Read().Frontends["vela"].AppearanceRevision; got != 2 {
		t.Fatalf("AppearanceRevision=%d, want 2", got)
	}

	// Removing built-in is rejected; removing harbor resets defaultSkinId to cinematic and bumps revision.
	delBuiltin := httptest.NewRequest(http.MethodDelete, "/api/vela/skins/mochi", nil)
	delBuiltin.Header.Set("X-Ether-Request", "1")
	delBuiltin.Header.Set("X-Plex-Token", "owner-token")
	delBuiltinRec := httptest.NewRecorder()
	h.ServeHTTP(delBuiltinRec, delBuiltin)
	if delBuiltinRec.Code != http.StatusBadRequest {
		t.Fatalf("DELETE builtin=%d, want 400", delBuiltinRec.Code)
	}

	delReq := httptest.NewRequest(http.MethodDelete, "/api/vela/skins/harbor", nil)
	delReq.Header.Set("X-Ether-Request", "1")
	delReq.Header.Set("X-Plex-Token", "owner-token")
	delRec := httptest.NewRecorder()
	h.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("DELETE /api/vela/skins/harbor=%d: %s", delRec.Code, delRec.Body.String())
	}
	afterDel := cfg.Read().Frontends["vela"]
	if afterDel.AppearanceRevision != 3 || afterDel.Settings["defaultSkinId"] != "cinematic" {
		t.Fatalf("after DELETE harbor: %#v", afterDel)
	}

	// URL install SSRF guard: local 127.0.0.1 server blocked when lan=false, allowed when lan=true;
	// link-local metadata IP blocked even when lan=true.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	postJSON := func(payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/vela/skins", strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Ether-Request", "1")
		req.Header.Set("X-Plex-Token", "owner-token")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	blockedLAN := postJSON(`{"url":"` + srv.URL + `/harbor.vskin","lan":false}`)
	if blockedLAN.Code != http.StatusBadRequest || !strings.Contains(blockedLAN.Body.String(), `"ssrf_blocked"`) {
		t.Fatalf("lan=false local URL=%d: %s", blockedLAN.Code, blockedLAN.Body.String())
	}

	blockedMeta := postJSON(`{"url":"http://169.254.169.254/latest/meta-data","lan":true}`)
	if blockedMeta.Code != http.StatusBadRequest || !strings.Contains(blockedMeta.Body.String(), `"ssrf_blocked"`) {
		t.Fatalf("link-local metadata URL=%d: %s", blockedMeta.Code, blockedMeta.Body.String())
	}

	allowedLAN := postJSON(`{"url":"` + srv.URL + `/harbor.vskin","lan":true}`)
	if allowedLAN.Code != http.StatusCreated {
		t.Fatalf("lan=true local URL=%d: %s", allowedLAN.Code, allowedLAN.Body.String())
	}
}
