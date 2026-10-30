//go:build unix

package main

// Acceptance for the read/watch half of the chat-parity commands (#4504):
// transcript parts, export, retry/edit, --watch, room --follow and the list
// and search flags. Harness: acceptance_chat_parity_test.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/crewship-ai/crewship/internal/cli"
	wsproto "github.com/crewship-ai/crewship/internal/ws"
)

// ─── chat <id> --parts, chat export ─────────────────────────────────────

const parityPartsMessages = `{"messages":[
 {"id":"m1","session_id":"chat_parity","agent_id":"ag_parity","role":"user","content":"why is CI red?","author_user_id":"u_ana","ts":"2026-10-01T09:00:00Z"},
 {"id":"m2","session_id":"chat_parity","agent_id":"ag_parity","role":"assistant","content":"The lint job fails.","ts":"2026-10-01T09:00:05Z",
  "parts":[{"type":"thinking","content":"check the workflow first"},
           {"type":"tool_call","tool_name":"Bash","tool_id":"t1","content":"gh run view 42"},
           {"type":"tool_result","tool_id":"t1","content":"golangci-lint: 1 issue"},
           {"type":"text","content":"The lint job fails."}]}]}`

func TestAcceptance_ChatTranscript_PartsShowsToolsAndAuthor(t *testing.T) {
	s := newChatParityStub(t)
	s.messagesBody = parityPartsMessages
	out, errOut, err := s.run("chat", parityChatID, "--parts", "--no-markdown")
	if err != nil {
		t.Fatalf("chat --parts: %v\n%s", err, errOut)
	}
	for _, want := range []string{"user (u_ana)", "[thinking] check the workflow first", "[tool Bash] gh run view 42", "[result] golangci-lint: 1 issue", "The lint job fails."} {
		if !strings.Contains(out, want) {
			t.Errorf("--parts output missing %q:\n%s", want, out)
		}
	}
	plain, _, _ := s.run("chat", parityChatID, "--no-markdown")
	if strings.Contains(plain, "[tool Bash]") {
		t.Errorf("without --parts the transcript stays flattened:\n%s", plain)
	}
}

func TestAcceptance_ChatExport_WritesTheExportDialogMarkdown(t *testing.T) {
	s := newChatParityStub(t)
	s.messagesBody = parityPartsMessages
	dest := filepath.Join(t.TempDir(), "chat.md")
	_, errOut, err := s.run("chat", "export", parityChatID, "-o", dest)
	if err != nil {
		t.Fatalf("chat export: %v\n%s", err, errOut)
	}
	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	// export-dialog.tsx turnsToMarkdown, section by section.
	for _, want := range []string{
		"# Conversation with Viktor\nExported ",
		"## You\nwhy is CI red?\n",
		"## Viktor\n> _Thinking:_ check the workflow first\n> _Tool:_ `Bash`\nThe lint job fails.\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("export missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "golangci-lint: 1 issue") {
		t.Errorf("the dialog does not export tool results:\n%s", got)
	}
}

// ─── chat retry / chat edit ─────────────────────────────────────────────

