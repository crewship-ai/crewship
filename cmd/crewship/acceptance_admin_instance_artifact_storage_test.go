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

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestParseStorageBytes(t *testing.T) {
	for _, c := range []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"1048576", 1 << 20, false}, {"512MiB", 512 << 20, false}, {"16GiB", 16 << 30, false}, {"2 tib", 2 << 40, false}, {"4g", 4 << 30, false},
		{"0", 0, true}, {"-1GiB", 0, true}, {"lots", 0, true}, {"", 0, true}, {"9999999999TiB", 0, true},
	} {
		got, err := parseStorageBytes("x", c.in)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("parseStorageBytes(%q) = %d, %v", c.in, got, err)
		}
	}
}

// A real CLI process against a real router: limits default to 32 GiB / 64 GiB,
// an instance administrator sets them, and recompute repairs injected drift
// (previewed first with --dry-run) idempotently without deleting any row.
func TestAcceptance_AdminInstanceArtifactStorage(t *testing.T) {
	t.Setenv(backup.InstanceOwnerEmailEnv, "")
	db := testutil.MigratedDB(t).DB
	admin, member := "crewship_cli_artstorageadmin000000000", "crewship_cli_artstoragemember00000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug,created_at) VALUES('as-a','Alpha','alpha','2026-01-01 00:00:00'),('as-b','Beta','beta','2026-06-01 00:00:00')`,
		`INSERT INTO users(id,email,full_name) VALUES('as-boss','boss@alpha.invalid','Boss'),('as-eve','eve@beta.invalid','Eve')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('as-m1','as-a','as-boss','OWNER'),('as-m2','as-b','as-eve','OWNER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	for id, tok := range map[string]string{"as-boss": admin, "as-eve": member} {
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
	binary := buildCrewshipBinary(t)
	run := func(token string, args ...string) (string, error) {
		cfg := filepath.Join(t.TempDir(), "cli.yaml")
		if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\ntoken: "+token+"\nformat: table\n"), 0600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1", "DATABASE_URL=")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	show := func() adminArtifactStorage {
		t.Helper()
		raw, err := run(admin, "admin", "instance", "artifact-storage", "get", "--format", "json")
		if err != nil {
			t.Fatalf("get: %v\n%s", err, raw)
		}
		var got adminArtifactStorage
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("json: %v\n%s", err, raw)
		}
		return got
	}

	// Defaults are unchanged.
	if got := show(); got.WorkspaceLimitBytes != 32<<30 || got.InstanceLimitBytes != 64<<30 || got.Retention != "keep-all" {
		t.Fatalf("defaults = %+v", got)
	}
	// Not an instance administrator: refused, nothing written.
	for _, args := range [][]string{{"artifact-storage"}, {"artifact-storage", "set", "--instance-limit", "2MiB"}, {"artifact-storage", "recompute"}} {
		if out, err := run(member, append([]string{"admin", "instance"}, args...)...); err == nil {
			t.Fatalf("non-admin %v succeeded:\n%s", args, out)
		}
	}
	// Validation never reaches the server.
	for _, args := range [][]string{{"set"}, {"set", "--instance-limit", "lots"}, {"set", "--workspace-limit", "1KiB"}} {
		if out, err := run(admin, append([]string{"admin", "instance", "artifact-storage"}, args...)...); err == nil {
			t.Fatalf("%v accepted:\n%s", args, out)
		}
	}
	// Set both, then only one.
	if out, err := run(admin, "admin", "instance", "artifact-storage", "set", "--workspace-limit", "16GiB", "--instance-limit", "48GiB"); err != nil || !strings.Contains(out, "16.0 GiB") || !strings.Contains(out, "48.0 GiB") {
		t.Fatalf("set: %v\n%s", err, out)
	}
	if _, err := run(admin, "admin", "instance", "artifact-storage", "set", "--instance-limit", "40GiB"); err != nil {
		t.Fatal(err)
	}
	if got := show(); got.WorkspaceLimitBytes != 16<<30 || got.InstanceLimitBytes != 40<<30 {
		t.Fatalf("after set = %+v", got)
	}

	// Recompute: inject drift, preview, repair, repeat.
	if _, err := db.Exec(`UPDATE artifact_storage_global SET reserved_bytes=4096, used_bytes=123`); err != nil {
		t.Fatal(err)
	}
	var rowsBefore int
	_ = db.QueryRow(`SELECT (SELECT count(*) FROM workspaces)+(SELECT count(*) FROM users)`).Scan(&rowsBefore)
	raw, err := run(admin, "admin", "instance", "artifact-storage", "recompute", "--dry-run", "--format", "json")
	if err != nil {
		t.Fatalf("dry-run: %v\n%s", err, raw)
	}
	var rep adminArtifactStorageRecompute
	if err := json.Unmarshal([]byte(raw), &rep); err != nil || !rep.DryRun || !rep.Changed || rep.Instance.ReservedBefore != 4096 || rep.Instance.ReservedAfter != 0 || rep.Instance.UsedBefore != 123 || rep.Instance.UsedAfter != 0 {
		t.Fatalf("dry-run report = %s (%v)", raw, err)
	}
	if got := show(); got.ReservedBytes != 4096 {
		t.Fatalf("dry run wrote: %+v", got)
	}
	out, err := run(admin, "admin", "instance", "artifact-storage", "recompute")
	if err != nil || !strings.Contains(out, "4096 -> 0") || !strings.Contains(out, "No artifact data was deleted") {
		t.Fatalf("recompute: %v\n%s", err, out)
	}
	if got := show(); got.ReservedBytes != 0 || got.UsedBytes != 0 || got.WorkspaceLimitBytes != 16<<30 || got.InstanceLimitBytes != 40<<30 {
		t.Fatalf("after recompute = %+v", got)
	}
	out, err = run(admin, "admin", "instance", "artifact-storage", "recompute")
	if err != nil || !strings.Contains(out, "already match") {
		t.Fatalf("second recompute not idempotent: %v\n%s", err, out)
	}
	var rowsAfter int
	_ = db.QueryRow(`SELECT (SELECT count(*) FROM workspaces)+(SELECT count(*) FROM users)`).Scan(&rowsAfter)
	if rowsAfter != rowsBefore {
		t.Fatal("recompute changed data rows")
	}
}
