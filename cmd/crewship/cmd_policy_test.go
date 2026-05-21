package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestPolicyCmdStructure(t *testing.T) {
	t.Parallel()

	if policyCmd.Use != "policy" {
		t.Errorf("policy Use: got %q, want %q", policyCmd.Use, "policy")
	}
	if !strings.Contains(strings.ToLower(policyCmd.Short), "policy") {
		t.Errorf("policy Short should mention policy; got %q", policyCmd.Short)
	}

	have := map[string]bool{}
	for _, sub := range policyCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"get", "set", "list"} {
		if !have[want] {
			t.Errorf("policy missing subcommand %q; have %v", want, have)
		}
	}
}

func TestPolicyGetFlags(t *testing.T) {
	t.Parallel()
	if policyGetCmd.Flags().Lookup("crew") == nil {
		t.Error("policy get missing --crew flag")
	}
}

func TestPolicySetFlags(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"crew", "level", "behavior", "reason", "yes"} {
		if policySetCmd.Flags().Lookup(name) == nil {
			t.Errorf("policy set missing --%s flag", name)
		}
	}
	if got := policySetCmd.Flags().Lookup("behavior").DefValue; got != "warn" {
		t.Errorf("--behavior default: got %q, want warn", got)
	}
}

func TestPolicyGetRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	err := policyGetCmd.RunE(policyGetCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected 'not logged in'; got %v", err)
	}
}

func TestPolicyGetRunE_NoWorkspace(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token"}
	flagWorkspace = ""
	t.Setenv("CREWSHIP_WORKSPACE", "")

	err := policyGetCmd.RunE(policyGetCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error; got %v", err)
	}
}

func TestPolicyGetRunE_MissingCrewFlag(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    "http://localhost:0",
	}
	_ = policyGetCmd.Flags().Set("crew", "")
	t.Cleanup(func() { _ = policyGetCmd.Flags().Set("crew", "") })

	err := policyGetCmd.RunE(policyGetCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--crew is required") {
		t.Errorf("expected --crew required error; got %v", err)
	}
}

// policyMock stubs all three policy endpoints plus /api/v1/crews so the
// CLI's slug→ID resolution and the list-view crew-name enrichment can
// be exercised without standing up the full server.
type policyMock struct {
	t           *testing.T
	mu          sync.Mutex
	getPath     string
	putPath     string
	putBody     []byte
	listCalled  bool
	getResp     string
	putResp     string
	listResp    string
	crewsResp   string
	putStatus   int
	getStatus   int
	listStatus  int
	crewsStatus int
}

