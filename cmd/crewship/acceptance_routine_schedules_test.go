package main

// Binary acceptance for the routine schedule lifecycle:
//
//   - `routine schedules create` → POST   .../pipeline-schedules
//   - `routine schedules list`   → GET    .../pipeline-schedules
//   - `routine schedules update` → PATCH  .../pipeline-schedules/{id}
//   - `routine schedules enable` / `disable` → the same PATCH with "enabled"
//   - `routine schedules now`    → POST   .../pipeline-schedules/{id}/run
//
// Driven through the BUILT BINARY against the REAL api router over a REAL
// migrated database, so the role tiers (create = role-or-capability inline
// gate, update = manage tier, now = create tier) are the ones production
// enforces. Preview and activate have their own binary tests
// (acceptance_routine_reliability_test.go, acceptance_routine_trigger_test.go);
// delete is deliberately not covered here.

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/testutil"
)

const routineSchedulesAcceptanceWorkspaceID = "cschedws00000000000001"

const routineSchedulesDef = `{"dsl_version":"1.0","name":"acceptance-sched","inputs":[{"name":"who","type":"string","widget":"text","default":"world"}],"steps":[{"id":"greet","type":"transform","transform":{"input":"{{ inputs.who }}","expression":"."}}]}`

const (
	routineSchedulesOwnerToken  = "crewship_cli_schedowner00000000000000000"
	routineSchedulesMemberToken = "crewship_cli_schedmember0000000000000000"
)

// startRoutineSchedulesAcceptanceServer boots the real router with the
// schedule store and a runner wired, and returns one CLI config per role.
func startRoutineSchedulesAcceptanceServer(t *testing.T) (ownerCfg, memberCfg string, db *sql.DB) {
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
	ws := routineSchedulesAcceptanceWorkspaceID
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Sched', 'sched-ws')`, ws)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('sch-owner', 'owner@sch-ex.com', 'Owner')`)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('sch-member', 'member@sch-ex.com', 'Member')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('schm-owner', ?, 'sch-owner', 'OWNER')`, ws)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('schm-member', ?, 'sch-member', 'MEMBER')`, ws)
	mustExec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode, container_memory_mb, container_cpus)
		VALUES ('sch-crew', ?, 'Crew', 'sch-crew', 'free', 4096, 2.0)`, ws)
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-sch-owner', 'sch-owner', 't', ?, datetime('now'))`,
		sha256HexToken(routineSchedulesOwnerToken))
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-sch-member', 'sch-member', 't', ?, datetime('now'))`,
		sha256HexToken(routineSchedulesMemberToken))

	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	router.PipelinesHandler.SetSaveTokenSecret([]byte("acceptance-test-save-token-secret-32b"))
	router.PipelinesHandler.SetScheduleStore(pipeline.NewScheduleStore(db))
	router.PipelinesHandler.SetRunner(unusedAgentRunner{})
	router.PipelinesHandler.SetRunStore(pipeline.NewRunStore(db))
	router.PipelinesHandler.SetRunRegistry(pipeline.NewRunRegistry())

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	writeCfg := func(name, token string) string {
		path := filepath.Join(t.TempDir(), name+"-cli-config.yaml")
		cfg := "server: " + srv.URL + "\nworkspace: " + ws + "\ntoken: " + token + "\nformat: table\n"
		if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		return path
	}
	return writeCfg("owner", routineSchedulesOwnerToken), writeCfg("member", routineSchedulesMemberToken), db
}

