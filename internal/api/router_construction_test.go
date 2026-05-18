package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/ws"
)

// ---------------------------------------------------------------------------
// router.go — NewRouter construction gates, SetVersion / Provisioning /
// SetScheduler / Shutdown lifecycle helpers, the noopEmitter run.* guard,
// Router.Journal() nil fallback, and the keeperWSBroadcaster adapter.
//
// router.go is wired into every server boot, so missing tests on the
// construction-error paths and the noopEmitter run.* invariant mean any
// regression slips into prod silently.
// ---------------------------------------------------------------------------

func TestNewRouter_RequiresNonNilDB(t *testing.T) {
	_, err := NewRouter(nil, "any-secret-32-chars-long-padding!", newTestLogger())
	if err == nil || !strings.Contains(err.Error(), "db is required") {
		t.Errorf("NewRouter(nil db) = %v, want \"db is required\" error", err)
	}
}

func TestNewRouter_RejectsBadJWTSecret(t *testing.T) {
	// auth.NewJWTValidator enforces a minimum secret length; an empty
	// secret must propagate as a clean construction error rather than
	// crashing at first authed request.
	_, err := NewRouter(setupTestDB(t), "", newTestLogger())
	if err == nil {
		t.Error("NewRouter(empty jwt secret) should error")
	}
}

func TestNewRouter_HappyPath_WiresMuxAndOptions(t *testing.T) {
	db := setupTestDB(t)
	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger(),
		WithSocketPath("/tmp/test.sock"),
		WithInternalToken("internal-test-token"),
		WithInternalBaseURL("http://localhost:9999"),
		WithStoragePath("/tmp/storage"),
		WithAllowSignup(true),
	)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	if r == nil {
		t.Fatal("NewRouter returned nil router with no error")
	}
	// Verify options actually landed on the struct (catches accidental
	// rename / field-removal where the setter compiles but no longer
	// targets the right slot).
	if r.socketPath != "/tmp/test.sock" {
		t.Errorf("socketPath = %q", r.socketPath)
	}
	if r.internalToken != "internal-test-token" {
		t.Errorf("internalToken = %q", r.internalToken)
	}
	if !r.allowSignup {
		t.Error("allowSignup = false, want true")
	}
	// Rate-limited mux variants must all be pre-wrapped (production
	// expects them non-nil; ServeHTTP will dispatch through them).
	if r.authRateLimitedMux == nil || r.apiRateLimitedMux == nil || r.credTestRateLimitedMux == nil {
		t.Error("rate-limited mux variants must all be non-nil after NewRouter")
	}
}

