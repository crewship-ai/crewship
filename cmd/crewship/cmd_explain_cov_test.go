package main

// Coverage tests for cmd_explain.go — runWindowStart and
// fetchJournalForExplain helpers plus the RunE orchestration up to the
// WS-token step (the streaming tail lives in cmd_run.go and needs a
// live WebSocket, so tests stop at the ws-token error boundary).

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func covExplainRunsResponse() map[string]any {
	return map[string]any{
		"data": []map[string]any{
			{"id": "r_other", "agent_id": "agent-9", "created_at": "2026-06-10T09:00:00Z"},
			{"id": "r_target", "agent_id": "agent-1", "agent_slug": "viktor",
				"chat_id": "chat-1", "created_at": "2026-06-10T10:00:00Z"},
		},
	}
}

func TestRunWindowStart_Found(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/runs", clitest.JSONResponse(200, covExplainRunsResponse()))
	client := newAPIClient()

	got, err := runWindowStart(client, "r_target")
	if err != nil {
		t.Fatalf("runWindowStart: %v", err)
	}
	want := time.Date(2026, 6, 10, 9, 55, 0, 0, time.UTC) // created_at − 5 min
	if !got.Equal(want) {
		t.Errorf("window start: got %v want %v", got, want)
	}
}

func TestRunWindowStart_NotFound(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{"data": []any{}}))
	client := newAPIClient()

	_, err := runWindowStart(client, "r_ghost")
	if err == nil || !strings.Contains(err.Error(), "not in recent window") {
		t.Errorf("expected not-in-window; got %v", err)
	}
}

func TestRunWindowStart_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/runs", clitest.ErrorResponse(500, "Internal server error"))
	client := newAPIClient()

	_, err := runWindowStart(client, "r_x")
	if err == nil || !strings.Contains(err.Error(), "Internal server error") {
		t.Errorf("expected API error; got %v", err)
	}
}

func TestFetchJournalForExplain(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/journal", clitest.JSONResponse(200, map[string]any{
		"entries": []map[string]any{
			// Server returns newest first; output must read oldest-first.
			{"ts": "2026-06-10T10:00:02.000Z", "entry_type": "run.completed", "severity": "info", "summary": "all done"},
			{"ts": "2026-06-10T10:00:01.000Z", "entry_type": "tool.exec", "severity": "warn", "summary": "ran a tool"},
		},
	}))
	client := newAPIClient()
	from := time.Date(2026, 6, 10, 9, 55, 0, 0, time.UTC)

	text, err := fetchJournalForExplain(client, "agent-1", from, "tool.exec,run.completed")
	if err != nil {
		t.Fatalf("fetchJournalForExplain: %v", err)
	}
	if !strings.Contains(text, "[warn/tool.exec]  ran a tool") ||
		!strings.Contains(text, "[info/run.completed]  all done") {
		t.Errorf("rendered text wrong:\n%s", text)
	}
	if strings.Index(text, "ran a tool") > strings.Index(text, "all done") {
		t.Errorf("entries not oldest-first:\n%s", text)
	}

	calls := stub.CallsFor("GET", "/api/v1/journal")
	if len(calls) != 1 {
		t.Fatalf("expected 1 journal GET, got %d", len(calls))
	}
	q := calls[0].Query
	for _, want := range []string{"agent_id=agent-1", "since=2026-06-10T09%3A55%3A00Z", "limit=200", "entry_type=tool.exec%2Crun.completed"} {
		if !strings.Contains(q, want) {
			t.Errorf("journal query missing %q: %q", want, q)
		}
	}
}

func TestFetchJournalForExplain_Empty(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/journal", clitest.JSONResponse(200, map[string]any{"entries": []any{}}))
	client := newAPIClient()

	text, err := fetchJournalForExplain(client, "agent-1", time.Now(), "")
	if err != nil || text != "" {
		t.Errorf("empty journal: got (%q, %v); want empty, nil", text, err)
	}
}

func TestExplainRunE_NoAuth(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := explainCmd.RunE(explainCmd, []string{"r_x"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}
}

func TestExplainRunE_RunNotFound(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{"data": []any{}}))

	err := explainCmd.RunE(explainCmd, []string{"r_ghost"})
	if err == nil || !strings.Contains(err.Error(), "not found in last 100 runs") {
		t.Errorf("expected not-found; got %v", err)
	}
}

func TestExplainRunE_RunWithoutAgent(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{
		"data": []map[string]any{{"id": "r_target", "agent_id": ""}},
	}))

	err := explainCmd.RunE(explainCmd, []string{"r_target"})
	if err == nil || !strings.Contains(err.Error(), "has no agent_id") {
		t.Errorf("expected no-agent error; got %v", err)
	}
}

