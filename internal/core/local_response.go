package core

import "context"

type localResponseKey struct{}

// WithLocalResponse requests an immediate answer from local snapshots and
// caches. Optional enrichment may refresh behind the response.
func WithLocalResponse(ctx context.Context) context.Context {
	return context.WithValue(ctx, localResponseKey{}, true)
}

// LocalResponse reports whether this read must avoid enrichment waits.
func LocalResponse(ctx context.Context) bool {
	v, _ := ctx.Value(localResponseKey{}).(bool)
	return v
}

// WithColdDetail permits one bounded provider payload for this exact page's
// missing catalogue record. Nested list rows and unrelated recommendations
// retain the ordinary local-response policy.
func WithColdDetail(ctx context.Context, key RatingKey) context.Context {
	return context.WithValue(ctx, coldDetailKey{}, key)
}

type coldDetailKey struct{}

// ColdDetail reports whether key is the exact page allowed a cold fill.
func ColdDetail(ctx context.Context, key RatingKey) bool {
	requested, ok := ctx.Value(coldDetailKey{}).(RatingKey)
	return ok && requested == key
}
