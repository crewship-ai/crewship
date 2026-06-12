package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/crewship-ai/crewship/internal/manifest"
)

// covApplyManifest is a minimal-but-valid single-crew manifest with
// one agent and a devcontainer block (so provisionHintForCrews fires).
const covApplyManifest = `
apiVersion: crewship/v1
kind: Crew
metadata:
  name: Cov Crew
  slug: cov-crew
spec:
  devcontainer:
    image: mcr.microsoft.com/devcontainers/base:ubuntu
  agents:
    - slug: cov-agent
      name: Cov Agent
      agent_role: LEAD
      cli_adapter: CLAUDE_CODE
      prompt: hello
`

func writeCovManifest(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "crew.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return path
}

func declareApplyFlags(c *cobra.Command) {
	c.Flags().String("file", "", "")
	c.Flags().Bool("dry-run", false, "")
	c.Flags().Bool("strict", false, "")
	c.Flags().Bool("replace", false, "")
	c.Flags().Bool("from-env", false, "")
	c.Flags().String("secrets-file", "", "")
	c.Flags().Bool("skip-test-gate", false, "")
	c.Flags().BoolP("yes", "y", false, "")
}

// ─── loadSecretsFile ─────────────────────────────────────────────────

func TestLoadSecretsFile_ParsesEnvShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.env")
	content := strings.Join([]string{
		"# comment line",
		"",
		"PLAIN=value1",
		`DQUOTED="with spaces"`,
		"SQUOTED='single'",
		"  PADDED  =  padded-value  ",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := loadSecretsFile(path)
	if err != nil {
		t.Fatalf("loadSecretsFile: %v", err)
	}
	want := map[string]string{
		"PLAIN":   "value1",
		"DQUOTED": "with spaces",
		"SQUOTED": "single",
		"PADDED":  "padded-value",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("m[%s] = %q, want %q", k, m[k], v)
		}
	}
	if len(m) != len(want) {
		t.Errorf("len = %d, want %d (%v)", len(m), len(want), m)
	}
}

