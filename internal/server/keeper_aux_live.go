package server

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/keepercfg"
	"github.com/crewship-ai/crewship/internal/llm"
)

// Live application of the runtime aux-slot overrides.
//
// The four Keeper Reviews evaluators are constructed once at boot
// (buildPhase2Evaluators) and their pointers are captured by the route handler,
// so changing a slot's model cannot mean rebuilding them — the endpoints would
// keep talking to the objects registered at startup. Rather than adding a swap
// path through the router, this reuses the seam the gatekeeper already has for
// exactly this shape of problem: GovModelResolver, which picks the provider and
// model per request.
//
// Precedence, most specific first:
//
//  1. the workspace's vault-backed governance model (M2a) — unchanged
//  2. the instance aux override for this slot (this file)
//  3. the slot's construction-time default (boot-time YAML/env)
//
// A slot with no override returns (nil, "") and the gatekeeper uses the provider
// it was built with, so an instance nobody has configured behaves exactly as it
// did before.
type auxLiveResolver struct {
	slot  string
	store *keepercfg.AuxStore
	next  gatekeeper.GovModelResolver
	// judge resolves the instance judge endpoint, so an "ollama" override dials
	// the endpoint the operator configured rather than whatever URL this process
	// booted with.
	judge  func() (endpointURL, model string)
	j      journal.Emitter
	db     *sql.DB
	logger *slog.Logger

	// The provider carries the middleware stack and a keep-alive'd HTTP client;
	// building one per request would put a fresh connection (and for Ollama, a
	// possible cold model load) into every evaluation. Keyed on the wiring so an
	// edit takes effect on the next request and nothing else churns it.
	mu       sync.Mutex
	fpr      string
	provider llm.Provider
	model    string
	// failedFpr suppresses the warn line for a wiring already reported as
	// unbuildable — the behaviour monitor samples tool calls, so one missing API
	// key could otherwise write a log line per tool call.
	failedFpr string
}

// newAuxLiveResolver wraps next with the instance override for one slot. Returns
// next unchanged when there is no store to read, so test and embedded wirings
// keep their exact previous behaviour.
func newAuxLiveResolver(
	slot string,
	store *keepercfg.AuxStore,
	next gatekeeper.GovModelResolver,
	judge func() (string, string),
	j journal.Emitter,
	db *sql.DB,
	logger *slog.Logger,
) gatekeeper.GovModelResolver {
	if store == nil {
		return next
	}
	if logger == nil {
		logger = slog.Default()
	}
	r := &auxLiveResolver{
		slot: slot, store: store, next: next, judge: judge,
		j: j, db: db, logger: logger,
	}
	return r.resolve
}

func (r *auxLiveResolver) resolve(ctx context.Context, workspaceID string) (llm.Provider, string) {
	// The per-workspace setting is the more specific one, so it still wins — an
	// instance-wide evaluator override must not silently undo a workspace that
	// deliberately pinned its own governance model.
	if r.next != nil {
		if p, m := r.next(ctx, workspaceID); p != nil {
			return p, m
		}
	}

	eff := r.store.EffectiveSlot(r.slot)
	if !eff.Overridden {
		return nil, "" // fall through to the construction-time default
	}
	provider, model := eff.Provider.Value, eff.Model.Value
	if provider == "" || model == "" {
		return nil, ""
	}

	var judgeURL string
	if provider == keepercfg.ProviderOllama && r.judge != nil {
		judgeURL, _ = r.judge()
	}
	fpr := fmt.Sprintf("%s|%s|%s", provider, model, judgeURL)

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.provider != nil && r.fpr == fpr {
		return r.provider, r.model
	}

	base, err := r.build(provider, model, judgeURL)
	if err != nil {
		if r.failedFpr != fpr {
			r.failedFpr = fpr
			r.logger.Warn("keeper: evaluator override could not be built; the slot keeps its configured default",
				"slot", r.slot, "provider", provider, "model", model, "error", err)
		}
		return nil, ""
	}
	wrapped := llm.Middleware(base, r.j, r.db)
	r.provider, r.model, r.fpr, r.failedFpr = wrapped, model, fpr, ""
	r.logger.Info("keeper: evaluator override in force",
		"slot", r.slot, "provider", provider, "model", model)
	return r.provider, r.model
}

// build constructs the provider for one override. Ollama gets the fenced client
// the access judge uses (httpsafe.TrustedEndpointClient) rather than a bare one:
// the endpoint is API-settable, and an evaluator's output reaches a reviewer.
func (r *auxLiveResolver) build(provider, model, judgeURL string) (llm.Provider, error) {
	if provider == keepercfg.ProviderOllama {
		if judgeURL == "" {
			return nil, fmt.Errorf("no judge endpoint is configured for a local evaluator")
		}
		return llm.NewOllamaWithClient(judgeURL, model, keeperJudgeHTTPClient()), nil
	}
	return llm.BuildAuxProviderAt(llm.AuxModel{Provider: provider, Model: model}, judgeURL)
}
