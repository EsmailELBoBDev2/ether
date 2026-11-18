package vela

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	"github.com/braedonsaunders/ether/internal/frontend"
)

// Cursors are issued per five-minute bucket and last at least ten minutes
// past the end of it. Within a bucket the same page always gets the same
// cursor, so a page answered 304 still holds a usable cursor: its ETag
// carries the bucket and changes before the cursor can expire.
const (
	liveCursorBucket = 5 * time.Minute
	liveCursorLife   = 10 * time.Minute
)

func liveCursorBucketStart(now time.Time) time.Time { return now.Truncate(liveCursorBucket) }

type liveCursor struct {
	Scope, Query, Snapshot string
	Offset                 int
	Expires                int64
}

func (i *instance) livePageScope(r *http.Request, v *frontend.Viewer, token string) (string, error) {
	family, err := i.grantScope(r.Context(), token, requestDevice(r))
	if err != nil {
		return "", err
	}
	return hashJSON([]any{i.cursorScope(r, v), family}), nil
}

func (i *instance) readLiveCursor(r *http.Request, scope, query string) (liveCursor, error) {
	c := liveCursor{Scope: scope, Query: query, Expires: liveCursorBucketStart(time.Now()).Add(liveCursorBucket + liveCursorLife).Unix()}
	token := r.URL.Query().Get("cursor")
	if token == "" {
		return c, nil
	}
	if len(token) > 4096 {
		return c, frontend.ErrLiveGuideStale
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(b) < i.seal.NonceSize() {
		return c, frontend.ErrLiveGuideStale
	}
	b, err = i.seal.Open(nil, b[:i.seal.NonceSize()], b[i.seal.NonceSize():], []byte("vela-live"))
	if err != nil || json.Unmarshal(b, &c) != nil || c.Scope != scope || c.Query != query || c.Expires <= time.Now().Unix() || c.Offset <= 0 || c.Snapshot == "" {
		return c, frontend.ErrLiveGuideStale
	}
	return c, nil
}

func (i *instance) nextLiveCursor(c liveCursor, offset int, snapshot string) (string, error) {
	c.Offset, c.Snapshot = offset, snapshot
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	// A nonce derived from the plaintext: the same cursor seals to the same
	// token, and a nonce only ever seals the plaintext it came from.
	sum := sha256.Sum256(append([]byte("vela-live-cursor\x00"), b...))
	nonce := append([]byte(nil), sum[:i.seal.NonceSize()]...)
	return base64.RawURLEncoding.EncodeToString(i.seal.Seal(nonce, nonce, b, []byte("vela-live"))), nil
}
