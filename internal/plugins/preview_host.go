package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/braedonsaunders/ether/internal/safefetch"
)

// PreviewFetch performs a draft plugin's http.fetch for Studio's preview:
// the same SSRF-guarded client, redirect credential confinement, and host
// allowlist as an installed plugin, checked against the draft's own declared
// network permissions, so a preview shows the real data the plugin will see
// once installed.
func (m *Manager) PreviewFetch(ctx context.Context, network []string, req FetchRequest) (FetchResponse, error) {
	u, err := url.Parse(req.URL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return FetchResponse{}, fmt.Errorf("%w: bad url", ErrPermissionDenied)
	}
	if !hostAllowed(network, u.Hostname()) {
		return FetchResponse{}, fmt.Errorf("%w: %s is not in the plugin's network permissions", ErrPermissionDenied, u.Hostname())
	}
	redirectPatterns, err := validateKeepHeadersOnRedirect(Manifest{Ether: &EtherBlock{Permissions: Permissions{Network: network}}}, Grants{}, req.KeepHeadersOnRedirect)
	if err != nil {
		return FetchResponse{}, err
	}
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	hr, err := http.NewRequestWithContext(ctx, method, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		return FetchResponse{}, err
	}
	for k, v := range req.Headers {
		hr.Header.Set(k, v)
	}
	client := *m.client
	redirectHeaders := safefetch.NewRedirectHeaderState(u, safefetch.RedirectHeaderOptions{
		Patterns:       redirectPatterns,
		InitialHeaders: hr.Header,
	})
	previous := client.CheckRedirect
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		redirectHeaders.Apply(next)
		if !hostAllowed(network, next.URL.Hostname()) {
			return fmt.Errorf("%w: redirect to %s", ErrPermissionDenied, next.URL.Hostname())
		}
		if previous != nil {
			return previous(next, via)
		}
		if len(via) >= 10 {
			return errors.New("plugins: stopped after 10 redirects")
		}
		return nil
	}
	resp, err := client.Do(hr)
	if err != nil {
		return FetchResponse{}, fetchError(u.Hostname(), err)
	}
	defer resp.Body.Close() //nolint:errcheck
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxFetchBytes+1))
	if err != nil {
		return FetchResponse{}, fetchError(u.Hostname(), err)
	}
	if len(body) > MaxFetchBytes {
		return FetchResponse{}, fmt.Errorf("plugins: response larger than %d bytes", MaxFetchBytes)
	}
	headers := map[string]string{}
	for k := range resp.Header {
		headers[k] = resp.Header.Get(k)
	}
	return FetchResponse{Status: resp.StatusCode, Headers: headers, Body: body}, nil
}

// PreviewCatalogSearch and PreviewCatalogGet give a draft read access to the
// real catalogue, the way catalog: read does once installed. ok is false when
// the server has no catalogue backend (tests, early startup).
func (m *Manager) PreviewCatalogSearch(ctx context.Context, query string, limit, offset int) (json.RawMessage, bool, error) {
	if m.deps.Backends.CatalogSearch == nil {
		return nil, false, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	out, err := m.deps.Backends.CatalogSearch(ctx, query, limit, offset)
	return out, true, err
}

func (m *Manager) PreviewCatalogGet(ctx context.Context, id string) (json.RawMessage, bool, error) {
	if m.deps.Backends.CatalogGet == nil {
		return nil, false, nil
	}
	out, err := m.deps.Backends.CatalogGet(ctx, id)
	return out, true, err
}

// PreviewAIComplete runs a Studio draft's ai.complete call against the owner's
// configured language model with the same input validation, schema checks, rate
// limits, and secret-safe error mapping as an installed plugin.
func (m *Manager) PreviewAIComplete(ctx context.Context, projectKey string, req AICompleteRequest) (AICompleteResponse, error) {
	compiled, err := ValidateAICompleteRequest(req)
	if err != nil {
		return AICompleteResponse{}, err
	}
	backend := m.deps.Backends.AIComplete
	if backend == nil {
		return AICompleteResponse{}, ErrNotConfigured
	}
	rateKey := "preview:" + projectKey
	limit := m.aiCompletePerMin()
	if !m.rateOK(m.aiCompleteTimes, rateKey, limit, time.Minute) {
		return AICompleteResponse{}, fmt.Errorf("%w: ai.complete is limited to %d calls per minute", ErrRateLimited, limit)
	}
	resp, err := executeAIComplete(ctx, m.log, rateKey, backend, compiled)
	if errors.Is(err, ErrNotConfigured) {
		m.rateRefund(m.aiCompleteTimes, rateKey)
	}
	return resp, err
}
