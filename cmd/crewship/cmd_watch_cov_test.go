package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func resetWatchFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		for _, name := range []string{"crew", "agent", "type", "severity"} {
			_ = watchCmd.Flags().Set(name, "")
		}
	})
}

func TestWatchCmdStructure(t *testing.T) {
	t.Parallel()

	if watchCmd.Use != "watch" {
		t.Errorf("watch Use: got %q", watchCmd.Use)
	}
	for _, name := range []string{"crew", "agent", "type", "severity"} {
		if watchCmd.Flags().Lookup(name) == nil {
			t.Errorf("watch missing --%s flag", name)
		}
	}
}

func TestWatchRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	err := watchCmd.RunE(watchCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("got %v; want not-logged-in error", err)
	}
}

func TestWatchRunE_NoWorkspace(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "tok"}
	flagWorkspace = ""
	t.Setenv("CREWSHIP_WORKSPACE", "")

	err := watchCmd.RunE(watchCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("got %v; want workspace error", err)
	}
}

func TestWatchRunE_CrewResolutionFails(t *testing.T) {
	saveCLIState(t)
	resetWatchFlags(t)

	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(http.StatusOK, []any{}))
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: stub.URL()}

	if err := watchCmd.Flags().Set("crew", "ghost-crew"); err != nil {
		t.Fatal(err)
	}
	err := watchCmd.RunE(watchCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "crew not found: ghost-crew") {
		t.Errorf("got %v; want crew-not-found error", err)
	}
	// followJournal must never have been reached.
	if calls := stub.CallsFor("GET", "/api/v1/journal/stream"); len(calls) != 0 {
		t.Errorf("stream called %d times before crew resolution failed", len(calls))
	}
}

func TestWatchRunE_FiltersForwardedToStream(t *testing.T) {
	saveCLIState(t)
	resetWatchFlags(t)

	stub := clitest.NewStubServer()
	defer stub.Close()
	// No stream route registered → stub's fallback answers 404, which
	// followJournal treats as a permanent SSE error and returns. That
	// keeps the test fast (no reconnect loop) while still exercising the
	// full query construction path.
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: stub.URL()}

	crewCUID := "cabcdefghijklmnopqrstuv" // ≥21 chars → used verbatim
	for flag, val := range map[string]string{
		"crew":     crewCUID,
		"agent":    "agent-1",
		"type":     "peer.escalation,keeper.decision",
		"severity": "warn,error",
	} {
		if err := watchCmd.Flags().Set(flag, val); err != nil {
			t.Fatalf("set --%s: %v", flag, err)
		}
	}

	err := watchCmd.RunE(watchCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "status 404") {
		t.Fatalf("got %v; want permanent SSE handshake error", err)
	}

	calls := stub.CallsFor("GET", "/api/v1/journal/stream")
	if len(calls) != 1 {
		t.Fatalf("want exactly 1 stream attempt (404 is permanent), got %d", len(calls))
	}
	q := calls[0].Query
	for _, want := range []string{
		"crew_id=" + crewCUID,
		"agent_id=agent-1",
		"entry_type=peer.escalation%2Ckeeper.decision",
		"severity=warn%2Cerror",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("stream query missing %q; got %q", want, q)
		}
	}
}
