package main

// Coverage tests for cmd_seed.go — env bootstrapping (loadDotEnvLocal /
// bridgeServerFromPort), the bootstrap/auth phase helpers, and a full
// runSeed happy-path drive against a stubbed API. Serial; cov* helpers in
// cmd_skill_cov_test.go.

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const (
	covSeedUserID = "cuser0123456789abcdefghi"
	covSeedWSID   = "cseedws0123456789abcdefg"
)

// covUnsetenv removes an env var for the test and restores the original
// value (or absence) afterwards. t.Setenv(key, "") would not work here:
// loadDotEnvLocal distinguishes "set to empty" from "unset" via LookupEnv.
func covUnsetenv(t *testing.T, key string) {
	t.Helper()
	orig, had := os.LookupEnv(key)
	_ = os.Unsetenv(key)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, orig)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

// ─── bridgeServerFromPort ────────────────────────────────────────────────

func TestBridgeServerFromPortCov(t *testing.T) {
	t.Run("no-op when CREWSHIP_SERVER already set", func(t *testing.T) {
		t.Setenv("CREWSHIP_SERVER", "http://example:9999")
		t.Setenv("CREWSHIP_PORT", "8081")
		bridgeServerFromPort()
		if got := os.Getenv("CREWSHIP_SERVER"); got != "http://example:9999" {
			t.Errorf("CREWSHIP_SERVER overwritten: %q", got)
		}
	})
	t.Run("no-op when no port", func(t *testing.T) {
		t.Setenv("CREWSHIP_SERVER", "") // registers restore
		covUnsetenv(t, "CREWSHIP_SERVER")
		covUnsetenv(t, "CREWSHIP_PORT")
		bridgeServerFromPort()
		if got := os.Getenv("CREWSHIP_SERVER"); got != "" {
			t.Errorf("CREWSHIP_SERVER = %q, want unset", got)
		}
	})
	t.Run("bridges port into server", func(t *testing.T) {
		t.Setenv("CREWSHIP_SERVER", "")
		covUnsetenv(t, "CREWSHIP_SERVER")
		t.Setenv("CREWSHIP_PORT", "8083")
		bridgeServerFromPort()
		if got := os.Getenv("CREWSHIP_SERVER"); got != "http://127.0.0.1:8083" {
			t.Errorf("CREWSHIP_SERVER = %q, want http://127.0.0.1:8083", got)
		}
	})
}

// ─── loadDotEnvLocal ─────────────────────────────────────────────────────

