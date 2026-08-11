package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/buildinfo"
	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/episodic"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/keepercfg"
	"github.com/crewship-ai/crewship/internal/license"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/policy"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ratelimitcfg"
	"github.com/crewship-ai/crewship/internal/ws"
	dockerclient "github.com/moby/moby/client"
)

// errPortExposeNoNetwork is returned by the router's DockerInspector adapter
// when the target container is not attached to the crew bridge network, so
// the handler turns it into a 502 instead of a misleading 500.

var errPortExposeNoNetwork = errors.New("container not attached to crew network")

// keeperWSBroadcaster adapts ws.Hub to the KeeperBroadcaster interface.
type keeperWSBroadcaster struct {
	hub *ws.Hub
}

// BroadcastKeeperEvent sends a Keeper event to all WebSocket clients subscribed to the workspace.
func (b *keeperWSBroadcaster) BroadcastKeeperEvent(workspaceID string, event map[string]any) {
	broadcastChannelEvent(b.hub, "keeper", workspaceID, "keeper_event", event)
}

// BroadcastInboxUpdated invalidates the workspace inbox queries in realtime
// after a Keeper inbox write — the same `inbox.updated` event every other
// inbox producer emits (payload is advisory; the client refetches on any).
func (b *keeperWSBroadcaster) BroadcastInboxUpdated(workspaceID string, source string) {
	broadcastWorkspaceEvent(b.hub, workspaceID, "inbox.updated", map[string]string{"source": source})
}

