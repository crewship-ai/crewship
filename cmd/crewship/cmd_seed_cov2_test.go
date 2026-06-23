package main

// Second-pass coverage for cmd_seed.go: the nuke + optional-phase runSeed
// variants (--nuke --yes, --with-users, --with-memory, --wait-provision,
// issues enabled), phase-failure aborts, and the remaining seedBootstrap /
// helper error branches. Helpers in cmd_skill_cov_test.go / cov2 /
// cmd_seed_cov_test.go.

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

func TestRunSeedCov2_CanceledContext(t *testing.T) {
	s := covSeedStub(t)
	covSetupRunSeed(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	seedCmd.SetContext(ctx)

	_, err := covCaptureStdout(t, func() error {
		return seedCmd.RunE(seedCmd, nil)
	})
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("want context cancellation; got %v", err)
	}
	if n := len(s.Calls()); n != 0 {
		t.Errorf("canceled seed must not call the API; got %d calls", n)
	}
}

// TestRunSeedCov2_NukeWithExtras drives the widest runSeed configuration
// that stays deterministic without Docker: --nuke --yes wipes (against empty
// lists), --with-users mints RBAC fixtures, --with-memory fails fast and
// non-fatally (no CREWSHIP_STORAGE_BASE_PATH), --wait-provision polls a
// status stub that reports completed immediately, and issues stay ENABLED.
func TestRunSeedCov2_NukeWithExtras(t *testing.T) {
	s := covSeedStub(t)
	// Workspace identity + entity lists consumed by confirmNuke + seedNuke.
	s.OnGet("/api/v1/workspaces", clitest.JSONResponse(200, []map[string]string{
		{"id": covSeedWSID, "name": "Demo", "slug": "demo"},
	}))
	empty := clitest.JSONResponse(200, []map[string]string{})
	for _, p := range []string{
		"/api/v1/issues", "/api/v1/projects", "/api/v1/labels",
		"/api/v1/agents", "/api/v1/crews", "/api/v1/integrations/crews",
		"/api/v1/workspaces/" + covSeedWSID + "/pipeline-webhooks",
		"/api/v1/workspaces/" + covSeedWSID + "/pipeline-schedules",
		"/api/v1/workspaces/" + covSeedWSID + "/pipelines",
	} {
		s.OnGet(p, empty)
	}
	// Provision status: completed on the first poll so --wait-provision
	// returns without ticking the 3 s poll loop.
	s.OnGet("/api/v1/crews/cseeded0123456789abcdefg/provision",
		clitest.JSONResponse(200, map[string]string{"status": "completed"}))

	covSetupRunSeed(t, s)
	covSetFlag(t, seedCmd, "nuke", "true")
	covSetFlag(t, seedCmd, "yes", "true")
	covSetFlag(t, seedCmd, "with-users", "true")
	covSetFlag(t, seedCmd, "with-memory", "true")
	covSetFlag(t, seedCmd, "wait-provision", "true")

	out, err := covCaptureStdout(t, func() error {
		return seedCmd.RunE(seedCmd, nil)
	})
	if err != nil {
		t.Fatalf("runSeed: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "Seed complete:") {
		t.Errorf("missing summary:\n%s", out)
	}
	// Nuke ran (issue listing consulted) and was gated by --yes.
	if n := len(s.CallsFor("GET", "/api/v1/issues")); n == 0 {
		t.Error("nuke never listed issues")
	}
	// --with-memory ran: it resolves the storage base path (falls back
	// under the test-scoped $HOME) and writes the demo tier files there.
	if !strings.Contains(out, "Seeding agent memory tiers...") {
		t.Errorf("expected memory phase to run:\n%s", out)
	}
	// --wait-provision polled the status endpoint.
	if n := len(s.CallsFor("GET", "/api/v1/crews/cseeded0123456789abcdefg/provision")); n == 0 {
		t.Error("wait-provision never polled status")
	}
	// Issues are enabled in this variant: label/issue creates must land.
	sawIssuePost := false
	for _, c := range s.Calls() {
		if c.Method == http.MethodPost && strings.HasSuffix(c.Path, "/issues") {
			sawIssuePost = true
			break
		}
	}
	if !sawIssuePost {
		t.Error("issue seeding did not POST any issues")
	}
}

func TestRunSeedCov2_CrewPhaseFailureAborts(t *testing.T) {
	s := covSeedStub(t)
	s.OnPost("/api/v1/crews", clitest.ErrorResponse(500, "crews table locked"))
	covSetupRunSeed(t, s)

	_, err := covCaptureStdout(t, func() error {
		return seedCmd.RunE(seedCmd, nil)
	})
	if err == nil || !strings.Contains(err.Error(), "crews table locked") {
		t.Fatalf("want crew-phase failure; got %v", err)
	}
	// Later phases must never run after a fatal crew failure.
	if n := len(s.CallsFor("POST", "/api/v1/agents")); n != 0 {
		t.Errorf("agents created after fatal crew failure: %d", n)
	}
}

func TestRunSeedCov2_AgentPhaseFailureAborts(t *testing.T) {
	s := covSeedStub(t)
	s.OnPost("/api/v1/agents", clitest.ErrorResponse(500, "agents broke"))
	covSetupRunSeed(t, s)

	_, err := covCaptureStdout(t, func() error {
		return seedCmd.RunE(seedCmd, nil)
	})
	if err == nil || !strings.Contains(err.Error(), "agents broke") {
		t.Fatalf("want agent-phase failure; got %v", err)
	}
}

func TestRunSeedCov2_CredentialPhaseFailureAborts(t *testing.T) {
	s := covSeedStub(t)
	s.OnPost("/api/v1/credentials", clitest.ErrorResponse(500, "vault sealed"))
	covSetupRunSeed(t, s)

	_, err := covCaptureStdout(t, func() error {
		return seedCmd.RunE(seedCmd, nil)
	})
	if err == nil || !strings.Contains(err.Error(), "anthropic credential") {
		t.Fatalf("want credential-phase failure; got %v", err)
	}
}

// TestRunSeedCov2_TestBackupProvisionFails: --test-backup implies
// --wait-provision; when the wait reports a failed provision the command
// must surface the provisioning error instead of starting the self-test
// (which would mask it behind a warmup failure).
func TestRunSeedCov2_TestBackupProvisionFails(t *testing.T) {
	s := covSeedStub(t)
	s.OnGet("/api/v1/crews/cseeded0123456789abcdefg/provision",
		clitest.JSONResponse(200, map[string]string{"status": "failed", "error": "image pull denied"}))
	covSetupRunSeed(t, s)
	covSetFlag(t, seedCmd, "skip-issues", "true")
	covSetFlag(t, seedCmd, "test-backup", "true")

	_, err := covCaptureStdout(t, func() error {
		return seedCmd.RunE(seedCmd, nil)
	})
	if err == nil || !strings.Contains(err.Error(), "failed to provision") {
		t.Fatalf("want provisioning failure surfaced; got %v", err)
	}
}

// TestRunSeedCov2_TestBackupNoStartedTargets: when every provision trigger
// fails, --test-backup must exit non-zero instead of silently skipping the
// self-test (CI would otherwise treat broken provisioning as green).
func TestRunSeedCov2_TestBackupNoStartedTargets(t *testing.T) {
	s := covSeedStub(t)
	s.OnPost("/api/v1/crews/cseeded0123456789abcdefg/provision",
		clitest.ErrorResponse(500, "provisioner down"))
	covSetupRunSeed(t, s)
	covSetFlag(t, seedCmd, "skip-issues", "true")
	covSetFlag(t, seedCmd, "test-backup", "true")

	_, err := covCaptureStdout(t, func() error {
		return seedCmd.RunE(seedCmd, nil)
	})
	if err == nil || !strings.Contains(err.Error(), "no crew successfully started provisioning") {
		t.Fatalf("want no-started-targets failure; got %v", err)
	}
}

// TestRunSeedCov2_WaitProvisionTriggerErrors: plain --wait-provision with a
// dead trigger endpoint must return the joined trigger error at the end so
// scripted callers exit non-zero.
func TestRunSeedCov2_WaitProvisionTriggerErrors(t *testing.T) {
	s := covSeedStub(t)
	s.OnPost("/api/v1/crews/cseeded0123456789abcdefg/provision",
		clitest.ErrorResponse(500, "provisioner down"))
	covSetupRunSeed(t, s)
	covSetFlag(t, seedCmd, "skip-issues", "true")
	covSetFlag(t, seedCmd, "wait-provision", "true")

	_, err := covCaptureStdout(t, func() error {
		return seedCmd.RunE(seedCmd, nil)
	})
	if err == nil || !strings.Contains(err.Error(), "provisioning trigger failed") {
		t.Fatalf("want trigger error surfaced in sync mode; got %v", err)
	}
}

// TestRunSeedCov2_CancelMidConnections: cancelling the context while the
// crew-connections fan-out runs must (a) log the phase error non-fatally
// and (b) stop the seed at the next inter-phase context check.
func TestRunSeedCov2_CancelMidConnections(t *testing.T) {
	s := covSeedStub(t)
	ctx, cancel := context.WithCancel(context.Background())
	s.OnPost("/api/v1/crew-connections", func(_ *http.Request, _ []byte) (int, []byte, string) {
		cancel() // seedCrewConnections hits its ctx check on the next pair
		return 201, []byte(`{"id":"cconn0123456789abcdefgh"}`), "application/json"
	})
	covSetupRunSeed(t, s)
	seedCmd.SetContext(ctx)
	covSetFlag(t, seedCmd, "skip-issues", "true")

	out, err := covCaptureStdout(t, func() error {
		return seedCmd.RunE(seedCmd, nil)
	})
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("want cancellation to abort the seed; got %v", err)
	}
	if !strings.Contains(out, "Crew connection seeding hit an error (continuing)") {
		t.Errorf("connection-phase error must be logged non-fatally:\n%s", out)
	}
	// Agents phase comes after the cancelled checkpoint — must not run.
	if n := len(s.CallsFor("POST", "/api/v1/agents")); n != 0 {
		t.Errorf("agents created after cancellation: %d", n)
	}
}

