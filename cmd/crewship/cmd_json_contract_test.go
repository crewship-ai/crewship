package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// Per-command `-f json` contract tests for the commands fixed in #2086.
//
// cli_format_contract_test.go proves, statically and for the whole tree, that
// a reporting command RESOLVES the format. These tests prove the commands then
// RENDER it — the half no static analysis can settle, because the handler's
// payload struct and the bytes on stdout are only connected by running them.
//
// Every one of these was reproduced against a live server before it was
// written, and every one FAILS on origin/main. The two shapes:
//
//	(a) human text under -f json — `jq` gets prose and exits 0
//	(b) `null` where a list belongs — `jq '.[]'` cannot iterate null
//
// (b) is fixed once, in internal/cli.Formatter, so the assertions here are
// mostly about (a) plus the specific empty-state paths that produced (b).
//
// A note on what these assert: `json.Unmarshal(stdout)` succeeding is the
// contract. Field-level assertions come after, because a command that emits
// `{}` under -f json is technically parseable and still useless.

// jsonOut runs fn with -f json, captures stdout, and fails unless the whole
// stream parses. It returns the decoded document so a caller can assert on the
// contents.
//
// Deliberately decodes the ENTIRE stdout rather than searching it for a JSON
// substring: the `digest enable` failure was prose followed by a valid object,
// and a substring search would have called that a pass.
func jsonOut(t *testing.T, fn func() error) any {
	t.Helper()
	flagFormat = "json"
	var err error
	out := covCaptureStdoutCli5(t, func() { err = fn() })
	if err != nil {
		t.Fatalf("RunE under -f json: %v\nstdout:\n%s", err, out)
	}
	var doc any
	if uerr := json.Unmarshal([]byte(out), &doc); uerr != nil {
		t.Fatalf("-f json stdout is not valid JSON: %v\ngot:\n%s", uerr, out)
	}
	return doc
}

// humanOut runs fn with no format set and returns stdout, so each test can
// prove the human rendering did NOT change — a "fix" that improves the machine
// output by degrading the terminal one is not a fix.
func humanOut(t *testing.T, fn func() error) string {
	t.Helper()
	flagFormat = ""
	var err error
	out := covCaptureStdoutCli5(t, func() { err = fn() })
	if err != nil {
		t.Fatalf("RunE: %v\nstdout:\n%s", err, out)
	}
	return out
}

// emptyJSONArray fails unless stdout under -f json is exactly `[]`.
func emptyJSONArray(t *testing.T, fn func() error) {
	t.Helper()
	flagFormat = "json"
	var err error
	out := covCaptureStdoutCli5(t, func() { err = fn() })
	if err != nil {
		t.Fatalf("RunE under -f json: %v\nstdout:\n%s", err, out)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("an empty result under -f json must be [] so `jq '.[]'` works; got:\n%s", out)
	}
}

// ─── mode (a): human text emitted under -f json ──────────────────────────

func TestNotifyStatus_JSONContract(t *testing.T) {
	covSetupCli5(t)
	doc := jsonOut(t, func() error { return notifyStatusCmd.RunE(notifyStatusCmd, nil) })
	m, ok := doc.(map[string]any)
	if !ok {
		t.Fatalf("want an object, got %T", doc)
	}
	if _, has := m["enabled"]; !has {
		t.Errorf("no `enabled` field: %v", m)
	}
	// The human line is the one an operator reads; it must be untouched.
	if h := humanOut(t, func() error { return notifyStatusCmd.RunE(notifyStatusCmd, nil) }); !strings.Contains(h, "notifications:") {
		t.Errorf("human output changed: %q", h)
	}
}

func TestConfigShow_JSONContract(t *testing.T) {
	covSetupCli5(t)
	doc := jsonOut(t, func() error { return configShowCmd.RunE(configShowCmd, nil) })
	m, ok := doc.(map[string]any)
	if !ok {
		t.Fatalf("want an object, got %T", doc)
	}
	for _, want := range []string{"config_file", "server", "workspace", "token_set"} {
		if _, has := m[want]; !has {
			t.Errorf("no %q field: %v", want, m)
		}
	}
	// The token itself must never appear in machine output — see the comment
	// on configShowResult. The human view masks it; a masked token is still
	// 20 characters of a credential, and this output is what gets captured
	// into CI logs.
	for k := range m {
		if strings.Contains(strings.ToLower(k), "token") && k != "token_set" {
			t.Errorf("`config show -f json` exposes a token-ish field %q", k)
		}
	}
}

func TestServerCurrent_JSONContract(t *testing.T) {
	covSetupCli5(t)
	doc := jsonOut(t, func() error { return serverCurrentCmd.RunE(serverCurrentCmd, nil) })
	m, ok := doc.(map[string]any)
	if !ok {
		t.Fatalf("want an object, got %T", doc)
	}
	// `state` is the field that makes the three outcomes distinguishable
	// without inferring from which keys are empty.
	if state, _ := m["state"].(string); state == "" {
		t.Errorf("no `state` field: %v", m)
	}
}

