package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/craftsession"
	"github.com/crewship-ai/crewship/internal/mooring"
)

type workbookMigrationRig struct {
	f   *pagesTransferRig
	d   *craftNativeDocumentsHTTP
	ctx context.Context
	h   *craftNativeHTTP
}

// workbookMigrationFixture: a workbook Sheet server with native Sheets left from before, an
// engine export that turns a native document into an xlsx.
func workbookMigrationFixture(t *testing.T, pages ...string) *workbookMigrationRig {
	t.Helper()
	f, h, ctx := craftNativeHTTPFixture(t)
	cfg := mooring.NativeStoreConfig{Application: "gridcraft", Format: mooring.NativeFormatWorkbook, EngineRevision: strings.Repeat("a", 40), EngineSHA256: strings.Repeat("b", 64), EditorBundle: strings.Repeat("c", 64), Validate: craftWorkbookValidate}
	d := newCraftNativeDocumentsHTTP(h, map[string]craftNativeApplication{"gridcraft": {Store: cfg, EditorURL: "/_editor/b/" + cfg.EditorBundle + "/index.html"}})
	d.importer().exportFaithful = func(_ context.Context, native []byte) ([]byte, error) {
		if bytes.Contains(native, []byte("broken")) {
			return nil, errors.New("engine refused")
		}
		if bytes.Contains(native, []byte("pivots")) {
			return nil, &craftsession.FidelityLossError{Parts: []craftsession.FidelityPart{{Code: "pivot_fields", Count: 120}}}
		}
		return craftTestWorkbook(t, "xlsx", string(native)), nil
	}
	v := artifacts.NewVersionStore(f.db, artifacts.VersionOptions{Enabled: true, ContentType: "gridcraft"})
	for _, page := range pages {
		f.seedPage(t, page, page, "Sheet "+page, f.userID, "")
		if _, err := v.Apply(t.Context(), artifacts.VersionKey{WorkspaceID: f.wsID, PageID: page}, artifacts.VersionRequest{EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {Content: []byte(`{"sheet":"` + page + `"}`), MediaType: "application/json"}}}, func(context.Context, *sql.Tx) (string, error) { return "user:" + f.userID, nil }); err != nil {
			t.Fatal(err)
		}
	}
	return &workbookMigrationRig{f: f, d: d, ctx: ctx, h: h}
}

func (rig *workbookMigrationRig) run(t *testing.T, body string) (int, craftWorkbookMigrationReport, string) {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/v1/admin/craft/migrate-workbooks", strings.NewReader(body)).WithContext(rig.ctx)
	w := httptest.NewRecorder()
	rig.d.migrateWorkbooks(w, r)
	var report craftWorkbookMigrationReport
	_ = json.Unmarshal(w.Body.Bytes(), &report)
	return w.Code, report, w.Body.String()
}

func (rig *workbookMigrationRig) head(t *testing.T, page string) (int64, string) {
	t.Helper()
	var version int64
	var entry string
	if err := rig.f.db.QueryRow(`SELECT h.latest_version, json_extract(v.manifest_json,'$.entry_point') FROM artifact_version_heads h JOIN artifact_versions v ON v.workspace_id=h.workspace_id AND v.page_id=h.page_id AND v.version=h.latest_version WHERE h.page_id=?`, page).Scan(&version, &entry); err != nil {
		t.Fatal(err)
	}
	return version, entry
}

func (rig *workbookMigrationRig) backup(t *testing.T, id string, at time.Time, scope string) {
	t.Helper()
	mustExec(t, rig.f.db, `INSERT INTO backup_catalog(id,file_path,scope,created_at,created_by,size,sha256,encrypted,format_version) VALUES(?,?,?,?,?,1,'x',1,1)`, id, "/backups/"+id+".tar", scope, at.UTC().Format(time.RFC3339), rig.f.userID)
}

// A dry run converts in memory and writes nothing; a real run needs a fresh instance backup, then
// adds one workbook version per native Sheet (CAS on the head, audited) and never removes the
// native versions; a second run finds nothing to do.
func TestWorkbookMigrationIsDryRunFirstBackedUpAndIdempotent(t *testing.T) {
	rig := workbookMigrationFixture(t, "wb-a", "wb-b")
	code, dry, raw := rig.run(t, `{"dry_run":true}`)
	if code != 200 || !dry.DryRun || dry.Pending != 2 || len(dry.Pages) != 2 || dry.Pages[0].Status != "would_migrate" || dry.Pages[0].WorkbookBytes == "" {
		t.Fatalf("dry run %d %s", code, raw)
	}
	if v, entry := rig.head(t, "wb-a"); v != 1 || entry != "document.json" {
		t.Fatalf("dry run wrote: %d %s", v, entry)
	}
	if code, _, raw = rig.run(t, `{}`); code != 412 || !strings.Contains(raw, `"code":"backup_required"`) {
		t.Fatalf("no backup %d %s", code, raw)
	}
	rig.backup(t, "old", time.Now().Add(-3*time.Hour), "instance")
	rig.backup(t, "ws", time.Now(), "workspace")
	for _, id := range []string{"old", "ws", "missing"} {
		if code, _, raw = rig.run(t, `{"backup_id":"`+id+`"}`); code != 412 || !strings.Contains(raw, `"code":"backup_required"`) {
			t.Fatalf("backup %s: %d %s", id, code, raw)
		}
	}
	rig.backup(t, "fresh", time.Now(), "instance")
	code, real, raw := rig.run(t, `{"backup_id":"fresh"}`)
	if code != 200 || real.DryRun || real.Migrated != 2 || real.BackupID != "fresh" {
		t.Fatalf("migration %d %s", code, raw)
	}
	for _, page := range []string{"wb-a", "wb-b"} {
		if v, entry := rig.head(t, page); v != 2 || entry != "workbook.xlsx" {
			t.Fatalf("%s head %d %s", page, v, entry)
		}
		var native, audited int
		_ = rig.f.db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE page_id=? AND version=1 AND json_extract(manifest_json,'$.entry_point')='document.json'`, page).Scan(&native)
		_ = rig.f.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE entity_id=? AND action='artifact.sheet_saved' AND json_extract(metadata,'$.source')='migration' AND json_extract(metadata,'$.version')='2'`, page).Scan(&audited)
		if native != 1 || audited != 1 {
			t.Fatalf("%s native kept %d audited %d", page, native, audited)
		}
	}
	if code, again, raw := rig.run(t, `{"backup_id":"fresh"}`); code != 200 || again.Pending != 0 || again.Migrated != 0 {
		t.Fatalf("second run %d %s", code, raw)
	}
}

