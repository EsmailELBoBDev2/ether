package vela

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/frontend/frontendtest"
)

// Exercise the actual stream's connect and heartbeat path with no media or
// observation requests. The manager's idle/stop semantics have a fake-clock test.
func TestVelaEventsLiveness(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	calls := 0
	s := &playSession{scope: "fake-family", viewer: 1, device: "device", signal: make(chan struct{}), plan: object{"sessionId": "session"}}
	sessions := frontendtest.Sessions{KeepAliveFunc: func(c context.Context, a frontend.SessionAccess, id string) error {
		if c.Err() != nil || a.Viewer.ID != 1 || a.Device.ID != "device" || a.Protocol != "vela" || id != "session" {
			t.Fatal("lost authorization", a, id)
		}
		calls++
		if calls == 2 {
			s.mu.Lock()
			s.ended = time.Now()
			s.publish("ended")
			s.mu.Unlock()
		}
		return nil
	}}
	i := &instance{ctx: ctx, host: frontend.Host{Identity: identityFake{}, Sessions: sessions}, sessions: map[string]*playSession{"session": s}}
	r := httptest.NewRequest("GET", "/vela/v0/sessions/session/events", nil).WithContext(ctx)
	r.SetPathValue("id", "session")
	r.Header.Set("Authorization", "Bearer token")
	r.Header.Set("X-Vela-Device-Id", "device")
	w := httptest.NewRecorder()
	i.events(w, r)
	if calls != 2 {
		t.Fatalf("connect + heartbeat calls = %d, body %s", calls, w.Body.String())
	}
}
