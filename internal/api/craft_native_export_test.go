package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/craftsession"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// sheetExportFixture is sheetDocumentsFixture with the session handler (for other users'
// admissions) and a recording converter in place of the engine.
type sheetExportRig struct {
	f      *pagesTransferRig
	h      *craftNativeHTTP
	d      *craftNativeDocumentsHTTP
	owner  context.Context
	token  string
	mu     sync.Mutex
	got    [][]byte
	fail   error
	issued func(context.Context) string
}

func sheetExportFixture(t *testing.T) *sheetExportRig {
	t.Helper()
	f, h, ctx := craftNativeHTTPFixture(t)
	mustExec(t, f.db, `DELETE FROM craft_native_documents WHERE page_id='native-page'`)
	mustExec(t, f.db, `INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) VALUES('native-page',?,'gridcraft',?,?,?)`, f.wsID, strings.Repeat("a", 40), strings.Repeat("b", 64), strings.Repeat("c", 64))
	token, _ := craftNativeIssued(t, h, ctx)
	cfg := mooring.NativeStoreConfig{Application: "gridcraft", EngineRevision: strings.Repeat("a", 40), EngineSHA256: strings.Repeat("b", 64), EditorBundle: strings.Repeat("c", 64), Validate: func(context.Context, []byte) error { return nil }}
	d := newCraftNativeDocumentsHTTP(h, map[string]craftNativeApplication{"gridcraft": {Store: cfg, EditorURL: "/_editor/b/" + cfg.EditorBundle + "/index.html"}})
	s := artifacts.NewVersionStore(f.db, artifacts.VersionOptions{Enabled: true, ContentType: "gridcraft"})
	for i, doc := range []string{`{"v":1}`, `{"v":2}`} {
		if _, err := s.Apply(t.Context(), artifacts.VersionKey{WorkspaceID: f.wsID, PageID: "native-page"}, artifacts.VersionRequest{BaseVersion: int64(i), EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {Content: []byte(doc), MediaType: "application/json"}}}, func(context.Context, *sql.Tx) (string, error) { return "user:" + f.userID, nil }); err != nil {
			t.Fatal(err)
		}
	}
	rig := &sheetExportRig{f: f, h: h, d: d, owner: ctx, token: token}
	d.importer().export = func(_ context.Context, native []byte) ([]byte, error) {
		rig.mu.Lock()
		defer rig.mu.Unlock()
		rig.got = append(rig.got, append([]byte(nil), native...))
		if rig.fail != nil {
			return nil, rig.fail
		}
		return []byte("PK-xlsx-of-" + string(native)), nil
	}
	rig.issued = func(ctx context.Context) string {
		w := craftNativeSessionCall(h, ctx)
		if w.Code != 201 {
			return ""
		}
		var admission struct {
			Token string `json:"session_token"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &admission); err != nil {
			t.Fatal(err)
		}
		return admission.Token
	}
	return rig
}

func (rig *sheetExportRig) export(ctx context.Context, token, query string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/pages/native-page/craft/document/export.xlsx"+query, nil).WithContext(ctx)
	r.SetPathValue("slug", "native-page")
	if token != "" {
		r.Header.Set("X-Craft-Session", token)
	}
	w := httptest.NewRecorder()
	rig.d.exportXLSX(w, r)
	return w
}

// The owner downloads the confirmed head as an xlsx named after the page; the issue is audited
// without the content.
func TestCraftSheetExportXLSXDownloadsTheConfirmedHead(t *testing.T) {
	rig := sheetExportFixture(t)
	w := rig.export(rig.owner, rig.token, "")
	if w.Code != 200 || w.Body.String() != `PK-xlsx-of-{"v":2}` {
		t.Fatalf("export %d %q", w.Code, w.Body)
	}
	h := w.Header()
	if h.Get("Content-Type") != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" || h.Get("Cache-Control") != "no-store" || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("X-Craft-Version") != "2" {
		t.Fatalf("headers %v", h)
	}
	if cd := h.Get("Content-Disposition"); !strings.HasPrefix(cd, `attachment; filename="Native.xlsx"`) || !strings.Contains(cd, "filename*=UTF-8''Native.xlsx") {
		t.Fatalf("disposition %q", cd)
	}
	rows := auditRowsOf(t, rig.f.db, "artifact.sheet_exported", "native-page")
	if len(rows) != 1 || rows[0].user != rig.f.userID {
		t.Fatalf("audit %+v", rows)
	}
	sum := sha256.Sum256(w.Body.Bytes())
	m := rows[0].meta
	if m["version"] != "2" || m["format"] != "xlsx" || m["bytes"] != float64(w.Body.Len()) || m["sha256"] != fmt.Sprintf("%x", sum[:8]) {
		t.Fatalf("audit metadata %v", m)
	}
	if raw, _ := json.Marshal(m); strings.Contains(string(raw), `"v"`) || strings.Contains(string(raw), "PK-") {
		t.Fatalf("audit carries content: %s", raw)
	}
}

// ?version=N exports that version when the workspace keeps version history, as the version
// read does; otherwise and for a malformed or absent version nothing is converted or audited.
func TestCraftSheetExportXLSXOfAVersion(t *testing.T) {
	rig := sheetExportFixture(t)
	if w := rig.export(rig.owner, rig.token, "?version=1"); w.Code != 404 {
		t.Fatalf("history off: %d %s", w.Code, w.Body)
	}
	craftNativeEnableVersionHistory(t, rig.f.db, true)
	if w := rig.export(rig.owner, rig.token, "?version=1"); w.Code != 200 || w.Body.String() != `PK-xlsx-of-{"v":1}` || w.Header().Get("X-Craft-Version") != "1" {
		t.Fatalf("version 1: %d %q", w.Code, w.Body)
	}
	for q, want := range map[string]int{"?version=0": 400, "?version=01": 400, "?version=x": 400, "?version=9": 404} {
		if w := rig.export(rig.owner, rig.token, q); w.Code != want {
			t.Fatalf("%s: %d %s", q, w.Code, w.Body)
		}
	}
	if rows := auditRowsOf(t, rig.f.db, "artifact.sheet_exported", "native-page"); len(rows) != 1 || rows[0].meta["version"] != "1" {
		t.Fatalf("audit %+v", rows)
	}
}

// Export has exactly the authority of the document read: whoever the read refuses gets the
// same answer, without a conversion or an audit row; a revoked grant loses it on the same admission.
func TestCraftSheetExportXLSXAuthorityMatchesDocumentRead(t *testing.T) {
	rig := sheetExportFixture(t)
	f := rig.f
	editor := craftNativeVersionsUser(t, f, "native-version-editor", "Editor Eva", true, "write")
	reader := craftNativeVersionsUser(t, f, "native-version-reader", "Reader Rita", true, "read")
	outsider := craftNativeVersionsUser(t, f, "native-version-outsider", "Outsider Olga", false, "")
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		token  string
		status int
	}{
		{"owner", rig.owner, rig.token, 200},
		{"editor", editor, rig.issued(editor), 200},
		{"reader", reader, rig.issued(reader), 403},
		{"non-member", outsider, rig.issued(outsider), 403},
		{"non-member replaying the owner's admission", outsider, rig.token, 403},
		{"no admission", rig.owner, "", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := craftNativeDocumentCall(t, rig.d, tc.ctx, tc.token, "read", nil)
			before := len(rig.got)
			w := rig.export(tc.ctx, tc.token, "")
			if w.Code != tc.status || read.Code != tc.status {
				t.Fatalf("export %d %s, document read %d, want %d", w.Code, w.Body, read.Code, tc.status)
			}
			if tc.status != 200 && (len(rig.got) != before || strings.Contains(w.Body.String(), "PK-")) {
				t.Fatalf("refused export converted or leaked: %s", w.Body)
			}
		})
	}
	editorToken := rig.issued(editor)
	if w := rig.export(editor, editorToken, ""); w.Code != 200 {
		t.Fatalf("editor before revoke %d", w.Code)
	}
	mustExec(t, f.db, `DELETE FROM page_grants WHERE page_id='native-page' AND subject_id='native-version-editor'`)
	read := craftNativeDocumentCall(t, rig.d, editor, editorToken, "read", nil)
	if w := rig.export(editor, editorToken, ""); w.Code == 200 || w.Code != read.Code || strings.Contains(w.Body.String(), "PK-") {
		t.Fatalf("revoked editor exported: %d (read %d)", w.Code, read.Code)
	}
	if rows := auditRowsOf(t, f.db, "artifact.sheet_exported", "native-page"); len(rows) != 3 {
		t.Fatalf("%d audit rows for 3 exports", len(rows))
	}
}

// A failed conversion hands out nothing and records nothing; a Word document has no xlsx export.
func TestCraftSheetExportXLSXFailuresAndOtherApplications(t *testing.T) {
	rig := sheetExportFixture(t)
	for err, want := range map[error]int{craftsession.ErrTooLarge: 413, craftsession.ErrExportFailed: 500, errors.New("engine gone"): 500} {
		rig.fail = err
		if w := rig.export(rig.owner, rig.token, ""); w.Code != want || strings.Contains(w.Body.String(), "PK-") {
			t.Fatalf("%v: %d %s", err, w.Code, w.Body)
		}
	}
	if rows := auditRowsOf(t, rig.f.db, "artifact.sheet_exported", "native-page"); len(rows) != 0 {
		t.Fatalf("failed exports audited: %+v", rows)
	}
	_, _, d, ctx, token, _, _ := craftNativeDocumentsFixture(t)
	d.importer().export = func(context.Context, []byte) ([]byte, error) { t.Fatal("converted a Word document"); return nil, nil }
	r := httptest.NewRequest(http.MethodGet, "/api/v1/pages/native-page/craft/document/export.xlsx", nil).WithContext(ctx)
	r.SetPathValue("slug", "native-page")
	r.Header.Set("X-Craft-Session", token)
	w := httptest.NewRecorder()
	d.exportXLSX(w, r)
	if w.Code != 422 {
		t.Fatalf("word: %d %s", w.Code, w.Body)
	}
}

func TestCraftSheetExportXLSXRefusesFidelityLoss(t *testing.T) {
	rig := sheetExportFixture(t)
	craftNativeEnableVersionHistory(t, rig.f.db, true)
	rig.fail = &craftsession.FidelityLossError{Parts: []craftsession.FidelityPart{{Code: "charts", Count: 1}}}

	for _, query := range []string{"", "?version=1"} {
		w := rig.export(rig.owner, rig.token, query)
		if w.Code != http.StatusUnprocessableEntity || w.Header().Get("Content-Disposition") != "" ||
			w.Header().Get("Content-Type") != "application/json" || !strings.Contains(w.Body.String(), "without losing parts") {
			t.Fatalf("export %q: %d %s %v", query, w.Code, w.Body, w.Header())
		}
	}
	if rows := auditRowsOf(t, rig.f.db, auditActionCraftSheetExported, "native-page"); len(rows) != 0 {
		t.Fatalf("refused exports were recorded as downloads: %+v", rows)
	}
}

// The file name keeps the page name readable and safe in a header.
func TestCraftExportFilename(t *testing.T) {
	for in, want := range map[string]string{
		"Native":               `attachment; filename="Native.xlsx"; filename*=UTF-8''Native.xlsx`,
		`Q3 "plan"/v2`:         `attachment; filename="Q3 _plan__v2.xlsx"; filename*=UTF-8''Q3%20%22plan%22%2Fv2.xlsx`,
		"Rozpočet 2026":        `attachment; filename="Rozpo_et 2026.xlsx"; filename*=UTF-8''Rozpo%C4%8Det%202026.xlsx`,
		"":                     `attachment; filename="sheet.xlsx"; filename*=UTF-8''sheet.xlsx`,
		"a\r\nSet-Cookie: x=1": `attachment; filename="a__Set-Cookie_ x=1.xlsx"; filename*=UTF-8''a%0D%0ASet-Cookie%3A%20x%3D1.xlsx`,
	} {
		if got := craftExportDisposition(in, "xlsx"); got != want {
			t.Errorf("%q: %s, want %s", in, got, want)
		}
	}
}
