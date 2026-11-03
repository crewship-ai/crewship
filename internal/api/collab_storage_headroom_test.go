package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/artifacts"
)

const lowFreeDiskSQL = `UPDATE artifact_storage_global SET min_free_bytes=4611686018427387904 WHERE id=1`
const freeDiskSQL = `UPDATE artifact_storage_global SET min_free_bytes=0 WHERE id=1`

func wantStorageRefusal(t *testing.T, got *httptest.ResponseRecorder, what string) {
	t.Helper()
	want := httptest.NewRecorder()
	artifactStorageRefusal(want, artifacts.ErrStorageQuota)
	if got.Code != 413 || got.Body.String() != want.Body.String() {
		t.Fatalf("%s below reserve = %d %s, want %d %s", what, got.Code, got.Body, want.Code, want.Body)
	}
}

// #4835: a GridCraft edit below the free-disk reserve answers with the same
// storage-full response as every other artifact writer, writes no revision,
// and reads and status keep answering.
func TestGridCraftEditBelowFreeDiskReserveUsesStorageRefusal(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	wmSeedPage(t, f.db, "ws-old", "headroom-book", "headroom-book", "Workbook", "boss", "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='headroom-book'`)
	f.r.RegisterGridCraft(appengine.Options{Enabled: true}, gridcraftFixtureEngine{})
	base := "/api/v1/pages/headroom-book/workbook"
	importBody := `{"mutation_id":"import-1","mandate_id":"human-import","base_rev":0,"generation":0,"command":{"kind":"import","snapshot":{"sheets":[{}]}}}`
	wantCode(t, f.do(f.boss, "POST", base+"/operations?workspace_id=ws-old", importBody), 200, "confirmed import")
	mustExec(t, f.db, lowFreeDiskSQL)
	next := `{"mutation_id":"import-2","mandate_id":"human-import","base_rev":1,"generation":1,"command":{"kind":"import","snapshot":{"sheets":[{"A1":"x"}]}}}`
	wantStorageRefusal(t, f.do(f.boss, "POST", base+"/operations?workspace_id=ws-old", next), "GridCraft import")
	var revisions, audits int
	if err := f.db.QueryRow(`SELECT (SELECT count(*) FROM gridcraft_revisions WHERE page_id='headroom-book'),(SELECT count(*) FROM gridcraft_audit WHERE page_id='headroom-book')`).Scan(&revisions, &audits); err != nil || revisions != 1 || audits != 1 {
		t.Fatalf("refused write left revisions=%d audits=%d err=%v", revisions, audits, err)
	}
	wantCode(t, f.do(f.boss, "GET", base+"?workspace_id=ws-old", ""), 200, "read below reserve")
	wantCode(t, f.do(f.boss, "GET", base+"/operations/import-1?workspace_id=ws-old", ""), 200, "status below reserve")
	wantCode(t, f.do(f.boss, "POST", base+"/operations?workspace_id=ws-old", importBody), 200, "exact retry of a confirmed import")
	mustExec(t, f.db, freeDiskSQL)
	wantCode(t, f.do(f.boss, "POST", base+"/operations?workspace_id=ws-old", next), 200, "same write after space returned")
}

// #4835: legacy Mooring document creation and commands below the reserve get
// the same storage-full response and write nothing; reads keep working.
func TestMooringJSONBelowFreeDiskReserveUsesStorageRefusal(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	f.r.RegisterGridCraft(appengine.Options{Enabled: true}, gridcraftFixtureEngine{})
	body := `{"app_id":"hello","slug":"headroom-hello","name":"Headroom"}`
	mustExec(t, f.db, lowFreeDiskSQL)
	wantStorageRefusal(t, f.do(f.boss, "POST", "/api/v1/mooring/documents?workspace_id=ws-old", body), "Mooring create")
	var pagesRows, documents int
	if err := f.db.QueryRow(`SELECT (SELECT count(*) FROM pages WHERE slug='headroom-hello'),(SELECT count(*) FROM mooring_documents)`).Scan(&pagesRows, &documents); err != nil || pagesRows != 0 || documents != 0 {
		t.Fatalf("refused create left pages=%d documents=%d err=%v", pagesRows, documents, err)
	}
	mustExec(t, f.db, freeDiskSQL)
	wantCode(t, f.do(f.boss, "POST", "/api/v1/mooring/documents?workspace_id=ws-old", body), 201, "create after space returned")
	base := "/api/v1/pages/headroom-hello/mooring"
	mustExec(t, f.db, lowFreeDiskSQL)
	packet := `{"mutation_id":"one","base_rev":1,"generation":1,"command":"set","arguments":{"text":"refused"}}`
	wantStorageRefusal(t, f.do(f.boss, "POST", base+"/commands?workspace_id=ws-old", packet), "Mooring command")
	var receipts, rev int
	if err := f.db.QueryRow(`SELECT (SELECT count(*) FROM mooring_receipts),(SELECT rev FROM mooring_documents)`).Scan(&receipts, &rev); err != nil || receipts != 0 || rev != 1 {
		t.Fatalf("refused command left receipts=%d rev=%d err=%v", receipts, rev, err)
	}
	wantCode(t, f.do(f.boss, "GET", base+"?workspace_id=ws-old", ""), 200, "read below reserve")
	mustExec(t, f.db, freeDiskSQL)
	wantCode(t, f.do(f.boss, "POST", base+"/commands?workspace_id=ws-old", packet), 200, "same command after space returned")
}

// #4835: a WordCraft command below the reserve is refused with the storage
// refusal (413 on HTTP), the staged candidate is rejected, nothing persists,
// and the confirmed checkpoint stays readable.
func TestCommandSQLBelowFreeDiskReserveIsStorageRefusal(t *testing.T) {
	r := newCommandSQLRig(t, true)
	before := append([]byte(nil), r.p.p...)
	mustExec(t, r.f.r.db, lowFreeDiskSQL)
	_, err := r.s.Submit(r.request(), r.a, r.actor(), r.input("one", 0, "hello"))
	w := httptest.NewRecorder()
	if !artifactStorageRefusal(w, err) {
		t.Fatalf("command below reserve = %v, want storage refusal", err)
	}
	wantStorageRefusal(t, w, "WordCraft command")
	rev, _, n := r.state(t)
	if rev != "0" || n != 0 || r.p.acks != 0 || r.p.rejects != 1 || r.p.k || !bytes.Equal(r.p.p, before) {
		t.Fatal("refused command polluted state", rev, n, r.p.acks, r.p.rejects)
	}
	if _, err = r.s.ReadCheckpoint(r.request(), r.a); err != nil {
		t.Fatalf("read below reserve: %v", err)
	}
	mustExec(t, r.f.r.db, freeDiskSQL)
	if _, err = r.s.Submit(r.request(), r.a, r.actor(), r.input("two", 0, "hello")); err != nil {
		t.Fatalf("command after space returned: %v", err)
	}
}

// #4835: WordCraft document creation below the reserve writes no document or
// checkpoint row.
func TestCommandSQLCreateBelowFreeDiskReserveWritesNothing(t *testing.T) {
	f := newContinuityRig(t, true)
	mustExec(t, f.r.db, `DELETE FROM mooring_collab_documents WHERE page_id='admission-page'`)
	p := &commandSQLFixturePort{done: make(chan struct{})}
	s, err := newCollaborationCommandStore(true, f.s.pages, f.s.owners, f.s.fence, f.s.clock, f.owner, p)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"format": commandFormat, "document": "admission-page", "generation": "fixture-generation", "generations": []string{"fixture-generation"}, "revision": 0, "p": map[string]any{}, "history": []any{}, "seen": map[string]any{}, "engine_identity": map[string]any{"revision": commandEngineRevision, "patch_sha256": commandClockPatch, "catalog": commandCatalogIdentity}})
	req := httptest.NewRequest("POST", "/not-a-public-route", nil).WithContext(f.ctx)
	mustExec(t, f.r.db, lowFreeDiskSQL)
	err = s.Create(req, commandSQLSnapshot{Checkpoint: raw})
	w := httptest.NewRecorder()
	if !artifactStorageRefusal(w, err) {
		t.Fatalf("create below reserve = %v, want storage refusal", err)
	}
	var rows int
	if err = f.r.db.QueryRow(`SELECT (SELECT count(*) FROM mooring_collab_documents WHERE page_id='admission-page')+(SELECT count(*) FROM mooring_collab_checkpoints WHERE page_id='admission-page')`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("refused create wrote %d rows: %v", rows, err)
	}
	mustExec(t, f.r.db, freeDiskSQL)
	if err = s.Create(req, commandSQLSnapshot{Checkpoint: raw}); err != nil {
		t.Fatalf("create after space returned: %v", err)
	}
}
