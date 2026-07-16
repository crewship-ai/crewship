package orchestrator

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// #1220 — the check→decide→pkill→start sequence had no mutual exclusion
// across execs sharing one crew container.
//
// The fake below is stateful, which is what makes the race observable: it
// reports "no sidecar running" until a startSidecar exec lands, then flips
// to a healthy free-mode reply. That is exactly what the real container
// does, and it's the premise the production comment already relies on —
// "multiple agents in the same crew share one container, only the first
// starts the sidecar". Without the gate that premise is false under
// concurrency: N agents all sample the empty health before any of them
// starts, so all N start.
type covStatefulSidecarContainer struct {
	covContainer
	started  atomic.Int32
	healthMu sync.RWMutex
	healthy  bool
	// startDelay widens the check→start window so the race is reliably
	// hit rather than depending on scheduler luck.
	startDelay time.Duration
}

func newCovStatefulSidecarContainer(startDelay time.Duration) *covStatefulSidecarContainer {
	c := &covStatefulSidecarContainer{startDelay: startDelay}
	c.route = func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
		script := covScript(cfg)
		switch {
		case strings.Contains(script, "command -v tmux"):
			return covResult("tmux-check", ""), nil
		case strings.Contains(script, "crewship-sidecar --addr"):
			c.started.Add(1)
			time.Sleep(c.startDelay)
			c.healthMu.Lock()
			c.healthy = true
			c.healthMu.Unlock()
			return covResult("sidecar-start", ""), nil
		case strings.Contains(script, "9119/health"):
			c.healthMu.RLock()
			defer c.healthMu.RUnlock()
			if !c.healthy {
				return covResult("sidecar-health", ""), nil
			}
			return covResult("sidecar-health", `{"status":"ok","network_mode":"free"}`), nil
		case len(cfg.Cmd) > 0 && cfg.Cmd[0] == "stdbuf":
			return covResult("agent-exec", "{}\n"), nil
		}
		return nil, nil
	}
	c.inspect = func(execID string) (bool, int, error) {
		switch execID {
		case "tmux-check":
			return false, 1, nil // tmux missing → stdbuf fallback
		case "agent-exec", "sidecar-start":
			return false, 0, nil
		}
		return false, 0, nil
	}
	return c
}

// TestRunAgent_ConcurrentExecsStartSidecarOnce is the #1220 regression: N
// agents dispatching into the SAME crew container at the same instant must
// produce exactly one sidecar start, not N.
func TestRunAgent_ConcurrentExecsStartSidecarOnce(t *testing.T) {
	t.Parallel()
	c := newCovStatefulSidecarContainer(30 * time.Millisecond)
	o := New(c, newMemState(), covQuietLogger())
	o.SetSidecarEnabled(true)
	o.SetIPCConfig("http://gw:9000", "master-secret")

	const agents = 4
	var wg sync.WaitGroup
	errs := make([]error, agents)
	for i := 0; i < agents; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := covRunReq()
			// Same ContainerID (covRunReq's default) — one crew, one
			// container, many agents. Distinct agent identity per
			// goroutine so nothing else dedupes them.
			req.AgentID = "agent-" + string(rune('a'+i))
			req.AgentSlug = req.AgentID
			errs[i] = o.RunAgent(context.Background(), req, nil)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("RunAgent[%d]: %v", i, err)
		}
	}
	if got := c.started.Load(); got != 1 {
		t.Errorf("sidecar started %d times across %d concurrent execs in one container, want exactly 1 — "+
			"the check→start sequence is not mutually excluded (#1220)", got, agents)
	}
}

// TestWithSidecarLock_DifferentContainersDoNotContend pins the other half
// of the ask: the gate must be per-container, never global. A global lock
// on the hot RunAgent path would serialize every crew on the host.
func TestWithSidecarLock_DifferentContainersDoNotContend(t *testing.T) {
	t.Parallel()
	o := &Orchestrator{} // bare, no New() — exercises the lazy map init

	// Both goroutines hold their own container's gate at the same time.
	// If the gate were global this deadlocks and the test times out.
	inA := make(chan struct{})
	inB := make(chan struct{})
	done := make(chan struct{})

	go func() {
		_ = o.withSidecarLock(context.Background(), "container-a", func() error {
			close(inA)
			<-inB // only completes if B can hold its gate concurrently
			return nil
		})
		close(done)
	}()

	<-inA
	if err := o.withSidecarLock(context.Background(), "container-b", func() error {
		close(inB)
		return nil
	}); err != nil {
		t.Fatalf("container-b: %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("different containers contended — the gate must not be global")
	}
	if n := o.sidecarRestartLockCount(); n != 2 {
		t.Errorf("lock count = %d, want 2 (one per container)", n)
	}
}

// TestWithSidecarLock_SerializesSameContainer proves the gate actually
// excludes: two holders of the same key must never overlap.
func TestWithSidecarLock_SerializesSameContainer(t *testing.T) {
	t.Parallel()
	o := &Orchestrator{}

	var mu sync.Mutex
	inside, maxInside := 0, 0
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = o.withSidecarLock(context.Background(), "same", func() error {
				mu.Lock()
				inside++
				if inside > maxInside {
					maxInside = inside
				}
				mu.Unlock()
				time.Sleep(2 * time.Millisecond)
				mu.Lock()
				inside--
				mu.Unlock()
				return nil
			})
		}()
	}
	wg.Wait()
	if maxInside != 1 {
		t.Errorf("max concurrent holders = %d, want 1", maxInside)
	}
}

// TestWithSidecarLock_CancelledContextDoesNotRun — the waiter is holding a
// runSem slot for the whole run, so a cancelled run must be able to
// abandon the wait rather than pin a process-wide slot behind another
// crew's slow startSidecar. A plain sync.Mutex could not do this.
func TestWithSidecarLock_CancelledContextDoesNotRun(t *testing.T) {
	t.Parallel()
	o := &Orchestrator{}

	held := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = o.withSidecarLock(context.Background(), "busy", func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ran := false
	err := o.withSidecarLock(ctx, "busy", func() error {
		ran = true
		return nil
	})
	close(release)

	if err == nil {
		t.Error("expected the cancelled wait to return an error")
	}
	if ran {
		t.Error("fn must not run when the wait was cancelled")
	}
}

// TestWithSidecarLock_EmptyContainerIDRunsUnlocked — an empty id must not
// key every crew onto one shared "" gate, which would serialize unrelated
// containers. The sidecar block is gated on sidecarEnabled, not on a
// non-empty ContainerID, so this is reachable in principle.
func TestWithSidecarLock_EmptyContainerIDRunsUnlocked(t *testing.T) {
	t.Parallel()
	o := &Orchestrator{}
	ran := false
	if err := o.withSidecarLock(context.Background(), "", func() error {
		ran = true
		return nil
	}); err != nil {
		t.Fatalf("empty container id: %v", err)
	}
	if !ran {
		t.Error("fn must still run for an empty container id")
	}
	if n := o.sidecarRestartLockCount(); n != 0 {
		t.Errorf("lock count = %d, want 0 — an empty id must not allocate a gate", n)
	}
}
