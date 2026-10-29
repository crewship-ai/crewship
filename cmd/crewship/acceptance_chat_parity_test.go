//go:build unix

package main

// Acceptance for the chat-parity commands and flags (#4504), driven through
// the BUILT BINARY against a stub that speaks the server's real wire shapes:
// the HTTP routes the commands call, and a WebSocket that records every frame
// the CLI sends and answers with ws.ServerMessage/ws.ChatEvent — the server's
// own types, so a renamed field breaks here rather than in production.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/documents"
	wsproto "github.com/crewship-ai/crewship/internal/ws"
)

const (
	parityAgentID = "ag_parity"
	parityChatID  = "chat_parity"
)

type parityRequest struct {
	Method string
	Path   string
	Query  string
	Body   []byte
}

// parityStub is one fake server. Tests set the hooks they need before running
// the binary; every request and every WS frame is recorded.
type parityStub struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	requests []parityRequest
	frames   []cli.WSMessage
	gotFrame chan cli.WSMessage

	messagesBody string // GET /chats/{id}/messages
	streamBody   string // GET /chats/{id}/stream (NDJSON)
	askForms     string // agent detail ask_forms
	chatList     string // GET /agents/{id}/chats
	// onFrame answers one client frame. Return false to close the socket.
	onFrame func(conn *websocket.Conn, f cli.WSMessage) bool
	routes  map[string]http.HandlerFunc // "METHOD /path" exact matches, checked first
}

