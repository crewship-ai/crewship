package server

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	goapi "github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/episodic"
	"github.com/crewship-ai/crewship/internal/fileserver"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/license"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/llmproxy"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	dockerprovider "github.com/crewship-ai/crewship/internal/provider/docker"
	"github.com/crewship-ai/crewship/internal/telemetry"
	"github.com/crewship-ai/crewship/internal/terminal"
	"github.com/crewship-ai/crewship/internal/ws"
)

// Server is the main crewship process, wiring together the HTTP server, IPC
// listener, WebSocket hub, orchestrator, scheduler, and all supporting services.

type Server struct {
	httpServer        *http.Server
	ipcServer         *http.Server
	mux               *http.ServeMux
	ipcMux            *http.ServeMux
	spaHandler        http.Handler
	cfg               *config.Config
	logger            *slog.Logger
	wsHub             *ws.Hub
	orchestrator      *orchestrator.Orchestrator
	missionEngine     *orchestrator.MissionEngine
	container         provider.ContainerProvider
	storage           provider.StorageProvider
	state             provider.StateProvider
	logWriter         *logcollector.Writer
	logReader         *logcollector.Reader
	convStore         *conversation.Store
	tokenPool         *llmproxy.TokenPool
	tokenSyncer       *llmproxy.TokenSyncer
	credMonitor       *llmproxy.CredentialMonitor
	debugLogs         *logging.RingBuffer
	db                *sql.DB
	apiRouter         *goapi.Router
	fileWatcher       *fileserver.Watcher
	watchedCrews      sync.Map
	statsCollector    *StatsCollector
	terminalHandler   *terminal.Handler
	journalWriter     *journal.Writer
	consolidator      *consolidate.Consolidator
	telemetryShutdown func()
	startedAt         time.Time
	runCtx            context.Context
	runCancel         context.CancelFunc

	// fileJournalPtr is the pointer the file-watcher closure dereferences
	// to emit file.written entries. Stored on the struct (instead of a
	// local in New()) so Shutdown can clear it BEFORE journalWriter.Close
	// — otherwise a late filesystem event could try to emit through a
	// draining/closed writer.
	fileJournalPtr atomic.Pointer[journal.Writer]
}

// Deps holds the external dependencies injected into the server at startup.

type Deps struct {
	Container provider.ContainerProvider
	Storage   provider.StorageProvider
	State     provider.StateProvider
	DebugLogs *logging.RingBuffer
	DB        *sql.DB
	WebFS     fs.FS
	License   *license.License
}

// Close releases resources held by the dependencies (e.g. state provider).

func (d *Deps) Close() {
	if d == nil {
		return
	}
	if c, ok := d.State.(interface{ Close() error }); ok {
		c.Close()
	}
}

// New creates and configures a Server with all subsystems (HTTP, IPC, WebSocket,
// orchestrator, scheduler, stats collector, etc.) wired together.

