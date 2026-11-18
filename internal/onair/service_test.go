package onair

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/llm"
	"github.com/braedonsaunders/ether/internal/onair/encode"
	"github.com/braedonsaunders/ether/internal/onair/families"
	"github.com/braedonsaunders/ether/internal/onair/raster"
	"github.com/braedonsaunders/ether/internal/onair/scene"
	"github.com/braedonsaunders/ether/internal/store"
)

func testService(t *testing.T, o Options) *Service {
	t.Helper()
	db, e := store.Open(context.Background(), t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	o.DB = db
	o.Dir = t.TempDir()
	s, e := New(o)
	if e != nil {
		t.Fatal(e)
	}
	s.Start()
	t.Cleanup(s.Close)
	return s
}
func TestAssetAddressAndUsage(t *testing.T) {
	s := testService(t, Options{})
	ctx := context.Background()
	b := []byte("font fixture used directly at storage boundary")
	a, e := s.putAsset(ctx, "one", "image", "", "image/png", b)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(b)
	if a.Digest != hex.EncodeToString(sum[:]) {
		t.Fatal("wrong digest")
	}
	a2, e := s.putAsset(ctx, "two", "image", "", "image/png", b)
	if e != nil || a2.Digest != a.Digest {
		t.Fatal("not deduplicated", e)
	}
	l, e := s.Look(ctx, "nocturne")
	if e != nil {
		t.Fatal(e)
	}
	l.Brand.Logo = a.Digest
	if _, e = s.SaveLook(ctx, l); e != nil {
		t.Fatal(e)
	}
	if e = s.DeleteAsset(ctx, a.Digest); e != ErrUsed {
		t.Fatalf("deleted referenced asset: %v", e)
	}
	if e = s.DeleteLook(ctx, l.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS livetv_channels(id TEXT PRIMARY KEY,data TEXT NOT NULL)`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.db.ExecContext(ctx, `INSERT INTO livetv_channels(id,data) VALUES(?,?)`, "channel", `{"look":{"momentAssets":{"ident":"`+a.Digest+`"}}}`); e != nil {
		t.Fatal(e)
	}
	if e = s.DeleteAsset(ctx, a.Digest); e != ErrUsed {
		t.Fatalf("deleted channel asset: %v", e)
	}
	if _, e = s.db.ExecContext(ctx, `DELETE FROM livetv_channels WHERE id=?`, "channel"); e != nil {
		t.Fatal(e)
	}
	if e = s.DeleteAsset(ctx, a.Digest); e != nil {
		t.Fatal(e)
	}
	as, e := s.Assets(ctx)
	if e != nil || len(as) != 0 {
		t.Fatal("asset retained", e)
	}
}
func TestHashEveryInput(t *testing.T) {
	inputs := []any{"renderer", map[string]any{"duration": 8}, map[string]any{"accent": "red"}, map[string]string{"title": "one"}, map[string]string{"logo": "abc"}, encode.Profile}
	hash := func(v []any) string { return encode.Hash(v[0].(string), v[1], v[2], v[3], v[4], v[5]) }
	base := hash(inputs)
	for i := range inputs {
		v := append([]any{}, inputs...)
		v[i] = "changed"
		if hash(v) == base {
			t.Fatalf("input %d missing", i)
		}
	}
	a := encode.Hash("v", nil, nil, map[string]int{"a": 1, "b": 2}, nil, nil)
	b := encode.Hash("v", nil, nil, map[string]int{"b": 2, "a": 1}, nil, nil)
	if a != b {
		t.Fatal("map order affected content address")
	}
}
func TestEnsureQueuesOnly(t *testing.T) {
	gate := make(chan struct{})
	entered := make(chan struct{}, 1)
	s := testService(t, Options{Render: func(ctx context.Context, d scene.Document, b raster.Bindings, out string) error {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-gate:
		}
		return os.WriteFile(out, []byte("mp4"), 0600)
	}})
	done := make(chan struct{})
	go func() {
		s.Ensure(context.Background(), core.ClipRequest{LookID: "nocturne", Moment: "ident", Bind: map[string]string{"channel.name": "unique"}})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Ensure blocked on renderer")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	close(gate)
	_, e := families.Get("signal", "screen.card")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.UploadAsset(context.Background(), "bad.ttf", "font", "", bytes.NewBufferString(strings.Repeat("x", 20))); e == nil {
		t.Fatal("font without licence accepted")
	}
}

type draftModel struct {
	replies []string
	calls   int
}

func (m *draftModel) Enabled() bool { return true }
func (m *draftModel) Chat(_ context.Context, _ []llm.Message, _ bool) (string, error) {
	v := m.replies[min(m.calls, len(m.replies)-1)]
	m.calls++
	return v, nil
}

// A model correction may change the draft, but never writes owner records.
func TestDraftValidationNeverSaves(t *testing.T) {
	model := &draftModel{}
	s := testService(t, Options{Chat: model})
	base, e := s.Look(context.Background(), "nocturne")
	if e != nil {
		t.Fatal(e)
	}
	draft := base
	draft.Roles = map[string]RoleConfig{}
	for k, v := range base.Roles {
		draft.Roles[k] = v
	}
	draft.Roles["welcome"] = RoleConfig{Duration: 8}
	draft.Scenes = append(append([]StyleScene(nil), base.Scenes...), defaultWelcomeScene("nocturne-welcome"))
	draft.Name = "Generated draft"
	draft.Brand.Tagline = strings.Repeat("x", 41)
	bad, _ := json.Marshal(draft)
	draft.Brand.Tagline = "A quieter evening"
	good, _ := json.Marshal(draft)
	model.replies = []string{string(bad), string(good)}
	got, e := s.Generate(context.Background(), "Make the motion calmer", "", base)
	if e != nil {
		t.Fatal(e)
	}
	if model.calls != 2 || got.Brand.Tagline != draft.Brand.Tagline {
		t.Fatal("validation retry did not correct the draft")
	}
	saved, e := s.Look(context.Background(), base.ID)
	if e != nil || saved.Name != base.Name {
		t.Fatal("draft mutated the saved look", e)
	}
}

// Shared styles must remain reusable when a channel chooses its own moment
// video. Both content and length participate in the content-addressed cache.
func TestChannelMomentAssetIsolation(t *testing.T) {
	look := familyLook("clean", "Clean")
	look.ID = "shared"
	for i := range look.Scenes {
		if look.Scenes[i].Role == "ident" {
			look.Scenes[i].Video = &SceneVideo{AssetDigest: "style-video", Mode: "replace", Fit: "cover"}
		}
	}
	s := &Service{looks: map[string]Look{"shared": look}}
	req := core.ClipRequest{LookID: "shared", Moment: "ident"}
	inherited, ok := s.plan(req)
	if !ok {
		t.Fatal("missing style plan")
	}
	req.AssetDigest = "channel-video"
	req.AssetDuration = 12 * time.Second
	custom, ok := s.plan(req)
	if !ok || custom.AssetDigest != "channel-video" || custom.Doc.Duration != 12 || custom.Hash == inherited.Hash {
		t.Fatal("channel asset missing from render plan")
	}
	selected, _ := pickScene(s.looks["shared"], req)
	if selected.Video.AssetDigest != "style-video" {
		t.Fatal("channel changed shared style")
	}
	req.AssetDuration = 8 * time.Second
	short, _ := s.plan(req)
	if short.Hash == custom.Hash {
		t.Fatal("asset duration absent from cache identity")
	}
	req.LookID = ""
	if _, ok := s.plan(req); !ok {
		t.Fatal("asset-only channel needs no style")
	}
}

// StyleDraftGuide's advertised layer and role lists must match the validator
// tables, server-side draft repair must fix common model errors (such as "line"
// layers, missing IDs, and out-of-range durations), and whole-style generation
// must complete or reject drafts missing any of the 9 playout roles.
func TestStyleDraftGuideAndCompleteRoles(t *testing.T) {
	extractCSVLine := func(prefix string) []string {
		for _, line := range strings.Split(StyleDraftGuide, "\n") {
			if after, ok := strings.CutPrefix(strings.TrimSpace(line), prefix); ok {
				after = strings.TrimSuffix(after, ".")
				parts := strings.Split(after, ",")
				out := make([]string, 0, len(parts))
				for _, p := range parts {
					if v := strings.TrimSpace(p); v != "" {
						out = append(out, v)
					}
				}
				return out
			}
		}
		t.Fatalf("guide missing line starting with %q", prefix)
		return nil
	}

	gotLayers := extractCSVLine("Layer types:")
	if !reflect.DeepEqual(gotLayers, scene.LayerTypes) {
		t.Fatalf("guide layer types %v != validator layer types %v", gotLayers, scene.LayerTypes)
	}
	for _, lt := range gotLayers {
		if lt == "line" {
			t.Fatal("guide must not advertise unsupported line layer")
		}
	}
	gotRoles := extractCSVLine("Required roles (9):")
	if !reflect.DeepEqual(gotRoles, Roles) {
		t.Fatalf("guide roles %v != expected roles %v", gotRoles, Roles)
	}
	for _, line := range strings.Split(StyleDraftGuide, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, `{"v":1,`) {
			if _, err := scene.Decode([]byte(trimmed)); err != nil {
				t.Fatalf("worked example in StyleDraftGuide failed scene.Decode: %v", err)
			}
		}
	}

	ctx := context.Background()
	model := &draftModel{}
	s := testService(t, Options{Chat: model})

	// Server-side repair turns "line" layers, missing IDs, bad durations, and
	// accidental full-frame backdrop/post on overlays into valid scenes.
	rawMalformed := `{
		"name": "Late Signal",
		"brand": {"palette": {"background": "#0a0d14", "accent": "#f59e0b", "text": "#111520", "glow": "#1e293b"}, "type": {"display": "Unknown Font", "body": "Inter"}, "tagline": "After midnight", "energy": 65, "textures": ["grain", "bogus"]},
		"roles": {"ident": {"duration": 6}, "lowerthird": {"duration": 99}},
		"scenes": [
			{"role": "ident", "document": {"duration": 6, "poster": 9, "layers": [
				{"type": "rect", "x": 0, "y": 0, "w": 1920, "h": 1080, "fill": "{bg}", "extraField": true},
				{"type": "line", "x1": 160, "y1": 520, "x2": 1760, "y2": 520, "stroke": "{accent}", "strokeWidth": 3},
				{"type": "text", "text": "{channel.name}", "x": 160, "y": 460, "size": 96, "font": "{type.display}", "fill": "{text}", "behaviors": [{"bounce": {"amplitude": 4, "speed": 1}}]}
			]}},
			{"role": "lowerthird", "document": {"duration": 99, "layers": [
				{"type": "rect", "x": 0, "y": 0, "w": 1920, "h": 1080, "fill": "{bg}"},
				{"type": "rect", "x": 160, "y": 820, "w": 900, "h": 120, "fill": "#080b12@0.85"},
				{"type": "text", "text": "{now.title}", "x": 196, "y": 896, "size": 48, "font": "{type.display}", "fill": "#ffffff"}
			], "post": [{"type": "grain", "amount": 0.2}]}}
		]
	}`
	repaired, err := ParseLookDraft([]byte(rawMalformed))
	if err != nil {
		t.Fatalf("ParseLookDraft failed: %v", err)
	}
	repaired, err = s.Draft(ctx, repaired)
	if err != nil {
		t.Fatalf("Draft after repair failed: %v", err)
	}
	if repaired.Scenes[0].Document.Duration != 8 || repaired.Scenes[0].Document.Layers[1].Type != "path" {
		t.Fatalf("expected snapped duration 8 and line->path repair, got dur=%v type=%q", repaired.Scenes[0].Document.Duration, repaired.Scenes[0].Document.Layers[1].Type)
	}
	if repaired.Scenes[1].Document.Duration != 28 || len(repaired.Scenes[1].Document.Post) != 0 || len(repaired.Scenes[1].Document.Layers) != 2 {
		t.Fatalf("expected lowerthird clamped to 28s with stripped full-frame backdrop/post, got %+v", repaired.Scenes[1].Document)
	}

	// Attempt 1 returns only the 4 historical roles; attempt 2 supplies the
	// remaining 5 roles, which Generate merges and validates into a 9-role style.
	full := defaultBaseLook()
	full.Name = "Synthwave 84"
	fourRoles := full
	fourRoles.Roles = map[string]RoleConfig{}
	fourRoles.Scenes = nil
	fiveRoles := full
	fiveRoles.Roles = map[string]RoleConfig{}
	fiveRoles.Scenes = nil
	initialSet := map[string]bool{"ident": true, "upnext": true, "break": true, "back": true}
	for _, sc := range full.Scenes {
		if initialSet[sc.Role] {
			fourRoles.Roles[sc.Role] = RoleConfig{Duration: sc.Document.Duration}
			fourRoles.Scenes = append(fourRoles.Scenes, sc)
		} else {
			fiveRoles.Roles[sc.Role] = RoleConfig{Duration: sc.Document.Duration}
			fiveRoles.Scenes = append(fiveRoles.Scenes, sc)
		}
	}
	bFour, _ := json.Marshal(fourRoles)
	bFive, _ := json.Marshal(fiveRoles)
	model.replies = []string{string(bFour), string(bFive)}
	model.calls = 0

	completed, err := s.Generate(ctx, "Create an 80s synthwave movie channel", "", Look{})
	if err != nil {
		t.Fatalf("expected retry to complete missing roles, got error: %v", err)
	}
	if model.calls != 2 {
		t.Fatalf("expected 2 model calls to complete missing roles, got %d", model.calls)
	}
	if missing := MissingRoles(completed); len(missing) != 0 {
		t.Fatalf("completed style still missing roles: %v", missing)
	}

	// A draft that never supplies all 9 roles across retries is rejected.
	model.replies = []string{string(bFour)}
	model.calls = 0
	if _, err := s.Generate(ctx, "Create a channel style", "", Look{}); err == nil || !strings.Contains(err.Error(), "missing required roles") {
		t.Fatalf("expected incomplete style to be rejected, got: %v", err)
	}
}
