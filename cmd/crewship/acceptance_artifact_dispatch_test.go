//go:build unix

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	wsproto "github.com/crewship-ai/crewship/internal/ws"
	"golang.org/x/net/websocket"
)

// artifactDispatchFixture is a stand-in host for the artifact viewer's
// "send to agent" round trip: comment create, source-chat lookup, the chat
// send carrying artifact_project_intent and the comment's later answer. It
// proves the CLI's wire behaviour; server admission of the intent is covered
// by internal/api's artifact chat tests.
type artifactDispatchFixture struct {
	busy     bool
	mu       sync.Mutex
	sends    []json.RawMessage
	polls    atomic.Int32
	comments atomic.Int32
}

func (f *artifactDispatchFixture) sent() []json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]json.RawMessage(nil), f.sends...)
}

func (f *artifactDispatchFixture) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
	mux.HandleFunc("POST /api/v1/pages/demo/artifact/comments", func(w http.ResponseWriter, r *http.Request) {
		f.comments.Add(1)
		reply(w, 201, `{"id":"comment1","version":3,"path":"index.html","selector":"#hero","quote":"Title","text":"Make it bold","status":"pending","replies":[]}`)
	})
	mux.HandleFunc("GET /api/v1/pages/demo/artifact", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("version") != "3" {
			t.Errorf("source chat read the wrong version: %s", r.URL.RawQuery)
		}
		reply(w, 200, `{"page_id":"cpagedemo","origin":{"chat_id":"chat-src","agent_id":"agent-src"}}`)
	})
	mux.HandleFunc("GET /api/v1/pages/demo/artifact/comments", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("version") != "3" {
			t.Errorf("poll read the wrong version: %s", r.URL.RawQuery)
		}
		status, replies := "working", `[]`
		if f.polls.Add(1) > 1 {
			status, replies = "answered", `[{"text":"Made the title bold in version 4","version":4,"author":"agent:agent-src","created_at":"2026-10-29T00:00:00Z"}]`
		}
		reply(w, 200, `{"comments":[{"id":"other","version":3,"status":"pending"},{"id":"comment1","version":3,"path":"index.html","selector":"#hero","quote":"Title","text":"Make it bold","status":"`+status+`","answered_version":4,"replies":`+replies+`}]}`)
	})
	mux.HandleFunc("GET /api/v1/chats/{chat}/execution-profile", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, `{"mode":"trusted"}`)
	})
	mux.HandleFunc("GET /api/v1/agents/{agent}/run-profile", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, `{"mode":"trusted"}`)
	})
	mux.HandleFunc("/api/v1/agents", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, `[{"id":"agent-src","slug":"viktor"}]`)
	})
	mux.HandleFunc("/api/v1/ws-token", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, `{"token":"ws-tok"}`)
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
			f.mu.Lock()
			f.sends = append(f.sends, frame.Payload)
			f.mu.Unlock()
			if f.busy {
				_ = sendScriptedEvents(conn, []cli.ChatEventPayload{{Type: wsproto.AgentBusyEventType, Content: "The agent is busy with another run right now."}})
				continue // the real server sends nothing after a busy bounce
			}
			var payload struct {
				Metadata map[string]any `json:"metadata"`
			}
			_ = json.Unmarshal(frame.Payload, &payload)
			_ = sendScriptedEvents(conn, []cli.ChatEventPayload{
				{Type: "user_message", Content: "echo", Metadata: map[string]any{"message_id": "m1", "artifact_project_intent": payload.Metadata["artifact_project_intent"]}},
				{Type: "text", Content: "Working on it."},
				{Type: "done"},
			})
		}
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func runArtifactBinary(t *testing.T, srv *httptest.Server, args ...string) (string, int) {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(cfg, []byte(fmt.Sprintf("server: %s\ntoken: fake-token\nworkspace: %s\nformat: json\n", srv.URL, covWorkspaceID)), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, buildCrewshipBinary(t), args...)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "CREWSHIP_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+cfg, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1", "HOME="+dir)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return string(out), exit.ExitCode()
	}
	if err != nil {
		t.Fatalf("%v: %v %s", args, err, out)
	}
	return string(out), 0
}

func artifactSentIntent(t *testing.T, raw json.RawMessage) (string, string, map[string]any) {
	t.Helper()
	var got struct {
		Session  string         `json:"session_id"`
		Content  string         `json:"content"`
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("send frame: %v %s", err, raw)
	}
	intent, _ := got.Metadata["artifact_project_intent"].(map[string]any)
	return got.Session, got.Content, intent
}