func TestLoadDotEnvLocalCov_ParsesFileWithoutOverwriting(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	content := strings.Join([]string{
		"# comment line",
		"",
		"COVSEED_PLAIN=value1",
		`COVSEED_DQUOTED="quoted value"`,
		"COVSEED_SQUOTED='single'",
		"COVSEED_PRESET=from-file",
		"malformed-line-no-equals",
		"=leading-equals-skipped",
		"CREWSHIP_PORT=8085",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"COVSEED_PLAIN", "COVSEED_DQUOTED", "COVSEED_SQUOTED"} {
		covUnsetenv(t, k)
	}
	t.Setenv("COVSEED_PRESET", "from-shell")
	covUnsetenv(t, "CREWSHIP_SERVER")
	covUnsetenv(t, "CREWSHIP_PORT")
	t.Cleanup(func() { _ = os.Unsetenv("CREWSHIP_PORT") }) // set by the loader

	loadDotEnvLocal()

	if got := os.Getenv("COVSEED_PLAIN"); got != "value1" {
		t.Errorf("COVSEED_PLAIN = %q", got)
	}
	if got := os.Getenv("COVSEED_DQUOTED"); got != "quoted value" {
		t.Errorf("double quotes not stripped: %q", got)
	}
	if got := os.Getenv("COVSEED_SQUOTED"); got != "single" {
		t.Errorf("single quotes not stripped: %q", got)
	}
	if got := os.Getenv("COVSEED_PRESET"); got != "from-shell" {
		t.Errorf("pre-set env must win over .env.local; got %q", got)
	}
	// CREWSHIP_PORT from the file must bridge into CREWSHIP_SERVER.
	if got := os.Getenv("CREWSHIP_SERVER"); got != "http://127.0.0.1:8085" {
		t.Errorf("CREWSHIP_SERVER = %q, want bridged 8085", got)
	}
}

func TestLoadDotEnvLocalCov_NoFileStillBridges(t *testing.T) {
	t.Chdir(t.TempDir())
	covUnsetenv(t, "CREWSHIP_SERVER")
	t.Setenv("CREWSHIP_PORT", "9091")

	loadDotEnvLocal()

	if got := os.Getenv("CREWSHIP_SERVER"); got != "http://127.0.0.1:9091" {
		t.Errorf("CREWSHIP_SERVER = %q, want bridge from CREWSHIP_PORT", got)
	}
}

// ─── readSetupTokenFile ──────────────────────────────────────────────────

func TestReadSetupTokenFileCov_StoragePathWins(t *testing.T) {
	dir := t.TempDir()
	token := strings.Repeat("ab12", 16)
	content := "# crewship: one-shot bootstrap token\n#\n\n" + token + "\n"
	if err := os.WriteFile(filepath.Join(dir, "initial_setup_token"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREWSHIP_STORAGE_BASE_PATH", dir)
	if got := readSetupTokenFile(); got != token {
		t.Errorf("readSetupTokenFile() = %q, want %q (comments skipped)", got, token)
	}
}

func TestReadSetupTokenFileCov_MissingEverywhere(t *testing.T) {
	covUnsetenv(t, "CREWSHIP_STORAGE_BASE_PATH")
	t.Setenv("HOME", t.TempDir()) // isolate ~/.crewship lookup
	if got := readSetupTokenFile(); got != "" {
		t.Errorf("readSetupTokenFile() = %q, want empty when no candidate files", got)
	}
}

func TestReadSetupTokenFileCov_CommentOnlyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "initial_setup_token"),
		[]byte("# only comments\n#\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREWSHIP_STORAGE_BASE_PATH", dir)
	t.Setenv("HOME", t.TempDir())
	if got := readSetupTokenFile(); got != "" {
		t.Errorf("readSetupTokenFile() = %q, want empty for comment-only file", got)
	}
}

// ─── postBootstrap ───────────────────────────────────────────────────────

func TestPostBootstrapCov_Headers(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/bootstrap", clitest.JSONResponse(201, map[string]string{"user_id": covSeedUserID}))

	resp, err := postBootstrap(context.Background(), s.URL(), "tok-setup-123",
		map[string]string{"email": "demo@crewship.ai"})
	if err != nil {
		t.Fatalf("postBootstrap: %v", err)
	}
	_ = resp.Body.Close()
	call := s.CallsFor("POST", "/api/v1/bootstrap")[0]
	if got := call.Headers.Get("X-Setup-Token"); got != "tok-setup-123" {
		t.Errorf("X-Setup-Token = %q", got)
	}
	if got := call.Headers.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if !strings.Contains(string(call.Body), "demo@crewship.ai") {
		t.Errorf("body = %s", call.Body)
	}

	// Without a setup token the header must be absent entirely.
	s.ResetCalls()
	resp2, err := postBootstrap(context.Background(), s.URL(), "", map[string]string{"email": "x"})
	if err != nil {
		t.Fatalf("postBootstrap (no token): %v", err)
	}
	_ = resp2.Body.Close()
	if _, has := s.CallsFor("POST", "/api/v1/bootstrap")[0].Headers["X-Setup-Token"]; has {
		t.Error("X-Setup-Token header must be omitted when token is empty")
	}
}

// ─── resolveCurrentUserID ────────────────────────────────────────────────

func TestResolveCurrentUserIDCov(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/auth/cli-token/validate", clitest.JSONResponse(200, map[string]string{
		"user_id": covSeedUserID,
	}))
	if got := resolveCurrentUserID(newAPIClient()); got != covSeedUserID {
		t.Errorf("resolveCurrentUserID = %q, want %q", got, covSeedUserID)
	}
}

func TestResolveCurrentUserIDCov_Unreachable(t *testing.T) {
	covSetup(t)
	dead := clitest.NewStubServer()
	url := dead.URL()
	dead.Close()
	client := cli.NewClient(url, "tok", covWorkspaceIDCli1)
	if got := resolveCurrentUserID(client); got != "" {
		t.Errorf("resolveCurrentUserID = %q, want empty on transport error", got)
	}
}

// ─── createOrResolve / resolveBySlug / resolveByName ─────────────────────

