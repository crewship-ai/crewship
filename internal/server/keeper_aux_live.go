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
//  3. the instance override for the fallback slot, when the slot itself is
//     unset — the case llm.ResolveAux would resolve through Fallback
//  4. the slot's construction-time default (boot-time YAML/env)
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
	judge func() (endpointURL, model string)
	// creds resolves the slot's named vault credential into an API key (#1554).
	// nil (test/embedded wirings, or an unwired vault) means "no credential is
	// resolvable", which is the pre-#1554 behaviour: the builder reads the key
	// from the process environment.
	creds  keepercfg.AuxCredentialLookup
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
	// apiKey is the vault key the cached provider was built from. It is compared
	// directly rather than folded into fpr, for the reason govModelCacheEntry
	// gives: secret material never goes through a fast hash, and a rotated key
	// can never collide back onto a provider built from the old one.
	apiKey string
	// failedFpr suppresses the warn line for a wiring already reported as
	// unbuildable — the behaviour monitor samples tool calls, so one missing API
	// key could otherwise write a log line per tool call.
	failedFpr string
	// degradedCred suppresses the revoke-safety WARN for a credential already
	// reported as unusable, same reason.
	degradedCred string
}

// newAuxLiveResolver wraps next with the instance override for one slot. Returns
// next unchanged when there is no store to read, so test and embedded wirings
// keep their exact previous behaviour.
func newAuxLiveResolver(
	slot string,
	store *keepercfg.AuxStore,
	next gatekeeper.GovModelResolver,
	judge func() (string, string),
	creds keepercfg.AuxCredentialLookup,
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
		slot: slot, store: store, next: next, judge: judge, creds: creds,
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
	provider, model, overridden := eff.Provider.Value, eff.Model.Value, eff.Overridden
	if provider == "" {
		// The slot itself is unset, so llm.ResolveAux would have reached the
		// fallback slot. Read the fallback's current value here too, or an
		// override there would be the one aux setting still needing a restart
		// (#1556) — it is only ever consulted during boot-time resolution.
		fb := r.store.EffectiveSlot(keepercfg.SlotFallback)
		provider, model, overridden = fb.Provider.Value, fb.Model.Value, fb.Overridden
	}
	if !overridden {
		return nil, "" // fall through to the construction-time default
	}
	if provider == "" || model == "" {
		return nil, ""
	}

	var judgeURL string
	if provider == keepercfg.ProviderOllama && r.judge != nil {
		judgeURL, _ = r.judge()
	}
	fpr := fmt.Sprintf("%s|%s|%s", provider, model, judgeURL)
	apiKey := r.resolveKey(ctx, provider)

	r.mu.Lock()
	defer r.mu.Unlock()
	// The key is compared, not fingerprinted: a rotated vault key must rebuild,
	// and no secret (or secret-derived digest) belongs in a cache key.
	if r.provider != nil && r.fpr == fpr && r.apiKey == apiKey {
		return r.provider, r.model
	}

	base, err := r.build(provider, model, judgeURL, apiKey)
	if err != nil {
		if r.failedFpr != fpr {
			r.failedFpr = fpr
			r.logger.Warn("keeper: evaluator override could not be built; the slot keeps its configured default",
				"slot", r.slot, "provider", provider, "model", model, "error", err)
		}
		return nil, ""
	}
	wrapped := llm.Middleware(base, r.j, r.db)
	r.provider, r.model, r.fpr, r.apiKey, r.failedFpr = wrapped, model, fpr, apiKey, ""
	r.logger.Info("keeper: evaluator override in force",
		"slot", r.slot, "provider", provider, "model", model)
	return r.provider, r.model
}

// resolveKey looks up the slot's named vault credential (#1554). "" means "no
// credential is in force" — either none is named, or the named one is no longer
// usable — and the builder then reads the key from the process environment,
// which is exactly the pre-#1554 behaviour.
//
// Revoke-safety (§4.4, mirroring governance.ResolveGovModel): a revoke is a SOFT
// delete, so the id is still in the row and the column's ON DELETE SET NULL
// never fires. The lookup is the thing that notices, and its failure DEGRADES
// the slot rather than taking the evaluator down or dialling with a stale id.
// The WARN is de-duplicated per reason: the behaviour monitor samples tool calls,
// so a stuck revoked credential would otherwise write a log line per call.
//
// A local ("ollama") slot dials the instance judge endpoint and needs no key, so
// a credential left over from when the slot was hosted is ignored rather than
// resolved.
func (r *auxLiveResolver) resolveKey(ctx context.Context, provider string) string {
	if r.creds == nil || provider == keepercfg.ProviderOllama {
		return ""
	}
	credID := r.store.CredentialFor(r.slot)
	if credID == "" {
		return ""
	}
	key, err := r.creds(ctx, credID)
	if err != nil {
		reason := err.Error()
		r.mu.Lock()
		repeat := r.degradedCred == reason
		r.degradedCred = reason
		r.mu.Unlock()
		if !repeat {
			r.logger.Warn("keeper: evaluator credential is unusable; the slot falls back to the server's own key",
				"slot", r.slot, "provider", provider, "error", err)
		}
		return ""
	}
	r.mu.Lock()
	r.degradedCred = ""
	r.mu.Unlock()
	return key
}

// build constructs the provider for one override. Ollama gets the fenced client
// the access judge uses (httpsafe.TrustedEndpointClient) rather than a bare one:
// the endpoint is API-settable, and an evaluator's output reaches a reviewer.
//
// apiKey == "" is the env path: see llm.BuildAuxProviderWithKey.
func (r *auxLiveResolver) build(provider, model, judgeURL, apiKey string) (llm.Provider, error) {
	if provider == keepercfg.ProviderOllama {
		if judgeURL == "" {
			return nil, fmt.Errorf("no judge endpoint is configured for a local evaluator")
		}
		return llm.NewOllamaWithClient(judgeURL, model, keeperJudgeHTTPClient()), nil
	}
	return llm.BuildAuxProviderWithKey(llm.AuxModel{Provider: provider, Model: model}, judgeURL, apiKey)
}