func TestAcceptance_ChatRetry_ResendsWithoutTheOldPageSnapshot(t *testing.T) {
	s := newChatParityStub(t)
	snapshot := "\n\n[Page context — untrusted reference; snapshot 2026-10-01T09:00:00Z]\nPage: Fleet\nSlug: fleet\nPage ID: pg_1\n[/Page context]"
	msgs := map[string]any{"messages": []map[string]any{
		{"id": "m1", "agent_id": parityAgentID, "role": "user", "author_user_id": "caller", "content": "first", "ts": "2026-10-01T08:00:00Z"},
		{"id": "m2", "agent_id": parityAgentID, "role": "assistant", "content": "answer", "ts": "2026-10-01T08:00:01Z"},
		{"id": "m3", "agent_id": parityAgentID, "role": "user", "author_user_id": "caller", "content": "summarise this page" + snapshot, "ts": "2026-10-01T09:00:00Z",
			"metadata": map[string]any{"page_context": map[string]any{"slug": "fleet", "page_id": "pg_1", "snapshot_at": "2026-10-01T09:00:00Z", "name": "Fleet"}}},
		{"id": "m4", "agent_id": parityAgentID, "role": "assistant", "content": "a bad answer", "ts": "2026-10-01T09:00:05Z"},
	}}
	b, _ := json.Marshal(msgs)
	s.messagesBody = string(b)
	s.onFrame = replyOnce
	_, errOut, err := s.run("chat", "retry", parityChatID)
	if err != nil {
		t.Fatalf("chat retry: %v\n%s", err, errOut)
	}
	sends := s.Frames("send_message")
	if len(sends) != 1 || sends[0].Channel != "agent:"+parityAgentID {
		t.Fatalf("sends = %+v", sends)
	}
	m := decodeSent(t, sends[0])
	if m.Content != "summarise this page" {
		t.Errorf("content = %q, want the typed text without the server's snapshot", m.Content)
	}
	if page, _ := m.Metadata["page_context"].(map[string]any); len(page) != 1 || page["slug"] != "fleet" {
		t.Errorf("metadata = %v, want the Page requested again by slug only", m.Metadata)
	}
}

func TestAcceptance_ChatEdit_SendsReplacementTextWithoutMetadata(t *testing.T) {
	s := newChatParityStub(t)
	s.messagesBody = `{"messages":[{"id":"m1","agent_id":"ag_parity","role":"user","content":"use prod","ts":"2026-10-01T08:00:00Z"}]}`
	s.onFrame = replyOnce
	_, errOut, err := s.run("chat", "edit", parityChatID, "-m", "use staging, not prod")
	if err != nil {
		t.Fatalf("chat edit: %v\n%s", err, errOut)
	}
	m := decodeSent(t, s.Frames("send_message")[0])
	if m.Content != "use staging, not prod" || m.Metadata != nil {
		t.Errorf("sent %+v, want the replacement text and no metadata", m)
	}
}

// ─── --watch and --follow ───────────────────────────────────────────────

