package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
)

// The restore checks judge the new name a new_workspace target lands under
// with the same rule the restore applies: free on this server, deleted
// workspaces included (#2990).
func TestRestoreChecksJudgeTheNewName(t *testing.T) {
	f := newInstanceFixture(t)
	ctx := context.Background()
	mustExec(t, f.db, `INSERT INTO workspaces (id, name, slug, deleted_at) VALUES ('ws-gone', 'Gone', 'ws-gone', datetime('now'))`)

	var payload bytes.Buffer
	tw, err := backup.NewTarZstWriter(&payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	m := &backup.Manifest{
		FormatVersion: backup.FormatVersion, Scope: backup.ScopeWorkspace, CreatedAt: time.Now().UTC(),
		CompatibleTargets: []backup.Target{backup.TargetAnyInstance}, CreatedBy: backup.Actor{UserID: "boss"},
		Contents: backup.Contents{Workspace: &backup.WorkspaceSummary{ID: "ws-old", Slug: "ws-old"}},
	}
	var sealed bytes.Buffer
	sha, n, err := backup.SealPayload(&sealed, bytes.NewReader(payload.Bytes()), backup.WriteBundleOptions{NoEncrypt: true})
	if err != nil {
		t.Fatal(err)
	}
	m.Checksums.PayloadSHA256 = sha
	path := filepath.Join(t.TempDir(), "crewship-workspace-ws-old-20260901T000000Z.tar.zst")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := backup.WriteBundleStream(file, m, &sealed, n); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	st, _ := os.Stat(path)
	if err := backup.UpsertCatalogEntry(ctx, f.db, backup.CatalogEntry{
		FilePath: path, Scope: "workspace", WorkspaceID: "ws-old", Slug: "ws-old", CreatedAt: time.Now(), Size: st.Size(), SHA256: sha, FormatVersion: backup.FormatVersion,
	}); err != nil {
		t.Fatal(err)
	}

	check := func(target, as string) backup.ConflictsCheck {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"path": path, "target": target, "as_workspace": as, "as_crew": as})
		rr := f.do(f.boss, "POST", "/api/v1/admin/instance/backups/restore/checks", string(body))
		wantCode(t, rr, http.StatusOK, "restore checks "+target+" "+as)
		return decodeAs[backup.RestoreChecks](t, rr.Body.Bytes()).Conflicts
	}
	if c := check("new_workspace", "ws-fresh"); !c.OK {
		t.Fatalf("a free name was refused: %+v", c)
	}
	if c := check("new_workspace", "ws-new"); c.OK || !strings.Contains(c.Detail, "already exists") {
		t.Fatalf("a live workspace's name passed: %+v", c)
	}
	if c := check("new_workspace", "ws-gone"); c.OK || !strings.Contains(c.Detail, "deleted") {
		t.Fatalf("a deleted workspace's name passed: %+v", c)
	}
	if c := check("crew", "ops-2"); c.OK {
		t.Fatalf("a crew target passed for a workspace backup: %+v", c)
	}
	if c := check("replace", ""); !c.OK || !strings.Contains(c.Detail, "replaces workspace ws-old") {
		t.Fatalf("replace = %+v", c)
	}
}
