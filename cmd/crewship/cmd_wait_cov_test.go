package main

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// Only the COMPLETED terminal status returns from RunE — every other
// terminal status calls os.Exit with a status-specific code, which
// cannot be asserted in-process. Those exit paths are intentionally
// not driven here.

func TestWaitRunE_CompletedTableOutput(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/runs/r_done", clitest.JSONResponse(200, map[string]any{
		"id": "r_done", "status": "COMPLETED", "agent_id": "ca1",
	}))
	covSetupCli10(t, s.URL())
	waitCmd.SetContext(context.Background())

	out, err := captureStdoutCovCli10(t, func() error {
		return waitCmd.RunE(waitCmd, []string{"r_done"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "[done]") || !strings.Contains(out, "status=COMPLETED") {
		t.Errorf("done banner missing: %q", out)
	}
	if got := len(s.CallsFor("GET", "/api/v1/runs/r_done")); got != 1 {
		t.Errorf("poll calls = %d, want 1 (already terminal)", got)
	}
}

func TestWaitRunE_QuietPrintsBareStatus(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/runs/r_q", clitest.JSONResponse(200, map[string]any{
		"id": "r_q", "status": "COMPLETED",
	}))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, waitCmd, "quiet", "true")
	waitCmd.SetContext(context.Background())

	out, err := captureStdoutCovCli10(t, func() error {
		return waitCmd.RunE(waitCmd, []string{"r_q"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if strings.TrimSpace(out) != "COMPLETED" {
		t.Errorf("quiet output = %q, want bare status", out)
	}
}

func TestWaitRunE_JSONFormatEmitsDetail(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/runs/r_j", clitest.JSONResponse(200, map[string]any{
		"id": "r_j", "status": "COMPLETED", "agent_id": "ca1",
	}))
	covSetupCli10(t, s.URL())
	flagFormat = "json"
	waitCmd.SetContext(context.Background())

	out, err := captureStdoutCovCli10(t, func() error {
		return waitCmd.RunE(waitCmd, []string{"r_j"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, `"r_j"`) || !strings.Contains(out, `"COMPLETED"`) {
		t.Errorf("json detail missing: %q", out)
	}
}

func TestWaitRunE_ZeroIntervalClampsAndCompletes(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/runs/r_c", clitest.JSONResponse(200, map[string]any{
		"id": "r_c", "status": "COMPLETED",
	}))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, waitCmd, "interval", "0s") // exercises the <=0 → 2s clamp
	waitCmd.SetContext(context.Background())

	// Run is already terminal, so the clamped interval never sleeps.
	if _, err := captureStdoutCovCli10(t, func() error {
		return waitCmd.RunE(waitCmd, []string{"r_c"})
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
}

func TestWaitRunE_OnTickPrintsStatusTransitions(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	// First poll RUNNING, second poll COMPLETED — onTick fires once.
	n := 0
	s.OnGet("/api/v1/runs/r_t", func(_ *http.Request, _ []byte) (int, []byte, string) {
		n++
		status := "RUNNING"
		if n > 1 {
			status = "COMPLETED"
		}
		return 200, []byte(`{"id":"r_t","status":"` + status + `"}`), "application/json"
	})
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, waitCmd, "interval", "10ms")
	waitCmd.SetContext(context.Background())

	var stderr string
	stdout, err := captureStdoutCovCli10(t, func() error {
		var runErr error
		stderr, runErr = captureStderrCov(t, func() error {
			return waitCmd.RunE(waitCmd, []string{"r_t"})
		})
		return runErr
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(stderr, "status=RUNNING") {
		t.Errorf("onTick transition line missing on stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "status=COMPLETED") {
		t.Errorf("final done line missing: %q", stdout)
	}
	if n < 2 {
		t.Errorf("expected at least 2 polls, got %d", n)
	}
}

func TestWaitRunE_NoAuth(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := waitCmd.RunE(waitCmd, []string{"r_x"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}
}
