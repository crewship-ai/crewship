package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/sidecar"
)

// version is overridden at build time via ldflags (-X main.version=...).
// Defaults to "dev" for local builds.
var version = "dev"

// sidecarInput is the JSON payload piped via stdin from the orchestrator.
// It carries credentials, optional memory configuration, and IPC config for assignment routing.
type sidecarInput struct {
	Credentials       []sidecar.Credential         `json:"credentials"`
	Memory            *sidecar.MemoryConfig        `json:"memory,omitempty"`
	IPC               *sidecar.IPCConfig           `json:"ipc,omitempty"`
	RouteAuth         *sidecar.RouteAuth           `json:"route_auth,omitempty"`
	CrewMembers       []sidecar.CrewMember         `json:"crew_members,omitempty"`
	NetworkPolicy     *sidecar.NetworkPolicyConfig `json:"network_policy,omitempty"`
	MCPServers        []sidecar.MCPServerInput     `json:"mcp_servers,omitempty"`
	ConfigFingerprint string                       `json:"config_fingerprint,omitempty"`
}

func main() {
	addr := flag.String("addr", sidecar.DefaultAddr, "listen address")
	showVersion := flag.Bool("version", false, "print version info and exit")
	healthCheck := flag.Bool("health-check", false,
		"probe a already-running sidecar's /health endpoint and exit 0 (healthy) or 1")
	flag.Parse()

	// --version is used by the Crewship container runtime as a sanity check
	// after bind-mounting the sidecar binary into BYOI containers: running
	// this flag exercises the Go runtime + libc, so a musl-vs-glibc ABI
	// mismatch surfaces as a non-zero exit instead of a mysterious IPC
	// timeout. Exit code must be 0 on success.
	if *showVersion {
		fmt.Printf("crewship-sidecar version %s (%s/%s, %s)\n",
			version, runtime.GOOS, runtime.GOARCH, runtime.Version())
		os.Exit(0)
	}

	// --health-check exists so that verifying the sidecar came up does not
	// depend on anything being installed in the agent's base image.
	//
	// The launch script used to probe with `wget`, falling back to `curl`.
	// debian:bookworm-slim — which is Crewship's own compiled-in default
	// runtime image (internal/config/config.go) and what the docs and the
	// devcontainer e2e both use — ships NEITHER. Both branches failed on
	// "command not found", the script took its else branch, and a perfectly
	// healthy sidecar was reported as "sidecar health check failed". Out of
	// the box, on the default image, no agent could answer a message.
	//
	// This binary is bind-mounted into the container by the runtime, so it
	// is present by construction — the one thing a probe can rely on. It is
	// also already the BYOI ABI canary via --version, so using it here keeps
	// that guarantee in one place.
	if *healthCheck {
		if err := probeHealth(*addr); err != nil {
			fmt.Fprintf(os.Stderr, "sidecar health check failed: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	// Ignore SIGPIPE so writes to closed stdout/stderr (after Docker exec
	// stream closes) return EPIPE errors instead of killing the process.
	// Without this, the sidecar dies as soon as the shell wrapper exits.
	signal.Ignore(syscall.SIGPIPE)

	// Route through logging.New (not a raw slog handler) so the sidecar's
	// logs get the same central ReplaceAttr treatment the server does:
	// secret redaction AND CR/LF + control-char neutralization
	// (CWE-117 / log-injection). The sidecar logs user-derived values
	// (proxy targets, memory paths, agent IDs), so an un-neutralized
	// handler here would leave those log-injection sinks open even after
	// the server side is fixed.
	logger := logging.New("debug", "json", os.Stderr)
	// Also claim the process-wide default: a handful of constructors
	// (memory executor, sidecar server, memory watcher) fall back to
	// slog.Default() when built without an explicit logger — without this
	// line those fallbacks would bypass the neutralizer barrier above.
	slog.SetDefault(logger)

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
		"mcp_servers", len(input.MCPServers),
	)

	srv := sidecar.NewServer(sidecar.ServerConfig{
		Addr:              *addr,
		Credentials:       input.Credentials,
		Memory:            input.Memory,
		IPC:               input.IPC,
		RouteAuth:         input.RouteAuth,
		CrewMembers:       input.CrewMembers,
		NetworkPolicy:     input.NetworkPolicy,
		MCPServers:        input.MCPServers,
		ConfigFingerprint: input.ConfigFingerprint,
		Logger:            logger,
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
	// before the goroutine runs. The primary readiness mechanism is the
	// --health-check probe run by sidecarLaunchScript, not this line.
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

// probeHealth GETs http://<addr>/health and reports whether the sidecar is
// serving. Used by --health-check.
//
// Deliberately dependency-free: net/http from the standard library, compiled
// into the same static binary the runtime already bind-mounts. That is the
// whole point — the previous probe shelled out to wget/curl, which the
// default runtime image does not contain, so it could not tell "the sidecar
// is down" apart from "I have no way to ask".
//
// The address is normalised rather than trusted: --addr is a listen address
// ("127.0.0.1:9119", or ":9119" when the caller binds all interfaces), and a
// bare ":9119" is not a dialable host. An empty host means localhost here.
func probeHealth(addr string) error {
	host := strings.TrimSpace(addr)
	if host == "" {
		host = sidecar.DefaultAddr
	}
	if strings.HasPrefix(host, ":") {
		host = "127.0.0.1" + host
	}

	// Short and fixed. The caller (sidecarLaunchScript) has already slept for
	// the sidecar to bind, and this runs on the critical path of a user's
	// first message — a long timeout here turns a dead sidecar into a stalled
	// chat rather than an error.
	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequest(http.MethodGet, "http://"+host+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}