// TestRunSeedCov2_AsyncTriggerErrorIsNonFatal: in default (async) mode a
// failed provisioning trigger is reported as a note but must NOT fail the
// seed — that matches the fire-and-forget contract of Phase 2b.
func TestRunSeedCov2_AsyncTriggerErrorIsNonFatal(t *testing.T) {
	s := covSeedStub(t)
	s.OnPost("/api/v1/crews/cseeded0123456789abcdefg/provision",
		clitest.ErrorResponse(500, "provisioner down"))
	covSetupRunSeed(t, s)
	covSetFlag(t, seedCmd, "skip-issues", "true")

	out, err := covCaptureStdout(t, func() error {
		return seedCmd.RunE(seedCmd, nil)
	})
	if err != nil {
		t.Fatalf("async trigger failure must not abort the seed: %v", err)
	}
	if !strings.Contains(out, "Note: provisioning trigger reported errors (continuing)") {
		t.Errorf("missing non-fatal trigger note:\n%s", out)
	}
}

// TestRunSeedCov2_NukeFailureAborts: when the wipe itself reports failures
// (here: every list endpoint returns undecodable bodies), runSeed must stop
// before seeding on top of a half-nuked workspace.
func TestRunSeedCov2_NukeFailureAborts(t *testing.T) {
	s := covSeedStub(t)
	s.OnGet("/api/v1/workspaces", clitest.JSONResponse(200, []map[string]string{
		{"id": covSeedWSID, "name": "Demo", "slug": "demo"},
	}))
	// GET /api/v1/issues (and the other nuke lists) fall through to the
	// fallback OBJECT body, which fails the []item decode → failures.
	covSetupRunSeed(t, s)
	covSetFlag(t, seedCmd, "nuke", "true")
	covSetFlag(t, seedCmd, "yes", "true")

	_, err := covCaptureStdout(t, func() error {
		return seedCmd.RunE(seedCmd, nil)
	})
	if err == nil || !strings.Contains(err.Error(), "workspace cleanup had") {
		t.Fatalf("want nuke failure to abort; got %v", err)
	}
	if n := len(s.CallsFor("POST", "/api/v1/crews")); n != 0 {
		t.Errorf("crews seeded after failed nuke: %d", n)
	}
}

