package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"fmt"
	"hash/crc32"
	"net/http"

	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"github.com/braedonsaunders/ether/internal/blobcache"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plugins/wasm"
	"io"
	"strings"
	"testing"
)

type dataRoundTripper func(*http.Request) (*http.Response, error)

func (f dataRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type dataTestHost struct {
	Host
	data   []byte
	opens  int
	closes int
}
type dataTestFile struct {
	*bytes.Reader
	close func()
}

func (f dataTestFile) Close() error { f.close(); return nil }
func (h *dataTestHost) CacheOpen(context.Context, string) (io.ReadSeekCloser, error) {
	h.opens++
	return dataTestFile{bytes.NewReader(h.data), func() { h.closes++ }}, nil
}
func dataZIP(t *testing.T, name string, method uint16, body []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	f, err := w.CreateHeader(&zip.FileHeader{Name: name, Method: method})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(body); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// These are host-service invariants: bounded envelopes, random access without
// whole-archive guest copies, pre-allocation directory validation and containment.
func TestDataHostInvariants(t *testing.T) {
	ctx := context.Background()

	t.Run("streaming acquisition and grants", func(t *testing.T) {
		env := newTestEnv(t)
		rec := installContent(t, env)
		rec.Manifest.Ether.Permissions.Network = []string{"data.example.test"}
		var marshalErr error
		rec.ManifestRaw, marshalErr = json.Marshal(rec.Manifest)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if err := env.m.store.upsert(ctx, rec); err != nil {
			t.Fatal(err)
		}
		body := []byte("a streamed dataset")
		requests := 0
		writes := 0
		env.m.client = &http.Client{Transport: dataRoundTripper(func(r *http.Request) (*http.Response, error) {
			requests++
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header), Request: r}, nil
		})}
		env.m.SetKindBackends(KindBackends{CachePut: func(_ context.Context, plugin, library, name string, r io.Reader) (string, error) {
			writes++
			data, err := io.ReadAll(r)
			if plugin != rec.ID || library != core.PluginLibraryKey(rec.ID, "recording") || name != "data.zip" || !bytes.Equal(data, body) {
				t.Fatal("cache received wrong bytes or namespace")
			}
			return "blob:test", err
		}})
		h := env.m.HostFor(rec.ID).(*host)
		req := FetchCacheRequest{FetchRequest: FetchRequest{URL: "https://data.example.test/data.zip"}, LibraryKey: core.PluginLibraryKey(rec.ID, "recording"), Name: "data.zip"}
		out, err := h.HTTPFetchToCache(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if out.Size != int64(len(body)) || out.CRC32 != fmt.Sprintf("%08x", crc32.ChecksumIEEE(body)) || out.MD5 != fmt.Sprintf("%x", md5.Sum(body)) || out.SHA1 != fmt.Sprintf("%x", sha1.Sum(body)) {
			t.Fatalf("digests: %+v", out)
		}
		bad := req
		bad.LibraryKey = "another-plugin-library"
		if _, err = h.HTTPFetchToCache(ctx, bad); !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("foreign cache namespace: %v", err)
		}
		if requests != 1 || writes != 1 {
			t.Fatal("foreign cache namespace caused I/O")
		}
		if err = env.m.SetGrants(ctx, rec.ID, Grants{Revoked: []string{"network"}}); err != nil {
			t.Fatal(err)
		}
		if _, err = h.HTTPFetchToCache(ctx, req); !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("revoked network: %v", err)
		}
		if requests != 1 || writes != 1 {
			t.Fatal("revoked network caused I/O")
		}
	})
	t.Run("ranged blob and ZIP member", func(t *testing.T) {
		body := bytes.Repeat([]byte{0, 255, 1, 2, 3, 4, 5, 6}, (5<<20)/8)
		h := &dataTestHost{data: body}
		raw, err := CallDataHost(ctx, h, "cache.read", json.RawMessage(`["blob:test",{"offset":4194300,"length":100}]`))
		if err != nil {
			t.Fatal(err)
		}
		var answer struct {
			BodyBase64 string `json:"bodyBase64"`
		}
		if err = json.Unmarshal(raw, &answer); err != nil {
			t.Fatal(err)
		}
		got, err := base64.StdEncoding.DecodeString(answer.BodyBase64)
		if err != nil || !bytes.Equal(got, body[4194300:4194400]) {
			t.Fatalf("range mismatch: %v", err)
		}
		h.data = dataZIP(t, "data/member.bin", zip.Store, body)
		raw, err = CallDataHost(ctx, h, "archive.list", json.RawMessage(`["blob:test"]`))
		if err != nil {
			t.Fatal(err)
		}
		var members []ArchiveMember
		if err = json.Unmarshal(raw, &members); err != nil || len(members) != 1 || members[0].Size != uint64(len(body)) {
			t.Fatalf("list %s: %v", raw, err)
		}
		raw, err = CallDataHost(ctx, h, "archive.open", json.RawMessage(`["blob:test",0,{"offset":4194300,"length":1048576}]`))
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) >= 4<<20 {
			t.Fatalf("range envelope too large: %d", len(raw))
		}
		if err = json.Unmarshal(raw, &answer); err != nil {
			t.Fatal(err)
		}
		got, err = base64.StdEncoding.DecodeString(answer.BodyBase64)
		if err != nil || !bytes.Equal(got, body[4194300:4194300+(1<<20)]) {
			t.Fatalf("member mismatch: %v", err)
		}

		h.data = dataZIP(t, "data/member.bin", zip.Deflate, body)
		if _, err = CallDataHost(ctx, h, "archive.open", json.RawMessage(`["blob:test",0,{"offset":4194300,"length":100}]`)); err != nil {
			t.Fatalf("deflate range: %v", err)
		}
		if h.opens != h.closes {
			t.Fatalf("leaked readers: %d opens/%d closes", h.opens, h.closes)
		}
		before := h.opens
		for _, args := range []string{`["blob:test",{"offset":-1,"length":1}]`, `["blob:test",{"offset":0,"length":8388609}]`} {
			if _, err = CallDataHost(ctx, h, "cache.read", json.RawMessage(args)); err == nil {
				t.Fatal("invalid range accepted")
			}
		}
		if before != h.opens {
			t.Fatal("invalid range opened cache")
		}
	})
	t.Run("directory and expansion limits", func(t *testing.T) {
		bomb := dataZIP(t, "bomb", zip.Deflate, bytes.Repeat([]byte("a"), 2<<20))
		central := bytes.Index(bomb, []byte{0x50, 0x4b, 0x01, 0x02})
		binary.LittleEndian.PutUint32(bomb[central+24:], 64<<20)
		if _, err := openDataZIP(ctx, bytes.NewReader(bomb)); err == nil {
			t.Fatal("compression bomb accepted")
		}
		b := dataZIP(t, "ok", zip.Store, []byte("ok"))
		end := len(b) - 22
		binary.LittleEndian.PutUint16(b[end+8:], 0)
		binary.LittleEndian.PutUint16(b[end+10:], 0)
		if _, err := openDataZIP(ctx, bytes.NewReader(b)); err == nil {
			t.Fatal("directory count mismatch accepted")
		}
		binary.LittleEndian.PutUint16(b[end+8:], 1)
		binary.LittleEndian.PutUint16(b[end+10:], 1)
		binary.LittleEndian.PutUint32(b[end+12:], maxArchiveDirectory+1)
		if _, err := openDataZIP(ctx, bytes.NewReader(b)); err == nil {
			t.Fatal("oversized directory accepted")
		}
	})
	t.Run("overflow and cancellation propagate", func(t *testing.T) {
		r := &boundedDataReader{ctx: ctx, r: strings.NewReader("12345"), left: 4}
		if _, err := io.ReadAll(r); err == nil {
			t.Fatal("overflow became successful EOF")
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		r = &boundedDataReader{ctx: canceled, r: strings.NewReader("a"), left: 4}
		if _, err := io.ReadAll(r); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	})
	t.Run("archive.extract streams into blobcache with digests and enforces grants", func(t *testing.T) {
		env := newTestEnv(t)
		rec := installContent(t, env)
		libKey := core.PluginLibraryKey(rec.ID, "recording")

		blobs, err := blobcache.Open(t.TempDir(), blobcache.Options{SweepPeriod: -1})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = blobs.Close() })
		quotaBytes := int64(16 << 20)
		cachePut, cacheOpen := blobs.HostBackends(func(_, _ string) blobcache.Quota {
			return blobcache.Quota{MaxBytes: quotaBytes}
		})
		env.m.SetKindBackends(KindBackends{CachePut: cachePut, CacheOpen: cacheOpen})

		body := bytes.Repeat([]byte("3DO-track-sector-payload-01234567"), (2<<20)/33+4096)
		var archiveBuf bytes.Buffer
		zw := zip.NewWriter(&archiveBuf)
		w0, err := zw.CreateHeader(&zip.FileHeader{Name: "disc/track01.bin", Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w0.Write(body); err != nil {
			t.Fatal(err)
		}
		w1, err := zw.CreateHeader(&zip.FileHeader{Name: "../escape.bin", Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w1.Write([]byte("nope")); err != nil {
			t.Fatal(err)
		}
		if _, err = zw.CreateHeader(&zip.FileHeader{Name: "disc/", Method: zip.Store}); err != nil {
			t.Fatal(err)
		}
		if err = zw.Close(); err != nil {
			t.Fatal(err)
		}

		h := env.m.HostFor(rec.ID).(*host)
		archiveRef, err := h.CachePut(ctx, libKey, "game.zip", bytes.NewReader(archiveBuf.Bytes()))
		if err != nil {
			t.Fatal(err)
		}

		args, _ := json.Marshal([]any{archiveRef, 0})
		raw, err := CallDataHost(ctx, h, "archive.extract", args)
		if err != nil {
			t.Fatalf("archive.extract: %v", err)
		}
		var out ArchiveExtractResult
		if err = json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		wantRef := fmt.Sprintf("blob:%x", sha256.Sum256(body))
		if out.Ref != wantRef ||
			out.Size != int64(len(body)) ||
			out.SHA1 != fmt.Sprintf("%x", sha1.Sum(body)) ||
			out.MD5 != fmt.Sprintf("%x", md5.Sum(body)) ||
			out.CRC32 != fmt.Sprintf("%08x", crc32.ChecksumIEEE(body)) {
			t.Fatalf("unexpected extract result: %+v", out)
		}
		rc, err := h.CacheOpen(ctx, out.Ref)
		if err != nil {
			t.Fatalf("open extracted ref: %v", err)
		}
		stored, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil || !bytes.Equal(stored, body) {
			t.Fatalf("stored blob mismatch (len=%d want=%d err=%v)", len(stored), len(body), err)
		}

		// Unsafe member refused by index with UnsafeArchiveMemberError.
		unsafeArgs, _ := json.Marshal([]any{archiveRef, 1})
		_, err = CallDataHost(ctx, h, "archive.extract", unsafeArgs)
		var unsafeErr *UnsafeArchiveMemberError
		if !errors.As(err, &unsafeErr) || unsafeErr.Index != 1 || unsafeErr.Name != "../escape.bin" {
			t.Fatalf("unsafe extract err = %v", err)
		}

		// Directory member refused.
		dirArgs, _ := json.Marshal([]any{archiveRef, 2})
		if _, err = CallDataHost(ctx, h, "archive.extract", dirArgs); err == nil {
			t.Fatal("directory extract succeeded")
		}

		// Foreign libraryKey refused with ErrPermissionDenied.
		foreignArgs, _ := json.Marshal([]any{archiveRef, 0, ArchiveExtractOptions{LibraryKey: "pk-other-plugin-recording"}})
		if _, err = CallDataHost(ctx, h, "archive.extract", foreignArgs); !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("foreign libraryKey err = %v, want ErrPermissionDenied", err)
		}

		// Per-library cache quota enforced before committing.
		rec.Manifest.Ether.Contributes.MediaKinds[0].Cache.MaxBytes = 512 << 10
		rec.ManifestRaw, _ = json.Marshal(rec.Manifest)
		if err = env.m.store.upsert(ctx, rec); err != nil {
			t.Fatal(err)
		}
		if _, err = CallDataHost(ctx, h, "archive.extract", args); !errors.Is(err, ErrQuotaExceeded) {
			t.Fatalf("cache quota err = %v, want ErrQuotaExceeded", err)
		}
		rec.Manifest.Ether.Contributes.MediaKinds[0].Cache.MaxBytes = 32 << 20
		rec.ManifestRaw, _ = json.Marshal(rec.Manifest)
		if err = env.m.store.upsert(ctx, rec); err != nil {
			t.Fatal(err)
		}

		// archiveMemberBytes grant enforced.
		rec.Manifest.Ether.Runtime.Limits = &RuntimeLimits{ArchiveMemberBytes: 512 << 10}
		rec.ManifestRaw, _ = json.Marshal(rec.Manifest)
		if err = env.m.store.upsert(ctx, rec); err != nil {
			t.Fatal(err)
		}
		if _, err = CallDataHost(ctx, h, "archive.extract", args); err == nil {
			t.Fatal("archiveMemberBytes limit not enforced")
		}
		rec.Manifest.Ether.Runtime.Limits = nil
		rec.ManifestRaw, _ = json.Marshal(rec.Manifest)
		if err = env.m.store.upsert(ctx, rec); err != nil {
			t.Fatal(err)
		}

		// Hosted QuickJS ABI + incremental per-MiB governance charging.
		eng := wasm.NewEngine(nil)
		t.Cleanup(func() { _ = eng.Close() })
		jsSrc := `globalThis.handle = (req) => ({ status: 200, body: ether.archive.extract(req.body.ref, 0) });`
		pOk, err := eng.LoadJS(rec.ID, "1.0.0", jsSrc, hostBridge{h: h}, wasm.Limits{UnitBudget: 500})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pOk.Close() })
		reqBody, _ := json.Marshal(map[string]string{"ref": archiveRef})
		wasmResp, err := pOk.Call(ctx, wasm.Request{Method: "POST", Path: "/extract", Body: reqBody})
		if err != nil {
			t.Fatalf("hosted Call: %v", err)
		}
		var jsOut ArchiveExtractResult
		if err = json.Unmarshal(wasmResp.Body, &jsOut); err != nil || jsOut != out {
			t.Fatalf("hosted JS result = %+v (err=%v), want %+v", jsOut, err, out)
		}

		// Tight unit budget (65 < 50 base + 3 started MiB * 10) aborts mid-stream with governance fault.
		pTight, err := eng.LoadJS(rec.ID, "1.0.0", jsSrc, hostBridge{h: h}, wasm.Limits{UnitBudget: 65})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pTight.Close() })
		if _, err = pTight.Call(ctx, wasm.Request{Method: "POST", Path: "/extract", Body: reqBody}); err == nil || !strings.Contains(err.Error(), "budget") {
			t.Fatalf("tight unit budget err = %v, want budget fault", err)
		}
	})
}
