package orchestrator

// PR-F24 — the IPC token handed to a sidecar must be bound to the
// run's workspace, never the raw master internal token. The master is
// the process-wide secret; if it enters a container, any agent that
// captures it can call /api/v1/internal/* for every workspace.

import (
	"log/slog"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func ipcTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestSidecarIPCToken_DerivesWorkspaceBoundToken(t *testing.T) {
	t.Parallel()
	const master = "master-secret"
	tok := sidecarIPCToken(master, "ws_a", ipcTestLogger())

	if tok == master {
		t.Fatal("sidecar received the raw master token — the workspace binding is not applied")
	}
	ws, ok := internaltoken.ValidateWorkspaceToken(master, tok)
	if !ok {
		t.Fatalf("issued token %q does not validate against the master", tok)
	}
	if ws != "ws_a" {
		t.Fatalf("issued token bound to %q, want ws_a", ws)
	}
}

func TestSidecarIPCToken_DistinctPerWorkspace(t *testing.T) {
	t.Parallel()
	const master = "master-secret"
	if sidecarIPCToken(master, "ws_a", ipcTestLogger()) == sidecarIPCToken(master, "ws_b", ipcTestLogger()) {
		t.Fatal("tokens for different workspaces must differ")
	}
}

func TestSidecarIPCToken_FailsClosed(t *testing.T) {
	t.Parallel()
	const master = "master-secret"
	// Empty workspace: refuse to issue anything rather than fall back
	// to the master. A sidecar without a workspace-scoped token gets
	// 403s from the internal API — loud and contained — instead of a
	// process-wide secret inside the container.
	if got := sidecarIPCToken(master, "", ipcTestLogger()); got != "" {
		t.Errorf("empty workspace: issued %q, want empty (never the master)", got)
	}
	// Empty master: nothing to derive from.
	if got := sidecarIPCToken("", "ws_a", ipcTestLogger()); got != "" {
		t.Errorf("empty master: issued %q, want empty", got)
	}
}
