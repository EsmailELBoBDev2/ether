package frontend

import (
	"context"
	"errors"
	"time"
)

// MaxMultiviewTiles is the maximum number of simultaneous live tiles in one
// multiview group (tile indices 0..MaxMultiviewTiles-1).
const MaxMultiviewTiles = 4

// ErrInvalidMultiview reports a malformed or out-of-range multiview request.
var ErrInvalidMultiview = errors.New("invalid multiview request")

// MultiviewTarget binds a live playback request to a tile within an active
// multiview group owned by the same viewer, protocol and device.
type MultiviewTarget struct {
	GroupID   string
	Tile      int
	TileCount int // optional declared layout tile count (1..MaxMultiviewTiles)
	MaxHeight int // optional client-declared tile height ceiling in pixels
	Focused   bool
}

// MultiviewGroup describes a created multiview group.
type MultiviewGroup struct {
	GroupID   string
	MaxTiles  int
	ExpiresAt time.Time
}

// MultiviewSessions is implemented by session brokers that manage per-device
// multiview groups of concurrent live playback tiles.
type MultiviewSessions interface {
	MultiviewAvailable() bool
	CreateMultiviewGroup(ctx context.Context, access SessionAccess) (MultiviewGroup, error)
	DeleteMultiviewGroup(ctx context.Context, access SessionAccess, groupID string) error
}