// covCancelOn returns a stub Handler that cancels the given context and
// answers 200 — used to abort the seed mid-phase deterministically.
func covCancelOn(cancel context.CancelFunc) clitest.Handler {
	return func(_ *http.Request, _ []byte) (int, []byte, string) {
		cancel()
		return 200, []byte(`{"id":"cseeded0123456789abcdefg"}`), "application/json"
	}
}

// TestRunSeedCov2_CancelMidPhases drives one runSeed per phase-local
// cancellation point and pins (a) which phases log non-fatally and (b) that
// the seed stops at the following inter-phase checkpoint.
func TestRunSeedCov2_CancelMidPhases(t *testing.T) {
	cases := []struct {
		name      string
		route     string // POST route whose first hit cancels the context
		withUsers bool
		skipIss   bool
		wantOut   string // non-fatal log line expected (empty = none)
	}{
		{"rbac users", "/api/v1/auth/signup", true, true,
			"RBAC user seeding hit an error (continuing)"},
		{"skills import", "/api/v1/workspaces/" + covSeedWSID + "/skills/import", false, true, ""},
		{"routines", "/api/v1/workspaces/" + covSeedWSID + "/pipelines/save", false, true,
			"Routine seeding hit an error (continuing)"},
		// Schedules seed only one demo cron, so the in-phase loop never
		// re-checks ctx after the cancel — the abort surfaces at the issues
		// checkpoint, which therefore must stay enabled here.
		{"schedules", "/api/v1/workspaces/" + covSeedWSID + "/pipeline-schedules", false, false, ""},
		{"issues labels", "/api/v1/labels", false, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := covSeedStub(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.OnPost(tc.route, covCancelOn(cancel))
			covSetupRunSeed(t, s)
			seedCmd.SetContext(ctx)
			if tc.withUsers {
				covSetFlag(t, seedCmd, "with-users", "true")
			}
			if tc.skipIss {
				covSetFlag(t, seedCmd, "skip-issues", "true")
			}

			out, err := covCaptureStdout(t, func() error {
				return seedCmd.RunE(seedCmd, nil)
			})
			if err == nil || !strings.Contains(err.Error(), "context canceled") {
				t.Fatalf("want cancellation surfaced; got %v", err)
			}
			if tc.wantOut != "" && !strings.Contains(out, tc.wantOut) {
				t.Errorf("missing %q in output:\n%s", tc.wantOut, out)
			}
		})
	}
}

