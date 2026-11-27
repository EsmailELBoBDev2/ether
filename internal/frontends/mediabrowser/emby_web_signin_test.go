package mediabrowser

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/frontend/frontendtest"
	"github.com/braedonsaunders/ether/internal/frontend/kit/ids"
)

type webCodeIdentity struct {
	frontendtest.Identity
	got frontend.Device
}

func (w *webCodeIdentity) RedeemDeviceWebCode(_ context.Context, code string, dev frontend.Device) (string, error) {
	if code != "one-time" {
		return "", errors.New("used")
	}
	w.got = dev
	return "emby-grant", nil
}

func TestEmbyWebSignInHandsOffTheConsoleCode(t *testing.T) {
	identity := &webCodeIdentity{Identity: frontendtest.Identity{ResolveFunc: func(_ context.Context, token string) (*frontend.Viewer, error) {
		if token != "emby-grant" {
			return nil, errors.New("denied")
		}
		return &frontend.Viewer{ID: 7}, nil
	}}}
	inst, err := (Emby{MachineID: "test"}).Mount(context.Background(), frontend.Host{Identity: identity, Experience: frontendtest.Experience{}})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close(context.Background())
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/Ether/WebSignIn", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		inst.Handler().ServeHTTP(w, r)
		return w
	}
	w := post(`{"Code":"one-time","Browser":"0123456789abcdef0123"}`)
	var got struct{ AccessToken, UserId, Id string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatalf("sign-in: %d %s", w.Code, w.Body)
	}
	user, _ := ids.GUID(ids.User, 7)
	if got.AccessToken != "emby-grant" || got.UserId != user || got.Id == "" {
		t.Fatalf("entry %+v", got)
	}
	if identity.got.ID != "0123456789abcdef0123" || identity.got.Client != "Emby Web" {
		t.Fatalf("grant bound to %+v, want this browser as Emby Web", identity.got)
	}
	if w := post(`{"Code":"used","Browser":"0123456789abcdef0123"}`); w.Code == 200 {
		t.Fatal("a spent code must not sign in")
	}
	if w := post(`{"Code":"one-time","Browser":"short"}`); w.Code != 400 {
		t.Fatalf("a short browser id: %d", w.Code)
	}

	page := httptest.NewRecorder()
	inst.Handler().ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/web/ether-signin", nil))
	body := page.Body.String()
	if page.Code != 200 || !strings.Contains(body, "servercredentials3") || strings.Contains(body, "jellyfin_credentials") {
		t.Fatalf("Emby sign-in page: %d", page.Code)
	}
}
