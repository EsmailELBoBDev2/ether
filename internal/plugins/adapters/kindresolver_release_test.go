package adapters

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

type captureDebridBackend struct {
	lastRelease core.Release
}

func (c *captureDebridBackend) CheckCached(_ context.Context, hashes []string) (map[string]bool, bool) {
	out := make(map[string]bool, len(hashes))
	return out, true
}

func (c *captureDebridBackend) Acquire(_ context.Context, rel core.Release) ([]byte, error) {
	c.lastRelease = rel
	return []byte("payload"), nil
}

type stubEngine struct {
	core.Engine
}

func (stubEngine) Open(_ context.Context, id string, data []byte) (core.MediaSource, error) {
	return stubMediaSource{id: id, data: data}, nil
}

type stubMediaSource struct {
	id   string
	data []byte
}

func (s stubMediaSource) ReleaseID() string                       { return s.id }
func (s stubMediaSource) Name() string                            { return "file.epub" }
func (s stubMediaSource) Size() int64                             { return int64(len(s.data)) }
func (s stubMediaSource) ReadAt(p []byte, off int64) (int, error) { return bytes.NewReader(s.data).ReadAt(p, off) }
func (s stubMediaSource) Close() error                            { return nil }

func TestKindResolverReleaseExplicitSelectorsEnableAllowNonVideo(t *testing.T) {
	ctx := context.Background()
	deb := &captureDebridBackend{}
	kr := NewKindResolver(KindResolverDeps{
		Debrid: deb,
		Engine: stubEngine{},
	})

	// Default HTTP client must honour request context (Timeout == 0) with 30s response-header timeout.
	if kr.http.Timeout != 0 {
		t.Fatalf("NewKindResolver default HTTP Timeout = %v, want 0", kr.http.Timeout)
	}
	if tr, ok := kr.http.Transport.(*http.Transport); !ok || tr.ResponseHeaderTimeout != 30*time.Second {
		t.Fatalf("NewKindResolver default Transport ResponseHeaderTimeout = %v, want 30s", kr.http.Transport)
	}

	idx0 := 0
	idx2 := 2
	for _, tc := range []struct {
		name      string
		hint      core.ReleaseHint
		wantAllow bool
		wantIdx   *int
		wantExact string
	}{
		{
			name:      "explicit release.file",
			hint:      core.ReleaseHint{InfoHash: "0123456789abcdef0123456789abcdef01234567", File: "Books/Title.epub"},
			wantAllow: true,
			wantExact: "Books/Title.epub",
		},
		{
			name:      "explicit release.fileIndex",
			hint:      core.ReleaseHint{InfoHash: "0123456789abcdef0123456789abcdef01234567", FileIndex: &idx0},
			wantAllow: true,
			wantIdx:   &idx0,
		},
		{
			name: "explicit release.candidate.fileIndex",
			hint: core.ReleaseHint{
				Candidate: &core.SourceCandidate{
					InfoHash:  "0123456789abcdef0123456789abcdef01234567",
					FileIndex: &idx2,
				},
			},
			wantAllow: true,
			wantIdx:   &idx2,
		},
		{
			name:      "automatic selection without explicit selector",
			hint:      core.ReleaseHint{InfoHash: "0123456789abcdef0123456789abcdef01234567"},
			wantAllow: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deb.lastRelease = core.Release{}
			ms, err := kr.openRelease(ctx, tc.hint)
			if err != nil {
				t.Fatalf("openRelease: %v", err)
			}
			_ = ms.Close()

			if !tc.wantAllow {
				if deb.lastRelease.Target != nil {
					t.Fatalf("expected nil Target for automatic selection, got %+v", deb.lastRelease.Target)
				}
				return
			}
			if deb.lastRelease.Target == nil || !deb.lastRelease.Target.AllowNonVideo {
				t.Fatalf("expected Target.AllowNonVideo=true, got %+v", deb.lastRelease.Target)
			}
			if deb.lastRelease.Target.ExactFile != tc.wantExact {
				t.Fatalf("ExactFile = %q, want %q", deb.lastRelease.Target.ExactFile, tc.wantExact)
			}
			if (tc.wantIdx == nil) != (deb.lastRelease.Target.FileIndex == nil) ||
				(tc.wantIdx != nil && *deb.lastRelease.Target.FileIndex != *tc.wantIdx) {
				t.Fatalf("FileIndex = %v, want %v", deb.lastRelease.Target.FileIndex, tc.wantIdx)
			}
		})
	}

	// Empty MIME on kindLocalFile defaults to application/octet-stream.
	lf := &kindLocalFile{mime: ""}
	if got := lf.MIME(); got != "application/octet-stream" {
		t.Fatalf("kindLocalFile.MIME() = %q, want application/octet-stream", got)
	}
}
