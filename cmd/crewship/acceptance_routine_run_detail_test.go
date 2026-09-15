package main

// #2577 acceptance — the run-detail and scheduling surface #2460/#2482/#2501
// shipped without a CLI, driven through the BUILT BINARY against the REAL
// api router over a REAL migrated database:
//
//   - `routine save --cron --trigger-inputs` stores the preset the schedule
//     fires with, and the SAME save door is how a 409 schedule_conflict is
//     escaped (the hint the server prints names it);
//   - `routine run --fire-at` parks a one-time start pinned to the accepted
//     archive, `--pinned-version` runs an archived recipe now, and `--async`
//     sends `Prefer: respond-async` and understands the 202 IN_PROGRESS
//     receipt;
//   - `routine calendar`, `routine executions` and `routine artifacts` read
//     the three endpoints the routines workspace uses and nothing else.
//
// Nothing is stubbed: the schedule store, run store and execution store are
// the ones cmd_start.go wires, so the executions the CLI lists are the rows
// a real transform run wrote.

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/testutil"
)

const routineDetailAcceptanceWorkspaceID = "cdetailws0000000000001"

// routineDetailV1Def declares one required, typed input so a schedule
// preset that omits it is refused — the shape the #2495 preset gate exists
// for. routineDetailV2Def renames that input: the smallest change that
// invalidates a stored preset and forces the 409 escape hatch.
const routineDetailV1Def = `{"dsl_version":"1.0","name":"acceptance-detail","inputs":[{"name":"who","type":"string","widget":"text","required":true}],"steps":[{"id":"greet","type":"transform","transform":{"input":"{{ inputs.who }}","expression":"."}}]}`
const routineDetailV2Def = `{"dsl_version":"1.0","name":"acceptance-detail","inputs":[{"name":"recipient","type":"string","widget":"text","required":true}],"steps":[{"id":"greet","type":"transform","transform":{"input":"{{ inputs.recipient }}","expression":"."}}]}`

// routineDetailSlowDef parks on a datetime wait the caller controls, so an
// async start is observed while the run is still in flight rather than
// racing a transform that finishes in microseconds.
const routineDetailSlowDef = `{"dsl_version":"1.0","name":"acceptance-slow","inputs":[{"name":"until","type":"string"}],"steps":[{"id":"nap","type":"wait","wait":{"kind":"datetime","until":"{{ inputs.until }}"}}]}`

func startRoutineDetailAcceptanceServer(t *testing.T) (cfgPath string, db *sql.DB) {
	t.Helper()

	dbh := testutil.MigratedDB(t)
	db = dbh.DB
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Detail', 'detail-ws')`, routineDetailAcceptanceWorkspaceID)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('dtl-owner', 'owner@dtl-ex.com', 'Owner')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('dtlm-owner', ?, 'dtl-owner', 'OWNER')`,
		routineDetailAcceptanceWorkspaceID)
	mustExec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode, container_memory_mb, container_cpus)
		VALUES ('dtl-crew', ?, 'Crew', 'dtl-crew', 'free', 4096, 2.0)`, routineDetailAcceptanceWorkspaceID)

	const ownerToken = "crewship_cli_dtlowner0000000000000000000"
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-dtl-owner', 'dtl-owner', 't', ?, datetime('now'))`,
		sha256HexToken(ownerToken))

	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	router.PipelinesHandler.SetSaveTokenSecret([]byte("acceptance-test-save-token-secret-32b"))
	router.PipelinesHandler.SetScheduleStore(pipeline.NewScheduleStore(db))
	router.PipelinesHandler.SetRunner(unusedAgentRunner{})
	// RunStore is what makes a run durable AND what turns on the execution
	// store the executions endpoint reads (executor_factory.go) — without
	// it a run leaves no pipeline_runs row and no step executions.
	router.PipelinesHandler.SetRunStore(pipeline.NewRunStore(db))
	router.PipelinesHandler.SetRunRegistry(pipeline.NewRunRegistry())
	waitpoints := pipeline.NewSQLWaitpointStore(db)
	t.Cleanup(waitpoints.Close)
	router.PipelinesHandler.SetWaitpointStore(waitpoints)

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	cfgPath = filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + srv.URL + "\nworkspace: " + routineDetailAcceptanceWorkspaceID +
		"\ntoken: " + ownerToken + "\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath, db
}

