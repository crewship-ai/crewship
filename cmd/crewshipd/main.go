package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/server"
)

func main() {
	configPath := flag.String("config", "", "path to config file (YAML)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	logger := logging.New(cfg.Logging.Level, cfg.Logging.Format, os.Stdout)
	slog.SetDefault(logger)

	logger.Info("crewshipd starting",
		"version", "0.1.0",
		"container_provider", cfg.Container.Provider,
		"storage_provider", cfg.Storage.Provider,
		"state_provider", cfg.State.Provider,
		"http_addr", cfg.Server.Host+":"+itoa(cfg.Server.Port),
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

	srv := server.New(cfg, logger)
	if err := srv.Start(ctx); err != nil {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}

	logger.Info("crewshipd stopped")
}

func itoa(i int) string {
	return fmt.Sprintf("%d", i)
}
