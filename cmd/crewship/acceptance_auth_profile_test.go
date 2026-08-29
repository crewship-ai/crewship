package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// Acceptance for `crewship auth profile`, the CLI PATCH /api/v1/users/me
// had none of (#2147) — avatar and password already had commands, this
// one did not. Driven through the BUILT BINARY per project convention
// (see acceptance_credential_openrouter_test.go's header for why), not
// RunE in-process.

// profileStubServer answers PATCH /api/v1/users/me and records what was
// sent.
type profileStubServer struct {
	mu      sync.Mutex
	patched map[string]any // decoded PATCH body, nil if never called
}

func (s *profileStubServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/users/me":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.mu.Lock()
			s.patched = body
			s.mu.Unlock()
			name, _ := body["full_name"].(string)
			if name == "" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"no fields to update"}`))
				return
			}
			if len(name) > 100 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"full_name must be 1-100 characters"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"user_stub_1","email":"demo@crewship.ai","full_name":"` +
				name + `","avatar_url":null}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"no stub for ` + r.Method + " " + r.URL.Path + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *profileStubServer) patchedBody(t *testing.T) map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.patched == nil {
		t.Fatal("PATCH /api/v1/users/me was never called")
	}
	return s.patched
}

// profileStubConfig writes a CLI config pointing at the stub. A workspace
// is set even though the endpoint is user-scoped (no workspace context) —
// requireAuth doesn't need one, but the config helper mirrors the other
// acceptance tests so config parsing itself is never the variable.
func profileStubConfig(t *testing.T, serverURL string) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + serverURL + "\nworkspace: ws_test\ntoken: fake-token\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

// runProfileCLI runs the built binary against the stub and returns its
// combined output plus the exit error.
func runProfileCLI(t *testing.T, cfgPath string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// The core contract: --full-name reaches the server verbatim and nothing
// else does — the handler ignores an `email` field, so the CLI must not
// invite one by sending it.
func TestAcceptance_AuthProfile_UpdatesFullName(t *testing.T) {
	stub := &profileStubServer{}
	srv := stub.start(t)
	cfg := profileStubConfig(t, srv.URL)

	out, err := runProfileCLI(t, cfg, "auth", "profile", "--full-name", "Jane Doe")
	if err != nil {
		t.Fatalf("auth profile: %v\noutput: %s", err, out)
	}

	body := stub.patchedBody(t)
	if body["full_name"] != "Jane Doe" {
		t.Errorf("full_name = %v, want %q", body["full_name"], "Jane Doe")
	}
	if len(body) != 1 {
		t.Errorf("PATCH body carries extra fields: %v", body)
	}
	if !strings.Contains(out, "Jane Doe") {
		t.Errorf("human output does not show the updated name:\n%s", out)
	}
}

// -f json must parse and must be the whole of stdout — an agent scripting
// against this command decodes stdout directly.
func TestAcceptance_AuthProfile_JSONOutputParses(t *testing.T) {
	stub := &profileStubServer{}
	srv := stub.start(t)
	cfg := profileStubConfig(t, srv.URL)

	out, err := runProfileCLI(t, cfg, "auth", "profile", "--full-name", "Jane Doe", "-f", "json")
	if err != nil {
		t.Fatalf("auth profile -f json: %v\noutput: %s", err, out)
	}

	var profile struct {
		ID        string  `json:"id"`
		Email     string  `json:"email"`
		FullName  *string `json:"full_name"`
		AvatarURL *string `json:"avatar_url"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &profile); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\noutput: %q", err, out)
	}
	if profile.FullName == nil || *profile.FullName != "Jane Doe" {
		t.Errorf("full_name = %v, want Jane Doe", profile.FullName)
	}
	if profile.Email != "demo@crewship.ai" {
		t.Errorf("email = %q", profile.Email)
	}
}

// Whitespace-only input is rejected client-side, exit 2 per
// scripts/cli-exit-code-contract.sh, and never reaches the server.
func TestAcceptance_AuthProfile_EmptyNameRejectedLocally(t *testing.T) {
	stub := &profileStubServer{}
	srv := stub.start(t)
	cfg := profileStubConfig(t, srv.URL)

	out, err := runProfileCLI(t, cfg, "auth", "profile", "--full-name", "   ")
	if err == nil {
		t.Fatalf("expected a non-zero exit; output: %s", out)
	}
	if got := exitCodeOf(t, err); got != cli.ExitValidation {
		t.Errorf("exit code = %d, want ExitValidation (%d)\noutput: %s", got, cli.ExitValidation, out)
	}
	if !strings.Contains(out, "--full-name is required") {
		t.Errorf("output missing the validation message:\n%s", out)
	}
	stub.mu.Lock()
	patched := stub.patched
	stub.mu.Unlock()
	if patched != nil {
		t.Errorf("a rejected update reached the server: %v", patched)
	}
}

// Missing --full-name entirely behaves the same as blank — both mean "no
// name was given" from the CLI's point of view.
func TestAcceptance_AuthProfile_MissingFlagRejectedLocally(t *testing.T) {
	stub := &profileStubServer{}
	srv := stub.start(t)
	cfg := profileStubConfig(t, srv.URL)

	out, err := runProfileCLI(t, cfg, "auth", "profile")
	if err == nil {
		t.Fatalf("expected a non-zero exit; output: %s", out)
	}
	if got := exitCodeOf(t, err); got != cli.ExitValidation {
		t.Errorf("exit code = %d, want ExitValidation (%d)\noutput: %s", got, cli.ExitValidation, out)
	}
}

// The server-side rejection (over-long name) surfaces as a normal API
// error, mapped through the shared exit-code contract rather than a
// bespoke path — HTTP 400 is ExitValidation same as the local check.
func TestAcceptance_AuthProfile_ServerRejectionMapsToValidationExit(t *testing.T) {
	stub := &profileStubServer{}
	srv := stub.start(t)
	cfg := profileStubConfig(t, srv.URL)

	longName := strings.Repeat("x", 101)
	out, err := runProfileCLI(t, cfg, "auth", "profile", "--full-name", longName)
	if err == nil {
		t.Fatalf("expected a non-zero exit; output: %s", out)
	}
	if got := exitCodeOf(t, err); got != cli.ExitValidation {
		t.Errorf("exit code = %d, want ExitValidation (%d)\noutput: %s", got, cli.ExitValidation, out)
	}
	body := stub.patchedBody(t)
	if body["full_name"] != longName {
		t.Errorf("full_name = %v, want the over-long name reaching the server unmodified", body["full_name"])
	}
}