type Router struct {
	// mux is a recording wrapper around http.ServeMux (router_mux.go).
	// Same Handle/HandleFunc surface, plus the registered route table —
	// which the method guards and the spec-drift test both need.
	mux    *routeMux
	db     *sql.DB
	logger *slog.Logger
	authMw *AuthMiddleware
	// mutationRoutes records the {method, pattern, role} of every mutation
	// route registered through authedMut / authedSelfMut. It is the walkable
	// route table http.ServeMux refuses to expose — the enumeration test
	// (route_authz_invariant_test.go) iterates it to assert complete
	// mediation: every state-changing endpoint declares a role at
	// registration, so a new mutation route that forgets its gate fails the
	// build instead of shipping silently open (Saltzer & Schroeder). See
	// rbac_routes.go for the recording wrappers.
	mutationRoutes []mutRoute
	// adminRoutes records the {method, pattern} of every admin-console READ
	// route registered through authedAdmin — the GET surface behind the
	// ADMIN+ floor (#865). Mutations already carry roleManage via authedMut;
	// this is the read half. The floor invariant walks it (and source-scans
	// router_admin.go) so an admin read that forgets its gate fails the build.
	adminRoutes   []adminRoute
	sessionsStore sessions.Store
	// revokeNotifiers are extra transports (beyond r.hub) that get an
	// immediate in-process signal on session revocation — see
	// WithSessionRevocationNotifiers and newNotifyingSessionStore.
	revokeNotifiers []SessionRevocationNotifier
	socketPath      string
	internalToken   string
	internalBaseURL string
	// internalLoopbackURL is for in-daemon HTTP calls (e.g. the webhook
	// secret resolver) that hit our own internal API. internalBaseURL is
	// designed for containers (host.docker.internal:<port>) and dialing
	// it from the daemon depends on the host's /etc/hosts mapping —
	// fragile on multi-host lab networks. Use the loopback variant
	// (typically 127.0.0.1:<port>) instead when set. (Issue #535.)
	internalLoopbackURL string
	hub                 *ws.Hub
	orch                *orchestrator.Orchestrator
	// admission is the read side of host admission control (#1668). nil =
	// not wired; GET /api/v1/runtime/capacity then reports enabled:false.
	admission     AdmissionSnapshotter
	keeperGK      gatekeeper.Evaluator
	keeperSecrets SecretGetter
	// keeperContainer is the process's ONE container provider — server.go
	// passes deps.Container here, and leaves it nil when the server has none
	// (--no-docker, or a provider that failed to build). The name records its
	// first consumer, not an exclusive owner; read it through activeContainer.
	keeperContainer provider.ContainerProvider
	keeperConfig    *config.KeeperConfig
	keeperSettings  *keepercfg.Store // runtime instance judge config layered over keeperConfig; nil → env values only
	// keeperHandler is kept so the credential path's wiring is assertable. It is
	// the seam the judge profile crosses, and a constructor call that is simply
	// absent is invisible to any test that builds the handler itself.
	keeperHandler  *KeeperHandler
	govModelStatus GovModelStatusProvider
	// govModelJudge is the same resolver as govModelStatus, concretely typed so
	// the admin judge routes can build a candidate hosted judge. nil → those
	// routes 503.
	govModelJudge    *GovModelResolver
	composioConfig   *config.ComposioConfig
	keeperConvReader ConversationReader
	convSearcher     ConversationSearcher
	missionCallback  MissionCallback
	scheduleUpdater  ScheduleUpdater
	logWriter        *logcollector.Writer
	allowSignup      bool
	googleClientID   string
	googleSecret     string
	authBaseURL      string
	license          *license.License
	agentHandler     *AgentHandler
	// credentialHandler and skillGenHandler are stashed at
	// registerCrewsRoutes time so the registerInternalRoutes step
	// can wire the matching /api/v1/internal/credentials and
	// /api/v1/internal/skills/generate adapters without re-
	// constructing a parallel instance (matters for state-bearing
	// fields like the SkillGenerateHandler's per-workspace LLM
	// credential cache). nil-safe — adapters skip registration when
	// the parent handler isn't wired (test routers, early init).
	credentialHandler   *CredentialHandler
	skillGenHandler     *SkillGenerateHandler
	skillPropHandler    *SkillProposedHandler
	hybridSearchHandler *MemoryHybridSearchHandler
	// attachmentHandler is retained so the internal (agent) attachment routes
	// registered in router_internal.go wrap the SAME instance the public routes
	// use. Both doors share one write path on purpose — see
	// issue_attachments_internal.go. nil when registerOrchestrationRoutes has
	// not run (test routers); the internal routes then skip registration.
	attachmentHandler      *AttachmentHandler
	storagePath            string // base path for crew file storage
	catalogFetcher         *devcontainer.CatalogFetcher
	runtimeFetcher         *devcontainer.RuntimeFetcher
	dockerClient           *dockerclient.Client
	imageBuilder           devcontainer.ImageBuilder
	featureCacheDir        string
	portExposeRegistry     *PortExposeRegistry // closed via Shutdown() on server stop
	portExposePublicURL    string              // e.g. http://crewship.example.com:8080, used to build capability URLs
	portExposeNetwork      string              // Docker bridge name; falls back to handler default when empty
	authRateLimitedMux     http.Handler        // mux wrapped with auth rate limiter
	apiRateLimitedMux      http.Handler        // mux wrapped with general API rate limiter
	credTestRateLimitedMux http.Handler        // mux wrapped with /credentials/test limiter (defence against credential-validation oracle abuse)
	// credRevealRateLimitedMux wraps the ONE route that returns a stored
	// secret in plaintext (PRD-CREDENTIALS-V2-2026 §2.6 L6). Far tighter
	// than every other bucket, and it must be selected BEFORE the general
	// branch below: authenticated CLI tokens are exempt from the general
	// per-IP bucket (#1333), so a reveal that fell through to it would be
	// unthrottled for exactly the caller shape we care most about.
	credRevealRateLimitedMux http.Handler
	// authRL/apiRL/credTestRL/credRevealRL are the underlying limiters behind
	// the wrapped muxes above, retained so a runtime override from the admin
	// Rate Limiters console (ratelimitStore.OnChange) can retune them in place
	// — SetReqPerMin — without rebuilding the handler chain.
	authRL         *RateLimiter
	apiRL          *RateLimiter
	credTestRL     *RateLimiter
	credRevealRL   *RateLimiter
	ratelimitStore *ratelimitcfg.Store // runtime-tunable limiter values; nil → shipped defaults
	cappedMux      http.Handler        // body-capped mux, NOT rate-limited — the #1333 authenticated-CLI exemption routes here directly
	cliExemptNeg   *cliExemptNegCache  // bounded negative cache of failed exemption lookups — stops spoofed CLI-prefix bearers from forcing an unthrottled DB lookup per request
	journal        journal.Emitter     // Crew Journal emitter; nil → emits become no-ops so dev builds without the server-level wiring still work
	consolidator   *consolidate.Consolidator
	// outputBasePath is the host-side root that the container
	// provider bind-mounts. PR-E F6 uses this to resolve per-agent
	// and per-crew PERSONA + peers/ paths without going through the
	// container. Empty → persona / peers endpoints respond 503
	// "storage not configured" rather than 404.
	//
	// It is also the root the consolidator surfaces resolve their
	// per-crew memory output from: there used to be a second field
	// holding a supposed "crew memory root", but a host process has no
	// such thing — /crew/shared/.memory is per-crew bind of
	// {outputBasePath}/crews/{crewID} (#1663).
	outputBasePath string
	// memoryVersionsBlobRoot is the v90 content-addressed blob
	// directory ApproveProposal records under. Empty disables
	// versioning on approve (the approve still succeeds; the
	// canonical merge just doesn't record an audit row).
	memoryVersionsBlobRoot string
	// hybridSearchEmbedder + hybridSearchProvider feed the
	// MemoryHybridSearchHandler. Either may be nil; the underlying
	// memory.HybridSearch degrades gracefully (FTS-only when
	// embedder is nil, episodic-only when provider is nil).
	hybridSearchEmbedder episodic.Embedder
	hybridSearchProvider WorkspaceMemoryProvider
	provisioning         *ProvisioningHandler // exposed via Provisioning() so chatbridge can auto-trigger builds
	// automations is exposed via Automations() so cmd_start can hand the
	// handler a refresh hook into the in-memory automation.Registry. Without
	// it a newly created rule would not fire until the 60s tick, which reads
	// as "the automation is broken" to whoever just saved it.
	automations *AutomationHandler
	// PipelinesHandler is exposed (capitalised) so the orchestrator
	// boot path can hand it the AgentRunner adapter post-construction.
	// The router builds handlers before the orchestrator is fully
	// initialised, so two-phase wiring is the cheapest fix.
	PipelinesHandler *PipelineHandler

	// steerer delivers mid-turn steering messages (POST
	// /api/v1/chats/{chatId}/steer). The chatbridge.Bridge satisfies it.
	// Wired post-construction from cmd_start (same boot-order reason as
	// the scheduler / provisioning enqueuer); nil → the steer route
	// returns 503. The SteerHandler holds this pointer indirectly via
	// the Router so SetSteerer can rewire it after the bridge is built.
	steerer      Steerer
	steerHandler *SteerHandler

	// authHandler is the live AuthHandler created during route
	// registration. Stored on the Router so server.New can call
	// MaybeGenerateSetupToken (Patch C) on the same instance that
	// /api/v1/bootstrap actually dispatches to — otherwise the armed
	// token lives on a handler the dispatcher never reaches.
	authHandler *AuthHandler

	// assignmentHandler is the live AssignmentHandler created during
	// route registration. Stored on the Router so the server boot path
	// can start the stuck-QUEUED sweeper (StartStuckQueueSweeper) on
	// the same instance the HTTP dispatch routes use — a second
	// instance would work (the sweeper is stateless over the DB) but
	// would split journal/log wiring across two handlers for no gain.
	assignmentHandler *AssignmentHandler

	// version is the ldflags-injected binary version (e.g. "v0.1.0-beta.1"
	// or "dev" for local builds). Surfaced on GET /api/v1/system/version
	// so the web UI can render an "update available" banner.
	version string

	// build is the resolved build identity — commit, build time, dirty flag
	// (#1645). Set alongside version by SetBuild; zero until then, which is
	// why the version handler re-resolves rather than trusting a zero value.
	build buildinfo.Info

	// policyResolver is the shared per-crew autonomy + behavior_mode
	// resolver introduced by PR-B F2. Carried on Router so PATCH
	// handlers can invalidate the cache (otherwise subsystems would
	// see stale values for up to the 10s TTL after an operator flip).
	// PR-C / PR-D / PR-E consumers will read through this same
	// instance. policyResolverOnce serialises lazy init — concurrent
	// HTTP handlers calling PolicyResolver() at startup would
	// otherwise race on the field and risk constructing two resolvers
	// (and Invalidate hitting the wrong cache).
	policyResolver     *policy.Resolver
	policyResolverOnce sync.Once

	// auxModels carries the PR-B F3 auxiliary-model assignment per
	// slot. Read by the system aux-status endpoint (and future PR-C
	// evaluators) to look up the resolved provider/model/timeout for
	// each subsystem. Unset → AuxModels() falls back to
	// llm.DefaultAuxiliaryModels so the diagnostic surface stays
	// useful in dev / test builds that haven't wired explicit config.
	auxModels    llm.AuxiliaryModels
	auxModelsSet bool

	// keeperAuxSettings layers the runtime per-slot overrides over auxModels.
	// nil → boot-time values only (CLI processes, most tests).
	keeperAuxSettings *keepercfg.AuxStore

	// Keeper Phase 2 (PR-C / PRD §6 F4) evaluators. Optional — the
	// router_internal route registration passes whichever are non-nil
	// to NewKeeperPhase2Handler; the handler returns 503 for nil
	// evaluators so partial rollouts have a deterministic surface.
	// Wired via SetKeeperPhase2Evaluators from the server bootstrap
	// where the aux-LLM providers (PR-B F3) get resolved.
	skillReviewEval *gatekeeper.SkillReviewEvaluator
	behaviorEval    *gatekeeper.BehaviorEvaluator
	memHealthEval   *gatekeeper.MemoryHealthEvaluator
	negativeEval    *gatekeeper.NegativeLearningEvaluator

	// keeperPhase2 is the single Phase 2 handler instance, shared by the
	// sidecar IPC routes and the operator-facing manual-run route (#1555).
	// Both registrars ask for it through keeperPhase2Handler(); the admin
	// registrar runs first, so constructing it there and again in the
	// internal one would give the two surfaces different handlers — and the
	// evaluators are captured by value, so the second copy could silently
	// hold a different set.
	keeperPhase2     *KeeperPhase2Handler
	keeperPhase2Once sync.Once

	// runVerdict* back the post-run outcome verdict (#1403). Every
	// production call site — the internal-runs terminal handler (ad-hoc
	// agent runs) and every pipeline.NewWiredExecutor (routine runs:
	// HTTP, boot-resume, cron scheduler) — holds the RunVerdict method
	// rather than its result, so they all share one built provider
	// without any of them capturing it. nil provider (after resolution)
	// means the slot is unconfigured/unbuildable; callers treat that as
	// "feature off".
	//
	// Cached on the wiring it was built from rather than behind a
	// sync.Once (#1556): the run_summary and fallback aux slots are
	// runtime-settable, and a once-built provider made those two the only
	// slots whose override needed a server restart. Keyed on
	// provider|model|ollama-endpoint so an edit is live on the next
	// verdict and an unchanged wiring still reuses one HTTP client.
	runVerdictCache auxProviderCache

	// userModelCache backs the operator-model extractor's curator slot
	// (#1669), on the same terms: resolved per sweep so an aux-slot edit
	// is live on the next one, cached on the wiring so an unchanged slot
	// reuses one HTTP client.
	userModelCache auxProviderCache

	// internalHandler is retained (it is otherwise local to
	// registerInternalRoutes) so shutdown can drain its in-flight
	// post-run verdict goroutines before the journal writer closes.
	internalHandler *InternalHandler
}

