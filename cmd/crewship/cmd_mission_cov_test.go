package main

// Coverage tests for cmd_mission.go — list/get RunE plus the
// resolveMission / findLeadAgent lookup helpers.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const (
	covMissionIDCli8 = "cmission0123456789012345"
	covCrewIDCli8    = "ccrew01234567890123456789"
)

func covMissionStub(t *testing.T) *clitest.StubServer {
	t.Helper()
	stub := clitest.NewStubServer()
	t.Cleanup(stub.Close)
	covSetupCli8(t, stub.URL())
	return stub
}

func TestMissionListRunE_NoAuth(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := missionListCmd.RunE(missionListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}
}

func TestMissionListRunE_AllMissions(t *testing.T) {
	stub := covMissionStub(t)
	stub.OnGet("/api/v1/missions", clitest.JSONResponse(200, []map[string]any{
		{
			"id": covMissionIDCli8, "title": "Ship the beta", "status": "IN_PROGRESS",
			"lead_agent_slug": "viktor", "created_at": "2026-06-01T00:00:00Z",
			"task_stats": map[string]int{"total": 5, "completed": 2},
		},
		{
			"id": "cmission1123456789012345", "title": strings.Repeat("very long title ", 10),
			"status": "PLANNING", "lead_agent_slug": "eva", "created_at": "2026-06-02T00:00:00Z",
		},
	}))

	out := covCaptureStdoutCli8(t, func() {
		if err := missionListCmd.RunE(missionListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"Ship the beta", "2/5", "viktor", "IN_PROGRESS", "..."} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
}

func TestMissionListRunE_CrewFilter(t *testing.T) {
	stub := covMissionStub(t)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]any{
		{"id": covCrewIDCli8, "slug": "backend"},
	}))
	stub.OnGet("/api/v1/crews/"+covCrewIDCli8+"/missions", clitest.JSONResponse(200, []map[string]any{}))
	covSetFlagCli8(t, missionListCmd, "crew", "backend")

	covCaptureStdoutCli8(t, func() {
		if err := missionListCmd.RunE(missionListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if calls := stub.CallsFor("GET", "/api/v1/crews/"+covCrewIDCli8+"/missions"); len(calls) != 1 {
		t.Errorf("expected crew-scoped missions call, got %d", len(calls))
	}
}

func TestMissionListRunE_CrewNotFound(t *testing.T) {
	stub := covMissionStub(t)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]any{}))
	covSetFlagCli8(t, missionListCmd, "crew", "ghost")

	err := missionListCmd.RunE(missionListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "crew not found: ghost") {
		t.Errorf("expected crew-not-found; got %v", err)
	}
}

