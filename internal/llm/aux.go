package llm

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// AuxModel describes one auxiliary-task model slot — provider name,
// model identifier, per-call timeout. Persisted in YAML config
// (cfg.auxiliary.<slot>) or via env vars (CREWSHIP_AUX_<SLOT>_*).
type AuxModel struct {
	Provider string        `yaml:"provider"`
	Model    string        `yaml:"model"`
	Timeout  time.Duration `yaml:"timeout"`
}

// AuxiliaryModels carries one slot per high-frequency low-stakes
// subsystem. PRD §6 F3 enumerates the slots; new subsystems that
// want their own dedicated model should add a slot here in lockstep
// with extending the Slot enum and the resolver switch.
//
// MVP defaults (DefaultAuxiliaryModels) put every slot on Anthropic
// claude-haiku-4-5. Local-model support (Ollama / llama.cpp) is a
// documented Phase 2 follow-up; until then F3 features (Keeper,
// F4 evaluators, Curator) require an ANTHROPIC_API_KEY. The "no API
// key required" core moat is a known compromise per PRD §6 F3
// "Known compromise" note.
type AuxiliaryModels struct {
	Curator      AuxModel `yaml:"curator"`       // memory consolidation, skill review (F4.1)
	Keeper       AuxModel `yaml:"keeper"`        // credential gatekeeper evaluator
	Behavior     AuxModel `yaml:"behavior"`      // F4.2 behavior monitor
	MemoryHealth AuxModel `yaml:"memory_health"` // F4.3 memory health evaluator
	Negative     AuxModel `yaml:"negative"`      // F4.4 negative learning evaluator
	RunSummary   AuxModel `yaml:"run_summary"`   // post-run outcome verdict (#1403)
	Fallback     AuxModel `yaml:"fallback"`      // used when a specific slot is empty
}

// Slot is the typed selector for aux-model lookup. Closed set —
// adding a slot requires extending both this enum and the
// ResolveAux switch (compiler can't enforce exhaustiveness, but the
// test matrix in aux_test.go can).
type Slot string

const (
	SlotCurator      Slot = "curator"
	SlotKeeper       Slot = "keeper"
	SlotBehavior     Slot = "behavior"
	SlotMemoryHealth Slot = "memory_health"
	SlotNegative     Slot = "negative"
	SlotRunSummary   Slot = "run_summary"
)

// auxDefaultTimeout is the shipped per-call budget for the slots whose evaluators
// enforce one (curator, behavior, memory_health, negative, and the fallback
// behind them — internal/server/keeper_phase2.go).
//
// It is 20s because that is the bound those calls have ACTUALLY been running
// under: the field reached no evaluator until #1601, so every one of them was
// capped by the gatekeeper's built-in 20s. The PRD's original per-slot numbers
// (behavior 8s, negative 5s) were written for haiku and were never enforced;
// making the field live at those values would have TIGHTENED three slots the
// moment it started working — and the fully-local wiring, where a slot with no
// API key degrades to a 7B judge that takes ~12s, is exactly where that bites.
// That is the #1530 failure (a budget too small for the model, surfacing as a
// fail-closed verdict) re-introduced by the fix for it.
//
// So: the wiring changes nothing for an operator who never configured a budget,
// and the number is theirs to lower — per slot, at runtime, from the Judge models
// card or `crewship keeper aux set <slot> --timeout`.
const auxDefaultTimeout = 20 * time.Second

// DefaultAuxiliaryModels returns the MVP-default config: every slot
// on claude-haiku-4-5, with the per-call budget each evaluator enforces.
//
// Keeper and RunSummary keep their PRD §6 F3 numbers: nothing resolves
// SlotKeeper (see keepercfg.AuxSlots, which deliberately omits it), and the
// run_summary path bounds its call by the caller's context rather than by this
// field — neither is a budget an evaluator reads.
func DefaultAuxiliaryModels() AuxiliaryModels {
	haiku := func(timeout time.Duration) AuxModel {
		return AuxModel{Provider: "anthropic", Model: "claude-haiku-4-5", Timeout: timeout}
	}
	return AuxiliaryModels{
		Curator:      haiku(auxDefaultTimeout),
		Keeper:       haiku(5 * time.Second),
		Behavior:     haiku(auxDefaultTimeout),
		MemoryHealth: haiku(auxDefaultTimeout),
		Negative:     haiku(auxDefaultTimeout),
		RunSummary:   haiku(15 * time.Second),
		Fallback:     haiku(auxDefaultTimeout),
	}
}

