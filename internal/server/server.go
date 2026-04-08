package server

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	goapi "github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/fileserver"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/license"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/llmproxy"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/terminal"
	"github.com/crewship-ai/crewship/internal/ws"
)

type Server struct {
	httpServer    *http.Server
	ipcServer     *http.Server
	mux           *http.ServeMux
	ipcMux        *http.ServeMux
	spaHandler    http.Handler
	cfg           *config.Config
	logger        *slog.Logger
	wsHub         *ws.Hub
	orchestrator  *orchestrator.Orchestrator
	missionEngine *orchestrator.MissionEngine
	container     provider.ContainerProvider
	storage       provider.StorageProvider
	state         provider.StateProvider
	logWriter     *logcollector.Writer
	logReader     *logcollector.Reader
	convStore     *conversation.Store
	tokenPool     *llmproxy.TokenPool
	tokenSyncer   *llmproxy.TokenSyncer
	credMonitor   *llmproxy.CredentialMonitor
	debugLogs     *logging.RingBuffer
	db             *sql.DB
	apiRouter      *goapi.Router
	fileWatcher    *fileserver.Watcher
	watchedCrews   sync.Map
	statsCollector  *StatsCollector
	terminalHandler *terminal.Handler
	startedAt       time.Time
	runCtx          context.Context
	runCancel       context.CancelFunc
}

type Deps struct {
	Container provider.ContainerProvider
	Storage   provider.StorageProvider
	State     provider.StateProvider
	DebugLogs *logging.RingBuffer
	DB        *sql.DB
	WebFS     fs.FS
	License   *license.License
}

func (d *Deps) Close() {
	if d == nil {
		return
	}
	if c, ok := d.State.(interface{ Close() error }); ok {
		c.Close()
	}
}

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
		channel := "crew:" + crewID
		wsHub.Broadcast(channel, ws.ServerMessage{
			Type:    "file.event",
			Channel: channel,
			Payload: event,
		})
	})

	var statsCollector *StatsCollector
	if ctr != nil {
		statsCollector = NewStatsCollector(ctr, wsHub, logger, 5*time.Second)
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
		opts = append(opts, goapi.WithSocketPath(cfg.IPC.SocketPath))
		opts = append(opts, goapi.WithInternalToken(cfg.Auth.InternalToken))
		opts = append(opts, goapi.WithInternalBaseURL(ipcBase))
		opts = append(opts, goapi.WithHub(wsHub))
		opts = append(opts, goapi.WithOrchestrator(orch))
		opts = append(opts, goapi.WithLogWriter(logW))
		if missionEngine != nil {
			opts = append(opts, goapi.WithMissionCallback(missionEngine))
		}
		opts = append(opts, goapi.WithAllowSignup(cfg.Auth.AllowSignup))
		opts = append(opts, goapi.WithStoragePath(cfg.Storage.BasePath))

		// Wire Keeper gatekeeper (Ollama-based credential access control)
		opts = append(opts, goapi.WithKeeperConfig(&cfg.Keeper))
		if cfg.Keeper.Enabled {
			provider := llm.NewOllama(cfg.Keeper.OllamaURL, cfg.Keeper.Model)
			gk := gatekeeper.New(provider, cfg.Keeper.Model, logger)
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

		apiRouter, err := goapi.NewRouter(deps.DB, cfg.Auth.JWTSecret, logger, opts...)
		if err != nil {
			logger.Error("failed to create API router", "error", err)
		} else {
			s.apiRouter = apiRouter
			mux.Handle("/api/", apiRouter)
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

// combinedHandler routes /api/, /healthz, /readyz, /metrics, /ws to the mux,
// and everything else to the SPA static file handler.
func (s *Server) combinedHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasPrefix(path, "/api/") ||
			path == "/healthz" || path == "/readyz" ||
			path == "/metrics" || path == "/ws" ||
			path == "/ws/terminal" {
			s.mux.ServeHTTP(w, r)
			return
		}
		s.spaHandler.ServeHTTP(w, r)
	})
}

func (s *Server) SetChatHandler(handler ws.ChatHandler) {
	s.wsHub.SetChatHandler(handler)
}

func (s *Server) Orchestrator() *orchestrator.Orchestrator {
	return s.orchestrator
}

func (s *Server) MissionEngine() *orchestrator.MissionEngine {
	return s.missionEngine
}

func (s *Server) TokenPool() *llmproxy.TokenPool {
	return s.tokenPool
}

func (s *Server) ConversationStore() *conversation.Store {
	return s.convStore
}

func (s *Server) LogWriter() *logcollector.Writer {
	return s.logWriter
}

func (s *Server) APIRouter() *goapi.Router {
	return s.apiRouter
}