// DrainVerdicts drains in-flight post-run verdict goroutines across both
// call sites — ad-hoc agent runs (InternalHandler) and routine runs
// (PipelineHandler) — bounded by timeout so a wedged LLM call can't hang
// shutdown. Invoked from the server's shutdown sequence AFTER the HTTP
// listener stops but BEFORE the journal writer closes, so a verdict that
// was mid-generation still records its journal entry (#1403).
func (r *Router) DrainVerdicts(timeout time.Duration) {
	if r.internalHandler != nil {
		r.internalHandler.DrainVerdicts(timeout)
	}
	if r.PipelinesHandler != nil {
		r.PipelinesHandler.DrainVerdicts(timeout)
	}
}

// RunVerdict resolves the run_summary aux slot to the LLM provider and model in
// force RIGHT NOW, for the post-run outcome verdict (#1403). It matches
// runverdict.Resolver, so call sites pass the method rather than its result.
//
// Resolved per verdict, not once at boot (#1556). run_summary and the fallback
// slot behind it are runtime-settable from the console; a provider built once
// and handed to the executors made those the only two of the seven aux slots
// whose override took effect on the next server start rather than the next
// evaluation. The provider is still built at most once per distinct wiring —
// it carries a keep-alive'd HTTP client, and for Ollama a rebuild can mean a
// cold model load — the same cache shape the four Keeper Reviews slots use in
// internal/server/keeper_aux_live.go.
//
// A nil provider means the slot is unconfigured or unbuildable (e.g. no
// ANTHROPIC_API_KEY). Callers must read that as "verdict generation is off",
// not as an error; a later fix to the wiring is picked up on the next call.
// activeContainer returns the container provider this process is holding, or
// nil when it has none. One field, one object: server.go wires deps.Container
// into keeperContainer, and that is the provider every crew container is
// created through — reading it under this name keeps the "which runtime is
// actually in use" question from looking like a keeper concern.
func (r *Router) activeContainer() provider.ContainerProvider { return r.keeperContainer }

