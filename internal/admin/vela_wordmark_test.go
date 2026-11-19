package admin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/admin"
	"github.com/braedonsaunders/ether/internal/config"
)

func TestVelaWordmarkUploadRequiresOwnerAndCSRF(t *testing.T) {
	cfg := newStore(t, func(c *config.Config) { seededConfig(c); c.SetupComplete = true })
	h := admin.NewHandler(admin.Deps{
		Config:       cfg,
		IsOwnerToken: storedOwner(cfg),
		UploadVelaWordmark: func(_ context.Context, r io.Reader) (admin.VelaWordmark, error) {
			b, err := io.ReadAll(r)
			if err != nil {
				return admin.VelaWordmark{}, err
			}
			if len(b) > 512<<10 {
				return admin.VelaWordmark{}, admin.ErrVelaWordmarkTooLarge
			}
			if !strings.HasPrefix(string(b), "<svg") {
				return admin.VelaWordmark{}, &admin.VelaWordmarkValidationError{Message: "invalid wordmark"}
			}
			return admin.VelaWordmark{
				URL:    "/vela/v0/branding/" + strings.Repeat("a", 64) + ".svg",
				Width:  240,
				Height: 60,
				Digest: strings.Repeat("a", 64),
			}, nil
		},
	})

	var mpBuf bytes.Buffer
	mw := multipart.NewWriter(&mpBuf)
	part, err := mw.CreateFormFile("file", "wordmark.svg")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="240" height="60"/>`))
	_ = mw.Close()

	makeMultipart := func() *http.Request {
		r, err := http.NewRequest(http.MethodPost, "/ether/api/frontends/vela/wordmark", bytes.NewReader(mpBuf.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		r.Host = "ether.example"
		r.RemoteAddr = "203.0.113.5:4000"
		r.Header.Set("Content-Type", mw.FormDataContentType())
		r.Header.Set("X-Ether-Request", "1")
		return r
	}

	// Unauthenticated -> 401.
	if got := serve(h, makeMultipart()).Code; got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: got %d, want 401", got)
	}

	// Missing X-Ether-Request -> 403.
	noCSRF := makeMultipart()
	noCSRF.Header.Del("X-Ether-Request")
	noCSRF.Header.Set("X-Plex-Token", ownerToken)
	if got := serve(h, noCSRF).Code; got != http.StatusForbidden {
		t.Fatalf("missing CSRF header: got %d, want 403", got)
	}

	// Cross-origin -> 403.
	crossOrigin := makeMultipart()
	crossOrigin.Header.Set("Origin", "https://evil.example")
	crossOrigin.Header.Set("X-Plex-Token", ownerToken)
	if got := serve(h, crossOrigin).Code; got != http.StatusForbidden {
		t.Fatalf("cross-origin: got %d, want 403", got)
	}

	// Authenticated owner multipart upload -> 200.
	authed := makeMultipart()
	authed.Header.Set("Origin", "https://ether.example")
	authed.Header.Set("X-Plex-Token", ownerToken)
	res := serve(h, authed)
	if res.Code != http.StatusOK {
		t.Fatalf("owner multipart upload: %d %s", res.Code, res.Body.String())
	}
	var wm admin.VelaWordmark
	if err := json.Unmarshal(res.Body.Bytes(), &wm); err != nil {
		t.Fatal(err)
	}
	if wm.Width != 240 || wm.Height != 60 || !strings.HasPrefix(wm.URL, "/vela/v0/branding/") {
		t.Fatalf("unexpected response: %+v", wm)
	}

	// Setup-open local tokenless caller still cannot upload wordmark -> 403.
	openCfg := newStore(t, func(c *config.Config) { seededConfig(c); c.SetupComplete = false })
	openHandler := admin.NewHandler(admin.Deps{
		Config:             openCfg,
		IsOwnerToken:       storedOwner(openCfg),
		UploadVelaWordmark: func(context.Context, io.Reader) (admin.VelaWordmark, error) { return wm, nil },
	})
	setupReq := makeMultipart()
	setupReq.Host = "localhost"
	setupReq.RemoteAddr = "127.0.0.1:4000"
	if got := serve(openHandler, setupReq).Code; got != http.StatusForbidden {
		t.Fatalf("setup-open tokenless upload: got %d, want 403", got)
	}
}
