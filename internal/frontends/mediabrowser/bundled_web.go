package mediabrowser

import (
	"bytes"
	"html"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/frontend/kit/clientnet"
)

// A private build can carry the official Emby Web files in the image (the
// fork image workflow copies them from the Emby Server image). When the
// directory holds an index.html, /web serves it; otherwise /web keeps the
// connection page. Ether never downloads or ships these files itself.
const (
	embyWebDirEnv     = "ETHER_EMBY_WEB_DIR"
	defaultEmbyWebDir = "/usr/share/ether/emby-web"
)

// bundledWebDir returns the web app directory named by env (or fallback)
// when it holds an index.html, or "".
func bundledWebDir(env, fallback string) string {
	dir := strings.TrimSpace(os.Getenv(env))
	if dir == "" {
		dir = fallback
	}
	if info, err := os.Stat(filepath.Join(dir, "index.html")); err != nil || info.IsDir() {
		return ""
	}
	return dir
}

// serveBundledWeb answers /web/{rel} from dir: no listings, no traversal.
// index.html gets the data-appversion attribute Emby Server writes into it:
// Emby Web takes its app version from there and will not start without one.
func serveBundledWeb(w http.ResponseWriter, r *http.Request, dir, rel string, originFn originResolver, version string) {
	if rel == "" && !strings.HasSuffix(r.URL.Path, "/") {
		target := clientnet.RedirectPath(requestRedirectPrefix(r, originFn), "", "/web/")
		http.Redirect(w, r, target, http.StatusFound)
		return
	}
	if rel == "" {
		rel = "index.html"
	}
	rel = strings.TrimPrefix(path.Clean("/"+rel), "/")
	if rel == "" || rel == "." || strings.Contains(rel, "..") {
		http.NotFound(w, r)
		return
	}
	full, err := resolveFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", webContentType(rel))
	if rel == "index.html" {
		raw, err := os.ReadFile(full)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, rel, time.Time{}, bytes.NewReader(withWebExtras(withAppVersion(raw, version))))
		return
	}
	w.Header().Set("Cache-Control", webCacheControl(rel))
	f, err := os.Open(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	var mod time.Time
	if st != nil {
		mod = st.ModTime()
	}
	http.ServeContent(w, r, rel, mod, f)
}

// withAppVersion adds data-appversion to the <html> tag unless it has one.
func withAppVersion(page []byte, version string) []byte {
	if version == "" || bytes.Contains(page, []byte("data-appversion")) {
		return page
	}
	i := bytes.Index(bytes.ToLower(page), []byte("<html"))
	if i < 0 {
		return page
	}
	at := i + len("<html")
	attr := []byte(` data-appversion="` + html.EscapeString(version) + `"`)
	out := make([]byte, 0, len(page)+len(attr))
	out = append(out, page[:at]...)
	out = append(out, attr...)
	return append(out, page[at:]...)
}