func (s *Server) Start(ctx context.Context) error {
	s.startedAt = time.Now()

	// Recover orphaned RUNNING runs from previous crashes/restarts.
	// Without this, agents whose runs were interrupted stay RUNNING forever.
	if s.db != nil {
		s.recoverOrphanedRuns(ctx)
	}

	ctx, cancel := context.WithCancel(ctx)
	s.runCtx, s.runCancel = ctx, cancel
	defer cancel()

	errCh := make(chan error, 2)

	go func() {
		s.logger.Info("starting HTTP server", "addr", s.httpServer.Addr)
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("http server: %w", err)
		}
	}()

	go func() {
		if err := s.startIPC(); err != nil {
			errCh <- fmt.Errorf("ipc server: %w", err)
		}
	}()

	go s.wsHub.Run(ctx)
	go s.orchestrator.Start(ctx)

	if s.statsCollector != nil {
		go s.statsCollector.Run(ctx)
	}

	if s.tokenSyncer != nil {
		go s.tokenSyncer.Run(ctx)
	}
	if s.credMonitor != nil {
		go s.credMonitor.Run(ctx)
	}

	select {
	case err := <-errCh:
		cancel()
		_ = s.Shutdown()
		return err
	case <-ctx.Done():
		return s.Shutdown()
	}
}

func (s *Server) Shutdown() error {
	s.logger.Info("shutting down servers")

	s.orchestrator.StopAccepting()
	if s.missionEngine != nil {
		s.missionEngine.Shutdown()
	}
	if s.runCancel != nil {
		s.runCancel()
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Server.ShutdownTimeout)
	defer cancel()

	var firstErr error
	if err := s.httpServer.Shutdown(ctx); err != nil {
		s.logger.Error("http server shutdown error", "error", err)
		firstErr = err
	}
	if err := s.ipcServer.Shutdown(ctx); err != nil {
		s.logger.Error("ipc server shutdown error", "error", err)
		if firstErr == nil {
			firstErr = err
		}
	}

	s.logWriter.Close()
	s.convStore.Close()
	// fileWatcher goroutines are closed via context cancellation (runCancel above);
	// explicit Close() is a no-op but signals intent.
	if s.fileWatcher != nil {
		s.fileWatcher.Close()
	}

	if s.state != nil {
		if err := s.state.Close(); err != nil {
			s.logger.Error("state provider close error", "error", err)
		}
	}

	return firstErr
}

func (s *Server) startIPC() error {
	socketPath := s.cfg.IPC.SocketPath

	// Remove stale socket file
	_ = removeSocketFile(socketPath)

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen unix %s: %w", socketPath, err)
	}

	s.logger.Info("starting IPC server", "socket", socketPath)
	if err := s.ipcServer.Serve(listener); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("ipc serve: %w", err)
	}
	return nil
}

// convStoreAdapter bridges conversation.Store → api.ConversationReader.
type convStoreAdapter struct {
	store *conversation.Store
}

func (a *convStoreAdapter) Read(ctx context.Context, sessionID string, offset, limit int) ([]goapi.ConversationMessage, error) {
	msgs, err := a.store.Read(ctx, sessionID, offset, limit)
	if err != nil {
		return nil, err
	}
	out := make([]goapi.ConversationMessage, len(msgs))
	for i, m := range msgs {
		out[i] = goapi.ConversationMessage{
			Role:    string(m.Role),
			Content: m.Content,
		}
	}
	return out, nil
}

// recoverOrphanedRuns marks stale RUNNING runs as CANCELLED and resets
// agent statuses. This handles cases where the server crashed or was
// restarted while agent runs were in progress.
func (s *Server) recoverOrphanedRuns(ctx context.Context) {
	now := time.Now().UTC().Format(time.RFC3339)

	result, err := s.db.ExecContext(ctx, `
		UPDATE agent_runs SET status = 'CANCELLED', finished_at = ?
		WHERE status = 'RUNNING'`, now)
	if err != nil {
		s.logger.Error("recover orphaned runs", "error", err)
		return
	}
	affected, err := result.RowsAffected()
	if err != nil {
		s.logger.Warn("rows affected check failed", "error", err)
		return
	}
	if affected == 0 {
		return
	}

	s.logger.Info("recovered orphaned runs", "count", affected)

	// Reset agents that no longer have active runs to IDLE
	if _, err := s.db.ExecContext(ctx, `
		UPDATE agents SET status = 'IDLE', updated_at = ?
		WHERE status = 'RUNNING'
		AND id NOT IN (SELECT DISTINCT agent_id FROM agent_runs WHERE status = 'RUNNING')`, now); err != nil {
		s.logger.Error("reset agent statuses after recovery", "error", err)
	}
}
