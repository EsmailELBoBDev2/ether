package identity_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/frontend/frontendtest"
	"github.com/braedonsaunders/ether/internal/frontend/kit/authhdr"
	"github.com/braedonsaunders/ether/internal/frontend/kit/ids"
	"github.com/braedonsaunders/ether/internal/identity"
	"github.com/braedonsaunders/ether/internal/store"
)

type channelArtworkFixture struct {
	frontendtest.Artwork
	visible map[string][]frontend.Image
}

func (f *channelArtworkFixture) List(_ context.Context, req frontend.ArtworkRequest) ([]frontend.Image, bool, error) {
	if req.Channel == "" {
		return nil, false, core.ErrNotFound
	}
	images, ok := f.visible[req.Channel]
	if !ok {
		return nil, false, core.ErrUnauthorized
	}
	if req.Programme != "" {
		var filtered []frontend.Image
		for _, img := range images {
			if img.Subject == req.Programme {
				filtered = append(filtered, img)
			}
		}
		return filtered, true, nil
	}
	return images, true, nil
}

func (f *channelArtworkFixture) Read(_ context.Context, req frontend.ArtworkRequest, img frontend.Image, _ frontend.ArtworkTransform) ([]byte, string, error) {
	return []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"/>"), "image/svg+xml", nil
}