func TestRouter_SetVersion_StoresValue(t *testing.T) {
	r, err := NewRouter(setupTestDB(t), "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	r.SetVersion("v0.42.0-test")
	if r.version != "v0.42.0-test" {
		t.Errorf("version = %q, want v0.42.0-test", r.version)
	}
}

func TestRouter_Provisioning_NilBeforeRegistration_NonNilAfter(t *testing.T) {
	// After NewRouter the provisioning handler is wired via registerRoutes,
	// which runs inside the constructor — Provisioning() should be non-nil.
	r, err := NewRouter(setupTestDB(t), "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	if r.Provisioning() == nil {
		t.Error("Provisioning() = nil after NewRouter; chatbridge wiring depends on this")
	}
}

func TestRouter_Journal_NilFallsBackToNoop(t *testing.T) {
	r, err := NewRouter(setupTestDB(t), "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	// No WithJournal opt — r.journal stays nil. Journal() must hand back
	// a usable no-op emitter rather than nil (handlers emit unconditionally).
	em := r.Journal()
	if em == nil {
		t.Fatal("Journal() returned nil; handlers would panic")
	}
	// Non-run entry types pass through silently.
	id, err := em.Emit(context.Background(), journal.Entry{Type: "memory.searched"})
	if err != nil {
		t.Errorf("Emit on non-run entry returned err = %v, want nil", err)
	}
	if id != "noop" {
		t.Errorf("Emit returned id = %q, want \"noop\"", id)
	}
}

func TestRouter_Journal_WrappedEmitterIsReturned(t *testing.T) {
	fake := &fakeJournalEmitter{}
	r, err := NewRouter(setupTestDB(t), "this-is-a-32-char-test-secret-pad", newTestLogger(),
		WithJournal(fake))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	if _, err := r.Journal().Emit(context.Background(), journal.Entry{Type: "memory.searched"}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if fake.calls != 1 {
		t.Errorf("fake.calls = %d, want 1 — WithJournal must route through to the supplied emitter", fake.calls)
	}
}

func TestNoopEmitter_RunEntryReturnsError(t *testing.T) {
	// noopEmitter is the safety net when SetJournal/WithJournal wasn't
	// called. For non-run entries it swallows silently; for run.* entries
	// it MUST loudly return errJournalNotWired so handlers (CreateRun,
	// UpdateRun, runAssignment, peer query) 500 rather than acknowledging
	// a phantom success that's lost forever.
	em := noopEmitter{}
	id, err := em.Emit(context.Background(), journal.Entry{Type: "run.completed"})
	if !errors.Is(err, errJournalNotWired) {
		t.Errorf("Emit on run.* entry err = %v, want errJournalNotWired", err)
	}
	if id != "" {
		t.Errorf("Emit on run.* entry id = %q, want \"\" (no phantom id)", id)
	}

	// Sanity: non-run entries return synthesized id + nil error.
	id, err = em.Emit(context.Background(), journal.Entry{Type: "memory.searched"})
	if err != nil || id != "noop" {
		t.Errorf("Emit on non-run entry = (%q, %v), want (\"noop\", nil)", id, err)
	}

	// If caller provided an ID, Emit echoes it (matches Writer.Emit's
	// "give the caller back their CUID" contract).
	id, err = em.Emit(context.Background(), journal.Entry{ID: "caller-id-7", Type: "memory.searched"})
	if err != nil || id != "caller-id-7" {
		t.Errorf("Emit with caller ID = (%q, %v), want (\"caller-id-7\", nil)", id, err)
	}

	if err := em.Flush(context.Background()); err != nil {
		t.Errorf("Flush returned err = %v, want nil", err)
	}
}

func TestRouter_SetScheduler_CascadesToAgentHandler(t *testing.T) {
	r, err := NewRouter(setupTestDB(t), "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	// The router constructs an AgentHandler internally; SetScheduler on
	// the router must also call SetScheduler on that handler so live
	// schedule updates reach the agent-update path.
	if r.agentHandler == nil {
		t.Fatal("agentHandler nil after NewRouter; SetScheduler cannot cascade")
	}
	updater := &fakeScheduler{}
	r.SetScheduler(updater)
	if r.scheduleUpdater != updater {
		t.Errorf("router.scheduleUpdater not stored")
	}
	if r.agentHandler.scheduleUpdater != updater {
		t.Errorf("agentHandler.scheduleUpdater not set — cascade broken")
	}
}

func TestRouter_SetScheduler_NilAgentHandlerNoPanic(t *testing.T) {
	// Defensive: even if a future refactor leaves agentHandler nil at the
	// moment SetScheduler runs, the call must not panic.
	r := &Router{}
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("SetScheduler on nil agentHandler panicked: %v", rec)
		}
	}()
	r.SetScheduler(&fakeScheduler{})
	if r.scheduleUpdater == nil {
		t.Error("scheduleUpdater not stored on nil-agentHandler path")
	}
}

func TestRouter_ServeHTTP_DispatchesThroughMiddlewareChain(t *testing.T) {
	// Verifies the SecurityHeaders + EnforceOrigin + rate-limit chain
	// wraps ServeHTTP at all (a regression that bypassed it would lose
	// CSRF protection silently). We hit a known route (the auth one is
	// path-mounted at construction); 404 is fine — what matters is the
	// security headers landed.
	r, err := NewRouter(setupTestDB(t), "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	req := httptest.NewRequest("GET", "/some-unknown-path", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	// SecurityHeaders applies X-Content-Type-Options on every response.
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want \"nosniff\" — security-headers middleware must run on all responses", got)
	}
}

func TestRouter_Shutdown_IsIdempotent(t *testing.T) {
	r, err := NewRouter(setupTestDB(t), "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	// Both subhandlers may or may not have started background goroutines
	// (no docker client in test). Multiple Shutdown calls must not panic.
	for i := 0; i < 3; i++ {
		r.Shutdown()
	}
}

// ---- keeperWSBroadcaster ----

func TestKeeperWSBroadcaster_NilHubIsSafe(t *testing.T) {
	// broadcastChannelEvent is nil-safe; the adapter must inherit that
	// safety so a Keeper event firing before the hub is wired doesn't
	// crash the engine.
	b := &keeperWSBroadcaster{hub: nil}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("BroadcastKeeperEvent with nil hub panicked: %v", r)
		}
	}()
	b.BroadcastKeeperEvent("ws-1", map[string]any{"kind": "tool_call"})
}

func TestKeeperWSBroadcaster_WithHub_DoesNotPanic(t *testing.T) {
	hub := ws.NewHub(slog.Default(), nil, ws.NopValidatorForTests, ws.NopSessionsForTests)
	b := &keeperWSBroadcaster{hub: hub}
	// No subscribers means BroadcastChannel returns without sending;
	// the call should still complete cleanly.
	b.BroadcastKeeperEvent("ws-1", map[string]any{"kind": "tool_call"})
}

// ---- noopEmitter run.* paths (additional types) ----

func TestNoopEmitter_AllRunPrefixedTypesError(t *testing.T) {
	// Spot-check several run.* event types — all must return
	// errJournalNotWired (the guard uses strings.HasPrefix).
	em := noopEmitter{}
	for _, evtType := range []journal.EntryType{
		"run.created",
		"run.started",
		"run.completed",
		"run.failed",
		"run.cancelled",
		"run.timeout",
	} {
		if _, err := em.Emit(context.Background(), journal.Entry{Type: evtType}); !errors.Is(err, errJournalNotWired) {
			t.Errorf("Emit(%q) err = %v, want errJournalNotWired", evtType, err)
		}
	}
}

// guardCompileTime is a sanity check that the http.Handler ServeHTTP
// signature matches the interface — would catch a refactor that breaks
// the contract before runtime.
var _ http.Handler = (*Router)(nil)
