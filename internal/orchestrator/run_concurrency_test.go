package orchestrator

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// These tests lock finding P1 (HIGH, perf/DoS) from the 2026-06 security audit
// (.claude/context/SECURITY-AUDIT-2026-06.md): RunAgent
// (internal/orchestrator/orchestrator_run.go) must bound concurrent agent-run
// exec fan-outs with a runSem. Before the fix its only admission control was
// the `accepting` drain bool — once past that, every caller fanned out
// ~12-18 container.Exec calls (mkdir, manifest, memory dirs, credential files,
// claude config, MCP config, tmux setup, plus the heavy agent CLI exec), so N
// concurrent runs meant N simultaneous heavy execs against a single Docker
// daemon — a trivial OOM/CPU-starvation vector. The lighter git-diff path
// already had a semaphore (internal/server/routes_container.go); the
// ~15×-heavier run path now has its equivalent (o.runSem, capacity
// o.runSemCap, configurable via CREWSHIP_MAX_CONCURRENT_RUNS).
//
// The tests drive many concurrent RunAgent calls against the probe provider and
// assert the peak number of simultaneous heavy execs never exceeds the cap. If
// the runSem is removed or bypassed, peak climbs to N and the assertion fails.

// concProbeContainer is a provider.ContainerProvider that blocks on the heavy
// agent CLI exec (the tmux-wrapped `claude ...` call) and records the peak
// number of those execs in flight simultaneously. Setup execs return instantly
// as no-ops; only the agent exec participates in the concurrency measurement,
// because that is the long-lived, resource-heavy call a runSem is meant to gate.
type concProbeContainer struct {
	inFlight int32
	peak     int32

	arrived chan struct{} // one send per agent-exec entry
	release chan struct{} // closed by the test to unblock all parked execs
}

func (c *concProbeContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	joined := strings.Join(cfg.Cmd, " ")
	// The agent CLI exec is wrapped as `sh -c "tmux new-session ... agent-<slug> ..."`
	// (see setupTmuxExec); the same signature is used to spot it in
	// orchestrator_test.go's mock. Setup execs never contain "tmux new-session".
	isAgentExec := strings.Contains(joined, "tmux new-session") && strings.Contains(joined, "agent-")
	if !isAgentExec {
		return &provider.ExecResult{ExecID: "noop", Reader: io.NopCloser(strings.NewReader(""))}, nil
	}

	// Heavy exec: count it in, record the running peak, announce arrival, then
	// park until the test releases everyone. With no runSem, every concurrent
	// RunAgent reaches this point at once, so peak == N.
	cur := atomic.AddInt32(&c.inFlight, 1)
	for {
		p := atomic.LoadInt32(&c.peak)
		if cur <= p || atomic.CompareAndSwapInt32(&c.peak, p, cur) {
			break
		}
	}
	c.arrived <- struct{}{}
	<-c.release
	atomic.AddInt32(&c.inFlight, -1)
	return &provider.ExecResult{ExecID: "agent-exec", Reader: io.NopCloser(strings.NewReader("done\n"))}, nil
}

// ExecInspect: running=false, exit=0 → tmux "installed" (for the setupTmuxExec
// probe) and "agent completed" (for the post-run inspect). One return serves
// both because both want a clean, finished exec.
func (c *concProbeContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, 0, nil
}

func (c *concProbeContainer) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	return "container-x", nil
}
func (c *concProbeContainer) StopCrewRuntime(_ context.Context, _ string) error   { return nil }
func (c *concProbeContainer) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (c *concProbeContainer) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{State: "running"}, nil
}
func (c *concProbeContainer) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (c *concProbeContainer) CrewContainerName(_ string, slug string) string {
	return "crewship-team-" + slug
}
func (c *concProbeContainer) CopyToContainer(_ context.Context, _ string, _ string, _ io.Reader) error {
	return nil
}

// lockedMemState wraps the package's in-memory state mock with a mutex so the
// concurrent RunAgent goroutines below don't race on the shared map (the bare
// memState in orchestrator_test.go is single-goroutine only).
type lockedMemState struct {
	mu sync.Mutex
	s  *memState
}

func newLockedMemState() *lockedMemState { return &lockedMemState{s: newMemState()} }