// The CLI half of the "stalls at pending" bug: --send-to-agent alone only
// queues the task, so --wait must deliver the exact intent the viewer sends
// and then report the agent's answer.
func TestAcceptance_ArtifactCommentWaitDeliversIntentAndReportsAnswer(t *testing.T) {
	fixture := &artifactDispatchFixture{}
	srv := fixture.server(t)
	out, code := runArtifactBinary(t, srv, "page", "artifact", "comment", "demo", "--version", "3", "--path", "index.html", "--selector", "#hero", "--quote", "Title", "--text", "Make it bold", "--send-to-agent", "--wait")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	sends := fixture.sent()
	if len(sends) != 1 {
		t.Fatalf("want exactly one task message, got %d", len(sends))
	}
	session, content, intent := artifactSentIntent(t, sends[0])
	if session != "chat-src" || !strings.Contains(content, "reply to comment comment1") {
		t.Fatalf("task message went to %q with %q", session, content)
	}
	want := map[string]any{"submission_id": "artifact-comment-comment1", "page_id": "cpagedemo", "comment_id": "comment1", "base_version": float64(3)}
	if len(intent) != len(want) {
		t.Fatalf("intent = %v, want %v", intent, want)
	}
	for k, v := range want {
		if intent[k] != v {
			t.Fatalf("intent[%s] = %v, want %v (%v)", k, intent[k], v, intent)
		}
	}
	if !strings.Contains(out, `"status": "answered"`) || !strings.Contains(out, "Made the title bold in version 4") {
		t.Fatalf("answer not reported: %s", out)
	}
	if fixture.comments.Load() != 1 {
		t.Fatal("comment was not created exactly once")
	}
}

func TestAcceptance_ArtifactCommentWaitBusyAgentExitsBusy(t *testing.T) {
	fixture := &artifactDispatchFixture{busy: true}
	srv := fixture.server(t)
	out, code := runArtifactBinary(t, srv, "page", "artifact", "comment", "demo", "--version", "3", "--path", "index.html", "--selector", "#hero", "--quote", "Title", "--text", "Make it bold", "--send-to-agent", "--wait")
	if code != cli.ExitBusy {
		t.Fatalf("busy agent exit = %d, want %d: %s", code, cli.ExitBusy, out)
	}
	if !strings.Contains(out, "agent busy") || !strings.Contains(out, `"submission_id":"artifact-comment-comment1"`) || !strings.Contains(out, "--artifact-target") {
		t.Fatalf("busy refusal lacks the retry: %s", out)
	}
	if fixture.polls.Load() != 0 {
		t.Fatal("polled for an answer although the task never started")
	}
}

func TestAcceptance_RunArtifactTargetSendsExactIntent(t *testing.T) {
	fixture := &artifactDispatchFixture{}
	srv := fixture.server(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	const packet = `{"kind":"design","submission_id":"retry-1","page_id":"cpagedemo","comment_id":"comment1","base_version":3}`
	if err := os.WriteFile(target, []byte(packet), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := runArtifactBinary(t, srv, "run", "viktor", "--chat", "chat-src", "--artifact-target", target, "--no-stream", "Apply comment comment1")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	sends := fixture.sent()
	if len(sends) != 1 {
		t.Fatalf("want one send, got %d", len(sends))
	}
	session, content, intent := artifactSentIntent(t, sends[0])
	var want map[string]any
	_ = json.Unmarshal([]byte(packet), &want)
	a, _ := json.Marshal(intent)
	b, _ := json.Marshal(want)
	if session != "chat-src" || content != "Apply comment comment1" || string(a) != string(b) {
		t.Fatalf("changed target: %s %q %s", session, content, a)
	}
}

func TestAcceptance_RunArtifactTargetRefusalsAndBusy(t *testing.T) {
	fixture := &artifactDispatchFixture{busy: true}
	srv := fixture.server(t)
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for _, tc := range []struct{ name, packet string }{
		{"unknown-field", `{"submission_id":"s1","page_id":"p","base_version":1,"extra":true}`},
		{"comment-without-page", `{"submission_id":"s1","comment_id":"c1"}`},
		{"zero-base", `{"submission_id":"s1","page_id":"p","base_version":0}`},
		{"duplicate-key", `{"submission_id":"s1","submission_id":"s2"}`},
	} {
		out, code := runArtifactBinary(t, srv, "run", "viktor", "--chat", "chat-src", "--artifact-target", write(tc.name+".json", tc.packet), "--no-stream", "x")
		if code != cli.ExitValidation {
			t.Fatalf("%s: exit %d, want %d: %s", tc.name, code, cli.ExitValidation, out)
		}
	}
	if out, code := runArtifactBinary(t, srv, "run", "viktor", "--artifact-target", write("create.json", `{"submission_id":"create-1"}`), "--no-stream", "x"); code != cli.ExitValidation {
		t.Fatalf("target without --chat: exit %d: %s", code, out)
	}
	if len(fixture.sent()) != 0 {
		t.Fatal("a refused target reached the chat")
	}
	out, code := runArtifactBinary(t, srv, "run", "viktor", "--chat", "chat-src", "--artifact-target", write("create.json", `{"submission_id":"create-1"}`), "--no-stream", "Describe it: a pricing page")
	if code != cli.ExitBusy || !strings.Contains(out, "busy") {
		t.Fatalf("busy run exit %d, want %d: %s", code, cli.ExitBusy, out)
	}
}