func TestCreateOrResolveCov_Created(t *testing.T) {
	s := covSetup(t)
	s.OnPost("/api/v1/crews", clitest.JSONResponse(201, map[string]string{"id": covCrewID}))
	id, err := createOrResolve(newAPIClient(), "/api/v1/crews",
		map[string]string{"slug": "eng"}, "/api/v1/crews", "eng")
	if err != nil {
		t.Fatalf("createOrResolve: %v", err)
	}
	if id != covCrewID {
		t.Errorf("id = %q", id)
	}
}

func TestCreateOrResolveCov_ConflictResolvesBySlug(t *testing.T) {
	s := covSetup(t)
	s.OnPost("/api/v1/crews", clitest.ErrorResponse(http.StatusConflict, "exists"))
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": "cother0123456789abcdefgh", "slug": "other"},
		{"id": covCrewID, "slug": "eng"},
	}))
	id, err := createOrResolve(newAPIClient(), "/api/v1/crews",
		map[string]string{"slug": "eng"}, "/api/v1/crews", "eng")
	if err != nil {
		t.Fatalf("createOrResolve on 409: %v", err)
	}
	if id != covCrewID {
		t.Errorf("id = %q, want resolved existing id", id)
	}
}

func TestCreateOrResolveCov_HardError(t *testing.T) {
	s := covSetup(t)
	s.OnPost("/api/v1/crews", clitest.ErrorResponse(422, "validation failed"))
	_, err := createOrResolve(newAPIClient(), "/api/v1/crews",
		map[string]string{}, "/api/v1/crews", "eng")
	if err == nil || !strings.Contains(err.Error(), "validation failed") {
		t.Errorf("want 422 surfaced; got %v", err)
	}
}

func TestResolveBySlugCov_NotFound(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))
	_, err := resolveBySlug(newAPIClient(), "/api/v1/crews", "ghost")
	if err == nil || !strings.Contains(err.Error(), `slug "ghost" not found`) {
		t.Errorf("want not-found; got %v", err)
	}
}

func TestResolveByNameCov(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []map[string]any{
		{"id": covCredID, "name": "Anthropic API Key"},
		{"id": "x", "slug": "no-name-key"},
	}))
	id, err := resolveByName(newAPIClient(), "/api/v1/credentials", "Anthropic API Key")
	if err != nil {
		t.Fatalf("resolveByName: %v", err)
	}
	if id != covCredID {
		t.Errorf("id = %q", id)
	}

	if _, err := resolveByName(newAPIClient(), "/api/v1/credentials", "Ghost"); err == nil ||
		!strings.Contains(err.Error(), `name "Ghost" not found`) {
		t.Errorf("want not-found; got %v", err)
	}

	s.OnGet("/api/v1/credentials", clitest.TextResponse(200, "not json"))
	if _, err := resolveByName(newAPIClient(), "/api/v1/credentials", "Anything"); err == nil {
		t.Error("want JSON parse error; got nil")
	}
}

// ─── seedBootstrap error paths ───────────────────────────────────────────

// covSeedEnv pins every env knob the seed path consults so the test can't
// pick up the developer's real shell/config state.
func covSeedEnv(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir()) // no .env.local
	t.Setenv("CREWSHIP_CONFIG", filepath.Join(t.TempDir(), "cli-config.yaml"))
	t.Setenv("HOME", t.TempDir())
	covUnsetenv(t, "CREWSHIP_SERVER")
	covUnsetenv(t, "CREWSHIP_PORT")
	covUnsetenv(t, "CREWSHIP_WORKSPACE")
	covUnsetenv(t, "CREWSHIP_STORAGE_BASE_PATH")
	covUnsetenv(t, "SEED_ANTHROPIC_API_KEY")
	covUnsetenv(t, "SEED_GOOGLE_EMAIL")
	covUnsetenv(t, "SEED_GOOGLE_PASSWORD")
}

func TestSeedBootstrapCov_Unreachable(t *testing.T) {
	saveCLIState(t)
	covSeedEnv(t)
	dead := clitest.NewStubServer()
	url := dead.URL()
	dead.Close()
	flagServer = url
	cliCfg = &cli.CLIConfig{}

	_, _, err := seedBootstrap(context.Background(), "pw")
	if err == nil || !strings.Contains(err.Error(), "bootstrap request failed") {
		t.Errorf("want transport failure; got %v", err)
	}
}