// LoadAuxiliaryModels returns the MVP defaults with any
// CREWSHIP_AUX_<SLOT>_{PROVIDER,MODEL,TIMEOUT} environment overrides applied.
// This is the wiring entry point for server bootstrap — operators can point
// individual aux slots at a cheaper (or local) model without a config-file
// redeploy, closing the "documented but unimplemented" gap the struct comment
// promised.
func LoadAuxiliaryModels() AuxiliaryModels {
	return AuxiliaryModelsFromEnv(DefaultAuxiliaryModels(), os.Getenv)
}

// AuxiliaryModelsFromEnv overlays CREWSHIP_AUX_<SLOT>_{PROVIDER,MODEL,TIMEOUT}
// onto base and returns the merged config. SLOT is the upper-cased slot name:
// CURATOR, KEEPER, BEHAVIOR, MEMORY_HEALTH, NEGATIVE, FALLBACK. Only vars that
// are set (non-empty after trim) override; everything else keeps base. TIMEOUT
// takes a Go duration string ("5s", "500ms"); an unparsable or non-positive
// value is IGNORED (base timeout kept) so a typo can never silently strip a
// slot's deadline. getenv is injected for testability (pass os.Getenv in prod).
func AuxiliaryModelsFromEnv(base AuxiliaryModels, getenv func(string) string) AuxiliaryModels {
	slots := map[string]*AuxModel{
		"CURATOR":       &base.Curator,
		"KEEPER":        &base.Keeper,
		"BEHAVIOR":      &base.Behavior,
		"MEMORY_HEALTH": &base.MemoryHealth,
		"NEGATIVE":      &base.Negative,
		"RUN_SUMMARY":   &base.RunSummary,
		"FALLBACK":      &base.Fallback,
	}
	for name, slot := range slots {
		prefix := "CREWSHIP_AUX_" + name + "_"
		if v := strings.TrimSpace(getenv(prefix + "PROVIDER")); v != "" {
			slot.Provider = v
		}
		if v := strings.TrimSpace(getenv(prefix + "MODEL")); v != "" {
			slot.Model = v
		}
		if v := strings.TrimSpace(getenv(prefix + "TIMEOUT")); v != "" {
			if d, err := time.ParseDuration(v); err == nil && d > 0 {
				slot.Timeout = d
			}
		}
	}
	return base
}

// ResolveAux returns the configured AuxModel for slot, falling back
// to cfg.Fallback when the specific slot is unconfigured. Returns
// an error if neither the slot nor Fallback has a Provider set —
// loud error beats silent degradation (PR-Z Z.2 principle).
func ResolveAux(cfg AuxiliaryModels, slot Slot) (AuxModel, error) {
	var picked AuxModel
	switch slot {
	case SlotCurator:
		picked = cfg.Curator
	case SlotKeeper:
		picked = cfg.Keeper
	case SlotBehavior:
		picked = cfg.Behavior
	case SlotMemoryHealth:
		picked = cfg.MemoryHealth
	case SlotNegative:
		picked = cfg.Negative
	case SlotRunSummary:
		picked = cfg.RunSummary
	default:
		return AuxModel{}, fmt.Errorf("llm: unknown aux slot %q", slot)
	}
	if picked.Provider != "" {
		// Explicit slot wins, but a missing Timeout would let the
		// caller's LLM call run without a deadline (an operator
		// forgetting `timeout:` in YAML shouldn't translate to "no
		// budget at all"). Borrow from Fallback if it has one, else
		// fall back to a sane hard default — 30s, deliberately no
		// tighter than any shipped per-slot budget, because this is
		// the branch where nobody stated one.
		if picked.Timeout <= 0 {
			if cfg.Fallback.Timeout > 0 {
				picked.Timeout = cfg.Fallback.Timeout
			} else {
				picked.Timeout = 30 * time.Second
			}
		}
		return picked, nil
	}
	if cfg.Fallback.Provider != "" {
		return cfg.Fallback, nil
	}
	return AuxModel{}, fmt.Errorf(
		"llm: aux slot %q is empty and no Fallback provider configured; "+
			"set cfg.auxiliary.%s.provider+model (or cfg.auxiliary.fallback.*) — F3 MVP defaults to anthropic/claude-haiku-4-5",
		slot, slot,
	)
}
