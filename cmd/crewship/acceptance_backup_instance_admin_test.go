package main

import (
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

// A real CLI process against a real router: the legacy backup commands
// follow the instance-admin rules. The instance admin belongs to "people"
// only and backs up, lists, verifies, downloads and dry-runs a restore of
// "lab" by naming it with --workspace. A workspace OWNER of "people" who is
// not an instance admin cannot reach "lab". Nothing is stubbed.
func TestAcceptance_BackupCommandsForAnInstanceAdmin(t *testing.T) {
	t.Setenv(backup.InstanceOwnerEmailEnv, "")
	t.Setenv("CREWSHIP_BACKUP_MIN_FREE_PERCENT", "0")
	// Bundles land under the data dir, never the developer's ~/.crewship.
	t.Setenv("CREWSHIP_DATA_DIR", t.TempDir())
	passFile := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(passFile, []byte("instance-admin-backup-pass-1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	db := testutil.MigratedDB(t).DB
	boss, other := "crewship_cli_bkinstadmin00000000000000", "crewship_cli_bkinstother00000000000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug,created_at) VALUES('bi-people','People','people','2026-01-01 00:00:00'),('bi-lab','Lab','lab','2026-06-01 00:00:00')`,
		`INSERT INTO users(id,email,full_name,instance_role) VALUES('bi-boss','boss@people.invalid','Boss','ADMIN')`,
		`INSERT INTO users(id,email,full_name) VALUES('bi-dan','dan@people.invalid','Dan'),('bi-carol','carol@lab.invalid','Carol')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('bi-m1','bi-people','bi-boss','OWNER'),('bi-m2','bi-lab','bi-carol','OWNER'),('bi-m3','bi-people','bi-dan','OWNER')`,
		`INSERT INTO app_settings(key,value,updated_at) VALUES('instance.admin_bootstrapped','already named',datetime('now'))`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	for id, tok := range map[string]string{"bi-boss": boss, "bi-dan": other} {
		if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES(?,?,'test',?,datetime('now'))`, "tok-"+id, id, sha256HexToken(tok)); err != nil {
			t.Fatal(err)
		}
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	defer srv.Close()
	cfgFor := func(tok string) string {
		p := filepath.Join(t.TempDir(), "cli.yaml")
		if err := os.WriteFile(p, []byte("server: "+srv.URL+"\nworkspace: people\ntoken: "+tok+"\nformat: table\n"), 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	bossCfg, otherCfg := cfgFor(boss), cfgFor(other)
	binary := buildCrewshipBinary(t)
	run := func(cfg string, args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1", "DATABASE_URL=")
		cmd.Stdin = strings.NewReader("")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := run(bossCfg, args...)
		if err != nil {
			t.Fatalf("CLI %v: %v\n%s", args, err, out)
		}
		return out
	}

	raw := must("backup", "create", "--scope", "workspace", "--passphrase-file", passFile, "--workspace", "lab")
	var created struct{ Path string }
	for _, line := range strings.Split(raw, "\n") {
		if p, ok := strings.CutPrefix(strings.TrimSpace(line), "Backup created: "); ok {
			created.Path = strings.TrimSpace(p)
		}
	}
	if !strings.Contains(created.Path, "-lab-") {
		t.Fatalf("create:\n%s", raw)
	}
	if out := must("backup", "list", "--workspace", "lab"); !strings.Contains(out, filepath.Base(created.Path)) {
		t.Fatalf("list lab:\n%s", out)
	}
	if out := must("backup", "list"); strings.Contains(out, filepath.Base(created.Path)) {
		t.Fatalf("people's list shows lab's bundle:\n%s", out)
	}
	must("backup", "verify", created.Path, "--workspace", "lab")
	dest := filepath.Join(t.TempDir(), "lab.tar.zst")
	must("backup", "download", created.Path, "--out", dest, "--workspace", "lab")
	if st, err := os.Stat(dest); err != nil || st.Size() == 0 {
		t.Fatalf("download wrote nothing: %v", err)
	}
	must("backup", "restore", created.Path, "--dry-run", "--passphrase-file", passFile, "--workspace", "lab")
	var member int
	_ = db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE workspace_id='bi-lab' AND user_id='bi-boss'`).Scan(&member)
	if member != 0 {
		t.Fatal("the instance admin became a member of lab")
	}

	// A workspace OWNER who is not an instance admin stays in their workspace.
	for _, args := range [][]string{
		{"backup", "list", "--workspace", "lab"},
		{"backup", "download", created.Path, "--out", filepath.Join(t.TempDir(), "x"), "--workspace", "lab"},
		{"backup", "restore", created.Path, "--dry-run", "--workspace", "lab"},
	} {
		if out, err := run(otherCfg, args...); err == nil {
			t.Fatalf("non-instance-admin %v succeeded:\n%s", args, out)
		}
	}
	if out, err := run(otherCfg, "backup", "list"); err != nil {
		t.Fatalf("OWNER lists their own workspace: %v\n%s", err, out)
	}
}
