package main

// Coverage tests for cmd_escalation.go — list (with client-side since /
// limit filtering), resolve, and pending-count.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestEscalationListRunE_CrewRequired(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	err := escalationListCmd.RunE(escalationListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--crew is required") {
		t.Errorf("expected --crew required; got %v", err)
	}
}

func TestEscalationListRunE_BadSince(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	covSetFlagCli8(t, escalationListCmd, "crew", covCrewIDCli8)
	covSetFlagCli8(t, escalationListCmd, "since", "not-a-duration")

	err := escalationListCmd.RunE(escalationListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "bad --since") {
		t.Errorf("expected bad --since; got %v", err)
	}
}

func TestEscalationListRunE_FiltersAndLimit(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	// JSON format so stdout is machine-checkable after client-side filtering.
	cliCfg.Format = "json"

	stub.OnGet("/api/v1/crews/"+covCrewIDCli8+"/escalations", clitest.JSONResponse(200, []map[string]any{
		{"id": "esc-new-1-padded-out-long", "from_slug": "viktor", "reason": strings.Repeat("r", 60),
			"status": "PENDING", "created_at": "2026-06-10T00:00:00Z"},
		{"id": "esc-new-2", "from_slug": "eva", "reason": "stuck",
			"status": "PENDING", "created_at": "2026-06-09T00:00:00Z"},
		{"id": "esc-new-3", "from_slug": "ada", "reason": "blocked",
			"status": "PENDING", "created_at": "2026-06-08T00:00:00Z"},
		{"id": "esc-ancient", "from_slug": "old", "reason": "ancient",
			"status": "PENDING", "created_at": "2020-01-01T00:00:00Z"},
	}))

	covSetFlagCli8(t, escalationListCmd, "crew", covCrewIDCli8)
	covSetFlagCli8(t, escalationListCmd, "status", "PENDING")
	covSetFlagCli8(t, escalationListCmd, "since", "2026-06-01T00:00:00Z")
	covSetFlagCli8(t, escalationListCmd, "limit", "2")

	out := covCaptureStdoutCli8(t, func() {
		if err := escalationListCmd.RunE(escalationListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})

	// Server-side status filter must be in the query string.
	calls := stub.CallsFor("GET", "/api/v1/crews/"+covCrewIDCli8+"/escalations")
	if len(calls) != 1 {
		t.Fatalf("expected 1 escalations GET, got %d", len(calls))
	}
	if !strings.Contains(calls[0].Query, "status=PENDING") {
		t.Errorf("status filter not propagated: %q", calls[0].Query)
	}

	// since drops the 2020 row, limit cuts 3 → 2.
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("output not JSON: %v\n%s", err, out)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows after since+limit, got %d: %s", len(rows), out)
	}
	if rows[0]["id"] != "esc-new-1-padded-out-long" || rows[1]["id"] != "esc-new-2" {
		t.Errorf("unexpected rows kept: %v", rows)
	}
}

