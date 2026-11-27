package plex

import (
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
)

// A private build can carry the official Plex Web files in the image (the
// fork image workflow copies them from the Plex Media Server image). When the
// directory holds an index.html, /web serves it as Plex Media Server does;
// otherwise /web answers 404 as before. Ether never downloads or ships these
// files itself.
const (
	plexWebDirEnv     = "ETHER_PLEX_WEB_DIR"
	defaultPlexWebDir = "/usr/share/ether/plex-web"
)

func plexWebDir() string {
	dir := strings.TrimSpace(os.Getenv(plexWebDirEnv))
	if dir == "" {
		dir = defaultPlexWebDir
	}
	if info, err := os.Stat(filepath.Join(dir, "index.html")); err != nil || info.IsDir() {
		return ""
	}
	return dir
}

// servePlexWeb answers /web and /web/{path} from the bundled Plex Web: no
// listings, no traversal.
func servePlexWeb(w http.ResponseWriter, r *http.Request) {
	dir := plexWebDir()
	if dir == "" {
		http.NotFound(w, r)
		return
	}
	rel := chi.URLParam(r, "*")
	if rel == "" {
		if !strings.HasSuffix(r.URL.Path, "/") || r.URL.Path == "/web" {
			http.Redirect(w, r, "/web/index.html", http.StatusFound)
			return
		}
		rel = "index.html"
	}
	rel = strings.TrimPrefix(path.Clean("/"+rel), "/")
	if rel == "" || rel == "." || strings.Contains(rel, "..") {
		http.NotFound(w, r)
		return
	}
	full := filepath.Join(dir, filepath.FromSlash(rel))
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	// The Plex surface forbids scripts on every response (commonHeaders);
	// Plex Web is a script app, served like Plex Media Server serves it.
	w.Header().Del("Content-Security-Policy")
	if ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(rel))); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if rel == "index.html" {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// ServeContent, not ServeFile: ServeFile redirects .../index.html to the
	// directory, while Plex Web is opened at /web/index.html.
	f, err := os.Open(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	http.ServeContent(w, r, rel, info.ModTime(), f)
}
