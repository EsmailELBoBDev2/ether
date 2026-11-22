package kindstream

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	at "github.com/braedonsaunders/ether/internal/archive/archivetest"
	"github.com/braedonsaunders/ether/internal/blobcache"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/debrid"
	"github.com/braedonsaunders/ether/internal/nzb"
	"github.com/braedonsaunders/ether/internal/stream"
)

type memFetcher struct {
	mu       sync.Mutex
	articles map[string]struct {
		data []byte
		part core.ArticlePart
	}
}

func newMemFetcher() *memFetcher {
	return &memFetcher{articles: map[string]struct {
		data []byte
		part core.ArticlePart
	}{}}
}

func (m *memFetcher) Body(ctx context.Context, id string, groups []string, prio core.Priority) ([]byte, error) {
	b, _, err := m.BodyPart(ctx, id, groups, prio)
	return b, err
}

func (m *memFetcher) BodyPart(_ context.Context, id string, _ []string, _ core.Priority) ([]byte, core.ArticlePart, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.articles[id]
	if !ok {
		return nil, core.ArticlePart{}, core.ErrArticleMissing
	}
	return bytes.Clone(a.data), a.part, nil
}

func (m *memFetcher) Stat(_ context.Context, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.articles[id]
	return ok, nil
}

func postTestNZB(f *memFetcher, partSize int, files []struct {
	name string
	data []byte
}) []byte {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?><nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">`)
	for i, pf := range files {
		var parts []int
		for off := 0; off < len(pf.data); off += partSize {
			parts = append(parts, min(partSize, len(pf.data)-off))
		}
		subj := fmt.Sprintf(`[%d/%d] - "%s" yEnc (1/%d)`, i+1, len(files), pf.name, len(parts))
		fmt.Fprintf(&sb, `<file poster="p@test" date="1" subject="%s"><groups><group>alt.binaries.test</group></groups><segments>`, html.EscapeString(subj))
		off := 0
		for pi, sz := range parts {
			n := pi + 1
			id := fmt.Sprintf("%s.%d@test", strings.ReplaceAll(pf.name, " ", "_"), n)
			f.mu.Lock()
			f.articles[id] = struct {
				data []byte
				part core.ArticlePart
			}{
				data: pf.data[off : off+sz],
				part: core.ArticlePart{Name: pf.name, FileSize: int64(len(pf.data)), Part: n, Total: len(parts), Begin: int64(off) + 1, End: int64(off + sz)},
			}
			f.mu.Unlock()
			fmt.Fprintf(&sb, `<segment bytes="%d" number="%d">%s</segment>`, sz+200, n, html.EscapeString(id))
			off += sz
		}
		sb.WriteString(`</segments></file>`)
	}
	sb.WriteString(`</nzb>`)
	return []byte(sb.String())
}

func readAllByteSource(t *testing.T, bs ByteSource) []byte {
	t.Helper()
	out := make([]byte, bs.Size())
	n, err := bs.ReadAt(out, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt(0, %d): %v", bs.Size(), err)
	}
	if int64(n) != bs.Size() {
		t.Fatalf("ReadAt got %d bytes, want %d", n, bs.Size())
	}
	return out
}

