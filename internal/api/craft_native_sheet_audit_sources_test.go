package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/mooring"
)

type sheetAuditRow struct {
	user string
	meta map[string]any
}

func sheetAuditRows(t *testing.T, db *sql.DB, page string) []sheetAuditRow {
	t.Helper()
	return auditRowsOf(t, db, "artifact.sheet_saved", page)
}

func auditRowsOf(t *testing.T, db *sql.DB, action, page string) []sheetAuditRow {
	t.Helper()
	rows, err := db.Query(`SELECT COALESCE(user_id,''),metadata FROM audit_logs WHERE action=? AND entity_type='page' AND entity_id=? ORDER BY created_at, id`, action, page)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []sheetAuditRow
	for rows.Next() {
		var r sheetAuditRow
		var meta string
		if err := rows.Scan(&r.user, &meta); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(meta), &r.meta); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// wantSheetAudit checks one row: its version, source, person, and the size and digest prefix of doc.
func wantSheetAudit(t *testing.T, r sheetAuditRow, version int64, source, user string, doc []byte) {
	t.Helper()
	sum := sha256.Sum256(doc)
	if r.meta["version"] != strconv.FormatInt(version, 10) || r.meta["source"] != source || r.user != user ||
		r.meta["bytes"] != float64(len(doc)) || r.meta["sha256"] != hex.EncodeToString(sum[:8]) || r.meta["agent_id"] != nil {
		t.Fatalf("audit row %+v, want version %d source %s user %s bytes %d", r, version, source, user, len(doc))
	}
}

// sheetDocumentsFixture is craftNativeDocumentsFixture for a Sheet (gridcraft) Page at version 1.
func sheetDocumentsFixture(t *testing.T) (*pagesTransferRig, *craftNativeDocumentsHTTP, context.Context, string, map[string]any) {
	t.Helper()
	f, h, ctx := craftNativeHTTPFixture(t)
	// the profile is immutable: the fixture's Document profile is replaced before any session
	mustExec(t, f.db, `DELETE FROM craft_native_documents WHERE page_id='native-page'`)
	mustExec(t, f.db, `INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) VALUES('native-page',?,'gridcraft',?,?,?)`, f.wsID, strings.Repeat("a", 40), strings.Repeat("b", 64), strings.Repeat("c", 64))
	token, id := craftNativeIssued(t, h, ctx)
	if w := craftNativeLeaseCall(h, ctx, token, "native-page", "acquire", `{}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	cfg := mooring.NativeStoreConfig{Application: "gridcraft", EngineRevision: strings.Repeat("a", 40), EngineSHA256: strings.Repeat("b", 64), EditorBundle: strings.Repeat("c", 64), Validate: func(context.Context, []byte) error { return nil }}
	d := newCraftNativeDocumentsHTTP(h, map[string]craftNativeApplication{"gridcraft": {Store: cfg, EditorURL: "/_editor/b/" + cfg.EditorBundle + "/index.html"}})
	s := artifacts.NewVersionStore(f.db, artifacts.VersionOptions{Enabled: true, ContentType: "gridcraft"})
	if _, err := s.Apply(t.Context(), artifacts.VersionKey{WorkspaceID: f.wsID, PageID: "native-page"}, artifacts.VersionRequest{EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {Content: []byte(`{"v":1}`), MediaType: "application/json"}}}, func(context.Context, *sql.Tx) (string, error) { return "user:" + f.userID, nil }); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"v":2}`)
	body := map[string]any{"workspaceId": f.wsID, "pageId": "native-page", "applicationId": "gridcraft", "enginePin": mooring.NewNativeStore(nil, cfg).EnginePin(), "sessionId": id, "generation": "1", "baseVersion": "1", "editorRevision": "10:47:3", "contentHash": fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), "bytes": raw}
	return f, d, ctx, token, body
}

