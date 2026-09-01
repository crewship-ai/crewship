package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestApprovalsCmdStructure(t *testing.T) {
	t.Parallel()

	if approvalsCmd.Use != "approvals" {
		t.Errorf("approvals Use: got %q want %q", approvalsCmd.Use, "approvals")
	}
	if !strings.Contains(strings.ToLower(approvalsCmd.Short), "approval") {
		t.Errorf("approvals Short should mention approval; got %q", approvalsCmd.Short)
	}
	have := map[string]bool{}
	for _, sub := range approvalsCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"list", "approve", "deny"} {
		if !have[want] {
			t.Errorf("approvals missing subcommand %q; have %v", want, have)
		}
	}
}

func TestApprovalsListFlags(t *testing.T) {
	t.Parallel()

	status := approvalsListCmd.Flags().Lookup("status")
	if status == nil {
		t.Fatal("approvals list missing --status flag")
	}
	if status.DefValue != "pending" {
		t.Errorf("--status default: got %q want %q", status.DefValue, "pending")
	}

	limit := approvalsListCmd.Flags().Lookup("limit")
	if limit == nil {
		t.Fatal("approvals list missing --limit flag")
	}
	if limit.DefValue != "50" {
		t.Errorf("--limit default: got %q want %q", limit.DefValue, "50")
	}
	if limit.Value.Type() != "int" {
		t.Errorf("--limit type: got %q want int", limit.Value.Type())
	}
}

func TestApprovalsDecideFlags(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		cmd  string
	}{
		{"approve", "approve"},
		{"deny", "deny"},
	} {
		var flagSet bool
		switch c.name {
		case "approve":
			flagSet = approvalsApproveCmd.Flags().Lookup("comment") != nil
		case "deny":
			flagSet = approvalsDenyCmd.Flags().Lookup("comment") != nil
		}
		if !flagSet {
			t.Errorf("approvals %s missing --comment flag", c.cmd)
		}
	}
}

func TestApprovalsListRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	err := approvalsListCmd.RunE(approvalsListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected 'not logged in'; got %v", err)
	}
}

func TestApprovalsListRunE_NoWorkspace(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token"}
	flagWorkspace = ""
	t.Setenv("CREWSHIP_WORKSPACE", "")

	err := approvalsListCmd.RunE(approvalsListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error; got %v", err)
	}
}

// approvalsMock stubs the two endpoints this file needs.
type approvalsMock struct {
	t          *testing.T
	mu         sync.Mutex
	listQS     string // query string captured from GET /api/v1/approvals
	decidePath string
	decideBody []byte
}

func (m *approvalsMock) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/approvals") && !strings.Contains(r.URL.Path, "/decide"):
			m.mu.Lock()
			m.listQS = r.URL.RawQuery
			m.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"rows":[{"id":"apr-1","status":"pending","kind":"shell.exec","reason":"rm -rf","crew_id":"crew-1","agent_id":"ag-1","mission_id":"mis-1","requested_by":"u-1","created_at":"2026-04-17T00:00:00Z"}],"count":1,"status":"pending"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/decide"):
			m.mu.Lock()
			m.decidePath = r.URL.Path
			b, _ := io.ReadAll(r.Body)
			m.decideBody = b
			m.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "approved", "decided_by": "u-42"})
		default:
			m.t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func TestApprovalsListRunE_HappyPath(t *testing.T) {
	saveCLIState(t)

	m := &approvalsMock{t: t}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    srv.URL,
	}
	if err := approvalsListCmd.Flags().Set("limit", "5"); err != nil {
		t.Fatalf("set --limit: %v", err)
	}
	if err := approvalsListCmd.Flags().Set("status", "pending"); err != nil {
		t.Fatalf("set --status: %v", err)
	}
	t.Cleanup(func() {
		_ = approvalsListCmd.Flags().Set("limit", "50")
		_ = approvalsListCmd.Flags().Set("status", "pending")
	})

	if err := approvalsListCmd.RunE(approvalsListCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	m.mu.Lock()
	got := m.listQS
	m.mu.Unlock()
	if !strings.Contains(got, "limit=5") {
		t.Errorf("limit not propagated: %q", got)
	}
	if !strings.Contains(got, "status=pending") {
		t.Errorf("status not propagated: %q", got)
	}
}