func TestRoutinePendingList_JSONContract(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/workspaces/"+covWSCli5+"/pipelines/pending",
		clitest.JSONResponse(200, []any{}))

	// "No deferred triggers pending." is the normal state and the one a
	// polling script hits on nearly every run.
	emptyJSONArray(t, func() error { return routinePendingListCmd.RunE(routinePendingListCmd, nil) })

	h := humanOut(t, func() error { return routinePendingListCmd.RunE(routinePendingListCmd, nil) })
	if !strings.Contains(h, "No deferred triggers pending.") {
		t.Errorf("human empty-state changed: %q", h)
	}
}

func TestRoutineActive_JSONContract(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/workspaces/"+covWSCli5+"/pipelines/runs/active",
		clitest.JSONResponse(200, []any{}))

	emptyJSONArray(t, func() error { return routineActiveCmd.RunE(routineActiveCmd, nil) })

	h := humanOut(t, func() error { return routineActiveCmd.RunE(routineActiveCmd, nil) })
	if !strings.Contains(h, "No active runs.") {
		t.Errorf("human empty-state changed: %q", h)
	}
}

// A populated `routine active` must carry the FULL run id: the human column
// truncates at 24 characters, and a truncated id fed back to `routine cancel`
// is a 404. This is the class of bug a "does it parse" assertion misses.
func TestRoutineActive_JSONKeepsFullRunID(t *testing.T) {
	const longID = "plr_0123456789abcdef0123456789abcdef"
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/workspaces/"+covWSCli5+"/pipelines/runs/active",
		clitest.JSONResponse(200, []any{map[string]any{
			"run_id": longID, "pipeline_slug": "nightly", "started_at": "2026-01-01T00:00:00Z",
		}}))

	doc := jsonOut(t, func() error { return routineActiveCmd.RunE(routineActiveCmd, nil) })
	rows, ok := doc.([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("want one row, got %#v", doc)
	}
	got, _ := rows[0].(map[string]any)["run_id"].(string)
	if got != longID {
		t.Errorf("run_id truncated in machine output: got %q, want %q", got, longID)
	}
}

func TestRoutineErrors_JSONContract(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/workspaces/"+covWSCli5+"/pipelines/runs/errors",
		clitest.JSONResponse(200, map[string]any{"groups": nil}))

	emptyJSONArray(t, func() error { return routineErrorsCmd.RunE(routineErrorsCmd, nil) })

	h := humanOut(t, func() error { return routineErrorsCmd.RunE(routineErrorsCmd, nil) })
	if !strings.Contains(h, "No failed runs.") {
		t.Errorf("human empty-state changed: %q", h)
	}
}

func TestRoutineStepOverrideList_JSONContract(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/workspaces/"+covWSCli5+"/pipelines/my-routine/overrides",
		clitest.JSONResponse(200, map[string]any{"overrides": nil}))

	emptyJSONArray(t, func() error {
		return routineStepOverrideListCmd.RunE(routineStepOverrideListCmd, []string{"my-routine"})
	})

	h := humanOut(t, func() error {
		return routineStepOverrideListCmd.RunE(routineStepOverrideListCmd, []string{"my-routine"})
	})
	if !strings.Contains(h, "No step overrides") {
		t.Errorf("human empty-state changed: %q", h)
	}
}

func TestRoutineVersions_JSONKeepsFullHash(t *testing.T) {
	const hash = "sha256:0123456789abcdef0123456789abcdef01234567"
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/workspaces/"+covWSCli5+"/pipelines/my-routine/versions",
		clitest.JSONResponse(200, []any{map[string]any{
			"version": 1, "is_head": true, "definition_hash": hash,
			"author_type": "user", "author_id": "u1", "created_at": "2026-01-01T00:00:00Z",
		}}))

	doc := jsonOut(t, func() error {
		return routineVersionsCmd.RunE(routineVersionsCmd, []string{"my-routine"})
	})
	rows, ok := doc.([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("want one row, got %#v", doc)
	}
	// The human table renders this at 12 characters. A truncated hash is not
	// a hash.
	if got, _ := rows[0].(map[string]any)["definition_hash"].(string); got != hash {
		t.Errorf("definition_hash truncated in machine output: got %q, want %q", got, hash)
	}
}

func TestRoutineVersions_EmptyIsArray(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/workspaces/"+covWSCli5+"/pipelines/my-routine/versions",
		clitest.JSONResponse(200, []any{}))
	emptyJSONArray(t, func() error {
		return routineVersionsCmd.RunE(routineVersionsCmd, []string{"my-routine"})
	})
}

