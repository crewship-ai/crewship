package pipeline

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// ---------------------------------------------------------------------------
// executor.go — With* optional-capability builders.
//
// Each builder follows a fixed contract:
//   - stores the value on the Executor's private field
//   - returns the receiver so callers can chain
//   - accepts nil (sets the field to nil — a documented production
//     fallback to the "feature disabled" code path inside the executor)
//
// These builders are wired at server boot — a regression that swaps the
// target field by name (e.g. WithRunRegistry → e.idempotency = r) compiles
// cleanly and silently disables the wrong feature. The tests below pin
// the field that each builder targets.
// ---------------------------------------------------------------------------

// captureWSBroadcaster is a tiny WSBroadcaster fake. The interface is
// narrow (single method) so a struct with one channel field is enough.
type captureWSBroadcaster struct{ calls int }

func (c *captureWSBroadcaster) BroadcastWorkspace(_, _ string, _ any) { c.calls++ }

// captureWaitpointStore is a tiny WaitpointStore fake — same shape.
type captureWaitpointStore struct{ calls int }

func (c *captureWaitpointStore) CreateApproval(_ context.Context, _ WaitpointApprovalRequest) (string, error) {
	c.calls++
	return "", nil
}
func (c *captureWaitpointStore) WaitFor(_ context.Context, _ string) (bool, error) {
	c.calls++
	return false, nil
}

func newExecutorForOptionsTest(t *testing.T) *Executor {
	t.Helper()
	store, resolver, cleanup := openExecutorTestDB(t)
	t.Cleanup(cleanup)
	return NewExecutor(store, resolver, newMockRunner(), nil)
}

func TestNewExecutor_DefaultsAndChainable(t *testing.T) {
	// NewExecutor with nil emitter must install a no-op emitter (the
	// executor body emits unconditionally; nil would panic).
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	e := NewExecutor(store, resolver, newMockRunner(), nil)
	if e == nil {
		t.Fatal("NewExecutor returned nil")
	}
	if e.emitter == nil {
		t.Fatal("emitter = nil after NewExecutor; ensureEmitter must install nopEmitter")
	}
	if _, err := e.emitter.Emit(context.Background(), journal.Entry{}); err != nil {
		t.Errorf("default emitter Emit returned error: %v", err)
	}
	// Default pipes resolver is the store itself.
	if e.pipes != PipelineResolver(store) {
		t.Errorf("default pipes resolver = %v, want store", e.pipes)
	}
	// Optional capabilities all start nil — the executor's per-step
	// branches degrade gracefully without them. Pin this so a future
	// constructor default doesn't silently turn one on.
	if e.waitpoints != nil || e.ws != nil || e.runs != nil || e.idempotency != nil || e.runStore != nil {
		t.Errorf("optional capabilities non-nil out of the box: waitpoints=%v ws=%v runs=%v idempotency=%v runStore=%v",
			e.waitpoints != nil, e.ws != nil, e.runs != nil, e.idempotency != nil, e.runStore != nil)
	}
}

func TestExecutor_WithWaitpointStore_AssignsFieldAndChains(t *testing.T) {
	e := newExecutorForOptionsTest(t)
	ws := &captureWaitpointStore{}
	got := e.WithWaitpointStore(ws)
	if got != e {
		t.Error("WithWaitpointStore did not return receiver — chain broken")
	}
	if e.waitpoints != ws {
		t.Error("WithWaitpointStore did not store the supplied WaitpointStore")
	}
	// Nil sets the field to nil — production uses this to opt back into
	// the in-memory wait-step path (documented fallback).
	e.WithWaitpointStore(nil)
	if e.waitpoints != nil {
		t.Error("WithWaitpointStore(nil) did not clear the field")
	}
}

func TestExecutor_WithWSBroadcaster_AssignsFieldAndChains(t *testing.T) {
	e := newExecutorForOptionsTest(t)
	b := &captureWSBroadcaster{}
	got := e.WithWSBroadcaster(b)
	if got != e {
		t.Error("WithWSBroadcaster did not return receiver — chain broken")
	}
	if e.ws != b {
		t.Error("WithWSBroadcaster did not store the supplied WSBroadcaster")
	}
	// Sanity: the stored broadcaster actually dispatches when called.
	e.ws.BroadcastWorkspace("ws-1", "pipeline.run.started", nil)
	if b.calls != 1 {
		t.Errorf("broadcaster received %d calls, want 1", b.calls)
	}
	e.WithWSBroadcaster(nil)
	if e.ws != nil {
		t.Error("WithWSBroadcaster(nil) did not clear the field")
	}
}

