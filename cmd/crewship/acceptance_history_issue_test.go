package main

// Acceptance for `crewship history --issue`, driven through the BUILT BINARY.
//
// The runs API now carries mission_id / mission_identifier and filters on
// ?mission_id=. The CLI has to SEND the flag (a filter that only lives in
// the client pages nothing on a real server) and print the identifier so a
// run row names the issue it worked on.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

type historyStub struct {
	mu      sync.Mutex
	queries []url.Values
}

func (s *historyStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/runs" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"no stub for ` + r.URL.Path + `"}`))
			return
		}
		s.mu.Lock()
		s.queries = append(s.queries, r.URL.Query())
		s.mu.Unlock()
		_, _ = w.Write([]byte(`{"data":[
			{"id":"run_a","agent_slug":"robin","status":"completed","trigger_type":"ASSIGNMENT","mission_id":"m_eng1","mission_identifier":"ENG-1","created_at":"2026-09-03T13:10:00Z"},
			{"id":"run_b","agent_slug":"riley","status":"completed","trigger_type":"USER","created_at":"2026-09-03T13:12:00Z"}
		],"stats":{"running":0,"today":2,"failed":0},"pagination":{"page":1,"limit":20,"total":2,"total_pages":1}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAcceptance_History_IssueFilterReachesServerAndShowsIdentifier(t *testing.T) {
	stub := &historyStub{}
	srv := stub.start(t)

	out, err := runPagedListCLI(t, srv.URL, "history", "--issue", "ENG-1", "--since", "")
	if err != nil {
		t.Fatalf("history --issue: %v\n%s", err, out)
	}
	stub.mu.Lock()
	if len(stub.queries) == 0 {
		stub.mu.Unlock()
		t.Fatal("the runs endpoint was never called")
	}
	q := stub.queries[len(stub.queries)-1]
	stub.mu.Unlock()
	if q.Get("mission_id") != "ENG-1" {
		t.Fatalf("--issue did not reach the server as mission_id: query=%v", q)
	}
	if !strings.Contains(out, "ENG-1") {
		t.Fatalf("run rows should name the issue they worked on; got:\n%s", out)
	}
}
