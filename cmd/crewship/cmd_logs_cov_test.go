package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/net/websocket"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func covLogsAgents() []map[string]any {
	crew := covCrew
	return []map[string]any{
		{"id": "cagentaaaaaaaaaaaaaaaa", "slug": "viktor", "crew_id": crew},
		{"id": "cagentbbbbbbbbbbbbbbbb", "slug": "nocrew", "crew_id": nil},
	}
}

func TestLogsRunE_PrintsEntries(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, covLogsAgents()))
	s.OnGet("/api/v1/agents/cagentaaaaaaaaaaaaaaaa/logs", clitest.JSONResponse(200, []map[string]string{
		{"ts": "2026-06-10T10:00:00Z", "level": "info", "agent": "viktor", "event": "output", "content": "hello world"},
		{"ts": "2026-06-10T10:00:01Z", "level": "error", "agent": "viktor", "event": "error", "content": "exploded"},
		{"ts": "not-a-ts", "level": "info", "agent": "viktor", "event": "sys", "content": "booted\x1b[31mansi"},
	}))
	covSetFlagCli9(t, logsCmd, "lines", "25")

	out := covCaptureStdoutCli9(t, func() {
		if err := logsCmd.RunE(logsCmd, []string{"viktor"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"2026-06-10 10:00:00", "[output]", "hello world", "[error]", "exploded", "not-a-ts", "booted"} {
		if !strings.Contains(out, want) {
			t.Errorf("logs output missing %q:\n%s", want, out)
		}
	}
	// Raw ESC byte from agent stdout must not reach the terminal verbatim.
	if strings.Contains(out, "booted\x1b[31m") {
		t.Errorf("ANSI escape from log content not sanitised:\n%s", out)
	}

	calls := s.CallsFor("GET", "/api/v1/agents/cagentaaaaaaaaaaaaaaaa/logs")
	if len(calls) != 1 {
		t.Fatalf("expected one logs GET, got %d", len(calls))
	}
	q := calls[0].Query
	if !strings.Contains(q, "crew_id="+covCrew) || !strings.Contains(q, "limit=25") {
		t.Errorf("logs query missing crew/limit: %q", q)
	}
}

func TestLogsRunE_ResolvesByAgentID(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, covLogsAgents()))
	s.OnGet("/api/v1/agents/cagentaaaaaaaaaaaaaaaa/logs", clitest.JSONResponse(200, []map[string]string{}))

	_ = covCaptureStdoutCli9(t, func() {
		if err := logsCmd.RunE(logsCmd, []string{"cagentaaaaaaaaaaaaaaaa"}); err != nil {
			t.Errorf("RunE by id: %v", err)
		}
	})
	if got := len(s.CallsFor("GET", "/api/v1/agents/cagentaaaaaaaaaaaaaaaa/logs")); got != 1 {
		t.Errorf("logs endpoint calls = %d, want 1", got)
	}
}

func TestLogsRunE_AgentNotFound(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, covLogsAgents()))

	err := logsCmd.RunE(logsCmd, []string{"ghost"})
	if err == nil || !strings.Contains(err.Error(), "agent not found: ghost") {
		t.Errorf("expected agent-not-found; got %v", err)
	}
}

func TestLogsRunE_AgentWithoutCrew(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, covLogsAgents()))

	err := logsCmd.RunE(logsCmd, []string{"nocrew"})
	if err == nil || !strings.Contains(err.Error(), "agent has no crew") {
		t.Errorf("expected no-crew error; got %v", err)
	}
}

func TestLogsRunE_FollowFailsWithoutWSToken(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, covLogsAgents()))
	s.OnGet("/api/v1/agents/cagentaaaaaaaaaaaaaaaa/logs", clitest.JSONResponse(200, []map[string]string{}))
	s.OnGet("/api/v1/ws-token", clitest.ErrorResponse(500, "no ws for you"))
	covSetFlagCli9(t, logsCmd, "follow", "true")

	var err error
	_ = covCaptureStdoutCli9(t, func() {
		err = logsCmd.RunE(logsCmd, []string{"viktor"})
	})
	if err == nil || !strings.Contains(err.Error(), "get WS token for follow") {
		t.Errorf("expected WS-token error from logsFollow; got %v", err)
	}
}