func (l *lockedMemState) Get(ctx context.Context, bucket, key string) ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.s.Get(ctx, bucket, key)
}
func (l *lockedMemState) Set(ctx context.Context, bucket, key string, value []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.s.Set(ctx, bucket, key, value)
}
func (l *lockedMemState) Delete(ctx context.Context, bucket, key string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.s.Delete(ctx, bucket, key)
}
func (l *lockedMemState) List(ctx context.Context, bucket string) (map[string][]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.s.List(ctx, bucket)
}
func (l *lockedMemState) ListByPrefix(ctx context.Context, bucket, prefix string) (map[string][]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.s.ListByPrefix(ctx, bucket, prefix)
}
func (l *lockedMemState) Close() error { return l.s.Close() }

// runConcurrencyProbe launches n concurrent RunAgent calls against a fresh
// probe provider, waits until `cap` of them have parked at the heavy agent
// exec, records the peak in-flight count, then releases everyone. It returns
// the observed peak and the orchestrator's configured cap. Excess runs (n>cap)
// block on the runSem and never reach the heavy exec, so exactly `cap` arrivals
// are expected.
func runConcurrencyProbe(t *testing.T, n int) (peak int, cap int) {
	t.Helper()
	probe := &concProbeContainer{
		arrived: make(chan struct{}, n),
		release: make(chan struct{}),
	}
	o := New(probe, newLockedMemState(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	cap = o.runSemCap
	if cap < 1 {
		t.Fatalf("runSemCap must be positive, got %d", cap)
	}
	if n <= cap {
		t.Fatalf("test misconfigured: n=%d must exceed cap=%d to exercise the bound", n, cap)
	}

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = o.RunAgent(context.Background(), AgentRunRequest{
				AgentID:     fmt.Sprintf("a%d", i),
				AgentSlug:   fmt.Sprintf("agent-%d", i),
				ChatID:      fmt.Sprintf("s%d", i),
				ContainerID: fmt.Sprintf("c%d", i),
				CLIAdapter:  "CLAUDE_CODE",
				UserMessage: "go",
				TimeoutSecs: 30,
			}, nil)
		}(i)
	}

	// Exactly `cap` runs can hold a runSem token at once, so only `cap` reach
	// the heavy exec and park; the rest block on the semaphore. Wait for those
	// `cap` arrivals (the probe records peak before signalling arrival, so once
	// we've seen `cap` the peak is settled). A deadline guards against a
	// regression that wedges fewer than `cap` runs.
	deadline := time.After(15 * time.Second)
	got := 0
	for got < cap {
		select {
		case <-probe.arrived:
			got++
		case <-deadline:
			peak = int(atomic.LoadInt32(&probe.peak))
			close(probe.release)
			wg.Wait()
			t.Fatalf("only %d/%d runs reached the heavy exec within the deadline (peak=%d) — runSem may be over-restricting", got, cap, peak)
		}
	}

	peak = int(atomic.LoadInt32(&probe.peak))
	close(probe.release) // let every parked exec (and every sem-blocked run) finish
	wg.Wait()
	return peak, cap
}

// TestRunAgent_ConcurrencyCapped is the flipped P1 tripwire: with N >> cap
// concurrent runs, the peak number of simultaneous heavy execs must equal the
// runSem capacity and never exceed it. If the runSem is removed/bypassed, peak
// climbs toward N and this fails.
func TestRunAgent_ConcurrencyCapped(t *testing.T) {
	// Pick N comfortably above the configured cap so excess runs must block on
	// the semaphore regardless of any CREWSHIP_MAX_CONCURRENT_RUNS override.
	n := resolveRunSemCap()*3 + 4

	peak, cap := runConcurrencyProbe(t, n)
	if peak > cap {
		t.Fatalf("runSem breached: peak in-flight heavy execs=%d exceeds cap=%d (N=%d) — the agent-run path is not bounded", peak, cap, n)
	}
	if peak != cap {
		t.Fatalf("expected peak in-flight to saturate the cap: peak=%d, cap=%d, N=%d", peak, cap, n)
	}
}

// TestRunAgent_ConcurrencyCap_SecureTarget is the regression guard at higher
// load: a large burst of runs must still be bounded by the runSem.
func TestRunAgent_ConcurrencyCap_SecureTarget(t *testing.T) {
	peak, cap := runConcurrencyProbe(t, 200)
	if peak > cap {
		t.Fatalf("runSem breached: peak in-flight=%d exceeds cap=%d", peak, cap)
	}
	if peak != cap {
		t.Fatalf("expected peak in-flight to saturate the cap: peak=%d, cap=%d", peak, cap)
	}
}