func TestLoadSecretsFile_BadLineReportsLineNumber(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.env")
	if err := os.WriteFile(path, []byte("GOOD=1\nnot-a-kv-line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadSecretsFile(path)
	if err == nil || !strings.Contains(err.Error(), ":2: expected KEY=VALUE") {
		t.Fatalf("want line-2 parse error, got %v", err)
	}
}

func TestLoadSecretsFile_MissingFile(t *testing.T) {
	_, err := loadSecretsFile(filepath.Join(t.TempDir(), "nope.env"))
	if err == nil || !strings.Contains(err.Error(), "open secrets file") {
		t.Fatalf("want open error, got %v", err)
	}
}

// ─── buildSecretsSource ──────────────────────────────────────────────

func TestBuildSecretsSource_Variants(t *testing.T) {
	// No sources → NoSecretsSource.
	src, err := buildSecretsSource(false, "")
	if err != nil {
		t.Fatalf("none: %v", err)
	}
	if _, ok := src.(manifest.NoSecretsSource); !ok {
		t.Errorf("want NoSecretsSource, got %T", src)
	}

	// File source resolves keys from the file.
	path := filepath.Join(t.TempDir(), "s.env")
	if err := os.WriteFile(path, []byte("FILE_KEY=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src, err = buildSecretsSource(false, path)
	if err != nil {
		t.Fatalf("file: %v", err)
	}
	if v, ok := src.ValueFor("FILE_KEY"); !ok || v != "from-file" {
		t.Errorf("file lookup = %q/%v", v, ok)
	}

	// Env source.
	t.Setenv("COV_APPLY_ENV_KEY", "from-env")
	src, err = buildSecretsSource(true, "")
	if err != nil {
		t.Fatalf("env: %v", err)
	}
	if v, ok := src.ValueFor("COV_APPLY_ENV_KEY"); !ok || v != "from-env" {
		t.Errorf("env lookup = %q/%v", v, ok)
	}

	// Chain: file wins over env for the same key (file is appended first).
	t.Setenv("FILE_KEY", "from-env-shadow")
	src, err = buildSecretsSource(true, path)
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	if v, ok := src.ValueFor("FILE_KEY"); !ok || v != "from-file" {
		t.Errorf("chain precedence: got %q/%v, want from-file", v, ok)
	}

	// Bad file path propagates the error.
	if _, err := buildSecretsSource(false, filepath.Join(t.TempDir(), "missing.env")); err == nil {
		t.Error("want error for missing secrets file")
	}
}

// ─── loadManifestBundle ──────────────────────────────────────────────

func TestLoadManifestBundle_FromFileAndStdin(t *testing.T) {
	path := writeCovManifest(t, covApplyManifest)
	b, err := loadManifestBundle(path)
	if err != nil {
		t.Fatalf("file load: %v", err)
	}
	if len(b.Documents) != 1 || b.Documents[0].Metadata.Slug != "cov-crew" {
		t.Fatalf("unexpected bundle from file: %+v", b.Documents)
	}

	// "-" sentinel reads stdin.
	err = covWithStdinCli4(t, covApplyManifest, func() error {
		b2, err := loadManifestBundle("-")
		if err != nil {
			return err
		}
		if len(b2.Documents) != 1 || b2.Documents[0].Metadata.Slug != "cov-crew" {
			t.Errorf("unexpected bundle from stdin: %+v", b2.Documents)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("stdin load: %v", err)
	}
}

// ─── print helpers ───────────────────────────────────────────────────

func TestPrintPlanSummaryWarnings(t *testing.T) {
	// All printers must be nil-safe.
	out, _ := covCaptureStdoutCli4(t, func() error {
		printPlan(nil, false)
		printSummary(nil, nil)
		printWarnings(nil)
		return nil
	})
	if out != "" {
		t.Errorf("nil plan should print nothing, got %q", out)
	}

	empty := &manifest.Plan{}
	out, _ = covCaptureStdoutCli4(t, func() error {
		printPlan(empty, false)
		return nil
	})
	if !strings.Contains(out, "(no resources)") {
		t.Errorf("empty plan placeholder missing: %q", out)
	}

	warned := &manifest.Plan{Warnings: []string{"code steps are not executed"}}
	out, _ = covCaptureStdoutCli4(t, func() error {
		printWarnings(warned)
		printSummary(warned, nil)
		printSummary(warned, &manifest.Result{Created: 2, Updated: 1, Unchanged: 3, Deleted: 0})
		return nil
	})
	if !strings.Contains(out, "Warnings:") || !strings.Contains(out, "! code steps are not executed") {
		t.Errorf("warnings block missing: %q", out)
	}
	if !strings.Contains(out, "Plan: 0 to create, 0 to update, 0 unchanged, 0 to delete.") {
		t.Errorf("plan summary missing: %q", out)
	}
	if !strings.Contains(out, "Applied: 2 created, 1 updated, 3 unchanged, 0 deleted.") {
		t.Errorf("applied summary missing: %q", out)
	}
}

func TestProvisionHintForCrews(t *testing.T) {
	b, err := manifest.Load([]byte(covApplyManifest))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	out, _ := covCaptureStdoutCli4(t, func() error {
		provisionHintForCrews(b)
		return nil
	})
	if !strings.Contains(out, "crewship crew provision cov-crew") {
		t.Errorf("provision hint missing: %q", out)
	}

	// No devcontainer → no hint.
	plain, err := manifest.Load([]byte(strings.Replace(covApplyManifest,
		"  devcontainer:\n    image: mcr.microsoft.com/devcontainers/base:ubuntu\n", "", 1)))
	if err != nil {
		t.Fatalf("load plain: %v", err)
	}
	out, _ = covCaptureStdoutCli4(t, func() error {
		provisionHintForCrews(plain)
		return nil
	})
	if out != "" {
		t.Errorf("no hint expected without devcontainer, got %q", out)
	}
}

// ─── confirmInteractive ──────────────────────────────────────────────

func TestConfirmInteractive(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"y\n", true},
		{"YES\n", true},
		{"n\n", false},
		{"\n", false},
		{"", false}, // closed stdin (no newline) → refuse
	}
	for _, tc := range cases {
		got := false
		_ = covWithStdinCli4(t, tc.input, func() error {
			_, _ = covCaptureStdoutCli4(t, func() error {
				got = confirmInteractive("destroy everything?")
				return nil
			})
			return nil
		})
		if got != tc.want {
			t.Errorf("confirmInteractive with input %q = %v, want %v", tc.input, got, tc.want)
		}
	}
}

// ─── runApply ────────────────────────────────────────────────────────

func TestRunApply_GateErrors(t *testing.T) {
	// No auth.
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	c := covFreshCmd(applyCmd, declareApplyFlags)
	if err := runApply(c, nil); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("want not-logged-in, got %v", err)
	}

	// No workspace.
	t.Setenv("CREWSHIP_WORKSPACE", "")
	flagWorkspace = ""
	cliCfg = &cli.CLIConfig{Token: "tok"}
	if err := runApply(c, nil); err == nil || !strings.Contains(err.Error(), "no workspace set") {
		t.Fatalf("want workspace error, got %v", err)
	}
}

func TestRunApply_FlagValidation(t *testing.T) {
	covSetupCli4(t)

	// Empty --file.
	c := covFreshCmd(applyCmd, declareApplyFlags)
	if err := runApply(c, nil); err == nil || !strings.Contains(err.Error(), "--file is required") {
		t.Fatalf("want file-required, got %v", err)
	}

	// --strict + --replace are mutually exclusive.
	c2 := covFreshCmd(applyCmd, declareApplyFlags)
	covSetFlagsCli4(t, c2, map[string]string{"file": "x.yaml", "strict": "true", "replace": "true"})
	if err := runApply(c2, nil); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("want mutual-exclusion error, got %v", err)
	}

	// Nonexistent manifest path surfaces the load error.
	c3 := covFreshCmd(applyCmd, declareApplyFlags)
	covSetFlagsCli4(t, c3, map[string]string{"file": filepath.Join(t.TempDir(), "ghost.yaml")})
	if err := runApply(c3, nil); err == nil {
		t.Fatal("want load error for missing manifest")
	}
}

func TestRunApply_DryRunPrintsPlanWithoutMutations(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]any{}))
	// Any other read the planner does sees an empty collection.
	stub.SetFallback(clitest.JSONResponse(200, []map[string]any{}))

	path := writeCovManifest(t, covApplyManifest)
	c := covFreshCmd(applyCmd, declareApplyFlags)
	covSetFlagsCli4(t, c, map[string]string{"file": path, "dry-run": "true"})

	out, err := covCaptureStdoutCli4(t, func() error { return runApply(c, nil) })
	if err != nil {
		t.Fatalf("runApply dry-run: %v", err)
	}
	if !strings.Contains(out, "Plan (dry-run, nothing will change):") {
		t.Errorf("dry-run header missing: %q", out)
	}
	if !strings.Contains(out, "cov-crew") {
		t.Errorf("plan should mention the crew: %q", out)
	}
	if !strings.Contains(out, "to create") {
		t.Errorf("plan summary missing: %q", out)
	}

	// Dry-run must never mutate: only GETs allowed.
	for _, call := range stub.Calls() {
		if call.Method != "GET" {
			t.Errorf("dry-run issued a %s %s", call.Method, call.Path)
		}
	}
}