func TestCreateOrResolveCov2_BadJSON(t *testing.T) {
	s := covSetup(t)
	s.OnPost("/api/v1/crews", clitest.TextResponse(201, "nope"))
	if _, err := createOrResolve(newAPIClient(), "/api/v1/crews",
		map[string]string{}, "/api/v1/crews", "eng"); err == nil {
		t.Error("want decode error; got nil")
	}
}

func TestReadSetupTokenFileCov2_OverlongLineWarns(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("y", 80*1024)
	if err := os.WriteFile(filepath.Join(dir, "initial_setup_token"),
		[]byte(long+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREWSHIP_STORAGE_BASE_PATH", dir)
	t.Setenv("HOME", t.TempDir())

	out, _ := covCaptureStdout(t, func() error {
		if got := readSetupTokenFile(); got != "" {
			t.Errorf("token = %q, want empty on scanner failure", got)
		}
		return nil
	})
	if !strings.Contains(out, "warning: failed reading") {
		t.Errorf("want scanner warning; got %q", out)
	}
}

func TestLoadDotEnvLocalCov2_OverlongLineWarns(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	// bufio.Scanner's default token cap is 64 KiB; a longer line makes
	// Scan() fail and must hit the warning branch, not crash the seed.
	long := "COVSEED_HUGE=" + strings.Repeat("x", 80*1024)
	if err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte(long+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	covUnsetenv(t, "COVSEED_HUGE")
	covUnsetenv(t, "CREWSHIP_SERVER")
	covUnsetenv(t, "CREWSHIP_PORT")

	out, _ := covCaptureStdout(t, func() error {
		loadDotEnvLocal()
		return nil
	})
	if !strings.Contains(out, "warning: failed reading .env.local") {
		t.Errorf("want scanner-error warning; got %q", out)
	}
}

// ─── seedBootstrap second pass ───────────────────────────────────────────

func TestSeedBootstrapCov2_CanceledContext(t *testing.T) {
	saveCLIState(t)
	covSeedEnv(t)
	cliCfg = &cli.CLIConfig{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := seedBootstrap(ctx, "pw"); err == nil {
		t.Error("want context error; got nil")
	}
}

func TestSeedBootstrapCov2_ForwardsSetupToken(t *testing.T) {
	saveCLIState(t)
	covSeedEnv(t)
	tokenDir := t.TempDir()
	token := strings.Repeat("f00d", 16)
	if err := os.WriteFile(filepath.Join(tokenDir, "initial_setup_token"),
		[]byte("# header\n"+token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREWSHIP_STORAGE_BASE_PATH", tokenDir)

	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/bootstrap", clitest.JSONResponse(201, map[string]string{
		"user_id": covSeedUserID, "workspace_id": covSeedWSID, "cli_token": "tok-x",
	}))
	flagServer = s.URL()
	cliCfg = &cli.CLIConfig{}

	_, userID, err := seedBootstrap(context.Background(), "pw")
	if err != nil {
		t.Fatalf("seedBootstrap: %v", err)
	}
	if userID != covSeedUserID {
		t.Errorf("userID = %q", userID)
	}
	got := s.CallsFor("POST", "/api/v1/bootstrap")[0].Headers.Get("X-Setup-Token")
	if got != token {
		t.Errorf("X-Setup-Token = %q, want token from storage path", got)
	}
}

func TestSeedBootstrapCov2_FreshButBadJSON(t *testing.T) {
	saveCLIState(t)
	covSeedEnv(t)
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/bootstrap", clitest.TextResponse(201, "not json"))
	flagServer = s.URL()
	cliCfg = &cli.CLIConfig{}

	_, _, err := seedBootstrap(context.Background(), "pw")
	if err == nil || !strings.Contains(err.Error(), "read bootstrap response") {
		t.Errorf("want decode error; got %v", err)
	}
}

func TestSeedBootstrapCov2_SaveConfigFailureIsNonFatal(t *testing.T) {
	saveCLIState(t)
	covSeedEnv(t)
	// Point CREWSHIP_CONFIG below a regular FILE so MkdirAll fails.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREWSHIP_CONFIG", filepath.Join(blocker, "sub", "cli-config.yaml"))

	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/bootstrap", clitest.JSONResponse(201, map[string]string{
		"user_id": covSeedUserID, "workspace_id": covSeedWSID, "cli_token": "tok-x",
	}))
	flagServer = s.URL()
	cliCfg = &cli.CLIConfig{}

	_, userID, err := seedBootstrap(context.Background(), "pw")
	if err != nil {
		t.Fatalf("save-config failure must not abort bootstrap: %v", err)
	}
	if userID != covSeedUserID {
		t.Errorf("userID = %q", userID)
	}
}