func (r *Router) RunVerdict() (llm.Provider, string) {
	p, m, _ := r.auxProvider(llm.SlotRunSummary, &r.runVerdictCache,
		"run verdict: run_summary", "outcome verdicts disabled")
	// The budget is dropped here on purpose: the post-run verdict is
	// bounded by the caller's context (see internal/runverdict), which is
	// the run's own teardown deadline, and narrowing it to the slot's
	// number would be a behaviour change this PR has no business making.
	return p, m
}

// UserModelAux resolves the curator aux slot for the operator-model
// extractor (#1669), on the same per-call terms as RunVerdict and for the
// same reason: the slot is runtime-settable and the sweep that uses it
// runs daily in a long-lived process, so a pair captured at boot is a
// pair the operator's edit cannot reach.
//
// The curator slot is the right one by its own documented purpose —
// "memory consolidation, skill review" (internal/llm/aux.go). Note that
// nothing else routes memory work through it today: the consolidator
// builds its summariser directly from cfg.Keeper.OllamaURL and never
// consults the aux slots at all, so an operator repointing curator has
// until now changed only skill review.
//
// The third return is the slot's per-call budget, which the extractor
// applies. #1601 was that field reaching no evaluator; here an unbounded
// call would not fail one extraction but stall the whole daily sweep,
// whose context lives until server shutdown.
//
// A nil provider means "extraction is off", not an error.
func (r *Router) UserModelAux() (llm.Provider, string, time.Duration) {
	return r.auxProvider(llm.SlotCurator, &r.userModelCache,
		"user model: curator", "operator-model extraction disabled")
}

// auxProviderCache memoises one slot's built (provider, model) pair
// against the wiring it was built from.
//
// Cached on a fingerprint rather than behind a sync.Once (#1556): every
// aux slot and the fallback behind it are runtime-settable, and a
// once-built provider makes a slot one whose override needs a server
// restart. Keyed on provider|model|ollama-endpoint|credential so an edit
// is live on the next call and an unchanged wiring still reuses one
// keep-alive'd HTTP client (for Ollama, a rebuild can mean a cold model
// load).
type auxProviderCache struct {
	mu       sync.Mutex
	fpr      string
	provider llm.Provider
	model    string
	// warned is the last failure already reported, so a slot that cannot
	// be built logs once per distinct problem rather than once per call.
	warned string
}

// auxProvider is the shared body of RunVerdict / UserModelAux. what names
// the caller and slot for the log line ("run verdict: run_summary");
// consequence says what stops working ("outcome verdicts disabled").
// The third return is the slot's per-call budget; callers that bound the
// call themselves may ignore it.
func (r *Router) auxProvider(slot llm.Slot, cache *auxProviderCache, what, consequence string) (llm.Provider, string, time.Duration) {
	aux, err := llm.ResolveAux(r.AuxModels(), slot)
	if err != nil {
		r.dropAuxProvider(cache, what+" aux slot resolve failed; "+consequence, err)
		return nil, "", 0
	}
	// An "ollama" slot must dial the endpoint the instance is configured with —
	// the judge endpoint is itself runtime-settable — rather than whatever
	// KEEPER_OLLAMA_URL the process started with.
	var ollamaBase string
	if aux.Provider == keepercfg.ProviderOllama && r.keeperSettings != nil {
		ollamaBase = r.keeperSettings.Effective().EndpointURL.Value
	}
	// The pinned vault key is part of the built provider's identity (#1554), so
	// it belongs in the fingerprint: repointing a slot at a different
	// subscription has to rebuild, not reuse a client authenticated with the
	// key the operator just moved away from.
	credID := r.keeperAuxSettings.CredentialFor(string(slot))
	fpr := aux.Provider + "|" + aux.Model + "|" + ollamaBase + "|" + credID

	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.provider != nil && cache.fpr == fpr {
		// The budget comes from the freshly resolved slot, not the cache:
		// it is not part of the provider's identity (changing a timeout
		// must not force an HTTP client rebuild) and it must still be live
		// on the next call after an operator lowers it.
		return cache.provider, cache.model, aux.Timeout
	}

	// Bounded: this resolves the key on the first call rather than at boot,
	// and a wedged database must not hang a run's teardown — nor hold this lock
	// for longer than the deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	provider, err := buildAuxWithCredential(ctx, aux, ollamaBase, credID, NewAuxCredentialLookup(r.db), r.logger)
	if err != nil {
		r.dropAuxProviderLocked(cache, what+" provider build failed; "+consequence, err)
		return nil, "", 0
	}
	cache.provider, cache.model, cache.fpr = provider, aux.Model, fpr
	cache.warned = ""
	return provider, aux.Model, aux.Timeout
}

// dropAuxProvider forgets any cached provider and logs why. Forgetting
// matters: the operator moved the slot to a wiring that does not resolve,
// and quietly billing the model they moved away from would be worse than
// the feature being off.
func (r *Router) dropAuxProvider(cache *auxProviderCache, msg string, err error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	r.dropAuxProviderLocked(cache, msg, err)
}

// dropAuxProviderLocked is dropAuxProvider for callers already holding
// the cache mutex.
func (r *Router) dropAuxProviderLocked(cache *auxProviderCache, msg string, err error) {
	cache.provider, cache.model, cache.fpr = nil, "", ""
	key := msg + ": " + err.Error()
	if cache.warned == key {
		return
	}
	cache.warned = key
	if r.logger != nil {
		r.logger.Warn(msg, "error", err)
	}
}

// SetKeeperPhase2Evaluators is the legacy post-construction setter.
//
// DEPRECATED: call WithKeeperPhase2Evaluators as a RouterOption on
// NewRouter instead. registerInternalRoutes constructs
// NewKeeperPhase2Handler captures these evaluator pointers BY VALUE at
// route registration time. Calling this setter AFTER NewRouter has
// returned writes to Router fields the live handler has already
// snapshotted as nil — the endpoint then 503s forever for the rest of
// the process lifetime. Kept on the type for backward compatibility
// with tests that build a Router stepwise; production callers must
// pass the option.
func (r *Router) SetKeeperPhase2Evaluators(
	skillReview *gatekeeper.SkillReviewEvaluator,
	behavior *gatekeeper.BehaviorEvaluator,
	memoryHealth *gatekeeper.MemoryHealthEvaluator,
	negative *gatekeeper.NegativeLearningEvaluator,
) {
	r.skillReviewEval = skillReview
	r.behaviorEval = behavior
	r.memHealthEval = memoryHealth
	r.negativeEval = negative
}