func TestRunApply_BuildPlanErrorSurfaces(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews", clitest.ErrorResponse(500, "list crews exploded"))
	stub.SetFallback(clitest.JSONResponse(200, []map[string]any{}))

	path := writeCovManifest(t, covApplyManifest)
	c := covFreshCmd(applyCmd, declareApplyFlags)
	covSetFlagsCli4(t, c, map[string]string{"file": path, "dry-run": "true"})

	_, err := covCaptureStdoutCli4(t, func() error { return runApply(c, nil) })
	if err == nil || !strings.Contains(err.Error(), "list crews exploded") {
		t.Fatalf("want plan error surfaced, got %v", err)
	}
}

func TestApplyCmdFlagSurface(t *testing.T) {
	for _, name := range []string{"file", "dry-run", "strict", "replace", "from-env", "secrets-file", "skip-test-gate", "yes"} {
		if applyCmd.Flags().Lookup(name) == nil {
			t.Errorf("apply missing --%s flag", name)
		}
	}
}

// ─── runApply: modes + full apply paths ──────────────────────────────

func TestRunApply_StrictAndReplaceDryRunModes(t *testing.T) {
	path := writeCovManifest(t, covApplyManifest)
	for _, mode := range []string{"strict", "replace"} {
		t.Run(mode, func(t *testing.T) {
			stub := covSetupCli4(t)
			stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]any{}))
			stub.SetFallback(clitest.JSONResponse(200, []map[string]any{}))

			c := covFreshCmd(applyCmd, declareApplyFlags)
			covSetFlagsCli4(t, c, map[string]string{"file": path, "dry-run": "true", mode: "true"})
			out, err := covCaptureStdoutCli4(t, func() error { return runApply(c, nil) })
			if err != nil {
				t.Fatalf("dry-run --%s: %v", mode, err)
			}
			if !strings.Contains(out, "Plan (dry-run") {
				t.Errorf("dry-run header missing in --%s mode: %q", mode, out)
			}
		})
	}
}

