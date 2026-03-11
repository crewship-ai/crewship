package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/crewship-ai/crewship/internal/sidecar"
)

// sidecarInput is the JSON payload piped via stdin from the orchestrator.
// It carries credentials, optional memory configuration, and IPC config for assignment routing.
type sidecarInput struct {
	Credentials   []sidecar.Credential        `json:"credentials"`
	Memory        *sidecar.MemoryConfig        `json:"memory,omitempty"`
	IPC           *sidecar.IPCConfig           `json:"ipc,omitempty"`
	CrewMembers   []sidecar.CrewMember         `json:"crew_members,omitempty"`
	NetworkPolicy *sidecar.NetworkPolicyConfig `json:"network_policy,omitempty"`
}

func main() {
	addr := flag.String("addr", sidecar.DefaultAddr, "listen address")
	flag.Parse()

	// Ignore SIGPIPE so writes to closed stdout/stderr (after Docker exec
	// stream closes) return EPIPE errors instead of killing the process.
	// Without this, the sidecar dies as soon as the shell wrapper exits.
	signal.Ignore(syscall.SIGPIPE)

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

	// Try new object format first; fall back to legacy array only on parse error.
	// Empty credentials is valid (e.g. memory-only startup with no API keys).
	if err := json.Unmarshal(rawBytes, &input); err != nil {
		// Fall back to legacy array format
		var creds []sidecar.Credential
		if err := json.Unmarshal(rawBytes, &creds); err != nil {
			logger.Error("failed to parse stdin as credentials", "error", err)
			os.Exit(1)
		}
		input.Credentials = creds
	}

	if input.NetworkPolicy != nil && input.NetworkPolicy.Mode == "" {
		input.NetworkPolicy.Mode = "free"
	}
	networkMode := "free"
	if input.NetworkPolicy != nil {
		networkMode = input.NetworkPolicy.Mode
	}

	logger.Info("sidecar starting",
		"addr", *addr,
		"credentials", len(input.Credentials),
		"memory_enabled", input.Memory != nil && input.Memory.Enabled,
		"ipc_enabled", input.IPC != nil,
		"crew_members", len(input.CrewMembers),
		"network_mode", networkMode,
	)

	srv := sidecar.NewServer(sidecar.ServerConfig{
		Addr:          *addr,
		Credentials:   input.Credentials,
		Memory:        input.Memory,
		IPC:           input.IPC,
		CrewMembers:   input.CrewMembers,
		NetworkPolicy: input.NetworkPolicy,
		Logger:        logger,
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
	// The write is non-fatal: stdout may already be closed if Docker exec stream ended
	// before the goroutine runs. Health check via wget/curl is the primary mechanism.
	go func() {
		<-srv.Ready()
		if _, err := os.Stdout.WriteString("SIDECAR_READY\n"); err != nil {
			logger.Warn("readiness signal not delivered (stdout closed)", "error", err)
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
		if err == io.EOF {
			break
		}
		if err != nil {
			return buf, err
		}
	}
	return buf, nil
}
