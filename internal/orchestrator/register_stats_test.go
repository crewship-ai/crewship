package orchestrator

import (
	"sync"
	"testing"
)

// TestRegisterStatsContainer covers the public hook that chatbridge
// invokes after EnsureCrewRuntime so the stats poller picks up
// chat-driven containers (the case that previously left Crow's Nest's
// Resources panel permanently empty for the dominant code path).
func TestRegisterStatsContainer(t *testing.T) {
	t.Run("no-op when callback unwired", func(t *testing.T) {
		o := &Orchestrator{}
		// Should not panic — nil statsRegister is the test/dry-run case.
		o.RegisterStatsContainer("ctr-1", "crew-1", "ws-1")
	})

	t.Run("fires when callback set", func(t *testing.T) {
		var (
			mu       sync.Mutex
			gotCID   string
			gotCrew  string
			gotWs    string
			callsRcv int
		)
		o := &Orchestrator{}
		o.SetStatsRegisterCallback(func(cid, crew, ws string) {
			mu.Lock()
			defer mu.Unlock()
			gotCID, gotCrew, gotWs = cid, crew, ws
			callsRcv++
		})
		o.RegisterStatsContainer("ctr-1", "crew-1", "ws-1")
		mu.Lock()
		defer mu.Unlock()
		if callsRcv != 1 {
			t.Fatalf("calls = %d, want 1", callsRcv)
		}
		if gotCID != "ctr-1" || gotCrew != "crew-1" || gotWs != "ws-1" {
			t.Errorf("got args (%q,%q,%q); want (ctr-1, crew-1, ws-1)", gotCID, gotCrew, gotWs)
		}
	})

	t.Run("skips empty containerID", func(t *testing.T) {
		var calls int
		o := &Orchestrator{}
		o.SetStatsRegisterCallback(func(_, _, _ string) { calls++ })
		o.RegisterStatsContainer("", "crew-1", "ws-1")
		if calls != 0 {
			t.Errorf("calls = %d, want 0 (empty containerID must short-circuit)", calls)
		}
	})

	t.Run("skips empty workspaceID", func(t *testing.T) {
		// Empty workspace ID is the bug guard from the StatsCollector
		// emit path — without a workspace the journal Validate call
		// would error and the callback silently fails. Better to skip.
		var calls int
		o := &Orchestrator{}
		o.SetStatsRegisterCallback(func(_, _, _ string) { calls++ })
		o.RegisterStatsContainer("ctr-1", "crew-1", "")
		if calls != 0 {
			t.Errorf("calls = %d, want 0 (empty workspaceID must short-circuit)", calls)
		}
	})
}