// keeperPhase2Handler returns (lazily constructs) the shared Keeper Phase 2
// handler. Nil evaluators are fine and deliberate: the handler answers 503
// "not configured" per endpoint so a partial rollout has a deterministic
// surface instead of a missing route.
//
// Evaluators are captured by value here, which is why
// SetKeeperPhase2Evaluators is deprecated: calling it after registration
// writes fields this handler has already snapshotted.
func (r *Router) keeperPhase2Handler() *KeeperPhase2Handler {
	r.keeperPhase2Once.Do(func() {
		h := NewKeeperPhase2Handler(
			r.db, r.internalToken, r.PolicyResolver(),
			r.skillReviewEval, r.behaviorEval, r.memHealthEval, r.negativeEval,
			r.logger,
		).WithMemoryBase(r.outputBasePath) // #1037: derive lesson write target server-side, not from the request body
		// Same broadcaster the credential-path KeeperHandler gets — the F4
		// endpoints write to the same inbox and owe the same realtime push
		// (#1001 M0).
		if r.hub != nil {
			h.WithBroadcaster(&keeperWSBroadcaster{hub: r.hub})
		}
		r.keeperPhase2 = h
	})
	return r.keeperPhase2
}

// PolicyResolver returns (lazily constructs) the shared per-crew
// policy resolver. Callers should always go through this rather
// than constructing their own — sharing the cache is what makes
// Invalidate work end-to-end. sync.Once guarantees a single
// resolver instance even under concurrent first-call races.
func (r *Router) PolicyResolver() *policy.Resolver {
	r.policyResolverOnce.Do(func() {
		r.policyResolver = policy.NewResolver(r.db)
	})
	return r.policyResolver
}

// AuxModels returns the wired PR-B F3 auxiliary-model config, or
// llm.DefaultAuxiliaryModels() when WithAuxiliaryModels was not
// passed. Callers should always go through this rather than reading
// r.auxModels directly — the default fallback keeps the aux-status
// endpoint useful in test/dev builds and prevents PR-C evaluators
// from blowing up on a zero-valued struct (every Provider would be
// "" → ResolveAux would error). Production wires the real config via
// WithAuxiliaryModels.
//
// When the runtime override store is wired it wins: it holds the same boot-time
// values as its inherited layer, so this returns the config in force rather than
// the one captured at construction. Every caller goes through here, which is why
// an admin edit reaches the aux-status surface and the run-verdict provider
// without a restart.
func (r *Router) AuxModels() llm.AuxiliaryModels {
	if r.keeperAuxSettings != nil {
		return r.keeperAuxSettings.Resolved()
	}
	if !r.auxModelsSet {
		return llm.DefaultAuxiliaryModels()
	}
	return r.auxModels
}

// KeeperAuxSettings exposes the runtime evaluator-override store for the admin
// handlers. nil when unwired — callers must surface that as 503 rather than
// silently pretending the write landed.
func (r *Router) KeeperAuxSettings() *keepercfg.AuxStore {
	return r.keeperAuxSettings
}

// SetVersion records the binary version for the version-info endpoint.
// Called from cmd_start.go after construction because the version lives
// in package main as an ldflags-injected var and can't be referenced
// from internal/api.
//
// Prefer SetBuild: version alone cannot identify a build, because every
// binary an ldflags-less `go build` has ever produced reports "dev".
func (r *Router) SetVersion(v string) {
	r.SetBuild(v, "", "")
}

// SetBuild records the full ldflags-injected build identity (#1645) —
// main.version, main.commit and main.date — for GET /api/v1/system/version.
// Commit and date may be the in-source placeholders; buildinfo.Resolve
// discards those and falls back to the binary's embedded VCS stamps, which
// is the only source a dev-slot build has.
//
// Called from cmd_start.go before the listener starts, for the same reason
// SetVersion is: the vars live in package main.
func (r *Router) SetBuild(version, commit, date string) {
	r.version = version
	r.build = buildinfo.Resolve(version, commit, date)
}

// Provisioning returns the registered ProvisioningHandler so wiring code (e.g.
// cmd_start) can hand it to chatbridge as a ProvisioningEnqueuer. Returns nil
// when registerRoutes hasn't run yet (e.g. tests that build a Router by hand).
func (r *Router) Provisioning() *ProvisioningHandler {
	return r.provisioning
}

// Automations returns the registered AutomationHandler so cmd_start can wire
// its registry-refresh hook. Returns nil when registerRoutes hasn't run yet.
func (r *Router) Automations() *AutomationHandler {
	return r.automations
}

// AuthHandler returns the registered AuthHandler so server startup code can
// call MaybeGenerateSetupToken on the same instance the /api/v1/bootstrap
// route dispatches to. Returns nil when registerAuthRoutes hasn't run yet
// (handler-only tests that build a Router by hand).
func (r *Router) AuthHandler() *AuthHandler {
	return r.authHandler
}

// Assignments returns the registered AssignmentHandler so server startup
// code can start the stuck-QUEUED sweeper on the same instance the
// dispatch routes use. Returns nil when registerOrchestrationRoutes
// hasn't run yet (handler-only tests that build a Router by hand).
func (r *Router) Assignments() *AssignmentHandler {
	return r.assignmentHandler
}

// Journal returns the journal emitter or a no-op if unset. Handlers should
// use this instead of accessing the field directly so the nil-guard lives

func (r *Router) Journal() journal.Emitter {
	if r.journal == nil {
		return noopEmitter{}
	}
	return r.journal
}

