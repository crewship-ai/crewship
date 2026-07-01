package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/net/websocket"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const covAgentID = "cagentaaaaaaaaaaaaaaaaaa"

// wsCapture records what the fake run server observed from the CLI —
// WS subscriptions, sent prompts, and HTTP chat creations.
type wsCapture struct {
	mu          sync.Mutex
	subscribed  []string
	sentContent []string
	chatCreates int
	chatBody    []byte
}

func (c *wsCapture) Subscribed() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.subscribed...)
}

func (c *wsCapture) SentContent() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.sentContent...)
}

func (c *wsCapture) ChatCreates() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.chatCreates
}

func (c *wsCapture) ChatBody() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.chatBody...)
}

// sendScriptedEvents writes the scripted chat events to the connection.
// The sentinel Type "__pong" emits a raw non-chat_event frame so tests
// can exercise the client's skip-unknown-message branches.
func sendScriptedEvents(conn *websocket.Conn, events []cli.ChatEventPayload) error {
	for _, ev := range events {
		if ev.Type == "__pong" {
			if err := websocket.Message.Send(conn, `{"type":"pong"}`); err != nil {
				return err
			}
			continue
		}
		pb, _ := json.Marshal(ev)
		mb, _ := json.Marshal(cli.WSMessage{Type: "chat_event", Payload: pb})
		if err := websocket.Message.Send(conn, string(mb)); err != nil {
			return err
		}
	}
	return nil
}

// newWSEchoHandler returns a websocket handler that waits for the CLI's
// send_message and then replays the scripted chat events. With loop=false
// it closes after the first script run (simulates server ending the
// session); with loop=true it answers every send_message — needed for
// interactive-mode tests that send several prompts on one connection.
func newWSEchoHandler(cap *wsCapture, events []cli.ChatEventPayload, loop bool) websocket.Handler {
	return websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		for {
			var raw []byte
			if err := websocket.Message.Receive(conn, &raw); err != nil {
				return
			}
			var msg cli.WSMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				return
			}
			switch msg.Type {
			case "subscribe":
				cap.mu.Lock()
				cap.subscribed = append(cap.subscribed, msg.Channel)
				cap.mu.Unlock()
			case "send_message":
				var payload struct {
					SessionID string `json:"session_id"`
					Content   string `json:"content"`
				}
				_ = json.Unmarshal(msg.Payload, &payload)
				cap.mu.Lock()
				cap.sentContent = append(cap.sentContent, payload.Content)
				cap.mu.Unlock()
				if err := sendScriptedEvents(conn, events); err != nil {
					return
				}
				if !loop {
					return
				}
			}
		}
	})
}

// newRunServerCov spins up an httptest server exposing the minimal HTTP
// + WS surface `crewship run` touches and points cliCfg at it. The
// chats handler status lets tests force the chat-creation error path.
func newRunServerCov(t *testing.T, cap *wsCapture, events []cli.ChatEventPayload, chatStatus, wsTokenStatus int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agents", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"` + covAgentID + `","slug":"viktor"}]`))
	})
	mux.HandleFunc("/api/v1/agents/"+covAgentID+"/chats", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		cap.mu.Lock()
		cap.chatCreates++
		cap.chatBody = body
		cap.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if chatStatus != http.StatusOK {
			w.WriteHeader(chatStatus)
			_, _ = w.Write([]byte(`{"error":"chat store down"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"chat1"}`))
	})
	mux.HandleFunc("/api/v1/ws-token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if wsTokenStatus != http.StatusOK {
			w.WriteHeader(wsTokenStatus)
			_, _ = w.Write([]byte(`{"error":"no ws for you"}`))
			return
		}
		_, _ = w.Write([]byte(`{"token":"ws-tok"}`))
	})
	mux.Handle("/ws", newWSEchoHandler(cap, events, false))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	saveCLIState(t)
	t.Setenv("CREWSHIP_SERVER", "")
	t.Setenv("CREWSHIP_WORKSPACE", "")
	flagServer = ""
	flagWorkspace = ""
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: covWorkspaceID, Server: srv.URL}
	return srv
}

