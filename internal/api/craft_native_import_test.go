package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/craftapps"
	"github.com/crewship-ai/crewship/internal/craftsession"
	"github.com/crewship-ai/crewship/internal/mooring"
)

func importFixture(t *testing.T, scenario string, convert func(context.Context, []byte) (*craftsession.ImportResult, error)) (*craftNativeDocumentsHTTP, *pagesTransferRig, context.Context, string) {
	t.Helper()
	f, h, ctx := craftNativeHTTPFixture(t)
	const page = "native-import"
	owner := f.userID
	if scenario == "nonowner-writer" {
		owner = f.adminID
	}
	f.seedPage(t, page, page, "Import target", owner, "")
	if scenario == "nonowner-writer" {
		mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user',?,'write',?)`, page, f.userID, f.adminID)
	}
	if strings.HasPrefix(scenario, "c1-") {
		craftNativeSeedCollaboration(t, f, page, scenario == "c1-command")
	}
	cfg := mooring.NativeStoreConfig{Application: "gridcraft", EngineRevision: strings.Repeat("a", 40), EngineSHA256: strings.Repeat("b", 64), EditorBundle: strings.Repeat("c", 64),
		Validate: func(context.Context, []byte) error { return nil }}
	d := newCraftNativeDocumentsHTTP(h, map[string]craftNativeApplication{"gridcraft": {Store: cfg, EditorURL: "/_editor/b/" + cfg.EditorBundle + "/index.html"}})
	d.profiles["gridcraft"] = CraftNativeDocumentProfile{Application: "gridcraft", CLISHA256: strings.Repeat("b", 64)}
	d.importer().convert = convert
	return d, f, ctx, page
}

func okConvert(context.Context, []byte) (*craftsession.ImportResult, error) {
	return &craftsession.ImportResult{Native: []byte(`{"sheets":[]}`), Archive: &craftsession.ArchiveReport{Cells: 3, Notes: []string{"macros are not imported"}}, KeptCell: 3}, nil
}

func startImport(d *craftNativeDocumentsHTTP, ctx context.Context, page string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/v1/pages/@"+page+"/craft/import", bytes.NewReader(body)).WithContext(ctx)
	req.SetPathValue("slug", "@"+page)
	out := httptest.NewRecorder()
	d.importStart(out, req)
	return out
}

func importStatusOf(d *craftNativeDocumentsHTTP, ctx context.Context, page, id string, document bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/x", nil).WithContext(ctx)
	req.SetPathValue("slug", "@"+page)
	req.SetPathValue("id", id)
	out := httptest.NewRecorder()
	if document {
		d.importDocument(out, req)
	} else {
		d.importStatus(out, req)
	}
	return out
}