// A Sheet open for editing now, or one the engine cannot convert, stays native and is reported;
// the others are migrated.
func TestWorkbookMigrationLeavesBusyAndFailingSheetsNative(t *testing.T) {
	rig := workbookMigrationFixture(t, "wb-busy", "wb-ok")
	broken := artifacts.NewVersionStore(rig.f.db, artifacts.VersionOptions{Enabled: true, ContentType: "gridcraft"})
	rig.f.seedPage(t, "wb-broken", "wb-broken", "Broken", rig.f.userID, "")
	if _, err := broken.Apply(t.Context(), artifacts.VersionKey{WorkspaceID: rig.f.wsID, PageID: "wb-broken"}, artifacts.VersionRequest{EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {Content: []byte(`{"broken":true}`), MediaType: "application/json"}}}, func(context.Context, *sql.Tx) (string, error) { return "user:" + rig.f.userID, nil }); err != nil {
		t.Fatal(err)
	}
	boot, now, err := rig.h.clock.Now()
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, rig.f.db, `INSERT INTO mooring_edit_leases(page_id,workspace_id,actor_id,session_id,generation,boot_id,expires_ns) VALUES('wb-busy',?,?,'s',1,?,?)`, rig.f.wsID, "user:"+rig.f.userID, boot, int64(now+time.Minute))
	rig.backup(t, "fresh", time.Now(), "instance")
	code, report, raw := rig.run(t, `{"backup_id":"fresh"}`)
	if code != 200 || report.Migrated != 1 || report.Busy != 1 || report.Failed != 1 {
		t.Fatalf("migration %d %s", code, raw)
	}
	status := map[string]craftWorkbookMigrationPage{}
	for _, p := range report.Pages {
		status[p.PageID] = p
	}
	if status["wb-busy"].Status != "busy" || status["wb-broken"].Status != "failed" || status["wb-broken"].Code != "convert" || status["wb-ok"].Status != "migrated" {
		t.Fatalf("pages %s", raw)
	}
	if strings.Contains(raw, "engine refused") {
		t.Fatalf("an engine error text reached the report: %s", raw)
	}
	for _, page := range []string{"wb-busy", "wb-broken"} {
		if v, entry := rig.head(t, page); v != 1 || entry != "document.json" {
			t.Fatalf("%s changed: %d %s", page, v, entry)
		}
	}
}

// The real run is refused while this server keeps Sheets natively; the dry run still answers.
func TestWorkbookMigrationNeedsWorkbookSheets(t *testing.T) {
	rig := workbookMigrationFixture(t, "wb-a")
	p := rig.d.applications["gridcraft"]
	p.Store.Format = ""
	rig.d.applications["gridcraft"] = p
	rig.backup(t, "fresh", time.Now(), "instance")
	if code, _, raw := rig.run(t, `{"backup_id":"fresh"}`); code != 409 || !strings.Contains(raw, `"code":"workbook_off"`) {
		t.Fatalf("native server %d %s", code, raw)
	}
	if code, dry, raw := rig.run(t, `{"dry_run":true}`); code != 200 || dry.Pending != 1 {
		t.Fatalf("dry run on a native server %d %s", code, raw)
	}
}

// A Sheet the xlsx writer would lose parts of (pivot fields) stays native, in the dry run and the
// real run, with what it would lose; nothing about its content is reported.
func TestWorkbookMigrationKeepsASheetThatWouldLoseParts(t *testing.T) {
	rig := workbookMigrationFixture(t, "pivots", "plain")
	rig.backup(t, "fresh", time.Now(), "instance")
	for _, body := range []string{`{"dry_run":true}`, `{"backup_id":"fresh"}`} {
		code, report, raw := rig.run(t, body)
		var lossy craftWorkbookMigrationPage
		for _, p := range report.Pages {
			if p.PageID == "pivots" {
				lossy = p
			}
		}
		if code != 200 || report.Failed != 1 || lossy.Status != "failed" || lossy.Code != "fidelity_loss" || len(lossy.Losses) != 1 || lossy.Losses[0] != (craftsession.FidelityPart{Code: "pivot_fields", Count: 120}) {
			t.Fatalf("%s: %d %s", body, code, raw)
		}
	}
	if v, entry := rig.head(t, "pivots"); v != 1 || entry != "document.json" {
		t.Fatalf("a lossy Sheet was migrated: %d %s", v, entry)
	}
	if v, entry := rig.head(t, "plain"); v != 2 || entry != "workbook.xlsx" {
		t.Fatalf("the clean Sheet: %d %s", v, entry)
	}
}
