package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/llm"
	"github.com/braedonsaunders/ether/internal/plugins/wasm"
)

func aiManifestJSON(aiPerm string, schedule []map[string]any) []byte {
	etherBlock := map[string]any{
		"protocol":     1,
		"kind":         "extension",
		"summary":      "Tags titles using the owner language model",
		"runtime":      map[string]any{"kind": "hosted", "entry": "dist/plugin.js"},
		"capabilities": []string{},
	}
	if aiPerm != "" {
		etherBlock["permissions"] = map[string]any{"ai": aiPerm}
	}
	if len(schedule) > 0 {
		etherBlock["schedule"] = schedule
	}
	raw, _ := json.Marshal(map[string]any{
		"id":      "community.example.ai",
		"version": "1.0.0",
		"name":    "AI Tagger",
		"ether":   etherBlock,
	})
	return raw
}

func TestAIManifestPermissionSheetAndDiff(t *testing.T) {
	bad, err := ParseManifest(aiManifestJSON("write", nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "ether.permissions.ai") {
		t.Fatalf("expected validation error for permissions.ai=write, got %v", err)
	}

	noneMan, err := ParseManifest(aiManifestJSON("none", nil))
	if err != nil || noneMan.Validate() != nil {
		t.Fatalf("permissions.ai=none failed: %v %v", err, noneMan.Validate())
	}
	noneLines, _, _ := PermissionSheet(noneMan)
	if slices.Contains(noneLines, "Uses your configured language model") {
		t.Fatalf("permissions.ai=none should not appear on PermissionSheet: %v", noneLines)
	}

	aiMan, err := ParseManifest(aiManifestJSON("complete", nil))
	if err != nil || aiMan.Validate() != nil {
		t.Fatalf("permissions.ai=complete failed: %v %v", err, aiMan.Validate())
	}
	aiLines, _, _ := PermissionSheet(aiMan)
	if !slices.Contains(aiLines, "Uses your configured language model") {
		t.Fatalf("PermissionSheet missing AI line: %v", aiLines)
	}
	added := PermissionDiff(noneMan, aiMan)
	if !slices.Contains(added, "Uses your configured language model") {
		t.Fatalf("PermissionDiff missing AI addition: %v", added)
	}
}

func TestHostAICompleteValidationSchemaBoundsAndSecrets(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	var backendReply string
	var backendErr error
	var backendCalls int
	var lastReq AICompleteBackendRequest
	env.m.deps.Backends.AIComplete = func(_ context.Context, req AICompleteBackendRequest) (string, error) {
		backendCalls++
		lastReq = req
		return backendReply, backendErr
	}

	// Install plugin with permissions.ai: "complete".
	prev, err := env.m.PreviewInstall(ctx, Source{
		Kind:     "studio",
		Manifest: aiManifestJSON("complete", []map[string]any{{"every": "1h", "call": "/ether/refresh"}}),
		Files:    map[string][]byte{"dist/plugin.js": []byte(`globalThis.handle = function(){ return { status: 200, body: "{}" }; };`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := env.m.Install(ctx, prev, InstallOptions{AcceptAddedPermissions: true})
	if err != nil {
		t.Fatal(err)
	}
	h := env.m.HostFor(rec.ID)

	// 1. Structured JSON Schema validation with fenced reply + canonical output.
	backendReply = "```json\n{\"genres\":[\"Sci-Fi\"],\"year\":1999}\n```"
	schema := json.RawMessage(`{
		"type":"object",
		"required":["genres"],
		"properties":{
			"genres":{"type":"array","items":{"type":"string"}},
			"year":{"type":"integer"}
		},
		"additionalProperties":false
	}`)
	actionCtx := withCallPath(ctx, "/ether/action/classify")
	resp, err := h.AIComplete(actionCtx, AICompleteRequest{
		Messages:  []AIMessage{{Role: "user", Content: "The Matrix"}},
		Schema:    schema,
		MaxTokens: 256,
	})
	if err != nil {
		t.Fatalf("AIComplete structured: %v", err)
	}
	if !lastReq.JSONMode || lastReq.MaxTokens != 256 {
		t.Fatalf("unexpected backend req: %+v", lastReq)
	}
	if string(resp.JSON) != resp.Text || !strings.Contains(resp.Text, `"Sci-Fi"`) {
		t.Fatalf("unexpected structured resp: %+v", resp)
	}

	// 2. Schema mismatch yields ErrInvalidOutput.
	backendReply = `{"genres":"not-an-array"}`
	if _, err := h.AIComplete(actionCtx, AICompleteRequest{
		Messages: []AIMessage{{Role: "user", Content: "bad shape"}},
		Schema:   schema,
	}); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("schema mismatch: got %v, want ErrInvalidOutput", err)
	}

	// 3. External $ref in schema is refused before hitting backend.
	callsBefore := backendCalls
	if _, err := h.AIComplete(actionCtx, AICompleteRequest{
		Messages: []AIMessage{{Role: "user", Content: "ssrf"}},
		Schema:   json.RawMessage(`{"$ref":"https://evil.example/schema.json"}`),
	}); err == nil {
		t.Fatal("expected validation error for external $ref schema")
	}
	if backendCalls != callsBefore {
		t.Fatalf("external $ref should not reach backend (calls %d -> %d)", callsBefore, backendCalls)
	}

	// 4. Provider errors NEVER leak credentials, internal URLs, or upstream response bodies.
	backendReply = ""
	backendErr = errors.New("POST http://10.0.0.5:8000/v1/chat failed with Authorization Bearer sk-secret-owner-key-999")
	_, err = h.AIComplete(actionCtx, AICompleteRequest{
		Messages: []AIMessage{{Role: "user", Content: "hi"}},
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("provider error: got %v, want ErrUnavailable", err)
	}
	if strings.Contains(err.Error(), "sk-secret") || strings.Contains(err.Error(), "10.0.0.5") {
		t.Fatalf("provider error leaked secret/URL to plugin: %v", err)
	}

	// 5. ErrNotConfigured returns clearly and does NOT burn rate-limit quota.
	env.m.mu.Lock()
	env.m.aiCompleteLimit = 2
	env.m.aiCompleteTimes[rec.ID] = nil
	env.m.mu.Unlock()
	backendErr = llm.ErrNotConfigured
	for range 5 {
		if _, err := h.AIComplete(actionCtx, AICompleteRequest{
			Messages: []AIMessage{{Role: "user", Content: "probe"}},
		}); !errors.Is(err, ErrNotConfigured) {
			t.Fatalf("unconfigured backend: got %v, want ErrNotConfigured", err)
		}
	}
	// Rate limit still has full budget of 2 after repeated not_configured calls.
	backendErr = nil
	backendReply = "ok"
	for i := range 2 {
		if _, err := h.AIComplete(actionCtx, AICompleteRequest{
			Messages: []AIMessage{{Role: "user", Content: "ok"}},
		}); err != nil {
			t.Fatalf("call %d after not_configured failed: %v", i, err)
		}
	}
	if _, err := h.AIComplete(actionCtx, AICompleteRequest{
		Messages: []AIMessage{{Role: "user", Content: "third"}},
	}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("3rd call in minute: got %v, want ErrRateLimited", err)
	}

	// 6. Client/viewer request paths are refused even with valid grant; scheduled route allowed.
	env.m.mu.Lock()
	env.m.aiCompleteLimit = 20
	env.m.aiCompleteTimes[rec.ID] = nil
	env.m.mu.Unlock()
	for _, blocked := range []string{
		"/catalog/movie/all.json",
		"/stream/movie/tt0133093.json",
		"/meta/movie/tt0133093.json",
		"/ether/stream/open",
		"/ether/v1/frontend/plex/library",
		"/ether/view/home",
		"/ether/v1/kinds/games/resolve",
	} {
		if _, err := h.AIComplete(withCallPath(ctx, blocked), AICompleteRequest{
			Messages: []AIMessage{{Role: "user", Content: "hi"}},
		}); !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("path %s: got %v, want ErrPermissionDenied", blocked, err)
		}
	}
	for _, allowed := range []string{
		"/ether/action/tag",
		"/ether/job/backfill",
		"/ether/v1/flow-actions/enrich",
		"/ether/v1/kinds/games/browse",
		"/ether/refresh", // declared in manifest ether.schedule
	} {
		if _, err := h.AIComplete(withCallPath(ctx, allowed), AICompleteRequest{
			Messages: []AIMessage{{Role: "user", Content: "hi"}},
		}); err != nil {
			t.Fatalf("allowed path %s failed: %v", allowed, err)
		}
	}

	// 7. Live grant revocation blocks immediately.
	if err := env.m.SetGrants(ctx, rec.ID, Grants{Revoked: []string{"ai"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.AIComplete(actionCtx, AICompleteRequest{
		Messages: []AIMessage{{Role: "user", Content: "revoked"}},
	}); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("revoked ai grant: got %v, want ErrPermissionDenied", err)
	}
}

func TestHostedWasmAICompleteRoutePolicyAndNotConfigured(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	eng := wasm.NewEngine(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { _ = eng.Close() })
	env.m.RegisterRuntime("hosted", HostedRuntimeFactory(eng))

	configured := true
	env.m.deps.Backends.AIComplete = func(_ context.Context, req AICompleteBackendRequest) (string, error) {
		if !configured {
			return "", llm.ErrNotConfigured
		}
		return `{"mood":"noir"}`, nil
	}

	script := `
globalThis.handle = function(req) {
  try {
    var res = ether.ai.complete({
      messages: [{ role: "user", content: "Blade Runner" }],
      schema: {
        type: "object",
        required: ["mood"],
        properties: { mood: { type: "string" } }
      }
    });
    return { status: 200, body: JSON.stringify({ ok: true, mood: res.json.mood, text: res.text }) };
  } catch (e) {
    return { status: 200, body: JSON.stringify({ ok: false, code: e.code || "", message: String(e.message || e) }) };
  }
};
`
	prev, err := env.m.PreviewInstall(ctx, Source{
		Kind:     "studio",
		Manifest: aiManifestJSON("complete", nil),
		Files:    map[string][]byte{"dist/plugin.js": []byte(script)},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := env.m.Install(ctx, prev, InstallOptions{AcceptAddedPermissions: true})
	if err != nil {
		t.Fatal(err)
	}

	callJSON := func(path string) map[string]any {
		t.Helper()
		resp, err := env.m.Call(ctx, rec.ID, Request{Method: "POST", Path: path})
		if err != nil || resp.Status != 200 {
			t.Fatalf("%s: resp=%+v err=%v", path, resp, err)
		}
		var bodyStr string
		if err := json.Unmarshal(resp.Body, &bodyStr); err != nil {
			bodyStr = string(resp.Body)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(bodyStr), &out); err != nil {
			t.Fatalf("%s invalid body %q: %v", path, bodyStr, err)
		}
		return out
	}

	// Allowed action route returns validated JSON object.
	got := callJSON("/ether/action/classify")
	if got["ok"] != true || got["mood"] != "noir" {
		t.Fatalf("/ether/action/classify = %+v", got)
	}

	// Viewer/client route is refused with forbidden inside the hosted sandbox.
	got = callJSON("/catalog/movie/all.json")
	if got["ok"] != false || got["code"] != "forbidden" {
		t.Fatalf("/catalog/movie/all.json = %+v, want forbidden", got)
	}

	// Unconfigured language model surfaces clear not_configured code.
	configured = false
	got = callJSON("/ether/action/classify")
	if got["ok"] != false || got["code"] != "not_configured" {
		t.Fatalf("not configured = %+v, want not_configured", got)
	}
}
