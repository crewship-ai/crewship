package chatbridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ws"
)

// This file locks the BridgeConfig fallback contract that PR #389 introduced:
//
//   if cfg.DefaultMemoryMB <= 0 { cfg.DefaultMemoryMB = 8192 }
//   if cfg.DefaultCPUs    <= 0 { cfg.DefaultCPUs    = 2.0  }
//
// The bump from 512 MiB → 8192 MiB landed because the old default was
// triggering Docker OOM-kills on real agent workloads. These tests pin the
// numeric values, the <=0 guard semantics, and — most importantly — the rule
// that a resolver-supplied MemoryMB > 0 always wins over the fallback.

// ---------- New() default-fallback unit tests ----------

// newBridgeForCfg constructs a Bridge with the given BridgeConfig and
// otherwise-zero collaborators. We're only interested in inspecting the cfg
// that New() materialised into b.cfg.
func newBridgeForCfg(t *testing.T, cfg BridgeConfig) *Bridge {
	t.Helper()
	dir := t.TempDir()
	logger := slog.Default()
	convStore := conversation.NewStore(dir, logger)
	logWriter := logcollector.NewWriter(dir, logger)
	orch := orchestrator.New(nil, &memState{data: make(map[string]map[string][]byte)}, logger)
	return New(orch, nil, convStore, logWriter, &mockResolver{}, cfg, logger)
}

// TestBridge_NewMemoryZeroDefault_AppliesEightGiB pins the PR #389 default:
// callers (e.g. server boot) that pass a zero-value BridgeConfig must land
// on 8192 MiB, not the legacy 512 MiB that caused Docker OOM-kills on
// claude/gemini CLI + MCP workloads.
func TestBridge_NewMemoryZeroDefault_AppliesEightGiB(t *testing.T) {
	b := newBridgeForCfg(t, BridgeConfig{DefaultMemoryMB: 0})
	if b.cfg.DefaultMemoryMB != 8192 {
		t.Errorf("zero-value DefaultMemoryMB should fall back to 8192, got %d", b.cfg.DefaultMemoryMB)
	}
}

// TestBridge_NewMemoryNegativeDefault_AppliesEightGiB pins the <=0 guard from
// PR #389. A negative value (e.g. a misused "-1 means unset" sentinel) must
// also clamp to 8192 — never reach Docker, which rejects negative limits.
func TestBridge_NewMemoryNegativeDefault_AppliesEightGiB(t *testing.T) {
	b := newBridgeForCfg(t, BridgeConfig{DefaultMemoryMB: -1})
	if b.cfg.DefaultMemoryMB != 8192 {
		t.Errorf("negative DefaultMemoryMB should fall back to 8192, got %d", b.cfg.DefaultMemoryMB)
	}
}

// TestBridge_NewMemoryExplicit_Preserved verifies that callers who deliberately
// pass a positive override (e.g. a smaller test rig, a larger production
// deployment) are not silently clobbered by the PR #389 fallback. The guard
// is <=0, so any positive int must survive untouched.
func TestBridge_NewMemoryExplicit_Preserved(t *testing.T) {
	b := newBridgeForCfg(t, BridgeConfig{DefaultMemoryMB: 4096})
	if b.cfg.DefaultMemoryMB != 4096 {
		t.Errorf("explicit DefaultMemoryMB=4096 must be preserved, got %d", b.cfg.DefaultMemoryMB)
	}
}

// TestBridge_NewCPUsZeroDefault_AppliesTwo pins the CPU half of PR #389:
// zero-value DefaultCPUs falls back to 2.0 (matches the comment in bridge.go
// — the resource limits are a paired contract, both must default safely).
func TestBridge_NewCPUsZeroDefault_AppliesTwo(t *testing.T) {
	b := newBridgeForCfg(t, BridgeConfig{DefaultCPUs: 0})
	if b.cfg.DefaultCPUs != 2.0 {
		t.Errorf("zero-value DefaultCPUs should fall back to 2.0, got %v", b.cfg.DefaultCPUs)
	}
}