func TestSeedBootstrapCov2_ConflictWithTokenButNoWorkspace(t *testing.T) {
	saveCLIState(t)
	covSeedEnv(t)
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/bootstrap", clitest.ErrorResponse(http.StatusConflict, "exists"))
	flagServer = s.URL()
	flagWorkspace = ""
	cliCfg = &cli.CLIConfig{Token: "tok"} // auth passes, workspace missing

	_, _, err := seedBootstrap(context.Background(), "pw")
	if err == nil || !strings.Contains(err.Error(), "DB already initialized") ||
		!strings.Contains(err.Error(), "workspace") {
		t.Errorf("want workspace-missing error; got %v", err)
	}
}

// ─── helper second pass ──────────────────────────────────────────────────

func TestResolveCurrentUserIDCov2_BadJSON(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/auth/cli-token/validate", clitest.TextResponse(200, "nope"))
	if got := resolveCurrentUserID(newAPIClient()); got != "" {
		t.Errorf("got %q, want empty on decode failure", got)
	}
}

func TestCreateOrResolveCov2_TransportError(t *testing.T) {
	covSetupDead(t)
	if _, err := createOrResolve(newAPIClient(), "/api/v1/crews",
		map[string]string{}, "/api/v1/crews", "eng"); err == nil {
		t.Error("want transport error; got nil")
	}
}

func TestResolveBySlugCov2_ErrorBranches(t *testing.T) {
	t.Run("transport error", func(t *testing.T) {
		covSetupDead(t)
		if _, err := resolveBySlug(newAPIClient(), "/api/v1/crews", "eng"); err == nil {
			t.Error("want transport error; got nil")
		}
	})
	t.Run("bad json", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/crews", clitest.TextResponse(200, "nope"))
		if _, err := resolveBySlug(newAPIClient(), "/api/v1/crews", "eng"); err == nil {
			t.Error("want decode error; got nil")
		}
	})
}

func TestResolveByNameCov2_TransportError(t *testing.T) {
	covSetupDead(t)
	if _, err := resolveByName(newAPIClient(), "/api/v1/credentials", "x"); err == nil {
		t.Error("want transport error; got nil")
	}
}

func TestPostBootstrapCov2_InvalidServerURL(t *testing.T) {
	_, err := postBootstrap(context.Background(), "http://\x7f", "", map[string]string{})
	if err == nil {
		t.Error("want request construction error for invalid URL; got nil")
	}
}
