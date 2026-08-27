package main

// Coverage tests for cmd_resume.go — the non-interactive resolution
// paths of RunE plus pickRecentChat / findChatForPR / deref. The final
// dispatch into runCmd (which opens a WebSocket stream) is intentionally
// not exercised; every test here stops at a resolution error.

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestDeref(t *testing.T) {
	if got := deref(nil); got != "" {
		t.Errorf("deref(nil) = %q", got)
	}
	s := "x"
	if got := deref(&s); got != "x" {
		t.Errorf("deref(&x) = %q", got)
	}
}

func TestResumeRunE_NoArgsNonTTY(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	resumeCmd.SetContext(context.Background())

	// Force a non-TTY stdin so the interactive picker refuses.
	covWithStdinCli8(t, "", func() {
		err := resumeCmd.RunE(resumeCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "requires a TTY") {
			t.Errorf("expected TTY error; got %v", err)
		}
	})
}

func TestResumeRunE_RunIDWithoutChat(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/runs/r_orphan", clitest.JSONResponse(200, map[string]any{
		"id": "r_orphan", "status": "COMPLETED",
	}))
	resumeCmd.SetContext(context.Background())

	err := resumeCmd.RunE(resumeCmd, []string{"r_orphan"})
	if err == nil || !strings.Contains(err.Error(), "no associated chat") {
		t.Errorf("expected no-chat error; got %v", err)
	}
}

func TestResumeRunE_RunIDLookupError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	// r_missing, not run_missing: resume's r_/run_ branch both call
	// client.GetRun, but #1193's IsPipelineRunID only intercepts the
	// run_ prefix (that's the shape `routine runs` mints) — r_ still
	// reaches the real HTTP call, so this exercises the genuine
	// not-found path rather than the pipeline-run-id-shape rejection.
	stub.OnGet("/api/v1/runs/r_missing", clitest.ErrorResponse(404, "run not found"))
	resumeCmd.SetContext(context.Background())

	err := resumeCmd.RunE(resumeCmd, []string{"r_missing"})
	if err == nil || !strings.Contains(err.Error(), "run not found") {
		t.Errorf("expected run-not-found; got %v", err)
	}
}

func TestResumeRunE_PipelineRunIDRejectedWithHint(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	resumeCmd.SetContext(context.Background())

	// #1193: a run_-prefixed id is a pipeline run (routine runs), not a
	// chat-turn run — resume should reject it before any HTTP call, with
	// a hint pointing at the right command, not a bare "not found".
	err := resumeCmd.RunE(resumeCmd, []string{"run_abc123"})
	if err == nil || !strings.Contains(err.Error(), "routine logs") {
		t.Errorf("expected pipeline-run-id hint mentioning `routine logs`; got %v", err)
	}
}

func TestResumeRunE_ChatIDAgentUnresolvable(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	// The agents walk 404s → no owning agent → resolution error.
	resumeCmd.SetContext(context.Background())

	err := resumeCmd.RunE(resumeCmd, []string{"chat-unknown"})
	if err == nil || !strings.Contains(err.Error(), "could not determine agent for chat chat-unknown") {
		t.Errorf("expected agent-resolution error; got %v", err)
	}
}

func TestResumeRunE_PRURLNoSession(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/journal", clitest.JSONResponse(200, map[string]any{"entries": []any{}}))
	resumeCmd.SetContext(context.Background())

	err := resumeCmd.RunE(resumeCmd, []string{"https://github.com/foo/bar/pull/42"})
	if err == nil || !strings.Contains(err.Error(), "no session found for PR foo/bar#42") {
		t.Errorf("expected no-session error; got %v", err)
	}

	calls := stub.CallsFor("GET", "/api/v1/journal")
	if len(calls) != 1 {
		t.Fatalf("expected 1 journal search, got %d", len(calls))
	}
	if !strings.Contains(calls[0].Query, "query=foo%2Fbar%2342") {
		t.Errorf("journal query missing PR needle: %q", calls[0].Query)
	}
}

