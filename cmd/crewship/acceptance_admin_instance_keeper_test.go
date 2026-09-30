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

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// A real CLI process against a real router: Admin › Security across
// workspaces. The instance admin sits in "people" and sets the Keeper of "lab",
// which they do not belong to, then of every workspace. Nothing is stubbed.
func TestAcceptance_AdminInstanceKeeper(t *testing.T) {
	t.Setenv(backup.InstanceOwnerEmailEnv, "")
	db := testutil.MigratedDB(t).DB
	token := "crewship_cli_instkeeper00000000000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug,created_at) VALUES('ik-people','People','people','2026-01-01 00:00:00'),('ik-lab','Lab','lab','2026-06-01 00:00:00')`,
		`INSERT INTO users(id,email,full_name) VALUES('ik-boss','boss@people.invalid','Boss'),('ik-carol','carol@lab.invalid','Carol')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('ik-m1','ik-people','ik-boss','OWNER'),('ik-m2','ik-lab','ik-carol','OWNER')`,
		`INSERT INTO agents(id,workspace_id,name,slug) VALUES('ik-a1','ik-lab','Lab agent','lab-agent')`,
		`INSERT INTO keeper_requests(id,requesting_agent_id,intent,decision,request_type) VALUES('ik-r1','ik-a1','read db','DENY','access')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('ik-token','ik-boss','test',?,datetime('now'))`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
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
	run := func(stdin string, args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1", "DATABASE_URL=")
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := run("", args...)
		if err != nil {
			t.Fatalf("CLI %v: %v\n%s", args, err, out)
		}
		return out
	}
	enabled := func(ws string) bool {
		t.Helper()
		var on int
		_ = db.QueryRow(`SELECT enabled FROM keeper_governance_settings WHERE workspace_id = ?`, ws).Scan(&on)
		return on == 1
	}

	// One workspace the admin is not a member of: saved straight away.
	must("admin", "instance", "keeper", "set", "--workspace", "lab", "--watchdog", "on", "--sample-every", "10")
	if !enabled("ik-lab") || enabled("ik-people") {
		t.Fatal("a one-workspace save missed lab or touched people")
	}

	// The matrix names both, lab with its own settings.
	var list struct {
		Workspaces []struct {
			WorkspaceSlug       string `json:"workspace_slug"`
			Configured          bool   `json:"configured"`
			BehaviorSampleEvery int    `json:"behavior_sample_every"`
		} `json:"workspaces"`
	}
	raw := must("admin", "instance", "keeper", "governance", "--format", "json")
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatalf("governance: %v\n%s", err, raw)
	}
	bySlug := map[string]int{}
	for _, w := range list.Workspaces {
		if w.Configured {
			bySlug[w.WorkspaceSlug] = w.BehaviorSampleEvery
		}
	}
	if bySlug["lab"] != 10 || len(list.Workspaces) < 2 {
		t.Fatalf("governance = %s", raw)
	}

	// --dry-run shows and writes nothing.
	if out := must("admin", "instance", "keeper", "set", "--all", "--watchdog", "off", "--dry-run"); !strings.Contains(out, "would change") || !strings.Contains(out, "lab") {
		t.Fatalf("dry run:\n%s", out)
	}
	if !enabled("ik-lab") {
		t.Fatal("a dry run switched lab off")
	}

	// Several workspaces ask first; "n" leaves everything as it was.
	if out, err := run("n\n", "admin", "instance", "keeper", "set", "--all", "--watchdog", "off"); err == nil || !strings.Contains(out, "will change") {
		t.Fatalf("declined bulk save: %v\n%s", err, out)
	}
	if !enabled("ik-lab") {
		t.Fatal("a declined bulk save still wrote")
	}
	// --yes writes every existing workspace — and not the defaults.
	if out := must("admin", "instance", "keeper", "set", "--all", "--watchdog", "on", "--deny-alert-risk", "5", "--yes"); !strings.Contains(out, "Saved in") {
		t.Fatalf("bulk save:\n%s", out)
	}
	if !enabled("ik-people") || !enabled("ik-lab") {
		t.Fatal("--all missed a workspace")
	}
	var defaultsSaved int
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_settings WHERE key = 'keeper.governance_defaults'`).Scan(&defaultsSaved)
	if defaultsSaved != 0 {
		t.Fatal("set --all changed the defaults for new workspaces")
	}

	// The defaults are their own operation, and a workspace created after
	// them takes a copy.
	if out := must("admin", "instance", "keeper", "defaults", "--watchdog", "on", "--sample-every", "20", "--yes"); !strings.Contains(out, "existing ones are unchanged") {
		t.Fatalf("defaults:\n%s", out)
	}
	if out := must("admin", "instance", "keeper", "defaults"); !strings.Contains(out, "1 in 20") {
		t.Fatalf("show defaults:\n%s", out)
	}
	must("admin", "instance", "create-workspace", "Later", "--slug", "later", "--owner", "carol@lab.invalid")
	var laterSample int
	_ = db.QueryRow(`SELECT g.behavior_sample_every FROM keeper_governance_settings g JOIN workspaces w ON w.id = g.workspace_id WHERE w.slug = 'later'`).Scan(&laterSample)
	if laterSample != 20 {
		t.Fatalf("new workspace sample = %d, want its copy of the defaults (20)", laterSample)
	}
	if out, err := run("", "admin", "instance", "keeper", "defaults", "--contact", "ik-carol", "--yes"); err == nil || !strings.Contains(out, "belong to one workspace") {
		t.Fatalf("contact as a default: %v\n%s", err, out)
	}

	// A person belongs to one workspace; the CLI passes the server's refusal on.
	if out, err := run("", "admin", "instance", "keeper", "set", "--workspace", "lab,people", "--contact", "ik-carol", "--yes"); err == nil || !strings.Contains(out, "per workspace") {
		t.Fatalf("contact across workspaces: %v\n%s", err, out)
	}

	if out := must("admin", "instance", "keeper", "requests", "--workspace", "lab"); !strings.Contains(out, "Lab agent") || !strings.Contains(out, "DENY") {
		t.Fatalf("requests:\n%s", out)
	}
	if out := must("admin", "instance", "keeper", "health"); !strings.Contains(out, "lab") || !strings.Contains(out, "people") {
		t.Fatalf("health:\n%s", out)
	}

	// An instance admin need not have a workspace selected — or belong to
	// one — to run instance commands.
	noWS := filepath.Join(t.TempDir(), "cli-nows.yaml")
	if err := os.WriteFile(noWS, []byte("server: "+srv.URL+"\ntoken: "+token+"\nformat: table\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"admin", "instance", "keeper", "governance"},
		{"admin", "instance", "audit", "--limit", "3"},
		{"admin", "instance", "keeper", "defaults"},
	} {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+noWS, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1", "DATABASE_URL=")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v with no workspace selected: %v\n%s", args, err, out)
		}
	}

	var audited int
	_ = db.QueryRow(`SELECT COUNT(*) FROM instance_audit_logs WHERE action = 'instance.keeper_governance_updated' AND target_workspace_id = 'ik-lab'`).Scan(&audited)
	if audited < 2 {
		t.Fatalf("lab audit entries = %d, want one per save that changed it", audited)
	}
}
