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

func TestSidecarIPCToken_CrewlessDerivesWorkspaceBoundToken(t *testing.T) {
	t.Parallel()
	const master = "master-secret"
	// No crew → workspace-bound fallback (PR-F24), the crew-less path the
	// in-process TokenSyncer and crew-less callers rely on.
	tok := sidecarIPCToken(master, "ws_a", "", ipcTestLogger())

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

// TestSidecarIPCToken_CrewBound covers #1159: a run with a crew gets a
// crew-bound token so the internal API can pin the crew scope server-side.
func TestSidecarIPCToken_CrewBound(t *testing.T) {
	t.Parallel()
	const master = "master-secret"
	tok := sidecarIPCToken(master, "ws_a", "crew_1", ipcTestLogger())
	if tok == master {
		t.Fatal("sidecar received the raw master token — the crew binding is not applied")
	}
	if !internaltoken.IsCrewToken(tok) {
		t.Fatalf("issued token %q is not crew-bound", tok)
	}
	ws, crew, ok := internaltoken.ValidateCrewToken(master, tok)
	if !ok || ws != "ws_a" || crew != "crew_1" {
		t.Fatalf("crew token validated to (%q,%q,%v), want (ws_a,crew_1,true)", ws, crew, ok)
	}
}

func TestSidecarIPCToken_DistinctPerScope(t *testing.T) {
	t.Parallel()
	const master = "master-secret"
	if sidecarIPCToken(master, "ws_a", "", ipcTestLogger()) == sidecarIPCToken(master, "ws_b", "", ipcTestLogger()) {
		t.Fatal("tokens for different workspaces must differ")
	}
	// Two crews in the same workspace must get distinct tokens.
	if sidecarIPCToken(master, "ws_a", "crew_1", ipcTestLogger()) == sidecarIPCToken(master, "ws_a", "crew_2", ipcTestLogger()) {
		t.Fatal("tokens for different crews must differ")
	}
	// A crew-bound token must differ from the workspace-bound fallback.
	if sidecarIPCToken(master, "ws_a", "crew_1", ipcTestLogger()) == sidecarIPCToken(master, "ws_a", "", ipcTestLogger()) {
		t.Fatal("crew-bound and workspace-bound tokens for the same workspace must differ")
	}
}

func TestSidecarIPCToken_FailsClosed(t *testing.T) {
	t.Parallel()
	const master = "master-secret"
	// Empty workspace: refuse to issue anything rather than fall back
	// to the master. A sidecar without a workspace-scoped token gets
	// 403s from the internal API — loud and contained — instead of a
	// process-wide secret inside the container.
	if got := sidecarIPCToken(master, "", "crew_1", ipcTestLogger()); got != "" {
		t.Errorf("empty workspace: issued %q, want empty (never the master)", got)
	}
	// Empty master: nothing to derive from.
	if got := sidecarIPCToken("", "ws_a", "crew_1", ipcTestLogger()); got != "" {
		t.Errorf("empty master: issued %q, want empty", got)
	}
}