func TestEscalationListRunE_TableOutput(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/crews/"+covCrewIDCli8+"/escalations", clitest.JSONResponse(200, []map[string]any{
		{"id": "esc-1-very-long-identifier", "from_slug": "viktor",
			"reason": strings.Repeat("why ", 20), "status": "PENDING",
			"created_at": "2026-06-10T00:00:00Z"},
	}))
	covSetFlagCli8(t, escalationListCmd, "crew", covCrewIDCli8)

	out := covCaptureStdoutCli8(t, func() {
		if err := escalationListCmd.RunE(escalationListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	// ID truncated to 12 chars, reason truncated with ellipsis.
	if !strings.Contains(out, "esc-1-very-l") || strings.Contains(out, "esc-1-very-long-identifier") {
		t.Errorf("id not truncated to 12 chars:\n%s", out)
	}
	if !strings.Contains(out, "...") {
		t.Errorf("reason not truncated:\n%s", out)
	}
}

func TestEscalationListRunE_NoAuth(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := escalationListCmd.RunE(escalationListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}
}

func TestEscalationResolveRunE_HappyPath(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnPatch("/api/v1/escalations/esc-1/resolve", clitest.JSONResponse(200, map[string]any{"ok": true}))
	covSetFlagCli8(t, escalationResolveCmd, "resolution", "restarted the container")

	if err := escalationResolveCmd.RunE(escalationResolveCmd, []string{"esc-1"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	calls := stub.CallsFor("PATCH", "/api/v1/escalations/esc-1/resolve")
	if len(calls) != 1 {
		t.Fatalf("expected 1 PATCH, got %d", len(calls))
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["resolution"] != "restarted the container" {
		t.Errorf("resolution not in body: %v", body)
	}
}

func TestEscalationResolveRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnPatch("/api/v1/escalations/ghost/resolve", clitest.ErrorResponse(404, "Escalation not found"))

	err := escalationResolveCmd.RunE(escalationResolveCmd, []string{"ghost"})
	if err == nil || !strings.Contains(err.Error(), "Escalation not found") {
		t.Errorf("expected not-found; got %v", err)
	}
}

func TestEscalationPendingCountRunE_Table(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/escalations/pending-count", clitest.JSONResponse(200, map[string]int{"count": 4}))

	out := covCaptureStdoutCli8(t, func() {
		if err := escalationPendingCountCmd.RunE(escalationPendingCountCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if strings.TrimSpace(out) != "4" {
		t.Errorf("pending-count table output: got %q want %q", strings.TrimSpace(out), "4")
	}
}

func TestEscalationPendingCountRunE_JSONAndYAML(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/escalations/pending-count", clitest.JSONResponse(200, map[string]int{"count": 4}))

	cliCfg.Format = "json"
	out := covCaptureStdoutCli8(t, func() {
		if err := escalationPendingCountCmd.RunE(escalationPendingCountCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, `"count": 4`) && !strings.Contains(out, `"count":4`) {
		t.Errorf("json output missing count: %q", out)
	}

	cliCfg.Format = "yaml"
	out = covCaptureStdoutCli8(t, func() {
		if err := escalationPendingCountCmd.RunE(escalationPendingCountCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "count: 4") {
		t.Errorf("yaml output missing count: %q", out)
	}
}

// TestEscalationRunE_ErrorBranches sweeps the remaining workspace /
// transport / decode branches.
func TestEscalationRunE_ErrorBranches(t *testing.T) {
	crewsOK := clitest.JSONResponse(200, []map[string]any{{"id": covCrewIDCli8, "slug": "backend"}})
	escPath := "/api/v1/crews/" + covCrewIDCli8 + "/escalations"
	withCrew := func(t *testing.T) { covSetFlagCli8(t, escalationListCmd, "crew", "backend") }

	cases := []struct {
		name    string
		run     func() error
		route   func(*clitest.StubServer)
		noAuth  bool
		noWS    bool
		prepare func(*testing.T)
	}{
		{name: "list no workspace", run: func() error { return escalationListCmd.RunE(escalationListCmd, nil) }, noWS: true},
		{name: "resolve no auth", run: func() error { return escalationResolveCmd.RunE(escalationResolveCmd, []string{"e"}) }, noAuth: true},
		{name: "resolve no workspace", run: func() error { return escalationResolveCmd.RunE(escalationResolveCmd, []string{"e"}) }, noWS: true},
		{name: "pending no auth", run: func() error { return escalationPendingCountCmd.RunE(escalationPendingCountCmd, nil) }, noAuth: true},
		{name: "pending no workspace", run: func() error { return escalationPendingCountCmd.RunE(escalationPendingCountCmd, nil) }, noWS: true},
		{name: "list crew resolve transport", prepare: withCrew,
			run:   func() error { return escalationListCmd.RunE(escalationListCmd, nil) },
			route: func(s *clitest.StubServer) { s.OnGet("/api/v1/crews", covAbort()) }},
		{name: "list transport", prepare: withCrew,
			run: func() error { return escalationListCmd.RunE(escalationListCmd, nil) },
			route: func(s *clitest.StubServer) {
				s.OnGet("/api/v1/crews", crewsOK)
				s.OnGet(escPath, covAbort())
			}},
		{name: "list api error", prepare: withCrew,
			run: func() error { return escalationListCmd.RunE(escalationListCmd, nil) },
			route: func(s *clitest.StubServer) {
				s.OnGet("/api/v1/crews", crewsOK)
				s.OnGet(escPath, clitest.ErrorResponse(500, "boom"))
			}},
		{name: "list decode", prepare: withCrew,
			run: func() error { return escalationListCmd.RunE(escalationListCmd, nil) },
			route: func(s *clitest.StubServer) {
				s.OnGet("/api/v1/crews", crewsOK)
				s.OnGet(escPath, covNotJSON())
			}},
		{name: "resolve transport",
			run:   func() error { return escalationResolveCmd.RunE(escalationResolveCmd, []string{"e"}) },
			route: func(s *clitest.StubServer) { s.OnPatch("/api/v1/escalations/e/resolve", covAbort()) }},
		{name: "pending transport",
			run:   func() error { return escalationPendingCountCmd.RunE(escalationPendingCountCmd, nil) },
			route: func(s *clitest.StubServer) { s.OnGet("/api/v1/escalations/pending-count", covAbort()) }},
		{name: "pending decode",
			run:   func() error { return escalationPendingCountCmd.RunE(escalationPendingCountCmd, nil) },
			route: func(s *clitest.StubServer) { s.OnGet("/api/v1/escalations/pending-count", covNotJSON()) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := clitest.NewStubServer()
			defer stub.Close()
			covSetupCli8(t, stub.URL())
			if c.noAuth {
				cliCfg = &cli.CLIConfig{Server: stub.URL()}
			} else if c.noWS {
				cliCfg = &cli.CLIConfig{Token: "tok", Server: stub.URL()}
			}
			if c.prepare != nil {
				c.prepare(t)
			}
			if c.route != nil {
				c.route(stub)
			}
			if err := c.run(); err == nil {
				t.Errorf("%s: expected error, got nil", c.name)
			}
		})
	}
}

func TestEscalationPendingCountRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/escalations/pending-count", clitest.ErrorResponse(500, "Internal server error"))

	err := escalationPendingCountCmd.RunE(escalationPendingCountCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "Internal server error") {
		t.Errorf("expected API error; got %v", err)
	}
}
