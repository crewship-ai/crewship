package main

// Acceptance for #2147's three commands, driven through the BUILT BINARY —
// the project rule ("every API endpoint gets a CLI command, and its
// acceptance test drives the CLI binary") plus the same reasoning
// acceptance_credential_openrouter_test.go documents: an in-process RunE call
// skips flag registration (so a missing --key would never be caught the way
// a real invocation catches it) and config/env precedence.
//
//   - POST   /api/v1/feature-flags            → feature-flag create
//   - PATCH  /api/v1/feature-flags/{key}       → feature-flag update
//   - GET    /api/v1/admin/workspaces          → admin workspaces
//   - GET    /api/v1/admin/memory/stats        → admin memory-stats

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// flagsAdminStub answers the routes these commands touch and records the
// decoded body of every request it receives, keyed by "METHOD path".
type flagsAdminStub struct {
	mu    sync.Mutex
	calls map[string][]map[string]any
	// handler lets an individual test override the canned response for one
	// "METHOD path" key.
	handler map[string]func(w http.ResponseWriter, r *http.Request)
}

func newFlagsAdminStub() *flagsAdminStub {
	return &flagsAdminStub{
		calls:   map[string][]map[string]any{},
		handler: map[string]func(w http.ResponseWriter, r *http.Request){},
	}
}

func (s *flagsAdminStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		body := map[string]any{}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &body)
		}
		s.mu.Lock()
		s.calls[key] = append(s.calls[key], body)
		h := s.handler[key]
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if h != nil {
			h(w, r)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"no stub for ` + key + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *flagsAdminStub) on(method, path string, fn func(w http.ResponseWriter, r *http.Request)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handler[method+" "+path] = fn
}

func (s *flagsAdminStub) bodiesFor(method, path string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[method+" "+path]
}

func flagsAdminConfig(t *testing.T, serverURL string) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + serverURL + "\nworkspace: ws_test\ntoken: fake-token\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

func runFlagsAdminCLI(t *testing.T, cfgPath string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ─── feature-flag create ──────────────────────────────────────────────────

func TestAcceptance_FeatureFlagCreate(t *testing.T) {
	stub := newFlagsAdminStub()
	srv := stub.start(t)
	cfg := flagsAdminConfig(t, srv.URL)

	stub.on("POST", "/api/v1/feature-flags", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"ff_1","key":"provisioner-v2","description":"rollout of v2","enabled":true,"percentage":25,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`))
	})

	out, err := runFlagsAdminCLI(t, cfg, "feature-flag", "create",
		"--key", "provisioner-v2",
		"--description", "rollout of v2",
		"--enabled",
		"--percentage", "25")
	if err != nil {
		t.Fatalf("create: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "provisioner-v2") {
		t.Errorf("output does not name the created flag:\n%s", out)
	}

	bodies := stub.bodiesFor("POST", "/api/v1/feature-flags")
	if len(bodies) != 1 {
		t.Fatalf("want 1 POST, got %d", len(bodies))
	}
	body := bodies[0]
	if body["key"] != "provisioner-v2" {
		t.Errorf("key = %v", body["key"])
	}
	if body["description"] != "rollout of v2" {
		t.Errorf("description = %v", body["description"])
	}
	if body["enabled"] != true {
		t.Errorf("enabled = %v", body["enabled"])
	}
	if body["percentage"] != float64(25) {
		t.Errorf("percentage = %v", body["percentage"])
	}

	// And it lists — same second-half-of-the-contract shape as the
	// credential acceptance test: a created row must show up where the
	// surface that enumerates it looks.
	stub.on("GET", "/api/v1/feature-flags", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"ff_1","key":"provisioner-v2","description":"rollout of v2","enabled":true,"percentage":25,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}]`))
	})
	listOut, err := runFlagsAdminCLI(t, cfg, "feature-flag", "list")
	if err != nil {
		t.Fatalf("list: %v\noutput: %s", err, listOut)
	}
	if !strings.Contains(listOut, "provisioner-v2") {
		t.Errorf("feature-flag list does not show the created flag:\n%s", listOut)
	}
}

