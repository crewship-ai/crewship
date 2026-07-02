package chatbridge

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ws"
)

// blockingContainer is a provider.ContainerProvider whose EnsureCrewRuntime
// blocks until the test signals proceed (or the caller's ctx is cancelled),
// letting a test deterministically observe "a run has genuinely started and
// is still live" without depending on timing/sleeps. entered is closed on
// the FIRST call so the test can synchronize on "the winning run reached
// container setup" before attempting a concurrent second send.
type blockingContainer struct {
	enteredOnce sync.Once
	entered     chan struct{}
	proceed     chan struct{}
	err         error // returned once unblocked via proceed

	mu        sync.Mutex
	callCount int
}

func (b *blockingContainer) EnsureCrewRuntime(ctx context.Context, _ provider.CrewConfig) (string, error) {
	b.mu.Lock()
	b.callCount++
	b.mu.Unlock()
	b.enteredOnce.Do(func() { close(b.entered) })
	select {
	case <-b.proceed:
		return "container-1", b.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
func (b *blockingContainer) StopCrewRuntime(_ context.Context, _ string) error   { return nil }
func (b *blockingContainer) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (b *blockingContainer) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return nil, fmt.Errorf("not running")
}
func (b *blockingContainer) Exec(_ context.Context, _ provider.ExecConfig) (*provider.ExecResult, error) {
	return nil, fmt.Errorf("exec not supported in test")
}
func (b *blockingContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, 1, nil
}
func (b *blockingContainer) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, fmt.Errorf("stats unavailable")
}
func (b *blockingContainer) CrewContainerName(_ string, slug string) string {
	return "crewship-team-" + slug
}
func (b *blockingContainer) CopyToContainer(_ context.Context, _ string, _ string, _ io.Reader) error {
	return fmt.Errorf("copy unsupported")
}

func exclusivityChatInfo() *ChatInfo {
	return &ChatInfo{
		AgentID:     "agent-1",
		AgentSlug:   "test-agent",
		CrewID:      "crew-1",
		CrewSlug:    "test-crew",
		CLIAdapter:  "CLAUDE_CODE",
		ToolProfile: "CODING",
		TimeoutSecs: 30,
	}
}

// TestHandleChatMessage_CrossUserExclusivity is the core regression test:
// two DIFFERENT users messaging the same group chat concurrently must not
// race two RunAgent execs into the same agent container/tmux session. On
// unfixed code both sends reach EnsureCrewRuntime (callCount==2) and neither
// gets an agent_busy event; the fix makes the second sender bounce off the
// per-chat run lock without ever touching the container.
func TestHandleChatMessage_CrossUserExclusivity(t *testing.T) {
	resolver := &mockResolver{info: exclusivityChatInfo()}
	ctr := &blockingContainer{
		entered: make(chan struct{}),
		proceed: make(chan struct{}),
		err:     fmt.Errorf("boom"),
	}
	b := testBridgeWithContainer(t, resolver, ctr)

	const chatID = "chat-shared"

	var muA, muB sync.Mutex
	var eventsA, eventsB []ws.ChatEvent
	streamFnA := func(e ws.ChatEvent) { muA.Lock(); eventsA = append(eventsA, e); muA.Unlock() }
	streamFnB := func(e ws.ChatEvent) { muB.Lock(); eventsB = append(eventsB, e); muB.Unlock() }

	errA := make(chan error, 1)
	go func() {
		errA <- b.HandleChatMessage(context.Background(), "user-A", chatID, "hello from A", streamFnA)
	}()

	select {
	case <-ctr.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the first run to reach container setup")
	}

	// A DIFFERENT user sends to the SAME chat while the first run is still
	// live. This must be rejected cleanly, without starting a second exec.
	// Run it in its own goroutine with a bounded wait: on unfixed code this
	// second send also reaches EnsureCrewRuntime and blocks on ctr.proceed
	// right alongside the first, so it would never return promptly.
	errB := make(chan error, 1)
	go func() {
		errB <- b.HandleChatMessage(context.Background(), "user-B", chatID, "hello from B", streamFnB)
	}()
	select {
	case err := <-errB:
		if err != nil {
			t.Fatalf("expected the busy-rejection to return nil (handled, not an error), got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second sender was not rejected promptly — it appears to have started its own exec " +
			"instead of bouncing off the already-active chat")
	}

	muB.Lock()
	gotBusy := false
	for _, e := range eventsB {
		if e.Type == "agent_busy" {
			gotBusy = true
		}
	}
	muB.Unlock()
	if !gotBusy {
		t.Errorf("expected an agent_busy event for the rejected concurrent sender, got events: %+v", eventsB)
	}

	// Let the winning run finish (with an error, so the test doesn't need a
	// full fake agent CLI turn) and confirm the lock is released after it.
	close(ctr.proceed)
	select {
	case err := <-errA:
		if err == nil || !strings.Contains(err.Error(), "ensure team runtime") {
			t.Fatalf("expected 'ensure team runtime' error from the winning run, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the winning run to finish")
	}

	ctr.mu.Lock()
	callCount := ctr.callCount
	ctr.mu.Unlock()
	if callCount != 1 {
		t.Errorf("expected exactly one EnsureCrewRuntime call (one exec), got %d", callCount)
	}
	if b.runInFlight(chatID) {
		t.Error("expected the per-chat run lock to be released after the winning run finished")
	}
}

// TestHandleChatMessage_ExclusivityReleasedOnImmediateError confirms the
// run lock is released via defer even when the run fails synchronously
// (container unavailable), not just on the happy path.
func TestHandleChatMessage_ExclusivityReleasedOnImmediateError(t *testing.T) {
	resolver := &mockResolver{info: exclusivityChatInfo()}
	b := testBridgeWithContainer(t, resolver, &failContainer{})

	const chatID = "chat-imm-err"
	var events []ws.ChatEvent
	streamFn := func(e ws.ChatEvent) { events = append(events, e) }

	if err := b.HandleChatMessage(context.Background(), "user-1", chatID, "hello", streamFn); err == nil {
		t.Fatal("expected error from RunAgent/container setup")
	}
	if b.runInFlight(chatID) {
		t.Error("expected the run lock to be released after an immediate container error")
	}
}

// TestHandleChatMessage_ExclusivityReleasedOnCancel confirms the run lock
// is released when the caller's context is cancelled mid-run (the
// same-user Stop/cancel flow), not just on completion/error.
func TestHandleChatMessage_ExclusivityReleasedOnCancel(t *testing.T) {
	resolver := &mockResolver{info: exclusivityChatInfo()}
	ctr := &blockingContainer{
		entered: make(chan struct{}),
		proceed: make(chan struct{}),
	}
	b := testBridgeWithContainer(t, resolver, ctr)

	const chatID = "chat-cancel"
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- b.HandleChatMessage(ctx, "user-1", chatID, "hello", func(ws.ChatEvent) {})
	}()

	select {
	case <-ctr.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the run to reach container setup")
	}

	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error after cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the cancelled run to return")
	}

	if b.runInFlight(chatID) {
		t.Error("expected the run lock to be released after cancellation")
	}
}

