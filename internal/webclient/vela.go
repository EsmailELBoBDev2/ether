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

var (
	htmlTitlePattern = regexp.MustCompile(`(?is)<title>.*?</title>`)
	htmlIconPattern  = regexp.MustCompile(`(?is)<link\b[^>]*\brel\s*=\s*["']icon["'][^>]*>`)
)

// VelaIconAsset is a pre-rendered owner PWA icon served in place of the
// release's built-in icon files.
type VelaIconAsset struct {
	ContentType string
	ETag        string
	Data        []byte
	IsSVG       bool
}

// VelaBrand carries the owner's optional Vela app name and icon resolver for
// dynamic index.html, manifest.webmanifest, and PWA icon serving.
type VelaBrand struct {
	AppName     string
	IconVersion string
	IconIsSVG   bool
	IconAsset   func(name string) (VelaIconAsset, bool)
}

var velaPWAIconPaths = map[string]string{
	"vela-icon.svg":              "image/svg+xml",
	"vela-icon-180.png":          "image/png",
	"vela-icon-192.png":          "image/png",
	"vela-icon-512.png":          "image/png",
	"vela-icon-maskable-192.png": "image/png",
	"vela-icon-maskable-512.png": "image/png",
	"favicon-16.png":             "image/png",
	"favicon-32.png":             "image/png",
	"favicon-48.png":             "image/png",
}

type velaShellKey struct {
	dir         string
	file        string
	modTime     int64
	size        int64
	name        string
	iconVersion string
	iconIsSVG   bool
}

type velaShellDoc struct {
	contentType string
	etag        string
	body        []byte
	isSVG       bool
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
	ServeVelaWithBrand(w, r, dir, VelaBrand{})
}

// ServeVelaBrand serves the installed build with SPA deep-link fallback, patching
// the root HTML <title> and web manifest name/short_name when appName is set.
func ServeVelaBrand(w http.ResponseWriter, r *http.Request, dir, appName string) {
	ServeVelaWithBrand(w, r, dir, VelaBrand{AppName: appName})
}