func New(cfg *config.Config, logger *slog.Logger, deps *Deps) *Server {
	// Refuse to start with rate limiting disabled in a production env.
	// Catches the deployment-drift mode where CREWSHIP_RATELIMIT_DISABLED=true
	// (or the legacy CREWSHIP_DISABLE_RATELIMIT) leaks from a dev shell into
	// a prod deploy and silently exposes /api/auth/* to credential stuffing.
	goapi.MustNotDisableRateLimitInProd()

	mux := http.NewServeMux()
	ipcMux := http.NewServeMux()

	var ctr provider.ContainerProvider
	var sto provider.StorageProvider
	var sta provider.StateProvider

	var debugLogs *logging.RingBuffer
	if deps != nil {
		ctr = deps.Container
		sto = deps.Storage
		sta = deps.State
		debugLogs = deps.DebugLogs
	}

	orch := orchestrator.New(ctr, sta, logger)
	if cfg.Container.SidecarEnabled {
		orch.SetSidecarEnabled(true)
		logger.Info("sidecar proxy enabled for credential injection")
	}
	if cfg.Keeper.Enabled {
		orch.SetKeeperEnabled(true)
	}

	// Calculate IPC base URL for containers to reach this server.
	hostAddr := "host.docker.internal" // default for Docker
	if ctr != nil {
		if hap, ok := ctr.(provider.HostAddressProvider); ok {
			if addr := hap.HostAddress(); addr != "" {
				hostAddr = addr
			}
		}
	}
	if strings.Contains(hostAddr, ":") {
		hostAddr = "[" + hostAddr + "]"
	}
	ipcBase := fmt.Sprintf("http://%s:%d", hostAddr, cfg.Server.Port)

	// Wire IPC config so lead agents can reach crewshipd for assignment routing.
	// The host address depends on the container provider:
	//   Docker: host.docker.internal (injected via ExtraHosts)
	//   Apple:  actual host IP (containers run in their own VMs)
	if cfg.Auth.InternalToken != "" {
		orch.SetIPCConfig(ipcBase, cfg.Auth.InternalToken)
		logger.Info("orchestrator IPC config set", "base_url", ipcBase)
	}
	logW := logcollector.NewWriter(cfg.Storage.LogPath, logger)
	logR := logcollector.NewReader(cfg.Storage.LogPath)
	convStore := conversation.NewStore(cfg.Storage.BasePath, logger)

	orch.SetConversationStore(convStore)

	// Auth is non-optional. Previously a missing NEXTAUTH_SECRET would
	// silently skip JWT validator construction, which then bubbled up
	// as a Hub running without auth (revoke checks no-op'd, every
	// upgrade accepted). The NEXTAUTH_SECRET-MUST-be-set rule is
	// already documented in CLAUDE.md after the prod misfire — make
	// it enforced at startup instead of leaving it for the user to
	// notice via 404'd routes. ws.NewHub panics on nil validator, so
	// missing secret takes the process down at startup with a clear
	// "jwtValidator required" message.
	if cfg.Auth.JWTSecret == "" {
		logger.Error("NEXTAUTH_SECRET is required: cannot start server without JWT validator")
		panic("NEXTAUTH_SECRET not set — refusing to start an unauthenticated server")
	}
	jwtValidator, err := auth.NewJWTValidator(cfg.Auth.JWTSecret)
	if err != nil {
		logger.Error("create JWT validator", "error", err)
		panic(fmt.Sprintf("create JWT validator: %v", err))
	}
	logger.Info("JWT validator configured for WebSocket auth")

	// Production startup MUST have a real DB-backed sessions store so
	// the WS hub enforces revocation on upgrade. The previous code
	// silently fell back to ws.NopSessionsForTests when deps.DB was
	// nil, which let CodeRabbit notice that an unconfigured server
	// would still happily upgrade WS connections without revocation
	// checks — the inverse of what the session-lifecycle work is for.
	//
	// Tests that exercise Server.New() without a real DB (handlers in
	// isolation, etc.) can either pass deps with an in-memory SQLite
	// or replace the resulting hub themselves; baking the bypass into
	// production startup wasn't worth saving them three lines.
	if deps == nil || deps.DB == nil {
		logger.Error("server.New: deps.DB is required (sessions store backing the WS hub)")
		panic("deps.DB not set — refusing to start a server without revocable sessions")
	}
	sessionsStore := sessions.NewDBStore(deps.DB)
	wsHub := ws.NewHub(logger, nil, jwtValidator, sessionsStore)

	// File watcher broadcasts real-time file events to WebSocket clients on
	// the crew:{crewID} channel AND emits file.written journal entries so
	// Crow's Nest's Filesystem panel actually fills. The journal pointer
	// lives on the Server struct (initialized below) because the journal
	// writer is constructed later (it depends on deps.DB), and Shutdown
	// needs to be able to clear the pointer before journalWriter.Close so
	// a late filesystem event doesn't try to emit through a draining
	// writer.
	//
	// We declare a sentinel here that closures over `serverPtr` (set
	// below after the Server is constructed) so the closure dereferences
	// the per-Server atomic instead of a local.
	var serverPtr *Server
	fileWatcher := fileserver.NewWatcher(cfg.Storage.BasePath, logger, func(crewID string, event fileserver.FileEvent) {
		wsHub.BroadcastChannel("crew", crewID, "file.event", event)
		if serverPtr == nil {
			return
		}
		if j := serverPtr.fileJournalPtr.Load(); j != nil {
			emitFileWrittenEntry(j, crewID, event, logger)
		}
	})

	var statsCollector *StatsCollector
	if ctr != nil {
		statsCollector = NewStatsCollector(ctr, wsHub, logger, 5*time.Second)
		// The orchestrator's stats-register callback is wired AFTER the
		// Server struct is constructed (further down in this function)
		// because the callback also needs to start the file watcher,
		// which is a method on Server. See the SetStatsRegisterCallback
		// call after `s := &Server{...}` below.
	}

	tokenPool := llmproxy.NewTokenPool(logger)

	var tokenSyncer *llmproxy.TokenSyncer
	var credMonitor *llmproxy.CredentialMonitor
	if cfg.LLMProxy.Enabled && cfg.Auth.InternalToken == "" {
		logger.Warn("LLM proxy enabled but INTERNAL_TOKEN not set, proxy features disabled")
	}
	if cfg.LLMProxy.Enabled && cfg.Auth.InternalToken != "" {
		internalToken := cfg.Auth.InternalToken
		tokenSyncer = llmproxy.NewTokenSyncer(
			tokenPool, cfg.Auth.NextjsURL, internalToken,
			cfg.LLMProxy.TokenSyncInterval, logger,
		)
		credMonitor = llmproxy.NewCredentialMonitor(
			tokenPool, cfg.Auth.NextjsURL, internalToken,
			cfg.LLMProxy.HealthCheckInterval, logger,
		)
		credMonitor.SetOnChange(func(connID string, oldStatus, newStatus llmproxy.ConnectionStatus) {
			wsHub.Broadcast("providers", ws.ServerMessage{
				Type:    "provider_status",
				Channel: "providers",
				Payload: map[string]string{
					"connection_id": connID,
					"old_status":    string(oldStatus),
					"new_status":    string(newStatus),
				},
			})
		})
	}

	// Create MissionEngine for orchestrating multi-task missions.
	// deps + deps.DB are guaranteed non-nil by the panic guard above (the
	// sessions store needs them too); the redundant nil check that was
	// here confused staticcheck SA5011 into flagging deps.DB usage at
	// the sessions.NewDBStore call.
	missionEngine := orchestrator.NewMissionEngine(deps.DB, orch, wsHub, logger)

	// Create terminal handler for interactive container shells.
	var termHandler *terminal.Handler
	if ctr != nil && jwtValidator != nil {
		var termDB *sql.DB
		if deps != nil {
			termDB = deps.DB
		}
		termHandler = terminal.New(ctr, jwtValidator, termDB, logger)
		logger.Info("terminal handler configured")
	}

	s := &Server{
		mux:             mux,
		ipcMux:          ipcMux,
		cfg:             cfg,
		logger:          logger,
		wsHub:           wsHub,
		orchestrator:    orch,
		missionEngine:   missionEngine,
		container:       ctr,
		storage:         sto,
		state:           sta,
		logWriter:       logW,
		logReader:       logR,
		convStore:       convStore,
		tokenPool:       tokenPool,
		tokenSyncer:     tokenSyncer,
		credMonitor:     credMonitor,
		debugLogs:       debugLogs,
		fileWatcher:     fileWatcher,
		statsCollector:  statsCollector,
		terminalHandler: termHandler,
	}
	if deps != nil {
		s.db = deps.DB
	}
	// Promote the closure's view of the server now that it exists.
	// The file-watcher closure declared above reads via this pointer.
	serverPtr = s

	// Wire the orchestrator's container-ready callback now that `s` is
	// constructed. The callback fans out to two concerns: (1) register
	// the container with the stats poller so container.metrics journal
	// entries flow, (2) ensure the file watcher is running for the crew
	// so file.written entries flow. Both are idempotent — repeated
	// calls for the same container/crew are no-ops.
	if statsCollector != nil {
		sc := statsCollector
		orch.SetStatsRegisterCallback(func(containerID, crewID, workspaceID string) {
			sc.Register(containerID, crewID, workspaceID)
			s.ensureFileWatcher(crewID)
		})
	}

	s.registerRoutes()
	s.registerIPCRoutes()

	// Mount Go API routes when database is available
	if deps != nil && deps.DB != nil && cfg.Auth.JWTSecret != "" {
		var opts []goapi.RouterOption
		if deps.License != nil {
			opts = append(opts, goapi.WithLicense(deps.License))
		}
		// Crew Journal emitter lives for the lifetime of the server; the
		// batched writer owns a goroutine and a buffered channel, so we
		// stash it on Server so Shutdown can Close it and flush pending
		// entries before the process exits.
		s.journalWriter = journal.NewWriter(deps.DB, logger, journal.WriterOptions{})
		opts = append(opts, goapi.WithJournal(s.journalWriter))

		// Wire the journal into the orchestrator so Docker exec, network,
		// and filesystem hook points inside the orchestrator can emit
		// Crow's Nest entries with full scope (workspace / crew / agent /
		// mission). The adapter bridges the orchestrator's narrow
		// JournalEmitter interface to the full journal.Emitter so the
		// orchestrator package stays independent of internal/journal.
		orch.SetJournal(newOrchestratorJournalAdapter(s.journalWriter))

		// Wire the journal into the stats collector so Crow's Nest's
		// resource sparklines and replay view get container.metrics rows
		// every 30s (or sooner on >10pp CPU/RAM swings). Live WebSocket
		// broadcast is unaffected — this is purely the persistence layer.
		if s.statsCollector != nil {
			s.statsCollector.SetJournal(s.journalWriter)
		}

		// Wire the journal into the file watcher's lazy pointer so file
		// events emitted by the fsnotify goroutine produce file.written
		// journal entries. The closure above reads s.fileJournalPtr so
		// this Store wakes up in-flight handlers without re-construction.
		s.fileJournalPtr.Store(s.journalWriter)

		// Wire the three orchestrator integration points (hooks dispatch,
		// approval gate, episodic recall) now that the journal is
		// available. Each adapter is small and lives in
		// orchestrator_adapters.go. Episodic recall needs an Ollama
		// embedder; if Keeper Ollama isn't configured we pass nil so
		// recall returns empty silently rather than blocking every run
		// on an unreachable service.
		orch.SetHooksDispatcher(newHooksAdapter(deps.DB, s.journalWriter))
		orch.SetApprovalGate(newApprovalGateAdapter(deps.DB, s.journalWriter))
		orch.SetPresenceTracker(newPresenceAdapter(deps.DB, s.journalWriter, logger))
		orch.SetMemoryMetrics(newMemoryMetricsAdapter(deps.DB))
		var embedder episodic.Embedder
		if cfg.Keeper.OllamaURL != "" {
			// nomic-embed-text is the expected model on the Ollama host
			// per episodic/embedder.go defaults. Workspaces can override
			// via future config; for now use the same base URL as Keeper.
			embedder = episodic.NewOllamaEmbedder(cfg.Keeper.OllamaURL)
		}
		orch.SetEpisodicRecall(newEpisodicRecallAdapter(deps.DB, embedder))

		// OTel GenAI telemetry. Endpoint defaults to OTEL_EXPORTER_OTLP_ENDPOINT
		// and silently degrades to a noop tracer when unset so local dev
		// runs without an observability stack keep working.
		otelEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
		if otelShutdown, err := telemetry.Init(context.Background(), otelEndpoint, "crewshipd"); err != nil {
			logger.Warn("telemetry init failed, falling back to noop tracer", "err", err)
		} else {
			s.telemetryShutdown = otelShutdown
			// Wire the current span's trace+span ID into every journal
			// entry emit. No-op when the tracer is noop, so entries just
			// get empty trace IDs (backwards compatible).
			telemetry.RegisterJournalResolver()
			if otelEndpoint != "" {
				logger.Info("OTel GenAI telemetry enabled", "endpoint", otelEndpoint)
			}
		}
		opts = append(opts, goapi.WithSocketPath(cfg.IPC.SocketPath))
		opts = append(opts, goapi.WithInternalToken(cfg.Auth.InternalToken))
		opts = append(opts, goapi.WithInternalBaseURL(ipcBase))
		// Port-expose capability URLs hand an externally reachable origin
		// back to the agent. CREWSHIP_PUBLIC_URL must be set to the host a
		// user's browser can actually reach (e.g. http://192.168.1.201:8080).
		// Left unset, the port-expose endpoint returns 503 with a message
		// pointing at this env var — better to fail loudly than to hand out
		// localhost URLs that silently 404 for anyone not on the host.
		publicURL := os.Getenv("CREWSHIP_PUBLIC_URL")
		if publicURL == "" {
			logger.Warn("CREWSHIP_PUBLIC_URL not set — /expose-port will return 503 until configured",
				"fix", "export CREWSHIP_PUBLIC_URL=http://<reachable-host>:8080")
		}
		opts = append(opts, goapi.WithPortExposePublicURL(publicURL))
		// Crew container Docker network — must match what the orchestrator
		// attaches containers to. Without this, multi-instance dev.sh
		// deployments (crewship-1-agents, crewship-2-agents, ...) would
		// silently 502 every /expose-port call because the handler defaults
		// to "crewship-agents" and the container is on a different bridge.
		if cfg.Container.Network != "" {
			opts = append(opts, goapi.WithPortExposeNetwork(cfg.Container.Network))
		}
		opts = append(opts, goapi.WithHub(wsHub))
		opts = append(opts, goapi.WithOrchestrator(orch))
		opts = append(opts, goapi.WithLogWriter(logW))
		if missionEngine != nil {
			opts = append(opts, goapi.WithMissionCallback(missionEngine))
		}
		opts = append(opts, goapi.WithAllowSignup(cfg.Auth.AllowSignup))
		if cfg.Auth.GoogleClientID != "" {
			opts = append(opts, goapi.WithGoogleOAuth(cfg.Auth.GoogleClientID, cfg.Auth.GoogleSecret, cfg.Auth.NextjsURL))
		}
		opts = append(opts, goapi.WithStoragePath(cfg.Storage.BasePath))

		// Dynamic catalog fetchers (devcontainer features + mise runtimes).
		// They default to cached / embedded data; a background goroutine
		// refreshes them at startup and every 6h.
		catalogCacheDir := ""
		if cfg.Storage.BasePath != "" {
			catalogCacheDir = cfg.Storage.BasePath + "/catalog-cache"
		}
		catalogFetcher := devcontainer.NewCatalogFetcher(catalogCacheDir, logger)
		runtimeFetcher := devcontainer.NewRuntimeFetcher(catalogCacheDir, logger)
		opts = append(opts, goapi.WithCatalogFetcher(catalogFetcher))
		opts = append(opts, goapi.WithRuntimeFetcher(runtimeFetcher))

		// Wire Docker SDK client into the provisioning handler so the
		// /api/v1/crews/{id}/provision trigger endpoint can actually run
		// the devcontainer provisioner (download features, exec installs,
		// docker commit). If the container provider isn't Docker (or
		// doesn't expose its client), provisioning returns 503 at runtime.
		if dp, ok := ctr.(*dockerprovider.Provider); ok {
			if dc := dp.DockerClient(); dc != nil {
				opts = append(opts, goapi.WithDockerClient(dc))
				featureCacheDir := ""
				if cfg.Storage.BasePath != "" {
					featureCacheDir = cfg.Storage.BasePath + "/feature-cache"
				}
				opts = append(opts, goapi.WithFeatureCacheDir(featureCacheDir))
			}
		}

		// Kick off initial + periodic refresh without blocking startup.
		startCatalogRefresh(catalogFetcher, runtimeFetcher, logger)

		// Wire Keeper gatekeeper (Ollama-based credential access control)
		opts = append(opts, goapi.WithKeeperConfig(&cfg.Keeper))
		if cfg.Keeper.Enabled {
			// Wrap the Ollama provider with the full middleware stack so
			// gatekeeper LLM calls are cost-tracked, guardrail-scanned,
			// and trace-instrumented. Local Ollama has zero dollar cost
			// but the paymaster ledger still records token counts, which
			// feeds the cache-hit metric and per-agent usage visibility.
			base := llm.NewOllama(cfg.Keeper.OllamaURL, cfg.Keeper.Model)
			wrapped := llm.Middleware(base, s.journalWriter, deps.DB)
			gk := gatekeeper.New(wrapped, cfg.Keeper.Model, logger)
			opts = append(opts, goapi.WithKeeperGatekeeper(gk))
			logger.Info("keeper gatekeeper enabled", "ollama_url", cfg.Keeper.OllamaURL, "model", cfg.Keeper.Model)
		} else {
			logger.Info("keeper gatekeeper disabled (set KEEPER_ENABLED=true or KEEPER_OLLAMA_URL to enable)")
		}

		// Wire keeper execute: load secrets store and pass container provider
		if ctr != nil {
			opts = append(opts, goapi.WithKeeperContainer(ctr))
			secretsStore := newSecretsAdapter(context.Background(), deps.DB, logger)
			if secretsStore != nil {
				opts = append(opts, goapi.WithKeeperSecrets(secretsStore))
			}
		}

		// Wire conversation history so Keeper can verify agent intent against actual chat
		if convStore != nil {
			opts = append(opts, goapi.WithKeeperConversations(&convStoreAdapter{store: convStore}))
		}

		// Build the shared Consolidator so the router-backed manual
		// trigger (/api/v1/consolidate/run) and the 6-hourly background
		// runner use the same instance. Summarizer is nil when Ollama
		// isn't configured; the handler surfaces that as 202 +
		// "no summarizer configured, skipping" so operators see the
		// feature is off without the request failing outright.
		var summarizerEarly consolidate.SummarizerClient
		if s.cfg.Keeper.OllamaURL != "" && s.cfg.Keeper.Model != "" {
			summBase := llm.NewOllama(s.cfg.Keeper.OllamaURL, s.cfg.Keeper.Model)
			summWrapped := llm.Middleware(summBase, s.journalWriter, s.db)
			summarizerEarly = newLLMSummarizer(summWrapped, s.cfg.Keeper.Model)
		}
		s.consolidator = &consolidate.Consolidator{
			DB:         deps.DB,
			Journal:    s.journalWriter,
			Summarizer: summarizerEarly,
			Logger:     logger,
		}
		opts = append(opts, goapi.WithConsolidator(s.consolidator))
		opts = append(opts, goapi.WithConsolidateMemoryRoot("/crew/shared/.memory"))

		apiRouter, err := goapi.NewRouter(deps.DB, cfg.Auth.JWTSecret, logger, opts...)
		if err != nil {
			logger.Error("failed to create API router", "error", err)
		} else {
			s.apiRouter = apiRouter
			mux.Handle("/api/", apiRouter)
			// /exposed/{token}/... needs two things: (a) combinedHandler has
			// to pick s.mux over spaHandler for this prefix (done there),
			// and (b) s.mux needs a route entry so the request actually
			// reaches apiRouter. Both.
			mux.Handle("/exposed/", apiRouter)
			logger.Info("Go API routes mounted")

			// Pipeline AgentRunner is wired in cmd_start.go after
			// the chatbridge.ChatResolver is built — the runner
			// needs the resolver + IPC base URL to look up agent
			// configs the same way the chat handler does. Until
			// the runner is wired, /pipelines/.../run returns 503.
			//
			// We attach the journal emitter here because that
			// dependency is local to the server, not the start
			// command. Runner attach happens in cmd_start.go.
			if apiRouter.PipelinesHandler != nil && s.journalWriter != nil {
				apiRouter.PipelinesHandler.SetJournal(s.journalWriter)
			}
		}
		// Static UI: wrap mux with SPA handler to avoid ServeMux redirect issues
		if deps.WebFS != nil {
			s.spaHandler = goapi.StaticFileHandler(deps.WebFS)
			logger.Info("serving embedded static UI")
		}
	}

	var mainHandler http.Handler = mux
	if s.spaHandler != nil {
		mainHandler = s.combinedHandler()
	}
	// V-10: Wrap with security headers middleware
	mainHandler = securityHeadersMiddleware(mainHandler)

	s.httpServer = &http.Server{
		Addr:        fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler:     mainHandler,
		ReadTimeout: 15 * time.Second,
		// WriteTimeout is deliberately unset (0 = no timeout) because
		// x/net/websocket does not hijack the connection, so Go's HTTP
		// server applies WriteTimeout to the entire WebSocket lifetime,
		// killing long-lived connections after the deadline. The WS hub
		// handles keep-alive via its own ping/pong mechanism.
		IdleTimeout: 120 * time.Second,
	}

	s.ipcServer = &http.Server{
		Handler:      ipcMux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	return s
}

// combinedHandler routes /api/, /exposed/, /healthz, /readyz, /metrics, /ws
// to the mux, and everything else to the SPA static file handler.
// /exposed/{token}/... must bypass the SPA handler so the port-expose reverse
// proxy sees the request instead of serving the Next.js fallback.

func (s *Server) combinedHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		// Reject sensitive paths up front so the SPA fallback can't
		// double as a 200-on-everything signal that masks them. Pre-fix
		// scanners hitting /.env, /.git/HEAD, /debug/vars, etc. all got
		// `200 text/html` (the SPA shell), which (a) creates noise in
		// pentest reports and (b) hides a real leak if a future reverse
		// proxy ever serves the actual file at the same path. Hard 404
		// keeps the surface honest. F-004.
		if isSensitiveStaticPath(path) {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(path, "/api/") ||
			strings.HasPrefix(path, "/exposed/") ||
			path == "/healthz" || path == "/readyz" ||
			path == "/metrics" || path == "/ws" ||
			path == "/ws/terminal" {
			s.mux.ServeHTTP(w, r)
			return
		}
		s.spaHandler.ServeHTTP(w, r)
	})
}