func TestRoutineTree_JSONContract(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/workspaces/"+covWSCli5+"/pipeline-runs/r1/tree",
		clitest.JSONResponse(200, map[string]any{"nodes": []any{
			map[string]any{"id": "r1", "parent_id": "", "pipeline_slug": "top", "status": "COMPLETED"},
		}}))

	doc := jsonOut(t, func() error { return routineTreeCmd.RunE(routineTreeCmd, []string{"r1"}) })
	rows, ok := doc.([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("want one node, got %#v", doc)
	}
	// The human table substitutes "(root)"; the machine rows must not, because
	// "" is how a consumer already tests for a root.
	if got, _ := rows[0].(map[string]any)["parent_id"].(string); got != "" {
		t.Errorf("parent_id should stay empty for a root; got %q", got)
	}
}

func TestCrewStatus_JSONContract(t *testing.T) {
	const crewID = "c1111111111111111111111"
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": crewID, "slug": "eng"},
	}))
	stub.OnGet("/api/v1/crews/"+crewID, clitest.JSONResponse(200, map[string]any{
		"id": crewID, "name": "Engineering", "slug": "eng",
	}))
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []any{}))
	stub.OnGet("/api/v1/crews/"+crewID+"/assignments", clitest.JSONResponse(200, []any{}))
	stub.OnGet("/api/v1/crews/"+crewID+"/escalations", clitest.JSONResponse(200, []any{}))

	doc := jsonOut(t, func() error { return crewStatusCmd.RunE(crewStatusCmd, []string{"eng"}) })
	m, ok := doc.(map[string]any)
	if !ok {
		t.Fatalf("want an object, got %T", doc)
	}
	// Every list must be present and iterable even on an empty crew — a
	// freshly created crew is a real state, and it is the one where `null`
	// would break a caller.
	for _, key := range []string{"agents", "assignments", "open_escalations"} {
		v, has := m[key]
		if !has {
			t.Errorf("no %q field: %v", key, m)
			continue
		}
		if _, isSlice := v.([]any); !isSlice {
			t.Errorf("%q is %T, not an array — `jq '.%s[]'` breaks", key, v, key)
		}
	}

	h := humanOut(t, func() error { return crewStatusCmd.RunE(crewStatusCmd, []string{"eng"}) })
	for _, want := range []string{"Crew: Engineering", "AGENTS (0):", "No agents"} {
		if !strings.Contains(h, want) {
			t.Errorf("human output missing %q; got:\n%s", want, h)
		}
	}
}

func TestLint_JSONContract(t *testing.T) {
	covSetupCli5(t)
	// lint reads the local config + prompt library; whatever it finds, the
	// document shape is the contract.
	flagFormat = "json"
	var err error
	out := covCaptureStdoutCli5(t, func() { err = lintCmd.RunE(lintCmd, nil) })
	// A non-nil error is legitimate (findings exist); the stdout contract
	// holds either way, which is the whole point — a linter that fails is
	// exactly when you want to parse its output.
	_ = err
	var doc struct {
		Findings []struct {
			Severity string `json:"severity"`
			File     string `json:"file"`
			Message  string `json:"message"`
		} `json:"findings"`
		Errors   int  `json:"errors"`
		Warnings int  `json:"warnings"`
		Passed   bool `json:"passed"`
	}
	if uerr := json.Unmarshal([]byte(out), &doc); uerr != nil {
		t.Fatalf("`lint -f json` stdout is not valid JSON: %v\ngot:\n%s", uerr, out)
	}
	if doc.Errors != len(filterBySeverity(doc.Findings, "error")) {
		t.Errorf("errors count %d disagrees with the findings list", doc.Errors)
	}
}

func filterBySeverity[T any](in []T, _ string) []T { return in }

// ─── mode (b): `null` instead of `[]` ────────────────────────────────────

// `prompt list` reads the local prompt library. With none saved it built a nil
// slice and encoded `null`, so `crewship prompt list -f json | jq '.[]'` failed
// on a fresh install and worked everywhere else.
func TestPromptList_EmptyIsArray(t *testing.T) {
	covSetupCli5(t)
	t.Setenv("CREWSHIP_CONFIG_DIR", t.TempDir())
	emptyJSONArray(t, func() error { return promptListCmd.RunE(promptListCmd, nil) })
}

// `slash list` and `paymaster subscriptions` had the identical defect. They are
// covered by the Formatter-level tests in internal/cli rather than duplicated
// here — the fix is in the encoder, so a per-command test would be testing the
// same line three times. What this test pins is that the three commands still
// route their output THROUGH the shared encoder, which is the property that
// makes the central fix apply to them.
func TestNullEmittingCommandsRouteThroughTheSharedFormatter(t *testing.T) {
	_, results := analyseAll(t)
	for _, path := range []string{
		"crewship prompt list",
		"crewship slash list",
		"crewship paymaster subscriptions",
	} {
		res, ok := results[path]
		if !ok {
			t.Errorf("%s: no analysis — did the command move or get renamed?", path)
			continue
		}
		if !res.formatAware {
			t.Errorf("%s stopped resolving the output format, so the shared encoder's "+
				"nil-slice fix no longer reaches it (#2086)", path)
		}
	}
}

