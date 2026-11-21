package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/llm"
	"github.com/braedonsaunders/ether/internal/plugins/wasm"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Safe per-plugin bounds for Host.AIComplete (#669). No owner settings are
// exposed for these bounds; they protect the owner's configured language model
// and memory budget with predictable defaults.
const (
	// DefaultAICompletePerMin caps completions per plugin in a sliding minute.
	DefaultAICompletePerMin = 20
	// MaxAIMessages caps the chat message count in a single completion request.
	MaxAIMessages = 20
	// MaxAIInputBytes caps total UTF-8 message content bytes per request.
	MaxAIInputBytes = 32 * 1024
	// MaxAISchemaBytes caps the optional JSON Schema document size.
	MaxAISchemaBytes = 32 * 1024
	// DefaultAIMaxTokens is the reply token budget when maxTokens is omitted.
	DefaultAIMaxTokens = 1024
	// MaxAIMaxTokens is the hard ceiling on maxTokens per completion.
	MaxAIMaxTokens = 4096
	// DefaultAITimeout bounds one completion when timeoutMs is omitted.
	DefaultAITimeout = 30 * time.Second
	// MaxAITimeout is the hard ceiling on timeoutMs per completion.
	MaxAITimeout = 30 * time.Second
	// MaxAIResponseBytes caps the model reply text / validated JSON returned
	// to the plugin.
	MaxAIResponseBytes = 64 * 1024
)

type callPathKey struct{}

// withCallPath records the active plugin route path on ctx so host calls can
// enforce route-class policies (for example blocking ai.complete on client
// request paths).
func withCallPath(ctx context.Context, path string) context.Context {
	if path == "" {
		return ctx
	}
	return context.WithValue(ctx, callPathKey{}, path)
}

func callPathFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if p, _ := ctx.Value(callPathKey{}).(string); p != "" {
		return p
	}
	return wasm.CallPath(ctx)
}

// isClientRequestPath reports whether p is an interactive viewer, client
// protocol adapter, or console UI/lifecycle path that must never wait on a
// language model call.
func isClientRequestPath(p string) bool {
	return wasm.ViewerRequestPath(p) ||
		strings.HasPrefix(p, "/ether/stream/") ||
		strings.HasPrefix(p, "/ether/v1/frontend/") ||
		strings.HasPrefix(p, "/ether/view/") || p == "/ether/view" ||
		strings.HasPrefix(p, "/ether/options/") || p == "/ether/options" ||
		p == "/ether/health" ||
		p == "/ether/configure" ||
		strings.HasPrefix(p, "/ether/file/") ||
		strings.HasPrefix(p, "/ether/directory/")
}

// AICompleteAllowedPath reports whether a plugin route may call ai.complete.
// Interactive viewer/client request paths are always refused so playback,
// browsing, metadata, frontend adapters, and console views never wait on an
// LLM. Actions, jobs, Flow actions, tools, event deliveries, setup/test,
// background sync, and declared scheduled routes are allowed.
func AICompleteAllowedPath(man Manifest, path string, background bool) bool {
	p, _, _ := strings.Cut(strings.TrimSpace(path), "?")
	if p == "" {
		return false
	}
	if isClientRequestPath(p) {
		return false
	}
	if background {
		return true
	}
	switch {
	case strings.HasPrefix(p, "/ether/action/"),
		strings.HasPrefix(p, "/ether/job/"),
		strings.HasPrefix(p, "/ether/v1/flow-actions/"),
		p == "/ether/event",
		strings.HasPrefix(p, "/ether/tool/"),
		strings.HasPrefix(p, "/ether/v1/setup/"),
		p == "/ether/test",
		strings.HasPrefix(p, "/ether/feed/"),
		wasm.KindSyncPath(p),
		wasm.LiveTVSyncPath(p):
		return true
	}
	man.Ether = man.EffectiveEther()
	if man.Ether != nil {
		for _, s := range man.Ether.Schedule {
			callPath, _, _ := strings.Cut(strings.TrimSpace(s.Call), "?")
			if callPath == p {
				return true
			}
		}
		for _, fa := range man.Ether.Contributes.FlowActions {
			route, _, _ := strings.Cut(strings.TrimSpace(fa.Route), "?")
			if route == p {
				return true
			}
		}
	}
	return false
}

