package sessions

import (
	"errors"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/transcode"
)

func capacityError(err error) error {
	if errors.Is(err, transcode.ErrTooManyTranscodes) {
		return &frontend.TranscodeCapacityError{RetryAfter: 5 * time.Second, Cause: err}
	}
	return err
}

func (b *Broker) recordEncoderFailure(playID string, err error) error {
	if msg := frontend.VideoEncoderCapacityMessage(err); msg != "" {
		if recorder, ok := b.deps.Playback.(interface{ NoteFailedStart(string, string) }); ok {
			var capacity *transcode.EncoderCapacityError
			if errors.As(err, &capacity) && len(capacity.Viewers) > 0 {
				msg += " (" + strings.Join(capacity.Viewers, ", ") + ")"
			}
			recorder.NoteFailedStart(playID, msg)
		}
	}
	return capacityError(err)
}
