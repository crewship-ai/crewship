package main

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/crewship-ai/crewship/internal/sidecar"
)

func main() {
	addr := flag.String("addr", sidecar.DefaultAddr, "listen address")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Read credentials from stdin as JSON array.
	// The orchestrator pipes them at startup to avoid putting secrets
	// in env vars, command args, or files on disk.
	var creds []sidecar.Credential
	if err := json.NewDecoder(os.Stdin).Decode(&creds); err != nil {
		logger.Error("failed to read credentials from stdin", "error", err)
		os.Exit(1)
	}

	logger.Info("sidecar starting",
		"addr", *addr,
		"credentials", len(creds),
	)

	srv := sidecar.NewServer(sidecar.ServerConfig{
		Addr:        *addr,
		Credentials: creds,
		Logger:      logger,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		logger.Info("sidecar shutting down")
		cancel()
	}()

	// Wait for the listener to be bound before signaling readiness.
	// This prevents the race where SIDECAR_READY is sent before Start() binds the port.
	go func() {
		<-srv.Ready()
		if _, err := os.Stdout.WriteString("SIDECAR_READY\n"); err != nil {
			logger.Error("failed to write readiness signal", "error", err)
			os.Exit(1)
		}
	}()

	if err := srv.Start(ctx); err != nil {
		logger.Error("sidecar error", "error", err)
		os.Exit(1)
	}
}
