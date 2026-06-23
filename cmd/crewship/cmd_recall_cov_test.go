package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func covRecallEntries() map[string]any {
	return map[string]any{
		"entries": []map[string]any{
			{
				"ts": "2026-04-30T10:13:00Z", "entry_type": "peer.escalation", "severity": "error",
				"summary": "auth deploy escalated", "crew_id": "backend", "agent_id": "viktor",
				"body": "lock contention during the auth deploy rollout",
			},
			{
				"ts": "2026-04-30T11:00:00Z", "entry_type": "deploy.done", "severity": "notice",
				"summary": "auth shipped", "crew_id": "backend",
				"body": map[string]any{"text": "the auth fix landed cleanly"},
			},
			{
				"ts": "garbled-ts", "entry_type": "note", "severity": "warn",
				"summary": "auth note", "agent_id": "eva",
			},
		},
		"count": 3,
	}
}

func TestRecallRunE_RendersSnippets(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/journal", clitest.JSONResponse(200, covRecallEntries()))

	out := covCaptureStdoutCli9(t, func() {
		if err := recallCmd.RunE(recallCmd, []string{"auth"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{
		`3 matches for "auth"`,
		"backend / viktor", // crew+agent scope
		"backend",          // crew-only scope
		"eva",              // agent-only scope
		// Query-term hits are wrapped in ANSI bold, so assert on the
		// snippet fragments around the highlighted word.
		"lock contention during the", // string body snippet
		"fix landed cleanly",         // map body snippet via "text"
	} {
		if !strings.Contains(out, want) {
			t.Errorf("recall output missing %q:\n%s", want, out)
		}
	}
	// Query term must be bold-highlighted somewhere.
	if !strings.Contains(out, cli.Bold+"auth"+cli.Reset) {
		t.Errorf("query not highlighted:\n%s", out)
	}

	calls := s.CallsFor("GET", "/api/v1/journal")
	if len(calls) != 1 {
		t.Fatalf("expected one journal GET, got %d", len(calls))
	}
	q := calls[0].Query
	if !strings.Contains(q, "q=auth") || !strings.Contains(q, "limit=20") {
		t.Errorf("query params wrong: %q", q)
	}
}

func TestRecallRunE_MultiWordQueryAndFilters(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/journal", clitest.JSONResponse(200, map[string]any{"entries": []map[string]any{}}))
	covSetFlagCli9(t, recallCmd, "since", "24h")
	covSetFlagCli9(t, recallCmd, "crew", covCrew) // CUID → no resolution round-trip
	covSetFlagCli9(t, recallCmd, "agent", "ag_1")
	covSetFlagCli9(t, recallCmd, "limit", "5")

	out := covCaptureStdoutCli9(t, func() {
		if err := recallCmd.RunE(recallCmd, []string{"rate", "limit"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "No matches.") {
		t.Errorf("empty-state line missing:\n%s", out)
	}
	calls := s.CallsFor("GET", "/api/v1/journal")
	if len(calls) != 1 {
		t.Fatalf("expected one GET, got %d", len(calls))
	}
	q := calls[0].Query
	for _, want := range []string{"q=rate+limit", "crew_id=" + covCrew, "agent_id=ag_1", "limit=5", "since="} {
		if !strings.Contains(q, want) {
			t.Errorf("query missing %q: %q", want, q)
		}
	}
}

func TestRecallRunE_CrewSlugResolution(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": "ccrewresolvedxyzabcd1", "slug": "backend-team"},
	}))
	s.OnGet("/api/v1/journal", clitest.JSONResponse(200, map[string]any{"entries": []map[string]any{}}))
	covSetFlagCli9(t, recallCmd, "crew", "backend-team")

	_ = covCaptureStdoutCli9(t, func() {
		if err := recallCmd.RunE(recallCmd, []string{"keeper"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	calls := s.CallsFor("GET", "/api/v1/journal")
	if len(calls) != 1 || !strings.Contains(calls[0].Query, "crew_id=ccrewresolvedxyzabcd1") {
		t.Errorf("crew slug not resolved to id: %+v", calls)
	}
}

func TestRecallRunE_JSONFormat(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/journal", clitest.JSONResponse(200, covRecallEntries()))
	flagFormat = "json"

	out := covCaptureStdoutCli9(t, func() {
		if err := recallCmd.RunE(recallCmd, []string{"auth"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, `"peer.escalation"`) {
		t.Errorf("json output missing entry type:\n%s", out)
	}
}

func TestRecallRunE_Validation(t *testing.T) {
	t.Run("query too long", func(t *testing.T) {
		covStubCli9(t)
		err := recallCmd.RunE(recallCmd, []string{strings.Repeat("x", 201)})
		if err == nil || !strings.Contains(err.Error(), "query too long: 201 chars") {
			t.Errorf("expected length error; got %v", err)
		}
	})
	t.Run("bad since", func(t *testing.T) {
		covStubCli9(t)
		covSetFlagCli9(t, recallCmd, "since", "yesterdayish")
		err := recallCmd.RunE(recallCmd, []string{"x"})
		if err == nil || !strings.Contains(err.Error(), "bad --since") {
			t.Errorf("expected bad --since; got %v", err)
		}
	})
	t.Run("server error", func(t *testing.T) {
		s := covStubCli9(t)
		s.OnGet("/api/v1/journal", clitest.ErrorResponse(500, "fts down"))
		err := recallCmd.RunE(recallCmd, []string{"x"})
		if err == nil || !strings.Contains(err.Error(), "fts down") {
			t.Errorf("expected server error; got %v", err)
		}
	})
	t.Run("no auth", func(t *testing.T) {
		covSaveState(t)
		cliCfg = &cli.CLIConfig{}
		if err := recallCmd.RunE(recallCmd, []string{"x"}); err == nil {
			t.Error("expected not-logged-in error")
		}
	})
	t.Run("no workspace", func(t *testing.T) {
		covSaveState(t)
		cliCfg = &cli.CLIConfig{Token: "tok"}
		err := recallCmd.RunE(recallCmd, []string{"x"})
		if err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Errorf("expected workspace error; got %v", err)
		}
	})
}

func TestPrintRecallEntry_Direct(t *testing.T) {
	out := covCaptureStdoutCli9(t, func() {
		printRecallEntry(map[string]any{
			"ts": "2026-04-30T10:13:00Z", "entry_type": "keeper.decision", "severity": "warn",
			"summary": "keeper denied request", "crew_id": "backend",
			"body": strings.Repeat("a", 100) + " keeper " + strings.Repeat("b", 200),
		}, "keeper")
	})
	if !strings.Contains(out, "2026-04-30 10:13") {
		t.Errorf("timestamp not reformatted:\n%s", out)
	}
	// "keeper" is bold-highlighted, so match the un-highlighted tail.
	if !strings.Contains(out, "denied request") {
		t.Errorf("summary missing:\n%s", out)
	}
	// Body windowing: match deeper than maxLen/2 → snippet starts with "...".
	if !strings.Contains(out, "...") {
		t.Errorf("expected windowed snippet ellipsis:\n%s", out)
	}
}
