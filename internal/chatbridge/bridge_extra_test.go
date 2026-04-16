package chatbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ws"
)

// ---------- truncateID ----------

func TestTruncateID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"shorter than limit", "abc", 5, "abc"},
		{"equal to limit", "abcde", 5, "abcde"},
		{"longer than limit", "abcdefghij", 4, "abcd"},
		{"zero limit on non-empty", "x", 0, ""},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := truncateID(tc.in, tc.n)
			if got != tc.want {
				t.Errorf("truncateID(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}

// ---------- devcontainerNeedsProvision ----------

func TestDevcontainerNeedsProvision(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfgJSON string
		mise    string
		want    bool
	}{
		{"both empty", "", "", false},
		{"mise non-empty triggers provision", "", "[tools]\nnode='20'", true},
		{"mise whitespace only does not", "", "   \n  \t", false},
		{"empty cfg with no mise", "", "", false},
		{"cfg only metadata (containerEnv)", `{"containerEnv":{"FOO":"bar"}}`, "", false},
		{"cfg with features triggers provision", `{"image":"x","features":{"ghcr.io/devcontainers/features/go:1":{}}}`, "", true},
		{"cfg with postCreateCommand triggers provision", `{"image":"x","postCreateCommand":"echo hi"}`, "", true},
		{"unparseable cfg yields false (cannot provision)", `{not json`, "", false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := devcontainerNeedsProvision(tc.cfgJSON, tc.mise)
			if got != tc.want {
				t.Errorf("devcontainerNeedsProvision(%q, %q) = %v, want %v", tc.cfgJSON, tc.mise, got, tc.want)
			}
		})
	}
}

// ---------- generateMsgID fallback ----------

// Even if crypto/rand somehow returned no entropy (a panic-worthy event), the
// returned ID still has the "msg_" prefix and is non-empty. We can't easily
// fault-inject crypto/rand here, but we can call generateMsgID many times to
// validate uniqueness under load.
func TestGenerateMsgIDUniqueness(t *testing.T) {
	t.Parallel()
	const n = 1000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		id := generateMsgID()
		if id == "" {
			t.Fatalf("empty ID at iteration %d", i)
		}
		if !strings.HasPrefix(id, "msg_") {
			t.Fatalf("id missing msg_ prefix at iteration %d: %q", i, id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate ID: %q", id)
		}
		seen[id] = struct{}{}
	}
}

// ---------- HandleChatMessage: devcontainer-needs-provision short-circuit ----------

// This branch fires before RunAgent and writes an error event without touching
// the container or the orchestrator. Matches the fail-fast contract.
func TestHandleChatMessageBlocksWhenDevcontainerNotProvisioned(t *testing.T) {
	t.Parallel()
	resolver := &mockResolver{
		info: &ChatInfo{
			AgentID:            "a1",
			AgentSlug:          "alice",
			CrewID:             "crew-1",
			CrewSlug:           "engineering",
			CLIAdapter:         "CLAUDE_CODE",
			ToolProfile:        "CODING",
			TimeoutSecs:        30,
			DevcontainerConfig: `{"image":"x","features":{"ghcr.io/devcontainers/features/go:1":{}}}`,
			CachedImage:        "", // no cached image — must block
		},
	}
	b, _ := testBridge(t, resolver)

	var events []ws.ChatEvent
	streamFn := func(e ws.ChatEvent) { events = append(events, e) }

	err := b.HandleChatMessage(context.Background(), "user-1", "sess-1", "hello", streamFn)
	if err == nil {
		t.Fatal("expected error when devcontainer needs provisioning")
	}
	if !strings.Contains(err.Error(), "no provisioned image") {
		t.Errorf("unexpected error: %v", err)
	}
	hasErr := false
	for _, e := range events {
		if e.Type == "error" && strings.Contains(e.Content, "provision") {
			hasErr = true
		}
	}
	if !hasErr {
		t.Errorf("expected error event mentioning provision, got %+v", events)
	}
	// User message must NOT have been persisted (fail-fast before append).
	msgs, err := b.convStore.Read(context.Background(), "sess-1", 0, 0)
	if err != nil {
		t.Fatalf("read conversation: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected no persisted messages on fail-fast, got %d", len(msgs))
	}
}

