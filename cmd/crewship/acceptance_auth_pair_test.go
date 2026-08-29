package main

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
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
)

// Acceptance for `crewship auth pair` (#2147), driven through the BUILT
// BINARY rather than by calling RunE in-process — same reasoning as
// acceptance_oauth_connect_test.go: the wire contract against
// POST /api/v1/auth/pair/start and GET /api/v1/auth/pair/poll is what
// matters, and cli_route_contract_test.go says nothing about a command that
// did not exist until this file's red run forced it into being.
//
// Before this, POST /api/v1/auth/pair/redeem had a CLI caller
// (`crewship login --pair --code=…`, cmd_login.go) but /start and /poll had
// none — you could redeem a pairing code the CLI could not issue. `auth pair`
// is the issuing side: it mints a code, prints the snippet to paste on the
// OTHER machine, and (by default) polls until that machine redeems it.

type authPairStubServer struct {
	mu sync.Mutex
	// startBody is the decoded POST /api/v1/auth/pair/start body, nil until
	// called.
	startBody map[string]any
	startHits int
	// pollStatus is what GET /api/v1/auth/pair/poll reports. Tests flip it to
	// simulate the code being redeemed elsewhere.
	pollStatus string
	pollHints  string
	pollHits   int
	// pollCodesSeen records every ?code= value poll was asked about, so a
	// case can assert the CLI polls the SAME code /start minted.
	pollCodesSeen []string
}

