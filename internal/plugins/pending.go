package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const ResolverTotal = 25 * time.Second

// PendingReply defers a kind resolve or match without recording a failure.
// RetryInMS is clamped to 250..5000. Job is an optional /ether/job/ route;
// the host passes the original request body and drives job continuations.
type PendingReply struct {
	Pending   bool   `json:"pending"`
	RetryInMS int64  `json:"retryInMs,omitempty"`
	Job       string `json:"job,omitempty"`
}

// Call applies the total pending budget to kind resolvers and matchers.
func (m *Manager) Call(ctx context.Context, pluginID string, req Request) (Response, error) {
	if !strings.HasPrefix(req.Path, "/ether/v1/kinds/") || (!strings.HasSuffix(req.Path, "/resolve") && !strings.HasSuffix(req.Path, "/match")) {
		return m.callOnce(ctx, pluginID, req)
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, ResolverTotal)
	defer cancel()
	// One job per plugin/kind/item, even across simultaneous Plays. Include the
	// request body when older callers do not supply an item reference.
	var identity struct {
		Item struct {
			Ref        string `json:"ref"`
			ExternalID string `json:"id"`
		} `json:"item"`
	}
	_ = json.Unmarshal(req.Body, &identity)
	item := identity.Item.Ref
	if item == "" {
		item = identity.Item.ExternalID
	}
	if item == "" {
		item = string(req.Body)
	}
	suffix := fmt.Sprintf("|%x", sha256.Sum256([]byte(strings.TrimSuffix(strings.TrimSuffix(req.Path, "/resolve"), "/match")+"|"+item)))
	started := false
	var job *actionJob
	for {
		resp, err := m.callOnce(ctx, pluginID, req)
		if err != nil {
			if parent.Err() != nil {
				return Response{}, parent.Err()
			}
			if ctx.Err() != nil {
				return Response{Status: 202, Body: json.RawMessage(`{"pending":true}`)}, nil
			}
			return resp, err
		}
		var pending PendingReply
		if resp.Status >= 400 || json.Unmarshal(resp.Body, &pending) != nil || !pending.Pending {
			return resp, nil
		}
		if !started && strings.HasPrefix(pending.Job, "/ether/job/") {
			job, err = m.startResolverJob(pluginID, resp.Body, req.Body, suffix)
			if err != nil {
				return Response{Status: 202, Body: json.RawMessage(`{"pending":true}`)}, nil
			}
			started = true
		}
		var done <-chan struct{}
		if job != nil {
			done = job.done
		}
		timer := time.NewTimer(time.Duration(max(int64(250), min(int64(5000), pending.RetryInMS))) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			if parent.Err() != nil {
				return Response{}, parent.Err()
			}
			return Response{Status: 202, Body: json.RawMessage(`{"pending":true}`)}, nil
		case <-done:
			timer.Stop()
			job = nil
		case <-timer.C:
		}
	}
}