func TestExecutor_WithRunRegistry_AssignsFieldAndChains(t *testing.T) {
	e := newExecutorForOptionsTest(t)
	reg := NewRunRegistry()
	got := e.WithRunRegistry(reg)
	if got != e {
		t.Error("WithRunRegistry did not return receiver — chain broken")
	}
	if e.runs != reg {
		t.Error("WithRunRegistry did not store the supplied registry")
	}
	e.WithRunRegistry(nil)
	if e.runs != nil {
		t.Error("WithRunRegistry(nil) did not clear the field")
	}
}

func TestExecutor_WithIdempotencyStore_AssignsFieldAndChains(t *testing.T) {
	e := newExecutorForOptionsTest(t)
	idem := NewIdempotencyStore(nil) // nil DB is fine — we only check field wiring
	got := e.WithIdempotencyStore(idem)
	if got != e {
		t.Error("WithIdempotencyStore did not return receiver — chain broken")
	}
	if e.idempotency != idem {
		t.Error("WithIdempotencyStore did not store the supplied store")
	}
	e.WithIdempotencyStore(nil)
	if e.idempotency != nil {
		t.Error("WithIdempotencyStore(nil) did not clear the field")
	}
}

func TestExecutor_WithRunStore_AssignsFieldAndChains(t *testing.T) {
	e := newExecutorForOptionsTest(t)
	rs := NewRunStore(nil) // nil DB is fine — only checking the wiring
	got := e.WithRunStore(rs)
	if got != e {
		t.Error("WithRunStore did not return receiver — chain broken")
	}
	if e.runStore != rs {
		t.Error("WithRunStore did not store the supplied store")
	}
	e.WithRunStore(nil)
	if e.runStore != nil {
		t.Error("WithRunStore(nil) did not clear the field")
	}
}

func TestExecutor_BuilderChain_AllOptionalsTogether(t *testing.T) {
	// The doc comment for WithEgressGate prescribes the chain:
	//   NewExecutor + WithEgressGate + WithCredentialResolver + ...
	// Verify the whole chain composes — landing on a single, fully
	// configured Executor instance.
	e := newExecutorForOptionsTest(t)

	gate := func(string) bool { return true }
	resolver := func(_ context.Context, _ string) (string, error) { return "", nil }
	ws := &captureWaitpointStore{}
	wsb := &captureWSBroadcaster{}
	reg := NewRunRegistry()
	idem := NewIdempotencyStore(nil)
	rs := NewRunStore(nil)

	chained := e.
		WithEgressGate(gate).
		WithCredentialResolver(resolver).
		WithWaitpointStore(ws).
		WithWSBroadcaster(wsb).
		WithRunRegistry(reg).
		WithIdempotencyStore(idem).
		WithRunStore(rs)

	if chained != e {
		t.Fatal("chain returned a different *Executor — builder identity broken")
	}
	if e.egressAllowed == nil || e.credentialByType == nil {
		t.Error("egress / credential resolvers not stored through chain")
	}
	if e.waitpoints != ws {
		t.Error("waitpoints not stored through chain")
	}
	if e.ws != wsb {
		t.Error("ws broadcaster not stored through chain")
	}
	if e.runs != reg {
		t.Error("run registry not stored through chain")
	}
	if e.idempotency != idem {
		t.Error("idempotency store not stored through chain")
	}
	if e.runStore != rs {
		t.Error("run store not stored through chain")
	}
}

// guardCompileTime pins the WSBroadcaster + WaitpointStore interface
// shapes — would catch a refactor that breaks the contract before
// runtime so the tests above don't have to chase signature drift.
var (
	_ WSBroadcaster  = (*captureWSBroadcaster)(nil)
	_ WaitpointStore = (*captureWaitpointStore)(nil)
)
