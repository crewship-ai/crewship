package main

// Coverage tests for cmd_ask_fanout.go — runFanout / fanoutOne against a
// combined HTTP + WebSocket stub server. The WS handler speaks the same
// minimal protocol internal/cli.WSClient uses (subscribe → send_message
// → chat_event stream), so the full fan-out pipeline runs for real:
// chat creation over HTTP, prompt delivery over WS, buffered display,
// per-agent error slots, and the --save artefact.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/crewship-ai/crewship/internal/cli"
)

// covFanoutServer serves:
//
//	POST /api/v1/agents/{id}/chats  → 200 {"id":"chat-{id}"}; 500 for agent-bad
//	GET  /ws                        → per-agent scripted chat_event streams
func covFanoutServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/agents/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		// api v1 agents {id} chats
		if len(parts) != 5 || parts[4] != "chats" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		agentID := parts[3]
		w.Header().Set("Content-Type", "application/json")
		switch agentID {
		case "agent-bad":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"chat backend down"}`))
			return
		case "agent-badjson":
			_, _ = w.Write([]byte(`not json`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "chat-" + agentID})
	})

	mux.Handle("/ws", websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		send := func(evType, content string) {
			payload, _ := json.Marshal(map[string]string{"type": evType, "content": content})
			frame, _ := json.Marshal(map[string]any{"type": "chat_event", "payload": json.RawMessage(payload)})
			_ = websocket.Message.Send(conn, string(frame))
		}
		for {
			var raw []byte
			if err := websocket.Message.Receive(conn, &raw); err != nil {
				return
			}
			var msg struct {
				Type    string `json:"type"`
				Channel string `json:"channel"`
			}
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			if msg.Type == "subscribe" {
				// Non-chat_event frame → client's ParseChatEvent returns
				// nil and the read loop must skip it.
				ack, _ := json.Marshal(map[string]string{"type": "subscribed", "channel": msg.Channel})
				_ = websocket.Message.Send(conn, string(ack))
				continue
			}
			if msg.Type != "send_message" {
				continue
			}
			agentID := strings.TrimPrefix(msg.Channel, "agent:")
			switch agentID {
			case "agent-err":
				send("text", "partial-before-error")
				send("error", "agent exploded")
				return
			case "agent-drop":
				send("text", "text-before-drop")
				return // close without done → read error path
			case "agent-silent":
				// Never respond — lets ctx-cancel tests drive the
				// read-unblock-via-Close path.
				continue
			default:
				send("text", "# hello-from-"+agentID)
				send("done", "")
				// Keep the conn open; client returns on done.
			}
		}
	}))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestRunFanout_NoAgents(t *testing.T) {
	err := runFanout("http://127.0.0.1:0", "tok", nil, "p", true, nil, nil, 1)
	if err == nil || !strings.Contains(err.Error(), "no agents") {
		t.Errorf("expected no-agents error; got %v", err)
	}
}

func TestRunFanout_MixedResults(t *testing.T) {
	srv := covFanoutServer(t)
	covSetupCli8(t, srv.URL)

	savePath := filepath.Join(t.TempDir(), "out.md")
	save, err := cli.NewAtomicFile(savePath)
	if err != nil {
		t.Fatalf("NewAtomicFile: %v", err)
	}
	defer save.Close()

	agents := map[string]string{
		"agent-a":    "alpha",
		"agent-err":  "erratic",
		"agent-drop": "dropper",
		"agent-bad":  "badcreate",
	}

	var runErr error
	out := covCaptureStdoutCli8(t, func() {
		runErr = runFanout(srv.URL, "ws-tok", agents, "what is up?", false, nil, save, 10)
	})
	if runErr != nil {
		t.Fatalf("runFanout: %v", runErr)
	}

	// Display: deterministic alphabetical slug order with headers.
	idxAlpha := strings.Index(out, "=== alpha ===")
	idxDrop := strings.Index(out, "=== dropper ===")
	idxErr := strings.Index(out, "=== erratic ===")
	if idxAlpha < 0 || idxDrop < 0 || idxErr < 0 {
		t.Fatalf("missing agent headers:\n%s", out)
	}
	if !(idxAlpha < idxDrop && idxDrop < idxErr) {
		t.Errorf("slugs not in sorted order:\n%s", out)
	}
	if !strings.Contains(out, "hello-from-agent-a") {
		t.Errorf("successful agent text missing:\n%s", out)
	}
	if !strings.Contains(out, "partial-before-error") || !strings.Contains(out, "text-before-drop") {
		t.Errorf("partial texts must be displayed before error footers:\n%s", out)
	}

	// Save artefact committed with every header + partial text.
	saved, err := os.ReadFile(savePath)
	if err != nil {
		t.Fatalf("read save file: %v", err)
	}
	for _, want := range []string{
		"=== alpha ===", "hello-from-agent-a",
		"=== erratic ===", "partial-before-error",
		"=== dropper ===", "text-before-drop",
		"=== badcreate ===",
	} {
		if !strings.Contains(string(saved), want) {
			t.Errorf("save file missing %q:\n%s", want, saved)
		}
	}
}

func TestRunFanout_QuietMarkdown(t *testing.T) {
	srv := covFanoutServer(t)
	covSetupCli8(t, srv.URL)

	agents := map[string]string{"agent-a": "alpha"}
	md := cli.NewMarkdownRenderer()

	var runErr error
	out := covCaptureStdoutCli8(t, func() {
		runErr = runFanout(srv.URL, "ws-tok", agents, "hi", true, md, nil, 10)
	})
	if runErr != nil {
		t.Fatalf("runFanout: %v", runErr)
	}
	// quiet suppresses the header but not the body.
	if strings.Contains(out, "=== alpha ===") {
		t.Errorf("quiet mode must not print headers:\n%s", out)
	}
	if !strings.Contains(out, "hello-from-agent-a") {
		t.Errorf("agent text missing:\n%s", out)
	}
}

func TestFanoutOne_Success(t *testing.T) {
	srv := covFanoutServer(t)
	covSetupCli8(t, srv.URL)
	client := newAPIClient()

	text, err := fanoutOne(t.Context(), client, srv.URL, "ws-tok", "agent-a", "ping")
	if err != nil {
		t.Fatalf("fanoutOne: %v", err)
	}
	if text != "# hello-from-agent-a" {
		t.Errorf("text: got %q", text)
	}
}

func TestFanoutOne_AgentErrorEvent(t *testing.T) {
	srv := covFanoutServer(t)
	covSetupCli8(t, srv.URL)
	client := newAPIClient()

	text, err := fanoutOne(t.Context(), client, srv.URL, "ws-tok", "agent-err", "ping")
	if err == nil || !strings.Contains(err.Error(), "agent error: agent exploded") {
		t.Errorf("expected agent-error; got %v", err)
	}
	if text != "partial-before-error" {
		t.Errorf("partial text must be returned alongside the error; got %q", text)
	}
}

func TestFanoutOne_ConnectionDrop(t *testing.T) {
	srv := covFanoutServer(t)
	covSetupCli8(t, srv.URL)
	client := newAPIClient()

	text, err := fanoutOne(t.Context(), client, srv.URL, "ws-tok", "agent-drop", "ping")
	if err == nil || !strings.Contains(err.Error(), "read:") {
		t.Errorf("expected read error on drop; got %v", err)
	}
	if text != "text-before-drop" {
		t.Errorf("partial text before drop: got %q", text)
	}
}

func TestFanoutOne_ChatCreateFails(t *testing.T) {
	srv := covFanoutServer(t)
	covSetupCli8(t, srv.URL)
	client := newAPIClient()

	_, err := fanoutOne(t.Context(), client, srv.URL, "ws-tok", "agent-bad", "ping")
	if err == nil || !strings.Contains(err.Error(), "chat backend down") {
		t.Errorf("expected chat-create failure; got %v", err)
	}
}

func TestFanoutOne_ChatDecodeError(t *testing.T) {
	srv := covFanoutServer(t)
	covSetupCli8(t, srv.URL)
	client := newAPIClient()

	if _, err := fanoutOne(t.Context(), client, srv.URL, "ws-tok", "agent-badjson", "ping"); err == nil {
		t.Error("expected decode error for malformed chat-create body")
	}
}

func TestFanoutOne_ChatCreateTransportError(t *testing.T) {
	srv := covFanoutServer(t)
	// Point the API client at a dead server; WS would go to the live one
	// but the flow must fail at chat creation already.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	covSetupCli8(t, deadURL)
	client := newAPIClient()

	_, err := fanoutOne(t.Context(), client, srv.URL, "ws-tok", "agent-a", "ping")
	if err == nil || !strings.Contains(err.Error(), "create chat") {
		t.Errorf("expected create-chat transport error; got %v", err)
	}
}

func TestFanoutOne_ContextCancelled(t *testing.T) {
	srv := covFanoutServer(t)
	covSetupCli8(t, srv.URL)
	client := newAPIClient()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	// agent-silent never answers; the cancel must unblock the read loop
	// and surface as a "cancelled" error.
	_, err := fanoutOne(ctx, client, srv.URL, "ws-tok", "agent-silent", "ping")
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("expected cancelled error; got %v", err)
	}
}

// TestRunFanout_SaveWriteFailure pre-breaks the save file (Close removes
// the tempfile) so the first write fails — the error must short-circuit
// later writes and surface as the command result.
func TestRunFanout_SaveWriteFailure(t *testing.T) {
	srv := covFanoutServer(t)
	covSetupCli8(t, srv.URL)

	save, err := cli.NewAtomicFile(filepath.Join(t.TempDir(), "out.md"))
	if err != nil {
		t.Fatal(err)
	}
	_ = save.Close() // removes the tempfile; writes now fail

	agents := map[string]string{"agent-a": "alpha", "agent-err": "erratic"}
	var runErr error
	covCaptureStdoutCli8(t, func() {
		runErr = runFanout(srv.URL, "ws-tok", agents, "hi", false, nil, save, 10)
	})
	if runErr == nil || !strings.Contains(runErr.Error(), "save write") {
		t.Errorf("expected save-write error; got %v", runErr)
	}
}

// TestRunFanout_SaveCommitFailure removes the target directory after the
// tempfile is created: writes succeed on the open fd but the final
// rename in Commit must fail and be surfaced.
func TestRunFanout_SaveCommitFailure(t *testing.T) {
	srv := covFanoutServer(t)
	covSetupCli8(t, srv.URL)

	dir := filepath.Join(t.TempDir(), "subdir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	save, err := cli.NewAtomicFile(filepath.Join(dir, "out.md"))
	if err != nil {
		t.Fatal(err)
	}
	defer save.Close()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	agents := map[string]string{"agent-a": "alpha"}
	var runErr error
	covCaptureStdoutCli8(t, func() {
		runErr = runFanout(srv.URL, "ws-tok", agents, "hi", false, nil, save, 10)
	})
	if runErr == nil || !strings.Contains(runErr.Error(), "save commit") {
		t.Errorf("expected save-commit error; got %v", runErr)
	}
}

func TestFanoutOne_WSDialFails(t *testing.T) {
	srv := covFanoutServer(t)
	covSetupCli8(t, srv.URL)
	client := newAPIClient()

	// Chat creation succeeds against srv, but the WS endpoint points at a
	// closed port → dial error.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	_, err := fanoutOne(t.Context(), client, deadURL, "ws-tok", "agent-a", "ping")
	if err == nil || !strings.Contains(err.Error(), "ws:") {
		t.Errorf("expected ws dial error; got %v", err)
	}
}
