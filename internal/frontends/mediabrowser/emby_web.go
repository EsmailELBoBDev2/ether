package mediabrowser

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/frontend/kit/clientnet"
)

// Emby Web is not open source and Ether does not ship or download it. An
// owner who runs their own Emby Server can point Ether at it with
// ETHER_EMBY_WEB_SOURCE (for example http://emby:8096); Ether then serves that
// server's web app files at /web, and the app talks to Ether. Only the static
// files under /web are fetched; no request carries a viewer's credentials.
const embyWebSourceEnv = "ETHER_EMBY_WEB_SOURCE"

var embyWebClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		// Container names resolve through Docker's embedded DNS even when this
		// container's resolver is a VPN's (network_mode: service:x).
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			d := net.Dialer{Timeout: 5 * time.Second}
			if ip := net.ParseIP(host); ip != nil {
				return d.DialContext(ctx, network, addr)
			}
			addrs, err := clientnet.LookupProxyHost(ctx, host)
			if err != nil || len(addrs) == 0 {
				return d.DialContext(ctx, network, addr)
			}
			return d.DialContext(ctx, network, net.JoinHostPort(addrs[0].String(), port))
		},
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// embyWebSource is the owner's Emby Server address, or "" when unset or not
// an http(s) URL.
func embyWebSource() string {
	raw := strings.TrimSpace(os.Getenv(embyWebSourceEnv))
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return ""
	}
	return strings.TrimRight(u.Scheme+"://"+u.Host+strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/web"), "/")
}

// embyWebHeaders are the response headers passed through from the source.
var embyWebHeaders = []string{"Content-Type", "Cache-Control", "ETag", "Last-Modified", "Expires", "Content-Language"}

// serveEmbyWebFrom answers /web/{rel} with the same file from the owner's
// Emby Server.
func serveEmbyWebFrom(w http.ResponseWriter, r *http.Request, source, rel string, originFn originResolver) {
	if rel == "" && !strings.HasSuffix(r.URL.Path, "/") {
		target := clientnet.RedirectPath(requestRedirectPrefix(r, originFn), "", "/web/")
		http.Redirect(w, r, target, http.StatusFound)
		return
	}
	if rel == "" {
		rel = "index.html"
	}
	clean := strings.TrimPrefix(path.Clean("/"+rel), "/")
	if clean == "" || clean == "." || strings.Contains(clean, "..") {
		http.NotFound(w, r)
		return
	}
	target := source + "/web/" + (&url.URL{Path: clean}).EscapedPath()
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, nil)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	for _, h := range []string{"If-None-Match", "If-Modified-Since", "Accept", "Accept-Language"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	resp, err := embyWebClient.Do(req)
	if err != nil {
		webPageWithNote(w, r, originFn, "Ether could not reach the Emby Server set in "+embyWebSourceEnv+".")
		return
	}
	defer resp.Body.Close()
	for _, h := range embyWebHeaders {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	switch {
	case resp.StatusCode == http.StatusNotModified:
		w.WriteHeader(http.StatusNotModified)
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		w.WriteHeader(resp.StatusCode)
		if r.Method != http.MethodHead {
			_, _ = io.Copy(w, io.LimitReader(resp.Body, 64<<20))
		}
	default:
		http.NotFound(w, r)
	}
}