// noopEmitter swallows Emit calls so early-init code paths and tests that
// don't wire a real writer still compile and run. It returns a synthesized
// ID so callers treating the return value as "something happened" stay
// happy.
//
// EXCEPTION: run.* lifecycle entries are the canonical source of truth
// for agent runs after Phase J of unified-journal — silently dropping
// them in production would leave the dashboard, KPIs and recovery loop
// blind to runs that did happen. So when a run.* type is emitted into
// the noop, we log loudly AND return an error. Handlers that check err
// (CreateRun, UpdateRun, runAssignment, peer query) will then 500
// rather than acknowledging a phantom success. Non-run entries pass
// through silently to preserve test ergonomics.

type noopEmitter struct{}

// errJournalNotWired is returned by noopEmitter for run lifecycle
// entries so callers fail loudly instead of silently dropping the
// canonical run record.
var errJournalNotWired = errors.New("journal emitter not wired (SetJournal not called); run lifecycle event dropped")

func (noopEmitter) Emit(_ context.Context, e journal.Entry) (string, error) {
	if strings.HasPrefix(string(e.Type), "run.") {
		slog.Default().Error("journal not wired — run lifecycle entry dropped",
			"entry_type", e.Type,
			"workspace_id", e.WorkspaceID,
			"trace_id", e.TraceID)
		return "", errJournalNotWired
	}
	if e.ID != "" {
		return e.ID, nil
	}
	return "noop", nil
}
func (noopEmitter) Flush(_ context.Context) error { return nil }

func NewRouter(db *sql.DB, jwtSecret string, logger *slog.Logger, opts ...RouterOption) (*Router, error) {
	// db is non-optional. NewAuthMiddleware joins to user_sessions on
	// every authed request, and the workspace-membership middleware
	// runs queries before any handler is reached. The previous code
	// accepted nil here and the failure mode was the first authed
	// request panicking with a nil-pointer dereference — fail at
	// construction so deployment-wiring bugs surface in startup logs
	// instead of production traffic.
	if db == nil {
		return nil, fmt.Errorf("new router: db is required")
	}
	validator, err := auth.NewJWTValidator(jwtSecret)
	if err != nil {
		return nil, fmt.Errorf("new router: create JWT validator: %w", err)
	}

	sessionsStore := sessions.NewDBStore(db)
	authMw := NewAuthMiddleware(validator, sessionsStore, db, logger)

	r := &Router{
		mux:           newRouteMux(),
		db:            db,
		logger:        logger,
		authMw:        authMw,
		sessionsStore: sessionsStore,
		// Seed the build identity from this binary's own VCS stamps so a
		// router whose wiring never reaches SetBuild still names the commit
		// it was compiled from (#1645). The ldflags values overwrite it.
		build: buildinfo.Resolve("", "", ""),
	}

	// Apply options before registering routes so that internalToken,
	// socketPath etc. are available during route setup.
	for _, opt := range opts {
		opt(r)
	}

	// Now that options have attached the hub and any extra revocation
	// notifiers, wrap the sessions store so every successful Revoke /
	// RevokeAllForUser — from ANY handler, present or future — pushes an
	// immediate disconnect to live WS/terminal connections (#1255 item 3)
	// instead of waiting for their 30s backstop sweeps. The wrap happens
	// BEFORE registerRoutes so every handler constructed there holds the
	// decorated store. authMw keeps the raw store — it only reads
	// (Get/TouchLastUsed) and never revokes.
	notifiers := make([]SessionRevocationNotifier, 0, len(r.revokeNotifiers)+1)
	if r.hub != nil {
		notifiers = append(notifiers, r.hub)
	}
	notifiers = append(notifiers, r.revokeNotifiers...)
	r.sessionsStore = newNotifyingSessionStore(r.sessionsStore, notifiers...)

	r.registerRoutes()

	// Close the method-routing hole every Go 1.22 ServeMux has: a request
	// whose method is not registered for a literal path falls through to a
	// sibling wildcard pattern instead of answering 405 (#1489). Must run
	// after every registrar and before the first request. See
	// router_mux.go.
	r.mux.sealMethodGuards()

	// Bound the request body on the public API surface (P3). The cap
	// wraps the mux beneath the rate limiters so it applies to every
	// auth/general/cred-test route, while internal sidecar IPC — which
	// routes straight to r.mux in routeWithRateLimiting and may carry
	// larger trusted payloads — is left to its own per-handler caps.
	capped := BodyCap(maxAPIBodyBytes)(r.mux)
	r.cappedMux = capped

	// Warm the signin timing-equalizer hash (lockout.go) off the
	// request path. Its generation moved out of package init (#967:
	// eager cost-12 bcrypt added ~290 ms to every CLI invocation of
	// the full binary); kicking it here restores the property that
	// even the server's very first unknown-email signin burns a
	// full-cost compare — while CLI processes, which never construct
	// a Router, never pay for it at all.
	go dummyBcryptHash()

	// Pre-wrap mux with rate limiters (once, not per-request). Values come
	// from the runtime store when one is installed, else the shipped defaults
	// (10 auth / 120 api / 60 cred-test req/min per IP — cred-test is tighter
	// to blunt its use as a credential-validation oracle).
	r.cliExemptNeg = newCLIExemptNegCache()
	authPM, apiPM, credPM := ratelimitcfg.DefaultFor(ratelimitcfg.KeyHTTPAuthPerMin),
		ratelimitcfg.DefaultFor(ratelimitcfg.KeyHTTPAPIPerMin),
		ratelimitcfg.DefaultFor(ratelimitcfg.KeyHTTPCredTestPerMin)
	revealPM := ratelimitcfg.DefaultFor(ratelimitcfg.KeyHTTPCredRevealPerMin)
	if r.ratelimitStore != nil {
		authPM = r.ratelimitStore.Value(ratelimitcfg.KeyHTTPAuthPerMin)
		apiPM = r.ratelimitStore.Value(ratelimitcfg.KeyHTTPAPIPerMin)
		credPM = r.ratelimitStore.Value(ratelimitcfg.KeyHTTPCredTestPerMin)
		revealPM = r.ratelimitStore.Value(ratelimitcfg.KeyHTTPCredRevealPerMin)
	}
	r.authRL = NewRateLimiter(authPM)
	r.apiRL = NewRateLimiter(apiPM)
	r.credTestRL = NewRateLimiter(credPM)
	r.credRevealRL = NewRateLimiter(revealPM)
	r.authRateLimitedMux = r.authRL.Middleware(capped)
	r.apiRateLimitedMux = r.apiRL.Middleware(capped)
	r.credTestRateLimitedMux = r.credTestRL.Middleware(capped)
	r.credRevealRateLimitedMux = r.credRevealRL.Middleware(capped)

	// Push runtime overrides onto the live limiters the moment an admin
	// changes one — no restart, no dropped in-flight buckets.
	if r.ratelimitStore != nil {
		r.ratelimitStore.OnChange(func() {
			r.authRL.SetReqPerMin(r.ratelimitStore.Value(ratelimitcfg.KeyHTTPAuthPerMin))
			r.apiRL.SetReqPerMin(r.ratelimitStore.Value(ratelimitcfg.KeyHTTPAPIPerMin))
			r.credTestRL.SetReqPerMin(r.ratelimitStore.Value(ratelimitcfg.KeyHTTPCredTestPerMin))
			r.credRevealRL.SetReqPerMin(r.ratelimitStore.Value(ratelimitcfg.KeyHTTPCredRevealPerMin))
		})
	}

	return r, nil
}