// Resolver returning info but with no container provider configured and no
// container ID must surface the "container provider not configured" error
// after the user message has been persisted.
func TestHandleChatMessageNoContainerProvider(t *testing.T) {
	t.Parallel()
	resolver := &mockResolver{
		info: &ChatInfo{
			AgentID:     "a1",
			AgentSlug:   "alice",
			CrewID:      "crew-1",
			CrewSlug:    "engineering",
			CLIAdapter:  "CLAUDE_CODE",
			ToolProfile: "CODING",
			TimeoutSecs: 30,
		},
	}
	b, _ := testBridge(t, resolver) // testBridge passes nil ContainerProvider

	var events []ws.ChatEvent
	streamFn := func(e ws.ChatEvent) { events = append(events, e) }
	err := b.HandleChatMessage(context.Background(), "user-1", "sess-1", "hello", streamFn)
	if err == nil {
		t.Fatal("expected error when no provider configured")
	}
	if !strings.Contains(err.Error(), "no container provider") {
		t.Errorf("unexpected error: %v", err)
	}
	// At least one error event with that text should have been streamed.
	saw := false
	for _, e := range events {
		if e.Type == "error" && strings.Contains(e.Content, "container provider") {
			saw = true
		}
	}
	if !saw {
		t.Errorf("expected error event about provider, got %+v", events)
	}
}

// ---------- mockResolver behavior under returned errors ----------

// Bridge's ResolveChat error path forwards the wrapped error. Verify the
// wrapping prefix.
func TestHandleChatMessageResolveErrorWrappedMessage(t *testing.T) {
	t.Parallel()
	resolver := &mockResolver{err: fmt.Errorf("db: connection refused")}
	b, _ := testBridge(t, resolver)
	err := b.HandleChatMessage(context.Background(), "u", "s", "hi", func(_ ws.ChatEvent) {})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "resolve chat") || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("expected wrapped error, got: %v", err)
	}
}

// ---------- successful container creation, then RunAgent failure ----------

// Container provider that returns a fresh ID from EnsureCrewRuntime and reports
// "running" on ContainerStatus. Exec always errors so RunAgent fails after
// exercising the container-creation branches in HandleChatMessage.
type startingContainer struct {
	createCalls atomic.Int32
}

func (s *startingContainer) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	s.createCalls.Add(1)
	return "container-id-1234567890", nil
}
func (s *startingContainer) StopCrewRuntime(_ context.Context, _ string) error   { return nil }
func (s *startingContainer) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (s *startingContainer) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{ID: "container-id-1234567890", State: "running"}, nil
}
func (s *startingContainer) Exec(_ context.Context, _ provider.ExecConfig) (*provider.ExecResult, error) {
	return nil, errors.New("exec stub")
}
func (s *startingContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, 1, nil
}
func (s *startingContainer) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, errors.New("stats stub")
}
func (s *startingContainer) CrewContainerName(slug string) string {
	return "crewship-team-" + slug
}
func (s *startingContainer) CopyToContainer(_ context.Context, _ string, _ string, _ io.Reader) error {
	return errors.New("copy stub")
}

// Drives the Status events on cold start, the runMeta build, and the FAILED
// UpdateRun path when RunAgent ultimately errors.
func TestHandleChatMessageColdStartStatusEvents(t *testing.T) {
	t.Parallel()
	resolver := &mockResolver{
		info: &ChatInfo{
			AgentID:     "agent-1",
			AgentSlug:   "valid-slug",
			CrewID:      "crew-1",
			CrewSlug:    "ops",
			CLIAdapter:  "CLAUDE_CODE",
			ToolProfile: "CODING",
			TimeoutSecs: 30,
		},
	}
	ctr := &startingContainer{}
	b := testBridgeWithContainer(t, resolver, ctr)

	var events []ws.ChatEvent
	streamFn := func(e ws.ChatEvent) { events = append(events, e) }

	err := b.HandleChatMessage(context.Background(), "user-1", "sess-1", "hello", streamFn)
	if err == nil {
		t.Fatal("expected error from RunAgent (Exec stubbed)")
	}
	if ctr.createCalls.Load() == 0 {
		t.Error("EnsureCrewRuntime should have been called on cold start")
	}

	// Cold start emits "Starting container..." → "Container ready" → "Starting agent...".
	wantStatuses := map[string]bool{
		"Starting container...": false,
		"Container ready":       false,
	}
	for _, e := range events {
		if e.Type == "status" {
			if _, ok := wantStatuses[e.Content]; ok {
				wantStatuses[e.Content] = true
			}
		}
	}
	for k, seen := range wantStatuses {
		if !seen {
			t.Errorf("missing status event %q in %+v", k, events)
		}
	}
}

