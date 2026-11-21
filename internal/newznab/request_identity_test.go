package newznab

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
)

func TestPreviewMasksAPIKeyAndSecrets(t *testing.T) {
	const (
		indexerKey = "super-secret-indexer-api-key-999"
		overrideK  = "override-secret-key-888"
		authToken  = "Bearer secret-auth-token-777"
	)
	saved := []config.Indexer{{
		ID:     "idx1",
		Name:   "Geek",
		URL:    "https://api.nzbgeek.info",
		APIKey: indexerKey,
	}}
	rule := config.IdentityRule{
		ID:        "r1",
		Target:    config.TargetIndexer,
		IndexerID: "idx1",
		Profile:   "sonarr",
		Headers: []config.KVOverride{
			{Name: "Authorization", Value: authToken},
			{Name: "X-Custom", Value: "visible"},
		},
		Params: []config.KVOverride{
			{Name: "apikey", Value: overrideK},
		},
	}

	p, err := PreviewRequestIdentity(PreviewInput{
		Rule:          &rule,
		Rules:         []config.IdentityRule{rule},
		HasRules:      true,
		SavedIndexers: saved,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	wire := string(raw)
	for _, secret := range []string{indexerKey, overrideK, authToken} {
		if strings.Contains(wire, secret) {
			t.Fatalf("preview leaked secret %q:\n%s", secret, wire)
		}
	}
	if !strings.Contains(p.Search.RequestLine, "apikey=••••") {
		t.Fatalf("search preview missing masked apikey: %s", p.Search.RequestLine)
	}
	if !strings.Contains(p.Download.RequestLine, "apikey=••••") {
		t.Fatalf("download preview missing masked apikey: %s", p.Download.RequestLine)
	}
	if strings.Contains(p.Search.RequestLine, "offset=0") || strings.Contains(p.Search.RequestLine, "season=00") {
		t.Fatalf("preview still has query mimicry: %s", p.Search.RequestLine)
	}
	for _, h := range p.Search.Headers {
		if strings.EqualFold(h.Name, "traceparent") {
			t.Fatalf("preview still includes traceparent: %+v", p.Search.Headers)
		}
		if strings.EqualFold(h.Name, "Authorization") && h.Value != "••••" {
			t.Fatalf("Authorization not masked: %+v", h)
		}
	}

	// Previewing a direct indexer draft with a real API key must never emit it either.
	ix := saved[0]
	p2, err := PreviewRequestIdentity(PreviewInput{
		Indexer:       &ix,
		SavedIndexers: saved,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw2, _ := json.Marshal(p2)
	if strings.Contains(string(raw2), indexerKey) {
		t.Fatalf("indexer preview leaked api key: %s", raw2)
	}
}
