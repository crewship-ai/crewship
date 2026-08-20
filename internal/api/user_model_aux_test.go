package api

import (
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/llm"
)

// UserModelAux hands the extractor the CURATOR slot's own per-call budget.
//
// The extractor's own tests cannot reach this: they inject a resolver, so
// a Router that resolved the right provider and then returned a zero
// budget would leave every extraction on the extractor's fallback while
// looking correct from both sides. That is #1601's shape — the slot's
// Timeout field reaching no evaluator — and it is only visible here.
func TestUserModelAux_CarriesTheCuratorSlotBudget(t *testing.T) {
	// ollama, because it builds without an API key — the point here is the
	// budget the Router hands back, not which vendor is behind it.
	aux := llm.DefaultAuxiliaryModels()
	aux.Curator = llm.AuxModel{Provider: "ollama", Model: "m", Timeout: 7 * time.Second}
	r := covRONewRouter(t, WithAuxiliaryModels(aux))

	_, _, budget := r.UserModelAux()
	if budget != 7*time.Second {
		t.Errorf("UserModelAux budget = %v, want the curator slot's 7s", budget)
	}
}

// Lowering the slot's budget is in force on the NEXT call, not the next
// restart — including when the provider itself is unchanged and served
// from the cache, which is the branch a naive memoisation gets wrong.
func TestUserModelAux_BudgetChangeIsLiveWithoutRebuildingTheProvider(t *testing.T) {
	aux := llm.DefaultAuxiliaryModels()
	aux.Curator = llm.AuxModel{Provider: "ollama", Model: "m", Timeout: 30 * time.Second}
	r := covRONewRouter(t, WithAuxiliaryModels(aux))

	p1, m1, b1 := r.UserModelAux()
	if b1 != 30*time.Second {
		t.Fatalf("first budget = %v, want 30s", b1)
	}

	// Same provider and model, lower budget — nothing about the built
	// client changed, so the cache must be reused AND the new number
	// must still come through.
	lowered := aux
	lowered.Curator.Timeout = 3 * time.Second
	r.auxModels = lowered

	p2, m2, b2 := r.UserModelAux()
	if b2 != 3*time.Second {
		t.Errorf("budget after the change = %v, want 3s — a cached provider must not pin the timeout", b2)
	}
	if m2 != m1 {
		t.Errorf("model changed unexpectedly: %q -> %q", m1, m2)
	}
	if p1 != nil && p2 != p1 {
		t.Error("a timeout change rebuilt the provider; it is not part of the client's identity")
	}
}

// ConsolidatorAux is the memory consolidator's half of the curator slot
// (#1695). Until it existed the consolidator built its summariser from
// KEEPER_OLLAMA_URL + KEEPER_MODEL and never read the slot at all, so the
// "Skill review + memory consolidation" label described a subsystem the slot
// did not reach.
func TestConsolidatorAux_ResolvesTheCuratorSlot(t *testing.T) {
	aux := llm.DefaultAuxiliaryModels()
	aux.Curator = llm.AuxModel{Provider: "ollama", Model: "curator-model", Timeout: 11 * time.Second}
	r := covRONewRouter(t, WithAuxiliaryModels(aux))

	p, model, budget := r.ConsolidatorAux()
	if p == nil {
		t.Fatal("no provider for a buildable curator slot")
	}
	if model != "curator-model" {
		t.Errorf("model = %q, want the curator slot's curator-model", model)
	}
	if budget != 11*time.Second {
		t.Errorf("budget = %v, want the curator slot's 11s", budget)
	}
}

// Both curator consumers share one cache, because they resolve one slot: two
// caches would mean two keep-alive'd clients (and for Ollama two model loads)
// for a wiring that is identical by construction.
func TestConsolidatorAux_SharesTheBuiltClientWithTheUserModelSweep(t *testing.T) {
	aux := llm.DefaultAuxiliaryModels()
	aux.Curator = llm.AuxModel{Provider: "ollama", Model: "curator-model", Timeout: 11 * time.Second}
	r := covRONewRouter(t, WithAuxiliaryModels(aux))

	sweep, _, _ := r.UserModelAux()
	consolidation, _, _ := r.ConsolidatorAux()
	if sweep == nil || consolidation == nil {
		t.Fatal("one of the curator consumers got no provider")
	}
	if sweep != consolidation {
		t.Error("the two curator consumers built separate clients for the same slot")
	}
}
