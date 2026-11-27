package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// craftTestWorkbook is a minimal xlsx or xlsm archive whose one cell holds marker.
func craftTestWorkbook(t *testing.T, format, marker string) []byte {
	t.Helper()
	main := "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"
	if format == "xlsm" {
		main = "application/vnd.ms-excel.sheet.macroEnabled.main+xml"
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, part := range [][2]string{
		{"[Content_Types].xml", `<Types><Override PartName="/xl/workbook.xml" ContentType="` + main + `"/></Types>`},
		{"xl/workbook.xml", `<workbook><sheets><sheet name="L" sheetId="1"/></sheets></workbook>`},
		{"xl/worksheets/sheet1.xml", `<worksheet><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>` + marker + `</t></is></c></row></sheetData></worksheet>`},
	} {
		w, _ := zw.Create(part[0])
		_, _ = w.Write([]byte(part[1]))
	}
	if format == "xlsm" {
		// The same VBA project as the integration test workbooks, so a test save over this base keeps it.
		vba := make([]byte, 0, 4096)
		vba = append(vba, "\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"...)
		for i := 0; len(vba) < 4096; i++ {
			vba = append(vba, byte(i*31+7))
		}
		w, _ := zw.Create("xl/vbaProject.bin")
		_, _ = w.Write(vba)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type workbookRig struct {
	d        *craftNativeDocumentsHTTP
	ctx      context.Context
	token    string
	session  string
	original []byte
	pin      string
	db       *sql.DB
	ws       string
	rebase   string
	touched  *string // X-Craft-Touched of the next save, when set
}

// workbookDocumentsFixture: a gridcraft page that keeps workbook files, version 1 the uploaded
// xlsm, the owner admitted with the edit lease.
func workbookDocumentsFixture(t *testing.T) *workbookRig {
	t.Helper()
	f, h, ctx := craftNativeHTTPFixture(t)
	mustExec(t, f.db, `DELETE FROM craft_native_documents WHERE page_id='native-page'`)
	mustExec(t, f.db, `INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) VALUES('native-page',?,'gridcraft',?,?,?)`, f.wsID, strings.Repeat("a", 40), strings.Repeat("b", 64), strings.Repeat("c", 64))
	token, id := craftNativeIssued(t, h, ctx)
	if w := craftNativeLeaseCall(h, ctx, token, "native-page", "acquire", `{}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	cfg := mooring.NativeStoreConfig{Application: "gridcraft", Format: mooring.NativeFormatWorkbook, EngineRevision: strings.Repeat("a", 40), EngineSHA256: strings.Repeat("b", 64), EditorBundle: strings.Repeat("c", 64), Validate: craftWorkbookValidate}
	d := newCraftNativeDocumentsHTTP(h, map[string]craftNativeApplication{"gridcraft": {Store: cfg, EditorURL: "/_editor/b/" + cfg.EditorBundle + "/index.html"}})
	original := craftTestWorkbook(t, "xlsm", "original")
	s := artifacts.NewVersionStore(f.db, artifacts.VersionOptions{Enabled: true, ContentType: "gridcraft"})
	if _, err := s.Apply(t.Context(), artifacts.VersionKey{WorkspaceID: f.wsID, PageID: "native-page"}, artifacts.VersionRequest{BaseVersion: 0, EntryPoint: "workbook.xlsm", Files: map[string]artifacts.VersionFile{"workbook.xlsm": {Content: original, MediaType: mooring.WorkbookMediaTypeXLSM}}}, func(context.Context, *sql.Tx) (string, error) { return "user:" + f.userID, nil }); err != nil {
		t.Fatal(err)
	}
	return &workbookRig{d: d, ctx: ctx, token: token, session: id, original: original, pin: mooring.NewNativeStore(nil, cfg).EnginePin(), db: f.db, ws: f.wsID}
}

func (rig *workbookRig) read(headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/pages/native-page/craft/document", nil).WithContext(rig.ctx)
	r.SetPathValue("slug", "native-page")
	r.Header.Set("X-Craft-Session", rig.token)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	rig.d.readSnapshot(w, r)
	return w
}

func (rig *workbookRig) save(body []byte, mediaType, base, hash string, length int64) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pages/native-page/craft/document/save", bytes.NewReader(body)).WithContext(rig.ctx)
	r.SetPathValue("slug", "native-page")
	r.Header.Set("Content-Type", mediaType)
	r.Header.Set("X-Craft-Session", rig.token)
	r.Header.Set("X-Craft-Generation", "1")
	r.Header.Set("X-Craft-Base-Version", base)
	r.Header.Set("X-Craft-Editor-Revision", "doc-1:7")
	r.Header.Set("X-Craft-Engine-Pin", rig.pin)
	r.Header.Set("X-Craft-Content-Hash", hash)
	if rig.rebase != "" {
		r.Header.Set("X-Craft-Rebase", rig.rebase)
	}
	if rig.touched != nil {
		r.Header.Set("X-Craft-Touched", *rig.touched)
	}
	if length >= 0 {
		r.ContentLength = length
	}
	w := httptest.NewRecorder()
	rig.d.saveSnapshot(w, r)
	return w
}

func workbookHash(b []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(b)) }

// The document read of a workbook Sheet is the stored file itself, never compressed again, with
// the version, its hash and the read's metadata in headers.
func TestWorkbookSheetReadsAsItsFile(t *testing.T) {
	rig := workbookDocumentsFixture(t)
	w := rig.read(map[string]string{"Accept-Encoding": "gzip"})
	h := w.Header()
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), rig.original) || h.Get("Content-Encoding") != "" {
		t.Fatalf("read %d %v (%d bytes)", w.Code, h, w.Body.Len())
	}
	hash := workbookHash(rig.original)
	if h.Get("Content-Type") != mooring.WorkbookMediaTypeXLSM || h.Get("ETag") != `"`+hash+`"` || h.Get("X-Craft-Content-Hash") != hash || h.Get("X-Craft-Version") != "1" || h.Get("Cache-Control") != "no-store" || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers %v", h)
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(h.Get("X-Craft-Document")), &meta); err != nil {
		t.Fatal(err)
	}
	if meta["version"] != "1" || meta["format"] != "xlsm" || meta["contentHash"] != hash || meta["size"] != strconv.Itoa(len(rig.original)) || meta["applicationId"] != "gridcraft" || !strings.HasSuffix(fmt.Sprint(meta["fileName"]), ".xlsm") || meta["editorURL"] == nil || meta["quota"] == nil {
		t.Fatalf("metadata %v", meta)
	}
}

// A save carries the editor's file as the body; it becomes the next version and version 1 stays
// the uploaded original.
func TestWorkbookSheetSavesTheEditorsFile(t *testing.T) {
	rig := workbookDocumentsFixture(t)
	edited := craftTestWorkbook(t, "xlsm", "edited")
	w := rig.save(edited, mooring.WorkbookMediaTypeXLSM, "1", workbookHash(edited), -1)
	if w.Code != 200 {
		t.Fatalf("save %d %s", w.Code, w.Body)
	}
	var receipt mooring.NativeSaveReceipt
	if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil || receipt.Version != "2" || receipt.ContentHash != workbookHash(edited) || receipt.EditorRevision != "doc-1:7" || receipt.Phase != "committed" {
		t.Fatalf("receipt %+v %v", receipt, err)
	}
	read := rig.read(nil)
	if !bytes.Equal(read.Body.Bytes(), edited) || read.Header().Get("Content-Type") != mooring.WorkbookMediaTypeXLSM || read.Header().Get("X-Craft-Version") != "2" {
		t.Fatalf("head after save %v", read.Header())
	}
	// The same save again is its own receipt (#5075); another file on the old base has moved.
	if again := rig.save(edited, mooring.WorkbookMediaTypeXLSM, "1", workbookHash(edited), -1); again.Code != 200 || !strings.Contains(again.Body.String(), `"version":"2"`) {
		t.Fatalf("repeat %d %s", again.Code, again.Body)
	}
	other := craftTestWorkbook(t, "xlsm", "other")
	if stale := rig.save(other, mooring.WorkbookMediaTypeXLSM, "1", workbookHash(other), -1); stale.Code != 409 || !strings.Contains(stale.Body.String(), `"code":"moved"`) {
		t.Fatalf("stale base %d %s", stale.Code, stale.Body)
	}
}

// Every refusal is decided before a version exists, and named for the host.
func TestWorkbookSheetSaveRefusals(t *testing.T) {
	rig := workbookDocumentsFixture(t)
	good := craftTestWorkbook(t, "xlsx", "x")
	ole := append([]byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"), []byte("E\x00n\x00c\x00r\x00y\x00p\x00t\x00e\x00d\x00P\x00a\x00c\x00k\x00a\x00g\x00e\x00")...)
	for _, tc := range []struct {
		name      string
		body      []byte
		mediaType string
		hash      string
		length    int64
		status    int
		code      string
	}{
		{"hash mismatch", good, mooring.WorkbookMediaTypeXLSX, workbookHash([]byte("other")), -1, 400, ""},
		{"declared xlsm, is xlsx", good, mooring.WorkbookMediaTypeXLSM, workbookHash(good), -1, 415, "unsupported"},
		{"json body", []byte(`{"sheets":[]}`), mooring.WorkbookMediaTypeXLSX, workbookHash([]byte(`{"sheets":[]}`)), -1, 415, "not_workbook"},
		{"encrypted", ole, mooring.WorkbookMediaTypeXLSX, workbookHash(ole), -1, 415, "encrypted"},
		{"over 64 MiB", good, mooring.WorkbookMediaTypeXLSX, workbookHash(good), 64<<20 + 1, 413, "too_large"},
	} {
		w := rig.save(tc.body, tc.mediaType, "1", tc.hash, tc.length)
		if w.Code != tc.status || tc.code != "" && !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) {
			t.Errorf("%s: %d %s", tc.name, w.Code, w.Body)
		}
	}
	if read := rig.read(nil); read.Header().Get("X-Craft-Version") != "1" {
		t.Fatalf("a refused save wrote version %s", read.Header().Get("X-Craft-Version"))
	}
}

// The xlsx export of a workbook Sheet is its stored file: no engine runs, an xlsm stays an xlsm.
func TestWorkbookSheetExportIsTheStoredFile(t *testing.T) {
	rig := workbookDocumentsFixture(t)
	rig.d.importer().export = func(context.Context, []byte) ([]byte, error) {
		t.Error("the engine converted a workbook Sheet")
		return nil, fmt.Errorf("not expected")
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/pages/native-page/craft/document/export.xlsx", nil).WithContext(rig.ctx)
	r.SetPathValue("slug", "native-page")
	r.Header.Set("X-Craft-Session", rig.token)
	w := httptest.NewRecorder()
	rig.d.exportXLSX(w, r)
	h := w.Header()
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), rig.original) || h.Get("Content-Type") != mooring.WorkbookMediaTypeXLSM || !strings.Contains(h.Get("Content-Disposition"), ".xlsm") || h.Get("X-Craft-Version") != "1" {
		t.Fatalf("export %d %v", w.Code, h)
	}
}

// History of a workbook Sheet names each version's file (format, hash, size), and a version read
// is that version's file, like the document read.
func TestWorkbookSheetVersionsNameAndServeTheirFiles(t *testing.T) {
	rig := workbookDocumentsFixture(t)
	craftNativeEnableVersionHistory(t, rig.d.human.pages.db, true)
	edited := craftTestWorkbook(t, "xlsm", "edited")
	if w := rig.save(edited, mooring.WorkbookMediaTypeXLSM, "1", workbookHash(edited), -1); w.Code != 200 {
		t.Fatalf("save %d %s", w.Code, w.Body)
	}
	list := craftNativeVersionsCall(rig.d, rig.ctx, rig.token)
	var got craftNativeVersionsResponse
	if err := json.Unmarshal(list.Body.Bytes(), &got); list.Code != 200 || err != nil || len(got.Versions) != 2 {
		t.Fatalf("list %d %s %v", list.Code, list.Body, err)
	}
	head, first := got.Versions[0], got.Versions[1]
	if head.Format != "xlsm" || head.ContentHash != workbookHash(edited) || head.Size != strconv.Itoa(len(edited)) ||
		first.Format != "xlsm" || first.ContentHash != workbookHash(rig.original) || first.Size != strconv.Itoa(len(rig.original)) {
		t.Fatalf("listed %+v / %+v", head, first)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/pages/native-page/craft/document/versions/1", nil).WithContext(rig.ctx)
	r.SetPathValue("slug", "native-page")
	r.SetPathValue("version", "1")
	r.Header.Set("X-Craft-Session", rig.token)
	w := httptest.NewRecorder()
	rig.d.readVersion(w, r)
	var meta map[string]any
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), rig.original) || w.Header().Get("Content-Type") != mooring.WorkbookMediaTypeXLSM || w.Header().Get("X-Craft-Version") != "1" ||
		json.Unmarshal([]byte(w.Header().Get("X-Craft-Document")), &meta) != nil || meta["currentVersion"] != "2" || meta["current"] != false || meta["format"] != "xlsm" {
		t.Fatalf("version read %d %v %v", w.Code, w.Header(), meta)
	}
}