func TestResumeRunE_NoAuth(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := resumeCmd.RunE(resumeCmd, []string{"chat-1"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}
}

func TestPickRecentChat_NonTTY(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	client := newAPIClient()
	covWithStdinCli8(t, "", func() {
		_, _, err := pickRecentChat(client)
		if err == nil || !strings.Contains(err.Error(), "requires a TTY") {
			t.Errorf("expected TTY error; got %v", err)
		}
	})
}

// The picker reads GET /api/v1/runs, which is every run in the workspace —
// there is no origin=CLI filter any more (#2086 removed the chats-list path
// that had one). `--help` promising "CLI sessions" therefore describes a
// filter that does not exist, and a workspace driven from the web UI offers
// UI-originated sessions under that promise. docs/cli/resume.mdx was corrected;
// the command's own help is what a user actually reads.
func TestResumeHelpDescribesTheListItReallyOffers(t *testing.T) {
	help := resumeCmd.Long
	for _, claim := range []string{"CLI sessions", "CLI-origin"} {
		if strings.Contains(help, claim) {
			t.Errorf("resume --help still promises %q, but the picker lists every "+
				"session in the workspace regardless of origin:\n%s", claim, help)
		}
	}
	if !strings.Contains(help, strconv.Itoa(resumeSessionCount)) {
		t.Errorf("resume --help does not say how many sessions the picker offers (%d):\n%s",
			resumeSessionCount, help)
	}
}

// recentSessions over-fetches (want*5) because one chat can own several runs
// and the dedupe below keeps the newest run per chat. GET /api/v1/runs clamps
// a limit outside 1..100 back to 50 rather than erroring, so an over-fetch
// that asks for more than 100 gets a SMALLER page than one that asks for 100 —
// the over-fetch silently inverts. At resumeSessionCount = 10 the product is
// 50 and nothing shows; the trap springs the day somebody raises the constant.
func TestRecentSessions_LimitStaysWithinServerClamp(t *testing.T) {
	tests := []struct {
		name      string
		want      int
		wantLimit string
	}{
		{"current picker size", resumeSessionCount, "50"},
		{"just under the ceiling", 20, "100"},
		{"past the ceiling clamps instead of overflowing", 40, "100"},
		{"far past the ceiling", 500, "100"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := clitest.NewStubServer()
			defer stub.Close()
			covSetupCli8(t, stub.URL())
			stub.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{"data": []any{}}))

			if _, err := recentSessions(newAPIClient(), tc.want); err != nil {
				t.Fatalf("recentSessions: %v", err)
			}
			calls := stub.CallsFor("GET", "/api/v1/runs")
			if len(calls) != 1 {
				t.Fatalf("expected 1 runs call, got %d", len(calls))
			}
			q, err := url.ParseQuery(calls[0].Query)
			if err != nil {
				t.Fatalf("parse runs query %q: %v", calls[0].Query, err)
			}
			if got := q.Get("limit"); got != tc.wantLimit {
				t.Errorf("runs limit = %q, want %q — a limit above 100 is not an error, "+
					"the server quietly serves 50 instead", got, tc.wantLimit)
			}
		})
	}
}

func TestFindChatForPR_Found(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/journal", clitest.JSONResponse(200, map[string]any{
		"entries": []map[string]any{
			{"trace_id": "t1", "chat_id": "", "agent_id": "a1"},
			{"trace_id": "t2", "chat_id": "chat-42", "agent_id": "a2"},
		},
	}))
	client := newAPIClient()

	chatID, slug, err := findChatForPR(client, "foo", "bar", 42)
	if err != nil {
		t.Fatalf("findChatForPR: %v", err)
	}
	if chatID != "chat-42" || slug != "" {
		t.Errorf("got (%q,%q)", chatID, slug)
	}
}

// covResetRunCmdFlags restores the runCmd flags the resume dispatch
// mutates (--chat / --interactive) so later tests see defaults.
func covResetRunCmdFlags(t *testing.T) {
	t.Helper()
	guardCLIState(t)
	t.Cleanup(func() {
		for _, name := range []string{"chat", "interactive"} {
			if fl := runCmd.Flags().Lookup(name); fl != nil {
				_ = fl.Value.Set(fl.DefValue)
				fl.Changed = false
			}
		}
	})
}

// TestResumeRunE_RunIDDispatches resolves a run to its chat and dispatches
// into runCmd; the stub serves agent resolution but fails the ws-token
// step, so the test proves the dispatch happened (chat threaded through)
// without opening a real stream.
func TestResumeRunE_RunIDDispatches(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	covResetRunCmdFlags(t)
	stub.OnGet("/api/v1/runs/r_ok", clitest.JSONResponse(200, map[string]any{
		"id": "r_ok", "status": "COMPLETED", "chat_id": "chat-55", "agent_slug": "viktor",
	}))
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]any{
		{"id": covAgentIDCli8, "slug": "viktor"},
	}))
	stub.OnGet("/api/v1/ws-token", clitest.ErrorResponse(500, "no ws for you"))
	resumeCmd.SetContext(context.Background())

	var err error
	covWithStdinCli8(t, "", func() {
		err = resumeCmd.RunE(resumeCmd, []string{"r_ok"})
	})
	if err == nil || !strings.Contains(err.Error(), "get WS token") {
		t.Errorf("expected dispatch to fail at ws-token; got %v", err)
	}
	if got, _ := runCmd.Flags().GetString("chat"); got != "chat-55" {
		t.Errorf("chat not threaded into run command: %q", got)
	}
	if got, _ := runCmd.Flags().GetBool("interactive"); !got {
		t.Error("interactive flag not set by resume dispatch")
	}
}