// A save from the editor writes one row with its version; a repeat and a stale save write none.
func TestCraftSheetEditorSaveIsAuditedWithItsVersion(t *testing.T) {
	f, d, ctx, token, body := sheetDocumentsFixture(t)
	if w := craftNativeDocumentCall(t, d, ctx, token, "save", body); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	rows := sheetAuditRows(t, f.db, "native-page")
	if len(rows) != 1 {
		t.Fatalf("%d audit rows after a save", len(rows))
	}
	wantSheetAudit(t, rows[0], 2, "editor", f.userID, []byte(`{"v":2}`))
	if w := craftNativeDocumentCall(t, d, ctx, token, "save", body); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	same := map[string]any{}
	for k, v := range body {
		same[k] = v
	}
	same["baseVersion"], same["editorRevision"] = "2", "10:48:3"
	if w := craftNativeDocumentCall(t, d, ctx, token, "save", same); w.Code != 200 || !strings.Contains(w.Body.String(), "unchanged") {
		t.Fatal(w.Code, w.Body)
	}
	stale := map[string]any{}
	for k, v := range body {
		stale[k] = v
	}
	other := []byte(`{"v":"stale"}`)
	stale["editorRevision"], stale["bytes"], stale["contentHash"] = "10:49:3", other, fmt.Sprintf("sha256:%x", sha256.Sum256(other))
	if w := craftNativeDocumentCall(t, d, ctx, token, "save", stale); w.Code == 200 {
		t.Fatalf("stale save admitted: %s", w.Body)
	}
	if rows = sheetAuditRows(t, f.db, "native-page"); len(rows) != 1 {
		t.Fatalf("%d audit rows after a repeat, an unchanged and a stale save", len(rows))
	}
}

// A workbook imported into an open Sheet is audited as an import.
func TestCraftSheetImportedSnapshotIsAuditedAsImport(t *testing.T) {
	f, d, ctx, token, body := sheetDocumentsFixture(t)
	if w := craftNativeDocumentCall(t, d, ctx, token, "import", body); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	rows := sheetAuditRows(t, f.db, "native-page")
	if len(rows) != 1 {
		t.Fatalf("%d audit rows", len(rows))
	}
	wantSheetAudit(t, rows[0], 2, "import", f.userID, []byte(`{"v":2}`))
}

// A restore is audited with the version it appends and the document it brings back.
func TestCraftSheetRestoreIsAuditedWithTheRestoredDocument(t *testing.T) {
	f, d, ctx, token, body := sheetDocumentsFixture(t)
	if w := craftNativeDocumentCall(t, d, ctx, token, "save", body); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	body["baseVersion"], body["targetVersion"] = "2", "1"
	if w := craftNativeDocumentCall(t, d, ctx, token, "restore", body); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	rows := sheetAuditRows(t, f.db, "native-page")
	if len(rows) != 2 {
		t.Fatalf("%d audit rows", len(rows))
	}
	wantSheetAudit(t, rows[1], 3, "restore", f.userID, []byte(`{"v":1}`))
}

