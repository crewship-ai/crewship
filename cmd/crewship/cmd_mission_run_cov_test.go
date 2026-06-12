package main

// Coverage tests for cmd_mission_run.go (mission start / resume /
// add-task / restart / task-update). Every command resolves the mission
// through GET /api/v1/missions first, so each test stubs that list.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func covStubMissionList(stub *clitest.StubServer) {
	stub.OnGet("/api/v1/missions", clitest.JSONResponse(200, []map[string]string{
		{"id": covMissionIDCli6, "crew_id": covCrewIDCli6},
		{"id": "cother0123456789abcde", "crew_id": covCrewIDCli6},
	}))
}

func covMissionPath(suffix string) string {
	return "/api/v1/crews/" + covCrewIDCli6 + "/missions/" + covMissionIDCli6 + suffix
}

// ─── start ───────────────────────────────────────────────────────────────

func TestMissionStartRunE_Happy(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covStubMissionList(stub)
	stub.OnPost(covMissionPath("/start"), clitest.EmptyResponse(200))

	if err := missionStartCmd.RunE(missionStartCmd, []string{covMissionIDCli6}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if n := len(stub.CallsFor("POST", covMissionPath("/start"))); n != 1 {
		t.Errorf("expected 1 start POST, got %d", n)
	}
}

func TestMissionStartRunE_PrefixMatch(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covStubMissionList(stub)
	stub.OnPost(covMissionPath("/start"), clitest.EmptyResponse(200))

	// >=8-char id prefixes resolve to the full mission id.
	prefix := covMissionIDCli6[:10]
	if err := missionStartCmd.RunE(missionStartCmd, []string{prefix}); err != nil {
		t.Fatalf("RunE with prefix: %v", err)
	}
	if n := len(stub.CallsFor("POST", covMissionPath("/start"))); n != 1 {
		t.Errorf("prefix must hit the FULL mission id path, got %d calls", n)
	}
}

func TestMissionStartRunE_NotFound(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covStubMissionList(stub)

	err := missionStartCmd.RunE(missionStartCmd, []string{"cmissingmission123456"})
	if err == nil || !strings.Contains(err.Error(), "mission not found") {
		t.Errorf("expected mission-not-found, got %v", err)
	}
}

func TestMissionStartRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	err := missionStartCmd.RunE(missionStartCmd, []string{covMissionIDCli6})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected 'not logged in', got %v", err)
	}
}

func TestMissionStartRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covStubMissionList(stub)
	stub.OnPost(covMissionPath("/start"), clitest.ErrorResponse(409, "not in PLANNING"))

	err := missionStartCmd.RunE(missionStartCmd, []string{covMissionIDCli6})
	if err == nil || !strings.Contains(err.Error(), "not in PLANNING") {
		t.Errorf("expected 409 surfaced, got %v", err)
	}
}

// ─── resume ──────────────────────────────────────────────────────────────

func TestMissionResumeRunE_Happy(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covStubMissionList(stub)
	stub.OnPost(covMissionPath("/resume"), clitest.JSONResponse(200, map[string]any{
		"reset_tasks": 3,
	}))

	out, err := covCaptureStderrCli6(t, func() error {
		return missionResumeCmd.RunE(missionResumeCmd, []string{covMissionIDCli6})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "3 task(s) reset") {
		t.Errorf("reset count missing from success line: %q", out)
	}
}

func TestMissionResumeRunE_DecodeError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covStubMissionList(stub)
	stub.OnPost(covMissionPath("/resume"), clitest.TextResponse(200, "not json"))

	err := missionResumeCmd.RunE(missionResumeCmd, []string{covMissionIDCli6})
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error, got %v", err)
	}
}

func TestMissionResumeRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covStubMissionList(stub)
	stub.OnPost(covMissionPath("/resume"), clitest.ErrorResponse(409, "not FAILED"))

	err := missionResumeCmd.RunE(missionResumeCmd, []string{covMissionIDCli6})
	if err == nil || !strings.Contains(err.Error(), "not FAILED") {
		t.Errorf("expected 409 surfaced, got %v", err)
	}
}

// ─── add-task ────────────────────────────────────────────────────────────

func TestMissionAddTaskRunE_RequiresTitle(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covResetFlagsCli6(t, missionAddTaskCmd, "title")

	err := missionAddTaskCmd.RunE(missionAddTaskCmd, []string{covMissionIDCli6})
	if err == nil || !strings.Contains(err.Error(), "--title is required") {
		t.Errorf("expected title-required error, got %v", err)
	}
	if n := len(stub.Calls()); n != 0 {
		t.Errorf("validation must precede HTTP, got %d calls", n)
	}
}

