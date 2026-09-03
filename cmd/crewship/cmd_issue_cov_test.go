package main

// Coverage tests for cmd_issue.go — the formatting helpers, fetchIssue,
// and the issue list / get RunE paths.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func covStrPtrCli6(s string) *string { return &s }

// ─── helpers ─────────────────────────────────────────────────────────────

func TestIssueRelativeTime(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name string
		ts   string
		want string
	}{
		{"just now", now.Add(-10 * time.Second).Format(time.RFC3339Nano), "just now"},
		{"minutes", now.Add(-5 * time.Minute).Format(time.RFC3339Nano), "5m ago"},
		{"hours", now.Add(-3 * time.Hour).Format(time.RFC3339Nano), "3h ago"},
		{"days", now.Add(-5 * 24 * time.Hour).Format(time.RFC3339Nano), "5d ago"},
		{"months", now.Add(-65 * 24 * time.Hour).Format(time.RFC3339Nano), "2mo ago"},
		{"years", now.Add(-800 * 24 * time.Hour).Format(time.RFC3339Nano), "2y ago"},
		{"plain RFC3339 accepted", now.Add(-2 * time.Hour).Truncate(time.Second).Format(time.RFC3339), "2h ago"},
		{"unparseable passthrough", "yesterday-ish", "yesterday-ish"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := issueRelativeTime(tc.ts); got != tc.want {
				t.Errorf("issueRelativeTime(%q) = %q, want %q", tc.ts, got, tc.want)
			}
		})
	}
}

func TestTruncateStr(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is too long", 10, "this is..."},
		{"abcdef", 3, "abc"},
		{"abcdef", 2, "ab"},
	}
	for _, tc := range cases {
		if got := truncateStr(tc.in, tc.max); got != tc.want {
			t.Errorf("truncateStr(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
	}
}

func TestDerefStr(t *testing.T) {
	if got := derefStr(nil, "-"); got != "-" {
		t.Errorf("nil deref = %q", got)
	}
	if got := derefStr(covStrPtrCli6(""), "fallback"); got != "fallback" {
		t.Errorf("empty deref = %q", got)
	}
	if got := derefStr(covStrPtrCli6("value"), "-"); got != "value" {
		t.Errorf("value deref = %q", got)
	}
}

func TestCapitalizePriority(t *testing.T) {
	cases := map[string]string{
		"":       "-",
		"urgent": "Urgent",
		"LOW":    "Low",
		"mEdIuM": "Medium",
	}
	for in, want := range cases {
		if got := capitalizePriority(in); got != want {
			t.Errorf("capitalizePriority(%q) = %q, want %q", in, got, want)
		}
	}
}

// ─── fetchIssue ──────────────────────────────────────────────────────────

func TestFetchIssue_Happy(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/issues/ENG-1", clitest.JSONResponse(200, map[string]any{
		"id": "ciss1", "crew_id": covCrewIDCli6, "identifier": "ENG-1",
		"title": "Fix the bug", "status": "TODO", "priority": "high",
	}))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	issue, err := fetchIssue(client, "ENG-1")
	if err != nil {
		t.Fatalf("fetchIssue: %v", err)
	}
	if issue.Title != "Fix the bug" || issue.CrewID != covCrewIDCli6 || derefStr(issue.Identifier, "") != "ENG-1" {
		t.Errorf("issue fields wrong: %+v", issue)
	}
}

func TestFetchIssue_NotFound(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/issues/ENG-404", clitest.ErrorResponse(404, "issue not found"))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	_, err := fetchIssue(client, "ENG-404")
	if err == nil || !strings.Contains(err.Error(), "issue not found") {
		t.Errorf("expected 404 surfaced, got %v", err)
	}
}

func TestFetchIssue_DecodeError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/issues/ENG-1", clitest.TextResponse(200, "not json"))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	_, err := fetchIssue(client, "ENG-1")
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error, got %v", err)
	}
}

// ─── issue list ──────────────────────────────────────────────────────────