func TestTruncateRunHelper(t *testing.T) {
	t.Parallel()
	if got := truncate("short", 10); got != "short" {
		t.Errorf("short: %q", got)
	}
	if got := truncate("line1\nline2", 100); got != "line1 line2" {
		t.Errorf("newline fold: %q", got)
	}
	long := strings.Repeat("é", 20)
	got := truncate(long, 10)
	if !strings.HasSuffix(got, "...") || len([]rune(got)) != 10 {
		t.Errorf("truncate runes: got %q (%d runes)", got, len([]rune(got)))
	}
}

func TestResolveMarkdownFromCmd(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	setFlagCov(t, runCmd, "markdown", "true")
	if md := resolveMarkdownFromCmd(runCmd); md == nil {
		t.Error("--markdown should force a renderer")
	}
	setFlagCov(t, runCmd, "no-markdown", "true")
	if md := resolveMarkdownFromCmd(runCmd); md != nil {
		t.Error("--no-markdown should win and disable rendering")
	}
}

func TestOpenSaveFile(t *testing.T) {
	t.Run("unset flag returns nil", func(t *testing.T) {
		f, err := openSaveFile(runCmd)
		if err != nil || f != nil {
			t.Fatalf("got (%v, %v), want (nil, nil)", f, err)
		}
	})

	t.Run("valid path opens atomic file", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "out.md")
		setFlagCov(t, runCmd, "save", target)
		f, err := openSaveFile(runCmd)
		if err != nil || f == nil {
			t.Fatalf("got (%v, %v), want file", f, err)
		}
		if _, err := f.WriteString("hello"); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := f.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
		_ = f.Close()
		b, err := os.ReadFile(target)
		if err != nil || string(b) != "hello" {
			t.Fatalf("target content: %q err=%v", b, err)
		}
	})

	t.Run("bad directory errors", func(t *testing.T) {
		setFlagCov(t, runCmd, "save", filepath.Join(t.TempDir(), "missing-dir", "out.md"))
		_, err := openSaveFile(runCmd)
		if err == nil || !strings.Contains(err.Error(), "open save file") {
			t.Fatalf("want open save file error, got %v", err)
		}
	})
}

func TestRunNoStream(t *testing.T) {
	t.Run("happy path prints text and commits save", func(t *testing.T) {
		cap := &wsCapture{}
		srv := newRunServerCov(t, cap, []cli.ChatEventPayload{
			{Type: "text", Content: "Hello "},
			{Type: "text", Content: "world"},
			{Type: "done"},
		}, http.StatusOK, http.StatusOK)

		target := filepath.Join(t.TempDir(), "saved.md")
		save, err := cli.NewAtomicFile(target)
		if err != nil {
			t.Fatalf("atomic file: %v", err)
		}
		defer save.Close()

		out, err := captureStdoutCov(t, func() error {
			return runNoStream(srv.URL, "ws-tok", covAgentID, "chat1", "hi", true, nil, save, 0)
		})
		if err != nil {
			t.Fatalf("runNoStream: %v", err)
		}
		if !strings.Contains(out, "Hello world") {
			t.Errorf("stdout: %q", out)
		}
		b, rerr := os.ReadFile(target)
		if rerr != nil || string(b) != "Hello world\n" {
			t.Errorf("saved file: %q err=%v", b, rerr)
		}
		if subs := cap.Subscribed(); len(subs) != 1 || subs[0] != "session:chat1" {
			t.Errorf("subscribed channels: %v", subs)
		}
		if sent := cap.SentContent(); len(sent) != 1 || sent[0] != "hi" {
			t.Errorf("sent content: %v", sent)
		}
	})

	t.Run("error event returns agent error without committing save", func(t *testing.T) {
		srv := newRunServerCov(t, &wsCapture{}, []cli.ChatEventPayload{
			{Type: "text", Content: "partial"},
			{Type: "error", Content: "boom"},
		}, http.StatusOK, http.StatusOK)

		target := filepath.Join(t.TempDir(), "saved.md")
		save, err := cli.NewAtomicFile(target)
		if err != nil {
			t.Fatalf("atomic file: %v", err)
		}
		defer save.Close()

		_, err = captureStdoutCov(t, func() error {
			return runNoStream(srv.URL, "ws-tok", covAgentID, "chat1", "hi", true, nil, save, 0)
		})
		if err == nil || !strings.Contains(err.Error(), "agent error: boom") {
			t.Fatalf("want agent error, got %v", err)
		}
		if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
			t.Errorf("save target must not exist after failed stream; stat err=%v", statErr)
		}
	})

	t.Run("connection closed before output", func(t *testing.T) {
		// No scripted events: the server closes right after send_message.
		srv := newRunServerCov(t, &wsCapture{}, nil, http.StatusOK, http.StatusOK)
		_, err := captureStdoutCov(t, func() error {
			return runNoStream(srv.URL, "ws-tok", covAgentID, "chat1", "hi", true, nil, nil, 0)
		})
		if err == nil || !strings.Contains(err.Error(), "connection closed before any output") {
			t.Fatalf("want connection-closed error, got %v", err)
		}
	})

	t.Run("done without text", func(t *testing.T) {
		srv := newRunServerCov(t, &wsCapture{}, []cli.ChatEventPayload{{Type: "done"}}, http.StatusOK, http.StatusOK)
		_, err := captureStdoutCov(t, func() error {
			return runNoStream(srv.URL, "ws-tok", covAgentID, "chat1", "hi", true, nil, nil, 0)
		})
		if err == nil || !strings.Contains(err.Error(), "agent returned no text") {
			t.Fatalf("want no-text error, got %v", err)
		}
	})

	t.Run("empty error event means no done and no text", func(t *testing.T) {
		srv := newRunServerCov(t, &wsCapture{}, []cli.ChatEventPayload{{Type: "error", Content: ""}}, http.StatusOK, http.StatusOK)
		_, err := captureStdoutCov(t, func() error {
			return runNoStream(srv.URL, "ws-tok", covAgentID, "chat1", "hi", true, nil, nil, 0)
		})
		if err == nil || !strings.Contains(err.Error(), "stream ended without done event") {
			t.Fatalf("want stream-ended error, got %v", err)
		}
	})
}

