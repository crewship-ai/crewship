package api

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/keepercfg"
)

// Nothing tested the SEAM. The judge profile had a migration, a config store, an
// admin API, a CLI and docs; the gatekeeper had the evidence block, the hard gate
// and the prompt budget; every test exercised one side or the other. Between them
// sat one line that was never written: the router builds the keeper handler and
// never hands it the keepercfg.Store it is already holding.
//
// The result was the worst shape a security control can take. h.judgeCfg was nil
// on every request, so gatherEvidence returned on its first guard and
// promptBudget() returned 0 — while `crewship keeper profile get` cheerfully
// reported `evidence: on, hard_gate: on`. An operator would have configured a
// protection, been told it was active, and had none.
//
// This test asserts the wiring itself, because that is the thing that was
// missing. Building the handler the way production builds it is the only way to
// catch a constructor call that is absent.
func TestRouter_KeeperHandlerReceivesTheJudgeProfile(t *testing.T) {
	db := setupTestDB(t)
	store := keepercfg.New(db, keepercfg.Defaults{})
	if err := store.Load(context.Background()); err != nil {
		t.Fatalf("load judge config: %v", err)
	}

	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger(),
		WithKeeperSettings(store))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	h := r.keeperHandlerForTest()
	if h == nil {
		t.Fatal("router built no keeper handler")
	}
	if h.judgeCfg == nil {
		t.Fatal("keeper handler has no judge profile: evidence, the hard gate and the prompt budget are all inert while the admin API reports them on")
	}
	if h.judgeCfg != store {
		t.Error("keeper handler holds a different store than the router was given")
	}
}

// With no store configured the handler must still work, with every new
// capability off — an instance that predates the profile behaves exactly as it
// did, rather than failing to build.
func TestRouter_KeeperHandlerWithoutAJudgeProfile(t *testing.T) {
	db := setupTestDB(t)
	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	h := r.keeperHandlerForTest()
	if h == nil {
		t.Fatal("router built no keeper handler")
	}
	if h.judgeCfg != nil {
		t.Error("a judge profile appeared from nowhere")
	}
	if got := h.promptBudget(); got != 0 {
		t.Errorf("promptBudget() = %d with no store, want 0", got)
	}
	facts, hardGate, factKeys, inPrompt := h.gatherEvidence(context.Background(), "ws_1", "agt_1", "cred_1")
	if facts != nil || hardGate || factKeys != nil || inPrompt {
		t.Errorf("gatherEvidence with no store = (%v, %v, %v, %v), want all zero", facts, hardGate, factKeys, inPrompt)
	}
}

// The hard gate is a POLICY check, not a prompt feature, and turning off the
// evidence block must not silently turn it off too.
//
// The two settings answer different questions. `evidence` is "how much context
// does my model get" — an operator shrinks it because their judge has a small
// window. `hard_gate` is "refuse a credential the agent is not bound to" — a
// deterministic refusal that never reaches the model at all. Someone trimming
// the prompt has not asked to stop refusing unbound credentials, and
// `keeper profile get` would go on reporting `Hard gate: on` while it did
// nothing. That is the shape this branch's review found nine times.
//
// Gathering for the gate costs one indexed query on agent_credentials, so the
// cheap answer is also the safe one.
func TestGatherEvidence_HardGateSurvivesEvidenceOff(t *testing.T) {
	db := setupTestDB(t)
	store := keepercfg.New(db, keepercfg.Defaults{})
	if err := store.Load(context.Background()); err != nil {
		t.Fatalf("load: %v", err)
	}
	// updated_by is an FK onto users; the actor has to exist.
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('u_test','t@example.com','T')`); err != nil {
		t.Fatalf("seed actor: %v", err)
	}
	off := keepercfg.TriOff
	if _, err := store.Apply(context.Background(), keepercfg.Patch{Evidence: &off}, "u_test"); err != nil {
		t.Fatalf("turn evidence off: %v", err)
	}

	prof := store.Effective().Profile
	if prof.Evidence.Value {
		t.Fatal("evidence did not turn off — the premise of this test")
	}
	if !prof.HardGate.Value {
		t.Fatal("hard gate is not on by default — the premise of this test")
	}

	h := &KeeperHandler{db: db, logger: newTestLogger(), judgeCfg: store}
	_, hardGate, _, inPrompt := h.gatherEvidence(context.Background(), "ws_1", "agt_1", "cred_1")
	if !hardGate {
		t.Error("hard gate was reported off because the evidence BLOCK was off — an operator shrinking the prompt did not ask to stop refusing unbound credentials")
	}
	if inPrompt {
		t.Error("the block was rendered into the prompt anyway — gathering for the gate is not permission to spend the operator's context window")
	}
}
