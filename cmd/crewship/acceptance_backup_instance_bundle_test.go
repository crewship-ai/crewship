//go:build !clionly

package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/quiesce"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// A real CLI process against a real router: `backup inspect`, `backup
// verify` and `backup download` work on an INSTANCE bundle. Those commands
// ask the workspace route first, which answers 404 for a bundle that belongs
// to no workspace; they then ask the instance route, which resolves any
// catalogued bundle. A workspace OWNER who is not an instance admin gets
// nothing. Nothing is stubbed.
func TestAcceptance_BackupCommandsReadAnInstanceBundle(t *testing.T) {
	t.Setenv(backup.InstanceOwnerEmailEnv, "")
	t.Setenv("CREWSHIP_BACKUP_MIN_FREE_PERCENT", "0")
	db := testutil.MigratedDB(t).DB
	boss, dan := "crewship_cli_instbundleboss0000000000", "crewship_cli_instbundledan00000000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug,created_at) VALUES('ib-people','People','people','2026-01-01 00:00:00')`,
		`INSERT INTO users(id,email,full_name,instance_role) VALUES('ib-boss','boss@people.invalid','Boss','ADMIN')`,
		`INSERT INTO users(id,email,full_name) VALUES('ib-dan','dan@people.invalid','Dan')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('ib-m1','ib-people','ib-boss','OWNER'),('ib-m2','ib-people','ib-dan','OWNER')`,
		`INSERT INTO app_settings(key,value,updated_at) VALUES('instance.admin_bootstrapped','already named',datetime('now'))`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	for id, tok := range map[string]string{"ib-boss": boss, "ib-dan": dan} {
		if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES(?,?,'test',?,datetime('now'))`, "tok-"+id, id, sha256HexToken(tok)); err != nil {
			t.Fatal(err)
		}
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
	cfgFor := func(tok string) string {
		p := filepath.Join(t.TempDir(), "cli.yaml")
		if err := os.WriteFile(p, []byte("server: "+srv.URL+"\nworkspace: people\ntoken: "+tok+"\nformat: table\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	bossCfg, danCfg := cfgFor(boss), cfgFor(dan)
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

	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	var runs []struct {
		Status     string `json:"status"`
		BundlePath string `json:"bundle_path"`
	}
	raw := must("admin", "instance", "backups", "run", "--scope", "instance", "--recipient", id.Recipient().String(), "--wait", "--format", "json")
	if err := json.Unmarshal([]byte(raw), &runs); err != nil || len(runs) != 1 || runs[0].Status != "done" || runs[0].BundlePath == "" {
		t.Fatalf("instance run: %v\n%s", err, raw)
	}
	bundle := runs[0].BundlePath

	// The workspace route alone does not find it — the reason for the
	// fallback.
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/admin/backups/inspect?workspace_id=people&path="+url.QueryEscape(bundle), nil)
	req.Header.Set("Authorization", "Bearer "+boss)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("workspace inspect of an instance bundle = %d; the fallback test assumes 400 or 404", resp.StatusCode)
	}

	var manifest struct {
		Scope      string `json:"scope"`
		Encryption struct {
			Enabled bool `json:"enabled"`
		} `json:"encryption"`
	}
	out := must("backup", "inspect", bundle, "--format", "json")
	if err := json.Unmarshal([]byte(out), &manifest); err != nil || manifest.Scope != "instance" || !manifest.Encryption.Enabled {
		t.Fatalf("inspect: %v\n%s", err, out)
	}
	if out := must("backup", "inspect", bundle, "--format", "table"); !strings.Contains(out, "instance") {
		t.Fatalf("inspect -f table:\n%s", out)
	}
	if out := must("backup", "verify", bundle); !strings.Contains(out, "VALID") {
		t.Fatalf("verify:\n%s", out)
	}
	dest := filepath.Join(tmp, "instance.tar.zst")
	must("backup", "download", bundle, "--out", dest)
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || string(got) != string(want) {
		t.Fatalf("download: %d bytes, the bundle has %d", len(got), len(want))
	}
	// The client can receive all Content-Length bytes before the handler's
	// post-copy audit transaction commits. Wait for that server-side work.
	deadline := time.Now().Add(20 * time.Second)
	for {
		var audited int
		if err := db.QueryRow(`SELECT COUNT(*) FROM instance_audit_logs WHERE action = 'instance.backup_downloaded'`).Scan(&audited); err != nil {
			t.Fatalf("read instance download audit: %v", err)
		}
		if audited == 1 {
			break
		}
		if audited > 1 || time.Now().After(deadline) {
			t.Fatalf("instance audit entries for the download = %d, want 1", audited)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Not in the catalog: still a 404 on both routes.
	if out, err := run(bossCfg, "backup", "inspect", filepath.Join(outDir, "nope.tar.zst")); err == nil {
		t.Fatalf("inspect of an uncatalogued path succeeded:\n%s", out)
	}
	// A workspace OWNER who is not an instance admin reaches neither route.
	for _, args := range [][]string{
		{"backup", "inspect", bundle},
		{"backup", "verify", bundle},
		{"backup", "download", bundle, "--out", filepath.Join(tmp, "x")},
	} {
		if out, err := run(danCfg, args...); err == nil {
			t.Fatalf("non-instance-admin %v succeeded:\n%s", args, out)
		}
	}
}