// TestLogsRunE_FollowStreamsEvents runs the full --follow path against a
// real (httptest) server that upgrades /ws and pushes one chat_event plus
// one non-event message before closing. The loop must print the event and
// exit cleanly when the connection drops.
func TestLogsRunE_FollowStreamsEvents(t *testing.T) {
	covSaveState(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agents", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(covLogsAgents())
	})
	mux.HandleFunc("/api/v1/agents/cagentaaaaaaaaaaaaaaaa/logs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})
	mux.HandleFunc("/api/v1/ws-token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"ws-tok"}`))
	})
	var gotSubscribe string
	mux.Handle("/ws", websocket.Handler(func(conn *websocket.Conn) {
		var raw []byte
		if err := websocket.Message.Receive(conn, &raw); err == nil {
			gotSubscribe = string(raw)
		}
		_, _ = conn.Write([]byte(`{"type":"chat_event","payload":{"type":"output","content":"streamed hello"}}`))
		_, _ = conn.Write([]byte(`{"type":"presence"}`)) // non-event → skipped
		_ = conn.Close()
	}))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: covWSCli9, Server: srv.URL}
	covSetFlagCli9(t, logsCmd, "follow", "true")

	var err error
	out := covCaptureStdoutCli9(t, func() {
		_ = covCaptureStderrCli9(t, func() {
			err = logsCmd.RunE(logsCmd, []string{"viktor"})
		})
	})
	if err != nil {
		t.Fatalf("follow should end cleanly when the socket closes: %v", err)
	}
	if !strings.Contains(out, "streamed hello") || !strings.Contains(out, "[output]") {
		t.Errorf("streamed event not printed:\n%s", out)
	}
	if !strings.Contains(gotSubscribe, `"channel":"agent:cagentaaaaaaaaaaaaaaaa"`) {
		t.Errorf("subscribe frame wrong: %q", gotSubscribe)
	}
}

func TestLogsRunE_TransportError(t *testing.T) {
	covSaveState(t)
	s := clitest.NewStubServer()
	deadURL := s.URL()
	s.Close() // nothing listens here any more → connection refused
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: covWSCli9, Server: deadURL}

	if err := logsCmd.RunE(logsCmd, []string{"viktor"}); err == nil {
		t.Error("expected transport error when server is down")
	}
}

func TestLogsRunE_NoWorkspace(t *testing.T) {
	covSaveState(t)
	cliCfg = &cli.CLIConfig{Token: "tok"}
	err := logsCmd.RunE(logsCmd, []string{"viktor"})
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error; got %v", err)
	}
}

func TestLogsRunE_DecodeErrors(t *testing.T) {
	t.Run("agents decode", func(t *testing.T) {
		s := covStubCli9(t)
		s.OnGet("/api/v1/agents", clitest.TextResponse(200, "{nope"))
		if err := logsCmd.RunE(logsCmd, []string{"viktor"}); err == nil {
			t.Error("expected agents decode error")
		}
	})
	t.Run("logs decode", func(t *testing.T) {
		s := covStubCli9(t)
		s.OnGet("/api/v1/agents", clitest.JSONResponse(200, covLogsAgents()))
		s.OnGet("/api/v1/agents/cagentaaaaaaaaaaaaaaaa/logs", clitest.TextResponse(200, "{nope"))
		if err := logsCmd.RunE(logsCmd, []string{"viktor"}); err == nil {
			t.Error("expected logs decode error")
		}
	})
}

func TestLogsRunE_AuthAndServerErrors(t *testing.T) {
	t.Run("no auth", func(t *testing.T) {
		covSaveState(t)
		cliCfg = &cli.CLIConfig{}
		if err := logsCmd.RunE(logsCmd, []string{"viktor"}); err == nil {
			t.Error("expected not-logged-in error")
		}
	})
	t.Run("agents listing fails", func(t *testing.T) {
		s := covStubCli9(t)
		s.OnGet("/api/v1/agents", clitest.ErrorResponse(500, "agents down"))
		err := logsCmd.RunE(logsCmd, []string{"viktor"})
		if err == nil || !strings.Contains(err.Error(), "agents down") {
			t.Errorf("expected listing error; got %v", err)
		}
	})
	t.Run("logs endpoint fails", func(t *testing.T) {
		s := covStubCli9(t)
		s.OnGet("/api/v1/agents", clitest.JSONResponse(200, covLogsAgents()))
		s.OnGet("/api/v1/agents/cagentaaaaaaaaaaaaaaaa/logs", clitest.ErrorResponse(502, "proxy sad"))
		err := logsCmd.RunE(logsCmd, []string{"viktor"})
		if err == nil || !strings.Contains(err.Error(), "proxy sad") {
			t.Errorf("expected proxy error; got %v", err)
		}
	})
}