// runRoutineSchedulesCLI returns stdout and stderr separately so the JSON
// cases can prove stdout is a clean document.
func runRoutineSchedulesCLI(t *testing.T, cfgPath string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

func saveRoutineSchedulesTarget(t *testing.T, cfgPath string) {
	t.Helper()
	def := filepath.Join(t.TempDir(), "sched.json")
	if err := os.WriteFile(def, []byte(routineSchedulesDef), 0o600); err != nil {
		t.Fatalf("write definition: %v", err)
	}
	out, stderr, err := runRoutineSchedulesCLI(t, cfgPath, "routine", "save",
		"--name", "acceptance-sched",
		"--definition", def,
		"--author-crew", "sch-crew",
		"--sample-inputs", `{"who":"sample"}`,
	)
	if err != nil {
		t.Fatalf("routine save failed: %v\n%s%s", err, out, stderr)
	}
}

type routineSchedulesJSONRow struct {
	ID                 string         `json:"id"`
	Name               string         `json:"name"`
	TargetPipelineSlug string         `json:"target_pipeline_slug"`
	CronExpr           string         `json:"cron_expr"`
	Timezone           string         `json:"timezone"`
	Inputs             map[string]any `json:"inputs"`
	Enabled            bool           `json:"enabled"`
}

type routineSchedulesJSONAck struct {
	ID      string `json:"id"`
	Fired   bool   `json:"fired"`
	Updated bool   `json:"updated"`
	Enabled *bool  `json:"enabled"`
}

func listRoutineSchedulesJSON(t *testing.T, cfgPath string) []routineSchedulesJSONRow {
	t.Helper()
	out, stderr, err := runRoutineSchedulesCLI(t, cfgPath, "-f", "json", "routine", "schedules", "list")
	if err != nil {
		t.Fatalf("schedules list -f json failed: %v\n%s%s", err, out, stderr)
	}
	var rows []routineSchedulesJSONRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("schedules list JSON is not a row array: %v\n%s", err, out)
	}
	return rows
}