// TestBridge_NewCPUsNegativeDefault_AppliesTwo pins the <=0 guard for CPUs.
// Same regression risk as memory: a negative override would be rejected by
// Docker downstream, so the bridge must clamp at construction time.
func TestBridge_NewCPUsNegativeDefault_AppliesTwo(t *testing.T) {
	b := newBridgeForCfg(t, BridgeConfig{DefaultCPUs: -0.5})
	if b.cfg.DefaultCPUs != 2.0 {
		t.Errorf("negative DefaultCPUs should fall back to 2.0, got %v", b.cfg.DefaultCPUs)
	}
}

// TestBridge_NewCPUsExplicit_Preserved verifies a positive caller-supplied
// CPU limit (e.g. a 4-core dev box bump) is honored — the fallback math is
// strictly a floor for misconfigured/unset values, not a clamp on legit ones.
func TestBridge_NewCPUsExplicit_Preserved(t *testing.T) {
	b := newBridgeForCfg(t, BridgeConfig{DefaultCPUs: 1.5})
	if b.cfg.DefaultCPUs != 1.5 {
		t.Errorf("explicit DefaultCPUs=1.5 must be preserved, got %v", b.cfg.DefaultCPUs)
	}
}

// TestBridge_NewBothFieldsIndependent pins that the two guards are
// independent: a caller can set memory and let CPUs default (or vice versa)
// without one field clobbering the other. The PR #389 implementation has
// two separate `if` blocks; a future refactor that, say, replaced them with
// a single `if memory<=0 && cpus<=0` would break this case.
func TestBridge_NewBothFieldsIndependent(t *testing.T) {
	b := newBridgeForCfg(t, BridgeConfig{DefaultMemoryMB: 2048, DefaultCPUs: 0})
	if b.cfg.DefaultMemoryMB != 2048 {
		t.Errorf("explicit memory should survive, got %d", b.cfg.DefaultMemoryMB)
	}
	if b.cfg.DefaultCPUs != 2.0 {
		t.Errorf("zero CPUs should fall back to 2.0, got %v", b.cfg.DefaultCPUs)
	}
}

// ---------- Resolver-data-path tests ----------

// capturingContainer records the CrewConfig passed into EnsureCrewRuntime so
// tests can assert what resource limits actually flow through to Docker. It
// reports "running" on ContainerStatus and stubs out exec so RunAgent fails
// quickly without us caring about the LLM path.
type capturingContainer struct {
	mu          sync.Mutex
	captured    []provider.CrewConfig
	createCalls atomic.Int32
}

func (c *capturingContainer) EnsureCrewRuntime(_ context.Context, cc provider.CrewConfig) (string, error) {
	c.mu.Lock()
	c.captured = append(c.captured, cc)
	c.mu.Unlock()
	c.createCalls.Add(1)
	return "captured-container-id", nil
}
func (c *capturingContainer) StopCrewRuntime(_ context.Context, _ string) error   { return nil }
func (c *capturingContainer) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (c *capturingContainer) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{ID: "captured-container-id", State: "running"}, nil
}
func (c *capturingContainer) Exec(_ context.Context, _ provider.ExecConfig) (*provider.ExecResult, error) {
	return nil, errors.New("exec stub: capturing container")
}
func (c *capturingContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, 1, nil
}
func (c *capturingContainer) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, errors.New("stats stub")
}
func (c *capturingContainer) CrewContainerName(_ string, slug string) string {
	return "crewship-team-" + slug
}
func (c *capturingContainer) CopyToContainer(_ context.Context, _ string, _ string, _ io.Reader) error {
	return errors.New("copy stub")
}

func (c *capturingContainer) lastCaptured(t *testing.T) provider.CrewConfig {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.captured) == 0 {
		t.Fatalf("EnsureCrewRuntime was never called")
	}
	return c.captured[len(c.captured)-1]
}

// bridgeWithCapturingContainer wires a Bridge with the given resolver, a
// capturing container provider, and the supplied BridgeConfig so tests can
// reason about both halves of the fallback math (primary resolver path vs
// bridge fallback).
func bridgeWithCapturingContainer(t *testing.T, resolver ChatResolver, cfg BridgeConfig) (*Bridge, *capturingContainer) {
	t.Helper()
	dir := t.TempDir()
	logger := slog.Default()
	convStore := conversation.NewStore(dir, logger)
	logWriter := logcollector.NewWriter(dir, logger)
	ctr := &capturingContainer{}
	orch := orchestrator.New(ctr, &memState{data: make(map[string]map[string][]byte)}, logger)
	return New(orch, ctr, convStore, logWriter, resolver, cfg, logger), ctr
}

