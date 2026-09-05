package main

// Acceptance for `crewship issue events`, driven through the BUILT BINARY.
//
// The project rule ("every API endpoint gets a CLI command, and its
// acceptance test drives the CLI binary") applies here too: ListEvents
// (internal/api/issue_events_list.go, PRD-ISSUES-AND-ROUTINES-2026 §14.1,
// work package B11 — #2368) is a new endpoint, and the CLI's own `events`
// struct (issueEventsCmd, cmd_issue_extra.go) has its own JSON tags that
// could drift from issueEventDTO independently of any unit test on either
// side. This drives the real binary against a stub server shaped like the
// real route, matching acceptance_issue_sessions_test.go's pattern.

import (
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

type issueEventsStub struct {
	mu          sync.Mutex
	eventsCalls []string
	eventsQuery []url.Values
	eventsBody  string
	// byAfterSeq, when non-nil, serves a DIFFERENT body per after_seq
	// value on the query string — the pagination test below needs the
	// stub to behave like a real multi-page server, not a single fixed
	// response every call returns identically.
	byAfterSeq map[string]string
}

func (s *issueEventsStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/issues/BE-42":
			_, _ = w.Write([]byte(`{
				"id": "iss_1",
				"crew_id": "crew_1",
				"identifier": "BE-42",
				"title": "Refresh token handling",
				"status": "IN_PROGRESS",
				"priority": "MEDIUM",
				"mission_type": "STANDARD",
				"created_at": "2026-08-30T09:00:00Z",
				"updated_at": "2026-08-30T09:00:00Z"
			}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/crews/crew_1/issues/BE-42/events":
			s.mu.Lock()
			s.eventsCalls = append(s.eventsCalls, r.URL.Path)
			s.eventsQuery = append(s.eventsQuery, r.URL.Query())
			body := s.eventsBody
			if s.byAfterSeq != nil {
				body = s.byAfterSeq[r.URL.Query().Get("after_seq")]
			}
			s.mu.Unlock()
			_, _ = w.Write([]byte(body))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"no stub for ` + r.URL.Path + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *issueEventsStub) calls(t *testing.T) []string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.eventsCalls...)
}

func (s *issueEventsStub) queries(t *testing.T) []url.Values {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]url.Values(nil), s.eventsQuery...)
}