func covIssueListPayload() []map[string]any {
	return []map[string]any{
		{
			"id": "ciss0000000000000001", "crew_id": covCrewIDCli6, "crew_slug": "eng",
			"identifier": "ENG-1", "title": "A very important issue title that goes on and on",
			"status": "TODO", "priority": "high",
			"assignee_name": "Viktor",
			"labels":        []map[string]string{{"id": "l1", "name": "bug", "color": "#f00"}},
			"updated_at":    time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339Nano),
			"created_at":    time.Now().UTC().Add(-3 * time.Hour).Format(time.RFC3339Nano),
		},
		{
			// identifier nil → list falls back to truncated raw id.
			"id": "ciss0000000000000002", "crew_id": covCrewIDCli6, "crew_slug": "eng",
			"title": "Short", "status": "DONE", "priority": "",
			"updated_at": time.Now().UTC().Format(time.RFC3339Nano),
			"created_at": time.Now().UTC().Format(time.RFC3339Nano),
		},
	}
}

func TestIssueListRunE_FiltersForwarded(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, issueListCmd, "status", "TODO")
	covSetFlagCli6(t, issueListCmd, "priority", "high")
	covSetFlagCli6(t, issueListCmd, "crew", covCrewIDCli6) // CUID → no resolution call
	covSetFlagCli6(t, issueListCmd, "assignee", "agent-1")
	covSetFlagCli6(t, issueListCmd, "label", "bug")
	covSetFlagCli6(t, issueListCmd, "search", "panic")
	covSetFlagCli6(t, issueListCmd, "limit", "5")

	stub.OnGet("/api/v1/issues", clitest.JSONResponse(200, covIssueListPayload()))

	out, err := covCaptureStdoutCli6(t, func() error {
		return issueListCmd.RunE(issueListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}

	calls := stub.CallsFor("GET", "/api/v1/issues")
	if len(calls) != 1 {
		t.Fatalf("expected 1 list call, got %d", len(calls))
	}
	q := calls[0].Query
	for _, want := range []string{
		"status=TODO", "priority=high", "crew_id=" + covCrewIDCli6,
		"assignee_id=agent-1", "label=bug", "search=panic", "limit=5",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query missing %q: %q", want, q)
		}
	}

	if !strings.Contains(out, "ENG-1") || !strings.Contains(out, "Viktor") {
		t.Errorf("table missing first issue: %q", out)
	}
	// Identifier-less issue falls back to the first 12 chars of its id.
	if !strings.Contains(out, "ciss00000000") {
		t.Errorf("id fallback missing: %q", out)
	}
}

