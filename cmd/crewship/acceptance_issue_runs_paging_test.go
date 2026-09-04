package main

// Acceptance for `crewship issue runs`, driven through the BUILT BINARY.
//
// The endpoint now returns the journal run id, the agent slug and pages with
// X-Total-Count. The table has to SHOW the run id (it is what `journal
// --run-id` and the Activity page take — without it the row is a dead end),
// prefer the slug over the display name, send --limit/--offset, and end
// with the server's total, not the page length.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

type issueRunsPagingStub struct {
	mu      sync.Mutex
	queries []url.Values
}

func (s *issueRunsPagingStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/issues/ENG-1":
			_, _ = w.Write([]byte(`{"id":"iss_1","crew_id":"crew_eng","identifier":"ENG-1","title":"Coordinate the launch page","status":"IN_PROGRESS","priority":"high"}`))
		case "/api/v1/crews/crew_eng/issues/ENG-1/runs":
			s.mu.Lock()
			s.queries = append(s.queries, r.URL.Query())
			s.mu.Unlock()
			w.Header().Set("X-Total-Count", "3")
			w.Header().Set("X-Limit", "2")
			w.Header().Set("X-Offset", "0")
			_, _ = w.Write([]byte(`[
				{"id":"asg_a","run_id":"run_aaaa","trace_id":"run_aaaa","status":"RUNNING","agent_id":"ag_1","agent_slug":"robin","agent_name":"Robin","task":"Build the landing page","started_at":"2026-09-03T13:10:00Z","duration_ms":0},
				{"id":"asg_b","status":"PENDING","agent_id":"ag_2","agent_slug":"sam","agent_name":"Sam","task":"Write the copy","duration_ms":0}
			]`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"no stub for ` + r.URL.Path + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAcceptance_IssueRunsPaging_ShowsRunIDAndPages(t *testing.T) {
	stub := &issueRunsPagingStub{}
	srv := stub.start(t)

	out, err := runPagedListCLI(t, srv.URL, "issue", "runs", "ENG-1", "--limit", "2")
	if err != nil {
		t.Fatalf("issue runs: %v\n%s", err, out)
	}
	stub.mu.Lock()
	if len(stub.queries) == 0 {
		stub.mu.Unlock()
		t.Fatal("the runs endpoint was never called")
	}
	q := stub.queries[len(stub.queries)-1]
	stub.mu.Unlock()
	if q.Get("limit") != "2" {
		t.Fatalf("limit did not reach the server: query=%v", q)
	}
	if !strings.Contains(out, "run_aaaa") {
		t.Fatalf("the run id must be in the table (it is the link to the run); got:\n%s", out)
	}
	if !strings.Contains(out, "robin") || !strings.Contains(out, "sam") {
		t.Fatalf("agent column should show the slug; got:\n%s", out)
	}
	if !strings.Contains(out, "showing 1–2 of 3") {
		t.Fatalf("footer must come from X-Total-Count; got:\n%s", out)
	}
}