func TestMissionAddTaskRunE_FullBody(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covStubMissionList(stub)
	covSetFlagCli6(t, missionAddTaskCmd, "title", "Write tests")
	covSetFlagCli6(t, missionAddTaskCmd, "description", "all of them")
	covSetFlagCli6(t, missionAddTaskCmd, "order", "2")
	covSetFlagCli6(t, missionAddTaskCmd, "agent", covAgentIDCli6) // CUID → no lookup
	covSetFlagCli6(t, missionAddTaskCmd, "depends-on", "t1, t2, ")

	stub.OnPost(covMissionPath("/tasks"), clitest.JSONResponse(201, map[string]string{
		"id": "task1", "title": "Write tests",
	}))

	if err := missionAddTaskCmd.RunE(missionAddTaskCmd, []string{covMissionIDCli6}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := stub.CallsFor("POST", covMissionPath("/tasks"))
	if len(calls) != 1 {
		t.Fatalf("expected 1 task POST, got %d", len(calls))
	}
	body := covDecodeBody(t, calls[0].Body)
	if body["title"] != "Write tests" || body["description"] != "all of them" {
		t.Errorf("body text fields wrong: %v", body)
	}
	if body["task_order"] != float64(2) || body["assigned_agent_id"] != covAgentIDCli6 {
		t.Errorf("order/agent wrong: %v", body)
	}
	deps, _ := body["depends_on"].([]any)
	if len(deps) != 2 || deps[0] != "t1" || deps[1] != "t2" {
		t.Errorf("depends_on = %v, want [t1 t2]", body["depends_on"])
	}
}

func TestMissionAddTaskRunE_AgentResolveError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covStubMissionList(stub)
	covSetFlagCli6(t, missionAddTaskCmd, "title", "x")
	covSetFlagCli6(t, missionAddTaskCmd, "agent", "ghost")

	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{}))

	err := missionAddTaskCmd.RunE(missionAddTaskCmd, []string{covMissionIDCli6})
	if err == nil || !strings.Contains(err.Error(), "agent not found: ghost") {
		t.Errorf("expected agent-not-found, got %v", err)
	}
}

// ─── restart ─────────────────────────────────────────────────────────────

func TestMissionRestartRunE_AbortedWithoutYes(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covResetFlagsCli6(t, missionRestartCmd, "yes")

	err := missionRestartCmd.RunE(missionRestartCmd, []string{covMissionIDCli6})
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("expected aborted, got %v", err)
	}
	if n := len(stub.Calls()); n != 0 {
		t.Errorf("aborted restart must not issue HTTP calls, got %d", n)
	}
}

func TestMissionRestartRunE_Happy(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, missionRestartCmd, "yes", "true")
	covStubMissionList(stub)
	stub.OnPost(covMissionPath("/restart"), clitest.EmptyResponse(200))

	if err := missionRestartCmd.RunE(missionRestartCmd, []string{covMissionIDCli6}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if n := len(stub.CallsFor("POST", covMissionPath("/restart"))); n != 1 {
		t.Errorf("expected 1 restart POST, got %d", n)
	}
}

func TestMissionRestartRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, missionRestartCmd, "yes", "true")
	covStubMissionList(stub)
	stub.OnPost(covMissionPath("/restart"), clitest.ErrorResponse(500, "engine busy"))

	err := missionRestartCmd.RunE(missionRestartCmd, []string{covMissionIDCli6})
	if err == nil || !strings.Contains(err.Error(), "engine busy") {
		t.Errorf("expected 500 surfaced, got %v", err)
	}
}

// ─── shared error-path tables ────────────────────────────────────────────

func missionRunRunners(t *testing.T) map[string]func() error {
	t.Helper()
	covSetFlagCli6(t, missionAddTaskCmd, "title", "x")
	covSetFlagCli6(t, missionRestartCmd, "yes", "true")
	covSetFlagCli6(t, missionTaskUpdateCmd, "title", "x")
	return map[string]func() error{
		"start":       func() error { return missionStartCmd.RunE(missionStartCmd, []string{covMissionIDCli6}) },
		"resume":      func() error { return missionResumeCmd.RunE(missionResumeCmd, []string{covMissionIDCli6}) },
		"add-task":    func() error { return missionAddTaskCmd.RunE(missionAddTaskCmd, []string{covMissionIDCli6}) },
		"restart":     func() error { return missionRestartCmd.RunE(missionRestartCmd, []string{covMissionIDCli6}) },
		"task-update": func() error { return missionTaskUpdateCmd.RunE(missionTaskUpdateCmd, []string{covMissionIDCli6, "task1"}) },
	}
}

func TestMissionRunCmds_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	for name, run := range missionRunRunners(t) {
		if err := run(); err == nil || !strings.Contains(err.Error(), "not logged in") {
			t.Errorf("%s: expected 'not logged in', got %v", name, err)
		}
	}
}

