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
	for _, want := range []string{"status", "enable", "disable", "contact", "threshold"} {
		if !have[want] {
			t.Errorf("keeper missing subcommand %q; have %v", want, have)
		}
	}
	if keeperContactCmd.Flags().Lookup("clear") == nil {
		t.Error("keeper contact missing --clear flag")
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

func TestKeeperEnableRunE_PreservesContactAndThreshold(t *testing.T) {
	m := startKeeperMock(t) // gov: enabled=false, contact=u-contact, risk=4

	if err := keeperEnableCmd.RunE(keeperEnableCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	body := m.decodePut(t)
	if body["enabled"] != true {
		t.Errorf("PUT enabled: got %v want true", body["enabled"])
	}
	if body["security_contact_user_id"] != "u-contact" {
		t.Errorf("PUT contact not preserved: got %v", body["security_contact_user_id"])
	}
	if body["deny_notify_min_risk"] != float64(4) {
		t.Errorf("PUT threshold not preserved: got %v", body["deny_notify_min_risk"])
	}
}

func TestKeeperDisableRunE_PreservesContactAndThreshold(t *testing.T) {
	m := startKeeperMock(t)
	m.gov["enabled"] = true

	if err := keeperDisableCmd.RunE(keeperDisableCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	body := m.decodePut(t)
	if body["enabled"] != false {
		t.Errorf("PUT enabled: got %v want false", body["enabled"])
	}
	if body["security_contact_user_id"] != "u-contact" {
		t.Errorf("PUT contact not preserved: got %v", body["security_contact_user_id"])
	}
	if body["deny_notify_min_risk"] != float64(4) {
		t.Errorf("PUT threshold not preserved: got %v", body["deny_notify_min_risk"])
	}
}

func TestKeeperContactRunE_ResolvesEmailToUserID(t *testing.T) {
	m := startKeeperMock(t)

	if err := keeperContactCmd.RunE(keeperContactCmd, []string{"admin@example.com"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	body := m.decodePut(t)
	if body["security_contact_user_id"] != "u-admin" {
		t.Errorf("PUT contact: got %v want u-admin", body["security_contact_user_id"])
	}
	// Merge must preserve the other two fields.
	if body["enabled"] != false {
		t.Errorf("PUT enabled not preserved: got %v", body["enabled"])
	}
	if body["deny_notify_min_risk"] != float64(4) {
		t.Errorf("PUT threshold not preserved: got %v", body["deny_notify_min_risk"])
	}
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
	m.gov["enabled"] = true

	if err := keeperContactCmd.Flags().Set("clear", "true"); err != nil {
		t.Fatalf("set --clear: %v", err)
	}
	t.Cleanup(func() { _ = keeperContactCmd.Flags().Set("clear", "false") })

	if err := keeperContactCmd.RunE(keeperContactCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	body := m.decodePut(t)
	if body["security_contact_user_id"] != "" {
		t.Errorf("PUT contact: got %v want empty", body["security_contact_user_id"])
	}
	if body["enabled"] != true {
		t.Errorf("PUT enabled not preserved: got %v", body["enabled"])
	}
	if body["deny_notify_min_risk"] != float64(4) {
		t.Errorf("PUT threshold not preserved: got %v", body["deny_notify_min_risk"])
	}
}

func TestKeeperContactRunE_RequiresEmailOrClear(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "cabcdefghijklmnopqrs"}

	err := keeperContactCmd.RunE(keeperContactCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--clear") {
		t.Errorf("expected usage error mentioning --clear; got %v", err)
	}
}

func TestKeeperThresholdRunE_MergesEnabledAndContact(t *testing.T) {
	m := startKeeperMock(t)
	m.gov["enabled"] = true

	if err := keeperThresholdCmd.RunE(keeperThresholdCmd, []string{"9"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	body := m.decodePut(t)
	if body["deny_notify_min_risk"] != float64(9) {
		t.Errorf("PUT threshold: got %v want 9", body["deny_notify_min_risk"])
	}
	if body["enabled"] != true {
		t.Errorf("PUT enabled not preserved: got %v", body["enabled"])
	}
	if body["security_contact_user_id"] != "u-contact" {
		t.Errorf("PUT contact not preserved: got %v", body["security_contact_user_id"])
	}
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