func runIssueEventsCLI(t *testing.T, serverURL string, args ...string) (string, error) {
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

// issueEventsFixture is shaped like issueEventsResponse
// (internal/api/issue_events_list.go): two events, seq-ordered.
const issueEventsFixture = `{
  "events": [
    {"id":"act_1","mission_id":"iss_1","seq":1,"actor_type":"user","actor_id":"u1","actor_name":"Jamie Lee",
     "action":"created","details":"Issue created","created_at":"2026-09-04T09:00:00Z"},
    {"id":"act_2","mission_id":"iss_1","seq":2,"actor_type":"agent","actor_id":"a1","actor_name":"Backend Dev",
     "action":"status_changed","details":"TODO -> IN_PROGRESS","created_at":"2026-09-04T10:00:00Z"}
  ],
  "after_seq": 0,
  "latest_seq": 2
}`

func TestAcceptance_IssueEvents_ListsInSeqOrder(t *testing.T) {
	stub := &issueEventsStub{eventsBody: issueEventsFixture}
	srv := stub.start(t)

	out, err := runIssueEventsCLI(t, srv.URL, "issue", "events", "BE-42")
	if err != nil {
		t.Fatalf("issue events: %v\noutput: %s", err, out)
	}

	calls := stub.calls(t)
	if len(calls) != 1 || calls[0] != "/api/v1/crews/crew_1/issues/BE-42/events" {
		t.Errorf("events calls = %v, want exactly one call to /api/v1/crews/crew_1/issues/BE-42/events", calls)
	}
	// No --after-seq passed: the query string must not carry after_seq at
	// all (0 means "full history" and is the omitted default, not a
	// literal after_seq=0 the server would also accept identically).
	queries := stub.queries(t)
	if len(queries) == 1 && queries[0].Get("after_seq") != "" {
		t.Errorf("after_seq = %q, want empty (no flag passed)", queries[0].Get("after_seq"))
	}

	for _, want := range []string{"Jamie Lee", "Backend Dev", "created", "status_changed"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	// Ordering matters here specifically — this endpoint's whole reason to
	// exist over `issue activity` is that it is seq-ordered, not just that
	// it contains the same rows.
	if i, j := strings.Index(out, "Jamie Lee"), strings.Index(out, "Backend Dev"); i == -1 || j == -1 || i > j {
		t.Errorf("output does not show seq 1 (Jamie Lee) before seq 2 (Backend Dev):\n%s", out)
	}
}

func TestAcceptance_IssueEvents_AfterSeqFlag(t *testing.T) {
	stub := &issueEventsStub{eventsBody: `{"events":[],"after_seq":2,"latest_seq":2}`}
	srv := stub.start(t)

	out, err := runIssueEventsCLI(t, srv.URL, "issue", "events", "BE-42", "--after-seq", "2")
	if err != nil {
		t.Fatalf("issue events: %v\noutput: %s", err, out)
	}

	queries := stub.queries(t)
	if len(queries) != 1 || queries[0].Get("after_seq") != "2" {
		t.Fatalf("queries = %v, want exactly one call with after_seq=2", queries)
	}
}

func TestAcceptance_IssueEvents_JSON_ShowsSeqAndLatestSeq(t *testing.T) {
	stub := &issueEventsStub{eventsBody: issueEventsFixture}
	srv := stub.start(t)

	out, err := runIssueEventsCLI(t, srv.URL, "issue", "events", "BE-42", "--format", "json")
	if err != nil {
		t.Fatalf("issue events: %v\noutput: %s", err, out)
	}
	for _, want := range []string{`"seq": 1`, `"seq": 2`, `"latest_seq": 2`} {
		if !strings.Contains(out, want) {
			t.Errorf("json output missing %s:\n%s", want, out)
		}
	}
}

func TestAcceptance_IssueEvents_EmptyList(t *testing.T) {
	stub := &issueEventsStub{eventsBody: `{"events":[],"after_seq":0,"latest_seq":0}`}
	srv := stub.start(t)

	out, err := runIssueEventsCLI(t, srv.URL, "issue", "events", "BE-42", "--format", "json")
	if err != nil {
		t.Fatalf("issue events: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, `"events": []`) {
		t.Errorf("output = %q, want an empty events array", out)
	}
}

// TestAcceptance_IssueEvents_PagesPastTheServerCap — code review on #2377:
// the one-shot version stopped after the FIRST page and silently dropped
// everything past the server's 500-row cap. This drives the real binary
// against a stub server that answers with THREE pages (mirroring a history
// longer than one page) and asserts the CLI followed all of them rather
// than reporting only the first.
func TestAcceptance_IssueEvents_PagesPastTheServerCap(t *testing.T) {
	stub := &issueEventsStub{byAfterSeq: map[string]string{
		"":  `{"events":[{"id":"a1","mission_id":"iss_1","seq":1,"actor_type":"user","actor_id":"u1","actor_name":"Page One","action":"created","created_at":"2026-09-04T09:00:00Z"}],"after_seq":0,"latest_seq":3}`,
		"1": `{"events":[{"id":"a2","mission_id":"iss_1","seq":2,"actor_type":"user","actor_id":"u1","actor_name":"Page Two","action":"status_changed","created_at":"2026-09-04T09:01:00Z"}],"after_seq":1,"latest_seq":3}`,
		"2": `{"events":[{"id":"a3","mission_id":"iss_1","seq":3,"actor_type":"user","actor_id":"u1","actor_name":"Page Three","action":"status_changed","created_at":"2026-09-04T09:02:00Z"}],"after_seq":2,"latest_seq":3}`,
	}}
	srv := stub.start(t)

	out, err := runIssueEventsCLI(t, srv.URL, "issue", "events", "BE-42", "--format", "json")
	if err != nil {
		t.Fatalf("issue events: %v\noutput: %s", err, out)
	}

	calls := stub.calls(t)
	if len(calls) != 3 {
		t.Fatalf("events calls = %d, want 3 (one per page): %v", len(calls), calls)
	}
	for _, want := range []string{"Page One", "Page Two", "Page Three", `"seq": 3`} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q (page not followed):\n%s", want, out)
		}
	}
}
