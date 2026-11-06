package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"golang.org/x/net/websocket"

	"github.com/crewship-ai/crewship/internal/cli"
	wsproto "github.com/crewship-ai/crewship/internal/ws"
)

// craftInstructionServer stands in for the production server: a saved
// instruction answers with its echo and a done that carries no reply, and task
// admission binds that message and nothing else.
func craftInstructionServer(t *testing.T, busy bool) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var sends []string
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
	mux.HandleFunc("/api/v1/agents", func(w http.ResponseWriter, _ *http.Request) {
		reply(w, 200, `[{"id":"agent-src","slug":"viktor"}]`)
	})
	mux.HandleFunc("POST /api/v1/agents/agent-src/chats", func(w http.ResponseWriter, _ *http.Request) {
		reply(w, 201, `{"id":"chat-new"}`)
	})
	mux.HandleFunc("GET /api/v1/chats/chat-new/execution-profile", func(w http.ResponseWriter, _ *http.Request) {
		reply(w, 200, `{"mode":"trusted"}`)
	})
	mux.HandleFunc("/api/v1/ws-token", func(w http.ResponseWriter, _ *http.Request) {
		reply(w, 200, `{"token":"ws-tok"}`)
	})
	mux.HandleFunc("POST /api/v1/pages/demo/craft/tasks", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var task struct {
			ChatID    string `json:"chat_id"`
			MessageID string `json:"message_id"`
		}
		if json.Unmarshal(raw, &task) != nil || task.ChatID != "chat-new" || task.MessageID != "saved-instruction" {
			reply(w, 403, `{"error":"native Craft agent task denied"}`)
			return
		}
		reply(w, 202, `{"work_id":"accepted-work","status":"queued"}`)
	})
	mux.Handle("/ws", websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		for {
			var raw []byte
			if websocket.Message.Receive(conn, &raw) != nil {
				return
			}
			var frame cli.WSMessage
			if json.Unmarshal(raw, &frame) != nil || frame.Type != "send_message" {
				continue
			}
			mu.Lock()
			sends = append(sends, string(frame.Payload))
			mu.Unlock()
			if busy {
				_ = sendScriptedEvents(conn, []cli.ChatEventPayload{{Type: wsproto.AgentBusyEventType, Content: "busy"}})
				continue
			}
			_ = sendScriptedEvents(conn, []cli.ChatEventPayload{
				{Type: "user_message", Content: "echo", Metadata: map[string]any{"message_id": "saved-instruction"}},
				{Type: "done", Metadata: map[string]any{"no_reply": true}},
			})
		}
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), sends...)
	}
}

// #5009: the CLI start matches the browser's. The instruction is saved with
// the literal mark that withholds a run, and only that message can start a task.
func TestAcceptance_CraftTaskInstructionThenStart(t *testing.T) {
	srv, sent := craftInstructionServer(t, false)
	out, code := runArtifactBinary(t, srv, "craft", "task", "instruction", "viktor", "Rewrite the summary")
	if code != 0 || !strings.Contains(out, "chat-new") || !strings.Contains(out, "saved-instruction") {
		t.Fatalf("instruction exit %d: %s", code, out)
	}
	sends := sent()
	if len(sends) != 1 {
		t.Fatalf("want one send, got %d", len(sends))
	}
	var frame struct {
		Session  string         `json:"session_id"`
		Content  string         `json:"content"`
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(sends[0]), &frame); err != nil {
		t.Fatal(err)
	}
	if frame.Session != "chat-new" || frame.Content != "Rewrite the summary" || len(frame.Metadata) != 1 || frame.Metadata["native_craft_instruction"] != true {
		t.Fatalf("instruction frame: %s", sends[0])
	}
	if out, code = runArtifactBinary(t, srv, "craft", "task", "start", "demo", "--chat-id", "chat-new", "--message-id", "saved-instruction", "--whole-document"); code != 0 || !strings.Contains(out, "accepted-work") {
		t.Fatalf("start exit %d: %s", code, out)
	}
	if out, code = runArtifactBinary(t, srv, "craft", "task", "start", "demo", "--chat-id", "chat-new", "--message-id", "ordinary-turn", "--whole-document"); code == 0 || !strings.Contains(out, "native HTTP 403") {
		t.Fatalf("an ordinary turn started a task: exit %d: %s", code, out)
	}
}

// A send that was not confirmed as saved names no message to start from.
func TestAcceptance_CraftTaskInstructionRefusedWithoutSavedEcho(t *testing.T) {
	srv, _ := craftInstructionServer(t, true)
	out, code := runArtifactBinary(t, srv, "craft", "task", "instruction", "viktor", "Rewrite the summary")
	if code == 0 || !strings.Contains(out, "not confirmed as saved") {
		t.Fatalf("unconfirmed instruction exit %d: %s", code, out)
	}
}