func TestExplainRunE_NoJournalEntries(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/runs", clitest.JSONResponse(200, covExplainRunsResponse()))
	stub.OnGet("/api/v1/journal", clitest.JSONResponse(200, map[string]any{"entries": []any{}}))

	err := explainCmd.RunE(explainCmd, []string{"r_target"})
	if err == nil || !strings.Contains(err.Error(), "no journal entries found") {
		t.Errorf("expected no-entries error; got %v", err)
	}
}

func TestExplainRunE_NoSummarizerAgent(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/runs", clitest.JSONResponse(200, covExplainRunsResponse()))
	stub.OnGet("/api/v1/journal", clitest.JSONResponse(200, map[string]any{
		"entries": []map[string]any{
			{"ts": "2026-06-10T10:00:01Z", "entry_type": "x", "severity": "info", "summary": "s"},
		},
	}))
	// covSetupCli8 blanks CREWSHIP_DEFAULT_AGENT; cliCfg has no default agent.

	err := explainCmd.RunE(explainCmd, []string{"r_target"})
	if err == nil || !strings.Contains(err.Error(), "no agent set to summarize") {
		t.Errorf("expected no-summarizer error; got %v", err)
	}
}

// covExplainRunsStateful serves the first /api/v1/runs call (fetchRun)
// with valid data, then degrades subsequent calls (runWindowStart) per
// `mode` — exercising the window-start fallback branches.
func covExplainRunsStateful(t *testing.T, stub *clitest.StubServer, mode string) {
	t.Helper()
	good, err := json.Marshal(covExplainRunsResponse())
	if err != nil {
		t.Fatal(err)
	}
	var call atomic.Int64
	stub.OnGet("/api/v1/runs", func(*http.Request, []byte) (int, []byte, string) {
		if call.Add(1) == 1 {
			return 200, good, "application/json"
		}
		switch mode {
		case "abort":
			panic(http.ErrAbortHandler)
		default:
			return 200, []byte("not json"), "application/json"
		}
	})
}

func TestExplainRunE_WindowStartFallback(t *testing.T) {
	for _, mode := range []string{"decode", "abort"} {
		t.Run(mode, func(t *testing.T) {
			stub := clitest.NewStubServer()
			defer stub.Close()
			covSetupCli8(t, stub.URL())
			covExplainRunsStateful(t, stub, mode)
			// Journal aborts → the command must surface "fetch journal"
			// AFTER having taken the wide-window fallback.
			stub.OnGet("/api/v1/journal", covAbort())

			err := explainCmd.RunE(explainCmd, []string{"r_target"})
			if err == nil || !strings.Contains(err.Error(), "fetch journal") {
				t.Errorf("expected fetch-journal error; got %v", err)
			}
		})
	}
}

func TestExplainRunE_SummarizerNotFound(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/runs", clitest.JSONResponse(200, covExplainRunsResponse()))
	stub.OnGet("/api/v1/journal", clitest.JSONResponse(200, map[string]any{
		"entries": []map[string]any{
			{"ts": "2026-06-10T10:00:01Z", "entry_type": "x", "severity": "info", "summary": "s"},
		},
	}))
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]any{
		{"id": covAgentIDCli8, "slug": "eva"},
	}))
	covSetFlagCli8(t, explainCmd, "agent", "viktor")

	err := explainCmd.RunE(explainCmd, []string{"r_target"})
	if err == nil || !strings.Contains(err.Error(), "agent not found") {
		t.Errorf("expected agent-not-found; got %v", err)
	}
}

func TestExplainRunE_ChatCreateFailures(t *testing.T) {
	base := func(t *testing.T, stub *clitest.StubServer) {
		t.Helper()
		stub.OnGet("/api/v1/runs", clitest.JSONResponse(200, covExplainRunsResponse()))
		stub.OnGet("/api/v1/journal", clitest.JSONResponse(200, map[string]any{
			"entries": []map[string]any{
				{"ts": "2026-06-10T10:00:01Z", "entry_type": "x", "severity": "info", "summary": "s"},
			},
		}))
		stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]any{
			{"id": covAgentIDCli8, "slug": "viktor"},
		}))
		covSetFlagCli8(t, explainCmd, "agent", "viktor")
	}
	chats := "/api/v1/agents/" + covAgentIDCli8 + "/chats"

	cases := []struct {
		name    string
		handler clitest.Handler
		wantSub string
	}{
		{"transport", covAbort(), "create chat"},
		{"api error", clitest.ErrorResponse(500, "chat backend down"), "chat backend down"},
		{"decode", covNotJSON(), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := clitest.NewStubServer()
			defer stub.Close()
			covSetupCli8(t, stub.URL())
			base(t, stub)
			stub.OnPost(chats, c.handler)

			err := explainCmd.RunE(explainCmd, []string{"r_target"})
			if err == nil {
				t.Fatalf("%s: expected error", c.name)
			}
			if c.wantSub != "" && !strings.Contains(err.Error(), c.wantSub) {
				t.Errorf("%s: error %v missing %q", c.name, err, c.wantSub)
			}
		})
	}
}