// runUntil starts the binary, waits until stdout contains want n times, then
// interrupts it and returns its output and exit error.
func (s *chatParityStub) runUntil(t *testing.T, want string, n int, args ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := s.command(ctx, args...)
	var stdout, stderr syncBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for strings.Count(stdout.String(), want) < n {
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("saw %q %d times, want %d\nstdout:\n%s\nstderr:\n%s", want, strings.Count(stdout.String(), want), n, stdout.String(), stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = cmd.Process.Signal(syscall.SIGINT)
	err := cmd.Wait()
	return stdout.String(), stderr.String(), err
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// hintOnSubscribe answers a chat-hints subscription with one invalidation.
func hintOnSubscribe(conn *websocket.Conn, f cli.WSMessage) bool {
	if f.Type == "subscribe" && strings.HasPrefix(f.Channel, "chat-hints:") {
		time.Sleep(100 * time.Millisecond)
		b, _ := json.Marshal(wsproto.ServerMessage{Type: "chat.entity_refs_changed", Channel: f.Channel, Payload: map[string]int{"contract_version": 1}})
		_ = websocket.Message.Send(conn, string(b))
	}
	return true
}

func TestAcceptance_ChatWatch_RereadsOnChatHints(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		path string
	}{
		{"refs", []string{"chat", "refs", parityChatID, "--watch"}, "/api/v1/chats/" + parityChatID + "/entity-refs"},
		{"decisions", []string{"chat", "decision", "list", parityChatID, "--watch"}, "/api/v1/chats/" + parityChatID + "/decisions"},
		{"proposals", []string{"chat", "proposal", "list", parityChatID, "--watch"}, "/api/v1/chats/" + parityChatID + "/proposals"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newChatParityStub(t)
			s.routes["GET "+tc.path] = func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"refs":[],"decisions":[],"proposals":[],"marker":"page"}`))
			}
			s.onFrame = hintOnSubscribe
			_, errOut, err := s.runUntil(t, `"marker"`, 2, tc.args...)
			if err != nil {
				t.Fatalf("an interrupted watch exits 0: %v\n%s", err, errOut)
			}
			if subs := s.Frames("subscribe"); len(subs) == 0 || subs[0].Channel != "chat-hints:"+parityChatID {
				t.Errorf("subscribed to %+v, want chat-hints:%s", subs, parityChatID)
			}
			if n := len(s.Requests("GET", tc.path)); n < 2 {
				t.Errorf("list read %d times, want a re-read after the hint", n)
			}
		})
	}
}

func TestAcceptance_ChatRoomMessages_FollowPrintsNewMessages(t *testing.T) {
	s := newChatParityStub(t)
	const room = "conv_parity"
	var mu sync.Mutex
	delivered := false
	s.routes["GET /api/v1/auth/cli-token/validate"] = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"valid":true,"user_id":"u_me"}`))
	}
	s.routes["GET "+chatRoomBase+"/"+room+"/messages"] = func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		after := r.URL.Query().Get("after_sequence")
		switch {
		case after == "":
			_, _ = w.Write([]byte(`{"messages":[{"id":"m1","conversation_id":"conv_parity","sequence":1,"author_name":"Ana","content":"standup in 5"}],"has_more":false}`))
		case after == "1" && delivered:
			_, _ = w.Write([]byte(`{"messages":[{"id":"m2","conversation_id":"conv_parity","sequence":2,"author_name":"Bob","content":"joining"}],"has_more":false}`))
		default:
			_, _ = w.Write([]byte(`{"messages":[],"has_more":false}`))
		}
	}
	s.onFrame = func(conn *websocket.Conn, f cli.WSMessage) bool {
		if f.Type == "subscribe" && f.Channel == "user:u_me" {
			time.Sleep(100 * time.Millisecond)
			mu.Lock()
			delivered = true
			mu.Unlock()
			b, _ := json.Marshal(wsproto.ServerMessage{Type: "conversation.updated", Channel: "user:u_me", Payload: map[string]string{"conversation_id": room}})
			_ = websocket.Message.Send(conn, string(b))
		}
		return true
	}
	out, errOut, err := s.runUntil(t, "joining", 1, "chat", "room", "messages", room, "--follow")
	if err != nil {
		t.Fatalf("interrupted follow exits 0: %v\n%s", err, errOut)
	}
	if !strings.Contains(out, "#1 Ana: standup in 5") || !strings.Contains(out, "#2 Bob: joining") {
		t.Errorf("follow output:\n%s", out)
	}
}

// ─── minor list/search flags ────────────────────────────────────────────

func TestAcceptance_ConversationSearch_ClientFilters(t *testing.T) {
	s := newChatParityStub(t)
	s.routes["POST /api/v1/conversations/search"] = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"count":3,"query":"deploy","scope":"workspace","hits":[
		 {"id":"h1","session_id":"c_a","agent_slug":"viktor","agent_name":"Viktor","role":"assistant","content":"see https://ci.example.invalid/42","ts":"2026-10-05T10:00:00Z"},
		 {"id":"h2","session_id":"c_a","agent_slug":"viktor","role":"user","content":"deploy https://x.example.invalid","ts":"2026-10-05T10:00:00Z"},
		 {"id":"h3","session_id":"c_b","agent_slug":"viktor","role":"assistant","content":"deploy done","ts":"2026-09-01T10:00:00Z"}]}`))
	}
	out, errOut, err := s.run("conversation", "search", "deploy", "--limit", "3", "--from", "agent", "--has", "link", "--after", "2026-10-01", "--chat", "c_a", "--format", "json")
	if err != nil {
		t.Fatalf("search: %v\n%s", err, errOut)
	}
	var res struct {
		Count   int  `json:"count"`
		Partial bool `json:"partial"`
		Hits    []struct {
			ID string `json:"id"`
		} `json:"hits"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if res.Count != 1 || len(res.Hits) != 1 || res.Hits[0].ID != "h1" || !res.Partial {
		t.Errorf("filtered result = %+v, want only h1, marked partial (full server page)", res)
	}
	if body := string(s.Requests("POST", "/api/v1/conversations/search")[0].Body); strings.Contains(body, "c_a") {
		t.Errorf("client-side filters leaked into the request the server cannot read: %s", body)
	}
	if _, errOut, err := s.run("conversation", "search", "deploy", "--from", "me"); err == nil || !strings.Contains(errOut, "author") {
		t.Errorf("--from me must be refused like the search box does: %v %s", err, errOut)
	}
}

