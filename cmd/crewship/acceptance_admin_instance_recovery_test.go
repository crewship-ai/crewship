//go:build !clionly

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
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/quiesce"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// A real CLI process against a real router, then the same binary offline:
// whole-instance backup and recovery end to end. The instance admin turns the
// recovery kit on, reads the vault key versions, runs an instance backup and
// waits for it, checks its contents (proof 2), runs the restore checks, runs
// a drill offline and records it on the server (proof 3), recovers the
// bundle into an empty data directory, and lists and resumes holds. Nothing
// is stubbed.
func TestAcceptance_AdminInstanceRecovery(t *testing.T) {
	t.Setenv(backup.InstanceOwnerEmailEnv, "")
	t.Setenv("CREWSHIP_BACKUP_MIN_FREE_PERCENT", "0") // the space floor has its own tests
	t.Setenv(encryption.KeyEnvVar("v1"), strings.Repeat("f6", 32))
	t.Setenv(encryption.KeyVersionEnvVar, "")
	db := testutil.MigratedDB(t).DB
	token := "crewship_cli_instrecovery00000000000"
	sealed, err := encryption.Encrypt("the deploy token")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug,created_at) VALUES('ir-people','People','people','2026-01-01 00:00:00'),('ir-lab','Lab','lab','2026-06-01 00:00:00')`,
		`INSERT INTO users(id,email,full_name) VALUES('ir-boss','boss@people.invalid','Boss'),('ir-carol','carol@lab.invalid','Carol')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('ir-m1','ir-people','ir-boss','OWNER'),('ir-m2','ir-lab','ir-carol','OWNER')`,
		`INSERT INTO credentials(id,workspace_id,name,type,status,encrypted_value,created_by) VALUES('ir-c1','ir-lab','deploy','SECRET','ACTIVE','` + sealed + `','ir-carol')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('ir-token','ir-boss','test',?,datetime('now'))`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	outDir, storage := t.TempDir(), t.TempDir()
	router.SetInstanceRecovery(api.InstanceRecoveryConfig{
		OutputDir: outDir, DataDir: storage, Quiesce: quiesce.New(),
		Paths: backup.InstancePaths{Output: storage},
	})
	srv := httptest.NewServer(router)
	defer srv.Close()

	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "cli.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: people\ntoken: "+token+"\nformat: table\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(tmp, "ops.key")
	if err := os.WriteFile(keyFile, []byte(id.String()+"\n"), 0o600); err != nil {
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

	// Recovery kit on, and the vault keys view says so.
	if out := must("admin", "instance", "backups", "recovery-kit", "on"); !strings.Contains(out, "Recovery kit on") {
		t.Fatalf("recovery-kit on:\n%s", out)
	}
	if out := must("admin", "instance", "backups", "vault-keys"); !strings.Contains(out, "ENCRYPTION_KEY") || !strings.Contains(out, "Recovery kit: on") {
		t.Fatalf("vault-keys:\n%s", out)
	}

	// An instance backup, waited for. It is a backup_runs row like every
	// other run: --wait prints the runs as 'backups runs' lists them.
	var runs []struct {
		ID         string `json:"id"`
		Scope      string `json:"scope"`
		Status     string `json:"status"`
		BundlePath string `json:"bundle_path"`
		HoldMs     *int64 `json:"hold_ms"`
	}
	raw := must("admin", "instance", "backups", "run", "--scope", "instance", "--recipient", id.Recipient().String(), "--wait", "--format", "json")
	if err := json.Unmarshal([]byte(raw), &runs); err != nil {
		t.Fatalf("run json: %v\n%s", err, raw)
	}
	if len(runs) != 1 || runs[0].Status != "done" || runs[0].Scope != "instance" || runs[0].BundlePath == "" || runs[0].HoldMs == nil {
		t.Fatalf("run = %s", raw)
	}
	bundle := runs[0].BundlePath
	if out := must("admin", "instance", "backups", "run-status", runs[0].ID); !strings.Contains(out, "done") || !strings.Contains(out, bundle) {
		t.Fatalf("run-status:\n%s", out)
	}
	if out := must("admin", "instance", "backups", "runs", "--scope", "instance"); !strings.Contains(out, "manual") || !strings.Contains(out, "done") {
		t.Fatalf("runs:\n%s", out)
	}
	// A passphrase run too; the passphrase comes from a file and is never
	// stored.
	passFile := filepath.Join(tmp, "pass.txt")
	if err := os.WriteFile(passFile, []byte("correct horse battery staple\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := must("admin", "instance", "backups", "run", "--scope", "instance", "--passphrase-file", passFile, "--wait"); !strings.Contains(out, ": done") {
		t.Fatalf("passphrase run:\n%s", out)
	}
	if out, err := run("admin", "instance", "backups", "run", "--scope", "instance", "--passphrase-file", passFile, "--recipient", id.Recipient().String()); err == nil {
		t.Fatalf("both keys were accepted:\n%s", out)
	}
	if out, err := run("admin", "instance", "backups", "run", "--scope", "instance"); err == nil {
		t.Fatalf("an unencrypted run was accepted:\n%s", out)
	}

	// Proof 2, then the checks before a restore.
	if out := must("admin", "instance", "backups", "check", bundle, "--identity", keyFile); !strings.Contains(out, "contents checked") {
		t.Fatalf("check:\n%s", out)
	}
	if out := must("admin", "instance", "backups", "restore-checks", bundle, "--target", "empty_server", "--identity", keyFile); !strings.Contains(out, "crewship recover") {
		t.Fatalf("restore-checks:\n%s", out)
	}

	// A drill offline, recorded on the server.
	if out := must("backup", "drill", "--bundle", bundle, "--identity", keyFile, "--post"); !strings.Contains(out, "Result: ok") || !strings.Contains(out, "proof level 3") {
		t.Fatalf("drill:\n%s", out)
	}
	if out, err := run("backup", "drill", "--bundle", bundle, "--identity", keyFile, "--post=http://elsewhere.invalid"); err == nil || !strings.Contains(out, "never sent to another server") {
		t.Fatalf("--post to another server was not refused:\n%s", out)
	}
	if out := must("admin", "instance", "backups", "drills"); !strings.Contains(out, "ok") || !strings.Contains(out, "instance") {
		t.Fatalf("drills:\n%s", out)
	}
	if e, err := backup.GetCatalogEntry(t.Context(), db, bundle); err != nil || e.ProofLevel != 3 || e.DrillResult != "ok" {
		t.Fatalf("catalog after drill = %+v, %v", e, err)
	}

	// Recover, offline, into an empty directory — and not into a full one.
	dataDir := filepath.Join(tmp, "recovered")
	out := must("recover", "--bundle", bundle, "--identity", keyFile, "--data-dir", dataDir)
	if !strings.Contains(out, "Result: ok") || !strings.Contains(out, "recovered-keys.env") || !strings.Contains(out, "Held: routines") {
		t.Fatalf("recover:\n%s", out)
	}
	env, err := backup.ReadEnvFile(filepath.Join(dataDir, backup.RecoveredKeysFile))
	if err != nil || env[encryption.KeyEnvVar("v1")] != strings.Repeat("f6", 32) {
		t.Fatalf("recovered keys: %v (%v)", err, len(env))
	}
	if out, err := run("recover", "--bundle", bundle, "--identity", keyFile, "--data-dir", dataDir); err == nil || !strings.Contains(out, "not empty") {
		t.Fatalf("recover into a non-empty dir:\n%s", out)
	}

	// Complete environments a recover staged are landed by the running
	// server. Nothing staged yet: it says so.
	if out := must("admin", "instance", "backups", "environments", "land", "--dry-run"); !strings.Contains(out, "No staged environments") {
		t.Fatalf("land with nothing staged:\n%s", out)
	}
	stageDir := filepath.Join(storage, backup.RecoveredEnvironmentsDir)
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	rec, _ := json.Marshal(backup.Environment{Format: backup.EnvironmentFormat, ID: "e1", Crew: "ops", Managed: true,
		ImageRef: "crewship-env/ops:e1", Unsafe: []backup.UnsafeSetting{{Kind: backup.UnsafeDockerSocket}}})
	af, err := os.Create(filepath.Join(stageDir, "ir-lab.tar.zst"))
	if err != nil {
		t.Fatal(err)
	}
	atw, err := backup.NewTarZstWriter(af)
	if err != nil {
		t.Fatal(err)
	}
	if err := atw.WriteFile("environments/ops.json", 0o600, time.Now(), rec); err != nil {
		t.Fatal(err)
	}
	_ = atw.Close()
	_ = af.Close()
	// This server has no Docker: the environment is skipped with the
	// reason, and what stays off is named.
	if out := must("admin", "instance", "backups", "environments", "land"); !strings.Contains(out, "ir-lab") || !strings.Contains(out, "skipped") ||
		!strings.Contains(out, "Docker socket mount on ops") {
		t.Fatalf("land:\n%s", out)
	}

	// Holds on the running server: list and resume.
	if err := quiesce.SetHolds(t.Context(), db, []quiesce.Hold{{Key: quiesce.HoldRoutines, Detail: "3 schedule(s) will not fire"}}); err != nil {
		t.Fatal(err)
	}
	if out := must("admin", "instance", "holds", "list"); !strings.Contains(out, "routines") {
		t.Fatalf("holds list:\n%s", out)
	}
	if out := must("admin", "instance", "holds", "resume", "routines"); !strings.Contains(out, "Resumed routines") {
		t.Fatalf("holds resume:\n%s", out)
	}
	if out := must("admin", "instance", "holds", "list"); !strings.Contains(out, "Nothing is held") {
		t.Fatalf("holds after resume:\n%s", out)
	}
}