func TestMissionGetRunE_HappyPathWithTasks(t *testing.T) {
	stub := covMissionStub(t)
	stub.OnGet("/api/v1/missions", clitest.JSONResponse(200, []map[string]any{
		{"id": covMissionIDCli8, "crew_id": covCrewIDCli8},
	}))
	stub.OnGet("/api/v1/crews/"+covCrewIDCli8+"/missions/"+covMissionIDCli8, clitest.JSONResponse(200, map[string]any{
		"id": covMissionIDCli8, "title": "Ship the beta", "status": "IN_PROGRESS",
		"description": "do it", "lead_agent_name": "Viktor", "lead_agent_slug": "viktor",
		"created_at": "2026-06-01T00:00:00Z",
		"tasks": []map[string]any{
			{"id": "task-1", "title": "Write tests", "status": "COMPLETED", "agent_slug": "eva", "task_order": 1},
			{"id": "task-2", "title": "Fix bugs", "status": "PENDING", "task_order": 2},
		},
	}))

	out := covCaptureStdoutCli8(t, func() {
		if err := missionGetCmd.RunE(missionGetCmd, []string{covMissionIDCli8}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"Ship the beta", "Viktor (viktor)", "TASKS (2)", "Write tests", "eva", "PENDING"} {
		if !strings.Contains(out, want) {
			t.Errorf("get output missing %q:\n%s", want, out)
		}
	}
}

func TestMissionGetRunE_NotFound(t *testing.T) {
	stub := covMissionStub(t)
	stub.OnGet("/api/v1/missions", clitest.JSONResponse(200, []map[string]any{}))

	err := missionGetCmd.RunE(missionGetCmd, []string{"nope"})
	if err == nil || !strings.Contains(err.Error(), "mission not found: nope") {
		t.Errorf("expected mission-not-found; got %v", err)
	}
}

func TestResolveMission_PrefixMatch(t *testing.T) {
	stub := covMissionStub(t)
	stub.OnGet("/api/v1/missions", clitest.JSONResponse(200, []map[string]any{
		{"id": "cmissionaaaa567890123456", "crew_id": "crew-a"},
		{"id": covMissionIDCli8, "crew_id": covCrewIDCli8},
	}))
	client := newAPIClient()

	// Exact id.
	crew, full, err := resolveMission(client, covMissionIDCli8)
	if err != nil || crew != covCrewIDCli8 || full != covMissionIDCli8 {
		t.Errorf("exact match: got (%q,%q,%v)", crew, full, err)
	}

	// >=8-char prefix.
	crew, full, err = resolveMission(client, covMissionIDCli8[:10])
	if err != nil || crew != covCrewIDCli8 || full != covMissionIDCli8 {
		t.Errorf("prefix match: got (%q,%q,%v)", crew, full, err)
	}

	// Short prefix (<8) must NOT match.
	if _, _, err := resolveMission(client, covMissionIDCli8[:5]); err == nil {
		t.Error("short prefix should not resolve")
	}
}

func TestResolveMission_APIError(t *testing.T) {
	stub := covMissionStub(t)
	stub.OnGet("/api/v1/missions", clitest.ErrorResponse(500, "Internal server error"))
	client := newAPIClient()

	_, _, err := resolveMission(client, "whatever")
	if err == nil || !strings.Contains(err.Error(), "Internal server error") {
		t.Errorf("expected API error; got %v", err)
	}
}

func TestFindLeadAgent(t *testing.T) {
	stub := covMissionStub(t)
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]any{
		{"id": "agent-1", "slug": "eva", "agent_role": "AGENT", "crew_id": covCrewIDCli8},
		{"id": "agent-2", "slug": "viktor", "agent_role": "LEAD", "crew_id": covCrewIDCli8},
		{"id": "agent-3", "slug": "lead-elsewhere", "agent_role": "LEAD", "crew_id": "other-crew"},
	}))
	client := newAPIClient()

	id, err := findLeadAgent(client, covCrewIDCli8)
	if err != nil {
		t.Fatalf("findLeadAgent: %v", err)
	}
	if id != "agent-2" {
		t.Errorf("lead id: got %q want agent-2", id)
	}

	// No LEAD in this crew.
	if _, err := findLeadAgent(client, "crew-without-lead"); err == nil ||
		!strings.Contains(err.Error(), "no LEAD agent found") {
		t.Errorf("expected no-lead error; got %v", err)
	}
}

func TestFindLeadAgent_APIError(t *testing.T) {
	stub := covMissionStub(t)
	stub.OnGet("/api/v1/agents", clitest.ErrorResponse(500, "Internal server error"))
	client := newAPIClient()

	_, err := findLeadAgent(client, covCrewIDCli8)
	if err == nil || !strings.Contains(err.Error(), "Internal server error") {
		t.Errorf("expected API error; got %v", err)
	}
}

