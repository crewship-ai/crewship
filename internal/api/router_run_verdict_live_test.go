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

	booted, model, _ := r.RunVerdict()
	if booted == nil {
		t.Fatal("no run-summary provider built from the boot-time config")
	}
	if model != "boot-model" {
		t.Fatalf("model at boot = %q, want boot-model", model)
	}

	setAuxSlot(t, r, "run_summary", "ollama", "live-model")

	rebuilt, model, _ := r.RunVerdict()
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

	if _, model, _ := r.RunVerdict(); model != "boot-fallback" {
		t.Fatalf("model at boot = %q, want boot-fallback", model)
	}

	setAuxSlot(t, r, "fallback", "ollama", "live-fallback")

	if _, model, _ := r.RunVerdict(); model != "live-fallback" {
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

	first, _, _ := r.RunVerdict()
	second, _, _ := r.RunVerdict()
	if first == nil || first != second {
		t.Errorf("got a different provider for identical wiring (%v vs %v)", first, second)
	}
}

// A slot that cannot resolve at all is "verdicts are off", not a panic and not
// an error — the same contract the boot-time resolution had.
func TestRunVerdict_UnresolvableSlotIsOff(t *testing.T) {
	r := auxRouterFor(t, llm.AuxiliaryModels{})

	if p, model, budget := r.RunVerdict(); p != nil || model != "" || budget != 0 {
		t.Errorf("got (%v, %q, %s), want (nil, \"\", 0) for an unconfigured run_summary slot", p, model, budget)
	}
}

// #1615: the third return is the slot's per-call budget, resolved on the same
// terms as its model. Until this landed RunVerdict resolved the budget and
// discarded it, so run_summary was the one aux slot whose timeout column was
// settable, validated and rendered beside four working ones — and read by
// nothing.
func TestRunVerdict_CarriesTheRunSummaryBudget(t *testing.T) {
	cases := []struct {
		name string
		dflt llm.AuxiliaryModels
		// override, when non-empty, is a runtime timeout the operator sets on
		// (slot, ms) — the keeper_aux_settings row the Judge models card writes.
		overrideSlot string
		overrideMS   int64
		want         time.Duration
	}{
		{
			name: "the slot's own configured budget",
			dflt: llm.AuxiliaryModels{
				RunSummary: llm.AuxModel{Provider: "ollama", Model: "m", Timeout: 9 * time.Second},
				Fallback:   llm.AuxModel{Provider: "ollama", Model: "f", Timeout: 12 * time.Second},
			},
			want: 9 * time.Second,
		},
		{
			name: "inherited from the fallback slot when run_summary has none",
			dflt: llm.AuxiliaryModels{
				Fallback: llm.AuxModel{Provider: "ollama", Model: "f", Timeout: 12 * time.Second},
			},
			want: 12 * time.Second,
		},
		{
			name: "the operator's override on the slot",
			dflt: llm.AuxiliaryModels{
				RunSummary: llm.AuxModel{Provider: "ollama", Model: "m", Timeout: 9 * time.Second},
			},
			overrideSlot: "run_summary",
			overrideMS:   40000,
			want:         40 * time.Second,
		},
		{
			name: "the operator's override on the fallback slot behind it",
			dflt: llm.AuxiliaryModels{
				Fallback: llm.AuxModel{Provider: "ollama", Model: "f", Timeout: 12 * time.Second},
			},
			overrideSlot: keepercfg.SlotFallback,
			overrideMS:   33000,
			want:         33 * time.Second,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := auxRouterFor(t, tc.dflt)
			if tc.overrideSlot != "" {
				ms := tc.overrideMS
				if _, err := r.KeeperAuxSettings().Apply(context.Background(), tc.overrideSlot,
					keepercfg.AuxPatch{TimeoutMS: &ms}, ""); err != nil {
					t.Fatalf("apply %s budget: %v", tc.overrideSlot, err)
				}
			}

			p, _, budget := r.RunVerdict()
			if p == nil {
				t.Fatal("no run-summary provider built")
			}
			if budget != tc.want {
				t.Errorf("RunVerdict budget = %s, want %s", budget, tc.want)
			}
		})
	}
}

// Lowering the budget is in force on the NEXT verdict, not the next restart —
// including when nothing about the wiring changed and the provider is served
// from the cache. That is the branch a provider-shaped memoisation gets wrong,
// and it is why the timeout is deliberately not part of the fingerprint.
func TestRunVerdict_BudgetChangeIsLiveWithoutRebuildingTheProvider(t *testing.T) {
	r := auxRouterFor(t, llm.AuxiliaryModels{
		RunSummary: llm.AuxModel{Provider: "ollama", Model: "m", Timeout: 30 * time.Second},
	})

	first, _, b1 := r.RunVerdict()
	if b1 != 30*time.Second {
		t.Fatalf("first budget = %s, want 30s", b1)
	}

	three := int64(3000)
	if _, err := r.KeeperAuxSettings().Apply(context.Background(), "run_summary",
		keepercfg.AuxPatch{TimeoutMS: &three}, ""); err != nil {
		t.Fatalf("apply: %v", err)
	}

	second, _, b2 := r.RunVerdict()
	if b2 != 3*time.Second {
		t.Errorf("budget after the edit = %s, want 3s — a cached provider must not pin the timeout", b2)
	}
	if first != nil && second != first {
		t.Error("a timeout change rebuilt the provider; it is not part of the client's identity")
	}
}