// SetScheduler attaches a ScheduleUpdater after construction (used by cmd_start).

func (r *Router) SetScheduler(su ScheduleUpdater) {
	r.scheduleUpdater = su
	if r.agentHandler != nil {
		r.agentHandler.SetScheduler(su)
	}
}

// SetSteerer wires the mid-turn steering delivery (chatbridge.Bridge)
// after route registration. The chat bridge is built later in the server
// boot sequence than the router; this flips the POST /chats/{id}/steer
// route from 503 to live.
func (r *Router) SetSteerer(s Steerer) {
	r.steerer = s
	if r.steerHandler != nil {
		r.steerHandler.SetSteerer(s)
	}
}

// RouterOption is a functional option for configuring a Router.

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Order matters: SecurityHeaders runs outermost so headers go on every
	// response (incl. 403s from the origin check); VersionHeader rides the
	// same everything-gets-it slot so even error responses let a skewed CLI
	// self-diagnose; EnforceOrigin runs next so a cross-site state-changing
	// request is rejected before it can even consume a rate-limit token
	// (and before per-handler logic); rate limiting and routing follow.
	SecurityHeaders(VersionHeader(r.version, EnforceOrigin(http.HandlerFunc(r.routeWithRateLimiting)))).ServeHTTP(w, req)
}

// Shutdown releases background resources the router owns — the port-expose
// registry's TTL purge goroutine and the provisioning handler's
// cleanup/GC loops. Safe to call multiple times. The server's shutdown
// path invokes this after the HTTP listener stops accepting new
// connections but before process exit, so neither loop keeps a hanging
// reference to the DB handle or the Docker client.

func (r *Router) Shutdown() {
	if r.provisioning != nil {
		r.provisioning.Stop()
	}
	if r.portExposeRegistry != nil {
		r.portExposeRegistry.Shutdown()
	}
}

// credTestStoredPathRe matches the per-credential test endpoint
// `/api/v1/credentials/{id}/test` exactly — anchored so a hypothetical
// future `/credentials/{id}/audit/test` doesn't accidentally fall under
// the tighter rate limiter as a forward-compat snag.
var credTestStoredPathRe = regexp.MustCompile(`^/api/v1/credentials/[^/]+/test$`)

// credRevealPathRe matches `/api/v1/credentials/{id}/reveal` exactly.
// Anchored for the same forward-compat reason as credTestStoredPathRe, and
// narrow on purpose: the reveal-policy and sensitivity routes are ordinary
// settings writes and belong on the general bucket.
var credRevealPathRe = regexp.MustCompile(`^/api/v1/credentials/[^/]+/reveal$`)

// changePasswordPath is POST /api/v1/users/me/password — the one route
// outside /api/v1/auth/ that verifies a caller-supplied CURRENT password
// and answers differently when it is wrong (#1513). It lives under
// /api/v1/users/ for URL reasons, not security ones, so it has to be named
// here rather than caught by the prefix. Matched as an exact path so the
// other /users/me/* routes (profile, avatar) stay on the general bucket.
const changePasswordPath = "/api/v1/users/me/password"

// isSelfServiceAuthPath reports whether path is an authenticated caller
// listing or revoking their OWN sessions / CLI tokens, as opposed to the
// credential-guessing surface (login, bootstrap, minting) the strict
// per-IP bucket exists to protect.
//
// Matching is exact-or-subpath, never a bare prefix: "/api/v1/auth/cli-token"
// (mint) and "/api/v1/auth/cli-tokens" (list) differ by one character.
func isSelfServiceAuthPath(path string) bool {
	switch path {
	case "/api/v1/auth/sessions", "/api/v1/auth/cli-tokens":
		return true
	}
	return strings.HasPrefix(path, "/api/v1/auth/sessions/") ||
		strings.HasPrefix(path, "/api/v1/auth/cli-tokens/")
}

// routeWithRateLimiting applies per-IP rate limiting based on the request path.