// sensitiveStaticPathPrefixes lists URL path prefixes that should never be
// served by the SPA fallback. Hitting one of these means either a probe
// or a misrouted request — either way, 404 not 200.
var sensitiveStaticPathPrefixes = []string{
	"/.git/", "/.env", "/.aws/", "/.ssh/",
	"/debug/vars", "/debug/pprof/",
	"/server-status", "/server-info",
}

// sensitiveStaticPathExact lists URL paths that are sensitive only as
// exact matches (so the legitimate SPA route /package-json doesn't
// accidentally match a /package.json denylist).
var sensitiveStaticPathExact = map[string]struct{}{
	"/.env":              {},
	"/.gitignore":        {},
	"/.git":              {},
	"/.htaccess":         {},
	"/composer.lock":     {},
	"/composer.json":     {},
	"/package.json":      {},
	"/package-lock.json": {},
	"/pnpm-lock.yaml":    {},
	"/yarn.lock":         {},
	"/go.mod":            {},
	"/go.sum":            {},
	"/Gemfile":           {},
	"/Gemfile.lock":      {},
	"/next.config.js":    {},
	"/next.config.ts":    {},
	"/next.config.mjs":   {},
	"/wp-config.php":     {},
	"/web.config":        {},
	// Catch /debug/pprof and /debug/vars without a trailing slash too —
	// the prefix denylist only matches "/debug/pprof/", missing the
	// bare-form probes some scanners use first. CodeRabbit's slash-bypass
	// note from the first review pass.
	"/debug/pprof": {},
	"/debug/vars":  {},
}

