package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"golang.org/x/net/websocket"

	"github.com/crewship-ai/crewship/internal/cli"
)

// artifactRunServer answers `crewship run --artifact` the way the chat bridge
// does (internal/chatbridge/artifact_refs.go, bridge.go): with the workspace
// flag off the key is ignored; on, the metadata must be exactly
// [{"page_id": id}] (1-10, unique), every id is checked as the human would read
// it, a refusal is one sentence, and only a stored message records rows.
// The pages API only answers a page the human can read. The recorded set stands
// in for the artifact_message_refs table.
type artifactRunServer struct {
	srv      *httptest.Server
	mu       sync.Mutex
	flagOn   bool
	requests []string
	sends    []map[string]any
	recorded []string
	// readable maps a slug to its page id; lost ids resolve but are refused at send.
	readable map[string]string
	lost     map[string]bool
}

var artifactRunRefID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func newArtifactRunServer(t *testing.T, flagOn bool) *artifactRunServer {
	t.Helper()
	a := &artifactRunServer{flagOn: flagOn, readable: map[string]string{"notes": "pg_notes1", "plan": "pg_plan1", "gone": "pg_lost1"}, lost: map[string]bool{"pg_lost1": true}}
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.requests = append(a.requests, r.Method+" "+r.URL.Path)
		a.mu.Unlock()
		switch {
		case r.URL.Path == "/api/v1/agents":
			reply(w, 200, `[{"id":"agent-src","slug":"viktor"}]`)
		case r.Method == "POST" && r.URL.Path == "/api/v1/agents/agent-src/chats":
			reply(w, 201, `{"id":"chat-new"}`)
		case r.URL.Path == "/api/v1/chats/chat-new/execution-profile", r.URL.Path == "/api/v1/agents/agent-src/run-profile":
			reply(w, 200, `{"mode":"trusted"}`)
		case r.URL.Path == "/api/v1/auth/cli-token/validate":
			reply(w, 200, `{"user_id":"user-1"}`)
		case r.URL.Path == "/api/v1/ws-token":
			reply(w, 200, `{"token":"ws-tok"}`)
		case strings.HasPrefix(r.URL.Path, "/api/v1/pages/"):
			ref := strings.TrimPrefix(r.URL.Path, "/api/v1/pages/")
			if strings.HasPrefix(ref, "@") {
				for slug, id := range a.readable {
					if "@"+id == ref {
						ref = slug
					}
				}
			}
			if id, ok := a.readable[ref]; ok {
				reply(w, 200, `{"id":"`+id+`","slug":"`+ref+`"}`)
				return
			}
			reply(w, 404, `{"error":"page not found"}`)
		default:
			reply(w, 404, `{"error":"unexpected `+r.Method+" "+r.URL.Path+`"}`)
		}
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
			var sent struct {
				Metadata map[string]any `json:"metadata"`
			}
			_ = json.Unmarshal(frame.Payload, &sent)
			a.mu.Lock()
			a.sends = append(a.sends, sent.Metadata)
			ids, refused := a.admit(sent.Metadata)
			if !refused {
				a.recorded = append(a.recorded, ids...)
			}
			a.mu.Unlock()
			if refused {
				_ = sendScriptedEvents(conn, []cli.ChatEventPayload{{Type: "error", Content: "An attached artifact is not available"}})
				continue
			}
			_ = sendScriptedEvents(conn, []cli.ChatEventPayload{{Type: "text", Content: "ok"}, {Type: "done"}})
		}
	}))
	a.srv = httptest.NewServer(mux)
	t.Cleanup(a.srv.Close)
	return a
}

