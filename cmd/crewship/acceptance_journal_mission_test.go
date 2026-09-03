package main

// Acceptance for `crewship journal --mission`, driven through the BUILT
// BINARY. The server now resolves an issue identifier (ENG-1) for
// ?mission_id=, so the CLI must pass the flag through untouched — a client
// that "helpfully" validated it as a cuid would refuse the one spelling a
// person has on hand.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

type journalMissionStub struct {
	mu      sync.Mutex
	queries []url.Values
}

func (s *journalMissionStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/journal" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"no stub for ` + r.URL.Path + `"}`))
			return
		}
		s.mu.Lock()
		s.queries = append(s.queries, r.URL.Query())
		s.mu.Unlock()
		_, _ = w.Write([]byte(`{"entries":[
			{"id":"je_1","ts":"2026-09-03T13:10:00Z","entry_type":"run.started","severity":"info","actor_type":"orchestrator","summary":"Robin started Build the landing page on ENG-1","mission_id":"m_eng1","trace_id":"run_a"},
			{"id":"je_2","ts":"2026-09-03T13:12:00Z","entry_type":"mission.assigned","severity":"info","actor_type":"user","summary":"ENG-1 assigned to Alex","mission_id":"m_eng1"}
		],"next_cursor":""}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAcceptance_Journal_MissionIdentifierReachesServer(t *testing.T) {
	stub := &journalMissionStub{}
	srv := stub.start(t)

	out, err := runPagedListCLI(t, srv.URL, "journal", "--mission", "ENG-1")
	if err != nil {
		t.Fatalf("journal --mission ENG-1: %v\n%s", err, out)
	}
	stub.mu.Lock()
	if len(stub.queries) == 0 {
		stub.mu.Unlock()
		t.Fatal("the journal endpoint was never called")
	}
	q := stub.queries[len(stub.queries)-1]
	stub.mu.Unlock()
	if q.Get("mission_id") != "ENG-1" {
		t.Fatalf("--mission must reach the server as mission_id verbatim: query=%v", q)
	}
	if !strings.Contains(out, "Build the landing page") {
		t.Fatalf("entries were not rendered; got:\n%s", out)
	}
}