func TestSeedBootstrapCov_HardFailure(t *testing.T) {
	saveCLIState(t)
	covSeedEnv(t)
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/bootstrap", clitest.ErrorResponse(500, "kaboom"))
	flagServer = s.URL()
	cliCfg = &cli.CLIConfig{}

	_, _, err := seedBootstrap(context.Background(), "pw")
	if err == nil || !strings.Contains(err.Error(), "bootstrap failed: HTTP 500") {
		t.Errorf("want hard failure; got %v", err)
	}
}

func TestSeedBootstrapCov_AlreadyInitializedNoAuth(t *testing.T) {
	saveCLIState(t)
	covSeedEnv(t)
	s := clitest.NewStubServer()
	defer s.Close()
	// Server's real shape: 403 + "Already initialized — ..." sentinel.
	s.OnPost("/api/v1/bootstrap", clitest.ErrorResponse(403, "Already initialized — run crewship login"))
	flagServer = s.URL()
	cliCfg = &cli.CLIConfig{} // no token → requireAuth fails

	_, _, err := seedBootstrap(context.Background(), "pw")
	if err == nil || !strings.Contains(err.Error(), "DB already initialized") ||
		!strings.Contains(err.Error(), "not logged in") {
		t.Errorf("want already-initialized + login hint; got %v", err)
	}
}

func TestSeedBootstrapCov_ConflictFallsBackToExistingAuth(t *testing.T) {
	saveCLIState(t)
	covSeedEnv(t)
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/bootstrap", clitest.ErrorResponse(http.StatusConflict, "exists"))
	s.OnGet("/api/v1/auth/cli-token/validate", clitest.JSONResponse(200, map[string]string{
		"user_id": covSeedUserID,
	}))
	flagServer = s.URL()
	cliCfg = &cli.CLIConfig{Token: "existing-token", Workspace: covWorkspaceIDCli1}

	client, userID, err := seedBootstrap(context.Background(), "pw")
	if err != nil {
		t.Fatalf("seedBootstrap: %v", err)
	}
	if client == nil {
		t.Fatal("client is nil")
	}
	if userID != covSeedUserID {
		t.Errorf("userID = %q, want %q", userID, covSeedUserID)
	}
}

// ─── runSeed ─────────────────────────────────────────────────────────────

// covSeedStub builds a stub API that satisfies the full default seed flow:
// bootstrap 201, list endpoints empty, provisioning triggers 202, and a
// permissive fallback that answers every create POST with a CUID id.
func covSeedStub(t *testing.T) *clitest.StubServer {
	t.Helper()
	s := clitest.NewStubServer()
	t.Cleanup(s.Close)
	s.OnPost("/api/v1/bootstrap", clitest.JSONResponse(201, map[string]string{
		"user_id":      covSeedUserID,
		"workspace_id": covSeedWSID,
		"cli_token":    "tok-seeded-123",
	}))
	s.OnGet("/api/v1/skills", clitest.JSONResponse(200, []map[string]string{}))
	s.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []map[string]string{}))
	s.SetFallback(func(r *http.Request, _ []byte) (int, []byte, string) {
		// Provision triggers only accept 200/202/409.
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/provision") {
			return http.StatusAccepted, []byte(`{"status":"started"}`), "application/json"
		}
		// Generic create/update: a CUID-shaped id satisfies every decoder
		// in the seed path (createOrResolve, skills import, schedules, …).
		return 200, []byte(`{"id":"cseeded0123456789abcdefg","skill_id":"cseeded0123456789abcdefg","updated":1}`), "application/json"
	})
	return s
}

func covSetupRunSeed(t *testing.T, s *clitest.StubServer) {
	t.Helper()
	saveCLIState(t)
	covSeedEnv(t)
	flagServer = s.URL()
	flagWorkspace = ""
	cliCfg = &cli.CLIConfig{}
	seedCmd.SetContext(context.Background())
	t.Cleanup(func() { seedCmd.SetContext(context.Background()) })
}

