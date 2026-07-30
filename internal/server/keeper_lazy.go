package server

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/httpsafe"
	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/keepercfg"
)

// keeperNotConfiguredReason is the verdict a credential request gets when
// Keeper is off. Wording is deliberately identical to the pre-existing
// nil-gatekeeper branches in internal/api (keeper_request.go,
// keeper_execute.go): the gatekeeper is now always wired, so "off" has to be
// expressed as a decision rather than as a missing dependency, and an agent
// must not be able to tell the two apart.
const keeperNotConfiguredReason = "Keeper not configured"

// judgeBuilder constructs an access gatekeeper for one effective instance
// configuration. It is a seam: internal/server owns the real construction
// (provider + middleware + watch-spec resolver + gov-model resolver), the tests
// pass a stub, and lazyGatekeeper knows only when to call it.
type judgeBuilder func(eff keepercfg.Effective) (gatekeeper.Evaluator, error)

// lazyGatekeeper defers building the access gatekeeper until a request needs
// one, and rebuilds it when the instance judge configuration changes.
//
// Why this exists: the gatekeeper used to be constructed once, inside
// `if cfg.Keeper.Enabled`, from values captured at boot. That made three things
// true at once — Keeper could only be turned on by restarting the server, the
// endpoint and model could only be changed by restarting the server, and an
// operator with no shell access could do neither. Building on demand from the
// keepercfg store collapses all three: the store is the only thing that decides
// whether a judge exists and what it is wired to, and it is writable at runtime.
//
// The cache is keyed on Effective.JudgeFingerprint, so a change to the wiring
// yields a new judge on the next evaluation and a change to anything else (who
// saved it, when) reuses the warm one. That matters more than it looks: the
// provider carries the middleware stack and a keep-alive'd HTTP client, and
// churning it would put a cold model load in the credential path.
type lazyGatekeeper struct {
	store  *keepercfg.Store
	build  judgeBuilder
	logger *slog.Logger

	mu  sync.Mutex
	fpr string
	cur gatekeeper.Evaluator
}

// newLazyGatekeeper wires a lazy gatekeeper over store. logger may be nil.
func newLazyGatekeeper(store *keepercfg.Store, logger *slog.Logger, build judgeBuilder) *lazyGatekeeper {
	if logger == nil {
		logger = slog.Default()
	}
	return &lazyGatekeeper{store: store, build: build, logger: logger}
}

// Evaluate implements gatekeeper.Evaluator.
func (l *lazyGatekeeper) Evaluate(ctx context.Context, req gatekeeper.EvalRequest) (keeper.GatekeeperResponse, error) {
	eff := l.store.Effective()

	if !eff.Enabled.Value {
		return deniedResponse(keeperNotConfiguredReason), nil
	}
	if !eff.JudgeConfigured() {
		// Unreachable through the admin API — enabling without a judge is a
		// validation error there — but KEEPER_ENABLED=true with no
		// KEEPER_MODEL can still arrive from the environment. Naming the
		// configuration is the whole point: a fail-closed judge that cannot be
		// built otherwise presents as a security verdict.
		l.logger.Error("keeper: enabled but no judge endpoint/model is configured; denying credential requests",
			"endpoint_source", eff.EndpointURL.Source, "model_source", eff.Model.Source)
		return deniedResponse("Keeper is enabled but has no judge endpoint or model configured"), nil
	}

	gk, err := l.evaluator(eff)
	if err != nil {
		// Returned as an error, not a decision: the caller already turns an
		// evaluation failure into "deny by default" and logs it, and collapsing
		// the two here would hide a broken judge behind a normal-looking DENY.
		return keeper.GatekeeperResponse{}, err
	}
	return gk.Evaluate(ctx, req)
}

// evaluator returns the judge for eff, building it if the wiring changed.
func (l *lazyGatekeeper) evaluator(eff keepercfg.Effective) (gatekeeper.Evaluator, error) {
	fpr := eff.JudgeFingerprint()

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cur != nil && l.fpr == fpr {
		return l.cur, nil
	}
	gk, err := l.build(eff)
	if err != nil {
		return nil, err
	}
	l.logger.Info("keeper: access judge built",
		"endpoint", eff.EndpointURL.Value, "model", eff.Model.Value,
		"endpoint_source", eff.EndpointURL.Source, "model_source", eff.Model.Source)
	l.cur, l.fpr = gk, fpr
	return gk, nil
}

// keeperJudgeHTTPClient dials the instance judge endpoint.
//
// The endpoint is operator-authored server configuration — the same trust class
// as KEEPER_OLLAMA_URL, which has always dialled unfenced because it is
// typically loopback or a LAN box. httpsafe.TrustedEndpointClient encodes
// exactly that: private ranges stay reachable, the hard tier (cloud metadata,
// link-local, multicast) does not. It matters more now than it did: the value is
// settable at runtime by an OWNER/ADMIN rather than only by whoever controls the
// process environment, and a verdict's reason string is surfaced to the
// requesting agent — so an unfenced dial would make it a reflection channel for
// whatever a metadata endpoint returned.
//
// The status probe of this same URL (internal/api/keeper_status.go) shares the
// constructor. Two copies of one trust decision is how they drift.
//
// 120s covers a cold model load: the gatekeeper sits in the credential path and
// the first request after an idle hour pays for the weights.
func keeperJudgeHTTPClient() *http.Client {
	return httpsafe.TrustedEndpointClient(120 * time.Second)
}

// deniedResponse is the fail-closed verdict shape both keeper handlers expect.
func deniedResponse(reason string) keeper.GatekeeperResponse {
	return keeper.GatekeeperResponse{
		Decision:  string(keeper.DecisionDeny),
		Reason:    reason,
		RiskScore: 10,
	}
}