func (s *authPairStubServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	if s.pollStatus == "" {
		s.pollStatus = "pending"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/pair/start":
			raw, _ := io.ReadAll(r.Body)
			body := map[string]any{}
			_ = json.Unmarshal(raw, &body)
			s.mu.Lock()
			s.startBody = body
			s.startHits++
			s.mu.Unlock()
			_, _ = w.Write([]byte(`{"code":"K3F9-X2NM","expires_at":"2026-01-01T00:10:00Z"}`))

		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/auth/pair/poll":
			s.mu.Lock()
			s.pollHits++
			s.pollCodesSeen = append(s.pollCodesSeen, r.URL.Query().Get("code"))
			status := s.pollStatus
			hint := s.pollHints
			s.mu.Unlock()
			resp := map[string]any{"status": status, "expires_at": "2026-01-01T00:10:00Z"}
			if hint != "" {
				resp["adapter_hint"] = hint
			}
			b, _ := json.Marshal(resp)
			_, _ = w.Write(b)

		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no stub for ` + r.Method + " " + r.URL.Path + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *authPairStubServer) setPollStatus(status string) {
	s.mu.Lock()
	s.pollStatus = status
	s.mu.Unlock()
}

func (s *authPairStubServer) getPollHits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pollHits
}

func (s *authPairStubServer) getStartBody(t *testing.T) map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.startBody == nil {
		t.Fatal("POST /api/v1/auth/pair/start was never called")
	}
	return s.startBody
}

func authPairStubConfig(t *testing.T, serverURL string) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	// No workspace: pairing is a self-scoped account action, same tier as
	// `auth passwd` / `auth avatar`.
	cfg := "server: " + serverURL + "\ntoken: fake-token\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

func runAuthPairCLI(t *testing.T, cfgPath string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// `--no-wait` is the scripted half: issue the code, print it and the paste
// snippet, return immediately without polling.
func TestAcceptance_AuthPairNoWaitPrintsCodeAndSnippet(t *testing.T) {
	stub := &authPairStubServer{}
	srv := stub.start(t)
	cfg := authPairStubConfig(t, srv.URL)

	out, err := runAuthPairCLI(t, cfg, "auth", "pair", "--no-wait")
	if err != nil {
		t.Fatalf("pair --no-wait: %v\noutput: %s", err, out)
	}

	stub.getStartBody(t) // fails the test if /start was never reached

	if !strings.Contains(out, "K3F9-X2NM") {
		t.Errorf("output does not print the issued code:\n%s", out)
	}
	if !strings.Contains(out, "crewship login") || !strings.Contains(out, "--pair") {
		t.Errorf("output does not print the paste-elsewhere snippet naming `login --pair`:\n%s", out)
	}
	if stub.getPollHits() != 0 {
		t.Errorf("--no-wait polled the code %d times; it must return immediately", stub.getPollHits())
	}
}

// --adapter is telemetry-only (per internal/api/cli_pair.go), but it still
// has to reach the server so the redeeming side's hint round-trips.
func TestAcceptance_AuthPairSendsAdapterHint(t *testing.T) {
	stub := &authPairStubServer{}
	srv := stub.start(t)
	cfg := authPairStubConfig(t, srv.URL)

	out, err := runAuthPairCLI(t, cfg, "auth", "pair", "--no-wait", "--adapter", "CLAUDE_CODE")
	if err != nil {
		t.Fatalf("pair --adapter: %v\noutput: %s", err, out)
	}
	body := stub.getStartBody(t)
	if body["adapter_hint"] != "CLAUDE_CODE" {
		t.Errorf("adapter_hint = %v, want CLAUDE_CODE", body["adapter_hint"])
	}
}

// The default is to wait, because a code sitting unredeemed in a terminal
// nobody is watching is not useful — the operator wants to know the other
// machine actually picked it up.
func TestAcceptance_AuthPairWaitsForRedemption(t *testing.T) {
	stub := &authPairStubServer{}
	srv := stub.start(t)
	cfg := authPairStubConfig(t, srv.URL)

	go func() {
		for stub.getPollHits() < 1 {
			time.Sleep(20 * time.Millisecond)
		}
		stub.setPollStatus("consumed")
	}()

	out, err := runAuthPairCLI(t, cfg, "auth", "pair",
		"--timeout", "20s", "--poll-interval", "100ms")
	if err != nil {
		t.Fatalf("pair: %v\noutput: %s", err, out)
	}
	if stub.getPollHits() == 0 {
		t.Error("pair never polled for redemption")
	}
	stub.mu.Lock()
	for _, code := range stub.pollCodesSeen {
		if code != "K3F9-X2NM" {
			t.Errorf("polled code %q, want the code /start minted (K3F9-X2NM)", code)
		}
	}
	stub.mu.Unlock()
	if !strings.Contains(strings.ToLower(out), "paired") {
		t.Errorf("pair did not report the code as redeemed/paired:\n%s", out)
	}
}

// …and when it never gets redeemed, that is a non-zero exit, not a tick — a
// silent "success" here would hide that no second CLI ever picked up the
// code.
func TestAcceptance_AuthPairTimeoutIsNotSuccess(t *testing.T) {
	stub := &authPairStubServer{}
	srv := stub.start(t)
	cfg := authPairStubConfig(t, srv.URL)

	out, err := runAuthPairCLI(t, cfg, "auth", "pair",
		"--timeout", "1s", "--poll-interval", "100ms")
	if err == nil {
		t.Fatalf("pair reported success for a code nobody redeemed\noutput: %s", out)
	}
	if strings.Contains(strings.ToLower(out), "paired as") {
		t.Errorf("pair claims a redemption that never happened:\n%s", out)
	}
}

// A code that expires mid-wait (server flips pending -> expired once the TTL
// passes) must fail fast, not spin out the rest of the timeout budget.
func TestAcceptance_AuthPairFailsFastWhenCodeExpires(t *testing.T) {
	stub := &authPairStubServer{}
	srv := stub.start(t)
	cfg := authPairStubConfig(t, srv.URL)

	go func() {
		for stub.getPollHits() < 1 {
			time.Sleep(20 * time.Millisecond)
		}
		stub.setPollStatus("expired")
	}()

	out, err := runAuthPairCLI(t, cfg, "auth", "pair",
		"--timeout", "30s", "--poll-interval", "100ms")
	if err == nil {
		t.Fatalf("pair succeeded against an expired code\noutput: %s", out)
	}
	if !strings.Contains(strings.ToLower(out), "expired") {
		t.Errorf("expired-code failure does not say so:\n%s", out)
	}
	// Failing fast: give it a moment to unwind, then check it didn't grind
	// through the whole 30s budget polling a code that already died.
	if stub.getPollHits() > 5 {
		t.Errorf("polled an expired code %d times; an expired status is terminal, not transient", stub.getPollHits())
	}
}

// --format json must parse, per the repo's CLI convention, and must carry
// the code even on the --no-wait path (the code is the entire point of the
// command's output).
func TestAcceptance_AuthPairJSONOutput(t *testing.T) {
	stub := &authPairStubServer{}
	srv := stub.start(t)
	cfg := authPairStubConfig(t, srv.URL)

	out, err := runAuthPairCLI(t, cfg, "auth", "pair", "--no-wait", "--format", "json")
	if err != nil {
		t.Fatalf("pair --format json: %v\noutput: %s", err, out)
	}
	var got struct {
		Code      string `json:"code"`
		ExpiresAt string `json:"expires_at"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("pair --format json is not JSON: %v\noutput: %s", err, out)
	}
	if got.Code != "K3F9-X2NM" {
		t.Errorf("code = %q, want K3F9-X2NM", got.Code)
	}
}

// requireAuth gates the command like every other self-service account
// command (auth passwd, auth avatar) — no token, no pairing.
func TestAcceptance_AuthPairRequiresAuth(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	if err := os.WriteFile(cfgPath, []byte("server: http://127.0.0.1:1\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	out, err := runAuthPairCLI(t, cfgPath, "auth", "pair", "--no-wait")
	if err == nil {
		t.Fatalf("pair succeeded with no token configured\noutput: %s", out)
	}
	if got := exitCodeOf(t, err); got != cli.ExitAuth {
		t.Errorf("exit code = %d, want ExitAuth (%d)\noutput: %s", got, cli.ExitAuth, out)
	}
}
