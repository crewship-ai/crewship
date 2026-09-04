package main

// Acceptance for `crewship chat list --kind`, driven through the BUILT BINARY.
//
// The project rule ("every API endpoint gets a CLI command, and its acceptance
// test drives the CLI binary") plus one specific hazard this flag has: a
// filter's whole value is that it reaches the SERVER. `?kind=` narrows inside
// the statement, before `LIMIT` — a client that registers the flag, prints a
// filtered table and never sends the parameter looks identical in a unit test
// and is broken against every real server, because the page it filtered was
// already full of routine rows.
//
// So the stub asserts on the raw query string, and the fixture is the shape
// that motivated the flag: one conversation and a wall of routine steps.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type chatListStub struct {
	mu      sync.Mutex
	queries []string
	body    string
	// total, when set, is published as X-Total-Count the way the server does.
	total int
	// agentQueries records how the roster was asked for — the resolver must
	// ask with include_setup=1 or the Guide's chats are unaddressable.
	agentQueries []string
}

func (s *chatListStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/agents":
			s.mu.Lock()
			s.agentQueries = append(s.agentQueries, r.URL.RawQuery)
			s.mu.Unlock()
			_, _ = w.Write([]byte(`[{"id":"ag_1","name":"Casey","slug":"casey"}]`))
		case strings.HasPrefix(r.URL.Path, "/api/v1/agents/ag_1/chats"):
			s.mu.Lock()
			s.queries = append(s.queries, r.URL.RawQuery)
			body := s.body
			total := s.total
			s.mu.Unlock()
			if total > 0 {
				w.Header().Set("X-Total-Count", strconv.Itoa(total))
				w.Header().Set("X-Limit", r.URL.Query().Get("limit"))
				w.Header().Set("X-Offset", r.URL.Query().Get("offset"))
			}
			_, _ = w.Write([]byte(body))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"no stub for ` + r.URL.Path + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *chatListStub) lastQuery(t *testing.T) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queries) == 0 {
		t.Fatal("the chats endpoint was never called")
	}
	return s.queries[len(s.queries)-1]
}

func runChatListCLI(t *testing.T, serverURL string, args ...string) (string, error) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + serverURL + "\nworkspace: ws_test\ntoken: fake-token\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

const chatListFixture = `[
  {"id":"c_abc","title":"Deploy rollback","kind":"direct","origin":"UI","status":"ACTIVE",
   "message_count":14,"started_at":"2026-08-30T09:00:00Z","last_activity_at":"2026-08-30T09:00:00Z","unread_count":2},
  {"id":"run_1","title":"Daily digest · summarize","kind":"routine","origin":"ROUTINE","status":"ACTIVE",
   "message_count":2,"started_at":"2026-08-31T07:20:00Z","last_activity_at":"2026-08-31T07:20:00Z","unread_count":0}
]`

func TestAcceptance_ChatList_SendsKindToTheServer(t *testing.T) {
	stub := &chatListStub{body: chatListFixture}
	srv := stub.start(t)

	out, err := runChatListCLI(t, srv.URL, "chat", "list", "casey", "--kind", "direct")
	if err != nil {
		t.Fatalf("chat list: %v\noutput: %s", err, out)
	}
	// The assertion that matters. A flag the binary parses and then drops is
	// the failure mode a stubbed unit test cannot see.
	if q := stub.lastQuery(t); !strings.Contains(q, "kind=direct") {
		t.Errorf("query = %q, want it to carry kind=direct", q)
	}
}

func TestAcceptance_ChatList_CommaSeparatesKinds(t *testing.T) {
	stub := &chatListStub{body: chatListFixture}
	srv := stub.start(t)

	out, err := runChatListCLI(t, srv.URL, "chat", "list", "casey", "--kind", "direct,issue")
	if err != nil {
		t.Fatalf("chat list: %v\noutput: %s", err, out)
	}
	// Encoded, not raw: a bare comma is legal in a query value but the
	// escaping is what proves the value went through url.QueryEscape rather
	// than being concatenated by hand.
	if q := stub.lastQuery(t); !strings.Contains(q, "kind=direct%2Cissue") {
		t.Errorf("query = %q, want both kinds", q)
	}
}

func TestAcceptance_ChatList_SendsNothingWhenNotAsked(t *testing.T) {
	// Absent and empty mean the same thing to the server, but sending an
	// empty one anyway would put the command's DEFAULT behaviour at the mercy
	// of the parameter's parsing instead of the server's default.
	stub := &chatListStub{body: chatListFixture}
	srv := stub.start(t)

	out, err := runChatListCLI(t, srv.URL, "chat", "list", "casey")
	if err != nil {
		t.Fatalf("chat list: %v\noutput: %s", err, out)
	}
	if q := stub.lastQuery(t); strings.Contains(q, "kind") {
		t.Errorf("query = %q, want no kind parameter at all", q)
	}
}

func TestAcceptance_ChatList_ShowsKindInsteadOfRawOrigin(t *testing.T) {
	// KIND replaces ORIGIN in the table: origin is a six-value provenance
	// token, three of whose values a reader has to already know mean "a
	// machine did this". `--output json` still carries both.
	stub := &chatListStub{body: chatListFixture}
	srv := stub.start(t)

	out, err := runChatListCLI(t, srv.URL, "chat", "list", "casey")
	if err != nil {
		t.Fatalf("chat list: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "KIND") {
		t.Errorf("no KIND column:\n%s", out)
	}
	if !strings.Contains(out, "routine") || !strings.Contains(out, "direct") {
		t.Errorf("kinds not rendered:\n%s", out)
	}
	if strings.Contains(out, "ORIGIN") {
		t.Errorf("ORIGIN should have given up its column to KIND:\n%s", out)
	}
}
