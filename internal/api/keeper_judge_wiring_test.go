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
	facts, hardGate, factKeys := h.gatherEvidence(context.Background(), "agt_1", "cred_1")
	if facts != nil || hardGate || factKeys != nil {
		t.Errorf("gatherEvidence with no store = (%v, %v, %v), want (nil, false, nil)", facts, hardGate, factKeys)
	}
}