func TestRunStream_StreamEvents(t *testing.T) {
	t.Run("full event mix with save commit", func(t *testing.T) {
		origVerbose := flagVerbose
		flagVerbose = true
		t.Cleanup(func() { flagVerbose = origVerbose })

		cap := &wsCapture{}
		srv := newRunServerCov(t, cap, []cli.ChatEventPayload{
			{Type: "thinking", Content: "pondering deeply"},
			{Type: "tool_call", Content: "Bash(ls)"},
			{Type: "tool_result", Content: "files listed"},
			{Type: "status", Content: "running"},
			{Type: "text", Content: "result text"},
			{Type: "done"},
		}, http.StatusOK, http.StatusOK)

		target := filepath.Join(t.TempDir(), "saved.md")
		save, err := cli.NewAtomicFile(target)
		if err != nil {
			t.Fatalf("atomic file: %v", err)
		}
		defer save.Close()

		out, err := captureStdoutCov(t, func() error {
			return runStream(srv.URL, "ws-tok", covAgentID, "viktor", "chat1", "do it", false, nil, save, 0)
		})
		if err != nil {
			t.Fatalf("runStream: %v", err)
		}
		if !strings.Contains(out, "result text") {
			t.Errorf("stdout: %q", out)
		}
		b, rerr := os.ReadFile(target)
		if rerr != nil || string(b) != "result text" {
			t.Errorf("saved: %q err=%v", b, rerr)
		}
		if sent := cap.SentContent(); len(sent) != 1 || sent[0] != "do it" {
			t.Errorf("sent: %v", sent)
		}
	})

	t.Run("show-thinking surfaces reasoning on stdout", func(t *testing.T) {
		srv := newRunServerCov(t, &wsCapture{}, []cli.ChatEventPayload{
			{Type: "thinking", Content: "internal monologue"},
			{Type: "text", Content: "answer"},
			{Type: "done"},
		}, http.StatusOK, http.StatusOK)
		SetShowThinking(true)
		t.Cleanup(ResetAIFirstLatches)

		out, err := captureStdoutCov(t, func() error {
			return runStream(srv.URL, "ws-tok", covAgentID, "viktor", "chat1", "q", true, nil, nil, 0)
		})
		if err != nil {
			t.Fatalf("runStream: %v", err)
		}
		if !strings.Contains(out, "internal monologue") {
			t.Errorf("thinking not on stdout: %q", out)
		}
	})

	t.Run("error event aborts with agent error", func(t *testing.T) {
		srv := newRunServerCov(t, &wsCapture{}, []cli.ChatEventPayload{
			{Type: "error", Content: "tool exploded"},
		}, http.StatusOK, http.StatusOK)

		_, err := captureStdoutCov(t, func() error {
			return runStream(srv.URL, "ws-tok", covAgentID, "viktor", "chat1", "q", true, nil, nil, 0)
		})
		if err == nil || !strings.Contains(err.Error(), "agent error: tool exploded") {
			t.Fatalf("want agent error, got %v", err)
		}
	})

	t.Run("markdown renderer path", func(t *testing.T) {
		srv := newRunServerCov(t, &wsCapture{}, []cli.ChatEventPayload{
			{Type: "text", Content: "# Heading\n\nbody text\n"},
			{Type: "done"},
		}, http.StatusOK, http.StatusOK)

		out, err := captureStdoutCov(t, func() error {
			return runStream(srv.URL, "ws-tok", covAgentID, "viktor", "chat1", "q", true, cli.NewMarkdownRenderer(), nil, 0)
		})
		if err != nil {
			t.Fatalf("runStream: %v", err)
		}
		if !strings.Contains(out, "Heading") || !strings.Contains(out, "body text") {
			t.Errorf("rendered output: %q", out)
		}
	})
}