func isSensitiveStaticPath(path string) bool {
	if _, ok := sensitiveStaticPathExact[path]; ok {
		return true
	}
	for _, p := range sensitiveStaticPathPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// SetChatHandler sets the handler for WebSocket chat messages.

func (s *Server) SetChatHandler(handler ws.ChatHandler) {
	s.wsHub.SetChatHandler(handler)
}

// SetChannelAuthorizer sets the authorizer for WebSocket channel subscriptions.
func (s *Server) SetChannelAuthorizer(auth ws.ChannelAuthorizer) {
	s.wsHub.SetChannelAuthorizer(auth)
}

// Orchestrator returns the server's orchestrator instance.
func (s *Server) Orchestrator() *orchestrator.Orchestrator {
	return s.orchestrator
}

// MissionEngine returns the server's mission engine instance.
func (s *Server) MissionEngine() *orchestrator.MissionEngine {
	return s.missionEngine
}

// TokenPool returns the LLM proxy token pool for credential rotation.
func (s *Server) TokenPool() *llmproxy.TokenPool {
	return s.tokenPool
}

// ConversationStore returns the conversation persistence store.
func (s *Server) ConversationStore() *conversation.Store {
	return s.convStore
}

// LogWriter returns the agent log writer.
func (s *Server) LogWriter() *logcollector.Writer {
	return s.logWriter
}

// APIRouter returns the API router for registering additional routes.
func (s *Server) APIRouter() *goapi.Router {
	return s.apiRouter
}

// WSHub exposes the WebSocket hub for subsystems (e.g. pipeline
// runtime) that need to push live events to subscribed clients.
// Returns nil before the server fully boots; callers must
// nil-check before subscribing.
func (s *Server) WSHub() *ws.Hub {
	return s.wsHub
}

// JournalWriter exposes the production journal writer so out-of-tree
// boot wiring (cmd/crewship) can pass it to subsystems that need to
// emit cost-ledger and run-trace entries through the same buffered
// pipeline as the rest of the server. Notably the LLMRunner needs
// it for llm.Middleware's paymaster + lookout layers.
//
// Returns nil when the server was constructed without a DB
// (test-only path) — callers must nil-check.
func (s *Server) JournalWriter() *journal.Writer {
	return s.journalWriter
}

// Start launches the HTTP server, IPC listener, WebSocket hub, scheduler,