func TestMissionRunCmds_NoWorkspace(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token"}
	flagWorkspace = ""
	t.Setenv("CREWSHIP_WORKSPACE", "")
	for name, run := range missionRunRunners(t) {
		if err := run(); err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Errorf("%s: expected workspace error, got %v", name, err)
		}
	}
}

// TestMissionRunCmds_ResolveError fails the GET /api/v1/missions lookup
// every command performs first.
func TestMissionRunCmds_ResolveError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	stub.OnGet("/api/v1/missions", clitest.ErrorResponse(500, "missions table locked"))

	for name, run := range missionRunRunners(t) {
		if err := run(); err == nil || !strings.Contains(err.Error(), "missions table locked") {
			t.Errorf("%s: expected resolve error surfaced, got %v", name, err)
		}
	}
}

func TestMissionAddTaskRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covStubMissionList(stub)
	covSetFlagCli6(t, missionAddTaskCmd, "title", "x")
	stub.OnPost(covMissionPath("/tasks"), clitest.ErrorResponse(422, "title too long"))

	err := missionAddTaskCmd.RunE(missionAddTaskCmd, []string{covMissionIDCli6})
	if err == nil || !strings.Contains(err.Error(), "title too long") {
		t.Errorf("expected 422 surfaced, got %v", err)
	}
}

func TestMissionAddTaskRunE_DecodeError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covStubMissionList(stub)
	covSetFlagCli6(t, missionAddTaskCmd, "title", "x")
	stub.OnPost(covMissionPath("/tasks"), clitest.TextResponse(200, "not json"))

	err := missionAddTaskCmd.RunE(missionAddTaskCmd, []string{covMissionIDCli6})
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error, got %v", err)
	}
}

func TestMissionTaskUpdateRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covStubMissionList(stub)
	covSetFlagCli6(t, missionTaskUpdateCmd, "title", "x")
	stub.OnPatch(covMissionPath("/tasks/task1"), clitest.ErrorResponse(404, "task gone"))

	err := missionTaskUpdateCmd.RunE(missionTaskUpdateCmd, []string{covMissionIDCli6, "task1"})
	if err == nil || !strings.Contains(err.Error(), "task gone") {
		t.Errorf("expected 404 surfaced, got %v", err)
	}
}

// ─── task-update ─────────────────────────────────────────────────────────

func TestMissionTaskUpdateRunE_NoUpdates(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covStubMissionList(stub)
	covResetFlagsCli6(t, missionTaskUpdateCmd, "title", "description", "status", "assigned-agent")

	err := missionTaskUpdateCmd.RunE(missionTaskUpdateCmd, []string{covMissionIDCli6, "task1"})
	if err == nil || !strings.Contains(err.Error(), "no updates specified") {
		t.Errorf("expected no-updates error, got %v", err)
	}
}

func TestMissionTaskUpdateRunE_Happy(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covStubMissionList(stub)
	covSetFlagCli6(t, missionTaskUpdateCmd, "title", "New title")
	covSetFlagCli6(t, missionTaskUpdateCmd, "description", "new desc")
	covSetFlagCli6(t, missionTaskUpdateCmd, "status", "completed")
	covSetFlagCli6(t, missionTaskUpdateCmd, "assigned-agent", "viktor")

	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
		{"id": covAgentIDCli6, "slug": "viktor"},
	}))
	patchPath := covMissionPath("/tasks/task1")
	stub.OnPatch(patchPath, clitest.EmptyResponse(200))

	if err := missionTaskUpdateCmd.RunE(missionTaskUpdateCmd, []string{covMissionIDCli6, "task1"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := stub.CallsFor("PATCH", patchPath)
	if len(calls) != 1 {
		t.Fatalf("expected 1 PATCH, got %d", len(calls))
	}
	body := covDecodeBody(t, calls[0].Body)
	if body["title"] != "New title" || body["description"] != "new desc" {
		t.Errorf("text fields wrong: %v", body)
	}
	if body["status"] != "COMPLETED" {
		t.Errorf("status must be upper-cased, got %v", body["status"])
	}
	if body["assigned_agent_id"] != covAgentIDCli6 {
		t.Errorf("assigned_agent_id = %v, want resolved %s", body["assigned_agent_id"], covAgentIDCli6)
	}
}

func TestMissionTaskUpdateRunE_AssigneeResolveError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covStubMissionList(stub)
	covSetFlagCli6(t, missionTaskUpdateCmd, "assigned-agent", "ghost")

	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{}))

	err := missionTaskUpdateCmd.RunE(missionTaskUpdateCmd, []string{covMissionIDCli6, "task1"})
	if err == nil || !strings.Contains(err.Error(), `cannot resolve assigned agent "ghost"`) {
		t.Errorf("expected wrapped resolve error, got %v", err)
	}
}