// denyExternalSchemaLoader refuses every external $ref load so plugin-supplied
// JSON Schemas can never trigger outbound network calls or local file reads.
type denyExternalSchemaLoader struct{}

func (denyExternalSchemaLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("external schema $ref is not allowed (%s)", url)
}

type compiledAIRequest struct {
	messages        []AIMessage
	maxTokens       int
	temperature     *float64
	timeout         time.Duration
	schema          *jsonschema.Schema
	canonicalSchema []byte
	jsonObjectMode  bool
}

// ValidateAICompleteRequest validates messages, size/token/temperature/timeout
// bounds, and compiles any optional JSON Schema. Exported for Studio and the
// conformance kit so offline checks enforce the same contract as the host.
func ValidateAICompleteRequest(req AICompleteRequest) (*compiledAIRequest, error) {
	var errs []FieldError
	if len(req.Messages) == 0 {
		errs = append(errs, FieldError{Field: "messages", Message: "at least one message is required"})
	} else if len(req.Messages) > MaxAIMessages {
		errs = append(errs, FieldError{Field: "messages", Message: fmt.Sprintf("at most %d messages are allowed", MaxAIMessages)})
	}

	var totalBytes int
	hasUser := false
	msgs := make([]AIMessage, 0, len(req.Messages))
	for i, m := range req.Messages {
		role := strings.TrimSpace(m.Role)
		switch role {
		case "system", "user", "assistant":
			if role == "user" {
				hasUser = true
			}
		default:
			errs = append(errs, FieldError{
				Field:   fmt.Sprintf("messages[%d].role", i),
				Message: "must be one of system, user, assistant",
			})
		}
		if strings.TrimSpace(m.Content) == "" {
			errs = append(errs, FieldError{
				Field:   fmt.Sprintf("messages[%d].content", i),
				Message: "must not be empty",
			})
		}
		totalBytes += len(m.Content)
		msgs = append(msgs, AIMessage{Role: role, Content: m.Content})
	}
	if len(req.Messages) > 0 && !hasUser {
		errs = append(errs, FieldError{Field: "messages", Message: "at least one user message is required"})
	}
	if totalBytes > MaxAIInputBytes {
		errs = append(errs, FieldError{Field: "messages", Message: fmt.Sprintf("total message content exceeds %d bytes", MaxAIInputBytes)})
	}

	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = DefaultAIMaxTokens
	} else if maxTokens < 0 || maxTokens > MaxAIMaxTokens {
		errs = append(errs, FieldError{Field: "maxTokens", Message: fmt.Sprintf("must be between 1 and %d", MaxAIMaxTokens)})
	}

	var temp *float64
	if req.Temperature != nil {
		t := *req.Temperature
		if math.IsNaN(t) || math.IsInf(t, 0) || t < 0 || t > 2 {
			errs = append(errs, FieldError{Field: "temperature", Message: "must be between 0 and 2"})
		} else {
			temp = &t
		}
	}

	timeout := DefaultAITimeout
	if req.TimeoutMS < 0 {
		errs = append(errs, FieldError{Field: "timeoutMs", Message: "must not be negative"})
	} else if req.TimeoutMS > 0 {
		timeout = min(time.Duration(req.TimeoutMS)*time.Millisecond, MaxAITimeout)
	}

	var compiled *jsonschema.Schema
	var canonicalSchema []byte
	var jsonObjectMode bool
	trimmedSchema := bytes.TrimSpace(req.Schema)
	if len(trimmedSchema) > 0 && !bytes.Equal(trimmedSchema, []byte("null")) {
		if len(trimmedSchema) > MaxAISchemaBytes {
			errs = append(errs, FieldError{Field: "schema", Message: fmt.Sprintf("exceeds %d bytes", MaxAISchemaBytes)})
		} else {
			doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(trimmedSchema))
			if err != nil {
				errs = append(errs, FieldError{Field: "schema", Message: "must be valid JSON"})
			} else if obj, ok := doc.(map[string]any); !ok {
				errs = append(errs, FieldError{Field: "schema", Message: "must be a JSON Schema object"})
			} else {
				compiler := jsonschema.NewCompiler()
				compiler.UseLoader(denyExternalSchemaLoader{})
				const schemaURI = "urn:ether:plugin:ai:schema.json"
				if err := compiler.AddResource(schemaURI, doc); err != nil {
					errs = append(errs, FieldError{Field: "schema", Message: "invalid JSON Schema: " + err.Error()})
				} else if s, err := compiler.Compile(schemaURI); err != nil {
					errs = append(errs, FieldError{Field: "schema", Message: "invalid JSON Schema: " + err.Error()})
				} else {
					compiled = s
					canonicalSchema, _ = json.Marshal(doc)
					jsonObjectMode = schemaExpectsJSONObject(obj)
				}
			}
		}
	}

	if len(errs) > 0 {
		return nil, &ValidationError{Fields: errs}
	}
	return &compiledAIRequest{
		messages:        msgs,
		maxTokens:       maxTokens,
		temperature:     temp,
		timeout:         timeout,
		schema:          compiled,
		canonicalSchema: canonicalSchema,
		jsonObjectMode:  jsonObjectMode,
	}, nil
}

