package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/provider/bbolt"
	"github.com/crewship-ai/crewship/internal/provider/docker"
	"github.com/crewship-ai/crewship/internal/provider/localfs"
	"github.com/crewship-ai/crewship/internal/server"
)

func main() {
	// Bootstrap JSON logger before config load so early errors are structured
	bootstrapLogger := logging.New("info", "json", os.Stdout)
	slog.SetDefault(bootstrapLogger)

	configPath := flag.String("config", "", "path to config file (YAML)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	logger := logging.New(cfg.Logging.Level, "json", os.Stdout)
	slog.SetDefault(logger)

	logger.Info("crewshipd starting",
		"version", "0.1.0",
		"container_provider", cfg.Container.Provider,
		"storage_provider", cfg.Storage.Provider,
		"state_provider", cfg.State.Provider,
		"http_addr", cfg.Server.Host+":"+strconv.Itoa(cfg.Server.Port),
		"ipc_socket", cfg.IPC.SocketPath,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ctx = logging.WithContext(ctx, logger)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sig
		logger.Info("received shutdown signal")
		cancel()
	}()

	deps, err := initProviders(cfg, logger)
	if err != nil {
		logger.Error("failed to initialize providers", "error", err)
		os.Exit(1)
	}
	defer deps.Close()

	srv := server.New(cfg, logger, deps)

	resolver := chatbridge.NewIPCResolver(cfg.Auth.NextjsURL, cfg.Auth.InternalToken, logger)
	bridge := chatbridge.New(
		srv.Orchestrator(),
		deps.Container,
		srv.ConversationStore(),
		srv.LogWriter(),
		resolver,
		chatbridge.BridgeConfig{
			DefaultMemoryMB: cfg.Container.DefaultMemoryMB,
			DefaultCPUs:     cfg.Container.DefaultCPUs,
		},
		logger,
	)
	srv.SetChatHandler(bridge)

	if err := srv.Start(ctx); err != nil {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}

	logger.Info("crewshipd stopped")
}

func initProviders(cfg *config.Config, logger *slog.Logger) (*server.Deps, error) {
	deps := &server.Deps{}

	switch cfg.Container.Provider {
	case "docker":
		d, err := docker.New(docker.Config{
			RuntimeImage:   cfg.Container.RuntimeImage,
			DefaultRuntime: cfg.Container.DefaultRuntime,
			Network:        cfg.Container.Network,
			OutputBasePath: cfg.Storage.BasePath,
		}, logger)
		if err != nil {
			logger.Warn("docker provider unavailable, running without containers", "error", err)
		} else {
			deps.Container = d
		}
	default:
		if cfg.Container.Provider != "" {
			logger.Warn("unknown container provider", "provider", cfg.Container.Provider)
		}
	}

	switch cfg.Storage.Provider {
	case "localfs":
		fs, err := localfs.New(cfg.Storage.BasePath)
		if err != nil {
			return nil, fmt.Errorf("init localfs provider: %w", err)
		}
		deps.Storage = fs
	default:
		if cfg.Storage.Provider != "" {
			logger.Warn("unknown storage provider", "provider", cfg.Storage.Provider)
		}
	}

	switch cfg.State.Provider {
	case "bbolt":
		b, err := bbolt.New(cfg.State.BoltPath)
		if err != nil {
			return nil, fmt.Errorf("init bbolt provider: %w", err)
		}
		deps.State = b
	default:
		if cfg.State.Provider != "" {
			logger.Warn("unknown state provider", "provider", cfg.State.Provider)
		}
	}

	return deps, nil
}