func TestRunCmdRunE_OfflineModes(t *testing.T) {
	t.Run("dry-run prints assembled prompt without auth", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = &cli.CLIConfig{} // unauthenticated on purpose
		setFlagCov(t, runCmd, "dry-run", "true")
		t.Cleanup(ResetAIFirstLatches)

		out, err := captureStdoutCov(t, func() error {
			return runCmd.RunE(runCmd, []string{"viktor", "hello world"})
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		if out != "hello world\n" {
			t.Errorf("dry-run stdout: %q", out)
		}
	})

	t.Run("estimate prints token estimate", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = &cli.CLIConfig{}
		setFlagCov(t, runCmd, "estimate", "true")
		t.Cleanup(ResetAIFirstLatches)

		out, err := captureStdoutCov(t, func() error {
			return runCmd.RunE(runCmd, []string{"viktor", "hello world"})
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		if !strings.Contains(strings.ToLower(out), "token") {
			t.Errorf("estimate stdout should mention tokens: %q", out)
		}
	})

	t.Run("prompt required when not interactive", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = &cli.CLIConfig{}
		setFlagCov(t, runCmd, "dry-run", "true")
		t.Cleanup(ResetAIFirstLatches)

		err := runCmd.RunE(runCmd, []string{"viktor"})
		if err == nil || !strings.Contains(err.Error(), "prompt is required") {
			t.Fatalf("want prompt-required error, got %v", err)
		}
	})

	t.Run("invalid effort rejected", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = &cli.CLIConfig{}
		setFlagCov(t, runCmd, "dry-run", "true")
		setFlagCov(t, runCmd, "effort", "ultra")
		t.Cleanup(ResetAIFirstLatches)

		err := runCmd.RunE(runCmd, []string{"viktor", "x"})
		if err == nil || !strings.Contains(err.Error(), `invalid --effort "ultra"`) {
			t.Fatalf("want invalid effort error, got %v", err)
		}
	})

	t.Run("plan flag prefixes prompt", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = &cli.CLIConfig{}
		setFlagCov(t, runCmd, "dry-run", "true")
		setFlagCov(t, runCmd, "plan", "true")
		t.Cleanup(ResetAIFirstLatches)

		out, err := captureStdoutCov(t, func() error {
			return runCmd.RunE(runCmd, []string{"viktor", "build the thing"})
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		if !strings.Contains(out, "build the thing") || out == "build the thing\n" {
			t.Errorf("plan mode should wrap the prompt; got %q", out)
		}
	})
}

func TestRunCmdRunE_AuthErrors(t *testing.T) {
	t.Run("no auth", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = &cli.CLIConfig{}
		err := runCmd.RunE(runCmd, []string{"viktor", "x"})
		if err == nil || !strings.Contains(err.Error(), "not logged in") {
			t.Fatalf("want not logged in, got %v", err)
		}
	})

	t.Run("no workspace", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = &cli.CLIConfig{Token: "fake-token"}
		flagWorkspace = ""
		t.Setenv("CREWSHIP_WORKSPACE", "")
		err := runCmd.RunE(runCmd, []string{"viktor", "x"})
		if err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Fatalf("want workspace error, got %v", err)
		}
	})

	t.Run("unknown agent slug suggests near matches", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
			{"id": covAgentID, "slug": "viktor"},
		}))

		err := runCmd.RunE(runCmd, []string{"vitkor", "x"})
		if err == nil || !strings.Contains(err.Error(), "agent not found: vitkor") ||
			!strings.Contains(err.Error(), "viktor") {
			t.Fatalf("want did-you-mean agent error, got %v", err)
		}
	})
}