// TestAcceptanceRoutineSchedules_Lifecycle drives create → list → update →
// disable → enable → now as the OWNER and checks every machine-readable
// reply carries the schedule id (and, where it applies, enabled).
func TestAcceptanceRoutineSchedules_Lifecycle(t *testing.T) {
	ownerCfg, _, db := startRoutineSchedulesAcceptanceServer(t)
	saveRoutineSchedulesTarget(t, ownerCfg)

	// Empty list is "[]" in JSON and a hint in the table, never "null".
	if rows := listRoutineSchedulesJSON(t, ownerCfg); len(rows) != 0 {
		t.Fatalf("expected no schedules yet, got %+v", rows)
	}
	emptyOut, _, err := runRoutineSchedulesCLI(t, ownerCfg, "routine", "schedules", "list")
	if err != nil || !strings.Contains(emptyOut, "No schedules in this workspace.") {
		t.Fatalf("expected the empty-list hint, got err=%v:\n%s", err, emptyOut)
	}

	// --- create -----------------------------------------------------------
	createOut, createErr, err := runRoutineSchedulesCLI(t, ownerCfg, "-f", "json", "routine", "schedules", "create",
		"--slug", "acceptance-sched", "--name", "nightly", "--cron", "0 9 * * *",
		"--timezone", "Europe/Prague", "--inputs", `{"who":"alice"}`)
	if err != nil {
		t.Fatalf("schedules create failed: %v\n%s%s", err, createOut, createErr)
	}
	var created routineSchedulesJSONRow
	if err := json.Unmarshal([]byte(createOut), &created); err != nil {
		t.Fatalf("schedules create -f json is not one schedule document: %v\n%s", err, createOut)
	}
	if created.ID == "" || !created.Enabled || created.Name != "nightly" || created.CronExpr != "0 9 * * *" ||
		created.Timezone != "Europe/Prague" || created.TargetPipelineSlug != "acceptance-sched" || created.Inputs["who"] != "alice" {
		t.Fatalf("created schedule does not carry the requested fields: %+v", created)
	}
	id := created.ID

	// Human create output names the full id so it can be pasted into `now`.
	humanOut, _, err := runRoutineSchedulesCLI(t, ownerCfg, "routine", "schedules", "create",
		"--slug", "acceptance-sched", "--name", "hourly", "--cron", "0 * * * *", "--enabled=false")
	if err != nil || !strings.Contains(humanOut, "Schedule created: hourly (0 * * * * @ UTC)") || !strings.Contains(humanOut, "  ID:     psched") {
		t.Fatalf("expected the human create receipt with the full id, got err=%v:\n%s", err, humanOut)
	}

	// --- list -------------------------------------------------------------
	rows := listRoutineSchedulesJSON(t, ownerCfg)
	if len(rows) != 2 {
		t.Fatalf("expected two schedules, got %+v", rows)
	}
	byName := map[string]routineSchedulesJSONRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	if byName["nightly"].ID != id || !byName["nightly"].Enabled || byName["hourly"].Enabled {
		t.Fatalf("list does not reflect the created schedules: %+v", rows)
	}
	tableOut, _, err := runRoutineSchedulesCLI(t, ownerCfg, "routine", "schedules", "list", "--slug", "acceptance-sched")
	if err != nil {
		t.Fatalf("schedules list failed: %v\n%s", err, tableOut)
	}
	for _, want := range []string{"ID", "ROUTINE", "ENABLED", id, "nightly", "acceptance-sched", "0 9 * * *", "Europe/Prague", "yes"} {
		if !strings.Contains(tableOut, want) {
			t.Fatalf("schedules table is missing %q:\n%s", want, tableOut)
		}
	}
	if out, _, err := runRoutineSchedulesCLI(t, ownerCfg, "routine", "schedules", "list", "--slug", "no-such-routine"); err != nil ||
		!strings.Contains(out, `No schedules for routine "no-such-routine".`) {
		t.Fatalf("expected the filtered empty-list hint, got err=%v:\n%s", err, out)
	}

	// --- update -----------------------------------------------------------
	updOut, updErr, err := runRoutineSchedulesCLI(t, ownerCfg, "-f", "json", "routine", "schedules", "update", id,
		"--cron", "30 8 * * 1-5", "--name", "weekday-morning")
	if err != nil {
		t.Fatalf("schedules update failed: %v\n%s%s", err, updOut, updErr)
	}
	var upd routineSchedulesJSONAck
	if err := json.Unmarshal([]byte(updOut), &upd); err != nil || upd.ID != id || !upd.Updated {
		t.Fatalf("update ack is not {id, updated:true} (err=%v):\n%s", err, updOut)
	}
	var cronExpr, name string
	if err := db.QueryRow(`SELECT cron_expr, name FROM pipeline_schedules WHERE id = ?`, id).Scan(&cronExpr, &name); err != nil {
		t.Fatalf("read updated schedule: %v", err)
	}
	if cronExpr != "30 8 * * 1-5" || name != "weekday-morning" {
		t.Fatalf("update did not reach the store: cron=%q name=%q", cronExpr, name)
	}
	if out, stderr, err := runRoutineSchedulesCLI(t, ownerCfg, "routine", "schedules", "update", id); err == nil ||
		!strings.Contains(out+stderr, "at least one of --cron") {
		t.Fatalf("expected an update with no flags to be refused locally, got err=%v:\n%s%s", err, out, stderr)
	}

	// --- disable / enable --------------------------------------------------
	for _, step := range []struct {
		verb string
		want bool
	}{{"disable", false}, {"enable", true}} {
		out, stderr, err := runRoutineSchedulesCLI(t, ownerCfg, "-f", "json", "routine", "schedules", step.verb, id)
		if err != nil {
			t.Fatalf("schedules %s failed: %v\n%s%s", step.verb, err, out, stderr)
		}
		var ack routineSchedulesJSONAck
		if err := json.Unmarshal([]byte(out), &ack); err != nil || ack.ID != id || ack.Enabled == nil || *ack.Enabled != step.want {
			t.Fatalf("schedules %s ack must carry id and enabled=%v (err=%v):\n%s", step.verb, step.want, err, out)
		}
		var enabled bool
		if err := db.QueryRow(`SELECT enabled FROM pipeline_schedules WHERE id = ?`, id).Scan(&enabled); err != nil {
			t.Fatalf("read enabled: %v", err)
		}
		if enabled != step.want {
			t.Fatalf("schedules %s did not reach the store: enabled=%v", step.verb, enabled)
		}
		human, _, err := runRoutineSchedulesCLI(t, ownerCfg, "routine", "schedules", step.verb, id)
		if err != nil || !strings.Contains(human, "Schedule "+id+" "+step.verb+"d.") {
			t.Fatalf("expected the human %s receipt, got err=%v:\n%s", step.verb, err, human)
		}
	}

	// --- now --------------------------------------------------------------
	nowOut, nowErr, err := runRoutineSchedulesCLI(t, ownerCfg, "-f", "json", "routine", "schedules", "now", id)
	if err != nil {
		t.Fatalf("schedules now failed: %v\n%s%s", err, nowOut, nowErr)
	}
	var fired routineSchedulesJSONAck
	if err := json.Unmarshal([]byte(nowOut), &fired); err != nil || fired.ID != id || !fired.Fired {
		t.Fatalf("now ack is not {id, fired:true} (err=%v):\n%s", err, nowOut)
	}
	// The force-fire is a real run attributed to the schedule and carrying
	// its preset inputs — not just a 200.
	var runs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_runs WHERE triggered_via = 'schedule' AND triggered_by_id = ?`, id).Scan(&runs); err != nil {
		t.Fatalf("count schedule runs: %v", err)
	}
	if runs != 1 {
		t.Fatalf("expected exactly one run triggered by schedule %s, found %d", id, runs)
	}
	humanNow, _, err := runRoutineSchedulesCLI(t, ownerCfg, "routine", "schedules", "now", id)
	if err != nil || !strings.Contains(humanNow, "Schedule "+id+" fired (out-of-cycle).") {
		t.Fatalf("expected the human force-fire receipt, got err=%v:\n%s", err, humanNow)
	}

	// An id that is not a schedule in this workspace is a non-zero exit with
	// a sentence, not a silent success.
	if out, stderr, err := runRoutineSchedulesCLI(t, ownerCfg, "routine", "schedules", "update", "psched_does_not_exist", "--cron", "0 1 * * *"); err == nil ||
		!strings.Contains(out+stderr, "404") {
		t.Fatalf("expected a 404 for an unknown schedule on update, got err=%v:\n%s%s", err, out, stderr)
	}
	// `now` used to turn EVERY 404 into "force-fire endpoint unavailable on
	// this server", so a typo'd id told the operator to upgrade the server.
	// The handler's own 404 must reach them as what it is.
	out, stderr, err := runRoutineSchedulesCLI(t, ownerCfg, "routine", "schedules", "now", "psched_does_not_exist")
	if err == nil || !strings.Contains(out+stderr, "schedule not found") || strings.Contains(out+stderr, "endpoint unavailable") {
		t.Fatalf("expected `now` on an unknown id to report the server's \"schedule not found\", got err=%v:\n%s%s", err, out, stderr)
	}
}

// TestAcceptanceRoutineSchedules_NowOnServerWithoutRoute keeps the other
// half of the 404 split: a server that predates the /run route answers
// with the mux's plain-text 404, and only then is "endpoint unavailable"
// the honest message.
func TestAcceptanceRoutineSchedules_NowOnServerWithoutRoute(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	cfgPath := filepath.Join(t.TempDir(), "old-server-cli-config.yaml")
	cfg := "server: " + srv.URL + "\nworkspace: " + routineSchedulesAcceptanceWorkspaceID +
		"\ntoken: " + routineSchedulesOwnerToken + "\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	out, stderr, err := runRoutineSchedulesCLI(t, cfgPath, "routine", "schedules", "now", "psched_any")
	if err == nil || !strings.Contains(out+stderr, "force-fire endpoint unavailable") {
		t.Fatalf("expected the unavailable-endpoint hint for a route-less 404, got err=%v:\n%s%s", err, out, stderr)
	}
}

// TestAcceptanceRoutineSchedules_MemberRefused: a MEMBER without the
// routine.create capability can read the list but every mutation exits
// non-zero with the server's 403 and changes nothing.
func TestAcceptanceRoutineSchedules_MemberRefused(t *testing.T) {
	ownerCfg, memberCfg, db := startRoutineSchedulesAcceptanceServer(t)
	saveRoutineSchedulesTarget(t, ownerCfg)
	createOut, createErr, err := runRoutineSchedulesCLI(t, ownerCfg, "-f", "json", "routine", "schedules", "create",
		"--slug", "acceptance-sched", "--name", "nightly", "--cron", "0 9 * * *")
	if err != nil {
		t.Fatalf("owner schedules create failed: %v\n%s%s", err, createOut, createErr)
	}
	var created routineSchedulesJSONRow
	if err := json.Unmarshal([]byte(createOut), &created); err != nil || created.ID == "" {
		t.Fatalf("owner create JSON unusable (err=%v):\n%s", err, createOut)
	}
	id := created.ID

	// Reading is a member right.
	if rows := listRoutineSchedulesJSON(t, memberCfg); len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("member should see the workspace schedule, got %+v", rows)
	}

	cases := []struct {
		name string
		args []string
	}{
		{"create", []string{"routine", "schedules", "create", "--slug", "acceptance-sched", "--cron", "0 10 * * *"}},
		{"update", []string{"routine", "schedules", "update", id, "--cron", "0 10 * * *"}},
		{"disable", []string{"routine", "schedules", "disable", id}},
		{"now", []string{"routine", "schedules", "now", id}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, err := runRoutineSchedulesCLI(t, memberCfg, tc.args...)
			if err == nil {
				t.Fatalf("member %s must exit non-zero, got:\n%s%s", tc.name, out, stderr)
			}
			combined := out + stderr
			if !strings.Contains(combined, "403") {
				t.Fatalf("member %s should surface the server's 403, got:\n%s", tc.name, combined)
			}
		})
	}

	var count, runs int
	var cronExpr string
	var enabled bool
	if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_schedules WHERE deleted_at IS NULL`).Scan(&count); err != nil {
		t.Fatalf("count schedules: %v", err)
	}
	if err := db.QueryRow(`SELECT cron_expr, enabled FROM pipeline_schedules WHERE id = ?`, id).Scan(&cronExpr, &enabled); err != nil {
		t.Fatalf("read schedule: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_runs WHERE triggered_by_id = ?`, id).Scan(&runs); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if count != 1 || cronExpr != "0 9 * * *" || !enabled || runs != 0 {
		t.Fatalf("a refused member call changed state: schedules=%d cron=%q enabled=%v runs=%d", count, cronExpr, enabled, runs)
	}
}

