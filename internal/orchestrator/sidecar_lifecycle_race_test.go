package orchestrator

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// These tests lock #1220: multiple agents share ONE Docker container per crew,
// and RunAgent's sidecar sequence (checkSidecar → sidecarNeedsRestart → pkill →
// startSidecar) had no mutual exclusion keyed by container. Two execs
// dispatching at nearly the same moment could both observe the same health
// state, both decide to (re)start, and both pkill + startSidecar — one killing
// the other's freshly started sidecar, or double-starting it. #1214 fixed the
// single-exec restart ordering (wait for the old process to die) and the
// needless restricted-mode restarts, but explicitly deferred cross-exec mutual
// exclusion to this follow-up.
//
// The fake below makes the interleaving deterministic: the sidecar health
// check parks until a QUORUM of concurrent health probes has arrived (or a
// grace timeout passes, so a serialized post-fix run is never wedged). Without
// a per-container lock, both RunAgents sample health simultaneously → both see
// the same pre-restart state → double kill / double start, and the counters
// prove it. With the lock, the second exec can only probe after the first
// finished starting, sees a healthy matching sidecar, and reuses it.

// sidecarRaceContainer is a stub ContainerProvider that models the running
// sidecar of each container as (network_mode, domains_hash) state, mutated by
// the pkill and startSidecar exec scripts, and reported by the health-check
// exec script — the exact three execs RunAgent's sidecar sequence issues.
type sidecarRaceContainer struct {
	mu   sync.Mutex
	mode map[string]string // containerID → running sidecar network_mode ("" = not running)
	hash map[string]string // containerID → domains_hash the sidecar reports on /health

	// What a (re)started sidecar reports afterwards — the fake's stand-in for
	// "the new sidecar picked up the desired policy".
	startMode string
	startHash string

	kills  int32
	starts int32

	// Health-probe rendezvous: each health exec parks until healthQuorum
	// probes have arrived or healthGrace elapses. quorum reached within the
	// grace window is recorded so tests can assert probes ran concurrently.
	healthArrivals  int32
	healthQuorum    int32
	healthGrace     time.Duration
	healthReleased  chan struct{}
	healthReleaseFn sync.Once
	quorumReached   int32
}

func newSidecarRaceContainer(quorum int32, grace time.Duration) *sidecarRaceContainer {
	return &sidecarRaceContainer{
		mode:           map[string]string{},
		hash:           map[string]string{},
		healthQuorum:   quorum,
		healthGrace:    grace,
		healthReleased: make(chan struct{}),
	}
}

func execResult(id, out string) *provider.ExecResult {
	return &provider.ExecResult{ExecID: id, Reader: io.NopCloser(strings.NewReader(out))}
}

func (c *sidecarRaceContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	joined := strings.Join(cfg.Cmd, " ")

	switch {
	case strings.Contains(joined, "pkill -f '^crewship-sidecar'"):
		atomic.AddInt32(&c.kills, 1)
		c.mu.Lock()
		c.mode[cfg.ContainerID] = "" // sidecar killed
		c.mu.Unlock()
		return execResult("kill", ""), nil

	case strings.Contains(joined, "crewship-sidecar --addr"):
		atomic.AddInt32(&c.starts, 1)
		c.mu.Lock()
		c.mode[cfg.ContainerID] = c.startMode
		c.hash[cfg.ContainerID] = c.startHash
		c.mu.Unlock()
		return execResult("start", ""), nil

	case strings.Contains(joined, "127.0.0.1:9119/health"):
		// checkSidecar's probe. Park until the quorum of concurrent probes
		// has arrived (deterministically exposing the unlocked interleaving)
		// or until the grace timeout (so a serialized run proceeds alone).
		if atomic.AddInt32(&c.healthArrivals, 1) >= c.healthQuorum {
			atomic.StoreInt32(&c.quorumReached, 1)
			c.healthReleaseFn.Do(func() { close(c.healthReleased) })
		}
		select {
		case <-c.healthReleased:
		case <-time.After(c.healthGrace):
		}
		c.mu.Lock()
		mode, hash := c.mode[cfg.ContainerID], c.hash[cfg.ContainerID]
		c.mu.Unlock()
		if mode == "" {
			return execResult("health", ""), nil // no sidecar listening
		}
		body, _ := json.Marshal(map[string]string{
			"status":       "ok",
			"network_mode": mode,
			"domains_hash": hash,
		})
		return execResult("health", string(body)), nil
	}

	return execResult("noop", ""), nil
}

func (c *sidecarRaceContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, 0, nil
}

func (c *sidecarRaceContainer) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	return "container-x", nil
}
func (c *sidecarRaceContainer) StopCrewRuntime(_ context.Context, _ string) error   { return nil }
func (c *sidecarRaceContainer) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (c *sidecarRaceContainer) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{State: "running"}, nil
}
func (c *sidecarRaceContainer) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (c *sidecarRaceContainer) CrewContainerName(_ string, slug string) string {
	return "crewship-team-" + slug
}
func (c *sidecarRaceContainer) CopyToContainer(_ context.Context, _ string, _ string, _ io.Reader) error {
	return nil
}

