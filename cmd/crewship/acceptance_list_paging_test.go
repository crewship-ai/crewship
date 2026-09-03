package main

// Acceptance for list paging (`--limit`, `--offset`, the "showing N of TOTAL"
// footer), driven through the BUILT BINARY against a stub that answers the
// way the S1 convention says a server does: bare array body plus
// X-Total-Count / X-Limit / X-Offset.
//
// Two hazards this guards against. A flag that is registered and printed but
// never SENT looks fine in a unit test and pages nothing on a real server.
// And a footer computed from the page length instead of the header would
// say "showing 3 of 3" on a workspace with 1 015 issues — the exact defect
// the web board shipped with.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type pagedListStub struct {
	mu      sync.Mutex
	queries []url.Values
}

func (s *pagedListStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/issues" && r.URL.Path != "/api/v1/missions" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"no stub for ` + r.URL.Path + `"}`))
			return
		}
		s.mu.Lock()
		s.queries = append(s.queries, r.URL.Query())
		s.mu.Unlock()

		// The server clamps and echoes; the stub echoes what it was asked so
		// the footer test can pin the arithmetic.
		limit := r.URL.Query().Get("limit")
		if limit == "" {
			limit = "50"
		}
		offset := r.URL.Query().Get("offset")
		if offset == "" {
			offset = "0"
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Total-Count", "7")
		w.Header().Set("X-Limit", limit)
		w.Header().Set("X-Offset", offset)
		if r.URL.Path == "/api/v1/missions" {
			_, _ = w.Write([]byte(`[{"id":"m1","title":"Harborlight launch","status":"IN_PROGRESS","lead_agent_slug":"alex","created_at":"2026-09-03T10:00:00Z"}]`))
			return
		}
		_, _ = w.Write([]byte(`[
			{"id":"iss_1","identifier":"ENG-1","title":"Coordinate the launch page","status":"IN_PROGRESS","priority":"high","crew_slug":"engineering","updated_at":"2026-09-03T10:00:00Z"},
			{"id":"iss_2","identifier":"ENG-2","title":"Rewrite the README","status":"REVIEW","priority":"medium","crew_slug":"engineering","updated_at":"2026-09-03T10:00:00Z"},
			{"id":"iss_3","identifier":"ENG-3","title":"Create a directory tree","status":"BACKLOG","priority":"low","crew_slug":"engineering","updated_at":"2026-09-03T10:00:00Z"}
		]`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *pagedListStub) lastQuery(t *testing.T) url.Values {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queries) == 0 {
		t.Fatal("the list endpoint was never called")
	}
	return s.queries[len(s.queries)-1]
}

func runPagedListCLI(t *testing.T, serverURL string, args ...string) (string, error) {
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

func TestAcceptance_IssueList_PagingReachesServerAndFooterReadsTotal(t *testing.T) {
	stub := &pagedListStub{}
	srv := stub.start(t)

	out, err := runPagedListCLI(t, srv.URL, "issue", "list", "--limit", "3")
	if err != nil {
		t.Fatalf("issue list --limit 3: %v\n%s", err, out)
	}
	q := stub.lastQuery(t)
	if q.Get("limit") != "3" {
		t.Fatalf("limit did not reach the server: query=%v", q)
	}
	if !strings.Contains(out, "showing 1–3 of 7") || !strings.Contains(out, "--offset 3") {
		t.Fatalf("footer must come from X-Total-Count and name the next page; got:\n%s", out)
	}

	out, err = runPagedListCLI(t, srv.URL, "issue", "list", "--limit", "3", "--offset", "3", "--search", "launch")
	if err != nil {
		t.Fatalf("issue list --offset 3: %v\n%s", err, out)
	}
	q = stub.lastQuery(t)
	if q.Get("offset") != "3" || q.Get("q") != "launch" {
		t.Fatalf("offset/search did not reach the server: query=%v", q)
	}
	if !strings.Contains(out, "showing 4–6 of 7") {
		t.Fatalf("footer must add the offset; got:\n%s", out)
	}
}

// Machine formats stay a clean array: the footer is a human courtesy and
// must never corrupt `-f json` for scripts.
func TestAcceptance_IssueList_JSONHasNoFooter(t *testing.T) {
	stub := &pagedListStub{}
	srv := stub.start(t)

	out, err := runPagedListCLI(t, srv.URL, "issue", "list", "--limit", "3", "-f", "json")
	if err != nil {
		t.Fatalf("issue list -f json: %v\n%s", err, out)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("-f json output is not a bare JSON array (footer leaked?): %v\n%s", err, out)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
}

func TestAcceptance_MissionList_PagingAndSearchReachServer(t *testing.T) {
	stub := &pagedListStub{}
	srv := stub.start(t)

	out, err := runPagedListCLI(t, srv.URL, "mission", "list", "--limit", "1", "--offset", "6", "--search", "Harbor")
	if err != nil {
		t.Fatalf("mission list: %v\n%s", err, out)
	}
	q := stub.lastQuery(t)
	if q.Get("limit") != "1" || q.Get("offset") != "6" || q.Get("q") != "Harbor" {
		t.Fatalf("paging/search did not reach the server: query=%v", q)
	}
	if !strings.Contains(out, "showing 7–7 of 7") {
		t.Fatalf("footer on the last page must not offer a next page; got:\n%s", out)
	}
}
