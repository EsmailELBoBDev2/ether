package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/plugins/wasm"
)

func TestInstanceWaitDoesNotCancelJobs(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	rec := env.installHello(t, "1.0.0")

	jobEntered := make(chan struct{})
	releaseJob := make(chan struct{})
	env.fake.mu.Lock()
	env.fake.routes["/ether/job/work"] = func(Request) (Response, error) {
		close(jobEntered)
		<-releaseJob
		return Response{Status: 200, Body: json.RawMessage(`{"done":true}`)}, nil
	}
	env.fake.routes["/ether/v1/kinds/books/resolve"] = func(Request) (Response, error) {
		return Response{}, &wasm.Error{
			Status:  wasm.StatusTimeout,
			Message: wasm.ErrPoolBusy.Error(),
			Err:     wasm.ErrPoolBusy,
		}
	}
	env.fake.mu.Unlock()

	job, err := env.m.startResolverJob(rec.ID, json.RawMessage(`{"job":"/ether/job/work"}`), nil, "")
	if err != nil || job == nil {
		t.Fatalf("startResolverJob: job=%v err=%v", job, err)
	}
	<-jobEntered

	_, callErr := env.m.Call(ctx, rec.ID, Request{
		Method: "POST",
		Path:   "/ether/v1/kinds/books/resolve",
		Body:   json.RawMessage(`{"item":{"ref":"p:books:1"}}`),
	})
	if !errors.Is(callErr, ErrBusy) || !errors.Is(callErr, wasm.ErrPoolBusy) {
		t.Fatalf("Call err = %v, want ErrBusy wrapping wasm.ErrPoolBusy", callErr)
	}
	if state := job.snapshot().State; state != "running" {
		t.Fatalf("job state after pool wait = %q, want running", state)
	}

	gotRec, err := env.m.Get(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !gotRec.Enabled {
		t.Fatal("plugin was disabled after instance-wait timeout")
	}
	health, err := env.m.HealthOf(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if health.Status == "error" || health.Status == "disabled" || health.Restarts > 0 {
		t.Fatalf("instance-wait timeout recorded crash/failure: %+v", health)
	}

	close(releaseJob)
	<-job.done
	if state := job.snapshot().State; state != "done" {
		t.Fatalf("job final state = %q, want done (error=%q)", state, job.snapshot().Error)
	}

	// Verify poolSize manifest requests respect defaults, cap, and owner ceilings.
	base := Manifest{
		ID:      "community.test.pool",
		Version: "1.0.0",
		Ether:   &EtherBlock{Runtime: RuntimeDecl{Kind: "hosted", Entry: "index.js"}},
	}
	if lim := runtimeLimitsWithCeilings(base, defaultRuntimeCeilings()); lim.PoolSize != wasm.DefaultPoolSize {
		t.Fatalf("default PoolSize = %d, want %d", lim.PoolSize, wasm.DefaultPoolSize)
	}
	base.Ether.Runtime.Limits = &RuntimeLimits{PoolSize: 8}
	if lim := runtimeLimitsWithCeilings(base, defaultRuntimeCeilings()); lim.PoolSize != wasm.MaxPoolSize {
		t.Fatalf("capped PoolSize = %d, want %d", lim.PoolSize, wasm.MaxPoolSize)
	}
	customCeilings := applyRuntimeCeilings(defaultRuntimeCeilings(), config.PluginRuntimeLimits{PoolSize: 3})
	if lim := runtimeLimitsWithCeilings(base, customCeilings); lim.PoolSize != 3 {
		t.Fatalf("owner-capped PoolSize = %d, want 3", lim.PoolSize)
	}
}
