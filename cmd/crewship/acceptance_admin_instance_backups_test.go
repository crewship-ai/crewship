package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// A real CLI process against a real router: Admin › Backups across
// workspaces. The instance admin sits in "people", reads the catalog of
// "lab" (which they do not belong to), pins and unpins its bundle, and reads
// the restore reports. Nothing is stubbed.
func TestAcceptance_AdminInstanceBackups(t *testing.T) {
	t.Setenv(backup.InstanceOwnerEmailEnv, "")
	db := testutil.MigratedDB(t).DB
	token := "crewship_cli_instbackups000000000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug,created_at) VALUES('ib-people','People','people','2026-01-01 00:00:00'),('ib-lab','Lab','lab','2026-06-01 00:00:00')`,
		`INSERT INTO users(id,email,full_name) VALUES('ib-boss','boss@people.invalid','Boss'),('ib-carol','carol@lab.invalid','Carol')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('ib-m1','ib-people','ib-boss','OWNER'),('ib-m2','ib-lab','ib-carol','OWNER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('ib-token','ib-boss','test',?,datetime('now'))`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	bundle := filepath.Join(t.TempDir(), "crewship-workspace-lab-20260901T000000Z.tar.zst")
	if err := os.WriteFile(bundle, []byte("bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := backup.UpsertCatalogEntry(ctx, db, backup.CatalogEntry{
		FilePath: bundle, Scope: "workspace", WorkspaceID: "ib-lab", Slug: "lab", CreatedAt: time.Now(),
		Size: 6, SHA256: "abc", Encrypted: true, FormatVersion: 3,
		Incomplete: []backup.IncompleteItem{{Kind: backup.IncompleteAttachmentMissing, Detail: "1 file", Count: 1, Workspace: "ib-lab"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.RecordRestoreReport(ctx, db, backup.RestoreReport{
		Kind: backup.RestoreKindDryRun, ActorUserID: "ib-boss", BundlePath: bundle, Target: "lab",
		Result: backup.RestoreResultPartial, Report: json.RawMessage(`{"attachments_missing":1}`),
	}); err != nil {
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
	pinned := func() bool {
		t.Helper()
		e, err := backup.GetCatalogEntry(ctx, db, bundle)
		if err != nil {
			t.Fatal(err)
		}
		return e.Pinned
	}

	// The catalog names lab's bundle, with its proof level and its gap.
	if out := must("admin", "instance", "backups", "bundles"); !strings.Contains(out, "lab") || !strings.Contains(out, "checksum") || !strings.Contains(out, "attachment_missing×1") {
		t.Fatalf("bundles table:\n%s", out)
	}
	var list struct {
		Bundles []struct {
			Path          string `json:"path"`
			WorkspaceSlug string `json:"workspace_slug"`
			ProofLevel    int    `json:"proof_level"`
			Pinned        bool   `json:"pinned"`
			Incomplete    []struct {
				Kind  string `json:"kind"`
				Count int    `json:"count"`
			} `json:"incomplete"`
		} `json:"bundles"`
	}
	raw := must("admin", "instance", "backups", "bundles", "--workspace", "lab", "--format", "json")
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatalf("bundles json: %v\n%s", err, raw)
	}
	if len(list.Bundles) != 1 || list.Bundles[0].Path != bundle || list.Bundles[0].WorkspaceSlug != "lab" ||
		list.Bundles[0].ProofLevel != 1 || len(list.Bundles[0].Incomplete) != 1 {
		t.Fatalf("bundles json = %s", raw)
	}
	if out := must("admin", "instance", "backups", "bundles", "--workspace", "people"); strings.Contains(out, bundle) {
		t.Fatalf("--workspace people still lists lab's bundle:\n%s", out)
	}

	// Pin, then unpin; both reach the catalog and the audit trail.
	if out := must("admin", "instance", "backups", "pin", bundle); !strings.Contains(out, "Pinned") || !pinned() {
		t.Fatalf("pin:\n%s", out)
	}
	if out := must("admin", "instance", "backups", "unpin", bundle); !strings.Contains(out, "Unpinned") || pinned() {
		t.Fatalf("unpin:\n%s", out)
	}
	var audits int
	_ = db.QueryRow(`SELECT COUNT(*) FROM instance_audit_logs WHERE action IN ('instance.backup_pinned','instance.backup_unpinned')`).Scan(&audits)
	if audits != 2 {
		t.Fatalf("audit entries = %d, want 2", audits)
	}
	if out, err := run("admin", "instance", "backups", "pin", "/not/a/bundle.tar.zst"); err == nil {
		t.Fatalf("pinning an uncatalogued path succeeded:\n%s", out)
	}

	// The restore reports, with their result and who ran them.
	if out := must("admin", "instance", "backups", "restores"); !strings.Contains(out, "dry_run") || !strings.Contains(out, "partial") || !strings.Contains(out, "boss@people.invalid") {
		t.Fatalf("restores:\n%s", out)
	}
	raw = must("admin", "instance", "backups", "restores", "--limit", "5", "--format", "json")
	var restores struct {
		Restores []struct {
			Result string         `json:"result"`
			Report map[string]any `json:"report"`
		} `json:"restores"`
	}
	if err := json.Unmarshal([]byte(raw), &restores); err != nil || len(restores.Restores) != 1 || restores.Restores[0].Report["attachments_missing"] != float64(1) {
		t.Fatalf("restores json (%v) = %s", err, raw)
	}
}
