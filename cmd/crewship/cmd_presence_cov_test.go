package main

// Coverage tests for cmd_presence.go — roster rendering (table / json /
// empty), crew scoping, and the --watch loop (terminated via SIGINT,
// which signal.NotifyContext turns into a clean exit).

import (
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"net/http"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func covPresenceRoster() map[string]any {
	return map[string]any{
		"rows": []map[string]any{
			{"agent_id": "agent-1", "crew_id": "crew-1", "status": "online", "since": "2026-06-10T12:00:00Z"},
			{"agent_id": "agent-2", "crew_id": "crew-1", "status": "blocked", "since": "2026-06-10T11:00:00Z"},
		},
		"count": 2,
	}
}

func TestClearScreen(t *testing.T) {
	out := covCaptureStdoutCli8(t, func() { clearScreen() })
	if out != "\033[2J\033[H" {
		t.Errorf("clearScreen wrote %q", out)
	}
}

func TestPresenceRosterRunE_NoAuth(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := presenceRosterCmd.RunE(presenceRosterCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}
}

func TestPresenceRosterRunE_Table(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/presence/roster", clitest.JSONResponse(200, covPresenceRoster()))

	out := covCaptureStdoutCli8(t, func() {
		if err := presenceRosterCmd.RunE(presenceRosterCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"agent-1", "agent-2", "online", "blocked"} {
		if !strings.Contains(out, want) {
			t.Errorf("roster output missing %q:\n%s", want, out)
		}
	}
}

func TestPresenceRosterRunE_Empty(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/presence/roster", clitest.JSONResponse(200, map[string]any{"rows": []any{}, "count": 0}))

	out := covCaptureStdoutCli8(t, func() {
		if err := presenceRosterCmd.RunE(presenceRosterCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "roster empty") {
		t.Errorf("empty roster message missing:\n%s", out)
	}
}

func TestPresenceRosterRunE_JSON(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	cliCfg.Format = "json"
	stub.OnGet("/api/v1/presence/roster", clitest.JSONResponse(200, covPresenceRoster()))

	out := covCaptureStdoutCli8(t, func() {
		if err := presenceRosterCmd.RunE(presenceRosterCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, `"agent_id"`) || !strings.Contains(out, "agent-1") {
		t.Errorf("json roster missing rows:\n%s", out)
	}
}

func TestPresenceRosterRunE_CrewScoped(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]any{
		{"id": covCrewIDCli8, "slug": "backend"},
	}))
	stub.OnGet("/api/v1/presence/roster", clitest.JSONResponse(200, covPresenceRoster()))
	covSetFlagCli8(t, presenceRosterCmd, "crew", "backend")

	covCaptureStdoutCli8(t, func() {
		if err := presenceRosterCmd.RunE(presenceRosterCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})

	calls := stub.CallsFor("GET", "/api/v1/presence/roster")
	if len(calls) != 1 {
		t.Fatalf("expected 1 roster GET, got %d", len(calls))
	}
	if !strings.Contains(calls[0].Query, "crew_id="+covCrewIDCli8) {
		t.Errorf("crew_id not propagated: %q", calls[0].Query)
	}
}

func TestPresenceRosterRunE_CrewNotFound(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]any{}))
	covSetFlagCli8(t, presenceRosterCmd, "crew", "ghost")

	err := presenceRosterCmd.RunE(presenceRosterCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "crew not found: ghost") {
		t.Errorf("expected crew-not-found; got %v", err)
	}
}

func TestPresenceRosterRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/presence/roster", clitest.ErrorResponse(500, "Internal server error"))

	err := presenceRosterCmd.RunE(presenceRosterCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "Internal server error") {
		t.Errorf("expected API error; got %v", err)
	}
}

func TestPresenceRosterRunE_NoWorkspace(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{Token: "tok"}
	err := presenceRosterCmd.RunE(presenceRosterCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error; got %v", err)
	}
}

func TestPresenceRosterRunE_IntervalClamps(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/presence/roster", clitest.JSONResponse(200, map[string]any{"rows": []any{}, "count": 0}))

	// interval <= 0 → default; sub-second → floored. Neither affects the
	// non-watch render, but both clamp branches must execute.
	for _, iv := range []string{"0s", "500ms"} {
		covSetFlagCli8(t, presenceRosterCmd, "interval", iv)
		covCaptureStdoutCli8(t, func() {
			if err := presenceRosterCmd.RunE(presenceRosterCmd, nil); err != nil {
				t.Errorf("interval %s: %v", iv, err)
			}
		})
	}
}

func TestPresenceRosterRunE_TransportAndDecode(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())

	stub.OnGet("/api/v1/presence/roster", covAbort())
	if err := presenceRosterCmd.RunE(presenceRosterCmd, nil); err == nil {
		t.Error("expected transport error")
	}

	stub.OnGet("/api/v1/presence/roster", covNotJSON())
	if err := presenceRosterCmd.RunE(presenceRosterCmd, nil); err == nil {
		t.Error("expected decode error")
	}
}