// TestBridge_ResolverMemoryWinsOverFallback is the load-bearing test for
// PR #389's "primary path is crews.container_memory_mb threaded through
// resolver" comment. When the resolver returns MemoryMB=4096, the bridge
// MUST forward 4096 — NOT the 8192 fallback — into provider.CrewConfig.
// Regression here would mean every crew silently gets the same default,
// no matter what container_memory_mb the user configured.
func TestBridge_ResolverMemoryWinsOverFallback(t *testing.T) {
	resolver := &mockResolver{
		info: &ChatInfo{
			AgentID:     "agent-1",
			AgentSlug:   "valid-slug",
			CrewID:      "crew-1",
			CrewSlug:    "ops",
			CLIAdapter:  "CLAUDE_CODE",
			ToolProfile: "CODING",
			TimeoutSecs: 30,
			MemoryMB:    4096, // primary path: resolver supplied
			CPUs:        3.5,  // primary path: resolver supplied
		},
	}
	b, ctr := bridgeWithCapturingContainer(t, resolver, BridgeConfig{}) // fallback would be 8192 / 2.0

	_ = b.HandleChatMessage(context.Background(), "u", "sess-1", "hello", func(_ ws.ChatEvent) {})

	got := ctr.lastCaptured(t)
	if got.MemoryMB != 4096 {
		t.Errorf("resolver MemoryMB=4096 must win over fallback 8192, got %d", got.MemoryMB)
	}
	if got.CPUs != 3.5 {
		t.Errorf("resolver CPUs=3.5 must win over fallback 2.0, got %v", got.CPUs)
	}
}

// TestBridge_ResolverZeroMemory_FallsBackToBridgeDefault verifies the other
// half of the contract: when the resolver returns MemoryMB=0 (crew has no
// per-row override), the bridge fills in its own default. We pair the assert
// with the PR #389 number so a future refactor of the fallback constant is
// caught here, not in prod.
func TestBridge_ResolverZeroMemory_FallsBackToBridgeDefault(t *testing.T) {
	resolver := &mockResolver{
		info: &ChatInfo{
			AgentID:     "agent-1",
			AgentSlug:   "valid-slug",
			CrewID:      "crew-1",
			CrewSlug:    "ops",
			CLIAdapter:  "CLAUDE_CODE",
			ToolProfile: "CODING",
			TimeoutSecs: 30,
			MemoryMB:    0, // resolver says "no override"
			CPUs:        0, // resolver says "no override"
		},
	}
	b, ctr := bridgeWithCapturingContainer(t, resolver, BridgeConfig{}) // New() promotes 0 → 8192 / 2.0

	_ = b.HandleChatMessage(context.Background(), "u", "sess-1", "hello", func(_ ws.ChatEvent) {})

	got := ctr.lastCaptured(t)
	if got.MemoryMB != 8192 {
		t.Errorf("resolver MemoryMB=0 must fall back to bridge default 8192, got %d", got.MemoryMB)
	}
	if got.CPUs != 2.0 {
		t.Errorf("resolver CPUs=0 must fall back to bridge default 2.0, got %v", got.CPUs)
	}
}

// TestBridge_ResolverNegativeMemory_FallsBackToBridgeDefault pins that the
// <=0 guard in HandleChatMessage (not just New()) also covers a buggy
// resolver that hands back a negative number. Without this guard, Docker
// would reject the create with a cryptic error mid-message.
func TestBridge_ResolverNegativeMemory_FallsBackToBridgeDefault(t *testing.T) {
	resolver := &mockResolver{
		info: &ChatInfo{
			AgentID:     "agent-1",
			AgentSlug:   "valid-slug",
			CrewID:      "crew-1",
			CrewSlug:    "ops",
			CLIAdapter:  "CLAUDE_CODE",
			ToolProfile: "CODING",
			TimeoutSecs: 30,
			MemoryMB:    -42,
			CPUs:        -1.0,
		},
	}
	b, ctr := bridgeWithCapturingContainer(t, resolver, BridgeConfig{})

	_ = b.HandleChatMessage(context.Background(), "u", "sess-1", "hello", func(_ ws.ChatEvent) {})

	got := ctr.lastCaptured(t)
	if got.MemoryMB != 8192 {
		t.Errorf("negative resolver MemoryMB must fall back, got %d", got.MemoryMB)
	}
	if got.CPUs != 2.0 {
		t.Errorf("negative resolver CPUs must fall back, got %v", got.CPUs)
	}
}

