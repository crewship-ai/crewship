package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// ─── plural ──────────────────────────────────────────────────────────────

func TestPlural(t *testing.T) {
	t.Parallel()
	if got := plural(1); got != "" {
		t.Errorf("plural(1) = %q, want empty", got)
	}
	for _, n := range []int{0, 2, 10} {
		if got := plural(n); got != "s" {
			t.Errorf("plural(%d) = %q, want s", n, got)
		}
	}
}

// ─── checklistRenderer ───────────────────────────────────────────────────

func TestChecklistRenderer_NoTTYPrintsTransitionsOnly(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	r := &checklistRenderer{}

	out := covCaptureStdoutCli5(t, func() {
		r.render(&provisionStatusResponse{Step: 2, Total: 5, Message: "Pull base image"})
	})
	if !strings.Contains(out, "[2/5] Pull base image") {
		t.Errorf("expected plain progress line; got %q", out)
	}
	if strings.Contains(out, "\033[") {
		t.Errorf("noTTY output must not contain ANSI escapes; got %q", out)
	}

	// No step info → silent (keeps CI logs sane).
	out = covCaptureStdoutCli5(t, func() {
		r.render(&provisionStatusResponse{Status: "running"})
	})
	if out != "" {
		t.Errorf("expected no output without step info; got %q", out)
	}
}

func TestChecklistRenderer_TTYChecklist(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	r := &checklistRenderer{}

	status := &provisionStatusResponse{
		Step:    2,
		Total:   3,
		Message: "Install features",
		Steps:   []string{"Pull base image", "Install features", "Finalize"},
	}
	out := covCaptureStdoutCli5(t, func() { r.render(status) })
	for _, want := range []string{"Pull base image", "Install features", "Finalize"} {
		if !strings.Contains(out, want) {
			t.Errorf("checklist missing %q; got %q", want, out)
		}
	}
	if r.lastLines != 3 {
		t.Errorf("lastLines = %d, want 3", r.lastLines)
	}

	// Second render must rewind the cursor over the previous 3 lines.
	out = covCaptureStdoutCli5(t, func() { r.render(status) })
	if !strings.Contains(out, "\033[3A\033[J") {
		t.Errorf("expected cursor-up escape for 3 lines; got %q", out)
	}
}

func TestChecklistRenderer_TTYBarAndStarting(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")

	// Total>0 but no Steps → single progress line.
	r := &checklistRenderer{}
	out := covCaptureStdoutCli5(t, func() {
		r.render(&provisionStatusResponse{Step: 1, Total: 4, Message: "Building"})
	})
	if !strings.Contains(out, "Building (1/4)") {
		t.Errorf("expected single progress line; got %q", out)
	}
	if r.lastLines != 1 {
		t.Errorf("lastLines = %d, want 1", r.lastLines)
	}

	// Total>0, empty message → default label.
	r2 := &checklistRenderer{}
	out = covCaptureStdoutCli5(t, func() {
		r2.render(&provisionStatusResponse{Step: 1, Total: 4})
	})
	if !strings.Contains(out, "Building image…") {
		t.Errorf("expected default message; got %q", out)
	}

	// No step info at all → Starting….
	r3 := &checklistRenderer{}
	out = covCaptureStdoutCli5(t, func() {
		r3.render(&provisionStatusResponse{Status: "pending"})
	})
	if !strings.Contains(out, "Starting…") {
		t.Errorf("expected Starting…; got %q", out)
	}
}

// ─── watchProvision ──────────────────────────────────────────────────────

func TestWatchProvision_CompletedFirstPoll(t *testing.T) {
	t.Setenv("NO_COLOR", "1") // keep renderer in append-only mode
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli5+"/provision", clitest.JSONResponse(200, map[string]any{
		"status": "completed",
	}))
	client := newAPIClient()

	var err error
	covCaptureStdoutCli5(t, func() { err = watchProvision(client, covCrewIDCli5, "backend") })
	if err != nil {
		t.Fatalf("watchProvision: %v", err)
	}
	if calls := stub.CallsFor("GET", "/api/v1/crews/"+covCrewIDCli5+"/provision"); len(calls) != 1 {
		t.Errorf("expected exactly 1 poll, got %d", len(calls))
	}
}

func TestWatchProvision_CompletedWithPendingRestart(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli5+"/provision", clitest.JSONResponse(200, map[string]any{
		"status": "completed", "agents_pending_restart": 2,
	}))
	client := newAPIClient()

	var err error
	out := covCaptureAll(t, func() { err = watchProvision(client, covCrewIDCli5, "backend") })
	if err != nil {
		t.Fatalf("watchProvision: %v", err)
	}
	if !strings.Contains(out, "2 agents on the old image") {
		t.Errorf("expected pending-restart hint; got:\n%s", out)
	}
	if !strings.Contains(out, "crewship crew restart-agents backend") {
		t.Errorf("expected restart-agents remediation; got:\n%s", out)
	}
}