// A flag with no explicit --description must not send the key at all —
// omitted, not an empty string — so the server-side "" means clear /
// nil means unset distinction (see Create's req.Description *string) is
// respected end to end.
func TestAcceptance_FeatureFlagCreate_OmitsUnsetDescription(t *testing.T) {
	stub := newFlagsAdminStub()
	srv := stub.start(t)
	cfg := flagsAdminConfig(t, srv.URL)

	stub.on("POST", "/api/v1/feature-flags", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"ff_2","key":"bare-flag","description":null,"enabled":false,"percentage":0,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`))
	})

	out, err := runFlagsAdminCLI(t, cfg, "feature-flag", "create", "--key", "bare-flag")
	if err != nil {
		t.Fatalf("create: %v\noutput: %s", err, out)
	}
	body := stub.bodiesFor("POST", "/api/v1/feature-flags")[0]
	if _, ok := body["description"]; ok {
		t.Errorf("description sent when --description was never passed: %v", body)
	}
	if body["enabled"] != false {
		t.Errorf("enabled = %v, want false (the unset default)", body["enabled"])
	}
}

// Cobra's own required-flag enforcement, exercised through the real argv
// path — never reaches the server.
func TestAcceptance_FeatureFlagCreate_RequiresKey(t *testing.T) {
	stub := newFlagsAdminStub()
	srv := stub.start(t)
	cfg := flagsAdminConfig(t, srv.URL)

	out, err := runFlagsAdminCLI(t, cfg, "feature-flag", "create", "--description", "no key here")
	if err == nil {
		t.Fatalf("expected a non-zero exit; output: %s", out)
	}
	if !strings.Contains(out, "key") {
		t.Errorf("error does not mention --key:\n%s", out)
	}
	if calls := stub.bodiesFor("POST", "/api/v1/feature-flags"); len(calls) != 0 {
		t.Errorf("a rejected create reached the server: %v", calls)
	}
}

func TestAcceptance_FeatureFlagCreate_PercentageOutOfRange(t *testing.T) {
	stub := newFlagsAdminStub()
	srv := stub.start(t)
	cfg := flagsAdminConfig(t, srv.URL)

	out, err := runFlagsAdminCLI(t, cfg, "feature-flag", "create",
		"--key", "over-100", "--percentage", "150")
	if err == nil {
		t.Fatalf("expected a non-zero exit; output: %s", out)
	}
	if got := exitCodeOf(t, err); got != 2 {
		t.Errorf("exit code = %d, want ExitValidation (2)\noutput: %s", got, out)
	}
	if calls := stub.bodiesFor("POST", "/api/v1/feature-flags"); len(calls) != 0 {
		t.Errorf("an out-of-range percentage reached the server: %v", calls)
	}
}

// ─── feature-flag update ──────────────────────────────────────────────────

// Only the flags actually PASSED are sent — the partial-PATCH contract
// memory-config's `set` already established, applied to this endpoint too.
func TestAcceptance_FeatureFlagUpdate_PartialBody(t *testing.T) {
	stub := newFlagsAdminStub()
	srv := stub.start(t)
	cfg := flagsAdminConfig(t, srv.URL)

	stub.on("PATCH", "/api/v1/feature-flags/provisioner-v2", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"ff_1","key":"provisioner-v2","description":"updated desc","enabled":false,"percentage":10,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`))
	})

	out, err := runFlagsAdminCLI(t, cfg, "feature-flag", "update", "provisioner-v2",
		"--description", "updated desc")
	if err != nil {
		t.Fatalf("update: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "provisioner-v2") {
		t.Errorf("output does not name the updated flag:\n%s", out)
	}

	bodies := stub.bodiesFor("PATCH", "/api/v1/feature-flags/provisioner-v2")
	if len(bodies) != 1 {
		t.Fatalf("want 1 PATCH, got %d", len(bodies))
	}
	body := bodies[0]
	if len(body) != 1 {
		t.Errorf("must send ONLY the one changed field, got %v", body)
	}
	if body["description"] != "updated desc" {
		t.Errorf("description = %v", body["description"])
	}
}