func schemaExpectsJSONObject(doc map[string]any) bool {
	if rawType, ok := doc["type"]; ok {
		s, _ := rawType.(string)
		return s == "object"
	}
	_, hasProps := doc["properties"]
	return hasProps
}

func (m *Manager) aiCompletePerMin() int {
	m.mu.Lock()
	limit := m.aiCompleteLimit
	m.mu.Unlock()
	if limit <= 0 {
		return DefaultAICompletePerMin
	}
	return limit
}

// executeAIComplete runs a validated completion against backend, keeping
// provider credentials, endpoints, and raw upstream errors out of plugin-visible
// results, and validating structured output against the compiled JSON Schema.
func executeAIComplete(
	ctx context.Context,
	log *slog.Logger,
	pluginID string,
	backend func(context.Context, AICompleteBackendRequest) (string, error),
	compiled *compiledAIRequest,
) (AICompleteResponse, error) {
	if backend == nil {
		return AICompleteResponse{}, ErrNotConfigured
	}

	msgs := make([]AIMessage, 0, len(compiled.messages)+1)
	if compiled.schema != nil {
		msgs = append(msgs, AIMessage{
			Role: "system",
			Content: "Respond with valid JSON only, with no markdown fences or extra text. " +
				"The JSON value must conform to this JSON Schema:\n" + string(compiled.canonicalSchema),
		})
	}
	msgs = append(msgs, compiled.messages...)

	callCtx, cancel := context.WithTimeout(ctx, compiled.timeout)
	defer cancel()

	rawText, err := backend(callCtx, AICompleteBackendRequest{
		Messages:    msgs,
		JSONMode:    compiled.jsonObjectMode,
		MaxTokens:   compiled.maxTokens,
		Temperature: compiled.temperature,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrNotConfigured) || errors.Is(err, llm.ErrNotConfigured):
			return AICompleteResponse{}, ErrNotConfigured
		case errors.Is(err, context.DeadlineExceeded) || errors.Is(callCtx.Err(), context.DeadlineExceeded):
			return AICompleteResponse{}, context.DeadlineExceeded
		case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
			return AICompleteResponse{}, context.Canceled
		default:
			if log != nil {
				log.Warn("plugin ai.complete failed", "plugin", pluginID, "err", err)
			}
			// Never return raw provider errors, URLs, or upstream response bodies
			// to the plugin: they may contain owner endpoint info or key hints.
			return AICompleteResponse{}, fmt.Errorf("%w: language model request failed", ErrUnavailable)
		}
	}

	text := llm.StripReasoning(rawText)
	if len(text) > MaxAIResponseBytes {
		return AICompleteResponse{}, fmt.Errorf("%w: language model response exceeded %d bytes", ErrInvalidOutput, MaxAIResponseBytes)
	}

	if compiled.schema == nil {
		return AICompleteResponse{Text: text}, nil
	}

	candidate := strings.TrimSpace(text)
	if candidate == "" || !json.Valid([]byte(candidate)) {
		candidate = strings.TrimSpace(stripCodeFence(candidate))
	}
	if candidate == "" || !json.Valid([]byte(candidate)) {
		candidate = strings.TrimSpace(llm.ExtractJSON(text))
	}
	if candidate == "" || !json.Valid([]byte(candidate)) {
		return AICompleteResponse{}, fmt.Errorf("%w: language model did not return valid JSON", ErrInvalidOutput)
	}

	val, err := jsonschema.UnmarshalJSON(strings.NewReader(candidate))
	if err != nil {
		return AICompleteResponse{}, fmt.Errorf("%w: language model did not return valid JSON", ErrInvalidOutput)
	}
	if err := compiled.schema.Validate(val); err != nil {
		return AICompleteResponse{}, fmt.Errorf("%w: language model output did not match schema", ErrInvalidOutput)
	}
	canonical, err := json.Marshal(val)
	if err != nil || len(canonical) > MaxAIResponseBytes {
		return AICompleteResponse{}, fmt.Errorf("%w: language model response exceeded %d bytes", ErrInvalidOutput, MaxAIResponseBytes)
	}
	return AICompleteResponse{
		Text: string(canonical),
		JSON: json.RawMessage(canonical),
	}, nil
}