func TestAcceptance_ChatList_CountsReadsTheKindHeader(t *testing.T) {
	s := newChatParityStub(t)
	s.routes["GET /api/v1/agents/"+parityAgentID+"/chats"] = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("counts") == "1" {
			w.Header().Set("X-Chat-Kind-Counts", "direct=3,routine=150,issue=2,agent=0")
		}
		_, _ = w.Write([]byte(`[{"id":"c1","kind":"direct","status":"ACTIVE","message_count":1,"started_at":"2026-10-01T00:00:00Z"}]`))
	}
	out, errOut, err := s.run("chat", "list", "viktor", "--counts")
	if err != nil {
		t.Fatalf("chat list --counts: %v\n%s", err, errOut)
	}
	if q := s.Requests("GET", "/api/v1/agents/"+parityAgentID+"/chats"); len(q) == 0 || !strings.Contains(q[len(q)-1].Query, "counts=1") {
		t.Errorf("counts=1 not sent: %+v", q)
	}
	if !strings.Contains(out, "totals: direct 3 · routine 150 · issue 2 · agent 0") {
		t.Errorf("totals line missing:\n%s", out)
	}
	out, _, _ = s.run("chat", "list", "viktor", "--counts", "--format", "json")
	var wrapped struct {
		KindCounts map[string]int `json:"kind_counts"`
	}
	if json.Unmarshal([]byte(out), &wrapped) != nil || wrapped.KindCounts["routine"] != 150 {
		t.Errorf("json output = %s", out)
	}
}

func TestAcceptance_CrewPeerConversations_SendsOffset(t *testing.T) {
	s := newChatParityStub(t)
	s.routes["GET /api/v1/crews"] = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"crew_1","slug":"ops","name":"Ops"}]`))
	}
	s.routes["GET /api/v1/crews/crew_1/peer-conversations"] = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}
	if _, errOut, err := s.run("crew", "peer-conversations", "ops", "--limit", "10", "--offset", "20"); err != nil {
		t.Fatalf("peer-conversations: %v\n%s", err, errOut)
	}
	q := s.Requests("GET", "/api/v1/crews/crew_1/peer-conversations")
	if len(q) != 1 || !strings.Contains(q[0].Query, "offset=20") || !strings.Contains(q[0].Query, "limit=10") {
		t.Errorf("query = %+v, want limit=10&offset=20", q)
	}
}

func TestAcceptance_ChatWatch_RefusedSubscriptionExitsInsteadOfRetrying(t *testing.T) {
	s := newChatParityStub(t)
	s.routes["GET /api/v1/chats/"+parityChatID+"/entity-refs"] = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"refs":[]}`))
	}
	s.onFrame = func(conn *websocket.Conn, f cli.WSMessage) bool {
		if f.Type == "subscribe" {
			// What ws.Client.subscribe sends on an authorizer denial.
			b, _ := json.Marshal(wsproto.ServerMessage{Type: "error", Channel: f.Channel, Payload: map[string]string{"message": "access denied", "error": "access denied"}})
			_ = websocket.Message.Send(conn, string(b))
		}
		return true
	}
	_, errOut, err := s.run("chat", "refs", parityChatID, "--watch")
	if err == nil || !strings.Contains(errOut, "access denied") {
		t.Fatalf("a denied subscription must end the watch with the reason: %v\n%s", err, errOut)
	}
	if n := len(s.Frames("subscribe")); n != 1 {
		t.Errorf("subscribed %d times; a refusal is not retried", n)
	}
}