func TestMissionGetRunE_CompletedAndLongTaskTitles(t *testing.T) {
	stub := covMissionStub(t)
	stub.OnGet("/api/v1/missions", clitest.JSONResponse(200, []map[string]any{
		{"id": covMissionIDCli8, "crew_id": covCrewIDCli8},
	}))
	stub.OnGet("/api/v1/crews/"+covCrewIDCli8+"/missions/"+covMissionIDCli8, clitest.JSONResponse(200, map[string]any{
		"id": covMissionIDCli8, "title": "Done mission", "status": "COMPLETED",
		"lead_agent_name": "Viktor", "lead_agent_slug": "viktor",
		"created_at": "2026-06-01T00:00:00Z", "completed_at": "2026-06-02T00:00:00Z",
		"tasks": []map[string]any{
			{"id": "task-long", "title": strings.Repeat("long title ", 10), "status": "COMPLETED", "task_order": 1},
		},
	}))

	out := covCaptureStdoutCli8(t, func() {
		if err := missionGetCmd.RunE(missionGetCmd, []string{covMissionIDCli8}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "2026-06-02T00:00:00Z") {
		t.Errorf("completed_at missing:\n%s", out)
	}
	if !strings.Contains(out, "...") {
		t.Errorf("long task title not truncated:\n%s", out)
	}
}

// TestMissionRunE_ErrorBranches sweeps auth / workspace / transport /
// decode branches for list, get, and the lookup helpers.
func TestMissionRunE_ErrorBranches(t *testing.T) {
	missionsOK := clitest.JSONResponse(200, []map[string]any{
		{"id": covMissionIDCli8, "crew_id": covCrewIDCli8},
	})
	detail := "/api/v1/crews/" + covCrewIDCli8 + "/missions/" + covMissionIDCli8

	cases := []struct {
		name  string
		run   func(t *testing.T, stub *clitest.StubServer) error
		route func(*clitest.StubServer)
	}{
		{"list no workspace", func(t *testing.T, _ *clitest.StubServer) error {
			cliCfg = &cli.CLIConfig{Token: "tok", Server: cliCfg.Server}
			return missionListCmd.RunE(missionListCmd, nil)
		}, nil},
		{"list transport", func(_ *testing.T, _ *clitest.StubServer) error {
			return missionListCmd.RunE(missionListCmd, nil)
		}, func(s *clitest.StubServer) { s.OnGet("/api/v1/missions", covAbort()) }},
		{"list api error", func(_ *testing.T, _ *clitest.StubServer) error {
			return missionListCmd.RunE(missionListCmd, nil)
		}, func(s *clitest.StubServer) {
			s.OnGet("/api/v1/missions", clitest.ErrorResponse(500, "boom"))
		}},
		{"list decode", func(_ *testing.T, _ *clitest.StubServer) error {
			return missionListCmd.RunE(missionListCmd, nil)
		}, func(s *clitest.StubServer) { s.OnGet("/api/v1/missions", covNotJSON()) }},
		{"get no auth", func(t *testing.T, _ *clitest.StubServer) error {
			cliCfg = &cli.CLIConfig{Server: cliCfg.Server}
			return missionGetCmd.RunE(missionGetCmd, []string{covMissionIDCli8})
		}, nil},
		{"get no workspace", func(t *testing.T, _ *clitest.StubServer) error {
			cliCfg = &cli.CLIConfig{Token: "tok", Server: cliCfg.Server}
			return missionGetCmd.RunE(missionGetCmd, []string{covMissionIDCli8})
		}, nil},
		{"get resolve transport", func(_ *testing.T, _ *clitest.StubServer) error {
			return missionGetCmd.RunE(missionGetCmd, []string{covMissionIDCli8})
		}, func(s *clitest.StubServer) { s.OnGet("/api/v1/missions", covAbort()) }},
		{"get resolve decode", func(_ *testing.T, _ *clitest.StubServer) error {
			return missionGetCmd.RunE(missionGetCmd, []string{covMissionIDCli8})
		}, func(s *clitest.StubServer) { s.OnGet("/api/v1/missions", covNotJSON()) }},
		{"get detail transport", func(_ *testing.T, _ *clitest.StubServer) error {
			return missionGetCmd.RunE(missionGetCmd, []string{covMissionIDCli8})
		}, func(s *clitest.StubServer) {
			s.OnGet("/api/v1/missions", missionsOK)
			s.OnGet(detail, covAbort())
		}},
		{"get detail api error", func(_ *testing.T, _ *clitest.StubServer) error {
			return missionGetCmd.RunE(missionGetCmd, []string{covMissionIDCli8})
		}, func(s *clitest.StubServer) {
			s.OnGet("/api/v1/missions", missionsOK)
			s.OnGet(detail, clitest.ErrorResponse(404, "Mission not found"))
		}},
		{"get detail decode", func(_ *testing.T, _ *clitest.StubServer) error {
			return missionGetCmd.RunE(missionGetCmd, []string{covMissionIDCli8})
		}, func(s *clitest.StubServer) {
			s.OnGet("/api/v1/missions", missionsOK)
			s.OnGet(detail, covNotJSON())
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := clitest.NewStubServer()
			defer stub.Close()
			covSetupCli8(t, stub.URL())
			if c.route != nil {
				c.route(stub)
			}
			if err := c.run(t, stub); err == nil {
				t.Errorf("%s: expected error, got nil", c.name)
			}
		})
	}
}

func TestFindLeadAgent_TransportAndDecode(t *testing.T) {
	stub := covMissionStub(t)
	stub.OnGet("/api/v1/agents", covAbort())
	client := newAPIClient()
	if _, err := findLeadAgent(client, covCrewIDCli8); err == nil || !strings.Contains(err.Error(), "list agents") {
		t.Errorf("expected list-agents transport error; got %v", err)
	}

	stub.OnGet("/api/v1/agents", covNotJSON())
	if _, err := findLeadAgent(client, covCrewIDCli8); err == nil {
		t.Error("expected decode error, got nil")
	}
}