func TestRunSeedCov_HappyPathFreshBootstrap(t *testing.T) {
	s := covSeedStub(t)
	covSetupRunSeed(t, s)
	covSetFlag(t, seedCmd, "skip-issues", "true")

	out, err := covCaptureStdout(t, func() error {
		return seedCmd.RunE(seedCmd, nil)
	})
	if err != nil {
		t.Fatalf("runSeed: %v\noutput:\n%s", err, out)
	}

	// Default dev password is announced and sent in the bootstrap body.
	if !strings.Contains(out, "Using dev default admin password: password123") {
		t.Errorf("missing default-password notice:\n%s", out)
	}
	if !strings.Contains(out, "Seed complete:") {
		t.Errorf("missing summary line:\n%s", out)
	}
	boot := s.CallsFor("POST", "/api/v1/bootstrap")
	if len(boot) != 1 {
		t.Fatalf("bootstrap calls = %d, want 1", len(boot))
	}
	body := covJSONBody(t, boot[0].Body)
	if body["email"] != "demo@crewship.ai" || body["password"] != "password123" {
		t.Errorf("bootstrap body = %v", body)
	}

	// Core create phases must have run.
	if n := len(s.CallsFor("POST", "/api/v1/crews")); n == 0 {
		t.Error("no crews created")
	}
	if n := len(s.CallsFor("POST", "/api/v1/agents")); n == 0 {
		t.Error("no agents created")
	}
	if n := len(s.CallsFor("POST", "/api/v1/credentials")); n == 0 {
		t.Error("no credentials created")
	}
	if n := len(s.CallsFor("POST", "/api/v1/crew-connections")); n == 0 {
		t.Error("no crew connections created")
	}

	// --skip-issues must keep the issue endpoints untouched.
	for _, c := range s.Calls() {
		if strings.Contains(c.Path, "/issues") {
			t.Errorf("issue endpoint hit despite --skip-issues: %s %s", c.Method, c.Path)
		}
	}

	// The fresh bootstrap must persist credentials to CREWSHIP_CONFIG.
	cfgBytes, readErr := os.ReadFile(os.Getenv("CREWSHIP_CONFIG"))
	if readErr != nil {
		t.Fatalf("config not written: %v", readErr)
	}
	cfg := string(cfgBytes)
	if !strings.Contains(cfg, "tok-seeded-123") || !strings.Contains(cfg, covSeedWSID) {
		t.Errorf("saved config missing token/workspace:\n%s", cfg)
	}
}

func TestRunSeedCov_CustomPasswordAndBootstrapFailure(t *testing.T) {
	s := clitest.NewStubServer()
	t.Cleanup(s.Close)
	s.OnPost("/api/v1/bootstrap", clitest.ErrorResponse(500, "db locked"))
	covSetupRunSeed(t, s)
	covSetFlag(t, seedCmd, "password", "supersecret")

	out, err := covCaptureStdout(t, func() error {
		return seedCmd.RunE(seedCmd, nil)
	})
	if err == nil || !strings.Contains(err.Error(), "bootstrap failed: HTTP 500") {
		t.Fatalf("want bootstrap failure; got %v", err)
	}
	if strings.Contains(out, "Using dev default admin password") {
		t.Errorf("--password must suppress the default-password notice:\n%s", out)
	}
	body := covJSONBody(t, s.CallsFor("POST", "/api/v1/bootstrap")[0].Body)
	if body["password"] != "supersecret" {
		t.Errorf("password = %v, want supersecret", body["password"])
	}
}

func TestRunSeedCov_NukeRefusedNonInteractive(t *testing.T) {
	s := covSeedStub(t)
	s.OnGet("/api/v1/workspaces", clitest.JSONResponse(200, []map[string]string{
		{"id": covSeedWSID, "name": "Demo", "slug": "demo"},
	}))
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{}))
	covSetupRunSeed(t, s)
	covSetFlag(t, seedCmd, "nuke", "true")
	// --yes deliberately NOT set; the test binary has no TTY, so the gate
	// must refuse rather than wipe.

	_, err := covCaptureStdout(t, func() error {
		return seedCmd.RunE(seedCmd, nil)
	})
	if err == nil || !strings.Contains(err.Error(), "refusing to nuke in a non-interactive session") {
		t.Fatalf("want non-interactive nuke refusal; got %v", err)
	}
	// The wipe must never have started: nothing may be deleted.
	for _, c := range s.Calls() {
		if c.Method == http.MethodDelete {
			t.Errorf("DELETE issued despite refused confirmation: %s", c.Path)
		}
	}
}