// #2286: the board (and the CLI mirroring it) fetched at most 100 rows with
// no total exposed anywhere — a workspace with 101+ issues looked identical
// to one with exactly 100. GET /api/v1/issues now reports the true total and
// whether the page was partial via X-Total-Count / X-Has-More response
// headers (internal/api/issue_handler_crud.go); `issue list` prints a
// footer from them and gains --offset to page past the cap. Needs response
// headers clitest.StubServer's Handler type can't set, so this drives a raw
// httptest.Server directly — same cliCfg wiring covSetupCli6 does, minus the
// StubServer dependency.
func TestIssueListRunE_TotalCountFooter_HasMore(t *testing.T) {
	guardCLIState(t)
	saveCLIState(t)
	flagServer = ""
	flagWorkspace = ""
	covResetFlagsCli6(t, issueListCmd, "status", "priority", "crew", "assignee", "label", "search", "limit", "offset")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/issues" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("X-Total-Count", "143")
		w.Header().Set("X-Has-More", "true")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"ciss1","crew_id":"ccrew1","crew_slug":"eng","identifier":"ENG-1","title":"A","status":"TODO","priority":"low"}]`))
	}))
	defer srv.Close()
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: covWorkspaceIDCli6, Server: srv.URL}

	out, err := covCaptureStdoutCli6(t, func() error {
		return issueListCmd.RunE(issueListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// The S1 footer (list_paging.go): the range shown, the total, the next
	// offset. X-Limit/X-Offset are absent here, so the range starts at 1.
	if !strings.Contains(out, "showing 1–1 of 143") || !strings.Contains(out, "--offset 1") {
		t.Errorf("footer missing/wrong: %q", out)
	}
}

func TestIssueListRunE_TotalCountFooter_LastPage(t *testing.T) {
	guardCLIState(t)
	saveCLIState(t)
	flagServer = ""
	flagWorkspace = ""
	covResetFlagsCli6(t, issueListCmd, "status", "priority", "crew", "assignee", "label", "search", "limit", "offset")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Total-Count", "1")
		w.Header().Set("X-Has-More", "false")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"ciss1","crew_id":"ccrew1","crew_slug":"eng","identifier":"ENG-1","title":"A","status":"TODO","priority":"low"}]`))
	}))
	defer srv.Close()
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: covWorkspaceIDCli6, Server: srv.URL}

	out, err := covCaptureStdoutCli6(t, func() error {
		return issueListCmd.RunE(issueListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// Everything is on screen: the S1 footer says nothing rather than
	// "showing 1–1 of 1", and above all does not offer a next page.
	if strings.Contains(out, "showing") {
		t.Errorf("a complete list must not print a paging footer: %q", out)
	}
	if strings.Contains(out, "--offset") {
		t.Errorf("last page should not suggest --offset: %q", out)
	}
}

func TestIssueListRunE_Offset_Forwarded(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, issueListCmd, "offset", "50")

	stub.OnGet("/api/v1/issues", clitest.JSONResponse(200, covIssueListPayload()))

	_, err := covCaptureStdoutCli6(t, func() error {
		return issueListCmd.RunE(issueListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := stub.CallsFor("GET", "/api/v1/issues")
	if len(calls) != 1 || !strings.Contains(calls[0].Query, "offset=50") {
		t.Fatalf("expected offset=50 forwarded, got %+v", calls)
	}
}

func TestIssueListRunE_CrewResolveError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, issueListCmd, "crew", "ghost")

	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))

	err := issueListCmd.RunE(issueListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "crew not found: ghost") {
		t.Errorf("expected crew-not-found, got %v", err)
	}
}

func TestIssueListRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covResetFlagsCli6(t, issueListCmd, "status", "priority", "crew", "assignee", "label", "search", "limit")

	stub.OnGet("/api/v1/issues", clitest.ErrorResponse(500, "boom"))

	err := issueListCmd.RunE(issueListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected 500 surfaced, got %v", err)
	}
}

func TestIssueListRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	err := issueListCmd.RunE(issueListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected 'not logged in', got %v", err)
	}
}

// ─── issue get ───────────────────────────────────────────────────────────

func covIssueDetailPayload() map[string]any {
	return map[string]any{
		"id": "ciss0000000000000001", "crew_id": covCrewIDCli6, "crew_slug": "eng",
		"identifier": "ENG-1", "title": "Fix the bug",
		"description": "It *crashes* on startup.",
		"status":      "IN_PROGRESS", "priority": "urgent",
		"assignee_name": "Viktor", "assignee_type": "agent",
		"mission_type":  "STANDARD",
		"labels":        []map[string]string{{"id": "l1", "name": "bug", "color": "#f00"}},
		"comment_count": 1,
		"created_at":    time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339Nano),
		"updated_at":    time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339Nano),
	}
}

