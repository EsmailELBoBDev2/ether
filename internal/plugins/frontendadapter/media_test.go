package frontendadapter

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
)

func TestMediaHandlerSurfacesArchiveUnstreamablePlainly(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	issuer := NewTicketIssuer()
	handler := NewMediaHandler(MediaDeps{
		Tickets: issuer,
		Log:     logger,
		Viewer: func(_ context.Context, id int64) (*core.Viewer, error) {
			return viewerWithLibraries(id, "Reader", core.LibraryMovies), nil
		},
		Open: func(_ context.Context, _ *core.Viewer, _ string, _ []string) (*frontend.KindMedia, error) {
			return nil, fmt.Errorf("open container: %w", core.ErrArchiveUnstreamable)
		},
	})

	token, err := issuer.Issue(StreamGrant{ViewerID: 7, ItemID: "plug:kind:item-1", Adapter: "feed"})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, KindMediaPath+token, nil))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	if got := rec.Header().Get("X-Ether-Media-Error"); got != "archive_unstreamable" {
		t.Fatalf("X-Ether-Media-Error = %q, want archive_unstreamable", got)
	}
	if !strings.Contains(rec.Body.String(), "This archive format or compression method cannot be streamed.") {
		t.Fatalf("unexpected body %q", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "temporarily unavailable") {
		t.Fatalf("body still says temporarily unavailable: %q", rec.Body.String())
	}
	if !strings.Contains(logBuf.String(), "cannot be streamed") {
		t.Fatalf("expected plain log entry, got %q", logBuf.String())
	}
}

func TestMediaHandlerDefaultsEmptyMIMEToOctetStreamNeverVideo(t *testing.T) {
	issuer := NewTicketIssuer()
	payload := append([]byte{0x00, 0x00, 0x00, 0x18}, []byte("ftypisom-opaque-binary")...)
	handler := NewMediaHandler(MediaDeps{
		Tickets: issuer,
		Viewer: func(_ context.Context, id int64) (*core.Viewer, error) {
			return viewerWithLibraries(id, "Reader", core.LibraryMovies), nil
		},
		Open: func(_ context.Context, _ *core.Viewer, _ string, _ []string) (*frontend.KindMedia, error) {
			return &frontend.KindMedia{
				ReaderAt: bytes.NewReader(payload),
				Closer:   io.NopCloser(nil),
				Size:     int64(len(payload)),
				MIME:     "",
				Name:     "payload.bin",
			}, nil
		},
	})

	token, err := issuer.Issue(StreamGrant{ViewerID: 7, ItemID: "plug:kind:bin-1", Adapter: "feed"})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, KindMediaPath+token, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("Content-Type = %q, want application/octet-stream", got)
	}
}