// TestRunCmdRunE_NoStreamFullPath drives the whole RunE pipeline:
// agent slug resolution → chat creation (with effort metadata) → WS
// token fetch → WS send → no-stream collection.
func TestRunCmdRunE_NoStreamFullPath(t *testing.T) {
	cap := &wsCapture{}
	newRunServerCov(t, cap, []cli.ChatEventPayload{
		{Type: "text", Content: "done deal"},
		{Type: "done"},
	}, http.StatusOK, http.StatusOK)

	setFlagCov(t, runCmd, "no-stream", "true")
	setFlagCov(t, runCmd, "quiet", "true")
	setFlagCov(t, runCmd, "effort", "high")
	setFlagCov(t, runCmd, "timeout", "30")
	t.Cleanup(ResetAIFirstLatches)

	out, err := captureStdoutCov(t, func() error {
		return runCmd.RunE(runCmd, []string{"viktor", "ship it"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "done deal") {
		t.Errorf("stdout: %q", out)
	}
	if got := cap.ChatCreates(); got != 1 {
		t.Fatalf("expected one chat creation POST, got %d", got)
	}
	var body map[string]any
	if err := json.Unmarshal(cap.ChatBody(), &body); err != nil {
		t.Fatalf("chat body: %v (%q)", err, cap.ChatBody())
	}
	if body["origin"] != "CLI" || body["mode"] != "CHAT" {
		t.Errorf("chat creation body: %v", body)
	}
	md, _ := body["metadata"].(map[string]any)
	if md == nil || md["effort"] != "high" {
		t.Errorf("metadata should carry effort=high; got %v", body["metadata"])
	}
	if sent := cap.SentContent(); len(sent) != 1 || sent[0] != "ship it" {
		t.Errorf("ws sent: %v", sent)
	}
}

func TestRunCmdRunE_ChatReuseAndErrorPaths(t *testing.T) {
	t.Run("existing chat skips creation", func(t *testing.T) {
		cap := &wsCapture{}
		newRunServerCov(t, cap, []cli.ChatEventPayload{
			{Type: "text", Content: "follow-up answer"},
			{Type: "done"},
		}, http.StatusOK, http.StatusOK)

		setFlagCov(t, runCmd, "no-stream", "true")
		setFlagCov(t, runCmd, "quiet", "true")
		setFlagCov(t, runCmd, "chat", "chat-existing")
		t.Cleanup(ResetAIFirstLatches)

		out, err := captureStdoutCov(t, func() error {
			return runCmd.RunE(runCmd, []string{"viktor", "again"})
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		if !strings.Contains(out, "follow-up answer") {
			t.Errorf("stdout: %q", out)
		}
		if got := cap.ChatCreates(); got != 0 {
			t.Errorf("chat creation must be skipped with --chat; got %d posts", got)
		}
		if subs := cap.Subscribed(); len(subs) != 1 || subs[0] != "session:chat-existing" {
			t.Errorf("subscribed: %v", subs)
		}
	})

	t.Run("chat creation failure", func(t *testing.T) {
		newRunServerCov(t, &wsCapture{}, nil, http.StatusInternalServerError, http.StatusOK)
		t.Cleanup(ResetAIFirstLatches)

		err := runCmd.RunE(runCmd, []string{"viktor", "x"})
		if err == nil || !strings.Contains(err.Error(), "chat store down") {
			t.Fatalf("want chat creation error, got %v", err)
		}
	})

	t.Run("ws token failure", func(t *testing.T) {
		newRunServerCov(t, &wsCapture{}, nil, http.StatusOK, http.StatusInternalServerError)
		t.Cleanup(ResetAIFirstLatches)

		err := runCmd.RunE(runCmd, []string{"viktor", "x"})
		if err == nil || !strings.Contains(err.Error(), "get WS token") {
			t.Fatalf("want ws token error, got %v", err)
		}
	})
}

func TestRunListRunE(t *testing.T) {
	t.Run("auth required", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = &cli.CLIConfig{}
		if err := runListCmd.RunE(runListCmd, nil); err == nil || !strings.Contains(err.Error(), "not logged in") {
			t.Fatalf("want not logged in, got %v", err)
		}
	})

	t.Run("renders run rows", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)

		finished := "2026-01-02T00:00:00Z"
		stub.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{
			"data": []map[string]any{
				{"id": "run_aaaaaaaaaaaaaaaaaaaa", "agent_slug": "viktor", "status": "COMPLETED",
					"trigger_type": "MANUAL", "created_at": "2026-01-01T00:00:00Z", "finished_at": finished},
				{"id": "r2", "agent_slug": "eva", "status": "RUNNING",
					"trigger_type": "SCHEDULE", "created_at": "2026-01-01T01:00:00Z", "finished_at": nil},
			},
		}))

		out, err := captureStdoutCov(t, func() error {
			return runListCmd.RunE(runListCmd, nil)
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		for _, want := range []string{"viktor", "COMPLETED", "eva", "RUNNING", "run_aaaaaaaaaaaa"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, out)
			}
		}
		// 16-char ID truncation: the full 22-char ID must not appear.
		if strings.Contains(out, "run_aaaaaaaaaaaaaaaaaaaa") {
			t.Errorf("run ID was not truncated to 16 chars:\n%s", out)
		}
	})
}