func runRoutineDetailCLI(t *testing.T, cfgPath string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func writeRoutineDetailDefinition(t *testing.T, name, def string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".json")
	if err := os.WriteFile(path, []byte(def), 0o600); err != nil {
		t.Fatalf("write definition: %v", err)
	}
	return path
}

var routineDetailRunIDRE = regexp.MustCompile(`Run (\S+): `)

// saveRoutineDetailV1 saves the v1 recipe with a nightly plan whose preset
// satisfies its required input — the starting state every test below
// builds on. Returns nothing; failures are fatal.
func saveRoutineDetailV1(t *testing.T, cfgPath string) {
	t.Helper()
	out, err := runRoutineDetailCLI(t, cfgPath, "routine", "save",
		"--name", "acceptance-detail",
		"--definition", writeRoutineDetailDefinition(t, "v1", routineDetailV1Def),
		"--author-crew", "dtl-crew",
		"--sample-inputs", `{"who":"sample"}`,
		"--cron", "0 9 * * *",
		"--trigger-inputs", "who=alice",
	)
	if err != nil {
		t.Fatalf("routine save v1 failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Trigger: schedule 0 9 * * *") {
		t.Fatalf("expected the save to report its schedule trigger, got:\n%s", out)
	}
}

// TestAcceptance_RoutineSave_TriggerInputs_EscapesScheduleConflict is the
// whole reason --trigger-inputs exists: without it the 409 hint ("Update this
// plan's inputs in the same save") named a door the CLI could not open.
func TestAcceptance_RoutineSave_TriggerInputs_EscapesScheduleConflict(t *testing.T) {
	cfgPath, db := startRoutineDetailAcceptanceServer(t)
	saveRoutineDetailV1(t, cfgPath)

	var preset string
	if err := db.QueryRow(`SELECT inputs_json FROM pipeline_schedules WHERE name='acceptance-detail' AND deleted_at IS NULL`).Scan(&preset); err != nil {
		t.Fatalf("read the plan's preset: %v", err)
	}
	if preset != `{"who":"alice"}` {
		t.Fatalf("--trigger-inputs did not reach the schedule preset, stored %q", preset)
	}

	// Renaming the required input without carrying the plan along is the
	// refusal the gate exists for — and it must come back as the structured
	// conflict, not "Failed to save".
	v2 := writeRoutineDetailDefinition(t, "v2", routineDetailV2Def)
	conflictOut, err := runRoutineDetailCLI(t, cfgPath, "routine", "save",
		"--name", "acceptance-detail",
		"--definition", v2,
		"--author-crew", "dtl-crew",
		"--sample-inputs", `{"recipient":"sample"}`,
	)
	if err == nil {
		t.Fatalf("expected the v2 save to be refused by the schedule preset gate, got:\n%s", conflictOut)
	}
	if !strings.Contains(conflictOut, "409") || !strings.Contains(conflictOut, "trigger") {
		t.Fatalf("expected a 409 whose hint names the trigger block, got:\n%s", conflictOut)
	}

	// The escape the hint prescribes: the same save carrying the repaired
	// preset. The plan is upserted BEFORE the gate runs, so this lands.
	// The value carries a comma on purpose: the flag is a StringArray, so
	// cobra must not split "bob,carol" into two pairs (a JSON list or a
	// comma-bearing string preset would otherwise never reach the server).
	escapeOut, err := runRoutineDetailCLI(t, cfgPath, "routine", "save",
		"--name", "acceptance-detail",
		"--definition", v2,
		"--author-crew", "dtl-crew",
		"--sample-inputs", `{"recipient":"sample"}`,
		"--cron", "0 9 * * *",
		"--trigger-inputs", "recipient=bob,carol",
	)
	if err != nil {
		t.Fatalf("expected the save carrying --trigger-inputs to escape the conflict: %v\n%s", err, escapeOut)
	}
	if err := db.QueryRow(`SELECT inputs_json FROM pipeline_schedules WHERE name='acceptance-detail' AND deleted_at IS NULL`).Scan(&preset); err != nil {
		t.Fatalf("re-read the plan's preset: %v", err)
	}
	if preset != `{"recipient":"bob,carol"}` {
		t.Fatalf("the escape save did not repair the preset (or split the value on the comma), stored %q", preset)
	}

	// A malformed pair is a CLI error before any request is made.
	badOut, err := runRoutineDetailCLI(t, cfgPath, "routine", "save",
		"--name", "acceptance-detail", "--definition", v2, "--author-crew", "dtl-crew",
		"--cron", "0 9 * * *", "--trigger-inputs", "no-equals-sign")
	if err == nil || !strings.Contains(badOut, "key=value") {
		t.Fatalf("expected a key=value usage error, got err=%v:\n%s", err, badOut)
	}
}

// TestAcceptance_RoutineRun_FireAtPinnedVersionAsync covers the three run
// body fields the UI could send and the CLI could not: fire_at (a one-time
// start pinned to the accepted archive, visible in `pending list` with its
// RECIPE VERSION), pinned_version (an archived recipe run now, refused when
// the version does not exist), and Prefer: respond-async (202 IN_PROGRESS).
func TestAcceptance_RoutineRun_FireAtPinnedVersionAsync(t *testing.T) {
	cfgPath, _ := startRoutineDetailAcceptanceServer(t)
	saveRoutineDetailV1(t, cfgPath)

	fireAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second).Format(time.RFC3339)
	deferredOut, err := runRoutineDetailCLI(t, cfgPath, "routine", "run", "acceptance-detail",
		"--inputs", `{"who":"carol"}`, "--fire-at", fireAt)
	if err != nil {
		t.Fatalf("routine run --fire-at failed: %v\n%s", err, deferredOut)
	}
	if !strings.Contains(deferredOut, "Scheduled: pending ") || !strings.Contains(deferredOut, "pending cancel") {
		t.Fatalf("expected a SCHEDULED receipt with the cancel hint, got:\n%s", deferredOut)
	}

	pendingOut, err := runRoutineDetailCLI(t, cfgPath, "routine", "pending", "list")
	if err != nil {
		t.Fatalf("routine pending list failed: %v\n%s", err, pendingOut)
	}
	// The one-time start is pinned to the archive that passed preflight —
	// v1, the only version — and the pending list says so.
	if !strings.Contains(pendingOut, "acceptance-detail") || !strings.Contains(pendingOut, "v1") {
		t.Fatalf("expected the pending list to show the start pinned to v1, got:\n%s", pendingOut)
	}
	// -f json carries the row's inputs preview, not just the columns.
	pendingJSON, err := runRoutineDetailCLI(t, cfgPath, "routine", "pending", "list", "-f", "json")
	if err != nil {
		t.Fatalf("routine pending list -f json failed: %v\n%s", err, pendingJSON)
	}
	var pendingRows []struct {
		PinnedVersion *int           `json:"pinned_version"`
		Inputs        map[string]any `json:"inputs"`
	}
	if err := json.Unmarshal([]byte(pendingJSON), &pendingRows); err != nil || len(pendingRows) != 1 {
		t.Fatalf("pending list JSON is not the endpoint's row list (err=%v):\n%s", err, pendingJSON)
	}
	if pendingRows[0].PinnedVersion == nil || *pendingRows[0].PinnedVersion != 1 || pendingRows[0].Inputs["who"] != "carol" {
		t.Fatalf("pending row should carry pinned_version 1 and the inputs preview, got %+v", pendingRows[0])
	}

	// fire_at cannot be combined with a delay — the server says so and the
	// CLI must relay it rather than swallow it.
	comboOut, err := runRoutineDetailCLI(t, cfgPath, "routine", "run", "acceptance-detail",
		"--inputs", `{"who":"carol"}`, "--fire-at", fireAt, "--delay", "30")
	if err == nil || !strings.Contains(comboOut, "fire_at cannot be combined") {
		t.Fatalf("expected the fire_at+delay refusal to surface, got err=%v:\n%s", err, comboOut)
	}

	pinnedOut, err := runRoutineDetailCLI(t, cfgPath, "routine", "run", "acceptance-detail",
		"--inputs", `{"who":"dave"}`, "--pinned-version", "1")
	if err != nil {
		t.Fatalf("routine run --pinned-version 1 failed: %v\n%s", err, pinnedOut)
	}
	if !strings.Contains(pinnedOut, "COMPLETED") || !strings.Contains(pinnedOut, "dave") {
		t.Fatalf("expected an immediate COMPLETED run of the pinned recipe, got:\n%s", pinnedOut)
	}

	missingOut, err := runRoutineDetailCLI(t, cfgPath, "routine", "run", "acceptance-detail",
		"--inputs", `{"who":"dave"}`, "--pinned-version", "99")
	if err == nil || !strings.Contains(missingOut, "recipe version not found") {
		t.Fatalf("expected a 404 for an archive that does not exist, got err=%v:\n%s", err, missingOut)
	}

	// Async: the CLI must send Prefer: respond-async and understand the 202
	// receipt. A datetime wait keeps the run in flight long enough that the
	// receipt is the only possible answer; --wait then follows it to the end
	// so the test (and the DB) outlive the background run.
	slowOut, err := runRoutineDetailCLI(t, cfgPath, "routine", "save",
		"--name", "acceptance-slow",
		"--definition", writeRoutineDetailDefinition(t, "slow", routineDetailSlowDef),
		"--author-crew", "dtl-crew",
		"--sample-inputs", `{"until":"2000-01-01T00:00:00Z"}`,
	)
	if err != nil {
		t.Fatalf("routine save acceptance-slow failed: %v\n%s", err, slowOut)
	}
	until := time.Now().UTC().Add(3 * time.Second).Format(time.RFC3339)
	asyncOut, err := runRoutineDetailCLI(t, cfgPath, "routine", "run", "acceptance-slow",
		"--inputs", `{"until":"`+until+`"}`, "--async", "--wait", "--wait-timeout", "1m")
	if err != nil {
		t.Fatalf("routine run --async --wait failed: %v\n%s", err, asyncOut)
	}
	if !strings.Contains(asyncOut, "IN_PROGRESS") {
		t.Fatalf("expected the async receipt to be reported as IN_PROGRESS, got:\n%s", asyncOut)
	}
	if !strings.Contains(asyncOut, "COMPLETED") {
		t.Fatalf("expected --wait to follow the async run to COMPLETED, got:\n%s", asyncOut)
	}
}