// TestTryMarkRunStart_PerChatNotGlobal is a regression guard: the run lock
// must be scoped per chat, not a single global lock — claiming the slot for
// one chat must never block a different chat's claim.
func TestTryMarkRunStart_PerChatNotGlobal(t *testing.T) {
	b, _ := testBridge(t, &mockResolver{})

	if !b.tryMarkRunStart("chat-one") {
		t.Fatal("expected to claim chat-one's run slot")
	}
	if !b.tryMarkRunStart("chat-two") {
		t.Fatal("a different chat's claim must not be blocked by chat-one's in-flight run")
	}
	b.markRunEnd("chat-one")
	b.markRunEnd("chat-two")
}

// TestTryMarkRunStart_ExclusiveClaim exercises the atomic check-and-claim
// primitive directly: only one caller can hold the slot for a chat at a
// time, and it becomes claimable again once released.
func TestTryMarkRunStart_ExclusiveClaim(t *testing.T) {
	b, _ := testBridge(t, &mockResolver{})
	const chatID = "chat_try"

	if !b.tryMarkRunStart(chatID) {
		t.Fatal("expected to claim the run slot on an idle chat")
	}
	if b.tryMarkRunStart(chatID) {
		t.Fatal("expected a second claim on the same chat to be rejected while the first is active")
	}
	b.markRunEnd(chatID)
	if !b.tryMarkRunStart(chatID) {
		t.Fatal("expected to reclaim the slot once the prior run ended")
	}
	b.markRunEnd(chatID)
}

// TestTryMarkRunStart_ConcurrentOnlyOneWins stresses the guard under
// genuine concurrency (run with -race): of N goroutines racing to claim the
// same chat's run slot, exactly one must win.
func TestTryMarkRunStart_ConcurrentOnlyOneWins(t *testing.T) {
	b, _ := testBridge(t, &mockResolver{})
	const chatID = "chat_try_conc"
	const n = 50

	var wg sync.WaitGroup
	var wins atomic.Int32
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if b.tryMarkRunStart(chatID) {
				wins.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := wins.Load(); got != 1 {
		t.Fatalf("expected exactly 1 winner among %d concurrent claims, got %d", n, got)
	}
	b.markRunEnd(chatID)
}