// withOSStdinCov replaces os.Stdin with the read end of a pipe carrying
// the given input, restoring the original at cleanup. Used to drive
// interactive-mode loops without a TTY.
func withOSStdinCov(t *testing.T, input string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	go func() {
		_, _ = w.WriteString(input)
		_ = w.Close()
	}()
	t.Cleanup(func() {
		os.Stdin = orig
		_ = r.Close()
	})
}

// TestRunInteractive drives the interactive loop directly: initial
// prompt → response, blank line skipped, follow-up sent, Ctrl-D exits.
func TestRunInteractive(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	cap := &wsCapture{}
	mux := http.NewServeMux()
	mux.Handle("/ws", newWSEchoHandler(cap, []cli.ChatEventPayload{
		{Type: "text", Content: "reply"},
		{Type: "done"},
	}, true))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	withOSStdinCov(t, "\nfollow up\n")

	out, err := captureStdoutCov(t, func() error {
		return runInteractive(srv.URL, "ws-tok", covAgentID, "viktor", "chat1", "initial question", false, nil, nil, 0)
	})
	if err != nil {
		t.Fatalf("runInteractive: %v", err)
	}
	if !strings.Contains(out, "reply") {
		t.Errorf("stdout: %q", out)
	}
	sent := cap.SentContent()
	if len(sent) != 2 || sent[0] != "initial question" || sent[1] != "follow up" {
		t.Errorf("sent messages: %v", sent)
	}
}

func TestRunInteractive_StreamErrorPropagates(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	cap := &wsCapture{}
	mux := http.NewServeMux()
	mux.Handle("/ws", newWSEchoHandler(cap, []cli.ChatEventPayload{
		{Type: "error", Content: "kaput"},
	}, true))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	withOSStdinCov(t, "")

	_, err := captureStdoutCov(t, func() error {
		return runInteractive(srv.URL, "ws-tok", covAgentID, "viktor", "chat1", "go", true, nil, nil, 0)
	})
	if err == nil || !strings.Contains(err.Error(), "agent error: kaput") {
		t.Fatalf("want agent error from initial prompt, got %v", err)
	}
}

