package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestInstanceBackupsAreForInstanceAdminsOnly(t *testing.T) {
	f := newInstanceFixture(t)
	wantCode(t, f.do(f.wsAdmin, "GET", "/api/v1/admin/instance/backups/bundles", ""), http.StatusForbidden, "workspace ADMIN lists")
	wantCode(t, f.do(f.wsAdmin, "GET", "/api/v1/admin/instance/backups/restores", ""), http.StatusForbidden, "workspace ADMIN reads restores")
	wantCode(t, f.do(f.wsAdmin, "POST", "/api/v1/admin/instance/backups/bundles/pin", `{"path":"/x"}`), http.StatusForbidden, "workspace ADMIN pins")
}

func TestInstanceBackupsBundlesPinAndUnpin(t *testing.T) {
	f := newInstanceFixture(t)
	ctx := context.Background()
	// A bundle of ws-new, which the instance admin does not belong to. The
	// file does not need to exist for the catalog, but the list prunes rows
	// whose file is gone, so give it one.
	path := t.TempDir() + "/crewship-workspace-new-20260901T000000Z.tar.zst"
	if err := writeFileForTest(path); err != nil {
		t.Fatal(err)
	}
	if err := backup.UpsertCatalogEntry(ctx, f.db, backup.CatalogEntry{
		FilePath: path, Scope: "workspace", WorkspaceID: "ws-new", Slug: "ws-new", CreatedAt: time.Now(),
		Size: 42, SHA256: "abc", Encrypted: true, FormatVersion: 3,
		Incomplete: []backup.IncompleteItem{{Kind: backup.IncompleteAttachmentMissing, Detail: "1 file", Count: 1, Workspace: "ws-new"}},
	}); err != nil {
		t.Fatal(err)
	}

	rr := f.do(f.boss, "GET", "/api/v1/admin/instance/backups/bundles", "")
	wantCode(t, rr, http.StatusOK, "list")
	type bundle struct {
		Path          string                  `json:"path"`
		WorkspaceSlug string                  `json:"workspace_slug"`
		Kind          string                  `json:"kind"`
		Pinned        bool                    `json:"pinned"`
		ProofLevel    int                     `json:"proof_level"`
		Incomplete    []backup.IncompleteItem `json:"incomplete"`
	}
	list := decodeAs[struct {
		Bundles []bundle `json:"bundles"`
	}](t, rr.Body.Bytes())
	if len(list.Bundles) != 1 || list.Bundles[0].Path != path || list.Bundles[0].Kind != "full" ||
		list.Bundles[0].ProofLevel != 1 || list.Bundles[0].Pinned || len(list.Bundles[0].Incomplete) != 1 {
		t.Fatalf("list = %s", rr.Body.String())
	}
	wantCode(t, f.do(f.boss, "GET", "/api/v1/admin/instance/backups/bundles?workspace=nope", ""), http.StatusNotFound, "unknown workspace filter")

	body, _ := json.Marshal(map[string]string{"path": path})
	rr = f.do(f.boss, "POST", "/api/v1/admin/instance/backups/bundles/pin", string(body))
	wantCode(t, rr, http.StatusOK, "pin")
	if got, _ := backup.GetCatalogEntry(ctx, f.db, path); !got.Pinned {
		t.Fatal("pin did not stick")
	}
	if !f.audited("instance.backup_pinned") {
		t.Fatal("pin left no instance audit entry")
	}
	rr = f.do(f.boss, "POST", "/api/v1/admin/instance/backups/bundles/unpin", string(body))
	wantCode(t, rr, http.StatusOK, "unpin")
	if got, _ := backup.GetCatalogEntry(ctx, f.db, path); got.Pinned {
		t.Fatal("unpin did not stick")
	}
	if !f.audited("instance.backup_unpinned") {
		t.Fatal("unpin left no instance audit entry")
	}
	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/backups/bundles/pin", `{"path":"/not/catalogued.tar.zst"}`), http.StatusNotFound, "pin unknown")
	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/backups/bundles/pin", `{}`), http.StatusBadRequest, "pin without path")
}

func TestInstanceBackupsRestoresListsReports(t *testing.T) {
	f := newInstanceFixture(t)
	if _, err := backup.RecordRestoreReport(context.Background(), f.db, backup.RestoreReport{
		Kind: backup.RestoreKindDryRun, ActorUserID: "boss", BundlePath: "/b/x.tar.zst", Target: "ws-new",
		Result: backup.RestoreResultPartial, Report: json.RawMessage(`{"attachments_missing":2}`),
	}); err != nil {
		t.Fatal(err)
	}
	rr := f.do(f.boss, "GET", "/api/v1/admin/instance/backups/restores?limit=10", "")
	wantCode(t, rr, http.StatusOK, "restores")
	got := decodeAs[struct {
		Restores []struct {
			Kind       string         `json:"kind"`
			ActorEmail string         `json:"actor_email"`
			Result     string         `json:"result"`
			Report     map[string]any `json:"report"`
		} `json:"restores"`
	}](t, rr.Body.Bytes())
	if len(got.Restores) != 1 || got.Restores[0].ActorEmail != "boss@ex.com" || got.Restores[0].Result != "partial" || got.Restores[0].Report["attachments_missing"] != float64(2) {
		t.Fatalf("restores = %s", rr.Body.String())
	}
	wantCode(t, f.do(f.boss, "GET", "/api/v1/admin/instance/backups/restores?limit=0", ""), http.StatusBadRequest, "bad limit")
}

func writeFileForTest(path string) error {
	return os.WriteFile(path, []byte("bundle"), 0o600)
}

// The workspace restore handler persists every restore and dry run it runs,
// failures included, and says which row it wrote.
func TestBackupRestore_PersistsReport(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h, userID, wsID := backupMutationRig(t)
	const pass = "persist-report-passphrase-123"
	rr := httptest.NewRecorder()
	h.Create(rr, withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/admin/backups",
		jsonBody(map[string]any{"scope": "workspace", "passphrase": pass})), userID, wsID, "OWNER"))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rr.Code, rr.Body.String())
	}
	var created createResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Incomplete == nil {
		t.Fatal("create response incomplete is null, want an array")
	}

	rr = httptest.NewRecorder()
	h.Restore(rr, withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/admin/backups/restore",
		jsonBody(map[string]any{"path": created.Path, "passphrase": pass, "dry_run": true})), userID, wsID, "OWNER"))
	if rr.Code != http.StatusOK {
		t.Fatalf("dry run = %d: %s", rr.Code, rr.Body.String())
	}
	var resp backupRestoreResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ReportID == "" || (resp.Result != "ok" && resp.Result != "partial") {
		t.Fatalf("dry run result=%q report_id=%q", resp.Result, resp.ReportID)
	}

	rr = httptest.NewRecorder()
	h.Restore(rr, withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/admin/backups/restore",
		jsonBody(map[string]any{"path": created.Path, "passphrase": "the wrong one", "dry_run": true})), userID, wsID, "OWNER"))
	if rr.Code == http.StatusOK {
		t.Fatalf("wrong passphrase restored: %s", rr.Body.String())
	}

	reports, err := backup.ListRestoreReports(context.Background(), h.db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 2 {
		t.Fatalf("reports = %+v, want 2", reports)
	}
	if reports[0].Result != backup.RestoreResultFailed || reports[0].Kind != backup.RestoreKindDryRun || reports[1].ID != resp.ReportID {
		t.Fatalf("reports = %+v", reports)
	}
}
