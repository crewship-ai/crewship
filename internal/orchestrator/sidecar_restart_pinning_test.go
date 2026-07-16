package orchestrator

// #1232 (the #1160 residual question): does a restricted-mode crew with a
// NON-EMPTY domain allowlist restart its sidecar on every exec? This is the
// issue's live repro (dispatch into the same crew container twice
// sequentially, second exec must log "reusing" not "restarting") in unit
// form, driven through the REAL RunAgent sidecar sequence.
//
// The #1220 race tests hard-code the hash a (re)started sidecar reports
// (fake.startHash = DomainsHash(domains)) — they feed the orchestrator its
// own answer back, so they can never catch RunAgent handing startSidecar a
// DIFFERENT domain set than the one it hashes for the restart decision.
// The fake here closes that gap: it derives the reported health state from
// the actual base64 payload startSidecar piped into the container, the way
// a real sidecar does (policy domains only when restricted). If desired-
// domains computation and the startSidecar wire payload ever diverge, the
// second exec restarts and this test goes red.
//
// (The cross-package half of the contract — that internal/sidecar's real
// NewServer + /health reports the same hash for that payload — is pinned by
// internal/sidecar/domains_hash_parity_test.go, which can import both
// packages.)

import (
	"context"
	"encoding/base64"
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

// payloadDrivenContainer is a stub ContainerProvider whose running-sidecar
// state (network_mode, domains_hash) is derived from the startSidecar stdin
// payload it actually received, instead of being preset by the test.
type payloadDrivenContainer struct {
	mu   sync.Mutex
	mode map[string]string // containerID → running sidecar network_mode ("" = not running)
	hash map[string]string // containerID → domains_hash the sidecar reports on /health

	kills  int32
	starts int32
}

func newPayloadDrivenContainer() *payloadDrivenContainer {
	return &payloadDrivenContainer{
		mode: map[string]string{},
		hash: map[string]string{},
	}
}

// sidecarPayloadPolicy extracts the network policy from startSidecar's shell
// script: `echo '<base64>' | base64 -d | crewship-sidecar --addr ...`.
func sidecarPayloadPolicy(t *testing.T, script string) *SidecarNetworkPolicy {
	t.Helper()
	start := strings.Index(script, "echo '")
	if start < 0 {
		t.Fatalf("start script carries no payload: %q", script)
	}
	rest := script[start+len("echo '"):]
	end := strings.Index(rest, "'")
	if end < 0 {
		t.Fatalf("unterminated payload in start script: %q", script)
	}
	raw, err := base64.StdEncoding.DecodeString(rest[:end])
	if err != nil {
		t.Fatalf("decode sidecar payload: %v", err)
	}
	var input struct {
		NetworkPolicy *SidecarNetworkPolicy `json:"network_policy,omitempty"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatalf("unmarshal sidecar payload: %v", err)
	}
	return input.NetworkPolicy
}

func (c *payloadDrivenContainer) exec(t *testing.T, cfg provider.ExecConfig) (*provider.ExecResult, error) {
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
		policy := sidecarPayloadPolicy(t, joined)
		// Mirror the real sidecar's derivation (main.go + NewServer): empty
		// mode defaults to free; the /health hash covers ONLY the policy
		// domains, and only restricted mode has any.
		mode := "free"
		var policyDomains []string
		if policy != nil && policy.Mode != "" {
			mode = policy.Mode
		}
		if policy != nil && mode == "restricted" {
			policyDomains = policy.AllowedDomains
		}
		c.mu.Lock()
		c.mode[cfg.ContainerID] = mode
		c.hash[cfg.ContainerID] = DomainsHash(policyDomains)
		c.mu.Unlock()
		return execResult("start", ""), nil

	case strings.Contains(joined, "127.0.0.1:9119/health"):
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

func (c *payloadDrivenContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, 0, nil
}
func (c *payloadDrivenContainer) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	return "container-x", nil
}
func (c *payloadDrivenContainer) StopCrewRuntime(_ context.Context, _ string) error   { return nil }
func (c *payloadDrivenContainer) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (c *payloadDrivenContainer) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{State: "running"}, nil
}
func (c *payloadDrivenContainer) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (c *payloadDrivenContainer) CrewContainerName(_ string, slug string) string {
	return "crewship-team-" + slug
}
func (c *payloadDrivenContainer) CopyToContainer(_ context.Context, _ string, _ string, _ io.Reader) error {
	return nil
}

// testExecT threads the *testing.T into the provider.ContainerProvider
// interface method (which has no test handle of its own).
type payloadDrivenContainerT struct {
	*payloadDrivenContainer
	t *testing.T
}

func (c payloadDrivenContainerT) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	return c.exec(c.t, cfg)
}

func runOneAgent(t *testing.T, o *Orchestrator, req AgentRunRequest) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = o.RunAgent(context.Background(), req, nil)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("RunAgent did not finish within 30s")
	}
}

// TestRunAgent_RestrictedNonEmptyAllowlist_SequentialExecsReuseSidecar is
// #1232's title as a test: two crew members dispatch sequentially into the
// same restricted-mode crew container with the SAME non-empty allowlist.
// The first exec cold-starts the sidecar; the second must reuse it — a
// second start (or any kill) means the restart-skip broke for the non-empty
// case and every exec into the crew churns the sidecar (#1160 residual).
func TestRunAgent_RestrictedNonEmptyAllowlist_SequentialExecsReuseSidecar(t *testing.T) {
	// Mixed case mirrors real operator input; the crew-level allowlist is
	// identical for both members, exactly like the dev2 observation.
	domains := []string{"api.GitHub.com", "registry.npmjs.org", "docs.crewship.ai", "api.linear.app"}

	fake := newPayloadDrivenContainer()
	o := New(payloadDrivenContainerT{fake, t}, newLockedMemState(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	o.SetSidecarEnabled(true)

	runOneAgent(t, o, sidecarRaceRequest("riley", "chat-riley", "shared-c1232", "restricted", domains))
	if got := atomic.LoadInt32(&fake.starts); got != 1 {
		t.Fatalf("first exec started the sidecar %d times, want exactly 1", got)
	}

	runOneAgent(t, o, sidecarRaceRequest("morgan", "chat-morgan", "shared-c1232", "restricted", domains))
	if got := atomic.LoadInt32(&fake.starts); got != 1 {
		t.Fatalf("second exec with an UNCHANGED non-empty allowlist restarted the sidecar (starts=%d, want 1) — restricted-mode crews churn their sidecar on every exec (#1232 / #1160 residual)", got)
	}
	if got := atomic.LoadInt32(&fake.kills); got != 0 {
		t.Fatalf("second exec with an unchanged allowlist killed the sidecar %d times, want 0", got)
	}
}

// TestRunAgent_RestrictedAllowlistChange_RestartsOnce is the counterpart:
// when the crew's allowlist genuinely changes between execs, the next exec
// must restart the sidecar exactly once — and an exec after THAT (same new
// allowlist) must reuse the restarted sidecar, proving the restart
// converges instead of ping-ponging.
func TestRunAgent_RestrictedAllowlistChange_RestartsOnce(t *testing.T) {
	before := []string{"api.github.com"}
	after := []string{"api.github.com", "api.linear.app"}

	fake := newPayloadDrivenContainer()
	o := New(payloadDrivenContainerT{fake, t}, newLockedMemState(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	o.SetSidecarEnabled(true)

	runOneAgent(t, o, sidecarRaceRequest("riley", "chat-1", "shared-c1232b", "restricted", before))
	runOneAgent(t, o, sidecarRaceRequest("morgan", "chat-2", "shared-c1232b", "restricted", after))

	if got := atomic.LoadInt32(&fake.kills); got != 1 {
		t.Fatalf("allowlist change killed the sidecar %d times, want exactly 1", got)
	}
	if got := atomic.LoadInt32(&fake.starts); got != 2 {
		t.Fatalf("allowlist change: %d sidecar starts, want 2 (cold start + one restart)", got)
	}

	// Convergence: the same new allowlist must now be reusable.
	runOneAgent(t, o, sidecarRaceRequest("riley", "chat-3", "shared-c1232b", "restricted", after))
	if got := atomic.LoadInt32(&fake.starts); got != 2 {
		t.Fatalf("exec after the restart restarted AGAIN (starts=%d, want 2) — the restart does not converge", got)
	}
}
