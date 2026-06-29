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

// stubEnqueuer captures the args passed to EnqueueForCrew so the test can
// assert the bridge auto-triggered the build with the right crew + workspace.
type stubEnqueuer struct {
	called      bool
	gotCrewID   string
	gotWsID     string
	resStarted  bool
	resRunning  bool
	resStatus   string
	returnError error
}

func (s *stubEnqueuer) EnqueueForCrew(_ context.Context, crewID, workspaceID string) (ProvisioningEnqueueResult, error) {
	s.called = true
	s.gotCrewID = crewID
	s.gotWsID = workspaceID
	if s.returnError != nil {
		return ProvisioningEnqueueResult{}, s.returnError
	}
	return ProvisioningEnqueueResult{
		Started:        s.resStarted,
		AlreadyRunning: s.resRunning,
		Status:         s.resStatus,
	}, nil
}

// When a provisioner is wired, sending a message at an unprovisioned crew
// must auto-trigger the build, emit a structured crew_provisioning event
// the chat surface can render, and return a sentinel error so the
// frontend knows the message wasn't actually run. The user message stays
// out of conv-store because the agent never executed.
func TestHandleChatMessageAutoTriggersProvisioning(t *testing.T) {
	t.Parallel()
	resolver := &mockResolver{
		info: &ChatInfo{
			AgentID:            "a1",
			AgentSlug:          "alice",
			CrewID:             "crew-1",
			CrewSlug:           "engineering",
			WorkspaceID:        "ws-1",
			CLIAdapter:         "CLAUDE_CODE",
			ToolProfile:        "CODING",
			TimeoutSecs:        30,
			DevcontainerConfig: `{"image":"x","features":{"ghcr.io/devcontainers/features/go:1":{}}}`,
			CachedImage:        "",
		},
	}
	b, _ := testBridge(t, resolver)
	enq := &stubEnqueuer{resStarted: true}
	b.SetProvisioningEnqueuer(enq)

	var events []ws.ChatEvent
	streamFn := func(e ws.ChatEvent) { events = append(events, e) }

	err := b.HandleChatMessage(context.Background(), "user-1", "sess-1", "hello", streamFn)
	if err == nil {
		t.Fatal("expected sentinel error so frontend knows message wasn't executed")
	}
	if !strings.Contains(err.Error(), "provisioning kicked off") {
		t.Errorf("unexpected error: %v", err)
	}
	if !enq.called {
		t.Fatalf("expected EnqueueForCrew to be called")
	}
	if enq.gotCrewID != "crew-1" || enq.gotWsID != "ws-1" {
		t.Errorf("enqueue called with wrong args: crew=%q ws=%q", enq.gotCrewID, enq.gotWsID)
	}

	var sawProvisioning bool
	for _, e := range events {
		if e.Type == "crew_provisioning" {
			sawProvisioning = true
			meta, _ := e.Metadata.(map[string]any)
			if meta["crew_id"] != "crew-1" {
				t.Errorf("crew_provisioning event missing crew_id: %v", meta)
			}
		}
		if e.Type == "error" {
			t.Errorf("auto-provision path must not emit a red error event; got: %+v", e)
		}
	}
	if !sawProvisioning {
		t.Errorf("expected crew_provisioning event, got: %+v", events)
	}

	msgs, err := b.convStore.Read(context.Background(), "sess-1", 0, 0)
	if err != nil {
		t.Fatalf("read conversation: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("user message must not be persisted when build hasn't run; got %d", len(msgs))
	}
}

// cacheCheckContainer is a container provider that also implements the
// imagePresenceChecker capability the auto-provision gate uses to detect a
// pruned cached image. It embeds failContainer so EnsureCrewRuntime (and the
// rest of the ContainerProvider surface) behave like the existing fake — the
// gate fires before EnsureCrewRuntime, so the present-image path stops there.
type cacheCheckContainer struct {
	failContainer
	present bool
	presErr error
}

func (c *cacheCheckContainer) ImagePresentLocally(_ context.Context, _ string) (bool, error) {
	return c.present, c.presErr
}

func cacheMissingInfo(cachedImage string) *ChatInfo {
	return &ChatInfo{
		AgentID:            "a1",
		AgentSlug:          "alice",
		CrewID:             "crew-1",
		CrewSlug:           "engineering",
		WorkspaceID:        "ws-1",
		CLIAdapter:         "CLAUDE_CODE",
		ToolProfile:        "CODING",
		TimeoutSecs:        30,
		DevcontainerConfig: `{"image":"x","features":{"ghcr.io/devcontainers/features/go:1":{}}}`,
		CachedImage:        cachedImage,
	}
}

// A crew with a recorded cached image that's been pruned from the local Docker
// daemon must re-provision rather than letting the run path ImagePull the dead
// crewship-cache:* tag. The gate detects absence via ImagePresentLocally and
// routes into the same EnqueueForCrew path used for never-built crews.
func TestHandleChatMessage_ReprovisionsWhenCachedImageMissing(t *testing.T) {
	t.Parallel()
	resolver := &mockResolver{info: cacheMissingInfo("crewship-cache:deadbeef")}
	b := testBridgeWithContainer(t, resolver, &cacheCheckContainer{present: false})
	enq := &stubEnqueuer{resStarted: true}
	b.SetProvisioningEnqueuer(enq)

	var events []ws.ChatEvent
	streamFn := func(e ws.ChatEvent) { events = append(events, e) }

	err := b.HandleChatMessage(context.Background(), "user-1", "sess-1", "hello", streamFn)
	if err == nil || !strings.Contains(err.Error(), "provisioning kicked off") {
		t.Fatalf("err = %v, want 'provisioning kicked off'", err)
	}
	if !enq.called {
		t.Fatal("expected EnqueueForCrew when cached image missing locally")
	}
	if enq.gotCrewID != "crew-1" || enq.gotWsID != "ws-1" {
		t.Errorf("enqueue args: crew=%q ws=%q", enq.gotCrewID, enq.gotWsID)
	}
	var sawProvisioning bool
	for _, e := range events {
		if e.Type == "crew_provisioning" {
			sawProvisioning = true
		}
		if e.Type == "error" {
			t.Errorf("re-provision path must not emit a red error event; got: %+v", e)
		}
	}
	if !sawProvisioning {
		t.Errorf("expected crew_provisioning event, got: %+v", events)
	}
}

// The mirror case: the cached image is still present locally, so the gate must
// NOT re-provision — it falls through to the normal container-ensure path
// (which here fails via the embedded failContainer, proving we got past the
// gate without enqueueing a build).
func TestHandleChatMessage_NoReprovisionWhenCachedImagePresent(t *testing.T) {
	t.Parallel()
	resolver := &mockResolver{info: cacheMissingInfo("crewship-cache:0d08da4b8ac3")}
	b := testBridgeWithContainer(t, resolver, &cacheCheckContainer{present: true})
	enq := &stubEnqueuer{resStarted: true}
	b.SetProvisioningEnqueuer(enq)

	var events []ws.ChatEvent
	streamFn := func(e ws.ChatEvent) { events = append(events, e) }

	err := b.HandleChatMessage(context.Background(), "user-1", "sess-1", "hello", streamFn)
	if enq.called {
		t.Fatal("must NOT re-provision when cached image is present locally")
	}
	if err == nil || !strings.Contains(err.Error(), "ensure team runtime") {
		t.Fatalf("err = %v, want fall-through to container ensure ('ensure team runtime')", err)
	}
	for _, e := range events {
		if e.Type == "crew_provisioning" {
			t.Errorf("must not emit crew_provisioning when image present: %+v", e)
		}
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
func (s *startingContainer) CrewContainerName(_ string, slug string) string {
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