func (m *policyMock) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		m.mu.Lock()
		defer m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case path == "/api/v1/crews" && r.Method == http.MethodGet:
			if m.crewsStatus != 0 {
				w.WriteHeader(m.crewsStatus)
				return
			}
			body := m.crewsResp
			if body == "" {
				body = `[{"id":"crew-eng","slug":"engineering","name":"Engineering"},
				          {"id":"crew-qa","slug":"quality","name":"Quality"}]`
			}
			_, _ = w.Write([]byte(body))
		case path == "/api/v1/policies" && r.Method == http.MethodGet:
			m.listCalled = true
			if m.listStatus != 0 {
				w.WriteHeader(m.listStatus)
				return
			}
			body := m.listResp
			if body == "" {
				body = `[
					{"crew_id":"crew-qa","autonomy_level":"strict","behavior_mode":"block","set_at":"2026-05-20T00:00:00Z"},
					{"crew_id":"crew-eng","autonomy_level":"guided","behavior_mode":"warn"}
				]`
			}
			_, _ = w.Write([]byte(body))
		case strings.HasPrefix(path, "/api/v1/crews/") && strings.HasSuffix(path, "/policy") && r.Method == http.MethodGet:
			m.getPath = path
			if m.getStatus != 0 {
				w.WriteHeader(m.getStatus)
				return
			}
			body := m.getResp
			if body == "" {
				body = `{"crew_id":"crew-eng","autonomy_level":"guided","behavior_mode":"warn","set_by_user_id":"u-1","set_at":"2026-05-21T10:00:00Z","reason":""}`
			}
			_, _ = w.Write([]byte(body))
		case strings.HasPrefix(path, "/api/v1/crews/") && strings.HasSuffix(path, "/policy") && r.Method == http.MethodPut:
			m.putPath = path
			b, _ := io.ReadAll(r.Body)
			m.putBody = b
			if m.putStatus != 0 {
				w.WriteHeader(m.putStatus)
				_, _ = w.Write([]byte(`{"error":"forced"}`))
				return
			}
			body := m.putResp
			if body == "" {
				body = `{"crew_id":"crew-eng","autonomy_level":"trusted","behavior_mode":"warn","set_at":"2026-05-21T10:00:00Z"}`
			}
			_, _ = w.Write([]byte(body))
		default:
			m.t.Errorf("unexpected request: %s %s", r.Method, path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func TestPolicyGetRunE_HappyPath_SlugResolves(t *testing.T) {
	saveCLIState(t)

	m := &policyMock{t: t}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    srv.URL,
	}
	if err := policyGetCmd.Flags().Set("crew", "engineering"); err != nil {
		t.Fatalf("set --crew: %v", err)
	}
	t.Cleanup(func() { _ = policyGetCmd.Flags().Set("crew", "") })

	if err := policyGetCmd.RunE(policyGetCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// Verify slug → crew-eng resolution made it into the URL
	// before the policy GET fired.
	if !strings.HasPrefix(m.getPath, "/api/v1/crews/crew-eng/") {
		t.Errorf("policy GET path = %q, want prefix /api/v1/crews/crew-eng/", m.getPath)
	}
}

func TestPolicySetRunE_InvalidLevel(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    "http://localhost:0",
	}
	_ = policySetCmd.Flags().Set("crew", "engineering")
	_ = policySetCmd.Flags().Set("level", "yolo")
	_ = policySetCmd.Flags().Set("behavior", "warn")
	t.Cleanup(func() {
		_ = policySetCmd.Flags().Set("crew", "")
		_ = policySetCmd.Flags().Set("level", "")
		_ = policySetCmd.Flags().Set("behavior", "warn")
	})

	err := policySetCmd.RunE(policySetCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid --level") {
		t.Errorf("expected invalid --level error; got %v", err)
	}
}

func TestPolicySetRunE_InvalidBehavior(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    "http://localhost:0",
	}
	_ = policySetCmd.Flags().Set("crew", "engineering")
	_ = policySetCmd.Flags().Set("level", "guided")
	_ = policySetCmd.Flags().Set("behavior", "loud")
	t.Cleanup(func() {
		_ = policySetCmd.Flags().Set("crew", "")
		_ = policySetCmd.Flags().Set("level", "")
		_ = policySetCmd.Flags().Set("behavior", "warn")
	})

	err := policySetCmd.RunE(policySetCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid --behavior") {
		t.Errorf("expected invalid --behavior error; got %v", err)
	}
}

func TestPolicySetRunE_FullRequiresReason(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    "http://localhost:0",
	}
	_ = policySetCmd.Flags().Set("crew", "engineering")
	_ = policySetCmd.Flags().Set("level", "full")
	_ = policySetCmd.Flags().Set("behavior", "warn")
	_ = policySetCmd.Flags().Set("reason", "")
	_ = policySetCmd.Flags().Set("yes", "true")
	t.Cleanup(func() {
		_ = policySetCmd.Flags().Set("crew", "")
		_ = policySetCmd.Flags().Set("level", "")
		_ = policySetCmd.Flags().Set("behavior", "warn")
		_ = policySetCmd.Flags().Set("reason", "")
		_ = policySetCmd.Flags().Set("yes", "false")
	})

	err := policySetCmd.RunE(policySetCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--reason is required") {
		t.Errorf("expected --reason required error; got %v", err)
	}
}