func TestWatchProvision_Failed(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli5+"/provision", clitest.JSONResponse(200, map[string]any{
		"status": "failed", "error": "base image pull denied",
	}))
	client := newAPIClient()

	var err error
	covCaptureStdoutCli5(t, func() { err = watchProvision(client, covCrewIDCli5, "backend") })
	if err == nil || !strings.Contains(err.Error(), "provisioning failed") {
		t.Errorf("expected provisioning-failed error; got %v", err)
	}
}

func TestWatchProvision_HTTPError(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli5+"/provision", clitest.ErrorResponse(500, "boom"))
	client := newAPIClient()

	err := watchProvision(client, covCrewIDCli5, "backend")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected API error; got %v", err)
	}
}

func TestWatchProvision_TransportError(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	stub := covSetupCli5(t)
	client := newAPIClient()
	stub.Close() // simulate connection refused

	err := watchProvision(client, covCrewIDCli5, "backend")
	if err == nil || !strings.Contains(err.Error(), "status fetch") {
		t.Errorf("expected status-fetch wrap; got %v", err)
	}
}

// ─── crew provision / rebuild / status / restart-agents RunE ────────────

func TestCrewProvisionRunE_NoWatch(t *testing.T) {
	stub := covSetupCli5(t)
	covSetFlagCli5(t, crewProvisionCmd, "no-watch", "true")
	stub.OnPost("/api/v1/crews/"+covCrewIDCli5+"/provision", clitest.JSONResponse(202, map[string]any{"queued": true}))

	var err error
	covCaptureStdoutCli5(t, func() { err = crewProvisionCmd.RunE(crewProvisionCmd, []string{covCrewIDCli5}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if calls := stub.CallsFor("POST", "/api/v1/crews/"+covCrewIDCli5+"/provision"); len(calls) != 1 {
		t.Errorf("expected 1 POST provision, got %d", len(calls))
	}
}

func TestCrewProvisionRunE_WatchUntilCompleted(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	stub := covSetupCli5(t)
	covSetFlagCli5(t, crewProvisionCmd, "no-watch", "false")
	stub.OnPost("/api/v1/crews/"+covCrewIDCli5+"/provision", clitest.JSONResponse(202, map[string]any{"queued": true}))
	stub.OnGet("/api/v1/crews/"+covCrewIDCli5+"/provision", clitest.JSONResponse(200, map[string]any{"status": "completed"}))

	var err error
	covCaptureStdoutCli5(t, func() { err = crewProvisionCmd.RunE(crewProvisionCmd, []string{covCrewIDCli5}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if calls := stub.CallsFor("GET", "/api/v1/crews/"+covCrewIDCli5+"/provision"); len(calls) != 1 {
		t.Errorf("expected 1 watch poll, got %d", len(calls))
	}
}

func TestCrewProvisionRunE_UnknownCrewSlug(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))

	err := crewProvisionCmd.RunE(crewProvisionCmd, []string{"ghost-crew"})
	if err == nil || !strings.Contains(err.Error(), "crew not found") {
		t.Errorf("expected crew-not-found; got %v", err)
	}
}

func TestCrewRebuildRunE_NoWatch(t *testing.T) {
	stub := covSetupCli5(t)
	covSetFlagCli5(t, crewRebuildCmd, "no-watch", "true")
	stub.OnPost("/api/v1/crews/"+covCrewIDCli5+"/rebuild", clitest.JSONResponse(202, map[string]any{"queued": true}))

	var err error
	covCaptureStdoutCli5(t, func() { err = crewRebuildCmd.RunE(crewRebuildCmd, []string{covCrewIDCli5}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if calls := stub.CallsFor("POST", "/api/v1/crews/"+covCrewIDCli5+"/rebuild"); len(calls) != 1 {
		t.Errorf("expected 1 POST rebuild, got %d", len(calls))
	}
}

func TestCrewRebuildRunE_APIError(t *testing.T) {
	stub := covSetupCli5(t)
	covSetFlagCli5(t, crewRebuildCmd, "no-watch", "true")
	stub.OnPost("/api/v1/crews/"+covCrewIDCli5+"/rebuild", clitest.ErrorResponse(409, "build already running"))

	err := crewRebuildCmd.RunE(crewRebuildCmd, []string{covCrewIDCli5})
	if err == nil || !strings.Contains(err.Error(), "build already running") {
		t.Errorf("expected conflict error; got %v", err)
	}
}

func TestCrewProvisionStatusRunE_Snapshot(t *testing.T) {
	stub := covSetupCli5(t)
	flagFormat = "json"
	stub.OnGet("/api/v1/crews/"+covCrewIDCli5+"/provision", clitest.JSONResponse(200, map[string]any{
		"status":                 "running",
		"cached_image":           "crewship-cache:abc",
		"config_hash":            "deadbeef",
		"devcontainer_config":    "{}",
		"step":                   2,
		"total":                  5,
		"message":                "Install features",
		"agents_pending_restart": 1,
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() {
		err = crewProvisionStatusCmd.RunE(crewProvisionStatusCmd, []string{covCrewIDCli5})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{`"status": "running"`, "crewship-cache:abc", "deadbeef"} {
		if !strings.Contains(out, want) {
			t.Errorf("snapshot output missing %q; got:\n%s", want, out)
		}
	}
}

func TestCrewProvisionStatusRunE_Watch(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	stub := covSetupCli5(t)
	covSetFlagCli5(t, crewProvisionStatusCmd, "watch", "true")
	stub.OnGet("/api/v1/crews/"+covCrewIDCli5+"/provision",
		clitest.JSONResponse(200, map[string]any{"status": "completed"}))

	var err error
	covCaptureStdoutCli5(t, func() {
		err = crewProvisionStatusCmd.RunE(crewProvisionStatusCmd, []string{covCrewIDCli5})
	})
	if err != nil {
		t.Fatalf("RunE --watch: %v", err)
	}
}

func TestCrewRestartAgentsRunE_Restarted(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnPost("/api/v1/crews/"+covCrewIDCli5+"/restart-agents",
		clitest.JSONResponse(200, map[string]any{"restarted": 2}))

	var err error
	out := covCaptureAll(t, func() {
		err = crewRestartAgentsCmd.RunE(crewRestartAgentsCmd, []string{covCrewIDCli5})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "2 agents will pick up the new image") {
		t.Errorf("expected restart confirmation; got:\n%s", out)
	}
}

func TestCrewRestartAgentsRunE_NothingRunning(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnPost("/api/v1/crews/"+covCrewIDCli5+"/restart-agents",
		clitest.JSONResponse(200, map[string]any{"restarted": 0}))

	var err error
	out := covCaptureAll(t, func() {
		err = crewRestartAgentsCmd.RunE(crewRestartAgentsCmd, []string{covCrewIDCli5})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "nothing to restart") {
		t.Errorf("expected idempotent message; got:\n%s", out)
	}
}

func TestCrewRestartAgentsRunE_JSON(t *testing.T) {
	stub := covSetupCli5(t)
	flagFormat = "json"
	stub.OnPost("/api/v1/crews/"+covCrewIDCli5+"/restart-agents",
		clitest.JSONResponse(200, map[string]any{"restarted": 3}))

	var err error
	out := covCaptureAll(t, func() {
		err = crewRestartAgentsCmd.RunE(crewRestartAgentsCmd, []string{covCrewIDCli5})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, `"restarted": 3`) {
		t.Errorf("expected JSON envelope; got:\n%s", out)
	}
}

// ─── error + format branches round 2 ─────────────────────────────────────

func TestCrewProvisionFamily_AuthAndWorkspaceGates(t *testing.T) {
	type cmdCase struct {
		name string
		run  func() error
	}
	cases := []cmdCase{
		{"provision", func() error { return crewProvisionCmd.RunE(crewProvisionCmd, []string{covCrewIDCli5}) }},
		{"status", func() error { return crewProvisionStatusCmd.RunE(crewProvisionStatusCmd, []string{covCrewIDCli5}) }},
		{"rebuild", func() error { return crewRebuildCmd.RunE(crewRebuildCmd, []string{covCrewIDCli5}) }},
		{"restart-agents", func() error { return crewRestartAgentsCmd.RunE(crewRestartAgentsCmd, []string{covCrewIDCli5}) }},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/no_auth", func(t *testing.T) {
			covSetupCli5(t)
			cliCfg = &cli.CLIConfig{}
			if err := tc.run(); err == nil || !strings.Contains(err.Error(), "not logged in") {
				t.Errorf("expected not-logged-in; got %v", err)
			}
		})
		t.Run(tc.name+"/no_workspace", func(t *testing.T) {
			covSetupCli5(t)
			cliCfg = &cli.CLIConfig{Token: "fake-token"}
			if err := tc.run(); err == nil || !strings.Contains(err.Error(), "workspace") {
				t.Errorf("expected workspace error; got %v", err)
			}
		})
	}
}

func TestCrewProvisionRunE_PostError(t *testing.T) {
	stub := covSetupCli5(t)
	covSetFlagCli5(t, crewProvisionCmd, "no-watch", "true")
	stub.OnPost("/api/v1/crews/"+covCrewIDCli5+"/provision", clitest.ErrorResponse(503, "provisioner queue full"))

	err := crewProvisionCmd.RunE(crewProvisionCmd, []string{covCrewIDCli5})
	if err == nil || !strings.Contains(err.Error(), "provisioner queue full") {
		t.Errorf("expected 503; got %v", err)
	}
}

func TestCrewProvisionStatusRunE_ErrorBranches(t *testing.T) {
	stub := covSetupCli5(t)
	covSetFlagCli5(t, crewProvisionStatusCmd, "watch", "false")
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))
	if err := crewProvisionStatusCmd.RunE(crewProvisionStatusCmd, []string{"ghost"}); err == nil ||
		!strings.Contains(err.Error(), "crew not found") {
		t.Errorf("expected crew-not-found; got %v", err)
	}

	stub.OnGet("/api/v1/crews/"+covCrewIDCli5+"/provision", clitest.ErrorResponse(500, "status wedged"))
	if err := crewProvisionStatusCmd.RunE(crewProvisionStatusCmd, []string{covCrewIDCli5}); err == nil ||
		!strings.Contains(err.Error(), "status wedged") {
		t.Errorf("expected 500; got %v", err)
	}

	stub.OnGet("/api/v1/crews/"+covCrewIDCli5+"/provision", clitest.TextResponse(200, "not json"))
	if err := crewProvisionStatusCmd.RunE(crewProvisionStatusCmd, []string{covCrewIDCli5}); err == nil ||
		!strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error; got %v", err)
	}
}

func TestCrewRebuildRunE_WatchUntilCompleted(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	stub := covSetupCli5(t)
	covSetFlagCli5(t, crewRebuildCmd, "no-watch", "false")
	stub.OnPost("/api/v1/crews/"+covCrewIDCli5+"/rebuild", clitest.JSONResponse(202, map[string]any{"queued": true}))
	stub.OnGet("/api/v1/crews/"+covCrewIDCli5+"/provision", clitest.JSONResponse(200, map[string]any{"status": "completed"}))

	var err error
	covCaptureAll(t, func() { err = crewRebuildCmd.RunE(crewRebuildCmd, []string{covCrewIDCli5}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if calls := stub.CallsFor("GET", "/api/v1/crews/"+covCrewIDCli5+"/provision"); len(calls) != 1 {
		t.Errorf("expected 1 watch poll, got %d", len(calls))
	}
}

func TestCrewRebuildRunE_UnknownCrew(t *testing.T) {
	stub := covSetupCli5(t)
	covSetFlagCli5(t, crewRebuildCmd, "no-watch", "true")
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))

	err := crewRebuildCmd.RunE(crewRebuildCmd, []string{"ghost"})
	if err == nil || !strings.Contains(err.Error(), "crew not found") {
		t.Errorf("expected crew-not-found; got %v", err)
	}
}

func TestCrewRestartAgentsRunE_ErrorAndYAML(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnPost("/api/v1/crews/"+covCrewIDCli5+"/restart-agents", clitest.ErrorResponse(500, "docker wedged"))
	if err := crewRestartAgentsCmd.RunE(crewRestartAgentsCmd, []string{covCrewIDCli5}); err == nil ||
		!strings.Contains(err.Error(), "docker wedged") {
		t.Errorf("expected 500; got %v", err)
	}

	stub2 := covSetupCli5(t)
	flagFormat = "yaml"
	stub2.OnPost("/api/v1/crews/"+covCrewIDCli5+"/restart-agents",
		clitest.JSONResponse(200, map[string]any{"restarted": 1}))
	var err error
	out := covCaptureStdoutCli5(t, func() {
		err = crewRestartAgentsCmd.RunE(crewRestartAgentsCmd, []string{covCrewIDCli5})
	})
	if err != nil {
		t.Fatalf("RunE yaml: %v", err)
	}
	if !strings.Contains(out, "restarted: 1") {
		t.Errorf("yaml output wrong; got:\n%s", out)
	}
}

func TestWatchProvision_MalformedStatus(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli5+"/provision", clitest.TextResponse(200, "not json"))
	client := newAPIClient()

	err := watchProvision(client, covCrewIDCli5, "backend")
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error; got %v", err)
	}
}