func TestPresenceRosterRunE_YAML(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	cliCfg.Format = "yaml"
	stub.OnGet("/api/v1/presence/roster", clitest.JSONResponse(200, covPresenceRoster()))

	out := covCaptureStdoutCli8(t, func() {
		if err := presenceRosterCmd.RunE(presenceRosterCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	// The row struct has no yaml tags, so fields marshal lowercased.
	if !strings.Contains(out, "agentid: agent-1") || !strings.Contains(out, "status: blocked") {
		t.Errorf("yaml roster missing rows:\n%s", out)
	}
}

// TestPresenceRosterRunE_WatchExitsOnSignal exercises the --watch loop:
// initial render happens immediately, then a SIGINT (delivered to our own
// process; a test-local signal.Notify keeps the default kill behaviour
// disarmed for the whole window) cancels the NotifyContext and RunE
// returns nil before the first 1s tick fires.
func TestPresenceRosterRunE_WatchExitsOnSignal(t *testing.T) {
	// Keep a handler registered for the entire test so the raised SIGINT
	// can never hit the default terminate-the-process action.
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, syscall.SIGINT)
	defer signal.Stop(guard)

	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	var polls atomic.Int64
	stub.OnGet("/api/v1/presence/roster", func(_ *http.Request, _ []byte) (int, []byte, string) {
		polls.Add(1)
		return 200, []byte(`{"rows":[],"count":0}`), "application/json"
	})
	covSetFlagCli8(t, presenceRosterCmd, "watch", "true")
	covSetFlagCli8(t, presenceRosterCmd, "interval", "1s")

	go func() {
		time.Sleep(80 * time.Millisecond)
		_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
	}()

	done := make(chan error, 1)
	out := covCaptureStdoutCli8(t, func() {
		done <- presenceRosterCmd.RunE(presenceRosterCmd, nil)
	})

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("watch RunE: %v", err)
		}
	default:
		t.Fatal("watch did not exit after SIGINT")
	}
	if polls.Load() < 1 {
		t.Error("expected at least the initial render poll")
	}
	if !strings.Contains(out, "polling every 1s") {
		t.Errorf("watch banner missing:\n%s", out)
	}
}

// TestPresenceRosterRunE_WatchTickAndRenderError lets one 1s tick fire:
// the initial render errors (500) and is swallowed, the tick render
// succeeds, then SIGINT exits. Pins the keep-looping-on-error contract.
func TestPresenceRosterRunE_WatchTickAndRenderError(t *testing.T) {
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, syscall.SIGINT)
	defer signal.Stop(guard)

	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	var polls atomic.Int64
	stub.OnGet("/api/v1/presence/roster", func(_ *http.Request, _ []byte) (int, []byte, string) {
		if polls.Add(1) == 1 {
			return 500, []byte(`{"error":"first render fails"}`), "application/json"
		}
		return 200, []byte(`{"rows":[],"count":0}`), "application/json"
	})
	covSetFlagCli8(t, presenceRosterCmd, "watch", "true")
	covSetFlagCli8(t, presenceRosterCmd, "interval", "1s")

	go func() {
		time.Sleep(1300 * time.Millisecond)
		_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
	}()

	var runErr error
	covCaptureStdoutCli8(t, func() {
		runErr = presenceRosterCmd.RunE(presenceRosterCmd, nil)
	})
	if runErr != nil {
		t.Fatalf("watch RunE must swallow render errors: %v", runErr)
	}
	if polls.Load() < 2 {
		t.Errorf("expected initial + >=1 tick render, got %d polls", polls.Load())
	}
}
