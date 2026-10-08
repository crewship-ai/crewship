package main

import (
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// A real CLI process against a real router: backup plans, a manual run that
// the server finishes after the CLI has returned, the run history and the
// overview. The instance admin sits in "people" and plans for "lab", which
// they do not belong to. Nothing is stubbed; the bundle is really written.
func TestAcceptance_AdminInstanceBackupPlans(t *testing.T) {
	t.Setenv(backup.InstanceOwnerEmailEnv, "")
	t.Setenv("CREWSHIP_DATA_DIR", t.TempDir())
	t.Setenv("CREWSHIP_BACKUP_MIN_FREE_PERCENT", "0") // the space floor has its own tests
	db := testutil.MigratedDB(t).DB
	token := "crewship_cli_instbackplans0000000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug,created_at) VALUES('bp-people','People','people','2026-01-01 00:00:00'),('bp-lab','Lab','lab','2026-06-01 00:00:00')`,
		`INSERT INTO users(id,email,full_name) VALUES('bp-boss','boss@people.invalid','Boss'),('bp-carol','carol@lab.invalid','Carol')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('bp-m1','bp-people','bp-boss','OWNER'),('bp-m2','bp-lab','bp-carol','OWNER')`,
		// Lab holds a crew: the overview lists only workspaces with crews.
		`INSERT INTO crews(id,workspace_id,slug,name) VALUES('bp-crew','bp-lab','research','Research')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('bp-token','bp-boss','test',?,datetime('now'))`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	key := id.Recipient().String()

	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	defer srv.Close()
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: people\ntoken: "+token+"\nformat: table\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := buildCrewshipBinary(t)
	run := func(args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1", "DATABASE_URL=")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := run(args...)
		if err != nil {
			t.Fatalf("CLI %v: %v\n%s", args, err, out)
		}
		return out
	}
	bp := []string{"admin", "instance", "backups"}
	cli := func(args ...string) []string { return append(append([]string{}, bp...), args...) }

	// Every plan is encrypted.
	if out, err := run(cli("plans", "create", "--preset", "workspace", "--workspace", "lab")...); err == nil || !strings.Contains(out, "recipient") {
		t.Fatalf("a plan without recipients was accepted:\n%s", out)
	}

	raw := must(cli("plans", "create", "--preset", "workspace", "--name", "Nightly", "--workspace", "lab",
		"--recipient", key, "--timezone", "Europe/Prague", "--format", "json")...)
	var plan struct {
		ID           string   `json:"id"`
		Name         string   `json:"name"`
		WorkspaceIDs []string `json:"workspace_ids"`
		Timezone     string   `json:"timezone"`
		Cadence      string   `json:"cadence"`
		NextRunAt    *string  `json:"next_run_at"`
	}
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		t.Fatalf("create json: %v\n%s", err, raw)
	}
	if plan.ID == "" || len(plan.WorkspaceIDs) != 1 || plan.WorkspaceIDs[0] != "bp-lab" || plan.Timezone != "Europe/Prague" || plan.NextRunAt == nil {
		t.Fatalf("created plan = %s", raw)
	}
	if out := must(cli("plans", "list")...); !strings.Contains(out, "Nightly") || !strings.Contains(out, "daily 03:00") {
		t.Fatalf("plans list:\n%s", out)
	}
	if out := must(cli("plans", "get", plan.ID)...); !strings.Contains(out, "bp-lab") || !strings.Contains(out, "keep      3 checked") {
		t.Fatalf("plans get:\n%s", out)
	}
	raw = must(cli("plans", "update", plan.ID, "--cadence", "weekly", "--weekday", "0", "--env-mode", "complete", "--format", "json")...)
	if err := json.Unmarshal([]byte(raw), &plan); err != nil || plan.Cadence != "weekly" || plan.Name != "Nightly" {
		t.Fatalf("update json (%v): %s", err, raw)
	}
	if out := must(cli("plans", "next", plan.ID, "--n", "2")...); strings.Count(out, "+ environments") != 2 {
		t.Fatalf("plans next (weekly with environments):\n%s", out)
	}
	raw = must(cli("plans", "calendar", plan.ID, "--from", "2026-10-01", "--to", "2026-10-31", "--format", "json")...)
	var cal struct {
		Days []struct {
			Date string `json:"date"`
		} `json:"days"`
	}
	if err := json.Unmarshal([]byte(raw), &cal); err != nil || len(cal.Days) != 31 {
		t.Fatalf("calendar json (%v): %s", err, raw)
	}
	if out := must(cli("plans", "preview", "--contents", "memory")...); !strings.Contains(out, "required  agents (needed by memory)") {
		t.Fatalf("preview:\n%s", out)
	}

	// A manual run: the CLI returns at once, the server finishes it.
	out := must(cli("run", "--plan", plan.ID, "--note", "before upgrade")...)
	if !strings.Contains(out, "Started br_") {
		t.Fatalf("run:\n%s", out)
	}
	router.BackupPlans().Wait()
	if out := must(cli("runs", "--ws", "lab")...); !strings.Contains(out, "Nightly") || !strings.Contains(out, "manual") || !strings.Contains(out, "done") || !strings.Contains(out, "check:done") {
		t.Fatalf("runs:\n%s", out)
	}
	raw = must(cli("runs", "--plan", plan.ID, "--format", "json")...)
	var runs struct {
		Data []struct {
			Status     string  `json:"status"`
			BundlePath *string `json:"bundle_path"`
			Note       *string `json:"note"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &runs); err != nil || len(runs.Data) != 1 || runs.Data[0].BundlePath == nil || runs.Data[0].Note == nil || *runs.Data[0].Note != "before upgrade" {
		t.Fatalf("runs json (%v): %s", err, raw)
	}
	if _, err := os.Stat(*runs.Data[0].BundlePath); err != nil {
		t.Fatalf("bundle not written: %v", err)
	}
	if out := must(cli("overview", "--scope", "workspaces", "--ws", "lab")...); !strings.Contains(out, "Nightly: checksum_only") || !strings.Contains(out, "Lab") {
		t.Fatalf("overview:\n%s", out)
	}
	if out := must(cli("overview")...); !strings.Contains(out, "Complete recovery: none") {
		t.Fatalf("instance overview:\n%s", out)
	}

	// The same run command with --wait: an ad-hoc workspace run, followed
	// to the end through GET …/run/{id}, then read back with run-status.
	raw = must(cli("run", "--scope", "workspaces", "--workspace", "lab", "--recipient", key, "--wait", "--format", "json")...)
	var waited []struct {
		ID            string  `json:"id"`
		Status        string  `json:"status"`
		WorkspaceName *string `json:"workspace_name"`
		BundlePath    *string `json:"bundle_path"`
	}
	if err := json.Unmarshal([]byte(raw), &waited); err != nil || len(waited) != 1 || waited[0].Status != "done" || waited[0].BundlePath == nil ||
		waited[0].WorkspaceName == nil || *waited[0].WorkspaceName != "Lab" {
		t.Fatalf("run --wait json (%v): %s", err, raw)
	}
	if out := must(cli("run-status", waited[0].ID)...); !strings.Contains(out, "Lab") || !strings.Contains(out, *waited[0].BundlePath) {
		t.Fatalf("run-status:\n%s", out)
	}
	if out, err := run(cli("run-status", "br_nope")...); err == nil || !strings.Contains(out, "no such run") {
		t.Fatalf("run-status of an unknown run:\n%s", out)
	}

	if out := must(cli("plans", "delete", plan.ID)...); !strings.Contains(out, "Deleted") {
		t.Fatalf("delete:\n%s", out)
	}
	var audits int
	_ = db.QueryRow(`SELECT COUNT(*) FROM instance_audit_logs WHERE action LIKE 'instance.backup_plan_%' OR action = 'instance.backup_run_started'`).Scan(&audits)
	if audits != 5 {
		t.Fatalf("audit entries = %d, want 5 (created, updated, two runs, deleted)", audits)
	}
}
