package main

// Acceptance for `crewship agent list --include-setup`, driven through the
// BUILT BINARY. The server hides the onboarding Guide's crew (kind=setup) from
// every roster by default; the flag is the explicit opt-in, and it is only
// worth anything if it reaches the query string.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type agentListStub struct {
	mu      sync.Mutex
	queries []string
}

func (s *agentListStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/agents":
			s.mu.Lock()
			s.queries = append(s.queries, r.URL.RawQuery)
			s.mu.Unlock()
			_, _ = w.Write([]byte(`[{"id":"ag_1","name":"Riley","slug":"riley","status":"IDLE","cli_adapter":"CLAUDE_CODE","agent_role":"AGENT","crew":{"name":"Ops","slug":"ops"}}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"no stub for ` + r.URL.Path + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *agentListStub) lastQuery(t *testing.T) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queries) == 0 {
		t.Fatal("the agents endpoint was never called")
	}
	return s.queries[len(s.queries)-1]
}

func TestAcceptance_AgentList_SendsIncludeSetupToTheServer(t *testing.T) {
	stub := &agentListStub{}
	srv := stub.start(t)

	out, err := runChatListCLI(t, srv.URL, "agent", "list", "--include-setup")
	if err != nil {
		t.Fatalf("agent list: %v\noutput: %s", err, out)
	}
	if q := stub.lastQuery(t); !strings.Contains(q, "include_setup=1") {
		t.Errorf("query = %q, want include_setup=1", q)
	}
}

func TestAcceptance_AgentList_HidesSetupByDefault(t *testing.T) {
	stub := &agentListStub{}
	srv := stub.start(t)

	out, err := runChatListCLI(t, srv.URL, "agent", "list")
	if err != nil {
		t.Fatalf("agent list: %v\noutput: %s", err, out)
	}
	if q := stub.lastQuery(t); strings.Contains(q, "include_setup") {
		t.Errorf("query = %q, want no include_setup parameter — hiding is the server's default", q)
	}
	if !strings.Contains(out, "riley") {
		t.Errorf("roster not rendered:\n%s", out)
	}
}