// TestBridge_BridgeConfigOverrideUsedWhenResolverZero verifies that a
// non-default BridgeConfig (e.g. an ops override at server boot) still gets
// consulted when the resolver returns zero values. Two-tier defaults:
// resolver > bridge config > hardcoded 8192. We're asserting tier 2.
func TestBridge_BridgeConfigOverrideUsedWhenResolverZero(t *testing.T) {
	resolver := &mockResolver{
		info: &ChatInfo{
			AgentID:     "agent-1",
			AgentSlug:   "valid-slug",
			CrewID:      "crew-1",
			CrewSlug:    "ops",
			CLIAdapter:  "CLAUDE_CODE",
			ToolProfile: "CODING",
			TimeoutSecs: 30,
			// no MemoryMB / CPUs override
		},
	}
	// Caller explicitly picks 6144 / 4.0. New() preserves them (positive).
	// HandleChatMessage then falls back to b.cfg.DefaultMemoryMB when
	// info.MemoryMB <=0, so this must surface 6144 — NOT 8192.
	b, ctr := bridgeWithCapturingContainer(t, resolver, BridgeConfig{DefaultMemoryMB: 6144, DefaultCPUs: 4.0})

	_ = b.HandleChatMessage(context.Background(), "u", "sess-1", "hello", func(_ ws.ChatEvent) {})

	got := ctr.lastCaptured(t)
	if got.MemoryMB != 6144 {
		t.Errorf("explicit bridge cfg DefaultMemoryMB=6144 must be used when resolver zero, got %d", got.MemoryMB)
	}
	if got.CPUs != 4.0 {
		t.Errorf("explicit bridge cfg DefaultCPUs=4.0 must be used when resolver zero, got %v", got.CPUs)
	}
}

// ---------- Cold-start vs warm path ----------

// TestBridge_ColdStart_AppliesResolverMemory drives the cold-start branch
// (empty cache → EnsureCrewRuntime called → "Starting container..." status).
// We assert both the side-effect (createCalls > 0) and that the resolver's
// MemoryMB threads through. This is the "first message on a freshly opened
// chat" path, the most common production hot path.
func TestBridge_ColdStart_AppliesResolverMemory(t *testing.T) {
	resolver := &mockResolver{
		info: &ChatInfo{
			AgentID:     "agent-1",
			AgentSlug:   "valid-slug",
			CrewID:      "crew-cold",
			CrewSlug:    "ops",
			CLIAdapter:  "CLAUDE_CODE",
			ToolProfile: "CODING",
			TimeoutSecs: 30,
			MemoryMB:    2048,
			CPUs:        1.0,
		},
	}
	b, ctr := bridgeWithCapturingContainer(t, resolver, BridgeConfig{})

	// Confirm cache is empty (cold).
	b.containerMu.RLock()
	_, cached := b.containerCache["crew-cold"]
	b.containerMu.RUnlock()
	if cached {
		t.Fatalf("test setup invariant: cache must be empty before first message")
	}

	var sawStarting bool
	streamFn := func(e ws.ChatEvent) {
		if e.Type == "status" && e.Content == "Starting container..." {
			sawStarting = true
		}
	}
	_ = b.HandleChatMessage(context.Background(), "u", "sess-cold", "hello", streamFn)

	if ctr.createCalls.Load() == 0 {
		t.Fatal("cold start should call EnsureCrewRuntime")
	}
	if !sawStarting {
		t.Error("cold start should emit 'Starting container...' status event")
	}
	got := ctr.lastCaptured(t)
	if got.MemoryMB != 2048 {
		t.Errorf("cold-start CrewConfig should have resolver MemoryMB=2048, got %d", got.MemoryMB)
	}
}