// ServeVelaWithBrand serves the installed Vela build with dynamic branding for
// index.html, manifest.webmanifest, and PWA/favicon icons.
func ServeVelaWithBrand(w http.ResponseWriter, r *http.Request, dir string, brand VelaBrand) {
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
	// Branded or release PWA icons: always served with no-cache and ETag.
	if fallbackCT, isIcon := velaPWAIconPaths[path]; isIcon {
		if brand.IconVersion != "" && brand.IconAsset != nil {
			if asset, ok := brand.IconAsset(path); ok {
				writeNoCacheDoc(w, r, velaShellDoc{
					contentType: asset.ContentType,
					etag:        asset.ETag,
					body:        asset.Data,
					isSVG:       asset.IsSVG,
				})
				return
			}
		}
		if e == nil && s.Mode().IsRegular() {
			serveReleaseIcon(w, r, f, dir, path, s, fallbackCT)
			return
		}
		http.NotFound(w, r)
		return
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
	if path == "manifest.webmanifest" || path == "manifest.json" {
		if e == nil && s.Mode().IsRegular() {
			serveBrandedVelaDoc(w, r, f, dir, path, s, brand)
			return
		}
		if path == "manifest.webmanifest" {
			serveFallbackVelaManifest(w, r, brand)
			return
		}
	}
	if path == "index.html" && e == nil && s.Mode().IsRegular() {
		serveBrandedVelaDoc(w, r, f, dir, "index.html", s, brand)
		return
	}
	if e != nil || s.IsDir() {
		if info, err := f.Stat("index.html"); err == nil && info.Mode().IsRegular() {
			serveBrandedVelaDoc(w, r, f, dir, "index.html", info, brand)
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

func serveReleaseIcon(w http.ResponseWriter, r *http.Request, root *os.Root, dir, file string, info os.FileInfo, contentType string) {
	key := velaShellKey{
		dir:     dir,
		file:    file,
		modTime: info.ModTime().UnixNano(),
		size:    info.Size(),
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
		sum := sha256.Sum256(raw)
		doc = velaShellDoc{
			contentType: contentType,
			etag:        `"` + hex.EncodeToString(sum[:16]) + `"`,
			body:        raw,
			isSVG:       contentType == "image/svg+xml",
		}
		velaShellMu.Lock()
		if len(velaShellCache) >= 64 {
			clear(velaShellCache)
		}
		velaShellCache[key] = doc
		velaShellMu.Unlock()
	}

	writeNoCacheDoc(w, r, doc)
}

func serveFallbackVelaManifest(w http.ResponseWriter, r *http.Request, brand VelaBrand) {
	base := []byte(`{"id":"/vela","name":"Vela","short_name":"Vela","start_url":"/vela","scope":"/vela","display":"standalone","theme_color":"#090B0E","background_color":"#090B0E"}`)
	patched := patchVelaManifest(base, strings.TrimSpace(brand.AppName), brand.IconVersion != "")
	sum := sha256.Sum256(append(append([]byte(nil), patched...), ("\x00" + brand.IconVersion)...))
	writeNoCacheDoc(w, r, velaShellDoc{
		contentType: "application/manifest+json; charset=utf-8",
		etag:        `"` + hex.EncodeToString(sum[:16]) + `"`,
		body:        patched,
	})
}

func serveBrandedVelaDoc(w http.ResponseWriter, r *http.Request, root *os.Root, dir, file string, info os.FileInfo, brand VelaBrand) {
	appName := strings.TrimSpace(brand.AppName)
	key := velaShellKey{
		dir:         dir,
		file:        file,
		modTime:     info.ModTime().UnixNano(),
		size:        info.Size(),
		name:        appName,
		iconVersion: brand.IconVersion,
		iconIsSVG:   brand.IconIsSVG,
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
			patched = patchVelaHTML(raw, appName, brand.IconVersion != "", brand.IconIsSVG)
		} else {
			ct = "application/manifest+json; charset=utf-8"
			patched = patchVelaManifest(raw, appName, brand.IconVersion != "")
		}
		hashInput := make([]byte, 0, len(patched)+1+len(brand.IconVersion))
		hashInput = append(hashInput, patched...)
		hashInput = append(hashInput, 0)
		hashInput = append(hashInput, brand.IconVersion...)
		sum := sha256.Sum256(hashInput)
		doc = velaShellDoc{
			contentType: ct,
			etag:        `"` + hex.EncodeToString(sum[:16]) + `"`,
			body:        patched,
		}
		velaShellMu.Lock()
		if len(velaShellCache) >= 64 {
			clear(velaShellCache)
		}
		velaShellCache[key] = doc
		velaShellMu.Unlock()
	}

	writeNoCacheDoc(w, r, doc)
}

func writeNoCacheDoc(w http.ResponseWriter, r *http.Request, doc velaShellDoc) {
	w.Header().Set("Content-Type", doc.contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", doc.etag)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if doc.isSVG {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	}
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

func patchVelaHTML(raw []byte, appName string, hasCustomIcon, iconIsSVG bool) []byte {
	out := raw
	if appName != "" {
		out = patchVelaHTMLTitle(out, appName)
	}
	if hasCustomIcon && !iconIsSVG {
		out = patchVelaHTMLFavicon(out)
	}
	return out
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

func patchVelaHTMLFavicon(raw []byte) []byte {
	replacement := []byte(`<link rel="icon" type="image/png" sizes="32x32" href="/vela/favicon-32.png" />`)
	loc := htmlIconPattern.FindIndex(raw)
	if loc == nil {
		return raw
	}
	out := make([]byte, 0, len(raw)-loc[1]+loc[0]+len(replacement))
	out = append(out, raw[:loc[0]]...)
	out = append(out, replacement...)
	out = append(out, raw[loc[1]:]...)
	return out
}

var brandedManifestIcons = []map[string]string{
	{"src": "/vela/vela-icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
	{"src": "/vela/vela-icon-maskable-192.png", "sizes": "192x192", "type": "image/png", "purpose": "maskable"},
	{"src": "/vela/vela-icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any"},
	{"src": "/vela/vela-icon-maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"},
}

func patchVelaManifest(raw []byte, appName string, hasCustomIcon bool) []byte {
	var manifest map[string]json.RawMessage
	if err := json.Unmarshal(raw, &manifest); err != nil || manifest == nil {
		return raw
	}
	if appName != "" {
		if encodedName, err := json.Marshal(appName); err == nil {
			manifest["name"] = encodedName
			manifest["short_name"] = encodedName
		}
	}
	if hasCustomIcon {
		if encodedIcons, err := json.Marshal(brandedManifestIcons); err == nil {
			manifest["icons"] = encodedIcons
		}
	}
	out, err := json.Marshal(manifest)
	if err != nil {
		return raw
	}
	return out
}