// runTwoAgents drives two concurrent RunAgent calls and waits for both.
func runTwoAgents(t *testing.T, o *Orchestrator, reqA, reqB AgentRunRequest) {
	t.Helper()
	var wg sync.WaitGroup
	for _, req := range []AgentRunRequest{reqA, reqB} {
		wg.Add(1)
		go func(r AgentRunRequest) {
			defer wg.Done()
			_ = o.RunAgent(context.Background(), r, nil)
		}(req)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("RunAgent goroutines did not finish within 30s")
	}
}

func sidecarRaceRequest(agent, chat, container, mode string, domains []string) AgentRunRequest {
	return AgentRunRequest{
		AgentID:        "id-" + agent,
		AgentSlug:      agent,
		ChatID:         chat,
		ContainerID:    container,
		CLIAdapter:     "CLAUDE_CODE",
		UserMessage:    "go",
		TimeoutSecs:    30,
		NetworkMode:    mode,
		AllowedDomains: domains,
	}
}

// TestRunAgent_ConcurrentColdStart_SingleSidecarStart: two agents dispatch
// into the same crew container while NO sidecar is running. Both health
// probes are held until both have sampled (the unlocked interleaving), so
// without per-container serialization both observe "not running" and both
// call startSidecar — a double start (#1220). With the sequence serialized,
// the second exec probes only after the first finished starting, sees the
// healthy free-mode sidecar, and reuses it: exactly one start, zero kills.
func TestRunAgent_ConcurrentColdStart_SingleSidecarStart(t *testing.T) {
	fake := newSidecarRaceContainer(2, 1500*time.Millisecond)
	fake.startMode = "free"

	o := New(fake, newLockedMemState(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	o.SetSidecarEnabled(true)

	runTwoAgents(t, o,
		sidecarRaceRequest("agent-a", "chat-a", "shared-c1", "", nil),
		sidecarRaceRequest("agent-b", "chat-b", "shared-c1", "", nil),
	)

	if got := atomic.LoadInt32(&fake.starts); got != 1 {
		t.Fatalf("sidecar started %d times for one shared container — concurrent execs must serialize the check→start sequence (#1220), want exactly 1", got)
	}
	if got := atomic.LoadInt32(&fake.kills); got != 0 {
		t.Fatalf("sidecar killed %d times on a cold start, want 0", got)
	}
}

// TestRunAgent_ConcurrentPolicyRestart_SingleKillSingleStart: the shared
// container runs a free-mode sidecar while both agents want restricted mode.
// Both health probes are held until both sampled the STALE pre-restart state;
// without serialization both decide "needs restart" and both pkill + start —
// the double kill/double start interleaving from #1220 where one exec kills
// the other's freshly started sidecar. Serialized, the second exec sees the
// already-restarted sidecar (matching mode + allowlist hash) and reuses it:
// exactly one kill and one start.
func TestRunAgent_ConcurrentPolicyRestart_SingleKillSingleStart(t *testing.T) {
	domains := []string{"example.com"}

	fake := newSidecarRaceContainer(2, 1500*time.Millisecond)
	fake.mode["shared-c2"] = "free" // running sidecar predates the policy change
	fake.startMode = "restricted"
	fake.startHash = domainsHash(domains) // restarted sidecar reports the desired allowlist

	o := New(fake, newLockedMemState(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	o.SetSidecarEnabled(true)

	runTwoAgents(t, o,
		sidecarRaceRequest("agent-a", "chat-a", "shared-c2", "restricted", domains),
		sidecarRaceRequest("agent-b", "chat-b", "shared-c2", "restricted", domains),
	)

	if got := atomic.LoadInt32(&fake.kills); got != 1 {
		t.Fatalf("sidecar killed %d times for one policy change — concurrent execs must serialize the check→kill→start sequence (#1220), want exactly 1", got)
	}
	if got := atomic.LoadInt32(&fake.starts); got != 1 {
		t.Fatalf("sidecar started %d times for one policy change, want exactly 1", got)
	}
}

// TestRunAgent_DifferentContainers_SidecarChecksRunConcurrently guards the
// issue's second requirement: serialization must be PER CONTAINER, not global.
// Two agents in two different crew containers (both already healthy, nothing
// to restart) must be able to have their health probes in flight
// simultaneously — the quorum-of-2 rendezvous must be reached within the
// grace window. A global lock would serialize the probes and never reach
// quorum concurrently.
func TestRunAgent_DifferentContainers_SidecarChecksRunConcurrently(t *testing.T) {
	fake := newSidecarRaceContainer(2, 5*time.Second)
	fake.mode["c-a"] = "free"
	fake.mode["c-b"] = "free"

	o := New(fake, newLockedMemState(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	o.SetSidecarEnabled(true)

	runTwoAgents(t, o,
		sidecarRaceRequest("agent-a", "chat-a", "c-a", "", nil),
		sidecarRaceRequest("agent-b", "chat-b", "c-b", "", nil),
	)

	if atomic.LoadInt32(&fake.quorumReached) != 1 {
		t.Fatal("health probes for DIFFERENT containers never overlapped — the #1220 lock must be per-container, not global")
	}
	if got := atomic.LoadInt32(&fake.starts); got != 0 {
		t.Fatalf("healthy sidecars were (re)started %d times, want 0", got)
	}
	if got := atomic.LoadInt32(&fake.kills); got != 0 {
		t.Fatalf("healthy sidecars were killed %d times, want 0", got)
	}
}