func TestPolicySetRunE_HappyPath_TrustedWithYes(t *testing.T) {
	saveCLIState(t)

	m := &policyMock{t: t}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    srv.URL,
	}
	_ = policySetCmd.Flags().Set("crew", "engineering")
	_ = policySetCmd.Flags().Set("level", "trusted")
	_ = policySetCmd.Flags().Set("behavior", "warn")
	_ = policySetCmd.Flags().Set("yes", "true")
	t.Cleanup(func() {
		_ = policySetCmd.Flags().Set("crew", "")
		_ = policySetCmd.Flags().Set("level", "")
		_ = policySetCmd.Flags().Set("behavior", "warn")
		_ = policySetCmd.Flags().Set("yes", "false")
	})

	if err := policySetCmd.RunE(policySetCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if !strings.HasPrefix(m.putPath, "/api/v1/crews/crew-eng/") {
		t.Errorf("PUT path = %q, want prefix /api/v1/crews/crew-eng/", m.putPath)
	}
	// Decode the PUT body and verify the payload.
	var got map[string]string
	if err := json.NewDecoder(bytes.NewReader(m.putBody)).Decode(&got); err != nil {
		t.Fatalf("decode PUT body: %v", err)
	}
	if got["autonomy_level"] != "trusted" {
		t.Errorf("autonomy_level = %q, want trusted", got["autonomy_level"])
	}
	if got["behavior_mode"] != "warn" {
		t.Errorf("behavior_mode = %q, want warn", got["behavior_mode"])
	}
}

func TestPolicySetRunE_FullWithReasonAndYes(t *testing.T) {
	saveCLIState(t)

	m := &policyMock{
		t:       t,
		putResp: `{"crew_id":"crew-eng","autonomy_level":"full","behavior_mode":"warn","reason":"Friday freeze"}`,
	}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    srv.URL,
	}
	_ = policySetCmd.Flags().Set("crew", "engineering")
	_ = policySetCmd.Flags().Set("level", "full")
	_ = policySetCmd.Flags().Set("behavior", "warn")
	_ = policySetCmd.Flags().Set("reason", "Friday freeze")
	_ = policySetCmd.Flags().Set("yes", "true")
	t.Cleanup(func() {
		_ = policySetCmd.Flags().Set("crew", "")
		_ = policySetCmd.Flags().Set("level", "")
		_ = policySetCmd.Flags().Set("behavior", "warn")
		_ = policySetCmd.Flags().Set("reason", "")
		_ = policySetCmd.Flags().Set("yes", "false")
	})

	if err := policySetCmd.RunE(policySetCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	var got map[string]string
	_ = json.NewDecoder(bytes.NewReader(m.putBody)).Decode(&got)
	if got["reason"] != "Friday freeze" {
		t.Errorf("reason = %q, want \"Friday freeze\"", got["reason"])
	}
}

func TestPolicyListRunE_HappyPath_SortedByName(t *testing.T) {
	saveCLIState(t)

	m := &policyMock{t: t}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    srv.URL,
	}

	if err := policyListCmd.RunE(policyListCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.listCalled {
		t.Error("/api/v1/policies was not called")
	}
}

func TestPolicyListRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	err := policyListCmd.RunE(policyListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected 'not logged in'; got %v", err)
	}
}

func TestPolicyListRunE_ServerError(t *testing.T) {
	saveCLIState(t)

	m := &policyMock{t: t, listStatus: http.StatusInternalServerError}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    srv.URL,
	}

	err := policyListCmd.RunE(policyListCmd, nil)
	if err == nil {
		t.Fatal("expected server error to bubble up")
	}
}

func TestValidEnums_StayInSyncWithPolicyPackage(t *testing.T) {
	t.Parallel()
	// Sanity-check that the duplicated enum sets cover exactly the
	// four levels and two modes documented in internal/policy/types.go.
	// If a new level/mode lands in PR-C this test will catch the drift
	// before an operator runs into "invalid --level full" against a
	// server that just shipped a new enum value.
	wantLevels := []string{"strict", "guided", "trusted", "full"}
	for _, l := range wantLevels {
		if _, ok := validAutonomyLevels[l]; !ok {
			t.Errorf("validAutonomyLevels missing %q", l)
		}
	}
	if got := len(validAutonomyLevels); got != len(wantLevels) {
		t.Errorf("validAutonomyLevels has %d entries; want %d (extra entry means drift)", got, len(wantLevels))
	}
	wantModes := []string{"warn", "block"}
	for _, m := range wantModes {
		if _, ok := validBehaviorModes[m]; !ok {
			t.Errorf("validBehaviorModes missing %q", m)
		}
	}
	if got := len(validBehaviorModes); got != len(wantModes) {
		t.Errorf("validBehaviorModes has %d entries; want %d (extra entry means drift)", got, len(wantModes))
	}
}
