// PR-C F4 wire-up: build the four Keeper Phase 2 evaluators (skill_review,
// behavior, memory_health, negative_learning) from the PR-B aux-model config
// and hand them to the API router. Constructed once at boot; per-evaluator
// init failures (missing API key, unsupported provider) are logged and the
// matching evaluator is left nil — the API handler returns 503 for nil
// evaluators so partial rollouts have a deterministic surface (graceful
// degradation, not crash on boot).
//
// Lives in internal/server/ (not internal/keeper/) because this is the
// single place that knows about cfg + journal + DB + the API router —
// the keeper packages must stay decoupled from those.
package server

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper/behaviorhook"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/policy"
)

// phase2Evaluators bundles the four evaluators the API router needs. Any
// field may be nil — the corresponding endpoint surfaces 503 in that case.
type phase2Evaluators struct {
	skillReview  *gatekeeper.SkillReviewEvaluator
	behavior     *gatekeeper.BehaviorEvaluator
	memoryHealth *gatekeeper.MemoryHealthEvaluator
	negative     *gatekeeper.NegativeLearningEvaluator
}

// buildPhase2Evaluators resolves each aux slot in `aux` to an LLM provider,
// wraps it with the standard middleware stack (cost ledger + lookout +
// telemetry), constructs a slot-specific Gatekeeper, and from that a
// per-slot evaluator. Each slot is attempted independently: a slot whose
// provider can't be built (e.g. anthropic provider with no
// ANTHROPIC_API_KEY) is logged as warn and left nil. The bundle is
// always returned — partial wiring is intentional, not an error.
//
// The slot → evaluator mapping per PRD §6 F3 / F4:
//
//	SlotCurator      → SkillReviewEvaluator     (F4.1, daily skill audit)
//	SlotBehavior     → BehaviorEvaluator        (F4.2, sampled tool-call monitor)
//	SlotMemoryHealth → MemoryHealthEvaluator    (F4.3, daily memory hygiene)
//	SlotNegative     → NegativeLearningEvaluator (F4.4, failure → lessons.md)
func buildPhase2Evaluators(
	aux llm.AuxiliaryModels,
	j journal.Emitter,
	db *sql.DB,
	logger *slog.Logger,
) phase2Evaluators {
	out := phase2Evaluators{}

	if gk := buildAuxGatekeeper(aux, llm.SlotCurator, j, db, logger); gk != nil {
		out.skillReview = gatekeeper.NewSkillReviewEvaluator(gk, logger)
	} else {
		logger.Warn("keeper: skill_review evaluator unavailable (curator aux slot not configured / API key missing)",
			"impact", "POST /api/v1/keeper/skill-review will return 503")
	}

	if gk := buildAuxGatekeeper(aux, llm.SlotBehavior, j, db, logger); gk != nil {
		out.behavior = gatekeeper.NewBehaviorEvaluator(gk, logger)
	} else {
		logger.Warn("keeper: behavior evaluator unavailable (behavior aux slot not configured / API key missing)",
			"impact", "POST /api/v1/keeper/behavior will return 503; F4.2 sampling hook will no-op")
	}

	if gk := buildAuxGatekeeper(aux, llm.SlotMemoryHealth, j, db, logger); gk != nil {
		out.memoryHealth = gatekeeper.NewMemoryHealthEvaluator(gk, logger)
	} else {
		logger.Warn("keeper: memory_health evaluator unavailable (memory_health aux slot not configured / API key missing)",
			"impact", "POST /api/v1/keeper/memory-health will return 503")
	}

	if gk := buildAuxGatekeeper(aux, llm.SlotNegative, j, db, logger); gk != nil {
		out.negative = gatekeeper.NewNegativeLearningEvaluator(gk, logger)
	} else {
		logger.Warn("keeper: negative_learning evaluator unavailable (negative aux slot not configured / API key missing)",
			"impact", "POST /api/v1/keeper/negative-learning will return 503")
	}

	return out
}

