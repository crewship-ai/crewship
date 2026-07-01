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
)

// DefaultAuxiliaryModels returns the MVP-default config: every slot
// on claude-haiku-4-5. PRD §6 F3 specifies per-slot timeouts
// (Keeper 5s for hot path, MemoryHealth 15s for daily sweep);
// reflected here.
func DefaultAuxiliaryModels() AuxiliaryModels {
	haiku := func(timeout time.Duration) AuxModel {
		return AuxModel{Provider: "anthropic", Model: "claude-haiku-4-5", Timeout: timeout}
	}
	return AuxiliaryModels{
		Curator:      haiku(30 * time.Second),
		Keeper:       haiku(5 * time.Second),
		Behavior:     haiku(8 * time.Second),
		MemoryHealth: haiku(15 * time.Second),
		Negative:     haiku(5 * time.Second),
		Fallback:     haiku(10 * time.Second),
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
	default:
		return AuxModel{}, fmt.Errorf("llm: unknown aux slot %q", slot)
	}
	if picked.Provider != "" {
		// Explicit slot wins, but a missing Timeout would let the
		// caller's LLM call run without a deadline (an operator
		// forgetting `timeout:` in YAML shouldn't translate to "no
		// budget at all"). Borrow from Fallback if it has one, else
		// fall back to a sane hard default matching the longest
		// per-slot default in DefaultAuxiliaryModels.
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