// TestRunCmdRunE_InteractiveFlag covers the RunE → runInteractive branch
// end to end: initial prompt streams, then EOF on stdin ends the session.
func TestRunCmdRunE_InteractiveFlag(t *testing.T) {
	cap := &wsCapture{}
	newRunServerCov(t, cap, []cli.ChatEventPayload{
		{Type: "text", Content: "interactive answer"},
		{Type: "done"},
	}, http.StatusOK, http.StatusOK)

	setFlagCov(t, runCmd, "interactive", "true")
	setFlagCov(t, runCmd, "quiet", "true")
	t.Cleanup(ResetAIFirstLatches)
	withOSStdinCov(t, "") // immediate EOF after the initial prompt round

	out, err := captureStdoutCov(t, func() error {
		return runCmd.RunE(runCmd, []string{"viktor", "hi there"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "interactive answer") {
		t.Errorf("stdout: %q", out)
	}
	if sent := cap.SentContent(); len(sent) != 1 || !strings.Contains(sent[0], "hi there") {
		t.Errorf("sent: %v", sent)
	}
}

func TestRunCmdRunE_StreamDefaultPathWithSave(t *testing.T) {
	cap := &wsCapture{}
	newRunServerCov(t, cap, []cli.ChatEventPayload{
		{Type: "__pong"}, // non-chat_event frame must be skipped
		{Type: "text", Content: "streamed text"},
		{Type: "done"},
	}, http.StatusOK, http.StatusOK)

	target := filepath.Join(t.TempDir(), "run.md")
	setFlagCov(t, runCmd, "quiet", "true")
	setFlagCov(t, runCmd, "save", target)
	t.Cleanup(ResetAIFirstLatches)

	out, err := captureStdoutCov(t, func() error {
		return runCmd.RunE(runCmd, []string{"viktor", "stream it"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "streamed text") {
		t.Errorf("stdout: %q", out)
	}
	b, rerr := os.ReadFile(target)
	if rerr != nil || string(b) != "streamed text" {
		t.Errorf("saved file: %q err=%v", b, rerr)
	}
}

func TestRunCmdRunE_SaveOpenError(t *testing.T) {
	newRunServerCov(t, &wsCapture{}, nil, http.StatusOK, http.StatusOK)
	setFlagCov(t, runCmd, "save", filepath.Join(t.TempDir(), "no-dir", "x.md"))
	t.Cleanup(ResetAIFirstLatches)

	err := runCmd.RunE(runCmd, []string{"viktor", "x"})
	if err == nil || !strings.Contains(err.Error(), "open save file") {
		t.Fatalf("want open save file error, got %v", err)
	}
}

func TestRunCmdRunE_BadPromptFile(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	setFlagCov(t, runCmd, "dry-run", "true")
	setFlagCov(t, runCmd, "prompt", "@"+filepath.Join(t.TempDir(), "missing.txt"))
	t.Cleanup(ResetAIFirstLatches)

	err := runCmd.RunE(runCmd, []string{"viktor"})
	if err == nil {
		t.Fatal("want prompt file read error")
	}
}

func TestRunCmdRunE_ShowThinkingFlag(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	setFlagCov(t, runCmd, "dry-run", "true")
	setFlagCov(t, runCmd, "show-thinking", "true")
	t.Cleanup(ResetAIFirstLatches)

	if _, err := captureStdoutCov(t, func() error {
		return runCmd.RunE(runCmd, []string{"viktor", "x"})
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// RunE defers ResetAIFirstLatches, so the latch must be back to false
	// once the command returns — a second invocation in the same process
	// (REPL turn) must not inherit it.
	if showThinking {
		t.Error("showThinking latch must be reset when RunE returns")
	}
}

func TestRunCmdRunE_ChatDecodeError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agents", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"` + covAgentID + `","slug":"viktor"}]`))
	})
	mux.HandleFunc("/api/v1/agents/"+covAgentID+"/chats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`this is not json`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	saveCLIState(t)
	t.Setenv("CREWSHIP_SERVER", "")
	t.Setenv("CREWSHIP_WORKSPACE", "")
	flagServer = ""
	flagWorkspace = ""
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: covWorkspaceID, Server: srv.URL}
	t.Cleanup(ResetAIFirstLatches)

	if err := runCmd.RunE(runCmd, []string{"viktor", "x"}); err == nil {
		t.Fatal("want chat decode error")
	}
}

func TestRunHelpers_BadServerURL(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	if err := runNoStream("http://127.0.0.1:1", "tok", covAgentID, "c", "p", true, nil, nil, 0); err == nil {
		t.Error("runNoStream must fail on unreachable server")
	}
	if err := runStream("http://127.0.0.1:1", "tok", covAgentID, "slug", "c", "p", true, nil, nil, 0); err == nil {
		t.Error("runStream must fail on unreachable server")
	}
	if err := runInteractive("http://127.0.0.1:1", "tok", covAgentID, "slug", "c", "p", true, nil, nil, 0); err == nil {
		t.Error("runInteractive must fail on unreachable server")
	}
}

func TestRunNoStream_PongSkippedAndMarkdown(t *testing.T) {
	srv := newRunServerCov(t, &wsCapture{}, []cli.ChatEventPayload{
		{Type: "__pong"},
		{Type: "text", Content: "# md text"},
		{Type: "done"},
	}, http.StatusOK, http.StatusOK)

	out, err := captureStdoutCov(t, func() error {
		return runNoStream(srv.URL, "ws-tok", covAgentID, "chat1", "hi", true, cli.NewMarkdownRenderer(), nil, 0)
	})
	if err != nil {
		t.Fatalf("runNoStream: %v", err)
	}
	if !strings.Contains(out, "md text") {
		t.Errorf("stdout: %q", out)
	}
}

func TestRunNoStream_SaveWriteError(t *testing.T) {
	srv := newRunServerCov(t, &wsCapture{}, []cli.ChatEventPayload{
		{Type: "text", Content: "content"},
		{Type: "done"},
	}, http.StatusOK, http.StatusOK)

	save, err := cli.NewAtomicFile(filepath.Join(t.TempDir(), "x.md"))
	if err != nil {
		t.Fatal(err)
	}
	_ = save.Close() // closed underlying tempfile → WriteString fails

	_, err = captureStdoutCov(t, func() error {
		return runNoStream(srv.URL, "ws-tok", covAgentID, "chat1", "hi", true, nil, save, 0)
	})
	if err == nil || !strings.Contains(err.Error(), "save write") {
		t.Fatalf("want save write error, got %v", err)
	}
}

func TestStreamEvents_WSReadErrorMidStream(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	// Script has no done event → server closes after text → read error.
	srv := newRunServerCov(t, &wsCapture{}, []cli.ChatEventPayload{
		{Type: "text", Content: "half an answer"},
	}, http.StatusOK, http.StatusOK)

	_, err := captureStdoutCov(t, func() error {
		return runStream(srv.URL, "ws-tok", covAgentID, "viktor", "chat1", "q", true, nil, nil, 0)
	})
	if err == nil || !strings.Contains(err.Error(), "ws read") {
		t.Fatalf("want ws read error, got %v", err)
	}
}

func TestStreamEvents_SaveWriteFailureSurfaces(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	srv := newRunServerCov(t, &wsCapture{}, []cli.ChatEventPayload{
		{Type: "text", Content: "some text"},
		{Type: "done"},
	}, http.StatusOK, http.StatusOK)

	save, err := cli.NewAtomicFile(filepath.Join(t.TempDir(), "x.md"))
	if err != nil {
		t.Fatal(err)
	}
	_ = save.Close() // forces both WriteString and any Commit to fail

	_, err = captureStdoutCov(t, func() error {
		return runStream(srv.URL, "ws-tok", covAgentID, "viktor", "chat1", "q", true, nil, save, 0)
	})
	if err == nil || !strings.Contains(err.Error(), "save write") {
		t.Fatalf("want save write error returned on done, got %v", err)
	}
}

func TestRunListRunE_ErrorPaths(t *testing.T) {
	t.Run("network error", func(t *testing.T) {
		saveCLIState(t)
		t.Setenv("CREWSHIP_SERVER", "")
		t.Setenv("CREWSHIP_WORKSPACE", "")
		flagServer = ""
		flagWorkspace = ""
		cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: covWorkspaceID, Server: "http://127.0.0.1:1"}
		if err := runListCmd.RunE(runListCmd, nil); err == nil {
			t.Fatal("want connection error")
		}
	})

	t.Run("server error", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnGet("/api/v1/runs", clitest.ErrorResponse(500, "runs store down"))
		err := runListCmd.RunE(runListCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "runs store down") {
			t.Fatalf("want surfaced 500, got %v", err)
		}
	})

	t.Run("decode error", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnGet("/api/v1/runs", clitest.TextResponse(200, "not json"))
		if err := runListCmd.RunE(runListCmd, nil); err == nil {
			t.Fatal("want decode error")
		}
	})
}