// buildAuxGatekeeper resolves one aux slot and returns a Gatekeeper backed
// by the right LLM provider with the standard middleware chain. Returns
// nil (logged as warn) on any failure: unknown provider, missing API key,
// model unset. The nil return is intentional — buildPhase2Evaluators uses
// it as the "skip this slot" signal so a single mis-configured slot
// doesn't take down the other three.
func buildAuxGatekeeper(
	aux llm.AuxiliaryModels,
	slot llm.Slot,
	j journal.Emitter,
	db *sql.DB,
	logger *slog.Logger,
) *gatekeeper.Gatekeeper {
	model, err := llm.ResolveAux(aux, slot)
	if err != nil {
		logger.Warn("keeper: aux slot resolve failed", "slot", slot, "error", err)
		return nil
	}
	if model.Model == "" {
		logger.Warn("keeper: aux slot has empty model", "slot", slot)
		return nil
	}

	base, perr := buildLLMProvider(model)
	if perr != nil {
		logger.Warn("keeper: aux slot provider build failed",
			"slot", slot, "provider", model.Provider, "error", perr)
		return nil
	}
	wrapped := llm.Middleware(base, j, db)
	return gatekeeper.New(wrapped, model.Model, logger)
}

// buildLLMProvider maps an AuxModel.Provider string to a concrete
// llm.Provider implementation. Closed set today: "anthropic" + "ollama".
// New providers (gemini, openai) require extending this switch in lockstep
// with internal/llm/. Returns an error rather than a silent no-op so
// mis-configuration surfaces as a startup warn line operators can grep.
//
// "anthropic" sources the key from ANTHROPIC_API_KEY (the same env the
// rest of the codebase reads — see internal/chatbridge/resolver_test.go).
// An empty key here is treated as a hard error: NewAnthropic would build
// a provider that 401s on every request, which is strictly worse than
// returning 503 from the endpoint with a clear "not configured" reason.
func buildLLMProvider(m llm.AuxModel) (llm.Provider, error) {
	switch m.Provider {
	case "anthropic":
		key := os.Getenv("ANTHROPIC_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("ANTHROPIC_API_KEY env not set (required for anthropic aux slot %q)", m.Model)
		}
		return llm.NewAnthropic(key), nil
	case "ollama":
		// Ollama aux slot — base URL is the same Keeper Ollama (or a
		// dedicated one). For MVP we accept the same env Keeper uses,
		// falling back to localhost. Production wiring for ollama-backed
		// aux slots is a deferred follow-up; the immediate F4 path is
		// anthropic (PR-B F3 MVP default).
		base := os.Getenv("KEEPER_OLLAMA_URL")
		if base == "" {
			base = "http://localhost:11434"
		}
		return llm.NewOllama(base, m.Model), nil
	default:
		return nil, fmt.Errorf("unsupported aux provider %q (want anthropic|ollama)", m.Provider)
	}
}

// registerBehaviorHook installs the F4.2 behavior monitor as the
// process-wide singleton. No-op when the behavior evaluator wasn't wired
// (e.g. anthropic API key missing) — Hook.MaybeEvaluate handles a nil
// evaluator by returning (nil, false) so callers stay safe.
//
// Called from server.New after the Router is constructed (PolicyResolver
// is lazily initialised on first access; calling it here serialises the
// first init before the orchestrator hot path races on it).
func registerBehaviorHook(
	ev *gatekeeper.BehaviorEvaluator,
	resolver *policy.Resolver,
	logger *slog.Logger,
) {
	if ev == nil || resolver == nil {
		// Explicit no-op log so operators see why the hook is dormant.
		logger.Info("keeper: behaviorhook NOT installed (evaluator or policy resolver nil)",
			"impact", "EventPostToolCall sampling will not run; F4.2 endpoint still serves on POST /api/v1/keeper/behavior")
		return
	}
	behaviorhook.Set(behaviorhook.New(ev, resolver, logger))
	logger.Info("keeper: behaviorhook installed (F4.2 sampling active on tool-call hot path)")
}
