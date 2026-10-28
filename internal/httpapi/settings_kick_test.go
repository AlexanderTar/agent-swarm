package httpapi

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AlexanderTar/swarmrun/internal/runtime"
	"github.com/AlexanderTar/swarmrun/internal/settings"
)

// Saving a changed max_concurrent_agents must wake the reconcile loop, so queued agents start (or
// excess ones pause) at once instead of at the next 5 s tick (TASK-1325). The loop asks for a new
// timer once per iteration, so a second After call proves the kick ran a tick; the timer itself
// never fires.
func TestPutSettingsKicksReconcileWhenTheAgentLimitChanges(t *testing.T) {
	var waits atomic.Int32
	var rt *runtime.Store
	// The runtime tree's fake agent kind fails settings validation on PUT, so wire a bare runtime
	// into the plain env; its ticks have nothing to do.
	e := newEnv(t, func(d *Deps) {
		rt = &runtime.Store{DB: d.DB, Events: d.Events, Items: d.Items, Repos: d.Repos, Settings: d.Settings,
			Now: time.Now, Log: func(string, ...any) {}, Tmux: &testTmux{},
			After: func(time.Duration) <-chan time.Time { waits.Add(1); return make(chan time.Time) }}
		d.RT = rt
	})
	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() { rt.ReconcileLoop(ctx, time.Hour); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	waitFor := func(n int32) bool {
		for i := 0; i < 100; i++ {
			if waits.Load() >= n {
				return true
			}
			time.Sleep(20 * time.Millisecond)
		}
		return false
	}
	if !waitFor(1) {
		t.Fatal("reconcile loop never started")
	}

	_, b := e.api("GET", "/api/settings", nil)
	cur := decode[settings.Settings](t, b)

	// Unrelated edit: no kick.
	cur.LowTokenMode = !cur.LowTokenMode
	if status, body := e.api("PUT", "/api/settings", cur); status != 200 {
		t.Fatalf("PUT = %d: %s", status, body)
	}
	time.Sleep(200 * time.Millisecond)
	if n := waits.Load(); n != 1 {
		t.Fatalf("a save that kept max_concurrent_agents woke the loop (%d waits)", n)
	}

	cur.MaxConcurrentAgents += 3
	if status, body := e.api("PUT", "/api/settings", cur); status != 200 {
		t.Fatalf("PUT = %d: %s", status, body)
	}
	if !waitFor(2) {
		t.Fatal("raising max_concurrent_agents did not run a reconcile tick")
	}
}
