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

func TestKeeperCmdStructure(t *testing.T) {
	t.Parallel()

	if keeperCmd.Use != "keeper" {
		t.Errorf("keeper Use: got %q want %q", keeperCmd.Use, "keeper")
	}
	if !strings.Contains(strings.ToLower(keeperCmd.Short), "keeper") {
		t.Errorf("keeper Short should mention keeper; got %q", keeperCmd.Short)
	}
	have := map[string]bool{}
	for _, sub := range keeperCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"status", "enable", "disable", "contact", "threshold", "requests", "second-approver"} {
		if !have[want] {
			t.Errorf("keeper missing subcommand %q; have %v", want, have)
		}
	}
	if keeperContactCmd.Flags().Lookup("clear") == nil {
		t.Error("keeper contact missing --clear flag")
	}

	haveSA := map[string]bool{}
	for _, sub := range keeperSecondApproverCmd.Commands() {
		haveSA[sub.Name()] = true
	}
	for _, want := range []string{"enable", "disable"} {
		if !haveSA[want] {
			t.Errorf("keeper second-approver missing subcommand %q; have %v", want, haveSA)
		}
	}
}

// keeperMock stubs the three endpoints the keeper commands consume:
// GET /api/v1/system/keeper, GET+PUT /api/v1/admin/keeper/governance,
// and GET /api/v1/workspaces/{id}/members.
type keeperMock struct {
	t  *testing.T
	mu sync.Mutex

	gov     map[string]any // current governance GET payload
	members []map[string]any

	systemHits int
	govGets    int
	putBody    []byte
}

func newKeeperMock(t *testing.T) *keeperMock {
	return &keeperMock{
		t: t,
		gov: map[string]any{
			"configured":               true,
			"enabled":                  false,
			"security_contact_user_id": "u-contact",
			"deny_notify_min_risk":     4,
			"watch_spec":               "",
			"watch_presets":            []any{"credentials"},
		},
		members: []map[string]any{
			{"id": "wm-1", "user_id": "u-admin", "role": "ADMIN",
				"user": map[string]any{"id": "u-admin", "email": "admin@example.com"}},
			{"id": "wm-2", "user_id": "u-member", "role": "MEMBER",
				"user": map[string]any{"id": "u-member", "email": "member@example.com"}},
		},
	}
}

func (m *keeperMock) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/system/keeper":
			m.mu.Lock()
			m.systemHits++
			m.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"enabled": true, "ollama_url": "http://ollama:11434",
				"model": "qwen3:8b", "ollama_online": true, "secret_count": 2,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/admin/keeper/governance":
			m.mu.Lock()
			m.govGets++
			gov := m.gov
			m.mu.Unlock()
			_ = json.NewEncoder(w).Encode(gov)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/admin/keeper/governance":
			b, _ := io.ReadAll(r.Body)
			m.mu.Lock()
			m.putBody = b
			m.mu.Unlock()
			var body map[string]any
			_ = json.Unmarshal(b, &body)
			body["configured"] = true
			_ = json.NewEncoder(w).Encode(body)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/members"):
			m.mu.Lock()
			members := m.members
			m.mu.Unlock()
			_ = json.NewEncoder(w).Encode(members)
		default:
			m.t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// startKeeperMock wires cliCfg at the mock server; callers get the mock back
// for assertions. saveCLIState handles restoration.
func startKeeperMock(t *testing.T) *keeperMock {
	t.Helper()
	saveCLIState(t)

	m := newKeeperMock(t)
	srv := httptest.NewServer(m.handler())
	t.Cleanup(srv.Close)

	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    srv.URL,
	}
	return m
}

func (m *keeperMock) decodePut(t *testing.T) map[string]any {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.putBody) == 0 {
		t.Fatal("no PUT /admin/keeper/governance was issued")
	}
	var body map[string]any
	if err := json.Unmarshal(m.putBody, &body); err != nil {
		t.Fatalf("decode PUT body %q: %v", m.putBody, err)
	}
	return body
}

func TestKeeperStatusRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	err := keeperStatusCmd.RunE(keeperStatusCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected 'not logged in'; got %v", err)
	}
}

func TestKeeperStatusRunE_HappyPath(t *testing.T) {
	m := startKeeperMock(t)

	if err := keeperStatusCmd.RunE(keeperStatusCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.systemHits != 1 {
		t.Errorf("GET /system/keeper hits: got %d want 1", m.systemHits)
	}
	if m.govGets != 1 {
		t.Errorf("GET /admin/keeper/governance hits: got %d want 1", m.govGets)
	}
}

// assertPartialPut checks the PUT body carries exactly the one expected field
// (partial update — no read-merge echo of untouched settings) and that the
// mutation issued no governance GET.
func assertPartialPut(t *testing.T, m *keeperMock, wantKey string, wantVal any) {
	t.Helper()
	body := m.decodePut(t)
	if body[wantKey] != wantVal {
		t.Errorf("PUT %s: got %v want %v", wantKey, body[wantKey], wantVal)
	}
	for _, other := range []string{"enabled", "security_contact_user_id", "deny_notify_min_risk"} {
		if other == wantKey {
			continue
		}
		if _, ok := body[other]; ok {
			t.Errorf("partial PUT must not carry %q; body=%v", other, body)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.govGets != 0 {
		t.Errorf("mutation must not GET governance (no read-merge); got %d", m.govGets)
	}
}

func TestKeeperEnableRunE_SendsEnabledOnly(t *testing.T) {
	m := startKeeperMock(t)

	if err := keeperEnableCmd.RunE(keeperEnableCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	assertPartialPut(t, m, "enabled", true)
}

func TestKeeperDisableRunE_SendsEnabledOnly(t *testing.T) {
	m := startKeeperMock(t)

	if err := keeperDisableCmd.RunE(keeperDisableCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	assertPartialPut(t, m, "enabled", false)
}

// TestKeeperSecondApproverEnableRunE_SendsFieldOnly drives the #1084
// four-eyes toggle through the CLI RunE the same way the escalation
// resolve acceptance path does — via the shared partial-update PUT, not a
// hand-rolled HTTP request.
func TestKeeperSecondApproverEnableRunE_SendsFieldOnly(t *testing.T) {
	m := startKeeperMock(t)

	if err := keeperSecondApproverEnableCmd.RunE(keeperSecondApproverEnableCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	assertPartialPut(t, m, "require_second_approver", true)
}

func TestKeeperSecondApproverDisableRunE_SendsFieldOnly(t *testing.T) {
	m := startKeeperMock(t)

	if err := keeperSecondApproverDisableCmd.RunE(keeperSecondApproverDisableCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	assertPartialPut(t, m, "require_second_approver", false)
}

func TestKeeperContactRunE_ResolvesEmailToUserID(t *testing.T) {
	m := startKeeperMock(t)

	if err := keeperContactCmd.RunE(keeperContactCmd, []string{"admin@example.com"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	assertPartialPut(t, m, "security_contact_user_id", "u-admin")
}

func TestKeeperContactRunE_NotFoundExitCode(t *testing.T) {
	m := startKeeperMock(t)

	err := keeperContactCmd.RunE(keeperContactCmd, []string{"ghost@example.com"})
	if err == nil {
		t.Fatal("expected error for unknown email")
	}
	if code := cli.ExitCodeFor(err); code != cli.ExitNotFound {
		t.Errorf("exit code: got %d want %d (ExitNotFound); err=%v", code, cli.ExitNotFound, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.putBody) != 0 {
		t.Errorf("no PUT expected on lookup miss; got %s", m.putBody)
	}
}

func TestKeeperContactRunE_RejectsNonAdminRole(t *testing.T) {
	m := startKeeperMock(t)

	err := keeperContactCmd.RunE(keeperContactCmd, []string{"member@example.com"})
	if err == nil || !strings.Contains(err.Error(), "OWNER or ADMIN") {
		t.Errorf("expected OWNER/ADMIN role error; got %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.putBody) != 0 {
		t.Errorf("no PUT expected for non-admin contact; got %s", m.putBody)
	}
}

func TestKeeperContactRunE_Clear(t *testing.T) {
	m := startKeeperMock(t)

	if err := keeperContactCmd.Flags().Set("clear", "true"); err != nil {
		t.Fatalf("set --clear: %v", err)
	}
	t.Cleanup(func() { _ = keeperContactCmd.Flags().Set("clear", "false") })

	if err := keeperContactCmd.RunE(keeperContactCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// --clear sends an explicit empty contact (present key = clear, not "leave").
	assertPartialPut(t, m, "security_contact_user_id", "")
}

func TestKeeperContactRunE_RequiresEmailOrClear(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "cabcdefghijklmnopqrs"}

	err := keeperContactCmd.RunE(keeperContactCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--clear") {
		t.Errorf("expected usage error mentioning --clear; got %v", err)
	}
}

func TestKeeperThresholdRunE_SendsThresholdOnly(t *testing.T) {
	m := startKeeperMock(t)

	if err := keeperThresholdCmd.RunE(keeperThresholdCmd, []string{"9"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	assertPartialPut(t, m, "deny_notify_min_risk", float64(9))
}

func TestKeeperThresholdRunE_RejectsInvalidValues(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "cabcdefghijklmnopqrs"}

	for _, bad := range []string{"0", "11", "-3", "abc"} {
		err := keeperThresholdCmd.RunE(keeperThresholdCmd, []string{bad})
		if err == nil || !strings.Contains(err.Error(), "between 1 and 10") {
			t.Errorf("threshold %q: expected 'between 1 and 10' error; got %v", bad, err)
		}
	}
}

// ─── requests (issue #966 part 3 — GET /api/v1/admin/keeper/requests had no CLI surface) ───

func TestKeeperRequestsRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	err := keeperRequestsCmd.RunE(keeperRequestsCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected 'not logged in'; got %v", err)
	}
}

func TestKeeperRequestsRunE_HappyPath(t *testing.T) {
	saveCLIState(t)

	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/keeper/requests" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"id": "kr_1", "agent_id": "a1", "agent_name": "Viktor",
				"crew_id": "c1", "credential_id": "cred1", "credential_name": "github-token",
				"intent": "clone repo", "request_type": "command",
				"decision": "ALLOW", "risk_score": 2,
				"created_at": "2026-07-01T00:00:00Z",
			},
		})
	}))
	defer srv.Close()

	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "cabcdefghijklmnopqrs", Server: srv.URL}
	if err := keeperRequestsCmd.Flags().Set("limit", "20"); err != nil {
		t.Fatalf("set --limit: %v", err)
	}
	t.Cleanup(func() { _ = keeperRequestsCmd.Flags().Set("limit", "50") })

	out, err := captureStdoutCov(t, func() error {
		return keeperRequestsCmd.RunE(keeperRequestsCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "github-token") || !strings.Contains(out, "ALLOW") {
		t.Errorf("stdout should render the request row, got: %q", out)
	}
	if !strings.Contains(gotQuery, "limit=20") {
		t.Errorf("expected limit=20 in query, got %q", gotQuery)
	}
}

func TestKeeperRequestsRunE_Forbidden(t *testing.T) {
	saveCLIState(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"Forbidden: ADMIN or OWNER only"}`))
	}))
	defer srv.Close()

	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "cabcdefghijklmnopqrs", Server: srv.URL}

	err := keeperRequestsCmd.RunE(keeperRequestsCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "ADMIN or OWNER") {
		t.Errorf("expected forbidden surfaced; got %v", err)
	}
}
