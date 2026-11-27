package mediabrowser

import (
	"bytes"
	_ "embed"
	"math"
	"net/http"
	"net/url"
	"strings"
)

// webExtrasJS sends the served Emby Web's server-admin routes to the Ether
// console and adds Ether's screensaver (see the file's header).
//
//go:embed web_extras.js
var webExtrasJS []byte

const webExtrasScriptTag = `<script src="ether-extras.js" defer></script>`

// withWebExtras loads Ether's extras script in the served Emby Web or
// Jellyfin Web page.
func withWebExtras(page []byte) []byte {
	if bytes.Contains(page, []byte("ether-extras.js")) {
		return page
	}
	i := bytes.Index(bytes.ToLower(page), []byte("</head>"))
	if i < 0 {
		return page
	}
	out := make([]byte, 0, len(page)+len(webExtrasScriptTag))
	out = append(out, page[:i]...)
	out = append(out, webExtrasScriptTag...)
	return append(out, page[i:]...)
}

func serveWebExtrasJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if r.Method != http.MethodHead {
		_, _ = w.Write(webExtrasJS)
	}
}

// webExtras answers GET /Ether/WebExtras for the extras script: the Ether
// console address (owner only; the admin pages it opens are the owner's) and
// the screensaver idle time the owner set (0: off).
func (i *instance) webExtras(w http.ResponseWriter, r *http.Request) {
	v, _, err := i.viewer(r)
	if err != nil || v == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	out := map[string]any{"screensaverMinutes": 0, "app": i.protocol}
	if i.host.Config != nil {
		if n, ok := i.host.Config()["screensaverMinutes"].(float64); ok && n > 0 && n <= maxScreensaverMinutes && n == math.Trunc(n) {
			out["screensaverMinutes"] = n
		}
	}
	if v.IsOwner && !v.Delegated && i.host.ViewerPortalURL != nil {
		if portal, err := i.host.ViewerPortalURL(r.Context(), r); err == nil {
			if console := consoleBase(portal); console != "" {
				out["console"] = console
			}
		}
	}
	jsonReply(w, out)
}

const maxScreensaverMinutes = 240

// consoleBase turns the viewer page address (…/ether/me) into the owner
// console's (…/ether).
func consoleBase(portal string) string {
	u, err := url.Parse(strings.TrimSpace(portal))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	p := strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(p, "/me") {
		u.Path = strings.TrimSuffix(p, "/me")
	} else {
		u.Path = "/ether"
	}
	u.RawQuery, u.Fragment = "", ""
	return u.String()
}
