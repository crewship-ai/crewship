package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The server recognises a mention only as the Markdown link
// `[@slug](crewship:agent/<id>)` (internal/mentions). A bare "@riley" typed
// into `issue comment` is plain text and mentions nobody — seen live on dev1
// (#2313). `--mention <slug>` resolves the slug and writes the link form, so
// an agent driving the CLI can wake another agent at all.
type issueCommentMentionStub struct {
	mu     sync.Mutex
	bodies []string
}

func (s *issueCommentMentionStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/agents"):
			_, _ = io.WriteString(w, `[{"id":"ag_riley_01","slug":"riley","name":"Riley"},{"id":"ag_morgan_01","slug":"morgan","name":"Morgan"}]`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			raw, _ := io.ReadAll(r.Body)
			var req struct {
				Body string `json:"body"`
			}
			_ = json.Unmarshal(raw, &req)
			s.mu.Lock()
			s.bodies = append(s.bodies, req.Body)
			s.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":"cmt_1","body":`+string(mentionBodyJSON(req.Body))+`}`)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues/"):
			_, _ = io.WriteString(w, `{"id":"iss_1","identifier":"ENG-1","title":"t","status":"BACKLOG","priority":"none","crew_id":"crew_1","crew_slug":"eng","mission_type":"issue","created_at":"2026-09-01T00:00:00Z","updated_at":"2026-09-01T00:00:00Z","labels":[]}`)
		default:
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func mentionBodyJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func TestAcceptance_IssueComment_MentionFlag_WritesTheLinkForm(t *testing.T) {
	stub := &issueCommentMentionStub{}
	srv := stub.start(t)

	out, err := runIssueRunsCLI(t, srv.URL, "issue", "comment", "ENG-1", "--mention", "riley", "--mention", "@morgan", "please look")
	if err != nil {
		t.Fatalf("issue comment --mention: %v\noutput: %s", err, out)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.bodies) != 1 {
		t.Fatalf("comments posted = %d, want 1", len(stub.bodies))
	}
	want := "[@riley](crewship:agent/ag_riley_01) [@morgan](crewship:agent/ag_morgan_01) please look"
	if stub.bodies[0] != want {
		t.Errorf("posted body:\n  got  %q\n  want %q", stub.bodies[0], want)
	}
}

func TestAcceptance_IssueComment_MentionFlag_UnknownSlugFailsLoudly(t *testing.T) {
	stub := &issueCommentMentionStub{}
	srv := stub.start(t)

	out, err := runIssueRunsCLI(t, srv.URL, "issue", "comment", "ENG-1", "--mention", "nobody", "hello")
	if err == nil {
		t.Fatalf("want a non-zero exit for an unknown slug, got none\noutput: %s", out)
	}
	if !strings.Contains(out, "nobody") {
		t.Errorf("error must name the slug that did not resolve:\n%s", out)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.bodies) != 0 {
		t.Errorf("a comment was posted despite the unresolved mention: %q", stub.bodies)
	}
}

func TestAcceptance_IssueComment_BareAtSlugIsPlainText(t *testing.T) {
	stub := &issueCommentMentionStub{}
	srv := stub.start(t)

	out, err := runIssueRunsCLI(t, srv.URL, "issue", "comment", "ENG-1", "@riley hello")
	if err != nil {
		t.Fatalf("issue comment: %v\noutput: %s", err, out)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.bodies) != 1 || stub.bodies[0] != "@riley hello" {
		t.Errorf("a bare @slug must be sent through untouched, got %q", stub.bodies)
	}
}
