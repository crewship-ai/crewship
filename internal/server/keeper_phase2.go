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
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper/behaviorhook"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
	"github.com/crewship-ai/crewship/internal/keepercfg"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/policy"
)

// watchSpecResolver builds the gatekeeper.WatchSpecResolver every Gatekeeper is
// wired with (M1, issue #1001). It compiles each workspace's admin-authored
// watch spec (presets + free-form rules) into the prompt block the evaluators
// inject. Read on the hot eval path; governance.Resolve never errors (fail-safe:
// an unconfigured or unreadable workspace yields "" ⇒ no watch block, the
// built-in anti-pattern list stays in force).
func watchSpecResolver(db *sql.DB, logger *slog.Logger) gatekeeper.WatchSpecResolver {
	return func(ctx context.Context, workspaceID string) string {
		// ResolveWatchBlock gates on Settings.Enabled, so a merely-authored spec
		// stays inert (no injection into the always-on access evaluator) until an
		// OWNER/ADMIN enables the watchdog — the opt-in contract the CLI/docs promise.
		return governance.ResolveWatchBlock(governance.Resolve(ctx, db, logger, workspaceID))
	}
}

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
// provider can't be built and has no local default judge to fall back to is
// logged as warn and left nil. The bundle is always returned — partial
// wiring is intentional, not an error.
//
// govModel is the per-workspace governance-model resolver (M2a/M2b, #1001) —
// the SAME resolver the access gatekeeper is wired with. Passing it here makes
// the vault-backed gov-model setting govern the behavior + F4 evaluators at
// request time, not just the credential-access judge. dfltOllamaURL/Model are
// the server's local default judge (cfg.Keeper.*): when a slot's configured
// provider can't be built at boot (e.g. anthropic default with no
// ANTHROPIC_API_KEY), the evaluator falls back to that local judge instead of
// being disabled — which is what lets governance run fully-local with no key.
//
// The slot → evaluator mapping per PRD §6 F3 / F4:
//
//	SlotCurator      → SkillReviewEvaluator     (F4.1, daily skill audit)
//	SlotBehavior     → BehaviorEvaluator        (F4.2, sampled tool-call monitor)
//	SlotMemoryHealth → MemoryHealthEvaluator    (F4.3, daily memory hygiene)
//	SlotNegative     → NegativeLearningEvaluator (F4.4, failure → lessons.md)
//
// auxSettings is the runtime per-slot override store (nil in test/embedded
// wirings, which then behave exactly as before): each evaluator's gov-model
// resolver AND its per-call budget are wrapped so an override applies at request
// time instead of requiring these evaluators to be rebuilt — see
// keeper_aux_live.go. judge resolves the current instance judge endpoint for a
// slot pointed at "ollama",
// and creds resolves the vault key a hosted slot spends (#1554; nil = the
// pre-existing process-env key).
func buildPhase2Evaluators(
	aux llm.AuxiliaryModels,
	govModel gatekeeper.GovModelResolver,
	dfltOllamaURL, dfltOllamaModel string,
	auxSettings *keepercfg.AuxStore,
	judge func() (string, string),
	creds keepercfg.AuxCredentialLookup,
	j journal.Emitter,
	db *sql.DB,
	logger *slog.Logger,
) phase2Evaluators {
	out := phase2Evaluators{}

	live := func(slot llm.Slot) gatekeeper.GovModelResolver {
		return newAuxLiveResolver(string(slot), auxSettings, govModel, judge, creds, j, db, logger)
	}
	// The slot's configured per-call budget, read at call time for the same
	// reason its model is (#1601): these evaluators are never rebuilt.
	budget := func(slot llm.Slot) gatekeeper.CallTimeoutResolver {
		return auxCallTimeout(auxSettings, string(slot))
	}

	if gk := buildAuxGatekeeper(aux, llm.SlotCurator, live(llm.SlotCurator), budget(llm.SlotCurator), dfltOllamaURL, dfltOllamaModel, j, db, logger); gk != nil {
		out.skillReview = gatekeeper.NewSkillReviewEvaluator(gk, logger)
	} else {
		logger.Warn("keeper: skill_review evaluator unavailable (curator aux slot not configured and no local default judge)",
			"impact", "POST /api/v1/keeper/skill-review will return 503")
	}

	if gk := buildAuxGatekeeper(aux, llm.SlotBehavior, live(llm.SlotBehavior), budget(llm.SlotBehavior), dfltOllamaURL, dfltOllamaModel, j, db, logger); gk != nil {
		out.behavior = gatekeeper.NewBehaviorEvaluator(gk, logger)
	} else {
		logger.Warn("keeper: behavior evaluator unavailable (behavior aux slot not configured and no local default judge)",
			"impact", "POST /api/v1/keeper/behavior will return 503; F4.2 sampling hook will no-op")
	}

	if gk := buildAuxGatekeeper(aux, llm.SlotMemoryHealth, live(llm.SlotMemoryHealth), budget(llm.SlotMemoryHealth), dfltOllamaURL, dfltOllamaModel, j, db, logger); gk != nil {
		out.memoryHealth = gatekeeper.NewMemoryHealthEvaluator(gk, logger)
	} else {
		logger.Warn("keeper: memory_health evaluator unavailable (memory_health aux slot not configured and no local default judge)",
			"impact", "POST /api/v1/keeper/memory-health will return 503")
	}

	if gk := buildAuxGatekeeper(aux, llm.SlotNegative, live(llm.SlotNegative), budget(llm.SlotNegative), dfltOllamaURL, dfltOllamaModel, j, db, logger); gk != nil {
		out.negative = gatekeeper.NewNegativeLearningEvaluator(gk, logger)
	} else {
		logger.Warn("keeper: negative_learning evaluator unavailable (negative aux slot not configured and no local default judge)",
			"impact", "POST /api/v1/keeper/negative-learning will return 503")
	}

	return out
}