func TestRunApply_BadSecretsFile(t *testing.T) {
	covSetupCli4(t)
	path := writeCovManifest(t, covApplyManifest)
	c := covFreshCmd(applyCmd, declareApplyFlags)
	covSetFlagsCli4(t, c, map[string]string{
		"file":         path,
		"secrets-file": filepath.Join(t.TempDir(), "missing.env"),
	})
	if err := runApply(c, nil); err == nil || !strings.Contains(err.Error(), "open secrets file") {
		t.Fatalf("want secrets-file error, got %v", err)
	}
}

func TestRunApply_ValidationError(t *testing.T) {
	covSetupCli4(t)
	// Duplicate agent slugs in one crew must fail bundle validation
	// before any network traffic.
	dup := strings.Replace(covApplyManifest, "    - slug: cov-agent",
		"    - slug: cov-agent\n      name: Dup\n      agent_role: AGENT\n      prompt: hi\n    - slug: cov-agent", 1)
	path := writeCovManifest(t, dup)
	c := covFreshCmd(applyCmd, declareApplyFlags)
	covSetFlagsCli4(t, c, map[string]string{"file": path})
	if err := runApply(c, nil); err == nil {
		t.Fatal("want validation error for duplicate agent slug")
	}
}

func TestRunApply_FullApplyCreatesResources(t *testing.T) {
	stub := covSetupCli4(t)
	createdCrew := map[string]any{
		"id": covCrewIDCli4, "workspace_id": covWorkspaceIDCli4, "name": "Cov Crew", "slug": "cov-crew",
	}
	// The crew list is stateful: empty until the POST lands, then the
	// created crew — apply re-fetches it to resolve the agent's crew.
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]any{}))
	stub.SetFallback(clitest.JSONResponse(200, []map[string]any{}))
	stub.OnPost("/api/v1/crews", func(_ *http.Request, _ []byte) (int, []byte, string) {
		stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]any{createdCrew}))
		b, _ := json.Marshal(createdCrew)
		return 201, b, "application/json"
	})
	stub.OnPost("/api/v1/agents", clitest.JSONResponse(201, map[string]any{
		"id": covAgentIDCli4, "slug": "cov-agent", "name": "Cov Agent",
	}))

	path := writeCovManifest(t, covApplyManifest)
	c := covFreshCmd(applyCmd, declareApplyFlags)
	covSetFlagsCli4(t, c, map[string]string{"file": path, "yes": "true"})

	out, err := covCaptureStdoutCli4(t, func() error { return runApply(c, nil) })
	if err != nil {
		t.Fatalf("full apply: %v\noutput: %q", err, out)
	}
	if !strings.Contains(out, "Applied:") {
		t.Errorf("applied summary missing: %q", out)
	}
	// Devcontainer crew → provision hint at the tail.
	if !strings.Contains(out, "crewship crew provision cov-crew") {
		t.Errorf("provision hint missing: %q", out)
	}
	if got := len(stub.CallsFor("POST", "/api/v1/crews")); got != 1 {
		t.Errorf("crew create calls = %d, want 1", got)
	}
	if got := len(stub.CallsFor("POST", "/api/v1/agents")); got != 1 {
		t.Errorf("agent create calls = %d, want 1", got)
	}
}

