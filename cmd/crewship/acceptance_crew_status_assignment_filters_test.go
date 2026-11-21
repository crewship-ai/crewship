package main

// Acceptance for `crewship crew status --assigned-to/--status` (#5603).
// The filters are worth nothing unless they reach the server before it cuts
// the page, so the stub records the raw query the BUILT binary sent to
// GET /api/v1/crews/{crewId}/assignments, and answers an unknown status the
// way the real handler does (400 {"error": ...}) to prove the CLI surfaces it.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

const crewStatusFilterCrewID = "ccrewstatusfilter000001"

type crewStatusFilterStub struct {
	mu      sync.Mutex
	queries []string
}

func (s *crewStatusFilterStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/crews/" + crewStatusFilterCrewID:
			_, _ = w.Write([]byte(`{"id":"` + crewStatusFilterCrewID + `","name":"Backend","slug":"backend"}`))
		case "/api/v1/agents":
			_, _ = w.Write([]byte(`[{"slug":"lead","agent_role":"LEAD","status":"ACTIVE"},{"slug":"ava","agent_role":"AGENT","status":"BUSY"}]`))
		case "/api/v1/crews/" + crewStatusFilterCrewID + "/assignments":
			s.mu.Lock()
			s.queries = append(s.queries, r.URL.RawQuery)
			s.mu.Unlock()
			if st := r.URL.Query().Get("status"); strings.Contains(strings.ToUpper(st), "BOGUS") {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"status must be a comma list of: PENDING, QUEUED, RUNNING, COMPLETED, FAILED, CANCELLED"}`))
				return
			}
			w.Header().Set("X-Total-Count", "1")
			_, _ = w.Write([]byte(`[{"id":"asg_1","task":"Rotate the deploy key","status":"QUEUED","queued_reason":"agent_busy",
			  "assigned_by_name":"Lead","assigned_by_slug":"lead","assigned_to_name":"Ava","assigned_to_slug":"ava","created_at":"2026-11-20T09:00:00Z"}]`))
		case "/api/v1/crews/" + crewStatusFilterCrewID + "/escalations":
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *crewStatusFilterStub) lastQuery(t *testing.T) url.Values {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queries) == 0 {
		t.Fatal("the CLI never asked for assignments")
	}
	q, err := url.ParseQuery(s.queries[len(s.queries)-1])
	if err != nil {
		t.Fatalf("parse query %q: %v", s.queries[len(s.queries)-1], err)
	}
	return q
}

func TestAcceptance_CrewStatus_SendsAssignmentFiltersToTheServer(t *testing.T) {
	stub := &crewStatusFilterStub{}
	srv := stub.start(t)

	out, err := runChatListCLIFormat(t, srv.URL, "table", "crew", "status", crewStatusFilterCrewID,
		"--assigned-to", "ava", "--status", "PENDING,QUEUED")
	if err != nil {
		t.Fatalf("crew status: %v\noutput: %s", err, out)
	}
	q := stub.lastQuery(t)
	if got := q.Get("assigned_to"); got != "ava" {
		t.Errorf("assigned_to = %q, want ava (query %v)", got, q)
	}
	if got := q.Get("status"); got != "PENDING,QUEUED" {
		t.Errorf("status = %q, want PENDING,QUEUED (query %v)", got, q)
	}
	for _, want := range []string{"ASSIGNMENTS (last 5, assigned to ava, status PENDING,QUEUED):", "QUEUED (agent_busy)", "lead -> ava", "Rotate the deploy key"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestAcceptance_CrewStatus_NoFiltersSendsNoFilterParams(t *testing.T) {
	stub := &crewStatusFilterStub{}
	srv := stub.start(t)

	out, err := runChatListCLIFormat(t, srv.URL, "table", "crew", "status", crewStatusFilterCrewID)
	if err != nil {
		t.Fatalf("crew status: %v\noutput: %s", err, out)
	}
	q := stub.lastQuery(t)
	if q.Has("assigned_to") || q.Has("status") {
		t.Errorf("unfiltered status sent filters: %v", q)
	}
	if !strings.Contains(out, "ASSIGNMENTS (last 5):") {
		t.Errorf("unfiltered heading changed:\n%s", out)
	}
}

func TestAcceptance_CrewStatus_UnknownStatusSurfacesTheServerError(t *testing.T) {
	stub := &crewStatusFilterStub{}
	srv := stub.start(t)

	out, err := runChatListCLIFormat(t, srv.URL, "table", "crew", "status", crewStatusFilterCrewID, "--status", "bogus")
	if err == nil {
		t.Fatalf("crew status --status bogus succeeded; output: %s", out)
	}
	if !strings.Contains(out, "status must be a comma list of") {
		t.Errorf("server's 400 reason not shown:\n%s", out)
	}
}

func TestAcceptance_CrewStatus_FilteredJSONCarriesTheRows(t *testing.T) {
	stub := &crewStatusFilterStub{}
	srv := stub.start(t)

	out, err := runChatListCLIFormat(t, srv.URL, "json", "crew", "status", crewStatusFilterCrewID, "--assigned-to", "ava", "--status", "queued")
	if err != nil {
		t.Fatalf("crew status: %v\noutput: %s", err, out)
	}
	// The value goes through as typed; the server folds case.
	if got := stub.lastQuery(t).Get("status"); got != "queued" {
		t.Errorf("status = %q, want queued", got)
	}
	var result struct {
		Assignments []struct {
			AssignedToSlug string `json:"assigned_to_slug"`
			Status         string `json:"status"`
		} `json:"assignments"`
		AssignmentCount int `json:"assignment_count"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("json output: %v\n%s", err, out)
	}
	if result.AssignmentCount != 1 || len(result.Assignments) != 1 || result.Assignments[0].AssignedToSlug != "ava" {
		t.Errorf("json assignments = %+v", result)
	}
}