// buildAuxGatekeeper resolves one aux slot and returns a Gatekeeper backed by
// the right LLM provider with the standard middleware chain. It is wired with
// two per-workspace seams identical to the access gatekeeper: the watch-spec
// resolver (M1) and the gov-model resolver (M2a) — so a workspace's vault-backed
// governance model overrides this slot's construction-time default at request
// time.
//
// budget is the slot's per-call deadline, read at call time (#1601). nil keeps
// the gatekeeper's built-in bound, which is what test and embedded wirings get.
//
// Construction-time provider selection (fully-local, M2, #1001):
//   - Build the slot's configured provider (DefaultAuxiliaryModels puts every
//     slot on anthropic; env overrides may repoint it).
//   - If that can't be built at boot — the common case being an anthropic slot
//     with no ANTHROPIC_API_KEY — fall back to the server's local default judge
//     (dfltOllamaURL/Model, i.e. cfg.Keeper.*), mirroring the access gatekeeper
//     which is likewise always constructed on the local Ollama base. This keeps
//     the F4 evaluators alive with zero API key; a configured gov-model still
//     overrides per request via the resolver.
//   - Only when there is neither a buildable provider NOR a local default judge
//     is nil returned (logged as warn) — the "skip this slot" signal so one
//     mis-configured slot doesn't take down the other three.
func buildAuxGatekeeper(
	aux llm.AuxiliaryModels,
	slot llm.Slot,
	govModel gatekeeper.GovModelResolver,
	budget gatekeeper.CallTimeoutResolver,
	dfltOllamaURL, dfltOllamaModel string,
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
	modelName := model.Model
	if perr != nil {
		// Configured provider can't be built at boot. Fall back to the local
		// default judge so the evaluator runs fully-local (no API key) instead
		// of going dark. A per-workspace gov-model setting still overrides this.
		if dfltOllamaURL == "" || dfltOllamaModel == "" {
			logger.Warn("keeper: aux slot provider build failed and no local default judge configured",
				"slot", slot, "provider", model.Provider, "error", perr)
			return nil
		}
		logger.Info("keeper: aux slot falling back to the local default judge",
			"slot", slot, "configured_provider", model.Provider, "reason", perr.Error(),
			"fallback_model", dfltOllamaModel)
		base = llm.NewOllama(dfltOllamaURL, dfltOllamaModel)
		modelName = dfltOllamaModel
	}

	wrapped := llm.Middleware(base, j, db)
	return gatekeeper.New(wrapped, modelName, logger,
		gatekeeper.WithWatchSpecResolver(watchSpecResolver(db, logger)),
		gatekeeper.WithGovModelResolver(govModel),
		// The operator's per-slot budget, not the gatekeeper's built-in constant.
		// A resolver rather than a captured value because this evaluator is built
		// once at boot and never rebuilt (#1601); a nil one keeps the built-in
		// bound, so nothing is ever unbounded.
		gatekeeper.WithCallTimeoutResolver(budget),
		// …and the command that raises THIS budget. The default names the
		// credential judge's setting, which for an evaluator would send the
		// operator to change a number governing a different model.
		gatekeeper.WithTimeoutRemedy(
			fmt.Sprintf("crewship keeper aux set %s --timeout 40s", slot)))
}

// buildLLMProvider maps an AuxModel.Provider string to a concrete
// llm.Provider implementation. Thin wrapper over llm.BuildAuxProvider
// (shared with internal/api's post-run verdict wiring, #1403) kept so
// call sites in this file don't need an `llm.` qualifier rename.
func buildLLMProvider(m llm.AuxModel) (llm.Provider, error) {
	return llm.BuildAuxProvider(m)
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