func TestChannelImageGrantsStatelessDeterministicAndScoped(t *testing.T) {
	ctx := context.Background()
	db := store.OpenTest(t)
	if _, err := db.ExecContext(ctx, `INSERT INTO auth_users(id,plex_id,created_at,updated_at) VALUES(1,123,0,0)`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 18, 18, 0, 10, 0, time.UTC)
	clock := func() time.Time { return now }
	grants, err := identity.NewGrants(db, members{db}, time.Hour, clock)
	if err != nil {
		t.Fatal(err)
	}
	device := core.Device{ID: "tv-1", Client: "Vela", Product: "Vela"}
	velaCred, err := grants.Issue(ctx, 1, nil, "vela", device)
	if err != nil {
		t.Fatal(err)
	}
	jfCred, err := grants.Issue(ctx, 1, nil, "jellyfin", device)
	if err != nil {
		t.Fatal(err)
	}

	artwork := &channelArtworkFixture{visible: map[string][]frontend.Image{}}
	for i := 0; i < 5000; i++ {
		chID := fmt.Sprintf("ch-%d", i)
		artwork.visible[chID] = []frontend.Image{
			{Kind: frontend.ImageChannel, Path: fmt.Sprintf("logo-%d", i), Tag: "v1"},
		}
	}
	artwork.visible["ch-a"] = []frontend.Image{
		{Kind: frontend.ImageChannel, Path: "logo-a", Tag: "v1"},
		{Kind: frontend.ImageStill, Path: "prog-a", Tag: "v1", Subject: "airing-a"},
	}
	artwork.visible["ch-b"] = []frontend.Image{
		{Kind: frontend.ImageChannel, Path: "logo-b", Tag: "v1"},
	}

	makeSvc := func(surface frontend.Protocol) *identity.ImageGrantService {
		svc, err := identity.NewArtworkImageGrantService(
			db,
			surface,
			grants,
			func() frontend.Experience { return frontendtest.Experience{} },
			artwork,
			5*time.Minute,
			clock,
			identity.ImageGrantOptions{Token: authhdr.MediaBrowserToken},
		)
		if err != nil {
			t.Fatal(err)
		}
		svc.SetImageKey([]byte("test-node-image-key"))
		return svc
	}
	velaSvc := makeSvc("vela")
	jfSvc := makeSvc("jellyfin")

	// 1. Issue 5,000 channel grants for one viewer: zero DB rows written.
	for i := 0; i < 5000; i++ {
		chID := fmt.Sprintf("ch-%d", i)
		secret, err := velaSvc.Issue(ctx, frontend.ImageGrantRequest{
			Credential: velaCred,
			Device:     device,
			Channel:    chID,
			Image:      artwork.visible[chID][0],
		})
		if err != nil || !strings.HasPrefix(secret, "e1.") {
			t.Fatalf("issue %d: secret=%q err=%v", i, secret, err)
		}
	}
	var imageRows, headerRows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM frontend_image_grants`).Scan(&imageRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM capability_headers WHERE namespace='image'`).Scan(&headerRows); err != nil {
		t.Fatal(err)
	}
	if imageRows != 0 || headerRows != 0 {
		t.Fatalf("expected zero persisted channel grant rows, got imageRows=%d headerRows=%d", imageRows, headerRows)
	}

	// 2. Deterministic within the same expiry bucket; rotates on next bucket.
	reqA := frontend.ImageGrantRequest{
		Credential: velaCred,
		Device:     device,
		Channel:    "ch-a",
		Image:      artwork.visible["ch-a"][0],
	}
	sec1, err := velaSvc.Issue(ctx, reqA)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(20 * time.Second)
	sec2, err := velaSvc.Issue(ctx, reqA)
	if err != nil {
		t.Fatal(err)
	}
	if sec1 != sec2 {
		t.Fatalf("expected deterministic channel secret inside bucket: %q != %q", sec1, sec2)
	}
	bucket1 := velaSvc.GrantBucket()
	now = now.Add(time.Minute)
	sec3, err := velaSvc.Issue(ctx, reqA)
	if err != nil {
		t.Fatal(err)
	}
	if sec3 == sec1 || velaSvc.GrantBucket() == bucket1 {
		t.Fatalf("expected secret and GrantBucket to rotate across expiry bucket")
	}

	// 3. Served image for visible channel returns 200.
	serve := func(svc *identity.ImageGrantService, path, secret string) int {
		w := httptest.NewRecorder()
		svc.Serve(w, httptest.NewRequest("GET", path, nil), secret)
		return w.Code
	}
	if code := serve(velaSvc, "/vela/v0/images/"+sec3, sec3); code != 200 {
		t.Fatalf("expected 200 for visible channel logo, got %d", code)
	}

	// 4. Grant for channel A fails (404) for channel B's image and unlisted image.
	unlisted := reqA
	unlisted.Image = artwork.visible["ch-b"][0]
	if _, err := velaSvc.Issue(ctx, unlisted); !errors.Is(err, core.ErrUnauthorized) {
		t.Fatalf("expected unauthorized issuing channel B image under channel A, got %v", err)
	}
	jfSecA, err := jfSvc.Issue(ctx, frontend.ImageGrantRequest{
		Credential: jfCred,
		Device:     device,
		Channel:    "ch-a",
		Image:      artwork.visible["ch-a"][0],
	})
	if err != nil {
		t.Fatal(err)
	}
	guidA, _ := ids.StringGUID(ids.Channel, "ch-a")
	guidB, _ := ids.StringGUID(ids.Channel, "ch-b")
	if code := serve(jfSvc, "/Items/"+guidA+"/Images/Primary", jfSecA); code != 200 {
		t.Fatalf("expected 200 on matching channel GUID, got %d", code)
	}
	if code := serve(jfSvc, "/Items/"+guidB+"/Images/Primary", jfSecA); code != 404 {
		t.Fatalf("expected 404 serving channel A grant on channel B path, got %d", code)
	}
	if code := serve(jfSvc, "/Items/"+ids.RatingKeyGUID(core.MovieKey(7))+"/Images/Primary", jfSecA); code != 404 {
		t.Fatalf("expected 404 serving channel A grant on library title path, got %d", code)
	}

	// 5. Channel hidden after issue returns 404.
	savedA := artwork.visible["ch-a"]
	delete(artwork.visible, "ch-a")
	if code := serve(velaSvc, "/vela/v0/images/"+sec3, sec3); code != 404 {
		t.Fatalf("expected 404 when channel hidden after issue, got %d", code)
	}
	artwork.visible["ch-a"] = savedA

	// 6. Explicit Revoke denies the grant, and credential revocation returns 404.
	if err := velaSvc.Revoke(ctx, velaCred, sec3); err != nil {
		t.Fatal(err)
	}
	if code := serve(velaSvc, "/vela/v0/images/"+sec3, sec3); code != 404 {
		t.Fatalf("expected 404 after explicit Revoke, got %d", code)
	}
	sec4, err := velaSvc.Issue(ctx, reqA)
	if err != nil || sec4 == sec3 {
		t.Fatalf("expected fresh secret after explicit revoke, got %q err=%v", sec4, err)
	}
	if code := serve(velaSvc, "/vela/v0/images/"+sec4, sec4); code != 200 {
		t.Fatalf("expected 200 for re-issued secret, got %d", code)
	}
	sum := sha256.Sum256([]byte(velaCred))
	if err := grants.RevokeGrant(ctx, 1, hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	if code := serve(velaSvc, "/vela/v0/images/"+sec4, sec4); code != 404 {
		t.Fatalf("expected 404 after credential revocation, got %d", code)
	}
}
