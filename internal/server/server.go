package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ws"
)

type Server struct {
	httpServer   *http.Server
	ipcServer    *http.Server
	mux          *http.ServeMux
	ipcMux       *http.ServeMux
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
	startedAt    time.Time
}

type Deps struct {
	Container provider.ContainerProvider
	Storage   provider.StorageProvider
	State     provider.StateProvider
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

	if deps != nil {
		ctr = deps.Container
		sto = deps.Storage
		sta = deps.State
	}

	orch := orchestrator.New(ctr, sta, logger)
	logW := logcollector.NewWriter(cfg.Storage.LogPath, logger)
	logR := logcollector.NewReader(cfg.Storage.LogPath)
	convStore := conversation.NewStore(cfg.Storage.BasePath, logger)

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
	}

	s.registerRoutes()
	s.registerIPCRoutes()

	s.httpServer = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler:      mux,
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

func (s *Server) SetChatHandler(handler ws.ChatHandler) {
	s.wsHub.SetChatHandler(handler)
}

func (s *Server) Orchestrator() *orchestrator.Orchestrator {
	return s.orchestrator
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