func TestAcceptance_FeatureFlagUpdate_MultipleFields(t *testing.T) {
	stub := newFlagsAdminStub()
	srv := stub.start(t)
	cfg := flagsAdminConfig(t, srv.URL)

	stub.on("PATCH", "/api/v1/feature-flags/noisy-experiment", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"ff_3","key":"noisy-experiment","description":null,"enabled":true,"percentage":75,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`))
	})

	out, err := runFlagsAdminCLI(t, cfg, "feature-flag", "update", "noisy-experiment",
		"--enabled", "--percentage", "75")
	if err != nil {
		t.Fatalf("update: %v\noutput: %s", err, out)
	}

	body := stub.bodiesFor("PATCH", "/api/v1/feature-flags/noisy-experiment")[0]
	if len(body) != 2 {
		t.Errorf("want exactly 2 changed fields, got %v", body)
	}
	if body["enabled"] != true {
		t.Errorf("enabled = %v", body["enabled"])
	}
	if body["percentage"] != float64(75) {
		t.Errorf("percentage = %v", body["percentage"])
	}
}

func TestAcceptance_FeatureFlagUpdate_NothingToUpdate(t *testing.T) {
	stub := newFlagsAdminStub()
	srv := stub.start(t)
	cfg := flagsAdminConfig(t, srv.URL)

	out, err := runFlagsAdminCLI(t, cfg, "feature-flag", "update", "provisioner-v2")
	if err == nil {
		t.Fatalf("expected a non-zero exit; output: %s", out)
	}
	if got := exitCodeOf(t, err); got != 2 {
		t.Errorf("exit code = %d, want ExitValidation (2)\noutput: %s", got, out)
	}
	if calls := stub.bodiesFor("PATCH", "/api/v1/feature-flags/provisioner-v2"); len(calls) != 0 {
		t.Errorf("a no-op update reached the server: %v", calls)
	}
}

func TestAcceptance_FeatureFlagUpdate_NotFoundPropagates(t *testing.T) {
	stub := newFlagsAdminStub()
	srv := stub.start(t)
	cfg := flagsAdminConfig(t, srv.URL)

	stub.on("PATCH", "/api/v1/feature-flags/nope", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Feature flag not found"}`))
	})

	out, err := runFlagsAdminCLI(t, cfg, "feature-flag", "update", "nope", "--enabled")
	if err == nil {
		t.Fatalf("expected a non-zero exit; output: %s", out)
	}
	if got := exitCodeOf(t, err); got != 3 {
		t.Errorf("exit code = %d, want ExitNotFound (3)\noutput: %s", got, out)
	}
	if !strings.Contains(out, "Feature flag not found") {
		t.Errorf("server detail not relayed:\n%s", out)
	}
}

// ─── admin workspaces ──────────────────────────────────────────────────────

