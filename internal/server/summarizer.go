package server

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/llm"
)

// llmSummarizer adapts an llm.Provider to consolidate.SummarizerClient so
// the memory consolidation worker can extract semantic rules from journal
// entries via whatever LLM the workspace has configured. The adapter is
// deliberately thin — it exists only to rebind the Summarize(prompt)
// single-argument contract to llm.Provider.Complete's richer Request
// struct, picking a sensible default model when the caller doesn't care.
//
// Kept in the server package rather than inside consolidate/ because the
// model string choice is deployment-dependent (local Ollama vs. cloud
// Anthropic) and the consolidate package stays provider-neutral.
type llmSummarizer struct {
	provider llm.Provider
	model    string
}

func newLLMSummarizer(p llm.Provider, model string) consolidate.SummarizerClient {
	if model == "" {
		// The catalog's cheap Anthropic model — consolidation prompts are
		// short and cost-sensitive; the bigger models don't improve rule
		// extraction quality enough to justify the 10x price.
		model = llm.HousekeepingModel("anthropic")
	}
	return &llmSummarizer{provider: p, model: model}
}

func (s *llmSummarizer) Summarize(ctx context.Context, prompt string) (string, error) {
	resp, err := s.provider.Complete(ctx, llm.Request{
		Model:     s.model,
		System:    "You extract stable semantic rules from agent event streams. Output ONLY valid JSON matching the requested schema. No prose, no markdown fences.",
		MaxTokens: 2048,
		Messages:  []llm.Message{{Role: llm.RoleUser, Content: prompt}},
	})
	if err != nil {
		return "", fmt.Errorf("summarizer complete: %w", err)
	}
	return resp.Content, nil
}

// auxResolver hands back the provider, model and per-call budget a slot
// resolves to RIGHT NOW. internal/api's Router.ConsolidatorAux is the
// production one; a nil provider means the slot has nothing buildable behind
// it (an anthropic default with no API key, say), which is a normal state and
// not an error.
type auxResolver func() (llm.Provider, string, time.Duration)

// auxSummarizer is the consolidator's SummarizerClient, resolving the CURATOR
// aux slot per consolidation (#1695).
//
// Before this the summariser was built once in server bootstrap from
// KEEPER_OLLAMA_URL + KEEPER_MODEL and never rebuilt, which made the curator
// slot's own description — "Skill review + memory consolidation", on the Judge
// models card, in keepercfg.AuxLabels and in /api/v1/system/aux-status — false
// about the second half. Repointing curator changed skill review only;
// consolidation stayed on whatever KEEPER_* the process started with, an
// install with an ANTHROPIC_API_KEY and no Ollama had no consolidation at all
// while the card reported curator as configured and healthy, and the
// orchestrator's conversation compaction rode along on the same wiring.
//
// fallback keeps that KEEPER_* client as the degraded path rather than
// replacing it, mirroring buildAuxGatekeeper (keeper_phase2.go): an install
// with a local judge and no API key must not LOSE consolidation to a slot that
// cannot be built.
type auxSummarizer struct {
	resolve auxResolver
	// wrap applies the standard middleware stack (cost ledger, lookout,
	// telemetry) to the resolved provider. Applied per call because the
	// provider is resolved per call; the wrapper is a decorator, the pooled
	// HTTP client underneath it is the resolver's cached one.
	wrap     func(llm.Provider) llm.Provider
	fallback consolidate.SummarizerClient
	logger   *slog.Logger
}

// newAuxSummarizer builds the consolidation summariser, or returns nil when
// this instance has no way to summarise at all — the signal
// consolidate.Consolidator reads as "run the pin-snapshot path only" and the
// /api/v1/consolidate/run handler reports as "no summarizer configured".
//
// The nil decision is made here, at boot, by asking the resolver once: the
// answer is cached by the Router, so this costs one build and no network. What
// it does NOT do is capture the answer — every Summarize resolves again, so a
// model, endpoint, credential or budget change lands on the next
// consolidation.
func newAuxSummarizer(
	resolve auxResolver,
	wrap func(llm.Provider) llm.Provider,
	fallback consolidate.SummarizerClient,
	logger *slog.Logger,
) consolidate.SummarizerClient {
	if logger == nil {
		logger = slog.Default()
	}
	s := &auxSummarizer{resolve: resolve, wrap: wrap, fallback: fallback, logger: logger}
	if resolve != nil {
		if p, model, _ := resolve(); p != nil {
			logger.Info("memory consolidation enabled via the curator aux slot",
				"provider", p.Name(), "model", model)
			return s
		}
	}
	if fallback != nil {
		logger.Info("memory consolidation enabled on the local KEEPER_* model; the curator aux slot has no buildable provider")
		return s
	}
	return nil
}

// Summarize resolves the curator slot and calls the model it names, falling
// back to the KEEPER_* client when the slot has nothing buildable behind it.
//
// The slot's per-call budget is deliberately NOT applied here, and that is not
// the #1615 omission repeated: a consolidation prompt is batch work over up to
// a few hundred journal entries, sized nothing like the evaluator call the
// 20s default was calibrated for, and the call is already bounded — by the
// provider's own client timeout (300s for Ollama) and by the runner's context.
// Imposing the evaluator budget here would cut every local-model consolidation
// off mid-flight, which is the #1530 failure the aux defaults were rewritten to
// avoid. The budget stays live for the slot's evaluator (skill review) and for
// the operator-model sweep.
func (s *auxSummarizer) Summarize(ctx context.Context, prompt string) (string, error) {
	if s.resolve != nil {
		if p, model, _ := s.resolve(); p != nil {
			if s.wrap != nil {
				p = s.wrap(p)
			}
			return newLLMSummarizer(p, model).Summarize(ctx, prompt)
		}
	}
	if s.fallback != nil {
		return s.fallback.Summarize(ctx, prompt)
	}
	// Only reachable if the slot became unbuildable after boot (a credential
	// revoked under it, say) on an instance that never had a KEEPER_* model.
	// Loud, because the alternative is a consolidation tick that silently
	// records nothing.
	return "", fmt.Errorf("consolidate: the curator aux slot has no buildable model and this instance has no KEEPER_OLLAMA_URL fallback")
}
