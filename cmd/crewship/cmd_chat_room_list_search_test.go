package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// `chat room list --q/--search` — GET /api/v1/conversations grew a `q`
// parameter (title / DM-peer casefold search, applied before pagination),
// and this file pins the CLI half of that contract: the flag must reach the
// server as q=, escaped; the --search spelling must behave identically; and
// giving both spellings must refuse before any request is made rather than
// silently preferring one.
//
// Distinct from `conversation search` (POST /api/v1/conversations/search),
// which searches message TEXT across history — different route, different
// command, untouched here.

// chatRoomListStub records the raw query of every GET /api/v1/conversations.
type chatRoomListStub struct {
	mu      sync.Mutex
	queries []string
}

func (s *chatRoomListStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/conversations" {
			s.mu.Lock()
			s.queries = append(s.queries, r.URL.RawQuery)
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"conversations":[],"next_offset":null}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *chatRoomListStub) lastQuery(t *testing.T) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queries) == 0 {
		t.Fatal("the conversations endpoint was never called")
	}
	return s.queries[len(s.queries)-1]
}

func (s *chatRoomListStub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queries)
}

func runChatRoomList(t *testing.T, serverURL string, args ...string) error {
	t.Helper()
	guardCLIState(t)
	setStubCLI(t, serverURL)
	c := newChatRoomCmd()
	c.SilenceUsage = true
	c.SilenceErrors = true
	c.SetArgs(append([]string{"list"}, args...))
	return c.Execute()
}

func TestChatRoomList_SendsSearchToTheServer(t *testing.T) {
	stub := &chatRoomListStub{}
	srv := stub.start(t)

	if err := runChatRoomList(t, srv.URL, "--q", "planning"); err != nil {
		t.Fatalf("room list: %v", err)
	}
	q := stub.lastQuery(t)
	if !strings.Contains(q, "q=planning") {
		t.Errorf("query = %q, want it to carry q=planning", q)
	}
	// The page params the command already sent must survive the switch to
	// url.Values — losing them would change every existing caller's page.
	if !strings.Contains(q, "limit=100") || !strings.Contains(q, "offset=0") {
		t.Errorf("query = %q, want limit and offset preserved", q)
	}
}

func TestChatRoomList_SearchSpellsOutAndEscapes(t *testing.T) {
	stub := &chatRoomListStub{}
	srv := stub.start(t)

	if err := runChatRoomList(t, srv.URL, "--search", "ops channel"); err != nil {
		t.Fatalf("room list: %v", err)
	}
	// Encoded, not raw: the phrase must go through url.Values, not string
	// concatenation — a bare space in a hand-built query truncates it.
	if q := stub.lastQuery(t); !strings.Contains(q, "q=ops+channel") {
		t.Errorf("query = %q, want the escaped phrase", q)
	}
}

func TestChatRoomList_NoSearchWhenNotAsked(t *testing.T) {
	stub := &chatRoomListStub{}
	srv := stub.start(t)

	if err := runChatRoomList(t, srv.URL); err != nil {
		t.Fatalf("room list: %v", err)
	}
	if q := stub.lastQuery(t); strings.Contains(q, "q=") {
		t.Errorf("query = %q, want no q parameter at all", q)
	}
}

func TestChatRoomList_BothSpellingsRefusedBeforeRequest(t *testing.T) {
	stub := &chatRoomListStub{}
	srv := stub.start(t)

	if err := runChatRoomList(t, srv.URL, "--q", "a", "--search", "b"); err == nil {
		t.Fatal("both spellings accepted; want a refusal naming the pair")
	} else if !strings.Contains(err.Error(), "--q") || !strings.Contains(err.Error(), "--search") {
		t.Errorf("refusal = %q, want it to name both flags", err)
	}
	if n := stub.count(); n != 0 {
		t.Errorf("refusal made %d requests, want 0", n)
	}
}
