package main

// Acceptance coverage for the escalation lifecycle surface this branch added —
// `escalation cancel`, `escalation sweep-expired`, and the `--status` filter —
// driven end to end through the BUILT binary against a stub server.
//
// All three shipped with no CLI test of any kind. The neighbours in
// cmd_escalation_cov_test.go call RunE in-process, which cannot see the two
// things that actually broke here before: whether a flag reaches the server as
// a query parameter, and whether the help text describes the states the server
// will accept. `--status` is the cautionary tale — the flag and
// docs/cli/escalation.mdx advertised a filter that ListEscalations ignored
// entirely, so `escalation list --status PENDING` quietly returned every row,
// and an in-process test asserting on the rendered table would have passed
// throughout.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// escStub records what the binary asked for. Every field is read under the
// mutex because httptest serves on its own goroutines.
type escStub struct {
	mu      sync.Mutex
	methods []string
	paths   []string
	queries []string
	bodies  []string

	// listBody is what GET .../escalations answers with.
	listBody string
	// sweepBody is what POST /escalations/sweep-expired answers with.
	sweepBody string
	// cancelStatus lets a test drive the terminal-state conflict.
	cancelStatus int
	cancelBody   string
}

func (s *escStub) record(r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.methods = append(s.methods, r.Method)
	s.paths = append(s.paths, r.URL.Path)
	s.queries = append(s.queries, r.URL.RawQuery)
	s.bodies = append(s.bodies, string(raw))
}

// sawQuery returns the query string recorded for the first request to path.
func (s *escStub) sawQuery(path string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.paths {
		if p == path {
			return s.queries[i], true
		}
	}
	return "", false
}

func (s *escStub) sawBody(method, path string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.paths {
		if p == path && s.methods[i] == method {
			return s.bodies[i], true
		}
	}
	return "", false
}