func (r *Router) routeWithRateLimiting(w http.ResponseWriter, req *http.Request) {
	path := req.URL.Path

	// Skip rate limiting for internal routes (sidecar IPC, X-Internal-Token
	// auth). serveInternal — not r.mux directly — is the single door onto that
	// surface; see its doc comment for why (#1501).
	//
	// The prefix is tested against the CLEANED path as well as the raw one.
	// `//api/v1/internal/credentials` does not literally start with
	// "/api/v1/internal/", so a raw-path-only test drops it out of this branch
	// and into the mux, which answers a non-canonical path with a 307 to the
	// cleaned one — handing back the very path the fence exists to keep quiet,
	// and reaching the internal surface by a door that is not serveInternal.
	// Cleaning first puts every spelling of the prefix through the same door.
	if isInternalPath(path) {
		r.serveInternal(w, req)
		return
	}

	// Read-only NextAuth GETs (/api/auth/session, /api/auth/csrf,
	// /api/auth/providers, /api/auth/signin, /api/auth/error) are polled on
	// EVERY dashboard page load — at least /session + /csrf per load. They
	// carry no credentials, so they must NOT share the tight 10/min login
	// brute-force bucket below: a handful of rapid refreshes would drain it,
	// the 429 reads to the frontend session probe as "logged out", and the
	// user gets bounced to /login (the refresh-logout bug). Route these safe
	// reads through the general 120/min API bucket instead. The method gate
	// keeps every credential-SUBMITTING /api/auth/ POST (login callback,
	// token refresh, signout) on the strict bucket, so brute-force protection
	// is untouched.
	if req.Method == http.MethodGet && strings.HasPrefix(path, "/api/auth/") {
		r.apiRateLimitedMux.ServeHTTP(w, req)
		return
	}

	// Managing your OWN sessions and CLI tokens lives under /api/v1/auth/
	// but is not a credential-guessing surface either, so it takes the same
	// exemption for the same reason. Revoking costs two requests (the write
	// plus the list refresh), so tidying up four stale tokens exceeded 10/min
	// and 429'd — and the Settings screen listing sessions 429'd with it,
	// reporting "couldn't load" for what was really a throttle.
	//
	// Everything here requires a valid session downstream and can only
	// REMOVE the caller's own access. Spamming it costs an attacker
	// nothing they could not achieve with one request. Unlike the GET
	// exemption above this one covers writes, which is the whole point —
	// revocation is a write.
	//
	// Note the exact matches: /api/v1/auth/cli-token (singular) MINTS a
	// credential and deliberately stays strict, so a prefix test on
	// "cli-token" would widen exactly the route we mean to keep narrow.
	if isSelfServiceAuthPath(path) {
		r.apiRateLimitedMux.ServeHTTP(w, req)
		return
	}

	// Stricter rate limiting for auth endpoints — plus the password change,
	// which is a credential-verification surface wearing a /users/me/ URL.
	// It has to be matched HERE, above the general branch: that branch
	// exempts authenticated CLI tokens from the per-IP bucket entirely
	// (#1333), and the attacker this limiter is for is holding exactly such
	// a credential. Changing your own password ten times a minute is not a
	// workflow, so nobody legitimate notices.
	if strings.HasPrefix(path, "/api/auth/") || strings.HasPrefix(path, "/api/v1/auth/") ||
		path == "/api/v1/bootstrap" || path == changePasswordPath {
		r.authRateLimitedMux.ServeHTTP(w, req)
		return
	}

	// Tighter limit on credential test endpoints — they hit external
	// provider APIs and could otherwise be used as a free key-validation
	// oracle for stolen secrets.
	if path == "/api/v1/credentials/test" || credTestStoredPathRe.MatchString(path) {
		r.credTestRateLimitedMux.ServeHTTP(w, req)
		return
	}

	// Tightest bucket in the system, for the only route that hands back a
	// stored secret in plaintext (PRD-CREDENTIALS-V2-2026 §2.6 L6). It has
	// to be matched HERE, above the general branch: that branch exempts
	// authenticated CLI tokens from the per-IP bucket entirely (#1333), and
	// an unthrottled reveal for token-bearing callers is precisely the hole
	// this limiter exists to close.
	if credRevealPathRe.MatchString(path) {
		r.credRevealRateLimitedMux.ServeHTTP(w, req)
		return
	}

	// General API rate limiting — except authenticated CLI callers, which
	// are exempt from the per-IP bucket entirely (#1333). A bulk operation
	// (crewship seed, template import) fires far more requests than the
	// 120/min budget in seconds, 429ing mid-run and leaving a half-seeded
	// tenant. The limiter must still cover everyone else: a request must
	// present a token that is REAL — cryptographically hash-matched,
	// non-revoked, non-expired (IsValidCLIToken, not just "shaped like" a
	// CLI token) — before it skips the bucket, so an unauthenticated
	// caller can't spoof the `crewship_cli_`/`crewship_admin_` prefix to
	// dodge throttling. The exempted request still goes through the real
	// RequireAuth validation (with its audit trail) downstream in r.mux —
	// IsValidCLIToken here is a side-effect-free pre-check.
	//
	// The validity check runs BEFORE the limiter on purpose: exempt CLI
	// traffic must not drain the per-IP bucket it shares with a browser
	// behind the same NAT. To keep that ordering from becoming a DoS
	// amplifier (spoofed prefix → unthrottled DB lookup per request),
	// failed lookups land in r.cliExemptNeg — see ratelimit_cli_negcache.go
	// — and a cached failure skips the DB and falls into the normal bucket.
	// Note this branch is dead for auth paths: the stricter auth bucket
	// above matched first, so a valid CLI token cannot exempt login/
	// bootstrap traffic (covered by TestRouteWithRateLimiting_AuthPath_
	// ValidCLIToken_StillAuthRateLimited).
	if strings.HasPrefix(path, "/api/") {
		if token := extractToken(req); token != "" && IsCLIToken(token) {
			key := sha256.Sum256([]byte(token))
			now := time.Now()
			if !r.cliExemptNeg.has(key, now) {
				if cliExemptDBLookupHook != nil {
					cliExemptDBLookupHook()
				}
				if IsValidCLIToken(req.Context(), r.db, token) {
					r.cappedMux.ServeHTTP(w, req)
					return
				}
				r.cliExemptNeg.put(key, now)
			}
		}
		r.apiRateLimitedMux.ServeHTTP(w, req)
		return
	}

	// Static files / other paths — no rate limiting
	r.mux.ServeHTTP(w, req)
}
