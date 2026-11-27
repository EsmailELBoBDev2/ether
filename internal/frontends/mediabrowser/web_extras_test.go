package mediabrowser

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/frontend"
)

func TestWebExtrasScriptAndConsoleAddress(t *testing.T) {
	page := []byte("<html><head><title>Emby</title></head><body></body></html>")
	got := string(withWebExtras(page))
	if !strings.Contains(got, `<script src="ether-extras.js" defer></script></head>`) {
		t.Fatalf("script not added before </head>: %s", got)
	}
	if again := string(withWebExtras([]byte(got))); strings.Count(again, "ether-extras.js") != 1 {
		t.Fatalf("script added twice: %s", again)
	}
	for portal, want := range map[string]string{
		"https://dashboard.example.com/ether/me": "https://dashboard.example.com/ether",
		"http://192.168.1.5:32400/ether/me/":     "http://192.168.1.5:32400/ether",
		"https://me.example.com/viewers":         "https://me.example.com/ether",
		"not a url":                              "",
	} {
		if got := consoleBase(portal); got != want {
			t.Errorf("consoleBase(%q) = %q, want %q", portal, got, want)
		}
	}
	rec := httptest.NewRecorder()
	serveWebExtrasJS(rec, httptest.NewRequest("GET", "/web/ether-extras.js", nil))
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript") || !strings.Contains(rec.Body.String(), "Ether/WebExtras") {
		t.Fatalf("script response: %q", rec.Header().Get("Content-Type"))
	}
	for _, s := range []frontend.Settings{{"screensaverMinutes": -1.0}, {"screensaverMinutes": 241.0}, {"screensaverMinutes": 1.5}} {
		if validatePersonalSettings(s) == nil {
			t.Errorf("accepted %v", s)
		}
	}
	if err := validatePersonalSettings(frontend.Settings{"screensaverMinutes": 5.0}); err != nil {
		t.Fatal(err)
	}
}