// TestResumeRunE_ChatIDLooksUpAgent covers the owning-agent lookup before
// dispatch: the agents walk, not a flat /chats/{id} fetch. #2086 — there is
// no GET /api/v1/chats/{chatId} route, so the old lookup 404'd every time
// and `resume <chat-id>` could never resolve an agent.
func TestResumeRunE_ChatIDLooksUpAgent(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	covResetRunCmdFlags(t)
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]any{
		{"id": covAgentIDCli8, "slug": "viktor"},
	}))
	stub.OnGet("/api/v1/agents/"+covAgentIDCli8+"/chats", clitest.JSONResponse(200, []map[string]any{
		{"id": "chat-77"},
	}))
	stub.OnGet("/api/v1/ws-token", clitest.ErrorResponse(500, "no ws for you"))
	resumeCmd.SetContext(context.Background())

	var err error
	covWithStdinCli8(t, "", func() {
		err = resumeCmd.RunE(resumeCmd, []string{"chat-77"})
	})
	if err == nil || !strings.Contains(err.Error(), "get WS token") {
		t.Errorf("expected dispatch to fail at ws-token; got %v", err)
	}
	if got, _ := runCmd.Flags().GetString("chat"); got != "chat-77" {
		t.Errorf("chat not threaded into run command: %q", got)
	}
	for _, c := range stub.Calls() {
		if c.Path == "/api/v1/chats/chat-77" || c.Path == "/api/v1/chats" {
			t.Errorf("resume called %s %s — that route does not exist (#2086)", c.Method, c.Path)
		}
	}
}

// TestRecentSessions_DedupesChatsAndCapsAtWant proves the picker's source of
// truth: the workspace-scoped runs list (the only registered route that lists
// sessions across agents), one entry per chat, newest first.
func TestRecentSessions_DedupesChatsAndCapsAtWant(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	slug := "viktor"
	chatA, chatB := "chat-a", "chat-b"
	stub.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{
		"data": []map[string]any{
			{"id": "r1", "chat_id": &chatA, "agent_slug": &slug, "created_at": "2026-08-26T10:00:00Z"},
			{"id": "r2", "chat_id": &chatA, "agent_slug": &slug, "created_at": "2026-08-26T09:00:00Z"},
			{"id": "r3", "chat_id": nil, "agent_slug": &slug, "created_at": "2026-08-26T08:00:00Z"},
			{"id": "r4", "chat_id": &chatB, "agent_slug": &slug, "created_at": "2026-08-26T07:00:00Z"},
		},
	}))
	client := newAPIClient()

	got, err := recentSessions(client, 2)
	if err != nil {
		t.Fatalf("recentSessions: %v", err)
	}
	want := []recentSession{
		{ChatID: chatA, AgentSlug: slug, Title: "run r1", UpdatedAt: "2026-08-26T10:00:00Z"},
		{ChatID: chatB, AgentSlug: slug, Title: "run r4", UpdatedAt: "2026-08-26T07:00:00Z"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d sessions, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("session %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	calls := stub.CallsFor("GET", "/api/v1/runs")
	if len(calls) != 1 {
		t.Fatalf("expected 1 runs list call, got %d", len(calls))
	}
	// Over-fetch: several runs can share one chat, so `want` sessions needs
	// more than `want` runs.
	if !strings.Contains(calls[0].Query, "limit=10") {
		t.Errorf("expected limit=10 for want=2, got query %q", calls[0].Query)
	}
}

func TestRecentSessions_ListError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/runs", clitest.ErrorResponse(500, "Internal server error"))
	client := newAPIClient()

	if _, err := recentSessions(client, 10); err == nil ||
		!strings.Contains(err.Error(), "list recent sessions") {
		t.Errorf("expected list-recent-sessions error; got %v", err)
	}
}

func TestFindChatForPR_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/journal", clitest.ErrorResponse(500, "Internal server error"))
	client := newAPIClient()

	_, _, err := findChatForPR(client, "foo", "bar", 7)
	if err == nil || !strings.Contains(err.Error(), "journal search") {
		t.Errorf("expected journal-search error; got %v", err)
	}
}