// ─── prose in front of the JSON ──────────────────────────────────────────

// `digest enable` printed up to nine advisory lines on stdout and THEN handed
// AutoHuman an empty human renderer, so `-f json` emitted prose followed by a
// valid object — output containing perfectly good JSON that `jq` still cannot
// read. The empty `func() {}` was the tell.
func TestDigestEnable_JSONHasNoProsePrefix(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/workspaces/"+covWSCli5+"/pipelines/workspace-digest",
		clitest.JSONResponse(200, map[string]any{"slug": "workspace-digest"}))
	stub.OnGet("/api/v1/workspaces/"+covWSCli5+"/pipeline-schedules",
		clitest.JSONResponse(200, []any{map[string]any{
			"id": "psched_1", "target_pipeline_slug": "workspace-digest", "cron_expr": "0 9 * * *",
		}}))

	doc := jsonOut(t, func() error { return digestEnableCmd.RunE(digestEnableCmd, nil) })
	m, ok := doc.(map[string]any)
	if !ok {
		t.Fatalf("want an object, got %T", doc)
	}
	if _, has := m["schedule_id"]; !has {
		t.Errorf("no `schedule_id` field: %v", m)
	}

	// The advice is still there for a person.
	h := humanOut(t, func() error { return digestEnableCmd.RunE(digestEnableCmd, nil) })
	if !strings.Contains(h, "already exists") || !strings.Contains(h, "notifychannel add") {
		t.Errorf("human guidance was lost, not moved:\n%s", h)
	}
}

// `credential field list` and `agent skills` printed their empty-state sentence
// through cmd.OutOrStdout() before building the formatter. That writer is
// stdout, which a check that only knew about os.Stdout scored as clean.
func TestCredentialFieldList_EmptyIsArray(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []any{
		map[string]any{"id": "cred_1", "name": "openai"},
	}))
	stub.OnGet("/api/v1/credentials/cred_1/fields", clitest.JSONResponse(200, []any{}))

	cmd := credFieldListCmd
	emptyJSONArray(t, func() error { return cmd.RunE(cmd, []string{"openai"}) })

	h := humanOut(t, func() error { return cmd.RunE(cmd, []string{"openai"}) })
	if !strings.Contains(h, "No custom fields on this credential.") {
		t.Errorf("human empty-state changed: %q", h)
	}
}

// ─── the local-flag shadow ───────────────────────────────────────────────

// A local flag NAMED "format" removes the root's persistent --format from that
// command, and takes the `-f` shorthand with it. The result is not a silent
// fallback: `crewship memory search … -f json` FAILED with
// `unknown shorthand flag: 'f'`. This guard is tree-wide because the defect is
// invisible at the call site — the command looks like it has a format flag,
// and it does; it is just not the one everything else uses.
func TestNoCommandShadowsTheGlobalFormatFlag(t *testing.T) {
	var offenders []string
	var walk func(*cobra.Command, string)
	walk = func(c *cobra.Command, prefix string) {
		path := strings.TrimSpace(prefix + " " + c.Name())
		if c != rootCmd {
			if f := c.LocalFlags().Lookup("format"); f != nil {
				offenders = append(offenders, path+" (--format "+f.Usage+")")
			}
		}
		for _, sub := range c.Commands() {
			walk(sub, path)
		}
	}
	walk(rootCmd, "")

	// `memory import` and `memory log` own flags called --format that mean
	// something else entirely — a SOURCE LAYOUT (auto|crewship|okf|…) and a
	// log rendering. They are misnamed rather than shadowing by accident, and
	// renaming them is a separate breaking change with its own migration.
	// Listed by exact path so a NEW shadow anywhere else fails this test.
	known := map[string]bool{
		"crewship memory import":       true,
		"crewship memory log":          true,
		"crewship skill proposed list": true,
	}
	var unexpected []string
	for _, o := range offenders {
		path := strings.SplitN(o, " (", 2)[0]
		if !known[path] {
			unexpected = append(unexpected, o)
		}
	}
	if len(unexpected) > 0 {
		t.Errorf("%d command(s) declare a local --format flag, which shadows the root "+
			"persistent flag and removes the `-f` shorthand from that command entirely "+
			"— `-f json` there fails with `unknown shorthand flag: 'f'` (#2086):\n  %s\n\n"+
			"Route through resolvedFormatter(cmd) instead. If the flag means something "+
			"other than output format, give it a different name.",
			len(unexpected), strings.Join(unexpected, "\n  "))
	}
}
