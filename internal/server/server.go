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
	"time"

	goapi "github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/auth"
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

	var jwtValidator *auth.JWTValidator
	if cfg.Auth.JWTSecret != "" {
		var err error
		jwtValidator, err = auth.NewJWTValidator(cfg.Auth.JWTSecret, "authjs.session-token")
		if err != nil {
			logger.Error("failed to create JWT validator", "error", err)
		} else {
			logger.Info("JWT validator configured for WebSocket auth")
		}
	} else {
		logger.Warn("NEXTAUTH_SECRET not set, WebSocket auth disabled")
	}

	wsHub := ws.NewHub(logger, nil, jwtValidator)

	// File watcher broadcasts real-time file events to WebSocket clients
	// on the crew:{crewID} channel.
	fileWatcher := fileserver.NewWatcher(cfg.Storage.BasePath, logger, func(crewID string, event fileserver.FileEvent) {
		wsHub.BroadcastChannel("crew", crewID, "file.event", event)
	})

	var statsCollector *StatsCollector
	if ctr != nil {
		statsCollector = NewStatsCollector(ctr, wsHub, logger, 5*time.Second)
		// Wire the orchestrator so every crew-container create/reuse on the
		// mission path also registers the container with the stats poller.
		// Without this, only the direct-run path (handleAgentStart) registers
		// containers and the dashboard's container resources tile stays empty
		// for mission-driven runs.
		sc := statsCollector
		orch.SetStatsRegisterCallback(func(containerID, crewID, workspaceID string) {
			sc.Register(containerID, crewID, workspaceID)
		})
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

	// Create MissionEngine for orchestrating multi-task missions
	var missionEngine *orchestrator.MissionEngine
	if deps != nil && deps.DB != nil {
		missionEngine = orchestrator.NewMissionEngine(deps.DB, orch, wsHub, logger)
	}

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

// securityHeadersMiddleware adds standard security headers to all HTTP responses.
// CSP is set per-path: the SPA needs 'unsafe-inline' / 'unsafe-eval' for the
// Next.js runtime, but every other surface (API, /exposed/ proxies, health
// endpoints) gets the strict default-src 'none' policy. Static assets shipped
// from the embed FS already match the SPA frame, so the looser policy is
// scoped to UI paths only — keeps API JSON responses from being rendered as
// HTML even if a future bug returns Content-Type: text/html.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")

		// Audit M5: Content-Security-Policy. The SPA bundle from Next.js
		// uses inline runtime hydration ('unsafe-inline' style, plus an
		// inline boot script), so the UI policy is permissive on those
		// axes but tight on script-src/connect-src. Non-UI surfaces
		// (API JSON, health probes) get the "default-src 'none'"
		// lockdown so a Content-Type mishap can't turn into an XSS.
		//
		// /exposed/ is the reverse-proxy path for port-exposed apps —
		// the upstream may serve arbitrary HTML/JS that needs its own
		// policy. We DON'T set CSP on those responses; if the upstream
		// returns a CSP header it propagates as-is, and if not, the
		// browser default applies. CodeRabbit flagged this in PR #236.
		path := r.URL.Path
		isExposed := strings.HasPrefix(path, "/exposed/")
		isUI := !isExposed &&
			!strings.HasPrefix(path, "/api/") &&
			path != "/healthz" && path != "/readyz" &&
			path != "/metrics" && path != "/ws" && path != "/ws/terminal"
		switch {
		case isExposed:
			// Upstream owns its own policy; do not stamp.
		case isUI:
			// connect-src is just 'self' — earlier 'ws: wss:' was a
			// scheme-only source that allows WebSocket to ANY host;
			// 'self' covers same-origin ws/wss, which is what we want.
			w.Header().Set("Content-Security-Policy",
				"default-src 'self'; "+
					"script-src 'self' 'unsafe-inline' 'unsafe-eval'; "+
					"style-src 'self' 'unsafe-inline'; "+
					"img-src 'self' data: blob:; "+
					"font-src 'self' data:; "+
					"connect-src 'self'; "+
					"frame-ancestors 'none'; "+
					"base-uri 'self'; "+
					"form-action 'self'")
		default:
			w.Header().Set("Content-Security-Policy",
				"default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		}

		next.ServeHTTP(w, r)
	})
}

// combinedHandler routes /api/, /exposed/, /healthz, /readyz, /metrics, /ws
// to the mux, and everything else to the SPA static file handler.
// /exposed/{token}/... must bypass the SPA handler so the port-expose reverse
// proxy sees the request instead of serving the Next.js fallback.

func (s *Server) combinedHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
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

// Start launches the HTTP server, IPC listener, WebSocket hub, scheduler,