func TestOpenReleaseUsenetCompressedArchivesEndToEnd(t *testing.T) {
	ctx := context.Background()
	f := newMemFetcher()
	eng := stream.New(f, nil, stream.Options{}, nil)
	t.Cleanup(func() { _ = eng.Close() })

	cache, err := blobcache.Open(t.TempDir(), blobcache.Options{SweepPeriod: -1})
	if err != nil {
		t.Fatalf("blobcache.Open: %v", err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	ns := cache.Namespace("nonvideo-test", blobcache.Quota{MaxBytes: 64 << 20})

	// 1. Deflate ZIP containing a PDF book (declared as default application/epub+zip -> sniffs application/pdf).
	pdfData := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("chapter one content line\n"), 2000)...)
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	w, _ := zw.CreateHeader(&zip.FileHeader{Name: "cover.jpg", Method: zip.Deflate})
	_, _ = w.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 16})
	w, _ = zw.CreateHeader(&zip.FileHeader{Name: "Books/Dune.pdf", Method: zip.Deflate})
	_, _ = w.Write(pdfData)
	_ = zw.Close()
	zipNZB := postTestNZB(f, 8*1024, []struct {
		name string
		data []byte
	}{{name: "Dune.zip", data: zipBuf.Bytes()}})

	// 2. LZMA2 7z containing a FLAC track.
	flacData := append([]byte("fLaC"), bytes.Repeat([]byte("audio-frame-payload-1234567890"), 1500)...)
	arc7z := at.Build7zLZMA2([]at.SevenZipFile{
		{Name: "cover.jpg", Data: []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 16}},
		{Name: "02 - Dreams.flac", Data: flacData},
	}, false)
	sevenZipNZB := postTestNZB(f, 8*1024, []struct {
		name string
		data []byte
	}{{name: "Album.7z", data: arc7z}})

	// 3. Multi-volume compressed RAR5 fixture (3 volumes) with explicit fileHint.
	var rarFiles []struct {
		name string
		data []byte
	}
	for _, name := range []string{"multi.part1.rar", "multi.part2.rar", "multi.part3.rar"} {
		b, err := os.ReadFile(filepath.Join("..", "archive", "testdata", "altmount", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		rarFiles = append(rarFiles, struct {
			name string
			data []byte
		}{name: name, data: b})
	}
	rarNZB := postTestNZB(f, 4*1024, rarFiles)

	nzbs := map[string][]byte{
		"https://idx.test/dune.nzb":  zipNZB,
		"https://idx.test/album.nzb": sevenZipNZB,
		"https://idx.test/multi.nzb": rarNZB,
	}

	deps := Deps{
		Cache: ns,
		OpenReleaseSource: func(ctx context.Context, src core.PlayableSource) (core.MediaSource, error) {
			raw, ok := nzbs[src.Release.NZBURL]
			if !ok {
				return nil, core.ErrNotAvailable
			}
			tgt := &core.ReleaseTarget{
				AllowNonVideo: true,
				Member:        src.Release.FileHint,
				MIME:          src.MIME,
			}
			prepared, err := debrid.EpisodeNZB(raw, tgt)
			if err != nil {
				return nil, err
			}
			return eng.Open(ctx, src.ID, prepared)
		},
	}

	// Verify Deflate ZIP book -> sniffed as application/pdf, supports Range ReadAt.
	bsPDF, err := Open(ctx, core.PlayableSource{
		ID:      "book-zip",
		Pattern: PatternRelease,
		MIME:    "application/epub+zip",
		Release: &core.ReleaseHint{NZBURL: "https://idx.test/dune.nzb"},
	}, deps)
	if err != nil {
		t.Fatalf("Open deflate ZIP book: %v", err)
	}
	if bsPDF.MIME() != "application/pdf" || bsPDF.Name() != "Dune.pdf" {
		_ = bsPDF.Close()
		t.Fatalf("ZIP book = (name=%q, mime=%q), want (Dune.pdf, application/pdf)", bsPDF.Name(), bsPDF.MIME())
	}
	if !bytes.Equal(readAllByteSource(t, bsPDF), pdfData) {
		_ = bsPDF.Close()
		t.Fatal("ZIP book bytes mismatch")
	}
	// Non-zero offset Range read.
	rangeBuf := make([]byte, 128)
	if n, err := bsPDF.ReadAt(rangeBuf, 4096); err != nil || n != 128 || !bytes.Equal(rangeBuf, pdfData[4096:4224]) {
		_ = bsPDF.Close()
		t.Fatalf("ZIP book range ReadAt(4096): n=%d err=%v", n, err)
	}
	_ = bsPDF.Close()

	// Verify LZMA2 7z FLAC track.
	bsFLAC, err := Open(ctx, core.PlayableSource{
		ID:      "album-7z",
		Pattern: PatternRelease,
		MIME:    "audio/flac",
		Release: &core.ReleaseHint{NZBURL: "https://idx.test/album.nzb"},
	}, deps)
	if err != nil {
		t.Fatalf("Open LZMA2 7z audio: %v", err)
	}
	if bsFLAC.MIME() != "audio/flac" || bsFLAC.Name() != "02 - Dreams.flac" {
		_ = bsFLAC.Close()
		t.Fatalf("7z audio = (name=%q, mime=%q), want (02 - Dreams.flac, audio/flac)", bsFLAC.Name(), bsFLAC.MIME())
	}
	if !bytes.Equal(readAllByteSource(t, bsFLAC), flacData) {
		_ = bsFLAC.Close()
		t.Fatal("7z audio bytes mismatch")
	}
	_ = bsFLAC.Close()

	// Verify multi-volume compressed RAR5 via fileHint.
	bsRAR, err := Open(ctx, core.PlayableSource{
		ID:      "rar-multi",
		Pattern: PatternRelease,
		MIME:    "application/octet-stream",
		Release: &core.ReleaseHint{NZBURL: "https://idx.test/multi.nzb", FileHint: "release.nzb"},
	}, deps)
	if err != nil {
		t.Fatalf("Open multi-vol compressed RAR: %v", err)
	}
	gotRAR := readAllByteSource(t, bsRAR)
	_ = bsRAR.Close()
	if len(gotRAR) != 10945 {
		t.Fatalf("multi-vol compressed RAR length = %d, want 10945", len(gotRAR))
	}

	if st := eng.Stats(); st.ActiveSources != 0 {
		t.Fatalf("engine ActiveSources after closes = %d, want 0", st.ActiveSources)
	}
}

func TestOpenSourcesNonVideoFailFastAndGrabCap(t *testing.T) {
	ctx := context.Background()

	makeUsenetSources := func(n int, mime string) []core.PlayableSource {
		out := make([]core.PlayableSource, n)
		for i := range n {
			out[i] = core.PlayableSource{
				ID:      fmt.Sprintf("usenet-%d", i+1),
				Pattern: PatternRelease,
				MIME:    mime,
				Release: &core.ReleaseHint{NZBURL: fmt.Sprintf("https://idx.test/r%d.nzb", i+1)},
			}
		}
		return out
	}

	// 1. Structural ErrNoUsableFile on non-video Usenet stops after 1 grab (not 10).
	var grabs atomic.Int32
	_, _, err := OpenSources(ctx, makeUsenetSources(10, "audio/flac"), Deps{
		OpenReleaseSource: func(_ context.Context, _ core.PlayableSource) (core.MediaSource, error) {
			grabs.Add(1)
			return nil, fmt.Errorf("%w: %w", core.ErrReleaseRejected, stream.ErrNoPlayableFile)
		},
	})
	if err == nil {
		t.Fatal("expected error when all non-video releases have no usable file")
	}
	if grabs.Load() != 1 {
		t.Fatalf("structural ErrNoUsableFile made %d grabs, want 1", grabs.Load())
	}

	// 2. Different candidate shape (debrid torrent) is still attempted after Usenet structural failure.
	var usenetGrabs, torrentGrabs atomic.Int32
	mixed := append(makeUsenetSources(5, "audio/flac"), core.PlayableSource{
		ID:      "torrent-1",
		Pattern: PatternRelease,
		MIME:    "audio/flac",
		Release: &core.ReleaseHint{InfoHash: "0123456789abcdef0123456789abcdef01234567"},
	})
	flacSample := append([]byte("fLaC"), bytes.Repeat([]byte{0x11}, 512)...)
	bs, winner, err := OpenSources(ctx, mixed, Deps{
		OpenReleaseSource: func(_ context.Context, src core.PlayableSource) (core.MediaSource, error) {
			if src.Release.NZBURL != "" {
				usenetGrabs.Add(1)
				return nil, fmt.Errorf("%w: %w", core.ErrReleaseRejected, stream.ErrNoPlayableFile)
			}
			torrentGrabs.Add(1)
			return &fakeMediaSource{id: src.ID, name: "01 - Track.flac", data: flacSample}, nil
		},
	})
	if err != nil {
		t.Fatalf("mixed Usenet + torrent failed: %v", err)
	}
	_ = bs.Close()
	if winner.ID != "torrent-1" || usenetGrabs.Load() != 1 || torrentGrabs.Load() != 1 {
		t.Fatalf("mixed winner=%q usenetGrabs=%d torrentGrabs=%d, want (torrent-1, 1, 1)", winner.ID, usenetGrabs.Load(), torrentGrabs.Load())
	}

	// 3. Non-structural failures (DMCA / missing articles) obey DefaultMaxNonVideoReleaseGrabs (3, not 10).
	var dmcaGrabs atomic.Int32
	_, _, err = OpenSources(ctx, makeUsenetSources(10, "application/epub+zip"), Deps{
		OpenReleaseSource: func(_ context.Context, _ core.PlayableSource) (core.MediaSource, error) {
			dmcaGrabs.Add(1)
			return nil, core.ErrArticleMissing
		},
	})
	if err == nil {
		t.Fatal("expected error when all Usenet releases are DMCA")
	}
	if int(dmcaGrabs.Load()) != DefaultMaxNonVideoReleaseGrabs {
		t.Fatalf("DMCA non-video grabs = %d, want %d", dmcaGrabs.Load(), DefaultMaxNonVideoReleaseGrabs)
	}

	// 4. Video releases are 100% unaffected: all 5 candidates tried until #5 succeeds.
	var videoGrabs atomic.Int32
	mkvSample := append([]byte{0x1a, 0x45, 0xdf, 0xa3}, bytes.Repeat([]byte{0x22}, 512)...)
	bsVid, winVid, err := OpenSources(ctx, makeUsenetSources(5, "video/x-matroska"), Deps{
		OpenReleaseSource: func(_ context.Context, src core.PlayableSource) (core.MediaSource, error) {
			videoGrabs.Add(1)
			if src.ID != "usenet-5" {
				return nil, fmt.Errorf("%w: %w", core.ErrReleaseRejected, stream.ErrNoPlayableFile)
			}
			return &fakeMediaSource{id: src.ID, name: "Movie.2024.mkv", data: mkvSample}, nil
		},
	})
	if err != nil {
		t.Fatalf("video failover failed: %v", err)
	}
	_ = bsVid.Close()
	if winVid.ID != "usenet-5" || videoGrabs.Load() != 5 {
		t.Fatalf("video failover winner=%q grabs=%d, want (usenet-5, 5)", winVid.ID, videoGrabs.Load())
	}

	// Sanity-check that nzb.IsNonVideoMIME stays aligned with book/audio families.
	if !nzb.IsNonVideoMIME("audio/flac") || !nzb.IsNonVideoMIME("application/epub+zip") || nzb.IsNonVideoMIME("video/x-matroska") {
		t.Fatal("unexpected nzb.IsNonVideoMIME classification")
	}
}