// TestAcceptance_RoutineRunDetail_CalendarExecutionsArtifacts drives the three
// read endpoints. The executions are the rows a real run wrote; the artifact
// is seeded because publishing one needs an agent runtime, and the CLI only
// reads them.
func TestAcceptance_RoutineRunDetail_CalendarExecutionsArtifacts(t *testing.T) {
	cfgPath, db := startRoutineDetailAcceptanceServer(t)
	saveRoutineDetailV1(t, cfgPath)

	runOut, err := runRoutineDetailCLI(t, cfgPath, "routine", "run", "acceptance-detail", "--inputs", `{"who":"erin"}`)
	if err != nil {
		t.Fatalf("routine run failed: %v\n%s", err, runOut)
	}
	m := routineDetailRunIDRE.FindStringSubmatch(runOut)
	if m == nil {
		t.Fatalf("could not find the run id in:\n%s", runOut)
	}
	runID := m[1]

	fireAt := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second).Format(time.RFC3339)
	if out, err := runRoutineDetailCLI(t, cfgPath, "routine", "run", "acceptance-detail",
		"--inputs", `{"who":"frank"}`, "--fire-at", fireAt); err != nil {
		t.Fatalf("routine run --fire-at failed: %v\n%s", err, out)
	}

	// --- executions -----------------------------------------------------
	execOut, err := runRoutineDetailCLI(t, cfgPath, "routine", "executions", runID)
	if err != nil {
		t.Fatalf("routine executions failed: %v\n%s", err, execOut)
	}
	for _, want := range []string{"greet", "transform", "completed", "/greet"} {
		if !strings.Contains(execOut, want) {
			t.Fatalf("executions table is missing %q:\n%s", want, execOut)
		}
	}
	execJSON, err := runRoutineDetailCLI(t, cfgPath, "routine", "executions", runID, "-f", "json")
	if err != nil {
		t.Fatalf("routine executions -f json failed: %v\n%s", err, execJSON)
	}
	var execPage struct {
		Rows []struct {
			ID          string `json:"id"`
			StepID      string `json:"step_id"`
			OutputBytes int    `json:"output_bytes"`
		} `json:"rows"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(execJSON), &execPage); err != nil {
		t.Fatalf("executions JSON is not the endpoint's page shape: %v\n%s", err, execJSON)
	}
	if len(execPage.Rows) != 1 || execPage.Rows[0].StepID != "greet" || execPage.NextCursor != nil {
		t.Fatalf("unexpected executions page: %+v", execPage)
	}
	// The list carries sizes, never transcripts; the output is a second call.
	if strings.Contains(execJSON, "erin") {
		t.Fatalf("the executions list inlined an output:\n%s", execJSON)
	}
	outputOut, err := runRoutineDetailCLI(t, cfgPath, "routine", "executions", runID, "--execution-id", execPage.Rows[0].ID)
	if err != nil {
		t.Fatalf("routine executions --execution-id failed: %v\n%s", err, outputOut)
	}
	if !strings.Contains(outputOut, "erin") {
		t.Fatalf("expected the execution output, got:\n%s", outputOut)
	}
	if out, err := runRoutineDetailCLI(t, cfgPath, "routine", "executions", runID, "--execution-id", "exec_nope"); err == nil || !strings.Contains(out, "404") {
		t.Fatalf("expected a 404 for an unknown execution, got err=%v:\n%s", err, out)
	}
	if out, err := runRoutineDetailCLI(t, cfgPath, "routine", "executions", runID, "--after", "notanumber"); err == nil || !strings.Contains(out, "400") {
		t.Fatalf("expected a 400 for a malformed cursor, got err=%v:\n%s", err, out)
	}
	if out, err := runRoutineDetailCLI(t, cfgPath, "routine", "executions", "run_does_not_exist"); err == nil || !strings.Contains(out, "404") {
		t.Fatalf("expected a 404 for an unknown run, got err=%v:\n%s", err, out)
	}

	// --- artifacts ------------------------------------------------------
	if _, err := db.Exec(`INSERT INTO pipeline_run_artifacts (id, run_id, step_execution_id, kind, label, state, content_type, content, source, created_at)
		VALUES ('art_acc_1', ?, ?, 'text', 'summary.md', 'available', 'text/markdown', '# Erin report', 'declared', datetime('now'))`,
		runID, execPage.Rows[0].ID); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	artOut, err := runRoutineDetailCLI(t, cfgPath, "routine", "artifacts", runID)
	if err != nil {
		t.Fatalf("routine artifacts failed: %v\n%s", err, artOut)
	}
	for _, want := range []string{"art_acc_1", "summary.md", "text/markdown", "available"} {
		if !strings.Contains(artOut, want) {
			t.Fatalf("artifacts table is missing %q:\n%s", want, artOut)
		}
	}
	if strings.Contains(artOut, "Erin report") {
		t.Fatalf("the artifacts list inlined a content body:\n%s", artOut)
	}
	artJSON, err := runRoutineDetailCLI(t, cfgPath, "routine", "artifacts", runID, "-f", "json")
	if err != nil {
		t.Fatalf("routine artifacts -f json failed: %v\n%s", err, artJSON)
	}
	var artPage struct {
		Artifacts  []struct{ ID string } `json:"artifacts"`
		Truncated  bool                  `json:"truncated"`
		NextCursor *string               `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(artJSON), &artPage); err != nil || len(artPage.Artifacts) != 1 || artPage.Artifacts[0].ID != "art_acc_1" {
		t.Fatalf("artifacts JSON is not the endpoint's page shape (err=%v):\n%s", err, artJSON)
	}
	contentOut, err := runRoutineDetailCLI(t, cfgPath, "routine", "artifacts", runID, "--artifact-id", "art_acc_1")
	if err != nil {
		t.Fatalf("routine artifacts --artifact-id failed: %v\n%s", err, contentOut)
	}
	if !strings.Contains(contentOut, "# Erin report") {
		t.Fatalf("expected the artifact content, got:\n%s", contentOut)
	}
	// A download names a stored file blob; a text artifact has none, and the
	// server's 404 must reach the caller rather than an empty file.
	if out, err := runRoutineDetailCLI(t, cfgPath, "routine", "artifacts", runID, "--download", "art_acc_1", "--out", "-"); err == nil || !strings.Contains(out, "404") {
		t.Fatalf("expected a 404 for a download with no blob, got err=%v:\n%s", err, out)
	}

	// --- calendar -------------------------------------------------------
	from := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	to := time.Now().UTC().Add(3 * 24 * time.Hour).Format(time.RFC3339)
	calOut, err := runRoutineDetailCLI(t, cfgPath, "routine", "calendar", "--from", from, "--to", to)
	if err != nil {
		t.Fatalf("routine calendar failed: %v\n%s", err, calOut)
	}
	// All three kinds in one window: the plan's next occurrences, the
	// one-time start parked above, and the run that already executed.
	for _, want := range []string{"planned", "pending", "run", "acceptance-detail", runID} {
		if !strings.Contains(calOut, want) {
			t.Fatalf("calendar is missing %q:\n%s", want, calOut)
		}
	}
	calJSON, err := runRoutineDetailCLI(t, cfgPath, "routine", "calendar", "--from", from, "--to", to, "-f", "json")
	if err != nil {
		t.Fatalf("routine calendar -f json failed: %v\n%s", err, calJSON)
	}
	var cal struct {
		Events []struct {
			Kind   string         `json:"kind"`
			Inputs map[string]any `json:"inputs"`
		} `json:"events"`
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(calJSON), &cal); err != nil {
		t.Fatalf("calendar JSON is not the endpoint's shape: %v\n%s", err, calJSON)
	}
	kinds := map[string]int{}
	for _, e := range cal.Events {
		kinds[e.Kind]++
		if e.Kind == "planned" && e.Inputs["who"] != "alice" {
			t.Fatalf("planned event should preview the --trigger-inputs preset, got %+v", e.Inputs)
		}
	}
	if kinds["planned"] == 0 || kinds["pending"] != 1 || kinds["run"] == 0 {
		t.Fatalf("expected planned/pending/run events, got %v", kinds)
	}
	// The default window is "from now, one week" — no flags is a valid call.
	if out, err := runRoutineDetailCLI(t, cfgPath, "routine", "calendar"); err != nil || !strings.Contains(out, "planned") {
		t.Fatalf("expected the default window to list the plan, got err=%v:\n%s", err, out)
	}
	wide := time.Now().UTC().Add(40 * 24 * time.Hour).Format(time.RFC3339)
	if out, err := runRoutineDetailCLI(t, cfgPath, "routine", "calendar", "--from", from, "--to", wide); err == nil || !strings.Contains(out, "32 days") {
		t.Fatalf("expected the 32-day cap to surface as the server's 400, got err=%v:\n%s", err, out)
	}
	if out, err := runRoutineDetailCLI(t, cfgPath, "routine", "calendar", "--from", "yesterday"); err == nil || !strings.Contains(out, "RFC3339") {
		t.Fatalf("expected a malformed --from to be refused, got err=%v:\n%s", err, out)
	}
}