// TestAcceptanceRoutineSchedules_InvalidInput: a bad cron is the server's
// readable 400, and the CLI-side guards fire before any request.
func TestAcceptanceRoutineSchedules_InvalidInput(t *testing.T) {
	ownerCfg, _, db := startRoutineSchedulesAcceptanceServer(t)
	saveRoutineSchedulesTarget(t, ownerCfg)

	out, stderr, err := runRoutineSchedulesCLI(t, ownerCfg, "routine", "schedules", "create",
		"--slug", "acceptance-sched", "--cron", "not a cron")
	combined := out + stderr
	if err == nil {
		t.Fatalf("an invalid cron must exit non-zero, got:\n%s", combined)
	}
	if !strings.Contains(combined, "400") || !strings.Contains(strings.ToLower(combined), "cron") {
		t.Fatalf("expected a 400 that names the cron expression, got:\n%s", combined)
	}
	if strings.Contains(combined, "panic") || strings.Contains(combined, "goroutine") {
		t.Fatalf("invalid cron produced a crash, not an error:\n%s", combined)
	}

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"missing slug", []string{"routine", "schedules", "create", "--cron", "0 9 * * *"}, "--slug is required"},
		{"missing cron", []string{"routine", "schedules", "create", "--slug", "acceptance-sched"}, "--cron or --when is required"},
		{"cron and when", []string{"routine", "schedules", "create", "--slug", "acceptance-sched", "--cron", "0 9 * * *", "--when", "every day at 9am"}, "mutually exclusive"},
		{"bad inputs", []string{"routine", "schedules", "create", "--slug", "acceptance-sched", "--cron", "0 9 * * *", "--inputs", "{nope"}, "--inputs must be valid JSON"},
		{"unknown routine", []string{"routine", "schedules", "create", "--slug", "no-such-routine", "--cron", "0 9 * * *"}, "400"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, err := runRoutineSchedulesCLI(t, ownerCfg, tc.args...)
			if err == nil || !strings.Contains(out+stderr, tc.want) {
				t.Fatalf("expected a non-zero exit mentioning %q, got err=%v:\n%s%s", tc.want, err, out, stderr)
			}
		})
	}

	// An invalid cron on update is refused the same way and leaves the row.
	createOut, _, err := runRoutineSchedulesCLI(t, ownerCfg, "-f", "json", "routine", "schedules", "create",
		"--slug", "acceptance-sched", "--cron", "0 9 * * *")
	if err != nil {
		t.Fatalf("schedules create failed: %v\n%s", err, createOut)
	}
	var created routineSchedulesJSONRow
	if err := json.Unmarshal([]byte(createOut), &created); err != nil {
		t.Fatalf("create JSON: %v\n%s", err, createOut)
	}
	out, stderr, err = runRoutineSchedulesCLI(t, ownerCfg, "routine", "schedules", "update", created.ID, "--cron", "61 25 * * *")
	if err == nil || !strings.Contains(out+stderr, "400") {
		t.Fatalf("expected an invalid cron on update to be a 400, got err=%v:\n%s%s", err, out, stderr)
	}
	var cronExpr string
	if err := db.QueryRow(`SELECT cron_expr FROM pipeline_schedules WHERE id = ?`, created.ID).Scan(&cronExpr); err != nil || cronExpr != "0 9 * * *" {
		t.Fatalf("a refused update changed the cron: %q err=%v", cronExpr, err)
	}
}