// admit is the bridge's decision for one send.
func (a *artifactRunServer) admit(md map[string]any) (ids []string, refused bool) {
	raw, present := md["artifact_refs"]
	if !present || !a.flagOn {
		return nil, false
	}
	list, ok := raw.([]any)
	if !ok || len(list) == 0 || len(list) > 10 {
		return nil, true
	}
	seen := map[string]bool{}
	for _, item := range list {
		obj, ok := item.(map[string]any)
		if !ok || len(obj) != 1 {
			return nil, true
		}
		id, ok := obj["page_id"].(string)
		if !ok || !artifactRunRefID.MatchString(id) || seen[id] || a.lost[id] {
			return nil, true
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, false
}

func (a *artifactRunServer) count() (requests int, sends int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.requests), len(a.sends)
}

func TestAcceptance_RunArtifactRecordsTheReferenceByPageID(t *testing.T) {
	a := newArtifactRunServer(t, true)
	out, code := runArtifactBinary(t, a.srv, "run", "viktor", "--no-stream", "--artifact", "notes", "--artifact", "@pg_plan1", "--artifact", "notes", "read these")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.sends) != 1 {
		t.Fatalf("sends = %d", len(a.sends))
	}
	want := `[{"page_id":"pg_notes1"},{"page_id":"pg_plan1"}]`
	if got, _ := json.Marshal(a.sends[0]["artifact_refs"]); string(got) != want {
		t.Errorf("artifact_refs = %s, want %s (ids, deduplicated, nothing else)", got, want)
	}
	if strings.Join(a.recorded, ",") != "pg_notes1,pg_plan1" {
		t.Errorf("recorded = %v", a.recorded)
	}
}

func TestAcceptance_RunArtifactUnreadableAndRefusedReadTheSame(t *testing.T) {
	a := newArtifactRunServer(t, true)
	// Not readable at all: the CLI stops before a chat exists.
	out, code := runArtifactBinary(t, a.srv, "run", "viktor", "--no-stream", "--artifact", "secret-page", "hi")
	if code == 0 || !strings.Contains(out, "an attached artifact is not available") || strings.Contains(out, "secret-page") {
		t.Fatalf("unreadable: exit %d: %s", code, out)
	}
	if _, sends := a.count(); sends != 0 {
		t.Errorf("a refused reference was sent")
	}
	// Resolves, but the server refuses it at send: the same sentence.
	out2, code2 := runArtifactBinary(t, a.srv, "run", "viktor", "--no-stream", "--artifact", "gone", "hi")
	if code2 == 0 || !strings.Contains(strings.ToLower(out2), "an attached artifact is not available") {
		t.Fatalf("server refusal: exit %d: %s", code2, out2)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.recorded) != 0 {
		t.Errorf("recorded = %v, want none", a.recorded)
	}
}

func TestAcceptance_RunArtifactFlagOffRecordsNothingAndRuns(t *testing.T) {
	a := newArtifactRunServer(t, false)
	out, code := runArtifactBinary(t, a.srv, "run", "viktor", "--no-stream", "--artifact", "notes", "hi")
	if code != 0 || !strings.Contains(out, "ok") {
		t.Fatalf("exit %d: %s", code, out)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.sends) != 1 || len(a.recorded) != 0 {
		t.Errorf("sends=%d recorded=%v, want one send and no rows", len(a.sends), a.recorded)
	}
}

func TestAcceptance_RunArtifactRefusedClientSideWithExit2(t *testing.T) {
	a := newArtifactRunServer(t, true)
	many := []string{"run", "viktor", "--no-stream", "hi"}
	for i := 0; i < 11; i++ {
		many = append(many, "--artifact", "notes")
	}
	cases := map[string][]string{
		"more than 10":     many,
		"empty":            {"run", "viktor", "--artifact", "  ", "hi"},
		"with page":        {"run", "viktor", "--artifact", "notes", "--page", "plan", "hi"},
		"with create":      {"run", "viktor", "--artifact", "notes", "--create-document", "hi"},
		"with ask-form":    {"run", "viktor", "--chat", "c1", "--artifact", "notes", "--ask-form", "receipt"},
		"with interactive": {"run", "viktor", "--artifact", "notes", "--interactive"},
	}
	for name, args := range cases {
		out, code := runArtifactBinary(t, a.srv, args...)
		if code != 2 {
			t.Errorf("%s: exit %d, want 2: %s", name, code, out)
		}
	}
	// The agent lookup before flag validation is older than this flag; what a
	// refusal must never reach is a page read, a chat or a send.
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, r := range a.requests {
		if strings.HasPrefix(r, "POST ") || strings.Contains(r, "/pages/") {
			t.Errorf("a refused invocation made request %s", r)
		}
	}
	if len(a.sends) != 0 {
		t.Errorf("a refused invocation sent %d messages", len(a.sends))
	}
}