func waitImportState(t *testing.T, d *craftNativeDocumentsHTTP, ctx context.Context, page, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out := importStatusOf(d, ctx, page, id, false)
		var st map[string]any
		if json.Unmarshal(out.Body.Bytes(), &st) == nil && st["state"] != "converting" {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("import never finished")
	return nil
}

func TestCraftNativeImportOwnerGetsStatusAndDocument(t *testing.T) {
	d, _, ctx, page := importFixture(t, "owner", okConvert)
	out := startImport(d, ctx, page, []byte("PK-anything"))
	if out.Code != 202 {
		t.Fatalf("start: %d %s", out.Code, out.Body.String())
	}
	var started struct{ ID string }
	_ = json.Unmarshal(out.Body.Bytes(), &started)
	st := waitImportState(t, d, ctx, page, started.ID)
	if st["state"] != "ready" || st["cells"].(float64) != 3 || st["archive"] == nil {
		t.Fatalf("status: %v", st)
	}
	doc := importStatusOf(d, ctx, page, started.ID, true)
	if doc.Code != 200 || doc.Body.String() != `{"sheets":[]}` {
		t.Fatalf("document: %d %q", doc.Code, doc.Body.String())
	}
}

func TestCraftNativeImportIsPrivateToItsPageAndUnknownIdsLookTheSame(t *testing.T) {
	d, f, ctx, page := importFixture(t, "owner", okConvert)
	f.seedPage(t, "other-page", "other-page", "Another page", f.userID, "")
	out := startImport(d, ctx, page, []byte("PK"))
	var started struct{ ID string }
	_ = json.Unmarshal(out.Body.Bytes(), &started)
	waitImportState(t, d, ctx, page, started.ID)
	for name, got := range map[string]*httptest.ResponseRecorder{
		"same id on another page":  importStatusOf(d, ctx, "other-page", started.ID, false),
		"document on another page": importStatusOf(d, ctx, "other-page", started.ID, true),
		"unknown id":               importStatusOf(d, ctx, page, strings.Repeat("0", 64), false),
	} {
		if got.Code != 404 || strings.Contains(got.Body.String(), "sheets") {
			t.Errorf("%s: %d %s", name, got.Code, got.Body.String())
		}
	}
}

func TestCraftNativeImportRefusesWhoMayNotInitializeThePage(t *testing.T) {
	for scenario, want := range map[string]int{"nonowner-writer": 403, "c1-causal": 409} {
		t.Run(scenario, func(t *testing.T) {
			d, _, ctx, page := importFixture(t, scenario, okConvert)
			out := startImport(d, ctx, page, []byte("PK"))
			if out.Code != want {
				t.Fatalf("%d %s", out.Code, out.Body.String())
			}
			if n := len(d.importer().jobs); n != 0 {
				t.Fatalf("a refused import left %d jobs", n)
			}
		})
	}
}

func TestCraftNativeImportReportsWhyAConversionFailed(t *testing.T) {
	for code, err := range map[string]error{"not_xlsx": craftsession.ErrNotXLSX, "too_large": craftsession.ErrTooLarge, "truncated": craftsession.ErrTruncated, "failed": errors.New("engine")} {
		t.Run(code, func(t *testing.T) {
			d, _, ctx, page := importFixture(t, "owner", func(context.Context, []byte) (*craftsession.ImportResult, error) { return nil, err })
			var started struct{ ID string }
			_ = json.Unmarshal(startImport(d, ctx, page, []byte("PK")).Body.Bytes(), &started)
			st := waitImportState(t, d, ctx, page, started.ID)
			if st["state"] != "failed" || st["code"] != code {
				t.Fatalf("%v", st)
			}
			if doc := importStatusOf(d, ctx, page, started.ID, true); doc.Code != 409 {
				t.Fatalf("a failed import served a document: %d", doc.Code)
			}
		})
	}
}

func TestCraftNativeImportBoundsBodyAndConcurrency(t *testing.T) {
	d, f, ctx, page := importFixture(t, "owner", okConvert)
	d.importer().maxXLSX = 16
	if out := startImport(d, ctx, page, bytes.Repeat([]byte("x"), 17)); out.Code != 413 {
		t.Fatalf("oversized body: %d", out.Code)
	}
	d.importer().maxXLSX = 0
	release := make(chan struct{})
	d.importer().convert = func(context.Context, []byte) (*craftsession.ImportResult, error) {
		<-release
		return okConvert(context.Background(), nil)
	}
	defer close(release)
	// one running conversion per person: two people fill the two slots
	f.seedPage(t, "admin-import", "admin-import", "Admin import", f.adminID, "")
	admin := craftNativeHumanAs(t, f, f.adminID)
	if out := startImport(d, ctx, page, []byte("PK")); out.Code != 202 {
		t.Fatalf("first: %d", out.Code)
	}
	if out := startImport(d, admin, "admin-import", []byte("PK")); out.Code != 202 {
		t.Fatalf("second person: %d", out.Code)
	}
	f.seedPage(t, "third-import", "third-import", "Third", f.adminID, "")
	if out := startImport(d, admin, "third-import", []byte("PK")); out.Code != 503 || out.Header().Get("Retry-After") == "" {
		t.Fatalf("over the active limit: %d", out.Code)
	}
}

func importInitializeOf(d *craftNativeDocumentsHTTP, ctx context.Context, page, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/x", nil).WithContext(ctx)
	req.SetPathValue("slug", "@"+page)
	req.SetPathValue("id", id)
	out := httptest.NewRecorder()
	d.importInitialize(out, req)
	return out
}

func TestCraftNativeImportInitializePublishesTheServerHeldDocumentOnce(t *testing.T) {
	d, f, ctx, page := importFixture(t, "owner", okConvert)
	f.seedPage(t, "other-page", "other-page", "Another page", f.userID, "")
	var started struct{ ID string }
	_ = json.Unmarshal(startImport(d, ctx, page, []byte("PK")).Body.Bytes(), &started)
	waitImportState(t, d, ctx, page, started.ID)
	// not another page's, and an unknown id looks the same
	if out := importInitializeOf(d, ctx, "other-page", started.ID); out.Code != 404 {
		t.Fatalf("another page: %d %s", out.Code, out.Body.String())
	}
	if out := importInitializeOf(d, ctx, page, strings.Repeat("1", 64)); out.Code != 404 {
		t.Fatalf("unknown id: %d", out.Code)
	}
	out := importInitializeOf(d, ctx, page, started.ID)
	if out.Code != 201 {
		t.Fatalf("initialize: %d %s", out.Code, out.Body.String())
	}
	var receipt craftNativeInitializeResponse
	if err := json.Unmarshal(out.Body.Bytes(), &receipt); err != nil || receipt.ApplicationID != "gridcraft" || receipt.Version != "1" || receipt.Token == "" {
		t.Fatalf("receipt %+v %v", receipt, err)
	}
	var stored int
	if err := f.db.QueryRow(`SELECT count(*) FROM craft_native_documents WHERE page_id=?`, page).Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("document rows %d %v", stored, err)
	}
	// consumed: neither the status nor a second publish finds it
	if got := importStatusOf(d, ctx, page, started.ID, false); got.Code != 404 {
		t.Fatalf("a consumed import is still listed: %d", got.Code)
	}
	if again := importInitializeOf(d, ctx, page, started.ID); again.Code != 404 {
		t.Fatalf("second publish: %d", again.Code)
	}
}