func newParityStub(t *testing.T) *parityStub {
	t.Helper()
	s := &parityStub{t: t, gotFrame: make(chan cli.WSMessage, 64), routes: map[string]http.HandlerFunc{}}
	wsHandler := websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		for {
			var raw []byte
			if websocket.Message.Receive(conn, &raw) != nil {
				return
			}
			var f cli.WSMessage
			if json.Unmarshal(raw, &f) != nil {
				return
			}
			s.mu.Lock()
			s.frames = append(s.frames, f)
			hook := s.onFrame
			s.mu.Unlock()
			select {
			case s.gotFrame <- f:
			default:
			}
			if hook != nil && !hook(conn, f) {
				return
			}
		}
	})
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws" {
			wsHandler.ServeHTTP(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		s.mu.Lock()
		s.requests = append(s.requests, parityRequest{r.Method, r.URL.Path, r.URL.RawQuery, body})
		route := s.routes[r.Method+" "+r.URL.Path]
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if route != nil {
			route(w, r)
			return
		}
		s.serve(w, r)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *parityStub) serve(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case p == "/api/v1/ws-token":
		_, _ = w.Write([]byte(`{"token":"ws-tok"}`))
	case p == "/api/v1/agents":
		_, _ = w.Write([]byte(`[{"id":"` + parityAgentID + `","slug":"viktor","name":"Viktor"}]`))
	case p == "/api/v1/agents/"+parityAgentID:
		forms, _ := json.Marshal(s.askForms)
		if s.askForms == "" {
			forms = []byte("null")
		}
		_, _ = fmt.Fprintf(w, `{"id":%q,"slug":"viktor","name":"Viktor","ask_forms":%s}`, parityAgentID, forms)
	case p == "/api/v1/agents/"+parityAgentID+"/run-profile" || strings.HasSuffix(p, "/execution-profile"):
		_, _ = w.Write([]byte(`{"mode":"trusted"}`))
	case p == "/api/v1/agents/"+parityAgentID+"/chats" && r.Method == http.MethodPost:
		_, _ = w.Write([]byte(`{"id":"` + parityChatID + `"}`))
	case p == "/api/v1/agents/"+parityAgentID+"/chats":
		if s.chatList != "" {
			_, _ = w.Write([]byte(s.chatList))
			return
		}
		_, _ = w.Write([]byte(`[{"id":"` + parityChatID + `"}]`))
	case strings.HasPrefix(p, "/api/v1/agents/"+parityAgentID+"/chats/") && strings.HasSuffix(p, "/attachments") && r.Method == http.MethodPost:
		chat := strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/agents/"+parityAgentID+"/chats/"), "/attachments")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"bad multipart"}`))
			return
		}
		_, hdr, err := r.FormFile("file")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"no file"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"filename": hdr.Filename, "size": hdr.Size,
			"path":       "attachments/" + chat + "/" + hdr.Filename,
			"agent_path": "/output/viktor/attachments/" + chat + "/" + hdr.Filename,
		})
	case strings.HasPrefix(p, "/api/v1/chats/") && strings.HasSuffix(p, "/messages"):
		_, _ = w.Write([]byte(s.messagesBody))
	case strings.HasPrefix(p, "/api/v1/chats/") && strings.HasSuffix(p, "/stream"):
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(s.streamBody))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"no stub for ` + r.Method + " " + p + `"}`))
	}
}

func (s *parityStub) Frames(typ string) []cli.WSMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []cli.WSMessage
	for _, f := range s.frames {
		if f.Type == typ {
			out = append(out, f)
		}
	}
	return out
}

func (s *parityStub) Requests(method, prefix string) []parityRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []parityRequest
	for _, r := range s.requests {
		if r.Method == method && strings.HasPrefix(r.Path, prefix) {
			out = append(out, r)
		}
	}
	return out
}

// sendEvents writes chat events on the session channel, built from the
// server's own types.
func sendEvents(conn *websocket.Conn, events ...wsproto.ChatEvent) {
	for _, ev := range events {
		b, _ := json.Marshal(wsproto.ServerMessage{Type: "chat_event", Channel: "session:" + parityChatID, Payload: ev})
		_ = websocket.Message.Send(conn, string(b))
	}
}

// sendPayload decodes a send_message payload.
type sentMessage struct {
	SessionID string         `json:"session_id"`
	Content   string         `json:"content"`
	Metadata  map[string]any `json:"metadata"`
}

func decodeSent(t *testing.T, f cli.WSMessage) sentMessage {
	t.Helper()
	var m sentMessage
	if err := json.Unmarshal(f.Payload, &m); err != nil {
		t.Fatalf("decode send payload %s: %v", f.Payload, err)
	}
	return m
}

func (s *parityStub) command(ctx context.Context, args ...string) *exec.Cmd {
	s.t.Helper()
	cfg := filepath.Join(s.t.TempDir(), "cli.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+s.srv.URL+"\nworkspace: ws_parity\ntoken: fake-token\nformat: table\n"), 0o600); err != nil {
		s.t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, buildCrewshipBinary(s.t), args...)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "CREWSHIP_") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+cfg, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
	return cmd
}

// run executes the binary and returns stdout, stderr and the exit error.
func (s *parityStub) run(args ...string) (string, string, error) {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := s.command(ctx, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// ─── chat <id>: the ts bug ──────────────────────────────────────────────

func TestAcceptance_ChatTranscript_ReadsServerTimestamp(t *testing.T) {
	s := newParityStub(t)
	// The shape conversation.Message marshals to: `ts`, RFC 3339 with nanos.
	s.messagesBody = `{"messages":[
	 {"id":"m1","session_id":"chat_parity","role":"user","content":"old question","ts":"2020-01-01T09:00:00.123456789Z"},
	 {"id":"m2","session_id":"chat_parity","role":"assistant","content":"fresh answer","ts":"2099-01-01T09:00:00.5Z"}]}`

	out, errOut, err := s.run("chat", parityChatID, "--no-markdown")
	if err != nil {
		t.Fatalf("chat: %v\n%s", err, errOut)
	}
	if !strings.Contains(out, "2020-01-01 09:00:00") {
		t.Errorf("header carries no time:\n%s", out)
	}

	out, errOut, err = s.run("chat", parityChatID, "--since", "24h", "--format", "json")
	if err != nil {
		t.Fatalf("chat --since: %v\n%s", err, errOut)
	}
	if strings.Contains(out, "old question") || !strings.Contains(out, "fresh answer") {
		t.Errorf("--since did not filter on ts:\n%s", out)
	}
}

// ─── chat stop ──────────────────────────────────────────────────────────

func TestAcceptance_ChatStop_WaitsForTheRunToEnd(t *testing.T) {
	s := newParityStub(t)
	s.onFrame = func(conn *websocket.Conn, f cli.WSMessage) bool {
		if f.Type == "cancel_message" {
			time.Sleep(100 * time.Millisecond)
			sendEvents(conn, wsproto.ChatEvent{Type: "done"})
		}
		return true
	}
	out, errOut, err := s.run("chat", "stop", parityChatID, "--wait", "--format", "json")
	if err != nil {
		t.Fatalf("chat stop --wait: %v\n%s", err, errOut)
	}
	subs := s.Frames("subscribe")
	if len(subs) != 1 || subs[0].Channel != "session:"+parityChatID {
		t.Errorf("subscribe frames = %+v, want one for session:%s before the cancel", subs, parityChatID)
	}
	cancels := s.Frames("cancel_message")
	if len(cancels) != 1 || !strings.Contains(string(cancels[0].Payload), `"session_id":"`+parityChatID+`"`) {
		t.Fatalf("cancel frames = %+v", cancels)
	}
	var res map[string]string
	if json.Unmarshal([]byte(out), &res) != nil || res["status"] != "stopped" || res["chat_id"] != parityChatID {
		t.Errorf("stdout = %q, want {chat_id, status: stopped}", out)
	}
}

func TestAcceptance_ChatStop_WaitTimesOutWithoutARun(t *testing.T) {
	s := newParityStub(t) // never answers the cancel: no run of ours is live
	_, errOut, err := s.run("chat", "stop", parityChatID, "--wait", "--timeout", "500ms")
	if err == nil {
		t.Fatal("an unconfirmed stop must exit non-zero")
	}
	if !strings.Contains(errOut, "no run end was confirmed") {
		t.Errorf("stderr does not say why:\n%s", errOut)
	}
}

func TestAcceptance_ChatStop_WithoutWaitOnlySends(t *testing.T) {
	s := newParityStub(t)
	_, errOut, err := s.run("chat", "stop", parityChatID)
	if err != nil {
		t.Fatalf("chat stop: %v\n%s", err, errOut)
	}
	deadline := time.After(5 * time.Second)
	for len(s.Frames("cancel_message")) == 0 {
		select {
		case <-deadline:
			t.Fatal("no cancel_message reached the server")
		case <-s.gotFrame:
		}
	}
	if len(s.Frames("subscribe")) != 0 {
		t.Error("without --wait nothing needs a subscription")
	}
}

// ─── run: SIGTERM stops like Ctrl-C, and says so only once confirmed ────

func TestAcceptance_Run_SIGTERMStopsTheRunAndWaitsForConfirmation(t *testing.T) {
	s := newParityStub(t)
	s.onFrame = func(conn *websocket.Conn, f cli.WSMessage) bool {
		switch f.Type {
		case "send_message":
			sendEvents(conn, wsproto.ChatEvent{Type: "text", Content: "working on it"})
		case "cancel_message":
			time.Sleep(200 * time.Millisecond)
			sendEvents(conn, wsproto.ChatEvent{Type: "done"})
		}
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := s.command(ctx, "run", "viktor", "a long task")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor := func(typ string) {
		deadline := time.After(20 * time.Second)
		for len(s.Frames(typ)) == 0 {
			select {
			case <-deadline:
				t.Fatalf("no %s frame; stderr:\n%s", typ, stderr.String())
			case <-s.gotFrame:
			}
		}
	}
	waitFor("send_message")
	time.Sleep(200 * time.Millisecond) // let the text frame land
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitFor("cancel_message")
	if err := cmd.Wait(); err != nil {
		t.Fatalf("a confirmed stop exits 0: %v\nstderr:\n%s", err, stderr.String())
	}
	errText := stderr.String()
	stopping := strings.Index(errText, "[stopping")
	cancelled := strings.Index(errText, "[cancelled]")
	if stopping < 0 || cancelled < 0 || cancelled < stopping {
		t.Errorf("want [stopping…] then [cancelled] after the confirmation, got:\n%s", errText)
	}
	if strings.Contains(errText, "[done]") {
		t.Errorf("a stopped run must not report [done]:\n%s", errText)
	}
	if !strings.Contains(stdout.String(), "working on it") {
		t.Errorf("partial output lost: %q", stdout.String())
	}
}

// ─── run|ask --attach, --page, --create-document ────────────────────────

// replyOnce answers the first send_message with a short reply.
func replyOnce(conn *websocket.Conn, f cli.WSMessage) bool {
	if f.Type == "send_message" {
		sendEvents(conn,
			wsproto.ChatEvent{Type: "text", Content: "ok"},
			wsproto.ChatEvent{Type: "result", Metadata: map[string]any{
				"total_cost_usd": 0.0123, "num_turns": 2,
				"usage": map[string]any{"input_tokens": 1200, "output_tokens": 340}}},
			wsproto.ChatEvent{Type: "done"})
		return false
	}
	return true
}

func writeTempFile(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAcceptance_Run_AttachUploadsAndNamesThePaths(t *testing.T) {
	s := newParityStub(t)
	s.onFrame = replyOnce
	a := writeTempFile(t, "notes.txt", "hello")
	b := writeTempFile(t, "diagram.png", "png")

	_, errOut, err := s.run("run", "viktor", "--chat", parityChatID, "--attach", a, "--attach", b, "review these")
	if err != nil {
		t.Fatalf("run --attach: %v\n%s", err, errOut)
	}
	if ups := s.Requests("POST", "/api/v1/agents/"+parityAgentID+"/chats/"+parityChatID+"/attachments"); len(ups) != 2 {
		t.Fatalf("uploads = %d, want 2", len(ups))
	}
	sends := s.Frames("send_message")
	if len(sends) != 1 {
		t.Fatalf("sends = %d", len(sends))
	}
	// lib/attachment-message.ts composeMessageWithAttachments, byte for byte.
	want := "review these\n\nI've attached 2 files to this message. The paths are relative to your working directory:\n\n" +
		"- attachments/" + parityChatID + "/notes.txt\n- attachments/" + parityChatID + "/diagram.png"
	if got := decodeSent(t, sends[0]).Content; got != want {
		t.Errorf("content =\n%q\nwant\n%q", got, want)
	}
	if !strings.Contains(errOut, "[summary]") || !strings.Contains(errOut, "1200 in / 340 out tokens") {
		t.Errorf("run did not show the result summary:\n%s", errOut)
	}
}

func TestAcceptance_Run_AttachOnlyNeedsNoPrompt(t *testing.T) {
	s := newParityStub(t)
	s.onFrame = replyOnce
	a := writeTempFile(t, "photo.jpg", "jpg")
	_, errOut, err := s.run("run", "viktor", "--chat", parityChatID, "--attach", a)
	if err != nil {
		t.Fatalf("run --attach without a prompt: %v\n%s", err, errOut)
	}
	want := "I've attached a file to this message. The path is relative to your working directory:\n\n- attachments/" + parityChatID + "/photo.jpg"
	if got := decodeSent(t, s.Frames("send_message")[0]).Content; got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestAcceptance_Run_AttachMissingFileFailsBeforeAnything(t *testing.T) {
	s := newParityStub(t)
	_, errOut, err := s.run("run", "viktor", "--attach", filepath.Join(t.TempDir(), "nope.txt"), "hi")
	if err == nil || !strings.Contains(errOut, "nope.txt") {
		t.Fatalf("want a refusal naming the file, got %v\n%s", err, errOut)
	}
	if n := len(s.Requests("POST", "/api/v1/agents/"+parityAgentID+"/chats")); n != 0 {
		t.Errorf("a bad --attach created %d chats", n)
	}
}

func TestAcceptance_Ask_AttachUploadsIntoTheNewChat(t *testing.T) {
	s := newParityStub(t)
	s.onFrame = replyOnce
	a := writeTempFile(t, "log.txt", "x")
	_, errOut, err := s.run("ask", "--agent", "viktor", "--attach", a, "what failed?")
	if err != nil {
		t.Fatalf("ask --attach: %v\n%s", err, errOut)
	}
	if ups := s.Requests("POST", "/api/v1/agents/"+parityAgentID+"/chats/"+parityChatID+"/attachments"); len(ups) != 1 {
		t.Fatalf("uploads = %d, want 1 into the chat ask created", len(ups))
	}
	if got := decodeSent(t, s.Frames("send_message")[0]).Content; !strings.HasPrefix(got, "what failed?\n\nI've attached a file") {
		t.Errorf("content = %q", got)
	}
}

func TestAcceptance_Run_PageSendsOnlyTheSlug(t *testing.T) {
	s := newParityStub(t)
	s.onFrame = replyOnce
	_, errOut, err := s.run("run", "viktor", "--chat", parityChatID, "--page", "fleet", "summarise this page")
	if err != nil {
		t.Fatalf("run --page: %v\n%s", err, errOut)
	}
	m := decodeSent(t, s.Frames("send_message")[0])
	if m.Content != "summarise this page" {
		t.Errorf("content changed: %q (the server appends the snapshot, not the client)", m.Content)
	}
	page, _ := m.Metadata["page_context"].(map[string]any)
	if len(page) != 1 || page["slug"] != "fleet" {
		t.Errorf("page_context = %v, want exactly {slug: fleet} (chatbridge.requestedPageSlug refuses anything else)", m.Metadata["page_context"])
	}
}

func TestAcceptance_Run_CreateDocumentSendsTheComposerIntent(t *testing.T) {
	s := newParityStub(t)
	s.onFrame = replyOnce
	_, errOut, err := s.run("run", "viktor", "--chat", parityChatID, "--create-document", "write up the incident")
	if err != nil {
		t.Fatalf("run --create-document: %v\n%s", err, errOut)
	}
	m := decodeSent(t, s.Frames("send_message")[0])
	intent, _ := m.Metadata["document_creation_intent"].(map[string]any)
	id, _ := intent["submission_id"].(string)
	if len(intent) != 2 || id == "" || intent["drafting"] != true {
		t.Errorf("document_creation_intent = %v, want {submission_id, drafting: true}", m.Metadata)
	}
	// The server's own parser must accept it.
	if _, err := documents.CreationIntentFromMetadata(m.Metadata); err != nil {
		t.Errorf("server refuses the intent: %v", err)
	}
}

func TestAcceptance_Run_ExtrasRefuseInteractive(t *testing.T) {
	s := newParityStub(t)
	_, errOut, err := s.run("run", "viktor", "--interactive", "--page", "fleet")
	if err == nil || !strings.Contains(errOut, "--interactive") {
		t.Fatalf("want refusal, got %v\n%s", err, errOut)
	}
}

// ─── run --ask-form ─────────────────────────────────────────────────────

const parityAskForms = `[{
	"id": "receipt",
	"label": "Add a receipt",
	"attachment": "optional",
	"template": "Please file this receipt.\n\nSupplier: {{supplier}}\nAmount: {{amount}} {{amount_currency}}\nCategory: {{category}}\nDocument: {{document}}",
	"fields": [
		{"name":"supplier","label":"Supplier","type":"text","required":true},
		{"name":"amount","label":"Amount","type":"money","required":true,"currency":["CZK","EUR"]},
		{"name":"category","label":"Category","type":"select","options":["Telco","Hosting"]},
		{"name":"document","label":"Document","type":"file"}
	]
}]`

func TestAcceptance_Run_AskFormPassesTheServersCheck(t *testing.T) {
	s := newParityStub(t)
	s.askForms = parityAskForms
	var checkErr error
	var checked bool
	s.onFrame = func(conn *websocket.Conn, f cli.WSMessage) bool {
		if f.Type != "send_message" {
			return true
		}
		var m sentMessage
		_ = json.Unmarshal(f.Payload, &m)
		// The server's own admission rule, not a copy of it.
		checkErr = chatbridge.CheckAskSubmission(parityAskForms, m.SessionID, m.Content, m.Metadata)
		checked = true
		env, _ := m.Metadata["ask_submission"].(map[string]any)
		sendEvents(conn,
			wsproto.ChatEvent{Type: "user_message", Content: m.Content, Metadata: map[string]any{"ask_submission_id": env["submission_id"]}},
			wsproto.ChatEvent{Type: "text", Content: "Filed."},
			wsproto.ChatEvent{Type: "done"})
		return false
	}
	doc := writeTempFile(t, "IMG_4821.heic", "img")
	extra := writeTempFile(t, "contract.pdf", "pdf")
	out, errOut, err := s.run("run", "viktor", "--chat", parityChatID, "--ask-form", "receipt",
		"--var", "supplier=Vodafone", "--var", "amount=1249", "--var", "amount_currency=CZK",
		"--var", "category=Telco", "--var", "document="+doc, "--attach", extra)
	if err != nil {
		t.Fatalf("run --ask-form: %v\n%s", err, errOut)
	}
	if !checked {
		t.Fatal("no send reached the server")
	}
	if checkErr != nil {
		t.Fatalf("the server would refuse this submission: %v", checkErr)
	}
	if !strings.Contains(errOut, "[form delivered]") {
		t.Errorf("no delivery confirmation shown:\n%s", errOut)
	}
	if !strings.Contains(out, "Filed.") {
		t.Errorf("reply missing: %q", out)
	}
	if ups := s.Requests("POST", "/api/v1/agents/"+parityAgentID+"/chats/"+parityChatID+"/attachments"); len(ups) != 2 {
		t.Errorf("uploads = %d, want the field file and the --attach file", len(ups))
	}
}

func TestAcceptance_Run_AskFormRefusalExitsNonZero(t *testing.T) {
	s := newParityStub(t)
	s.askForms = parityAskForms
	s.onFrame = func(conn *websocket.Conn, f cli.WSMessage) bool {
		if f.Type == "send_message" {
			// Sender-only and never followed by done (ws/ask_submission.go).
			sendEvents(conn, wsproto.ChatEvent{Type: "ask_submission_error", Content: "Form has changed — reopen the form and try again.",
				Metadata: map[string]any{"submission_id": "x", "ask_errors": nil}})
		}
		return true
	}
	_, errOut, err := s.run("run", "viktor", "--chat", parityChatID, "--ask-form", "receipt",
		"--var", "supplier=Vodafone", "--var", "amount=10", "--var", "amount_currency=EUR")
	if err == nil {
		t.Fatal("a refused form must exit non-zero")
	}
	if !strings.Contains(errOut, "Form has changed") {
		t.Errorf("refusal reason not shown:\n%s", errOut)
	}
}

func TestAcceptance_Run_AskFormValidatesLocally(t *testing.T) {
	s := newParityStub(t)
	s.askForms = parityAskForms
	_, errOut, err := s.run("run", "viktor", "--chat", parityChatID, "--ask-form", "receipt", "--var", "category=Telco")
	if err == nil || !strings.Contains(errOut, "supplier") {
		t.Fatalf("missing required answers must be refused before sending: %v\n%s", err, errOut)
	}
	if n := len(s.Frames("send_message")); n != 0 {
		t.Errorf("an invalid form was sent %d times", n)
	}
}

// ─── chat stream: the events it used to drop ───────────────────────────

func TestAcceptance_ChatStream_RendersBroadcastsAndSummaries(t *testing.T) {
	s := newParityStub(t)
	s.streamBody = strings.Join([]string{
		`{"type":"stream.open","chat_id":"chat_parity","active":true,"from_seq":0}`,
		`{"type":"run_begin","seq":1}`,
		`{"type":"user_message","seq":2,"content":"please delegate","metadata":{"author_user_id":"u_ana"}}`,
		`{"type":"assignment_created","metadata":{"id":"as_1","target":"lucy","task":"collect invoices"}}`,
		`{"type":"escalation_created","metadata":{"id":"es_1","from":"lucy","reason":"need the bank login"}}`,
		`{"type":"port_expose_created","metadata":{"id":"pe_1","url":"https://p.example.invalid","expires_at":"2026-10-30T00:00:00Z"}}`,
		`{"type":"text","seq":3,"content":"delegated"}`,
		`{"type":"result","seq":4,"metadata":{"total_cost_usd":0.5,"usage":{"input_tokens":10,"output_tokens":20}}}`,
		`{"type":"done","seq":5}`,
		`{"type":"stream.end","reason":"run_complete","last_seq":5}`,
	}, "\n") + "\n"
	out, errOut, err := s.run("chat", "stream", parityChatID)
	if err != nil {
		t.Fatalf("chat stream: %v\n%s", err, errOut)
	}
	for _, want := range []string{
		"[user] u_ana: please delegate",
		"[delegated] → lucy · collect invoices",
		"[escalation] from lucy · need the bank login",
		"[port exposed] https://p.example.invalid",
		"[summary] $0.5000 · 10 in / 20 out tokens",
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut)
		}
	}
	if strings.TrimSpace(out) != "delegated" {
		t.Errorf("stdout must stay the reply only, got %q", out)
	}
}