func TestApprovalsApproveRunE_HappyPath(t *testing.T) {
	saveCLIState(t)

	m := &approvalsMock{t: t}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    srv.URL,
	}
	if err := approvalsApproveCmd.Flags().Set("comment", "ok"); err != nil {
		t.Fatalf("set --comment: %v", err)
	}
	t.Cleanup(func() { _ = approvalsApproveCmd.Flags().Set("comment", "") })

	if err := approvalsApproveCmd.RunE(approvalsApproveCmd, []string{"apr-1"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	m.mu.Lock()
	path := m.decidePath
	body := string(m.decideBody)
	m.mu.Unlock()
	if !strings.HasSuffix(path, "/approvals/apr-1/decide") {
		t.Errorf("decide path: got %q", path)
	}
	if !strings.Contains(body, `"status":"approved"`) {
		t.Errorf("decide body missing status: %s", body)
	}
	if !strings.Contains(body, `"comment":"ok"`) {
		t.Errorf("decide body missing comment: %s", body)
	}
}

func TestApprovalsDenyRunE_HappyPath(t *testing.T) {
	saveCLIState(t)

	m := &approvalsMock{t: t}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    srv.URL,
	}
	_ = approvalsDenyCmd.Flags().Set("comment", "")
	t.Cleanup(func() { _ = approvalsDenyCmd.Flags().Set("comment", "") })

	if err := approvalsDenyCmd.RunE(approvalsDenyCmd, []string{"apr-2"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	m.mu.Lock()
	body := string(m.decideBody)
	m.mu.Unlock()
	if !strings.Contains(body, `"status":"denied"`) {
		t.Errorf("decide body missing denied status: %s", body)
	}
}

// pagingMock answers a two-page feed and records the query string of every
// request, so a test can assert the CLI actually walked the pages instead of
// asserting on its own fixture. The row shape is the one the server really
// emits (snake_case, has_more) — see TestApprovals_WireShape_IsSnakeCase in
// internal/api, which pins that shape at the other end.
type pagingMock struct {
	t     *testing.T
	path  string
	mu    sync.Mutex
	seen  []string
	pages []string
}

func (m *pagingMock) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, m.path) {
			m.t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		m.mu.Lock()
		n := len(m.seen)
		m.seen = append(m.seen, r.URL.RawQuery)
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n < len(m.pages) {
			_, _ = w.Write([]byte(m.pages[n]))
			return
		}
		m.t.Errorf("CLI asked for page %d but the mock only has %d", n+1, len(m.pages))
	})
}

// A CLI that stops at the first page cannot be the contract agents script
// against — the web inbox walks every page, so `--all` has to as well.
func TestApprovalsList_All_WalksEveryPage(t *testing.T) {
	saveCLIState(t)

	m := &pagingMock{t: t, path: "/api/v1/approvals", pages: []string{
		`{"rows":[{"id":"apr-1","status":"pending","kind":"tool_call","created_at":"2026-04-17T00:00:00Z"},{"id":"apr-2","status":"pending","kind":"tool_call","created_at":"2026-04-17T00:00:00Z"}],"count":2,"has_more":true}`,
		`{"rows":[{"id":"apr-3","status":"pending","kind":"tool_call","created_at":"2026-04-17T00:00:00Z"}],"count":1,"has_more":false}`,
	}}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "cabcdefghijklmnopqrs", Server: srv.URL}

	if err := approvalsListCmd.Flags().Set("all", "true"); err != nil {
		t.Fatalf("set --all: %v", err)
	}
	t.Cleanup(func() { _ = approvalsListCmd.Flags().Set("all", "false") })

	out, err := captureStdoutCov(t, func() error { return approvalsListCmd.RunE(approvalsListCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}

	m.mu.Lock()
	seen := append([]string(nil), m.seen...)
	m.mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("requests = %d (%v), want 2 — --all stopped before has_more went false", len(seen), seen)
	}
	if strings.Contains(seen[0], "offset=") {
		t.Errorf("first page sent an offset: %q", seen[0])
	}
	if !strings.Contains(seen[1], "offset=2") {
		t.Errorf("second page offset = %q, want offset=2 (rows returned by page 1)", seen[1])
	}
	for _, id := range []string{"apr-1", "apr-2", "apr-3"} {
		if !strings.Contains(out, id) {
			t.Errorf("output is missing %s — a walked page was dropped:\n%s", id, out)
		}
	}
}

// Without --all the walk must not happen: one request, no offset. A flag that
// silently paginates would change the cost of every existing script.
func TestApprovalsList_WithoutAll_StopsAtFirstPage(t *testing.T) {
	saveCLIState(t)

	m := &pagingMock{t: t, path: "/api/v1/approvals", pages: []string{
		`{"rows":[{"id":"apr-1","status":"pending","kind":"tool_call","created_at":"2026-04-17T00:00:00Z"}],"count":1,"has_more":true}`,
	}}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "cabcdefghijklmnopqrs", Server: srv.URL}

	if _, err := captureStdoutCov(t, func() error { return approvalsListCmd.RunE(approvalsListCmd, nil) }); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	m.mu.Lock()
	n := len(m.seen)
	m.mu.Unlock()
	if n != 1 {
		t.Fatalf("requests = %d, want 1 — has_more must be ignored without --all", n)
	}
}
