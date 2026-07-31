package server

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/keepercfg"
	"github.com/crewship-ai/crewship/internal/llm"
)

// The property this file exists for: the per-slot budget an operator sets on the
// Judge models card (or with `crewship keeper aux set <slot> --timeout`) is the
// one the evaluator's model call is bounded by.
//
// It was storage only. keeper_aux_settings.timeout_ms was settable, validated,
// rendered with its provenance — and the evaluators were built with no
// WithCallTimeout at all, so every call ran under the gatekeeper's built-in 20s
// constant whatever the card said. A control that visibly does nothing costs a
// debugging session before anyone doubts the field (#1601).

// The budget reaches the call, and an edit reaches the NEXT call: these
// evaluators are built once at boot and their pointers are held by the route
// handler, so a value captured at construction would be the boot-time one
// forever — the same trap the slot's model fell into before #1556.
func TestBuildAuxGatekeeper_UsesTheSlotBudgetAndFollowsAnEdit(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "") // build falls back to the local default judge
	ctx := context.Background()
	store := auxStoreFor(t)
	slot := string(llm.SlotBehavior)

	gk := buildAuxGatekeeper(llm.DefaultAuxiliaryModels(), llm.SlotBehavior, nil,
		auxCallTimeout(store, slot), "http://localhost:11434", "qwen2.5:3b-instruct",
		nil, nil, slog.Default())
	if gk == nil {
		t.Fatal("no gatekeeper built")
	}

	// Nothing configured: the slot's inherited budget, which is what the card
	// shows and what the call must run under.
	want := time.Duration(store.EffectiveSlot(slot).TimeoutMS.Value) * time.Millisecond
	if want <= 0 {
		t.Fatalf("the shipped default for %s has no budget to assert on", slot)
	}
	if got := gk.CallTimeout(); got != want {
		t.Errorf("CallTimeout = %s, want the slot's inherited %s", got, want)
	}

	// The operator watches a hosted evaluator time out and raises the budget.
	forty := int64(40000)
	if _, err := store.Apply(ctx, slot, keepercfg.AuxPatch{TimeoutMS: &forty}, ""); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := gk.CallTimeout(); got != 40*time.Second {
		t.Errorf("CallTimeout = %s after the edit, want 40s — the setting did not reach the call", got)
	}

	// And clearing it goes back to the inherited budget, not to "no budget".
	zero := int64(0)
	if _, err := store.Apply(ctx, slot, keepercfg.AuxPatch{TimeoutMS: &zero}, ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := gk.CallTimeout(); got != want {
		t.Errorf("CallTimeout = %s after the clear, want the inherited %s", got, want)
	}
}

// A slot with no budget of its own resolves through the fallback slot — the same
// place llm.ResolveAux reaches for its provider and model, so the budget must not
// come from somewhere else.
func TestAuxCallTimeout_UnsetSlotTakesTheFallbackBudget(t *testing.T) {
	ctx := context.Background()
	// Behavior deliberately has neither provider nor timeout: the shape in which
	// llm.ResolveAux resolves the slot through Fallback.
	store := auxStoreWith(t, llm.AuxiliaryModels{
		Fallback: llm.AuxModel{Provider: "ollama", Model: "boot-fallback", Timeout: 12 * time.Second},
	})
	budget := auxCallTimeout(store, string(llm.SlotBehavior))
	if budget == nil {
		t.Fatal("no resolver for a live store")
	}

	if got := budget(); got != 12*time.Second {
		t.Errorf("budget = %s, want the fallback slot's 12s", got)
	}

	// An override on the fallback slot is live for the same reason its model is.
	thirtyThree := int64(33000)
	if _, err := store.Apply(ctx, keepercfg.SlotFallback,
		keepercfg.AuxPatch{TimeoutMS: &thirtyThree}, ""); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := budget(); got != 33*time.Second {
		t.Errorf("budget = %s, want the fallback override's 33s", got)
	}

	// The slot's own budget is the more specific one and still wins.
	seven := int64(7000)
	if _, err := store.Apply(ctx, string(llm.SlotBehavior),
		keepercfg.AuxPatch{TimeoutMS: &seven}, ""); err != nil {
		t.Fatalf("apply slot: %v", err)
	}
	if got := budget(); got != 7*time.Second {
		t.Errorf("budget = %s, want the slot's own 7s", got)
	}
}

// No store (test and embedded wirings) means no resolver, and the gatekeeper
// keeps its built-in bound — an unbounded model call is the failure audit M4
// added the timeout for.
func TestAuxCallTimeout_NilStoreKeepsTheBuiltInBound(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	if budget := auxCallTimeout(nil, string(llm.SlotBehavior)); budget != nil {
		t.Errorf("got a resolver for a nil store: %s", budget())
	}

	gk := buildAuxGatekeeper(llm.DefaultAuxiliaryModels(), llm.SlotBehavior, nil, nil,
		"http://localhost:11434", "qwen2.5:3b-instruct", nil, nil, slog.Default())
	if gk == nil {
		t.Fatal("no gatekeeper built")
	}
	if got := gk.CallTimeout(); got != 20*time.Second {
		t.Errorf("CallTimeout = %s, want the 20s built-in bound", got)
	}
}