func TestCraftNativeImportInitializeWaitsForAReadyImport(t *testing.T) {
	d, _, ctx, page := importFixture(t, "owner", okConvert)
	release := make(chan struct{})
	d.importer().convert = func(context.Context, []byte) (*craftsession.ImportResult, error) {
		<-release
		return okConvert(context.Background(), nil)
	}
	defer close(release)
	var started struct{ ID string }
	_ = json.Unmarshal(startImport(d, ctx, page, []byte("PK")).Body.Bytes(), &started)
	if out := importInitializeOf(d, ctx, page, started.ID); out.Code != 409 {
		t.Fatalf("converting import: %d", out.Code)
	}
}

// craftNativeHumanAs is the fixture's session context for another member.
func craftNativeHumanAs(t *testing.T, f *pagesTransferRig, user string) context.Context {
	t.Helper()
	session := "native-source-" + user
	mustExec(t, f.db, `INSERT INTO user_sessions(id,user_id,expires_at) VALUES(?,?,?)`, session, user, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
	u := &AuthUser{ID: user, SessionID: session, CredentialExpiresAt: time.Now().Add(time.Hour)}
	source, err := captureOriginalHumanCredential(t.Context(), f.db, u, AuthKindSession, "")
	if err != nil {
		t.Fatal(err)
	}
	u.OriginalCredential = source
	return auth.WithOriginalCredential(context.WithValue(context.WithValue(context.WithValue(t.Context(), ctxUser, u), ctxAuthKind, AuthKindSession), ctxWorkspaceID, f.wsID), *source)
}

func importID(t *testing.T, out *httptest.ResponseRecorder) string {
	t.Helper()
	var started struct{ ID string }
	if out.Code != 202 || json.Unmarshal(out.Body.Bytes(), &started) != nil || started.ID == "" {
		t.Fatalf("start: %d %s", out.Code, out.Body.String())
	}
	return started.ID
}

// One person cannot take the import slots from everyone else: their second conversion waits
// for their first, while another member's still starts.
func TestCraftNativeImportLimitsRunningConversionsPerPerson(t *testing.T) {
	d, f, ctx, page := importFixture(t, "owner", okConvert)
	release := make(chan struct{})
	d.importer().convert = func(context.Context, []byte) (*craftsession.ImportResult, error) {
		<-release
		return okConvert(context.Background(), nil)
	}
	defer close(release)
	importID(t, startImport(d, ctx, page, []byte("PK")))
	if out := startImport(d, ctx, page, []byte("PK")); out.Code != 503 || out.Header().Get("Retry-After") == "" {
		t.Fatalf("a second running import of the same person: %d", out.Code)
	}
	f.seedPage(t, "admin-import", "admin-import", "Admin import", f.adminID, "")
	if out := startImport(d, craftNativeHumanAs(t, f, f.adminID), "admin-import", []byte("PK")); out.Code != 202 {
		t.Fatalf("another member is blocked by the first one: %d %s", out.Code, out.Body.String())
	}
}

// Finished imports a person keeps do not fill the shared table: a new one replaces their oldest.
func TestCraftNativeImportKeepsFewFinishedImportsPerPerson(t *testing.T) {
	d, _, ctx, page := importFixture(t, "owner", okConvert)
	var ids []string
	for i := 0; i < craftNativeImportMaxJobs+2; i++ {
		id := importID(t, startImport(d, ctx, page, []byte("PK")))
		waitImportState(t, d, ctx, page, id)
		ids = append(ids, id)
	}
	im := d.importer()
	im.mu.Lock()
	held := len(im.jobs)
	im.mu.Unlock()
	if held != craftNativeImportMaxPerUser {
		t.Fatalf("one person holds %d imports", held)
	}
	if out := importStatusOf(d, ctx, page, ids[0], false); out.Code != 404 {
		t.Fatalf("the oldest import was kept: %d", out.Code)
	}
	if out := importStatusOf(d, ctx, page, ids[len(ids)-1], false); out.Code != 200 {
		t.Fatalf("the newest import is gone: %d", out.Code)
	}
}

// A finished import is forgotten after its time even when nobody calls the server again.
func TestCraftNativeImportForgetsTheWorkbookAfterItsTime(t *testing.T) {
	d, _, ctx, page := importFixture(t, "owner", okConvert)
	d.importer().keep = 50 * time.Millisecond
	id := importID(t, startImport(d, ctx, page, []byte("PK")))
	waitImportState(t, d, ctx, page, id)
	deadline := time.Now().Add(5 * time.Second)
	for {
		im := d.importer()
		im.mu.Lock()
		n := len(im.jobs)
		im.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the converted workbook is still held")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The import id is no permission: once the person may no longer initialize the page (here the
// page changed hands), the import answers like an unknown one and is forgotten.
func TestCraftNativeImportIsRecheckedOnEveryRead(t *testing.T) {
	d, f, ctx, page := importFixture(t, "owner", okConvert)
	id := importID(t, startImport(d, ctx, page, []byte("PK")))
	waitImportState(t, d, ctx, page, id)
	mustExec(t, f.db, `UPDATE pages SET owner_user_id=? WHERE id=?`, f.adminID, page)
	for name, document := range map[string]bool{"status": false, "document": true} {
		if out := importStatusOf(d, ctx, page, id, document); out.Code != 404 || strings.Contains(out.Body.String(), "sheets") {
			t.Fatalf("%s after the page changed hands: %d %s", name, out.Code, out.Body.String())
		}
	}
	if out := importInitializeOf(d, ctx, page, id); out.Code != 404 {
		t.Fatalf("initialize after the page changed hands: %d", out.Code)
	}
	mustExec(t, f.db, `UPDATE pages SET owner_user_id=? WHERE id=?`, f.userID, page)
	if out := importStatusOf(d, ctx, page, id, false); out.Code != 404 {
		t.Fatalf("a refused import came back: %d", out.Code)
	}
}

// A refused workbook tells its owner where the engine would lose cells: sheet, kind and count.
func TestCraftNativeImportSaysWhereCellsWouldBeLost(t *testing.T) {
	lost := &craftsession.TruncatedError{Lost: []craftsession.LostCells{{Sheet: 2, Name: "Data", Kind: craftsession.LostFormula, Count: 3}}}
	d, _, ctx, page := importFixture(t, "owner", func(context.Context, []byte) (*craftsession.ImportResult, error) { return nil, lost })
	var started struct{ ID string }
	_ = json.Unmarshal(startImport(d, ctx, page, []byte("PK")).Body.Bytes(), &started)
	st := waitImportState(t, d, ctx, page, started.ID)
	got, _ := json.Marshal(st["lost"])
	if st["code"] != "truncated" || string(got) != `[{"count":3,"kind":"formula","name":"Data","sheet":2}]` {
		t.Fatalf("%v", st)
	}
}

// A refused import says whether to try again: "busy" while other workbooks convert, "sheet_off"
// when Sheet is not available on this server (both 503).
func TestCraftNativeImportSaysBusyOrSheetOff(t *testing.T) {
	d, f, ctx, page := importFixture(t, "owner", okConvert)
	d.importer().active = 1 << 20 // every conversion slot is taken
	out := startImport(d, ctx, page, []byte("PK"))
	var got struct{ Code string }
	_ = json.Unmarshal(out.Body.Bytes(), &got)
	if out.Code != 503 || got.Code != "busy" || out.Header().Get("Retry-After") == "" {
		t.Fatalf("busy: %d %s", out.Code, out.Body.String())
	}
	d.importer().active = 0
	d.human.apps = newCraftAppsGate(&craftapps.Policy{}, f.db) // Sheet turned off
	out = startImport(d, ctx, page, []byte("PK"))
	got.Code = ""
	_ = json.Unmarshal(out.Body.Bytes(), &got)
	if out.Code != 503 || got.Code != "sheet_off" || out.Header().Get("Retry-After") != "" {
		t.Fatalf("sheet off: %d %s", out.Code, out.Body.String())
	}
}

// A finished conversion is recorded as a document the server's own engine computed (its next
// open keeps its values); a failed one is not.
func TestCraftNativeImportRecordsItsComputedDocument(t *testing.T) {
	d, _, ctx, page := importFixture(t, "owner", okConvert)
	var mu sync.Mutex
	var got [][]byte
	var apps []string
	d.computed = func(p CraftNativeDocumentProfile, doc []byte) {
		mu.Lock()
		defer mu.Unlock()
		got, apps = append(got, doc), append(apps, p.Application)
	}
	var started struct{ ID string }
	_ = json.Unmarshal(startImport(d, ctx, page, []byte("PK")).Body.Bytes(), &started)
	waitImportState(t, d, ctx, page, started.ID)
	mu.Lock()
	if len(got) != 1 || string(got[0]) != `{"sheets":[]}` || apps[0] != "gridcraft" {
		t.Fatalf("recorded %d %q", len(got), apps)
	}
	mu.Unlock()
}

// The document an import records as computed is byte for byte the one initialize publishes, so
// the first session opened from the store recognizes it.
func TestCraftNativeImportRecordsExactlyTheDocumentInitializePublishes(t *testing.T) {
	d, _, ctx, page := importFixture(t, "owner", okConvert)
	var recorded []byte
	d.computed = func(_ CraftNativeDocumentProfile, doc []byte) { recorded = append([]byte(nil), doc...) }
	var started struct{ ID string }
	_ = json.Unmarshal(startImport(d, ctx, page, []byte("PK")).Body.Bytes(), &started)
	waitImportState(t, d, ctx, page, started.ID)
	out := importInitializeOf(d, ctx, page, started.ID)
	var receipt craftNativeInitializeResponse
	if out.Code != 201 || json.Unmarshal(out.Body.Bytes(), &receipt) != nil {
		t.Fatalf("initialize: %d %s", out.Code, out.Body.String())
	}
	app, _ := d.application("gridcraft")
	cfg := app.Store
	cfg.Enabled, cfg.Leases = true, d.human.leases
	cfg.Authorize = func(context.Context, *sql.Tx, mooring.EditLeaseScope, string) (mooring.EditLeasePrincipal, error) {
		return mooring.EditLeasePrincipal{ActorID: "user:test", SessionID: "test"}, nil
	}
	st, err := mooring.NewNativeStore(d.human.pages.db, cfg).Read(ctx, mooring.EditLeaseScope{WorkspaceID: WorkspaceIDFromContext(ctx), ArtifactID: receipt.PageID})
	if err != nil {
		t.Fatal(err)
	}
	if f, ok := st.Manifest.Files["document.json"]; !ok || !bytes.Equal(f.Content, recorded) {
		t.Fatalf("published %d bytes, recorded %d", len(f.Content), len(recorded))
	}
}
