package main

// Acceptance for `crewship credential login`, driven through the BUILT
// BINARY against a stub server (the same rule and the same reason as
// acceptance_credential_openrouter_test.go: the route-contract test cannot
// see calls that go through the api helpers, so the wire bodies are asserted
// here). The stub plays the server; the server plays the provider — nothing
// here dials OpenAI.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// deviceLoginStubServer answers the two provider-login endpoints and the
// credential read the command finishes with. pollsUntilComplete is how many
// status reads answer "pending" before "complete".
type deviceLoginStubServer struct {
	mu                 sync.Mutex
	started            map[string]any
	statusReads        int
	pollsUntilComplete int
	finalStatus        string
}

func (s *deviceLoginStubServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/provider-logins/device":
			s.mu.Lock()
			raw, _ := io.ReadAll(r.Body)
			s.started = map[string]any{}
			_ = json.Unmarshal(raw, &s.started)
			s.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"device_id":"dev_stub_1","user_code":"ABCD-EFGHJ","verification_url":"https://auth.example/codex/device","expires_at":"2099-01-01T00:00:00Z","interval_s":1}`))

		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/provider-logins/device/dev_stub_1":
			s.mu.Lock()
			s.statusReads++
			reads := s.statusReads
			final := s.finalStatus
			s.mu.Unlock()
			if reads <= s.pollsUntilComplete {
				_, _ = w.Write([]byte(`{"status":"pending","user_code":"ABCD-EFGHJ","verification_url":"https://auth.example/codex/device","expires_at":"2099-01-01T00:00:00Z"}`))
				return
			}
			switch final {
			case "denied":
				_, _ = w.Write([]byte(`{"status":"denied","error":"the provider refused the sign-in","user_code":"ABCD-EFGHJ","verification_url":"x","expires_at":"2099-01-01T00:00:00Z"}`))
			case "expired":
				_, _ = w.Write([]byte(`{"status":"expired","error":"the code expired","user_code":"ABCD-EFGHJ","verification_url":"x","expires_at":"2099-01-01T00:00:00Z"}`))
			default:
				_, _ = w.Write([]byte(`{"status":"complete","credential_id":"cred_stub_1","user_code":"ABCD-EFGHJ","verification_url":"x","expires_at":"2099-01-01T00:00:00Z"}`))
			}

		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/credentials/cred_stub_1":
			_, _ = w.Write([]byte(`{"id":"cred_stub_1","name":"ChatGPT Plus · jana@unify.cz","type":"AI_CLI_TOKEN","provider":"OPENAI","status":"ACTIVE","scope":"WORKSPACE","account_email":"jana@unify.cz","created_at":"2026-09-06T10:00:00Z"}`))

		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"no stub for ` + r.Method + " " + r.URL.Path + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAcceptance_CredentialLogin_WaitsAndPrintsCredential(t *testing.T) {
	stub := &deviceLoginStubServer{pollsUntilComplete: 2}
	srv := stub.start(t)
	cfg := credStubConfig(t, srv.URL)

	out, err := runCredCLI(t, cfg, "credential", "login", "--provider", "openai")
	if err != nil {
		t.Fatalf("login: %v\noutput: %s", err, out)
	}
	stub.mu.Lock()
	started := stub.started
	reads := stub.statusReads
	stub.mu.Unlock()
	if started == nil || started["provider"] != "OPENAI" {
		t.Errorf("start body = %v, want provider OPENAI (upper-cased by the CLI)", started)
	}
	if _, hasMode := started["mode"]; hasMode {
		t.Errorf("start body carries a mode when none was given: %v", started)
	}
	if reads < 3 {
		t.Errorf("status reads = %d, want the CLI to keep polling through the pending answers", reads)
	}
	for _, want := range []string{"https://auth.example/codex/device", "ABCD-EFGHJ", "cred_stub_1", "ChatGPT Plus · jana@unify.cz", "jana@unify.cz"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestAcceptance_CredentialLogin_NoWaitPrintsDeviceID(t *testing.T) {
	stub := &deviceLoginStubServer{pollsUntilComplete: 100}
	srv := stub.start(t)
	cfg := credStubConfig(t, srv.URL)

	out, err := runCredCLI(t, cfg, "credential", "login", "--provider", "OPENAI", "--mode", "subscription", "--no-wait")
	if err != nil {
		t.Fatalf("login --no-wait: %v\noutput: %s", err, out)
	}
	stub.mu.Lock()
	started := stub.started
	reads := stub.statusReads
	stub.mu.Unlock()
	if started["mode"] != "subscription" {
		t.Errorf("start body = %v, want mode subscription", started)
	}
	if reads != 0 {
		t.Errorf("status reads = %d, want none with --no-wait", reads)
	}
	if !strings.Contains(out, "dev_stub_1") || !strings.Contains(out, "login-status") {
		t.Errorf("output should name the device id and the follow-up command:\n%s", out)
	}

	out, err = runCredCLI(t, cfg, "credential", "login-status", "dev_stub_1")
	if err != nil {
		t.Fatalf("login-status: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "pending") {
		t.Errorf("login-status output = %s, want pending", out)
	}
}

func TestAcceptance_CredentialLogin_FailuresExitNonZero(t *testing.T) {
	cases := []struct {
		final   string
		wantMsg string
	}{
		{"denied", "refused"},
		{"expired", "expired"},
	}
	for _, tc := range cases {
		t.Run(tc.final, func(t *testing.T) {
			stub := &deviceLoginStubServer{pollsUntilComplete: 0, finalStatus: tc.final}
			srv := stub.start(t)
			cfg := credStubConfig(t, srv.URL)
			out, err := runCredCLI(t, cfg, "credential", "login", "--provider", "OPENAI")
			if err == nil {
				t.Fatalf("expected a non-zero exit for a %s sign-in; output: %s", tc.final, out)
			}
			if !strings.Contains(out, tc.wantMsg) {
				t.Errorf("output lacks %q:\n%s", tc.wantMsg, out)
			}
		})
	}
}

func TestAcceptance_CredentialLogin_ProviderRequired(t *testing.T) {
	stub := &deviceLoginStubServer{}
	srv := stub.start(t)
	cfg := credStubConfig(t, srv.URL)
	out, err := runCredCLI(t, cfg, "credential", "login")
	if err == nil || !strings.Contains(out, "--provider") {
		t.Fatalf("want a validation error naming --provider; err=%v output=%s", err, out)
	}
	if stub.started != nil {
		t.Errorf("the server was called without a provider")
	}
}