func TestIssueGetRunE_HappyWithComments(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	stub.OnGet("/api/v1/issues/ENG-1", clitest.JSONResponse(200, covIssueDetailPayload()))
	stub.OnGet(fmt.Sprintf("/api/v1/crews/%s/issues/ENG-1/comments", covCrewIDCli6),
		clitest.JSONResponse(200, []map[string]any{
			{"id": "c1", "body": "On it.", "author_name": "Viktor",
				"created_at": time.Now().UTC().Add(-30 * time.Minute).Format(time.RFC3339Nano)},
		}))

	out, err := covCaptureStdoutCli6(t, func() error {
		return issueGetCmd.RunE(issueGetCmd, []string{"ENG-1"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"ENG-1", "Fix the bug", "Urgent", "Viktor", "Description:", "Comments:", "@Viktor", "On it."} {
		if !strings.Contains(out, want) {
			t.Errorf("detail output missing %q: %q", want, out)
		}
	}
}

func TestIssueGetRunE_CommentsFailureIsNonFatal(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	stub.OnGet("/api/v1/issues/ENG-1", clitest.JSONResponse(200, covIssueDetailPayload()))
	stub.OnGet(fmt.Sprintf("/api/v1/crews/%s/issues/ENG-1/comments", covCrewIDCli6),
		clitest.ErrorResponse(500, "comments down"))

	out, err := covCaptureStdoutCli6(t, func() error {
		return issueGetCmd.RunE(issueGetCmd, []string{"ENG-1"})
	})
	if err != nil {
		t.Fatalf("comments failure must be non-fatal, got %v", err)
	}
	if !strings.Contains(out, "Fix the bug") {
		t.Errorf("issue detail must still render: %q", out)
	}
	// The "Comments:" metadata row (count) still renders, but no comment
	// bodies may appear.
	if strings.Contains(out, "On it.") || strings.Contains(out, "@Viktor") {
		t.Errorf("comment bodies must be skipped on failure: %q", out)
	}
}

func TestIssueGetRunE_FetchError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	stub.OnGet("/api/v1/issues/ENG-404", clitest.ErrorResponse(404, "issue not found"))

	err := issueGetCmd.RunE(issueGetCmd, []string{"ENG-404"})
	if err == nil || !strings.Contains(err.Error(), "issue not found") {
		t.Errorf("expected fetch error, got %v", err)
	}
}

func TestIssueGetRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	err := issueGetCmd.RunE(issueGetCmd, []string{"ENG-1"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected 'not logged in', got %v", err)
	}
}

func TestIssueCmdStructure(t *testing.T) {
	have := map[string]bool{}
	for _, sub := range issueCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"list", "get", "create", "update", "delete", "comment", "labels", "start", "stop", "review"} {
		if !have[want] {
			t.Errorf("issue missing subcommand %q", want)
		}
	}
}

// ─── creator attribution ─────────────────────────────────────────────────

func TestCreatorLabel(t *testing.T) {
	cases := []struct {
		name string
		in   *issueCreator
		want string
	}{
		{"nil (legacy issue)", nil, "-"},
		{"agent with name", &issueCreator{Type: "agent", ID: "a1", Name: "Scout"}, "Scout (agent)"},
		{"user with name", &issueCreator{Type: "user", ID: "u1", Name: "Demo User"}, "Demo User"},
		{"agent without name falls back to id", &issueCreator{Type: "agent", ID: "a1"}, "a1 (agent)"},
		{"user without name falls back to id", &issueCreator{Type: "user", ID: "u1"}, "u1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := creatorLabel(tc.in); got != tc.want {
				t.Errorf("creatorLabel(%+v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestIssueListRunE_CreatorColumn asserts the CREATOR column renders the
// resolved creator (with the agent marker) and "-" for legacy rows.
func TestIssueListRunE_CreatorColumn(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	payload := covIssueListPayload()
	payload[0]["created_by"] = map[string]string{"type": "agent", "id": "agent-1", "name": "Scout"}
	stub.OnGet("/api/v1/issues", clitest.JSONResponse(200, payload))

	out, err := covCaptureStdoutCli6(t, func() error {
		return issueListCmd.RunE(issueListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "CREATOR") {
		t.Errorf("list header missing CREATOR column: %q", out)
	}
	if !strings.Contains(out, "Scout (agent)") {
		t.Errorf("agent-created issue must show creator with agent marker: %q", out)
	}
}

// TestIssueGetRunE_CreatedByShown asserts the detail view renders the
// "Created By" row for an agent-created issue.
func TestIssueGetRunE_CreatedByShown(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	payload := covIssueDetailPayload()
	payload["created_by"] = map[string]string{"type": "agent", "id": "agent-1", "name": "Scout"}
	stub.OnGet("/api/v1/issues/ENG-1", clitest.JSONResponse(200, payload))
	stub.OnGet(fmt.Sprintf("/api/v1/crews/%s/issues/ENG-1/comments", covCrewIDCli6),
		clitest.JSONResponse(200, []map[string]any{}))

	out, err := covCaptureStdoutCli6(t, func() error {
		return issueGetCmd.RunE(issueGetCmd, []string{"ENG-1"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Created By") || !strings.Contains(out, "Scout (agent)") {
		t.Errorf("detail output missing creator attribution: %q", out)
	}
}
