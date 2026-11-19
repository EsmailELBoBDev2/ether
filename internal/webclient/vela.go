package webclient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

var htmlTitlePattern = regexp.MustCompile(`(?is)<title>.*?</title>`)

type velaShellKey struct {
	dir     string
	file    string
	modTime int64
	size    int64
	name    string
}

type velaShellDoc struct {
	contentType string
	etag        string
	body        []byte
}

var (
	velaShellMu    sync.Mutex
	velaShellCache = map[velaShellKey]velaShellDoc{}
)

// VelaReady reports an owner-installed optional build. No download is attempted
// until a pinned Vela release artifact is published and verified by the host.
func VelaReady(dir string) bool {
	if dir == "" {
		return false
	}
	s, e := os.Stat(filepath.Join(dir, "index.html"))
	return e == nil && !s.IsDir()
}

// ServeVela serves the installed build with SPA deep-link fallback and default branding.
func ServeVela(w http.ResponseWriter, r *http.Request, dir string) {
	ServeVelaBrand(w, r, dir, "")
}

// ServeVelaBrand serves the installed build with SPA deep-link fallback, patching
// the root HTML <title> and web manifest name/short_name when appName is set.
func ServeVelaBrand(w http.ResponseWriter, r *http.Request, dir, appName string) {
	if r.Method != "GET" && r.Method != "HEAD" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	if r.URL.Path == "/vela/" {
		target := url.URL{Path: "/vela", RawQuery: r.URL.RawQuery, Fragment: r.URL.Fragment, RawFragment: r.URL.RawFragment}
		// Browsers retain an incoming fragment across a redirect whose Location
		// omits one; ordinary HTTP requests never transmit that fragment.
		http.Redirect(w, r, target.String(), http.StatusPermanentRedirect)
		return
	}
	if !VelaReady(dir) {
		http.Error(w, "Vela client is not installed", 503)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/vela/")
	if r.URL.Path == "/vela" {
		path = ""
	}
	f, e := os.OpenRoot(dir)
	if e != nil {
		http.Error(w, "Vela client is unavailable", 503)
		return
	}
	defer f.Close()
	s, e := f.Stat(path)
	if path == "sw.js" {
		// The worker's directory is /vela/, but the canonical home and worker
		// scope are /vela. Never serve the SPA as a missing worker script.
		if e != nil || !s.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Service-Worker-Allowed", "/vela")
	}
	// Native installer pages are ordinary nested index documents. FileServer
	// redirects their explicit index.html URL to a directory; a SPA fallback
	// there would serve the player instead. Serve existing nested indexes as
	// documents, without changing the canonical /vela home redirect.
	indexPath := ""
	if e == nil && s.IsDir() {
		indexPath = strings.TrimSuffix(path, "/") + "/index.html"
	} else if e == nil && strings.HasSuffix(path, "/index.html") {
		indexPath = path
	}
	if indexPath != "" {
		if info, err := f.Stat(indexPath); err == nil && info.Mode().IsRegular() {
			file, err := f.Open(indexPath)
			if err != nil {
				http.Error(w, "Vela client is unavailable", 503)
				return
			}
			defer file.Close()
			http.ServeContent(w, r, info.Name(), info.ModTime(), file)
			return
		}
	}
	if (path == "manifest.webmanifest" || path == "manifest.json") && e == nil && s.Mode().IsRegular() {
		serveBrandedVelaDoc(w, r, f, dir, path, s, appName)
		return
	}
	if e != nil || s.IsDir() {
		if info, err := f.Stat("index.html"); err == nil && info.Mode().IsRegular() {
			serveBrandedVelaDoc(w, r, f, dir, "index.html", info, appName)
			return
		}
		r = r.Clone(r.Context())
		u := *r.URL
		u.Path = "/"
		r.URL = &u
	} else {
		r = r.Clone(r.Context())
		u := *r.URL
		u.Path = "/" + path
		r.URL = &u
	}
	http.FileServer(http.FS(f.FS())).ServeHTTP(w, r)
}

func serveBrandedVelaDoc(w http.ResponseWriter, r *http.Request, root *os.Root, dir, file string, info os.FileInfo, appName string) {
	brand := strings.TrimSpace(appName)
	if brand == "" {
		brand = "Vela"
	}
	key := velaShellKey{
		dir:     dir,
		file:    file,
		modTime: info.ModTime().UnixNano(),
		size:    info.Size(),
		name:    brand,
	}
	velaShellMu.Lock()
	doc, ok := velaShellCache[key]
	velaShellMu.Unlock()

	if !ok {
		fh, err := root.Open(file)
		if err != nil {
			http.Error(w, "Vela client is unavailable", 503)
			return
		}
		raw, err := io.ReadAll(fh)
		_ = fh.Close()
		if err != nil {
			http.Error(w, "Vela client is unavailable", 503)
			return
		}
		var patched []byte
		var ct string
		if file == "index.html" {
			ct = "text/html; charset=utf-8"
			patched = patchVelaHTMLTitle(raw, brand)
		} else {
			ct = "application/manifest+json; charset=utf-8"
			patched = patchVelaManifest(raw, brand)
		}
		sum := sha256.Sum256(patched)
		doc = velaShellDoc{
			contentType: ct,
			etag:        `"` + hex.EncodeToString(sum[:16]) + `"`,
			body:        patched,
		}
		velaShellMu.Lock()
		if len(velaShellCache) >= 32 {
			clear(velaShellCache)
		}
		velaShellCache[key] = doc
		velaShellMu.Unlock()
	}

	w.Header().Set("Content-Type", doc.contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", doc.etag)
	for _, candidate := range strings.Split(r.Header.Get("If-None-Match"), ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == doc.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(doc.body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(doc.body)
	}
}

func patchVelaHTMLTitle(raw []byte, brand string) []byte {
	replacement := []byte("<title>" + html.EscapeString(brand) + "</title>")
	loc := htmlTitlePattern.FindIndex(raw)
	if loc == nil {
		return raw
	}
	out := make([]byte, 0, len(raw)-loc[1]+loc[0]+len(replacement))
	out = append(out, raw[:loc[0]]...)
	out = append(out, replacement...)
	out = append(out, raw[loc[1]:]...)
	return out
}

func patchVelaManifest(raw []byte, brand string) []byte {
	var manifest map[string]json.RawMessage
	if err := json.Unmarshal(raw, &manifest); err != nil || manifest == nil {
		return raw
	}
	encodedName, err := json.Marshal(brand)
	if err != nil {
		return raw
	}
	manifest["name"] = encodedName
	manifest["short_name"] = encodedName
	out, err := json.Marshal(manifest)
	if err != nil {
		return raw
	}
	return out
}
