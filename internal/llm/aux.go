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
// The SHIPPED defaults (DefaultAuxiliaryModels) put every slot on the
// first row of the provider registry that names a DefaultAuxModel —
// anthropic/claude-haiku-4-5 today. What an instance actually boots
// with is LoadAuxiliaryModels, which retargets that at the first
// registered provider whose credential is present, so an operator
// holding only an OPENAI_API_KEY gets working evaluators instead of
// six slots that fail at first use. Neither path invents a key: with
// no credential at all the slots keep the shipped default and the
// builder still errors loudly, naming the env var to set.
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

// auxDefaultTimeout is the shipped per-call budget for the slots whose callers
// enforce one: the four Keeper Reviews evaluators (curator, behavior,
// memory_health, negative — internal/server/keeper_phase2.go), the post-run
// verdict (run_summary — internal/runverdict, #1615), and the fallback behind
// them all.
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

// auxDefaultSpec returns the registry row an unconfigured slot points at: the
// first provider in DECLARATION order that names a DefaultAuxModel and whose
// credential requirement getenv can satisfy.
//
// getenv == nil means "ignore credentials" — the SHIPPED answer, which is what
// DefaultAuxiliaryModels wants and what keepercfg compares against to tell a
// value we shipped apart from one the operator's environment chose.
//
// A row with no DefaultAuxModel is skipped, credential or not. That is the
// ollama row: it needs no key, but the model id is whatever the operator
// pulled, and a slot naming the empty model would build a provider that fails
// on its first request — strictly worse than the shipped default plus the
// builder's "ANTHROPIC_API_KEY env not set" error, which at least names the fix.
//
// The key is trimmed before it counts. BuildAuxProviderWithKey tests the raw
// value, so a whitespace-only key is "set" there and would 401 on first use;
// treating it as absent here keeps the default on a provider whose failure mode
// is a startup line rather than a live 401.
func auxDefaultSpec(getenv func(string) string) (ProviderSpec, bool) {
	for _, spec := range RegisteredProviderSpecs() {
		if spec.DefaultAuxModel == "" {
			continue
		}
		if getenv != nil && spec.KeyEnv != "" && strings.TrimSpace(getenv(spec.KeyEnv)) == "" {
			continue
		}
		return spec, true
	}
	return ProviderSpec{}, false
}

// auxiliaryModelsOn puts every slot on one provider/model, with the per-call
// budget each evaluator enforces.
//
// Keeper alone keeps its PRD §6 F3 number, because nothing resolves SlotKeeper
// (see keepercfg.AuxSlots, which deliberately omits it) — it is the one budget
// here that no call site reads.
//
// RunSummary used to be the second such number, at the PRD's 15s. #1615 made the
// field live on that path, and it takes auxDefaultTimeout for exactly the reason
// given there: the verdict call had been running under the caller's context (a
// background one at both call sites), so shipping 15s as its first real deadline
// would TIGHTEN it — and the fully-local wiring, where the slot degrades to a 7B
// judge, is where that bites. The operator's own number is still whatever they
// set. Carried across this branch's refactor into auxiliaryModelsOn: the budgets
// deliberately do not move with the provider.
//
// The budgets do not move with the provider. auxDefaultTimeout is already sized
// for a slow local judge (see its comment), so re-deriving it per provider would
// only re-introduce the #1530 failure it was widened to fix.
func auxiliaryModelsOn(provider, model string) AuxiliaryModels {
	slot := func(timeout time.Duration) AuxModel {
		return AuxModel{Provider: provider, Model: model, Timeout: timeout}
	}
	return AuxiliaryModels{
		Curator:      slot(auxDefaultTimeout),
		Keeper:       slot(5 * time.Second),
		Behavior:     slot(auxDefaultTimeout),
		MemoryHealth: slot(auxDefaultTimeout),
		Negative:     slot(auxDefaultTimeout),
		RunSummary:   slot(auxDefaultTimeout),
		Fallback:     slot(auxDefaultTimeout),
	}
}

// DefaultAuxiliaryModels returns the SHIPPED config: every slot on the first
// registry row that names a DefaultAuxModel — anthropic/claude-haiku-4-5 — with
// the per-call budget each evaluator enforces.
//
// Deliberately environment-independent, which is the whole reason it is a
// separate function from LoadAuxiliaryModels. keepercfg.AuxStore keeps this
// value as its `builtin` layer purely to tell "we shipped this" apart from "the
// operator's environment selected this" (keepercfg.pickAux); making it read the
// environment would collapse that distinction exactly where it matters — the
// console would report a slot the operator's OPENAI_API_KEY moved as a default
// nobody chose.
//
// It reads the registry rather than repeating the literal so the shipped model
// and ProviderSpec.DefaultAuxModel cannot drift apart. If no row names a
// DefaultAuxModel at all the slots come back empty and ResolveAux errors — loud,
// per PR-Z Z.2, rather than a hidden third copy of "anthropic".
func DefaultAuxiliaryModels() AuxiliaryModels {
	spec, _ := auxDefaultSpec(nil)
	return auxiliaryModelsOn(spec.ID, spec.DefaultAuxModel)
}

// AvailableAuxiliaryModels returns the defaults retargeted at the first
// registered provider whose credential is actually present in getenv.
//
// An instance whose operator holds only an OPENAI_API_KEY used to get six
// evaluator slots hardcoded to anthropic, each failing at first use with
// "ANTHROPIC_API_KEY env not set" — a key they have no reason to own. The
// registry knows which env var each provider reads (ProviderSpec.KeyEnv) and
// which model to name (DefaultAuxModel), so "the provider this instance can
// actually reach" is answerable from the table instead of from a literal.
//
// Declaration order decides ties, the same order the console's picker renders:
// an instance holding both keys keeps the shipped anthropic default, so adding
// a second key never silently repoints a working evaluator.
//
// With no credential for any row this returns the shipped default unchanged —
// it does not fall through to a keyless provider it cannot name a model for,
// and it does not degrade to a slot that resolves and then 401s. The operator
// still gets the builder's loud error, naming the env var to set.
//
// getenv is injected for testability (pass os.Getenv in prod), matching
// AuxiliaryModelsFromEnv.
func AvailableAuxiliaryModels(getenv func(string) string) AuxiliaryModels {
	if spec, ok := auxDefaultSpec(getenv); ok {
		return auxiliaryModelsOn(spec.ID, spec.DefaultAuxModel)
	}
	return DefaultAuxiliaryModels()
}

// LoadAuxiliaryModels returns the defaults for the providers this instance has
// credentials for, with any CREWSHIP_AUX_<SLOT>_{PROVIDER,MODEL,TIMEOUT}
// environment overrides applied on top. This is the wiring entry point for
// server bootstrap — operators can point individual aux slots at a cheaper (or
// local) model without a config-file redeploy, closing the "documented but
// unimplemented" gap the struct comment promised.
//
// Order matters: availability picks the base, the explicit CREWSHIP_AUX_* values
// win over it. An operator who named a provider has already answered the
// question this function's first half guesses at.
func LoadAuxiliaryModels() AuxiliaryModels {
	return AuxiliaryModelsFromEnv(AvailableAuxiliaryModels(os.Getenv), os.Getenv)
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