// COORDINATOR agents (no crew) get a synthetic crew identity for container management.
// Deprecated: COORDINATOR role is deprecated (2026-04-16); test retained for regression safety.
func TestHandleChatMessageCoordinatorSyntheticCrew(t *testing.T) {
	t.Parallel()
	resolver := &mockResolver{
		info: &ChatInfo{
			AgentID:     "agent-coord",
			AgentSlug:   "coord",
			AgentRole:   "COORDINATOR",
			CrewID:      "", // no crew — bridge fabricates one
			WorkspaceID: "ws-1",
			CLIAdapter:  "CLAUDE_CODE",
			ToolProfile: "CODING",
			TimeoutSecs: 30,
		},
	}
	ctr := &startingContainer{}
	b := testBridgeWithContainer(t, resolver, ctr)

	_ = b.HandleChatMessage(context.Background(), "user-1", "sess-1", "hi", func(_ ws.ChatEvent) {})
	if ctr.createCalls.Load() == 0 {
		t.Error("EnsureCrewRuntime should have been called for coordinator")
	}
	// The bridge mutates info.CrewID/CrewSlug to the synthetic values — assert
	// the resolver received the same instance reflecting that.
	if resolver.info.CrewID != "coordinator-ws-1" {
		t.Errorf("CrewID = %q, want coordinator-ws-1", resolver.info.CrewID)
	}
	if resolver.info.CrewSlug != "coordinator" {
		t.Errorf("CrewSlug = %q, want coordinator", resolver.info.CrewSlug)
	}
}

// Container provider that reports the cached container as stopped — the bridge
// must invalidate the cache and recreate.
type stoppedThenStartedContainer struct {
	startingContainer
	statusCalls atomic.Int32
}

func (s *stoppedThenStartedContainer) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	s.statusCalls.Add(1)
	return &provider.ContainerStatus{ID: "stale", State: "stopped"}, nil
}

// Pre-populating the cache makes the bridge call ContainerStatus first; a
// "stopped" reply triggers eviction and a fresh EnsureCrewRuntime call.
func TestHandleChatMessageCachedContainerStaleRecreate(t *testing.T) {
	t.Parallel()
	resolver := &mockResolver{
		info: &ChatInfo{
			AgentID:     "agent-1",
			AgentSlug:   "valid-slug",
			CrewID:      "crew-1",
			CrewSlug:    "ops",
			CLIAdapter:  "CLAUDE_CODE",
			ToolProfile: "CODING",
			TimeoutSecs: 30,
		},
	}
	ctr := &stoppedThenStartedContainer{}
	b := testBridgeWithContainer(t, resolver, ctr)
	// Seed the cache with a stale container id.
	b.containerMu.Lock()
	b.containerCache["crew-1"] = "stale"
	b.containerMu.Unlock()

	_ = b.HandleChatMessage(context.Background(), "user-1", "sess-1", "hi", func(_ ws.ChatEvent) {})

	if ctr.statusCalls.Load() == 0 {
		t.Error("ContainerStatus should have been queried for cached id")
	}
	if ctr.createCalls.Load() == 0 {
		t.Error("EnsureCrewRuntime should have been called after cache invalidation")
	}
	// Cache should now hold the fresh ID, not the stale one.
	b.containerMu.RLock()
	got := b.containerCache["crew-1"]
	b.containerMu.RUnlock()
	if got == "stale" {
		t.Errorf("stale id should have been evicted, still cached as %q", got)
	}
}

// Cancelled context never lets a raw "context canceled" string leak into a
// streamed error event — the bridge maps that case to a clean done.
func TestHandleChatMessageContextCancelledNoLeakedError(t *testing.T) {
	t.Parallel()
	resolver := &mockResolver{
		info: &ChatInfo{
			AgentID:     "agent-1",
			AgentSlug:   "valid-slug",
			CrewID:      "crew-1",
			CrewSlug:    "ops",
			CLIAdapter:  "CLAUDE_CODE",
			ToolProfile: "CODING",
			TimeoutSecs: 30,
		},
	}
	b := testBridgeWithContainer(t, resolver, &startingContainer{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	streamFn := func(e ws.ChatEvent) {
		if e.Type == "error" && strings.Contains(e.Content, "context canceled") {
			t.Errorf("raw 'context canceled' leaked to user: %q", e.Content)
		}
	}
	_ = b.HandleChatMessage(ctx, "user-1", "sess-1", "hi", streamFn)
}