// TestBridge_WarmStart_SkipsContainerCreate drives the warm-path branch by
// pre-seeding b.containerCache. With the cached container reporting "running"
// (capturingContainer.ContainerStatus), the bridge MUST NOT call
// EnsureCrewRuntime and MUST NOT emit "Starting container..." — that's the
// whole point of the cache (kill status-event noise on every reply).
func TestBridge_WarmStart_SkipsContainerCreate(t *testing.T) {
	resolver := &mockResolver{
		info: &ChatInfo{
			AgentID:     "agent-1",
			AgentSlug:   "valid-slug",
			CrewID:      "crew-warm",
			CrewSlug:    "ops",
			CLIAdapter:  "CLAUDE_CODE",
			ToolProfile: "CODING",
			TimeoutSecs: 30,
			MemoryMB:    4096,
		},
	}
	b, ctr := bridgeWithCapturingContainer(t, resolver, BridgeConfig{})
	// Pre-warm the cache — simulates "user already sent N messages on this crew".
	b.containerMu.Lock()
	b.containerCache["crew-warm"] = "captured-container-id"
	b.containerMu.Unlock()

	var statuses []string
	streamFn := func(e ws.ChatEvent) {
		if e.Type == "status" {
			statuses = append(statuses, e.Content)
		}
	}
	_ = b.HandleChatMessage(context.Background(), "u", "sess-warm", "hello", streamFn)

	if ctr.createCalls.Load() != 0 {
		t.Errorf("warm start should NOT call EnsureCrewRuntime, got %d calls", ctr.createCalls.Load())
	}
	for _, s := range statuses {
		if s == "Starting container..." {
			t.Errorf("warm start should NOT emit 'Starting container...' status, got: %v", statuses)
		}
	}
}

// ---------- Resolver HTTP path (agent_config endpoint via /resolve) ----------

// TestBridge_ResolverHTTP_MemoryFlowsToCrewConfig is the end-to-end version
// of TestBridge_ResolverMemoryWinsOverFallback: we spin up an httptest server
// that mimics the internal /api/v1/internal/chats/{id}/resolve endpoint,
// have it return memory_mb=4096, and verify the bridge forwards exactly that
// value into provider.CrewConfig. Catches a bug where, say, the JSON tag
// changed on chatResolveResponse.MemoryMB and the value silently became 0
// — falling back to 8192 and looking "fine" in isolated unit tests.
func TestBridge_ResolverHTTP_MemoryFlowsToCrewConfig(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Both ResolveChat and the resource-config wire format land on
		// /resolve; the bridge.Run path only calls ResolveChat.
		if r.Header.Get("X-Internal-Token") != "tok" {
			t.Errorf("missing internal token header on %s", r.URL.Path)
		}
		resp := chatResolveResponse{
			AgentID:     "agent-http",
			AgentSlug:   "valid-slug",
			CrewID:      "crew-http",
			CrewSlug:    "ops",
			CLIAdapter:  "CLAUDE_CODE",
			ToolProfile: "CODING",
			TimeoutSecs: 30,
			MemoryMB:    4096, // primary path: server says 4096
			CPUs:        2.5,
			NetworkMode: "free",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	resolver := NewIPCResolver(ts.URL, "tok", slog.Default())

	// Wire bridge + capturing container with intentionally-low defaults so
	// a regression (resolver value lost) would show up as the bridge cfg
	// values, not the legacy 8192 fallback — clearer failure signal.
	dir := t.TempDir()
	logger := slog.Default()
	convStore := conversation.NewStore(dir, logger)
	logWriter := logcollector.NewWriter(dir, logger)
	ctr := &capturingContainer{}
	orch := orchestrator.New(ctr, &memState{data: make(map[string]map[string][]byte)}, logger)
	b := New(orch, ctr, convStore, logWriter, resolver, BridgeConfig{DefaultMemoryMB: 1, DefaultCPUs: 0.1}, logger)

	_ = b.HandleChatMessage(context.Background(), "u", "sess-http", "hello", func(_ ws.ChatEvent) {})

	got := ctr.lastCaptured(t)
	if got.MemoryMB != 4096 {
		t.Errorf("HTTP resolver memory_mb=4096 must reach CrewConfig, got %d", got.MemoryMB)
	}
	if got.CPUs != 2.5 {
		t.Errorf("HTTP resolver cpus=2.5 must reach CrewConfig, got %v", got.CPUs)
	}
}
