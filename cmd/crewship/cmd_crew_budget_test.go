package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// These drive the commands behind PUT/GET/DELETE /api/v1/crews/{id}/budget
// and POST /api/v1/agents/{id}/release, the endpoints the Crew settings
// Budget card and the Team tab's Release now button use.

func budgetStub(t *testing.T) *clitest.StubServer {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]any{{"id": covCrewIDCli5, "slug": "research"}}))
	return stub
}

func TestCrewBudgetSet(t *testing.T) {
	tests := []struct {
		name     string
		limit    string
		mode     string
		wantBody map[string]any
		wantErr  string
	}{
		{name: "limit only", limit: "80", wantBody: map[string]any{"limit_usd": float64(80)}},
		{name: "with mode", limit: "12.5", mode: "soft", wantBody: map[string]any{"limit_usd": 12.5, "mode": "soft"}},
		{name: "refuses zero", limit: "0", wantErr: "greater than 0"},
		{name: "refuses text", limit: "lots", wantErr: "number"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := budgetStub(t)
			path := "/api/v1/crews/" + covCrewIDCli5 + "/budget"
			stub.OnPut(path, clitest.JSONResponse(200, map[string]any{"crew_id": covCrewIDCli5, "window": "month", "limit_usd": 80, "mode": "tiered", "spent_usd": 3, "state": "ok"}))
			covSetFlagCli8(t, crewBudgetSetCmd, "limit", tc.limit)
			if tc.mode != "" {
				covSetFlagCli8(t, crewBudgetSetCmd, "mode", tc.mode)
			}
			var err error
			out := covCaptureStdoutCli5(t, func() { err = crewBudgetSetCmd.RunE(crewBudgetSetCmd, []string{"research"}) })
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				if len(stub.CallsFor("PUT", path)) != 0 {
					t.Error("a refused value must not reach the server")
				}
				return
			}
			if err != nil {
				t.Fatalf("RunE: %v (%s)", err, out)
			}
			calls := stub.CallsFor("PUT", path)
			if len(calls) != 1 {
				t.Fatalf("PUT calls = %d", len(calls))
			}
			var body map[string]any
			clitest.MustDecodeJSONBody(calls[0].Body, &body)
			for k, v := range tc.wantBody {
				if body[k] != v {
					t.Errorf("body[%s] = %v, want %v", k, body[k], v)
				}
			}
		})
	}
}

func TestCrewBudgetGetAndClear(t *testing.T) {
	stub := budgetStub(t)
	flagFormat = "json"
	path := "/api/v1/crews/" + covCrewIDCli5 + "/budget"
	stub.OnGet(path, clitest.JSONResponse(200, map[string]any{"crew_id": covCrewIDCli5, "window": "month", "limit_usd": 80, "mode": "tiered", "spent_usd": 3, "state": "ok"}))
	stub.OnDelete(path, clitest.JSONResponse(200, map[string]any{"crew_id": covCrewIDCli5, "window": "month", "limit_usd": nil, "spent_usd": 3, "state": "none"}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = crewBudgetGetCmd.RunE(crewBudgetGetCmd, []string{"research"}) })
	if err != nil || !strings.Contains(out, `"limit_usd": 80`) {
		t.Fatalf("get: %v\n%s", err, out)
	}
	covSetFlagCli8(t, crewBudgetClearCmd, "yes", "true")
	out = covCaptureStdoutCli5(t, func() { err = crewBudgetClearCmd.RunE(crewBudgetClearCmd, []string{"research"}) })
	if err != nil || !strings.Contains(out, `"state": "none"`) {
		t.Fatalf("clear: %v\n%s", err, out)
	}
}

func TestAgentReleaseRunE(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]any{{"id": covAgentIDCli5, "slug": "pager"}}))
	path := "/api/v1/agents/" + covAgentIDCli5 + "/release"
	stub.OnPost(path, clitest.JSONResponse(200, map[string]any{"id": covAgentIDCli5, "released": true, "expired_at": "2026-10-19T10:00:00Z"}))
	covSetFlagCli8(t, releaseCmd, "yes", "true")
	var err error
	out := covCaptureStdoutCli5(t, func() { err = releaseCmd.RunE(releaseCmd, []string{"pager"}) })
	if err != nil {
		t.Fatalf("RunE: %v\n%s", err, out)
	}
	if len(stub.CallsFor("POST", path)) != 1 {
		t.Errorf("release POST calls = %d, want 1", len(stub.CallsFor("POST", path)))
	}

	stub.OnPost(path, clitest.ErrorResponse(409, "The hire is running; stop its run first"))
	err = releaseCmd.RunE(releaseCmd, []string{"pager"})
	if err == nil || !strings.Contains(err.Error(), "running") {
		t.Errorf("a refused release must surface the server's reason, got %v", err)
	}
}

func TestRoutineCalendarICSFlag(t *testing.T) {
	stub := covSetupCli5(t)
	path := "/api/v1/workspaces/" + covWSCli5 + "/pipelines/calendar.ics"
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nEND:VCALENDAR\r\n"
	stub.OnGet(path, func(r *http.Request, body []byte) (int, []byte, string) {
		return 200, []byte(ics), "text/calendar; charset=utf-8"
	})
	covSetFlagCli8(t, routineCalendarCmd, "from", "2030-03-01T00:00:00Z")
	covSetFlagCli8(t, routineCalendarCmd, "to", "2030-03-08T00:00:00Z")
	covSetFlagCli8(t, routineCalendarCmd, "ics", "true")
	var err error
	out := covCaptureStdoutCli5(t, func() { err = routineCalendarCmd.RunE(routineCalendarCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if out != ics {
		t.Errorf("stdout = %q, want the file verbatim", out)
	}
	calls := stub.CallsFor("GET", path)
	if len(calls) != 1 || !strings.Contains(calls[0].Query, "from=2030-03-01T00%3A00%3A00Z") {
		t.Errorf("calls = %+v", calls)
	}
}