// The version and its audit row commit together: when the row cannot be written, nothing is saved.
func TestCraftSheetSaveFailsWholeWhenItsAuditCannotBeWritten(t *testing.T) {
	f, d, ctx, token, body := sheetDocumentsFixture(t)
	key := sha256.Sum256([]byte("native-page\x002"))
	mustExec(t, f.db, `INSERT INTO audit_logs(id,workspace_id,action,entity_type,entity_id,metadata,created_at) VALUES(?,?,'other','page','native-page','{}','2026-01-01T00:00:00Z')`, "sheet-saved-"+hex.EncodeToString(key[:16]), f.wsID)
	if w := craftNativeDocumentCall(t, d, ctx, token, "save", body); w.Code == 200 {
		t.Fatalf("saved without its audit row: %s", w.Body)
	}
	var versions int
	if err := f.db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE page_id='native-page'`).Scan(&versions); err != nil || versions != 1 {
		t.Fatalf("versions %d %v", versions, err)
	}
	if rows := sheetAuditRows(t, f.db, "native-page"); len(rows) != 0 {
		t.Fatalf("%d sheet audit rows", len(rows))
	}
}

// A Document (wordcraft) has the same audit under its own action, never a Sheet row.
func TestCraftDocumentSaveAndRestoreAreAuditedAsDocument(t *testing.T) {
	f, _, d, ctx, token, _, body := craftNativeDocumentsFixture(t)
	if w := craftNativeDocumentCall(t, d, ctx, token, "save", body); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	body["baseVersion"], body["targetVersion"] = "2", "1"
	if w := craftNativeDocumentCall(t, d, ctx, token, "restore", body); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if rows := sheetAuditRows(t, f.db, "native-page"); len(rows) != 0 {
		t.Fatalf("%d sheet audit rows for a Document", len(rows))
	}
	rows := auditRowsOf(t, f.db, "artifact.document_saved", "native-page")
	if len(rows) != 2 {
		t.Fatalf("%d document audit rows", len(rows))
	}
	wantSheetAudit(t, rows[0], 2, "editor", f.userID, []byte(`{"v":2}`))
	wantSheetAudit(t, rows[1], 3, "restore", f.userID, []byte(`{"v":1}`))
}

// A person accepting an agent's proposal is audited with the agent and the proposal's request.
func TestCraftSheetAcceptedProposalIsAuditedWithItsAgent(t *testing.T) {
	f, d, ctx, token, body := sheetDocumentsFixture(t)
	id := "ncp_" + strings.Repeat("ab", 16)
	content := []byte(`{"v":"proposed"}`)
	pin := mooring.NewNativeStore(nil, d.applications["gridcraft"].Store).EnginePin()
	mustExec(t, f.db, `INSERT INTO craft_native_proposals(id,page_id,workspace_id,application_id,engine_pin,author_actor_id,request_id,base_version,summary,content_hash,content) VALUES(?,'native-page',?,'gridcraft',?,'agent:proposal-agent','req-7',1,'',?,?)`, id, f.wsID, pin, fmt.Sprintf("sha256:%x", sha256.Sum256(content)), content)
	decision := fmt.Sprintf(`{"sessionId":%q,"generation":"1"}`, body["sessionId"])
	if w := craftNativeProposalRoute(d, ctx, token, "POST", "/"+id+"/accept", id, decision); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	rows := sheetAuditRows(t, f.db, "native-page")
	if len(rows) != 1 {
		t.Fatalf("%d audit rows", len(rows))
	}
	r := rows[0]
	sum := sha256.Sum256(content)
	if r.meta["version"] != "2" || r.meta["source"] != "agent_proposal_accepted" || r.user != f.userID || r.meta["agent_id"] != "proposal-agent" ||
		r.meta["request_id"] != "req-7" || r.meta["bytes"] != float64(len(content)) || r.meta["sha256"] != hex.EncodeToString(sum[:8]) {
		t.Fatalf("audit row %+v", r)
	}
}

// A new Sheet is audited as initialize, its first version from a server-held import as import.
func TestCraftSheetInitializeIsAudited(t *testing.T) {
	d, f, ctx, page := importFixture(t, "owner", okConvert)
	out := craftSheetInitialize(d, ctx, page, `{"application_id":"gridcraft","bytes":"e30="}`)
	if out.Code != 201 {
		t.Fatalf("initialize: %d %s", out.Code, out.Body)
	}
	rows := sheetAuditRows(t, f.db, page)
	if len(rows) != 1 {
		t.Fatalf("%d audit rows", len(rows))
	}
	wantSheetAudit(t, rows[0], 1, "initialize", f.userID, []byte(`{}`))
}

func TestCraftSheetImportInitializeIsAuditedAsImport(t *testing.T) {
	d, f, ctx, page := importFixture(t, "owner", okConvert)
	var started struct{ ID string }
	_ = json.Unmarshal(startImport(d, ctx, page, []byte("PK")).Body.Bytes(), &started)
	waitImportState(t, d, ctx, page, started.ID)
	if out := importInitializeOf(d, ctx, page, started.ID); out.Code != 201 {
		t.Fatalf("initialize: %d %s", out.Code, out.Body)
	}
	rows := sheetAuditRows(t, f.db, page)
	if len(rows) != 1 {
		t.Fatalf("%d audit rows", len(rows))
	}
	wantSheetAudit(t, rows[0], 1, "import", f.userID, []byte(`{"sheets":[]}`))
}

// A refused initialize writes no row.
func TestCraftSheetRefusedInitializeIsNotAudited(t *testing.T) {
	d, f, ctx, page := importFixture(t, "nonowner-writer", okConvert)
	if out := craftSheetInitialize(d, ctx, page, `{"application_id":"gridcraft","bytes":"e30="}`); out.Code != 403 {
		t.Fatalf("initialize: %d %s", out.Code, out.Body)
	}
	if rows := sheetAuditRows(t, f.db, page); len(rows) != 0 {
		t.Fatalf("%d audit rows", len(rows))
	}
}

func craftSheetInitialize(d *craftNativeDocumentsHTTP, ctx context.Context, page, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body)).WithContext(ctx)
	req.SetPathValue("slug", "@"+page)
	rec := httptest.NewRecorder()
	d.initialize(rec, req)
	return rec
}
