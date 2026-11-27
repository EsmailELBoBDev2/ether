package mediabrowser

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/frontend"
)

func TestBrandingCSSCarriesEtherThemeAndOwnerCSS(t *testing.T) {
	cfg := frontend.Settings{"webTheme": "nord", "customCss": ".card { border-radius: 0 }"}
	emby := &instance{protocol: "emby", host: frontend.Host{Config: func() frontend.Settings { return cfg }}}
	css := emby.brandingCSS()
	theme := strings.Index(css, "--background-lightness: 22%")
	own := strings.Index(css, ".card { border-radius: 0 }")
	if theme < 0 || own < theme {
		t.Fatalf("Emby CSS lacks the theme, or the owner's CSS comes before it:\n%s", css)
	}
	// Jellyfin Web does not use Emby's variables: only the owner's CSS.
	jf := &instance{protocol: "jellyfin", host: frontend.Host{Config: func() frontend.Settings { return cfg }}}
	if got := jf.brandingCSS(); got != ".card { border-radius: 0 }" {
		t.Fatalf("Jellyfin CSS = %q", got)
	}
	rec := httptest.NewRecorder()
	writeBrandingCSS(rec, css)
	if rec.Header().Get("Content-Type") != "text/css; charset=utf-8" || rec.Body.String() != css {
		t.Fatalf("served %q %q", rec.Header().Get("Content-Type"), rec.Body.String())
	}
	for _, s := range []frontend.Settings{{"webTheme": "neon"}, {"webTheme": 3.0}, {"customCss": strings.Repeat("a", maxCustomCSS+1)}} {
		if validatePersonalSettings(s) == nil {
			t.Errorf("accepted %v", s)
		}
	}
	if err := validatePersonalSettings(frontend.Settings{"webTheme": "", "customCss": ""}); err != nil {
		t.Fatal(err)
	}
}