func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	rest := s[3:]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[nl+1:]
	}
	if end := strings.LastIndex(rest, "```"); end >= 0 {
		return rest[:end]
	}
	return s
}

func (m *Manager) rateRefund(bucket map[string][]time.Time, key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if times := bucket[key]; len(times) > 0 {
		bucket[key] = times[:len(times)-1]
	}
}

// AIComplete runs one bounded completion on the owner's configured language
// model. Requires permissions.ai: "complete" (checked live against grants),
// refuses interactive viewer/client request paths, enforces input/schema/rate
// bounds, and never leaks provider secrets or raw upstream errors.
func (h *host) AIComplete(ctx context.Context, req AICompleteRequest) (AICompleteResponse, error) {
	r, g, err := h.record(ctx)
	if err != nil {
		return AICompleteResponse{}, err
	}
	level := normLevel(r.Manifest.Ether.Permissions.AI)
	if err := h.deny(r, g, "ai", level, func(l string) bool { return l == "complete" }); err != nil {
		return AICompleteResponse{}, err
	}
	if path := callPathFromContext(ctx); path != "" {
		if !AICompleteAllowedPath(r.Manifest, path, isBackgroundCall(ctx) || wasm.IsBackgroundCall(ctx)) {
			h.noteDenied("ai_client_path")
			return AICompleteResponse{}, fmt.Errorf("%w: ai.complete cannot run on viewer or client request path %s", ErrPermissionDenied, path)
		}
	}

	compiled, err := ValidateAICompleteRequest(req)
	if err != nil {
		return AICompleteResponse{}, err
	}

	backend := h.m.deps.Backends.AIComplete
	if backend == nil {
		return AICompleteResponse{}, ErrNotConfigured
	}

	limit := h.m.aiCompletePerMin()
	if !h.m.rateOK(h.m.aiCompleteTimes, h.plugin, limit, time.Minute) {
		return AICompleteResponse{}, fmt.Errorf("%w: ai.complete is limited to %d calls per minute", ErrRateLimited, limit)
	}

	resp, err := executeAIComplete(ctx, h.m.log, h.plugin, backend, compiled)
	if errors.Is(err, ErrNotConfigured) {
		h.m.rateRefund(h.m.aiCompleteTimes, h.plugin)
	}
	return resp, err
}