func TestRunApply_DestructivePlanAbortsWithoutYes(t *testing.T) {
	stub := covSetupCli4(t)
	// Existing crew matches the manifest slug; --replace turns that
	// into delete + recreate → destructive plan → interactive prompt.
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]any{
		{"id": covCrewIDCli4, "workspace_id": covWorkspaceIDCli4, "name": "Cov Crew", "slug": "cov-crew"},
	}))
	stub.SetFallback(clitest.JSONResponse(200, []map[string]any{}))

	path := writeCovManifest(t, covApplyManifest)
	c := covFreshCmd(applyCmd, declareApplyFlags)
	covSetFlagsCli4(t, c, map[string]string{"file": path, "replace": "true"})

	var runErr error
	_ = covWithStdinCli4(t, "n\n", func() error {
		_, runErr = covCaptureStdoutCli4(t, func() error { return runApply(c, nil) })
		return nil
	})
	if runErr == nil || !strings.Contains(runErr.Error(), "aborted") {
		t.Fatalf("want aborted, got %v", runErr)
	}
	// Nothing may have been mutated.
	for _, call := range stub.Calls() {
		if call.Method != "GET" {
			t.Errorf("aborted destructive plan issued %s %s", call.Method, call.Path)
		}
	}
}

func TestRunApply_ApplyErrorStillPrintsSummary(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]any{}))
	stub.SetFallback(clitest.JSONResponse(200, []map[string]any{}))
	// Crew create explodes mid-apply.
	stub.OnPost("/api/v1/crews", clitest.ErrorResponse(500, "create blew up"))

	path := writeCovManifest(t, covApplyManifest)
	c := covFreshCmd(applyCmd, declareApplyFlags)
	covSetFlagsCli4(t, c, map[string]string{"file": path, "yes": "true"})

	out, err := covCaptureStdoutCli4(t, func() error { return runApply(c, nil) })
	if err == nil {
		t.Fatal("want apply error")
	}
	// The partial summary still lands before the error returns.
	if !strings.Contains(out, "Plan:") && !strings.Contains(out, "Applied:") {
		t.Errorf("summary missing on apply error: %q", out)
	}
}

func TestLoadManifestBundle_StdinOverflow(t *testing.T) {
	// >4 MiB on stdin must be rejected, not silently truncated. The
	// writer runs in a goroutine because a 4 MiB+1 write would
	// deadlock against the pipe buffer otherwise.
	old := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	defer func() {
		os.Stdin = old
		_ = r.Close()
	}()

	go func() {
		chunk := strings.Repeat("#", 1<<20) // 1 MiB comment lines
		for i := 0; i < 5; i++ {            // 5 MiB total
			if _, err := w.WriteString(chunk); err != nil {
				break
			}
		}
		_ = w.Close()
	}()

	_, err = loadManifestBundle("-")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("want overflow rejection, got %v", err)
	}
}
