package main

// Coverage tests for cmd_seed_smoke.go. smokeTestAgent is exercised
// against tiny /bin/sh scripts (always present on darwin/linux) instead
// of the real crewship binary; runBackupSelfTest is covered only on its
// pre-exec validation paths because the warmup path would exec
// os.Args[0] — the test binary itself.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// TestMain doubles as a fake `crewship` binary: the smoke/warmup code
// paths exec os.Args[0] (the test binary in tests), which would
// otherwise recursively run the whole suite. With COV_FAKE_CREWSHIP_MODE
// set, the child process short-circuits here with a deterministic
// outcome instead.
func TestMain(m *testing.M) {
	switch os.Getenv("COV_FAKE_CREWSHIP_MODE") {
	case "":
		// Normal test run.
	case "ok":
		fmt.Println("hello from fake crewship")
		os.Exit(0)
	case "fail":
		fmt.Println("fake crewship boom")
		os.Exit(3)
	default:
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// covWarmupTarget picks the crew slug + warmup agent the production
// LEAD-preference logic would choose, mirroring runBackupSelfTest.
func covWarmupTarget(t *testing.T) (crewSlug, warmupSlug string) {
	t.Helper()
	if len(seeddata.Agents) == 0 {
		t.Fatal("seed data has no agents")
	}
	crewSlug = seeddata.Agents[0].CrewSlug
	for _, a := range seeddata.Agents {
		if a.CrewSlug == crewSlug {
			if a.AgentRole == "LEAD" {
				return crewSlug, a.Slug
			}
			if warmupSlug == "" {
				warmupSlug = a.Slug
			}
		}
	}
	return crewSlug, warmupSlug
}

// covWriteScript drops an executable /bin/sh script into a temp dir.
func covWriteScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-crewship.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// ─── truncateForSmoke ────────────────────────────────────────────────────

func TestTruncateForSmoke(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"short passthrough", "hello", 10, "hello"},
		{"collapses whitespace", "a\nb\t  c", 80, "a b c"},
		{"exact fit", "abcde", 5, "abcde"},
		{"truncates with ellipsis", "abcdefghij", 8, "abcde..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := truncateForSmoke(tc.in, tc.n); got != tc.want {
				t.Errorf("truncateForSmoke(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}

func TestTruncateForSmoke_RuneSafe(t *testing.T) {
	in := strings.Repeat("č", 20)
	got := truncateForSmoke(in, 10)
	if got != strings.Repeat("č", 7)+"..." {
		t.Errorf("rune-unsafe truncation: %q", got)
	}
}

// ─── printSmokeLine / printSmokeSummary ──────────────────────────────────

func TestPrintSmokeLine_OK(t *testing.T) {
	out, _ := covCaptureStderrCli6(t, func() error {
		printSmokeLine(smokeTestResult{
			CrewSlug: "eng", AgentSlug: "viktor", OK: true,
			Elapsed: 1500 * time.Millisecond, Output: "hi, I am Viktor",
		})
		return nil
	})
	if !strings.Contains(out, "eng/viktor") || !strings.Contains(out, "OK") {
		t.Errorf("OK line wrong: %q", out)
	}
	if !strings.Contains(out, `"hi, I am Viktor"`) {
		t.Errorf("output quote missing: %q", out)
	}
}

func TestPrintSmokeLine_Timeout(t *testing.T) {
	out, _ := covCaptureStderrCli6(t, func() error {
		printSmokeLine(smokeTestResult{
			CrewSlug: "eng", AgentSlug: "eva", Timeout: true, ErrMsg: "exceeded 30s",
		})
		return nil
	})
	if !strings.Contains(out, "TIMEOUT") || !strings.Contains(out, "exceeded 30s") {
		t.Errorf("timeout line wrong: %q", out)
	}
}

func TestPrintSmokeLine_Fail(t *testing.T) {
	out, _ := covCaptureStderrCli6(t, func() error {
		printSmokeLine(smokeTestResult{
			CrewSlug: "this-is-a-really-long-crew", AgentSlug: "agent-with-long-slug",
			ErrMsg: "exit status 1",
		})
		return nil
	})
	if !strings.Contains(out, "FAIL") || !strings.Contains(out, "exit status 1") {
		t.Errorf("fail line wrong: %q", out)
	}
}

func TestPrintSmokeLine_FailSurfacesOutput(t *testing.T) {
	out, _ := covCaptureStderrCli6(t, func() error {
		printSmokeLine(smokeTestResult{
			CrewSlug: "engineering", AgentSlug: "alex",
			ErrMsg: "exit status 1",
			Output: "agent error: failed to start agent container: legacy slug-scoped volume \"crewship-3-tools-engineering\" exists",
		})
		return nil
	})
	if !strings.Contains(out, "FAIL") || !strings.Contains(out, "exit status 1") {
		t.Errorf("fail line missing status: %q", out)
	}
	// The real cause must be surfaced, not just "exit status 1".
	if !strings.Contains(out, "failed to start agent container") ||
		!strings.Contains(out, "crewship-3-tools-engineering") {
		t.Errorf("fail line should surface captured output: %q", out)
	}
}

func TestPrintSmokeSummary_AllPass(t *testing.T) {
	out, err := covCaptureStderrCli6(t, func() error {
		return printSmokeSummary([]smokeTestResult{{OK: true}, {OK: true}})
	})
	if err != nil {
		t.Errorf("all-pass must return nil, got %v", err)
	}
	if !strings.Contains(out, "2 passed, 0 failed, 0 skipped") {
		t.Errorf("summary wrong: %q", out)
	}
}

func TestPrintSmokeSummary_SomeFail(t *testing.T) {
	out, err := covCaptureStderrCli6(t, func() error {
		return printSmokeSummary([]smokeTestResult{{OK: true}, {}, {Timeout: true}})
	})
	if err == nil || !strings.Contains(err.Error(), "smoke test: 2/3 agents failed") {
		t.Errorf("expected failure error, got %v", err)
	}
	if !strings.Contains(out, "1 passed, 2 failed, 0 skipped") {
		t.Errorf("summary wrong: %q", out)
	}
}

// ─── smokeTestAgent ──────────────────────────────────────────────────────

func TestSmokeTestAgent_OK(t *testing.T) {
	bin := covWriteScript(t, `echo "hello from agent"`)
	res := smokeTestAgent(context.Background(), bin, "viktor", "eng", "http://localhost:0", 5*time.Second)
	if !res.OK {
		t.Fatalf("expected OK, got %+v", res)
	}
	if res.Output != "hello from agent" {
		t.Errorf("Output = %q", res.Output)
	}
	if res.CrewSlug != "eng" || res.AgentSlug != "viktor" {
		t.Errorf("slugs wrong: %+v", res)
	}
}

func TestSmokeTestAgent_ExitFailure(t *testing.T) {
	bin := covWriteScript(t, `exit 3`)
	res := smokeTestAgent(context.Background(), bin, "viktor", "eng", "http://localhost:0", 5*time.Second)
	if res.OK || res.Timeout {
		t.Fatalf("expected plain failure, got %+v", res)
	}
	if !strings.Contains(res.ErrMsg, "exit status 3") {
		t.Errorf("ErrMsg = %q", res.ErrMsg)
	}
}

func TestSmokeTestAgent_EmptyOutput(t *testing.T) {
	bin := covWriteScript(t, `exit 0`)
	res := smokeTestAgent(context.Background(), bin, "viktor", "eng", "http://localhost:0", 5*time.Second)
	if res.OK {
		t.Fatal("empty output must not count as OK")
	}
	if res.ErrMsg != "empty response" {
		t.Errorf("ErrMsg = %q, want 'empty response'", res.ErrMsg)
	}
}

func TestSmokeTestAgent_Timeout(t *testing.T) {
	// The subprocess sleeps; CommandContext kills it when the 50ms
	// timeout expires, so the test itself stays fast.
	bin := covWriteScript(t, `sleep 5`)
	res := smokeTestAgent(context.Background(), bin, "viktor", "eng", "http://localhost:0", 50*time.Millisecond)
	if !res.Timeout {
		t.Fatalf("expected timeout, got %+v", res)
	}
	if !strings.Contains(res.ErrMsg, "exceeded 50ms") {
		t.Errorf("ErrMsg = %q", res.ErrMsg)
	}
}

// ─── runSmokeTest ────────────────────────────────────────────────────────

func TestRunSmokeTest_NoAgents(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Server: "http://localhost:0"}

	out, err := covCaptureStderrCli6(t, func() error {
		return runSmokeTest(context.Background(), map[string]string{}, time.Second)
	})
	if err != nil {
		t.Fatalf("runSmokeTest with no agents must pass, got %v", err)
	}
	if !strings.Contains(out, "0 passed, 0 failed, 0 skipped") {
		t.Errorf("summary missing: %q", out)
	}
}

func TestRunSmokeTest_CancelledContext(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Server: "http://localhost:0"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Cancellation is checked before any subprocess exec, so this never
	// spawns os.Args[0].
	_, err := covCaptureStderrCli6(t, func() error {
		return runSmokeTest(ctx, map[string]string{"viktor": covAgentIDCli6}, time.Second)
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestRunSmokeTest_AllAgentsPass(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Server: "http://localhost:0"}
	t.Setenv("COV_FAKE_CREWSHIP_MODE", "ok")

	// Two real seed slugs (stable crew ordering) + one unknown slug
	// (empty crew) exercise the sort comparator on both branches.
	agentIDs := map[string]string{"zz-unknown-agent": covAgentIDCli6}
	for _, a := range seeddata.Agents[:min(2, len(seeddata.Agents))] {
		agentIDs[a.Slug] = covAgentIDCli6
	}

	out, err := covCaptureStderrCli6(t, func() error {
		return runSmokeTest(context.Background(), agentIDs, 10*time.Second)
	})
	if err != nil {
		t.Fatalf("runSmokeTest: %v", err)
	}
	if !strings.Contains(out, fmt.Sprintf("%d passed, 0 failed, 0 skipped", len(agentIDs))) {
		t.Errorf("summary wrong: %q", out)
	}
	if !strings.Contains(out, "hello from fake crewship") {
		t.Errorf("fake agent output missing from OK lines: %q", out)
	}
}

func TestRunSmokeTest_AgentFails(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Server: "http://localhost:0"}
	t.Setenv("COV_FAKE_CREWSHIP_MODE", "fail")

	agentIDs := map[string]string{seeddata.Agents[0].Slug: covAgentIDCli6}
	out, err := covCaptureStderrCli6(t, func() error {
		return runSmokeTest(context.Background(), agentIDs, 10*time.Second)
	})
	if err == nil || !strings.Contains(err.Error(), "smoke test: 1/1 agents failed") {
		t.Errorf("expected failure summary error, got %v", err)
	}
	if !strings.Contains(out, "FAIL") || !strings.Contains(out, "exit status 3") {
		t.Errorf("failure line wrong: %q", out)
	}
}

// ─── runBackupSelfTest (pre-exec validation paths) ───────────────────────

func TestRunBackupSelfTest_MissingCrewID(t *testing.T) {
	err := runBackupSelfTest(context.Background(), nil,
		provisionTarget{slug: "engineering"}, map[string]string{}, map[string]string{})
	if err == nil || !strings.Contains(err.Error(), `no crew id for slug "engineering"`) {
		t.Errorf("expected missing-crew-id error, got %v", err)
	}
}

func TestRunBackupSelfTest_NoAgentInCrew(t *testing.T) {
	slug := "zzz-no-such-crew"
	// Defensive: the error path under test only exists while the seed
	// data has no agents in this synthetic crew.
	for _, a := range seeddata.Agents {
		if a.CrewSlug == slug {
			t.Fatalf("seed data unexpectedly contains crew %q", slug)
		}
	}
	err := runBackupSelfTest(context.Background(), nil,
		provisionTarget{slug: slug},
		map[string]string{slug: covCrewIDCli6}, map[string]string{})
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("no agent found in crew %q", slug)) {
		t.Errorf("expected no-agent error, got %v", err)
	}
}

func TestRunBackupSelfTest_WarmupAgentNotSeeded(t *testing.T) {
	if len(seeddata.Agents) == 0 {
		t.Fatal("seed data has no agents")
	}
	slug := seeddata.Agents[0].CrewSlug
	// agentIDs deliberately empty: the chosen warmup agent is missing,
	// which must fail BEFORE any subprocess is spawned.
	err := runBackupSelfTest(context.Background(), nil,
		provisionTarget{slug: slug},
		map[string]string{slug: covCrewIDCli6}, map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "missing from seeded agents") {
		t.Errorf("expected warmup-agent-missing error, got %v", err)
	}
}

// ─── runBackupSelfTest (full path via the fake-crewship TestMain hook) ───

// covBackupSelfTestFixture wires crewIDs/agentIDs for the production
// warmup-agent selection and a stub server for the self-test POST.
func covBackupSelfTestFixture(t *testing.T) (*clitest.StubServer, provisionTarget, map[string]string, map[string]string) {
	t.Helper()
	saveCLIState(t)
	stub := clitest.NewStubServer()
	t.Cleanup(stub.Close)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: covWorkspaceIDCli6, Server: stub.URL()}

	crewSlug, warmupSlug := covWarmupTarget(t)
	crewIDs := map[string]string{crewSlug: covCrewIDCli6}
	agentIDs := map[string]string{warmupSlug: covAgentIDCli6}
	return stub, provisionTarget{slug: crewSlug, id: covCrewIDCli6}, crewIDs, agentIDs
}

func TestRunBackupSelfTest_Happy(t *testing.T) {
	t.Setenv("COV_FAKE_CREWSHIP_MODE", "ok")
	stub, target, crewIDs, agentIDs := covBackupSelfTestFixture(t)

	stub.OnPost("/api/v1/admin/backups/self-test", clitest.JSONResponse(200, map[string]any{
		"ok": true, "crew_slug": target.slug, "bundle_bytes": 4096, "elapsed_ms": 250,
	}))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	out, err := covCaptureStderrCli6(t, func() error {
		return runBackupSelfTest(context.Background(), client, target, crewIDs, agentIDs)
	})
	if err != nil {
		t.Fatalf("runBackupSelfTest: %v", err)
	}
	if !strings.Contains(out, "4096 bytes, 250ms") {
		t.Errorf("success line missing: %q", out)
	}
	calls := stub.CallsFor("POST", "/api/v1/admin/backups/self-test")
	if len(calls) != 1 {
		t.Fatalf("expected 1 self-test POST, got %d", len(calls))
	}
	body := covDecodeBody(t, calls[0].Body)
	if body["crew_id"] != covCrewIDCli6 {
		t.Errorf("crew_id = %v", body["crew_id"])
	}
}

func TestRunBackupSelfTest_CanaryFails(t *testing.T) {
	t.Setenv("COV_FAKE_CREWSHIP_MODE", "ok")
	stub, target, crewIDs, agentIDs := covBackupSelfTestFixture(t)

	stub.OnPost("/api/v1/admin/backups/self-test", clitest.JSONResponse(200, map[string]any{
		"ok": false, "error": "restore mismatch",
	}))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	_, err := covCaptureStderrCli6(t, func() error {
		return runBackupSelfTest(context.Background(), client, target, crewIDs, agentIDs)
	})
	if err == nil || !strings.Contains(err.Error(), "restore mismatch") {
		t.Errorf("expected canary failure surfaced, got %v", err)
	}
}

func TestRunBackupSelfTest_HTTPError(t *testing.T) {
	t.Setenv("COV_FAKE_CREWSHIP_MODE", "ok")
	stub, target, crewIDs, agentIDs := covBackupSelfTestFixture(t)

	stub.OnPost("/api/v1/admin/backups/self-test", clitest.ErrorResponse(503, "docker unavailable"))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	_, err := covCaptureStderrCli6(t, func() error {
		return runBackupSelfTest(context.Background(), client, target, crewIDs, agentIDs)
	})
	if err == nil || !strings.Contains(err.Error(), "docker unavailable") {
		t.Errorf("expected 503 surfaced, got %v", err)
	}
}

func TestRunBackupSelfTest_DecodeError(t *testing.T) {
	t.Setenv("COV_FAKE_CREWSHIP_MODE", "ok")
	stub, target, crewIDs, agentIDs := covBackupSelfTestFixture(t)

	stub.OnPost("/api/v1/admin/backups/self-test", clitest.TextResponse(200, "not json"))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	_, err := covCaptureStderrCli6(t, func() error {
		return runBackupSelfTest(context.Background(), client, target, crewIDs, agentIDs)
	})
	if err == nil || !strings.Contains(err.Error(), "backup self-test decode") {
		t.Errorf("expected decode error, got %v", err)
	}
}

func TestRunBackupSelfTest_WarmupExecFails(t *testing.T) {
	t.Setenv("COV_FAKE_CREWSHIP_MODE", "fail")
	stub, target, crewIDs, agentIDs := covBackupSelfTestFixture(t)

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	_, err := covCaptureStderrCli6(t, func() error {
		return runBackupSelfTest(context.Background(), client, target, crewIDs, agentIDs)
	})
	if err == nil || !strings.Contains(err.Error(), "backup self-test warmup") || !strings.Contains(err.Error(), "warmup exec") {
		t.Errorf("expected warmup exec error, got %v", err)
	}
	if n := len(stub.CallsFor("POST", "/api/v1/admin/backups/self-test")); n != 0 {
		t.Errorf("self-test must not run after warmup failure, got %d calls", n)
	}
}
