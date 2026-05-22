package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/episodic"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/license"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/policy"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ws"
	dockerclient "github.com/docker/docker/client"
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

type Router struct {
	mux             *http.ServeMux
	db              *sql.DB
	logger          *slog.Logger
	authMw          *AuthMiddleware
	sessionsStore   sessions.Store
	socketPath      string
	internalToken   string
	internalBaseURL string
	// internalLoopbackURL is for in-daemon HTTP calls (e.g. the webhook
	// secret resolver) that hit our own internal API. internalBaseURL is
	// designed for containers (host.docker.internal:<port>) and dialing
	// it from the daemon depends on the host's /etc/hosts mapping —
	// fragile on multi-host lab networks. Use the loopback variant
	// (typically 127.0.0.1:<port>) instead when set. (Issue #535.)
	internalLoopbackURL    string
	hub                    *ws.Hub
	orch                   *orchestrator.Orchestrator
	keeperGK               gatekeeper.Evaluator
	keeperSecrets          SecretGetter
	keeperContainer        provider.ContainerProvider
	keeperConfig           *config.KeeperConfig
	keeperConvReader       ConversationReader
	missionCallback        MissionCallback
	scheduleUpdater        ScheduleUpdater
	logWriter              *logcollector.Writer
	allowSignup            bool
	googleClientID         string
	googleSecret           string
	authBaseURL            string
	license                *license.License
	agentHandler           *AgentHandler
	storagePath            string // base path for crew file storage
	catalogFetcher         *devcontainer.CatalogFetcher
	runtimeFetcher         *devcontainer.RuntimeFetcher
	dockerClient           *dockerclient.Client
	featureCacheDir        string
	portExposeRegistry     *PortExposeRegistry // closed via Shutdown() on server stop
	portExposePublicURL    string              // e.g. http://crewship.example.com:8080, used to build capability URLs
	portExposeNetwork      string              // Docker bridge name; falls back to handler default when empty
	authRateLimitedMux     http.Handler        // mux wrapped with auth rate limiter
	apiRateLimitedMux      http.Handler        // mux wrapped with general API rate limiter
	credTestRateLimitedMux http.Handler        // mux wrapped with /credentials/test limiter (defence against credential-validation oracle abuse)
	journal                journal.Emitter     // Crew Journal emitter; nil → emits become no-ops so dev builds without the server-level wiring still work
	consolidator           *consolidate.Consolidator
	consolidateMemoryRoot  string
	// outputBasePath is the host-side root that the container
	// provider bind-mounts. PR-E F6 uses this to resolve per-agent
	// and per-crew PERSONA + peers/ paths without going through the
	// container. Empty → persona / peers endpoints respond 503
	// "storage not configured" rather than 404.
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
	// PipelinesHandler is exposed (capitalised) so the orchestrator
	// boot path can hand it the AgentRunner adapter post-construction.
	// The router builds handlers before the orchestrator is fully
	// initialised, so two-phase wiring is the cheapest fix.
	PipelinesHandler *PipelineHandler

	// authHandler is the live AuthHandler created during route
	// registration. Stored on the Router so server.New can call
	// MaybeGenerateSetupToken (Patch C) on the same instance that
	// /api/v1/bootstrap actually dispatches to — otherwise the armed
	// token lives on a handler the dispatcher never reaches.
	authHandler *AuthHandler

	// version is the ldflags-injected binary version (e.g. "v0.1.0-beta.1"
	// or "dev" for local builds). Surfaced on GET /api/v1/system/version
	// so the web UI can render an "update available" banner.
	version string

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
func (r *Router) AuxModels() llm.AuxiliaryModels {
	if !r.auxModelsSet {
		return llm.DefaultAuxiliaryModels()
	}
	return r.auxModels
}

// SetVersion records the binary version for the version-info endpoint.
// Called from cmd_start.go after construction because the version lives
// in package main as an ldflags-injected var and can't be referenced
// from internal/api.
func (r *Router) SetVersion(v string) {
	r.version = v
}

// Provisioning returns the registered ProvisioningHandler so wiring code (e.g.
// cmd_start) can hand it to chatbridge as a ProvisioningEnqueuer. Returns nil
// when registerRoutes hasn't run yet (e.g. tests that build a Router by hand).
func (r *Router) Provisioning() *ProvisioningHandler {
	return r.provisioning
}

// AuthHandler returns the registered AuthHandler so server startup code can
// call MaybeGenerateSetupToken on the same instance the /api/v1/bootstrap
// route dispatches to. Returns nil when registerAuthRoutes hasn't run yet
// (handler-only tests that build a Router by hand).
func (r *Router) AuthHandler() *AuthHandler {
	return r.authHandler
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
		mux:           http.NewServeMux(),
		db:            db,
		logger:        logger,
		authMw:        authMw,
		sessionsStore: sessionsStore,
	}

	// Apply options before registering routes so that internalToken,
	// socketPath etc. are available during route setup.
	for _, opt := range opts {
		opt(r)
	}

	r.registerRoutes()

	// Pre-wrap mux with rate limiters (once, not per-request)
	r.authRateLimitedMux = NewRateLimiter(10).Middleware(r.mux)     // 10 req/min per IP
	r.apiRateLimitedMux = NewRateLimiter(120).Middleware(r.mux)     // 120 req/min per IP
	r.credTestRateLimitedMux = NewRateLimiter(60).Middleware(r.mux) // 60 req/min per IP — tighter on /credentials/test to limit its use as a credential-validation oracle (a tenant should never need 60 manual test clicks per minute)

	return r, nil
}

// SetScheduler attaches a ScheduleUpdater after construction (used by cmd_start).

func (r *Router) SetScheduler(su ScheduleUpdater) {
	r.scheduleUpdater = su
	if r.agentHandler != nil {
		r.agentHandler.SetScheduler(su)
	}
}

// RouterOption is a functional option for configuring a Router.

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Order matters: SecurityHeaders runs outermost so headers go on every
	// response (incl. 403s from the origin check); EnforceOrigin runs next
	// so a cross-site state-changing request is rejected before it can
	// even consume a rate-limit token (and before per-handler logic);
	// rate limiting and routing follow.
	SecurityHeaders(EnforceOrigin(http.HandlerFunc(r.routeWithRateLimiting))).ServeHTTP(w, req)
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

// routeWithRateLimiting applies per-IP rate limiting based on the request path.

func (r *Router) routeWithRateLimiting(w http.ResponseWriter, req *http.Request) {
	path := req.URL.Path

	// Skip rate limiting for internal routes (sidecar IPC, X-Internal-Token auth)
	if strings.HasPrefix(path, "/api/v1/internal/") {
		r.mux.ServeHTTP(w, req)
		return
	}

	// Stricter rate limiting for auth endpoints
	if strings.HasPrefix(path, "/api/auth/") || strings.HasPrefix(path, "/api/v1/auth/") || path == "/api/v1/bootstrap" {
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

	// General API rate limiting
	if strings.HasPrefix(path, "/api/") {
		r.apiRateLimitedMux.ServeHTTP(w, req)
		return
	}

	// Static files / other paths — no rate limiting
	r.mux.ServeHTTP(w, req)
}
