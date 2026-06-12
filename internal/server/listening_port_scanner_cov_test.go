package server

// Coverage tests for runListeningPortScanner itself (the loop body was
// previously only exercised via an inline mirror of its diff logic).
// The scan ticker is a 15s const, so the cycle-driving test runs with
// t.Parallel() and waits out exactly one tick; it overlaps the rest of
// the suite instead of extending it.

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/provider"
)

// covScanProvider answers Exec per container ID: one healthy payload,
// one permanently-gone error, one transient error.
type covScanProvider struct {
	mockContainer
}

func (p *covScanProvider) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	switch cfg.ContainerID {
	case "cov-ok-ctr":
		// Port 8000 (0x1F40) LISTEN.
		return &provider.ExecResult{
			ExecID: "e",
			Reader: io.NopCloser(strings.NewReader("  0: 0100007F:1F40 00000000:0000 0A\n")),
		}, nil
	case "cov-gone-ctr":
		return nil, errors.New("Error response from daemon: No such container: cov-gone-ctr")
	default:
		return nil, errors.New("transient daemon hiccup")
	}
}

// covSyncEmitter is a mutex-guarded journal.Emitter — the scanner emits
// from its own goroutine while the test polls.
type covSyncEmitter struct {
	mu      sync.Mutex
	entries []journal.Entry
}

func (r *covSyncEmitter) Emit(_ context.Context, e journal.Entry) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
	return "cov-" + e.Summary, nil
}

func (r *covSyncEmitter) Flush(_ context.Context) error { return nil }

func (r *covSyncEmitter) snapshot() []journal.Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]journal.Entry, len(r.entries))
	copy(out, r.entries)
	return out
}

func TestRunListeningPortScanner_NilDepsAndCancelledCtx(t *testing.T) {
	t.Parallel()
	// Nil dependencies return synchronously — guard for headless boots.
	runListeningPortScanner(context.Background(), nil, nil, nil, nil)

	// With deps but a pre-cancelled context the loop must exit before
	// the first 15s tick (and tolerate a nil logger).
	stub := &covScanProvider{}
	sc := NewStatsCollector(stub, nil, nil, time.Hour)
	em := &covSyncEmitter{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		runListeningPortScanner(ctx, stub, sc, em, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scanner did not exit on cancelled context")
	}
	if got := len(em.snapshot()); got != 0 {
		t.Errorf("entries = %d, want 0 before the first tick", got)
	}
}

// TestRunListeningPortScanner_FirstCycle waits out one real 15s tick and
// asserts the three per-container branches of the loop body:
//
//   - healthy container → network.port_opened for its LISTEN port
//   - permanently-gone container → unregistered from the stats collector
//   - transiently-failing container → no events, still tracked
//   - tracked row with empty workspace → skipped entirely
func TestRunListeningPortScanner_FirstCycle(t *testing.T) {
	t.Parallel()

	stub := &covScanProvider{}
	sc := NewStatsCollector(stub, nil, nil, time.Hour)
	sc.Register("cov-ok-ctr", "crew-ok", "ws-cov")
	sc.Register("cov-gone-ctr", "crew-gone", "ws-cov")
	sc.Register("cov-flaky-ctr", "crew-flaky", "ws-cov")
	sc.Register("cov-noscope-ctr", "crew-x", "") // empty workspace → skipped

	em := &covSyncEmitter{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runListeningPortScanner(ctx, stub, sc, em, krLogger())
		close(done)
	}()

	goneDropped := func() bool {
		for _, tc := range sc.Tracked() {
			if tc.ContainerID == "cov-gone-ctr" {
				return false
			}
		}
		return true
	}

	deadline := time.Now().Add(25 * time.Second) // one 15s tick + slack
	for time.Now().Before(deadline) {
		if len(em.snapshot()) > 0 && goneDropped() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("scanner did not exit after cancel")
	}

	entries := em.snapshot()
	if len(entries) != 1 {
		t.Fatalf("entries = %d (%+v), want exactly 1 port_opened", len(entries), entries)
	}
	e := entries[0]
	if e.Type != journal.EntryNetworkPortOpen {
		t.Errorf("entry type = %s, want %s", e.Type, journal.EntryNetworkPortOpen)
	}
	if e.WorkspaceID != "ws-cov" || e.CrewID != "crew-ok" {
		t.Errorf("entry scope = %s/%s, want ws-cov/crew-ok", e.WorkspaceID, e.CrewID)
	}
	if got := e.Payload["port"]; got != 8000 {
		t.Errorf("payload.port = %v, want 8000", got)
	}
	if got := e.Payload["container_id"]; got != "cov-ok-ctr" {
		t.Errorf("payload.container_id = %v, want cov-ok-ctr", got)
	}

	if !goneDropped() {
		t.Error("cov-gone-ctr still tracked — containerGone branch did not unregister it")
	}
	// The transient failure must keep the container tracked for the
	// next cycle instead of dropping it.
	flakyTracked := false
	for _, tc := range sc.Tracked() {
		if tc.ContainerID == "cov-flaky-ctr" {
			flakyTracked = true
		}
	}
	if !flakyTracked {
		t.Error("cov-flaky-ctr was dropped — transient errors must not unregister")
	}
}