// Full path through ws-token + save file + the meta line; the run ends
// inside runStream when the WebSocket dial hits the stub's 404 /ws.
func TestExplainRunE_ReachesStream(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/runs", clitest.JSONResponse(200, covExplainRunsResponse()))
	stub.OnGet("/api/v1/journal", clitest.JSONResponse(200, map[string]any{
		"entries": []map[string]any{
			{"ts": "2026-06-10T10:00:01Z", "entry_type": "x", "severity": "info", "summary": "s"},
		},
	}))
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]any{
		{"id": covAgentIDCli8, "slug": "viktor"},
	}))
	stub.OnPost("/api/v1/agents/"+covAgentIDCli8+"/chats", clitest.JSONResponse(200, map[string]string{"id": "chat-1"}))
	stub.OnGet("/api/v1/ws-token", clitest.JSONResponse(200, map[string]string{"token": "ws-jwt"}))

	covSetFlagCli8(t, explainCmd, "agent", "viktor")
	covSetFlagCli8(t, explainCmd, "save", filepath.Join(t.TempDir(), "out.md"))

	err := explainCmd.RunE(explainCmd, []string{"r_target"})
	// The stub has no /ws endpoint → the stream layer must fail with a
	// websocket dial error, proving the command got past token+save setup.
	if err == nil || !strings.Contains(err.Error(), "websocket") {
		t.Errorf("expected websocket dial error; got %v", err)
	}
}

func TestExplainRunE_BadSavePath(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/runs", clitest.JSONResponse(200, covExplainRunsResponse()))
	stub.OnGet("/api/v1/journal", clitest.JSONResponse(200, map[string]any{
		"entries": []map[string]any{
			{"ts": "2026-06-10T10:00:01Z", "entry_type": "x", "severity": "info", "summary": "s"},
		},
	}))
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]any{
		{"id": covAgentIDCli8, "slug": "viktor"},
	}))
	stub.OnPost("/api/v1/agents/"+covAgentIDCli8+"/chats", clitest.JSONResponse(200, map[string]string{"id": "chat-1"}))
	stub.OnGet("/api/v1/ws-token", clitest.JSONResponse(200, map[string]string{"token": "ws-jwt"}))

	covSetFlagCli8(t, explainCmd, "agent", "viktor")
	covSetFlagCli8(t, explainCmd, "save", "bad\x00path")

	err := explainCmd.RunE(explainCmd, []string{"r_target"})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "path") {
		t.Errorf("expected unsafe-path error; got %v", err)
	}
}

// Deep path: everything resolves, chat is created, the test stops the
// flow at the ws-token fetch (500). Asserts the chat-create request hit
// the resolved summarizer agent.
func TestExplainRunE_StopsAtWSToken(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/runs", clitest.JSONResponse(200, covExplainRunsResponse()))
	stub.OnGet("/api/v1/journal", clitest.JSONResponse(200, map[string]any{
		"entries": []map[string]any{
			{"ts": "2026-06-10T10:00:01Z", "entry_type": "run.failed", "severity": "error", "summary": "boom"},
		},
	}))
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]any{
		{"id": covAgentIDCli8, "slug": "viktor"},
	}))
	stub.OnPost("/api/v1/agents/"+covAgentIDCli8+"/chats", clitest.JSONResponse(200, map[string]string{"id": "chat-77"}))
	stub.OnGet("/api/v1/ws-token", clitest.ErrorResponse(500, "ws token unavailable"))

	covSetFlagCli8(t, explainCmd, "agent", "viktor")
	covSetFlagCli8(t, explainCmd, "types", "run.failed")

	err := explainCmd.RunE(explainCmd, []string{"r_target"})
	if err == nil || !strings.Contains(err.Error(), "get WS token") {
		t.Errorf("expected ws-token error; got %v", err)
	}

	chats := stub.CallsFor("POST", "/api/v1/agents/"+covAgentIDCli8+"/chats")
	if len(chats) != 1 {
		t.Fatalf("expected 1 chat create, got %d", len(chats))
	}
	var body map[string]string
	clitest.MustDecodeJSONBody(chats[0].Body, &body)
	if body["mode"] != "CHAT" || body["origin"] != "CLI" {
		t.Errorf("chat-create body wrong: %v", body)
	}
	// The journal lookup carried the --types filter.
	journals := stub.CallsFor("GET", "/api/v1/journal")
	if len(journals) != 1 || !strings.Contains(journals[0].Query, "entry_type=run.failed") {
		t.Errorf("types filter not propagated: %+v", journals)
	}
}