func TestAcceptance_AdminWorkspaces(t *testing.T) {
	stub := newFlagsAdminStub()
	srv := stub.start(t)
	cfg := flagsAdminConfig(t, srv.URL)

	stub.on("GET", "/api/v1/admin/workspaces", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"ws_1","name":"Acme","slug":"acme","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","_count_members":3,"_count_agents":8,"_count_crews":2}]`))
	})

	out, err := runFlagsAdminCLI(t, cfg, "admin", "workspaces")
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}
	for _, want := range []string{"Acme", "acme", "3", "8", "2"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	jsonOut, err := runFlagsAdminCLI(t, cfg, "admin", "workspaces", "--format", "json")
	if err != nil {
		t.Fatalf("json run: %v\noutput: %s", err, jsonOut)
	}
	var rows []adminWorkspaceRow
	if err := json.Unmarshal([]byte(jsonOut), &rows); err != nil {
		t.Fatalf("--format json does not parse: %v\noutput: %s", err, jsonOut)
	}
	if len(rows) != 1 || rows[0].Slug != "acme" || rows[0].AgentCount != 8 {
		t.Errorf("decoded rows = %+v", rows)
	}
}

func TestAcceptance_AdminWorkspaces_EmptyListIsBracketsNotNull(t *testing.T) {
	stub := newFlagsAdminStub()
	srv := stub.start(t)
	cfg := flagsAdminConfig(t, srv.URL)

	stub.on("GET", "/api/v1/admin/workspaces", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})

	out, err := runFlagsAdminCLI(t, cfg, "admin", "workspaces", "--format", "json")
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("empty result must marshal as [] not null, got: %q", out)
	}
}

func TestAcceptance_AdminWorkspaces_ForbiddenPropagates(t *testing.T) {
	stub := newFlagsAdminStub()
	srv := stub.start(t)
	cfg := flagsAdminConfig(t, srv.URL)

	stub.on("GET", "/api/v1/admin/workspaces", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"detail":"Forbidden: ADMIN or OWNER only"}`))
	})

	out, err := runFlagsAdminCLI(t, cfg, "admin", "workspaces")
	if err == nil {
		t.Fatalf("expected a non-zero exit; output: %s", out)
	}
	if got := exitCodeOf(t, err); got != 4 {
		t.Errorf("exit code = %d, want ExitAuth (4)\noutput: %s", got, out)
	}
}

// ─── admin memory-stats ────────────────────────────────────────────────────

func TestAcceptance_AdminMemoryStats(t *testing.T) {
	stub := newFlagsAdminStub()
	srv := stub.start(t)
	cfg := flagsAdminConfig(t, srv.URL)

	stub.on("GET", "/api/v1/admin/memory/stats", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"workspace_id":"ws_test",
			"totals":{"versions":42,"bytes":1048576,"blobs":30,"oldest_at":"2026-01-01T00:00:00Z","newest_at":"2026-01-10T00:00:00Z"},
			"by_tier":[{"tier":"agent","versions":30,"bytes":800000},{"tier":"crew","versions":12,"bytes":248576}],
			"by_agent":[{"agent_slug":"martin","versions":20,"bytes":600000,"newest_at":"2026-01-10T00:00:00Z"},{"agent_slug":"","versions":22,"bytes":448576,"newest_at":"2026-01-09T00:00:00Z"}]
		}`))
	})

	out, err := runFlagsAdminCLI(t, cfg, "admin", "memory-stats")
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}
	for _, want := range []string{"42", "martin", "agent", "crew"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	jsonOut, err := runFlagsAdminCLI(t, cfg, "admin", "memory-stats", "--format", "json")
	if err != nil {
		t.Fatalf("json run: %v\noutput: %s", err, jsonOut)
	}
	var result adminMemoryStatsResult
	if err := json.Unmarshal([]byte(jsonOut), &result); err != nil {
		t.Fatalf("--format json does not parse: %v\noutput: %s", err, jsonOut)
	}
	if result.Totals.Versions != 42 || result.Totals.Bytes != 1048576 || result.Totals.Blobs != 30 {
		t.Errorf("totals = %+v", result.Totals)
	}
	if len(result.ByTier) != 2 || len(result.ByAgent) != 2 {
		t.Errorf("by_tier/by_agent = %+v / %+v", result.ByTier, result.ByAgent)
	}
}

func TestAcceptance_AdminMemoryStats_EmptyWorkspace(t *testing.T) {
	stub := newFlagsAdminStub()
	srv := stub.start(t)
	cfg := flagsAdminConfig(t, srv.URL)

	stub.on("GET", "/api/v1/admin/memory/stats", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"workspace_id":"ws_test","totals":{"versions":0,"bytes":0,"blobs":0,"oldest_at":"","newest_at":""},"by_tier":[],"by_agent":[]}`))
	})

	out, err := runFlagsAdminCLI(t, cfg, "admin", "memory-stats")
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "no memory_versions rows") {
		t.Errorf("empty workspace does not say so:\n%s", out)
	}
}
