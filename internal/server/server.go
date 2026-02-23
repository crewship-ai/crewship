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
	"time"

	goapi "github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/llmproxy"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ws"
)

type Server struct {
	httpServer   *http.Server
	ipcServer    *http.Server
	mux          *http.ServeMux
	ipcMux       *http.ServeMux
	spaHandler   http.Handler
	cfg          *config.Config
	logger       *slog.Logger
	wsHub        *ws.Hub
	orchestrator *orchestrator.Orchestrator
	container    provider.ContainerProvider
	storage      provider.StorageProvider
	state        provider.StateProvider
	logWriter    *logcollector.Writer
	logReader    *logcollector.Reader
	convStore    *conversation.Store
	tokenPool    *llmproxy.TokenPool
	tokenSyncer  *llmproxy.TokenSyncer
	credMonitor  *llmproxy.CredentialMonitor
	debugLogs    *logging.RingBuffer
	startedAt    time.Time
	runCtx       context.Context
	runCancel    context.CancelFunc
}

type Deps struct {
	Container provider.ContainerProvider
	Storage   provider.StorageProvider
	State     provider.StateProvider
	DebugLogs *logging.RingBuffer
	DB        *sql.DB
	WebFS     fs.FS
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

	// Wire IPC config so lead agents can reach crewshipd for assignment routing.
	// host.docker.internal resolves to the Docker host from inside containers.
	if cfg.Auth.InternalToken != "" {
		ipcBase := fmt.Sprintf("http://host.docker.internal:%d", cfg.Server.Port)
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

	s := &Server{
		mux:          mux,
		ipcMux:       ipcMux,
		cfg:          cfg,
		logger:       logger,
		wsHub:        wsHub,
		orchestrator: orch,
		container:    ctr,
		storage:      sto,
		state:        sta,
		logWriter:    logW,
		logReader:    logR,
		convStore:    convStore,
		tokenPool:    tokenPool,
		tokenSyncer:  tokenSyncer,
		credMonitor:  credMonitor,
		debugLogs:    debugLogs,
	}

	s.registerRoutes()
	s.registerIPCRoutes()

	// Mount Go API routes when database is available
	if deps != nil && deps.DB != nil && cfg.Auth.JWTSecret != "" {
		var opts []goapi.RouterOption
		opts = append(opts, goapi.WithSocketPath(cfg.IPC.SocketPath))
		opts = append(opts, goapi.WithInternalToken(cfg.Auth.InternalToken))
		opts = append(opts, goapi.WithHub(wsHub))
		opts = append(opts, goapi.WithOrchestrator(orch))

		// Wire Keeper gatekeeper (Ollama-based credential access control)
		opts = append(opts, goapi.WithKeeperConfig(&cfg.Keeper))
		if cfg.Keeper.Enabled {
			gk := gatekeeper.New(cfg.Keeper.OllamaURL, cfg.Keeper.Model, logger)
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

		apiRouter, err := goapi.NewRouter(deps.DB, cfg.Auth.JWTSecret, logger, opts...)
		if err != nil {
			logger.Error("failed to create API router", "error", err)
		} else {
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
		Addr:         fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler:      mainHandler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
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
			path == "/metrics" || path == "/ws" {
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

func (s *Server) TokenPool() *llmproxy.TokenPool {
	return s.tokenPool
}

func (s *Server) ConversationStore() *conversation.Store {
	return s.convStore
}

func (s *Server) LogWriter() *logcollector.Writer {
	return s.logWriter
}

func (s *Server) Start(ctx context.Context) error {
	s.startedAt = time.Now()

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
