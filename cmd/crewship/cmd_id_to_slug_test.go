package main

// Raw internal ids in columns a human reads (#2413 item 2).
//
// Three commands rendered a CUID where every other list command renders a
// name: `routine list`'s AUTHOR CREW, `cost`'s "Top spenders" and "By crew",
// and `history`'s agent column (which printed "?" for a routine run, plus an
// empty trigger). A cuid there is not a cosmetic problem — nothing else in
// the CLI accepts one in that position, so the operator's only next move is
// to grep another list by hand.
//
// Each test below asserts the human rendering names the thing, and — where
// the command has one — that the MACHINE rendering still carries the id. The
// second half matters as much as the first: a script cross-referencing the
// cost ledger joins on the id, and "fixing" the JSON would break it.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// journalLookupBody is what GET /api/v1/journal/lookup answers with: the
// denormalised id→slug table fetchWorkspaceSlugs reads.
func journalLookupBody() map[string]any {
	return map[string]any{
		"agents": []map[string]any{
			{"id": "cmtov6c2f010216ea53ca", "slug": "viktor", "name": "Viktor"},
		},
		"crews": []map[string]any{
			{"id": "cmtov6bxv00fa7a9515de", "slug": "engineering", "name": "Engineering"},
			{"id": "crew_noslug", "slug": "", "name": "Legacy Crew"},
		},
	}
}

func TestRoutineList_AuthorCrewShowsSlugNotCUID(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/workspaces/"+covWSCli9+"/pipelines", clitest.JSONResponse(200, []map[string]any{
		{"slug": "nightly-digest", "status": "active", "invocation_count": 3,
			"author_crew_id": "cmtov6bxv00fa7a9515de", "description": "d"},
	}))
	s.OnGet("/api/v1/journal/lookup", clitest.JSONResponse(200, journalLookupBody()))

	out := covCaptureStdoutCli9(t, func() {
		if err := pipelineListCmd.RunE(pipelineListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "engineering") {
		t.Errorf("AUTHOR CREW should name the crew by slug:\n%s", out)
	}
	if strings.Contains(out, "cmtov6bxv00fa7a9515de") {
		t.Errorf("AUTHOR CREW still prints the raw crew cuid:\n%s", out)
	}
}

func TestRoutineList_UnresolvableCrewFallsBackToTheID(t *testing.T) {
	// The lookup is capped server-side and a deleted crew is not in it. The
	// id is then the only truth left, and it beats a placeholder: a "—" would
	// destroy the one handle an operator could paste into another command.
	s := covStubCli9(t)
	s.OnGet("/api/v1/workspaces/"+covWSCli9+"/pipelines", clitest.JSONResponse(200, []map[string]any{
		{"slug": "orphaned", "author_crew_id": "crew_vanished", "description": "d"},
	}))
	s.OnGet("/api/v1/journal/lookup", clitest.JSONResponse(200, journalLookupBody()))

	out := covCaptureStdoutCli9(t, func() {
		if err := pipelineListCmd.RunE(pipelineListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "crew_vanished") {
		t.Errorf("an unresolvable crew id must still be shown:\n%s", out)
	}
}

func TestRoutineList_JSONKeepsTheCrewID(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/workspaces/"+covWSCli9+"/pipelines", clitest.JSONResponse(200, []map[string]any{
		{"slug": "nightly-digest", "author_crew_id": "cmtov6bxv00fa7a9515de"},
	}))
	s.OnGet("/api/v1/journal/lookup", clitest.JSONResponse(200, journalLookupBody()))
	flagFormat = "json" // covStubCli9 restores it via covSaveState

	out := covCaptureStdoutCli9(t, func() {
		if err := pipelineListCmd.RunE(pipelineListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(rows) != 1 || rows[0]["author_crew_id"] != "cmtov6bxv00fa7a9515de" {
		t.Errorf("the machine document must keep the id a script joins on: %v", rows)
	}
}

func TestCost_TopSpendersAndByCrewNameTheScope(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/paymaster/top-spenders", clitest.JSONResponse(200, map[string]any{
		"rows": []map[string]any{
			{"scope_kind": "agent", "scope_id": "cmtov6c2f010216ea53ca", "cost_usd": 1.5, "call_count": 4},
		},
	}))
	s.OnGet("/api/v1/paymaster/spend/by-crew", clitest.JSONResponse(200, map[string]any{
		"rows": []map[string]any{
			{"crew_id": "cmtov6bxv00fa7a9515de", "cost_usd": 1.5, "call_count": 4},
			{"crew_id": "", "cost_usd": 0.25, "call_count": 1},
		},
	}))
	s.OnGet("/api/v1/paymaster/subscriptions", clitest.JSONResponse(200, map[string]any{"rows": []map[string]any{}}))
	s.OnGet("/api/v1/journal/lookup", clitest.JSONResponse(200, journalLookupBody()))

	out := covCaptureStdoutCli9(t, func() {
		if err := costCmd.RunE(costCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "agent/viktor") {
		t.Errorf("Top spenders should read agent/<slug>:\n%s", out)
	}
	if !strings.Contains(out, "engineering") {
		t.Errorf("By crew should name the crew:\n%s", out)
	}
	if strings.Contains(out, "cmtov6c2f010216ea53ca") || strings.Contains(out, "cmtov6bxv00fa7a9515de") {
		t.Errorf("cost still prints raw cuids:\n%s", out)
	}
	// A ledger row with no crew is real — the GROUP BY coalesces unattributed
	// calls — and an empty cell would read as a rendering bug.
	if !strings.Contains(out, "(unattributed)") {
		t.Errorf("a crew-less spend row should say so:\n%s", out)
	}
}

func TestHistory_RoutineRunNamesTheRoutineInsteadOfQuestionMark(t *testing.T) {
	recent := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	s := covStubCli9(t)
	s.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{"data": []map[string]any{
		// A routine run: no agent by design, trigger recovered from
		// pipeline_runs.triggered_via by the server.
		{"id": "rn1", "kind": "pipeline", "pipeline_slug": "nightly-digest",
			"status": "completed", "trigger_type": "schedule", "created_at": recent},
		// A routine run the server could not name, and with no trigger
		// recorded anywhere.
		{"id": "rn2", "kind": "pipeline", "status": "completed", "trigger_type": "", "created_at": recent},
		// An agent run is unchanged.
		{"id": "ra1", "kind": "agent", "agent_slug": "viktor",
			"status": "completed", "trigger_type": "chat", "created_at": recent},
	}}))

	out := covCaptureStdoutCli9(t, func() {
		if err := historyCmd.RunE(historyCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "nightly-digest") {
		t.Errorf("a routine run should name its routine:\n%s", out)
	}
	if !strings.Contains(out, "(routine)") {
		t.Errorf("an unnameable routine run should say it is a routine, not \"?\":\n%s", out)
	}
	if strings.Contains(out, "?") {
		t.Errorf("no row here is unknown, so nothing should render as \"?\":\n%s", out)
	}
	if !strings.Contains(out, "schedule") {
		t.Errorf("the recovered trigger should be shown:\n%s", out)
	}
	if !strings.Contains(out, "—") {
		t.Errorf("a missing trigger should read as a gap, not an empty column:\n%s", out)
	}
	if !strings.Contains(out, "viktor") {
		t.Errorf("agent runs must be unaffected:\n%s", out)
	}
}
