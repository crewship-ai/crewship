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

// sidecarInput is the JSON payload piped via stdin from the orchestrator.
// It carries credentials and optional memory configuration.
type sidecarInput struct {
	Credentials []sidecar.Credential  `json:"credentials"`
	Memory      *sidecar.MemoryConfig `json:"memory,omitempty"`
}

func main() {
	addr := flag.String("addr", sidecar.DefaultAddr, "listen address")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Read configuration from stdin as JSON.
	// The orchestrator pipes credentials and memory config at startup to
	// avoid putting secrets in env vars, command args, or files on disk.
	//
	// Backwards compatible: accepts both the new object format and the
	// legacy array-of-credentials format.
	var input sidecarInput
	rawBytes, err := readStdin()
	if err != nil {
		logger.Error("failed to read stdin", "error", err)
		os.Exit(1)
	}

	// Try new object format first
	if err := json.Unmarshal(rawBytes, &input); err != nil || len(input.Credentials) == 0 {
		// Fall back to legacy array format
		var creds []sidecar.Credential
		if err := json.Unmarshal(rawBytes, &creds); err != nil {
			logger.Error("failed to parse stdin as credentials", "error", err)
			os.Exit(1)
		}
		input.Credentials = creds
	}

	logger.Info("sidecar starting",
		"addr", *addr,
		"credentials", len(input.Credentials),
		"memory_enabled", input.Memory != nil && input.Memory.Enabled,
	)

	srv := sidecar.NewServer(sidecar.ServerConfig{
		Addr:        *addr,
		Credentials: input.Credentials,
		Memory:      input.Memory,
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

func readStdin() ([]byte, error) {
	var buf []byte
	tmp := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			break
		}
	}
	return buf, nil
}
