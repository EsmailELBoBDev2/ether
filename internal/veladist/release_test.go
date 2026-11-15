package veladist

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func hash(b []byte) string { d := sha256.Sum256(b); return hex.EncodeToString(d[:]) }
func descriptor() Release {
	b := false
	z := 0
	r := Release{SchemaVersion: 1, Kind: "vela-release", Channel: "stable", Version: "0.1.42", PublishedAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(time.Hour), SourceRevision: "revision", SourceDirty: &b, MinimumEtherVersion: "0.3.12", Archive: Asset{Bytes: 1, SHA256: hash([]byte("x"))}, Manifest: Asset{Bytes: 1, SHA256: hash([]byte("x"))}}
	r.Protocol.MinInclusive = &z
	r.Protocol.MaxInclusive = &z
	return r
}
func tarball(t *testing.T, headers []*tar.Header, contents []string) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for i, h := range headers {
		if e := tw.WriteHeader(h); e != nil {
			t.Fatal(e)
		}
		if h.Typeflag == tar.TypeReg {
			if _, e := tw.Write([]byte(contents[i])); e != nil {
				t.Fatal(e)
			}
		}
	}
	if e := tw.Close(); e != nil {
		t.Fatal(e)
	}
	if e := gz.Close(); e != nil {
		t.Fatal(e)
	}
	return out.Bytes()
}
func write(t *testing.T, dir, p, s string) {
	t.Helper()
	p = filepath.Join(dir, p)
	if e := os.MkdirAll(filepath.Dir(p), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte(s), 0644); e != nil {
		t.Fatal(e)
	}
}
func TestDistribution(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	t.Run("descriptor", func(t *testing.T) {
		for _, name := range []string{"good", "bad-signature", "expired", "dirty", "protocol", "downgrade", "rotation"} {
			t.Run(name, func(t *testing.T) {
				r := descriptor()
				installed := "0.1.41"
				keys := []ed25519.PublicKey{pub}
				switch name {
				case "expired":
					r.ExpiresAt = time.Now().Add(-time.Minute)
				case "dirty":
					b := true
					r.SourceDirty = &b
				case "protocol":
					n := 1
					r.Protocol.MinInclusive = &n
				case "downgrade":
					installed = "0.1.43"
				case "rotation":
					other, _, _ := ed25519.GenerateKey(rand.Reader)
					keys = []ed25519.PublicKey{other, pub}
				}
				raw, _ := json.Marshal(r)
				sig := ed25519.Sign(priv, raw)
				if name == "bad-signature" {
					raw = append(raw, ' ')
				}
				_, e := Verify(raw, []byte(base64.StdEncoding.EncodeToString(sig)), keys, "0.3.13", installed, time.Now())
				want := name == "good" || name == "rotation"
				if (e == nil) != want {
					t.Fatalf("valid=%v error=%v", want, e)
				}
			})
		}
	})
	t.Run("tar-safety", func(t *testing.T) {
		for _, h := range []*tar.Header{{Name: "../escape", Typeflag: tar.TypeReg}, {Name: "/absolute", Typeflag: tar.TypeReg}, {Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../../escape"}, {Name: "hard", Typeflag: tar.TypeLink, Linkname: "../../escape"}, {Name: "device", Typeflag: tar.TypeChar}, {Name: "large", Typeflag: tar.TypeReg, Size: 2}} {
			t.Run(h.Name, func(t *testing.T) {
				data := tarball(t, []*tar.Header{h}, []string{strings.Repeat("x", int(h.Size))})
				if e := extract(context.Background(), bytes.NewReader(data), t.TempDir(), 1); e == nil {
					t.Fatal("unsafe tar accepted")
				}
			})
		}
	})
	t.Run("install-transaction", func(t *testing.T) {
		assets := map[string][]byte{}
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, ok := assets[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(b)
		}))
		defer srv.Close()
		root := t.TempDir()
		m := New(root, "0.3.13", func() string { return srv.URL + "/channel.json" })
		m.client = srv.Client()
		m.keys = []ed25519.PublicKey{pub}
		write(t, m.live(), "index.html", "old")
		write(t, m.live(), "app.version.json", `{"version":"0.1.41"}`)
		for _, p := range []string{"diagnostic-core-old.js", "core-app-old.html", "universal/key/module.js", "engines/cinematic/old/app.js"} {
			write(t, m.live(), p, "owner")
		}
		publish := func(mismatch bool) {
			content := "new"
			if mismatch {
				content = "bad"
			}
			assets["/archive"] = tarball(t, []*tar.Header{{Name: "./index.html", Typeflag: tar.TypeReg, Size: 3}}, []string{content})
			inv := inventory{SchemaVersion: 1, Version: "0.1.42", Files: []File{{Path: "index.html", Bytes: 3, SHA256: hash([]byte("new"))}}}
			assets["/manifest"], _ = json.Marshal(inv)
			r := descriptor()
			r.Archive = Asset{URL: srv.URL + "/archive", Bytes: int64(len(assets["/archive"])), SHA256: hash(assets["/archive"])}
			r.Manifest = Asset{URL: srv.URL + "/manifest", Bytes: int64(len(assets["/manifest"])), SHA256: hash(assets["/manifest"])}
			assets["/channel.json"], _ = json.Marshal(r)
			assets["/channel.json.sig"] = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, assets["/channel.json"])))
		}
		publish(true)
		if e := m.Check(context.Background()); e != nil {
			t.Fatal(e)
		}
		if e := m.Install(context.Background()); e == nil {
			t.Fatal("mismatch installed")
		}
		b, _ := os.ReadFile(filepath.Join(m.live(), "index.html"))
		if string(b) != "old" || len(m.rollbacks()) != 0 {
			t.Fatal("mismatch disturbed live build")
		}
		publish(false)
		if e := m.Check(context.Background()); e != nil {
			t.Fatal(e)
		}
		if e := m.Install(context.Background()); e != nil {
			t.Fatal(e)
		}
		if m.Status().InstalledVersion != "0.1.42" {
			t.Fatal(m.Status())
		}
		for _, p := range []string{"diagnostic-core-old.js", "core-app-old.html", "universal/key/module.js", "engines/cinematic/old/app.js"} {
			b, e := os.ReadFile(filepath.Join(m.live(), p))
			if e != nil || string(b) != "owner" {
				t.Fatalf("additive file %s lost: %v", p, e)
			}
		}
		if e := m.Rollback(context.Background()); e != nil {
			t.Fatal(e)
		}
		b, _ = os.ReadFile(filepath.Join(m.live(), "index.html"))
		if string(b) != "old" {
			t.Fatal("rollback did not restore old build")
		}
	})
}