// TestAcceptance_RoutineFixtureTest_Remote drives POST .../pipelines/fixture_test
// through the CLI. The default stays in-process (the command's documented
// promise is "without contacting Crewship"); --remote asks the server's
// build for the same verdict, which is what an agent inside a container —
// with a token but no local Go — needs.
func TestAcceptance_RoutineFixtureTest_Remote(t *testing.T) {
	cfgPath, _ := startRoutineDetailAcceptanceServer(t)
	recipe := writeRoutineDetailDefinition(t, "fixture", `{"name":"fixture-cli","inputs":[{"name":"count","type":"integer","widget":"number","default":0}],"steps":[{"id":"a","type":"transform","transform":{"input":"{{ inputs.count }}","expression":"."},"validation":{"must_contain":["0"]}}]}`)

	okOut, err := runRoutineDetailCLI(t, cfgPath, "routine", "fixture-test", recipe, "--step", "a", "--input", `{"count":0}`, "--remote")
	if err != nil {
		t.Fatalf("fixture-test --remote failed: %v\n%s", err, okOut)
	}
	for _, want := range []string{`"execution_mode": "fixtures"`, `"fixture_hash"`, `"valid": true`} {
		if !strings.Contains(okOut, want) {
			t.Fatalf("remote fixture report is missing %s:\n%s", want, okOut)
		}
	}
	// The server's verdict drives the exit code exactly as the local one does.
	badOut, err := runRoutineDetailCLI(t, cfgPath, "routine", "fixture-test", recipe, "--step", "a", "--input", `{"count":7}`, "--remote")
	if err == nil || !strings.Contains(badOut, "failed validation") {
		t.Fatalf("expected the remote verdict to fail the CLI, got err=%v:\n%s", err, badOut)
	}
	// A step the recipe does not have is the endpoint's 400, relayed verbatim.
	noStep, err := runRoutineDetailCLI(t, cfgPath, "routine", "fixture-test", recipe, "--step", "zzz", "--remote")
	if err == nil || !strings.Contains(noStep, "400") {
		t.Fatalf("expected the endpoint's 400 for an unknown step, got err=%v:\n%s", err, noStep)
	}
}
