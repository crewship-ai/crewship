package api

import (
	"context"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/keepercfg"
	"github.com/crewship-ai/crewship/internal/llm"
)

// The property this file exists for (#1556): an operator changes the run_summary
// evaluator — or the fallback slot behind it — in the console, and the NEXT run
// verdict uses it. No restart, and no reconstruction of the Router the pipeline
// executors and the internal handler were wired from.
//
// "ollama" is the provider under test because it is the one llm.BuildAuxProviderAt
// can build with no API key in the environment; nothing here dials it.

func auxRouterFor(t *testing.T, dflt llm.AuxiliaryModels) *Router {
	t.Helper()
	db := setupTestDB(t)
	store := keepercfg.NewAuxStore(db, dflt)
	if err := store.Load(context.Background()); err != nil {
		t.Fatalf("load aux store: %v", err)
	}
	return &Router{keeperAuxSettings: store, logger: newTestLogger()}
}

func setAuxSlot(t *testing.T, r *Router, slot, provider, model string) {
	t.Helper()
	if _, err := r.KeeperAuxSettings().Apply(context.Background(), slot,
		keepercfg.AuxPatch{Provider: &provider, Model: &model}, ""); err != nil {
		t.Fatalf("apply %s override: %v", slot, err)
	}
}

func TestRunVerdict_RunSummaryOverrideAppliesWithoutRestart(t *testing.T) {
	r := auxRouterFor(t, llm.AuxiliaryModels{
		RunSummary: llm.AuxModel{Provider: "ollama", Model: "boot-model", Timeout: 15 * time.Second},
		Fallback:   llm.AuxModel{Provider: "ollama", Model: "boot-fallback", Timeout: 10 * time.Second},
	})

	booted, model := r.RunVerdict()
	if booted == nil {
		t.Fatal("no run-summary provider built from the boot-time config")
	}
	if model != "boot-model" {
		t.Fatalf("model at boot = %q, want boot-model", model)
	}

	setAuxSlot(t, r, "run_summary", "ollama", "live-model")

	rebuilt, model := r.RunVerdict()
	if model != "live-model" {
		t.Errorf("model after the override = %q, want live-model — the run_summary slot still needs a restart", model)
	}
	if rebuilt == booted {
		t.Error("the provider was not rebuilt for the new wiring; verdicts would keep calling the old model")
	}
}

// The fallback slot is what llm.ResolveAux reaches when run_summary itself is
// unset, so an override there has to be live for the same reason.
func TestRunVerdict_FallbackOverrideAppliesWithoutRestart(t *testing.T) {
	r := auxRouterFor(t, llm.AuxiliaryModels{
		Fallback: llm.AuxModel{Provider: "ollama", Model: "boot-fallback", Timeout: 10 * time.Second},
	})

	if _, model := r.RunVerdict(); model != "boot-fallback" {
		t.Fatalf("model at boot = %q, want boot-fallback", model)
	}

	setAuxSlot(t, r, "fallback", "ollama", "live-fallback")

	if _, model := r.RunVerdict(); model != "live-fallback" {
		t.Errorf("model after the override = %q, want live-fallback — the fallback slot still needs a restart", model)
	}
}

// Repeated resolution with nothing changed must hand back the SAME provider.
// Building one per call would put a fresh HTTP client — and for Ollama a
// possible cold model load — into every verdict.
func TestRunVerdict_UnchangedWiringReusesTheProvider(t *testing.T) {
	r := auxRouterFor(t, llm.AuxiliaryModels{
		RunSummary: llm.AuxModel{Provider: "ollama", Model: "boot-model", Timeout: 15 * time.Second},
	})

	first, _ := r.RunVerdict()
	second, _ := r.RunVerdict()
	if first == nil || first != second {
		t.Errorf("got a different provider for identical wiring (%v vs %v)", first, second)
	}
}

// A slot that cannot resolve at all is "verdicts are off", not a panic and not
// an error — the same contract the boot-time resolution had.
func TestRunVerdict_UnresolvableSlotIsOff(t *testing.T) {
	r := auxRouterFor(t, llm.AuxiliaryModels{})

	if p, model := r.RunVerdict(); p != nil || model != "" {
		t.Errorf("got (%v, %q), want (nil, \"\") for an unconfigured run_summary slot", p, model)
	}
}