func (s *escStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	if s.cancelStatus == 0 {
		s.cancelStatus = http.StatusOK
	}
	if s.cancelBody == "" {
		s.cancelBody = `{"status":"CANCELLED"}`
	}
	if s.sweepBody == "" {
		s.sweepBody = `{"expired":3}`
	}
	if s.listBody == "" {
		s.listBody = `[]`
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		// resolveCrewID's slug scan.
		case r.URL.Path == "/api/v1/crews":
			_, _ = w.Write([]byte(`[{"id":"ccrewcrewcrewcrewcrew","slug":"backend"}]`))
		case strings.HasSuffix(r.URL.Path, "/escalations") && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(s.listBody))
		case r.URL.Path == "/api/v1/escalations/sweep-expired":
			_, _ = w.Write([]byte(s.sweepBody))
		case strings.HasSuffix(r.URL.Path, "/cancel"):
			w.WriteHeader(s.cancelStatus)
			_, _ = w.Write([]byte(s.cancelBody))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"unexpected route"}`))
		}
	}))
}

func escCLIConfig(t *testing.T) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	if err := os.WriteFile(cfgPath,
		[]byte("token: test-token\nworkspace: c000000000000000000esc\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

// The load-bearing test for standard #2: --status must arrive at the server as
// a query parameter. Rendering is not evidence — the flag was honoured by
// nobody for weeks while the table looked perfectly correct.
func TestAcceptance_EscalationList_StatusReachesTheServer(t *testing.T) {
	bin := buildCrewshipBinary(t)

	for _, status := range []string{"PENDING", "RESOLVED", "EXPIRED", "CANCELLED"} {
		t.Run(status, func(t *testing.T) {
			s := &escStub{}
			srv := s.start(t)
			defer srv.Close()

			out, err := runCrewship(t, bin, escCLIConfig(t), srv.URL,
				"escalation", "list", "--crew", "backend", "--status", status, "--no-color")
			if err != nil {
				t.Fatalf("run: %v\n%s", err, out)
			}
			q, ok := s.sawQuery("/api/v1/crews/ccrewcrewcrewcrewcrew/escalations")
			if !ok {
				t.Fatalf("the list route was never called; paths=%v", s.paths)
			}
			if !strings.Contains(q, "status="+status) {
				t.Errorf("--status %s did not reach the server: query was %q", status, q)
			}
		})
	}
}

// Every state the --status help advertises must be one the server's vocabulary
// contains. The flag's usage string is a contract: it told operators for weeks
// that PENDING|RESOLVED were the options, while EXPIRED and CANCELLED rows were
// being written and were unreachable through it.
func TestAcceptance_EscalationList_StatusHelpNamesEveryState(t *testing.T) {
	bin := buildCrewshipBinary(t)
	out, err := runCrewship(t, bin, escCLIConfig(t), "http://127.0.0.1:1",
		"escalation", "list", "--help")
	if err != nil {
		t.Fatalf("help: %v\n%s", err, out)
	}
	for _, state := range []string{"PENDING", "RESOLVED", "EXPIRED", "CANCELLED"} {
		if !strings.Contains(out, state) {
			t.Errorf("`escalation list --help` never mentions %s, which the server accepts:\n%s", state, out)
		}
	}
}

// The ANSWER BY column: "when does this stop being answerable" is the question
// a PENDING list is scanned for, and the answer is `answer_deadline_at` — NOT
// `deadline_at`, which bounds the agent's long poll and runs out in 300 s.
// Printing the agent's clock in the operator's column is the console half of
// the regression the two clocks fixed in the server, so this test pins that the
// table shows the human's number and never the agent's.
//
// A null answer deadline (a row raised before the column existed) must be
// visibly different from one that has passed.
func TestAcceptance_EscalationList_ShowsAnswerDeadlineNotAgentDeadline(t *testing.T) {
	bin := buildCrewshipBinary(t)
	s := &escStub{listBody: `[
		{"id":"esc_with","type":"DECISION","from_name":"Atlas","from_slug":"atlas",
		 "reason":"ship it?","status":"PENDING","created_at":"2026-08-01T10:00:00Z",
		 "deadline_at":"2026-08-01T10:05:00Z","answer_deadline_at":"2026-08-08T10:00:00Z"},
		{"id":"esc_gone","type":"DECISION","from_name":"Atlas","from_slug":"atlas",
		 "reason":"still open, agent left","status":"PENDING","created_at":"2026-08-01T09:00:00Z",
		 "deadline_at":"2026-08-01T09:05:00Z","answer_deadline_at":"2026-08-08T09:00:00Z",
		 "agent_gave_up_at":"2026-08-01T09:05:00Z"},
		{"id":"esc_without","type":"DECISION","from_name":"Atlas","from_slug":"atlas",
		 "reason":"older row","status":"PENDING","created_at":"2026-07-01T10:00:00Z",
		 "deadline_at":null,"answer_deadline_at":null}
	]`}
	srv := s.start(t)
	defer srv.Close()

	out, err := runCrewship(t, bin, escCLIConfig(t), srv.URL,
		"escalation", "list", "--crew", "backend", "--no-color")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "ANSWER BY") {
		t.Errorf("no ANSWER BY column:\n%s", out)
	}
	if !strings.Contains(out, "2026-08-08T10:00:00Z") {
		t.Errorf("the answer deadline is not rendered:\n%s", out)
	}
	// The agent's 300 s window must not appear as the operator's countdown.
	if strings.Contains(out, "2026-08-01T10:05:00Z") {
		t.Errorf("the table printed the AGENT's deadline as the operator's answer-by time:\n%s", out)
	}
	// A row the agent stopped waiting on is still PENDING and still worth
	// answering, and the table must say which it is.
	if !strings.Contains(out, "agent moved on") {
		t.Errorf("a row with agent_gave_up_at is not marked, so an operator cannot tell that "+
			"answering it will not reach the run that asked:\n%s", out)
	}
	// The null case must render as an explicit placeholder, not as an empty
	// cell that reads like a deadline of "".
	if !strings.Contains(out, "—") {
		t.Errorf("a null deadline must be visibly absent, not blank:\n%s", out)
	}
}

// `escalation cancel` is a distinct terminal state, not resolve --action
// reject. It must POST to the cancel route and nowhere else.
func TestAcceptance_EscalationCancel_PostsToTheCancelRoute(t *testing.T) {
	bin := buildCrewshipBinary(t)
	s := &escStub{}
	srv := s.start(t)
	defer srv.Close()

	out, err := runCrewship(t, bin, escCLIConfig(t), srv.URL,
		"escalation", "cancel", "esc_abc", "--reason", "the deploy was rolled back", "--no-color")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	body, ok := s.sawBody(http.MethodPost, "/api/v1/escalations/esc_abc/cancel")
	if !ok {
		t.Fatalf("POST /api/v1/escalations/esc_abc/cancel was never called; paths=%v", s.paths)
	}
	var decoded map[string]any
	if jerr := json.Unmarshal([]byte(body), &decoded); jerr != nil {
		t.Fatalf("cancel body is not JSON: %v (%s)", jerr, body)
	}
	if decoded["reason"] != "the deploy was rolled back" {
		t.Errorf("--reason did not reach the server: body was %s", body)
	}
	if !strings.Contains(strings.ToLower(out), "cancelled") {
		t.Errorf("output does not confirm the cancellation:\n%s", out)
	}
}

// Omitted, not empty. The server treats an absent reason differently from a
// blank one, and a "" reason would journal a withdrawal with an explanation
// that is present and says nothing.
func TestAcceptance_EscalationCancel_OmitsAnUnsetReason(t *testing.T) {
	bin := buildCrewshipBinary(t)
	s := &escStub{}
	srv := s.start(t)
	defer srv.Close()

	out, err := runCrewship(t, bin, escCLIConfig(t), srv.URL,
		"escalation", "cancel", "esc_abc", "--no-color")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	body, ok := s.sawBody(http.MethodPost, "/api/v1/escalations/esc_abc/cancel")
	if !ok {
		t.Fatalf("cancel route never called; paths=%v", s.paths)
	}
	var decoded map[string]any
	if jerr := json.Unmarshal([]byte(body), &decoded); jerr != nil {
		t.Fatalf("cancel body is not JSON: %v (%s)", jerr, body)
	}
	if _, present := decoded["reason"]; present {
		t.Errorf("an unset --reason must be omitted from the body, not sent empty: %s", body)
	}
}

// A question that is already terminal cannot be withdrawn. The CLI must
// surface the server's 409 as a failure — a cancel that silently succeeded
// against an EXPIRED row would tell an operator they had closed something
// nobody could still act on.
func TestAcceptance_EscalationCancel_SurfacesTheTerminalConflict(t *testing.T) {
	bin := buildCrewshipBinary(t)
	s := &escStub{
		cancelStatus: http.StatusConflict,
		cancelBody:   `{"error":"escalation already expired"}`,
	}
	srv := s.start(t)
	defer srv.Close()

	out, err := runCrewship(t, bin, escCLIConfig(t), srv.URL,
		"escalation", "cancel", "esc_abc", "--no-color")
	if err == nil {
		t.Fatalf("a 409 must be a non-zero exit; got success:\n%s", out)
	}
	if !strings.Contains(out, "expired") {
		t.Errorf("the server's reason is not shown to the operator:\n%s", out)
	}
}

// `escalation sweep-expired` makes a background ticker observable. The count
// is the entire answer, so it has to survive to stdout in both shapes.
func TestAcceptance_EscalationSweepExpired_ReportsTheCount(t *testing.T) {
	bin := buildCrewshipBinary(t)
	s := &escStub{sweepBody: `{"expired":7}`}
	srv := s.start(t)
	defer srv.Close()

	out, err := runCrewship(t, bin, escCLIConfig(t), srv.URL,
		"escalation", "sweep-expired", "--no-color")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if _, ok := s.sawBody(http.MethodPost, "/api/v1/escalations/sweep-expired"); !ok {
		t.Fatalf("sweep route never called; paths=%v", s.paths)
	}
	if !strings.Contains(out, "7") {
		t.Errorf("the number expired is not in the output:\n%s", out)
	}

	jsonOut, err := runCrewship(t, bin, escCLIConfig(t), srv.URL,
		"escalation", "sweep-expired", "--format", "json")
	if err != nil {
		t.Fatalf("json run: %v\n%s", err, jsonOut)
	}
	var decoded struct {
		Expired int `json:"expired"`
	}
	if jerr := json.Unmarshal([]byte(jsonOut), &decoded); jerr != nil {
		t.Fatalf("json does not parse: %v\n%s", jerr, jsonOut)
	}
	if decoded.Expired != 7 {
		t.Errorf("expired = %d, want 7 (from %s)", decoded.Expired, jsonOut)
	}
}

// sweep-expired takes no arguments. It is workspace-wide by construction — the
// workspace comes from the auth context — and an argument silently ignored
// would read as a scope the command does not have.
func TestAcceptance_EscalationSweepExpired_RejectsArguments(t *testing.T) {
	bin := buildCrewshipBinary(t)
	s := &escStub{}
	srv := s.start(t)
	defer srv.Close()

	out, err := runCrewship(t, bin, escCLIConfig(t), srv.URL,
		"escalation", "sweep-expired", "backend")
	if err == nil {
		t.Errorf("an argument must be refused, not ignored:\n%s", out)
	}
}
