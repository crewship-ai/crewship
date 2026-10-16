package documents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
)

var ctx = context.Background()
var key = Key{"workspace-a", "page-a"}
var projection = []byte(`[{"id":"one","hash":"h1"}]`)

func allow(context.Context, *sql.Tx) (string, error) { return "server-actor", nil }
func openTest(t *testing.T, path string) *database.DB {
	t.Helper()
	db, e := database.Open("file:" + path)
	if e != nil {
		t.Fatal(e)
	}
	return db
}
func fixture(t *testing.T, o Options) (*Store, *database.DB, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "documents.db")
	db := openTest(t, p)
	t.Cleanup(func() { db.Close() })
	if e := database.Migrate(ctx, db.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); e != nil {
		t.Fatal(e)
	}
	for _, q := range []string{
		`INSERT INTO users(id,email) VALUES('document-owner','document-owner@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('workspace-a','A','document-a'),('workspace-b','B','document-b')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('page-a','workspace-a','a','A','document-owner','{}'),('page-b','workspace-b','b','B','document-owner','{}'),('page-c','workspace-a','c','C','document-owner','{}')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s := New(db.DB, o)
	if _, e := s.Init(ctx, key, 1, []byte("baseline"), projection, allow); e != nil {
		t.Fatal(e)
	}
	return s, db, p
}
func acquire(t *testing.T, s *Store) Lease {
	t.Helper()
	l, e := s.Acquire(ctx, key, "node-instance", allow)
	if e != nil {
		t.Fatal(e)
	}
	return l
}
func commit(l Lease, id string, rev int64) Commit {
	return Commit{Lease: l, ID: id, Fingerprint: "semantic", IfRev: rev, Schema: 1, Update: []byte("incremental"), CandidateSnapshot: []byte("candidate"), Projection: projection, IfHashes: map[string]string{"one": "h1"}, Kind: "edit"}
}
func TestRestartReplayReceiptsAndWriterFence(t *testing.T) {
	s, db, p := fixture(t, Options{})
	l := acquire(t, s)
	c := commit(l, "request", 0)
	r, e := s.Commit(ctx, key, c, allow)
	if e != nil {
		t.Fatal(e)
	}
	if r.Actor != "server-actor" {
		t.Fatal(r)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	db2 := openTest(t, p)
	defer db2.Close()
	s = New(db2.DB, Options{})
	v, e := s.Read(ctx, key, allow)
	if e != nil || v.HeadRev != 1 || string(v.Snapshot) != "baseline" || len(v.Updates) != 1 {
		t.Fatalf("restart state %#v %v", v, e)
	}
	if e = s.InvalidateLeases(ctx); e != nil {
		t.Fatal(e)
	}
	r2, found, e := s.LookupReceipt(ctx, key, c.ID, c.Fingerprint, allow)
	if e != nil || !found || r2 != r {
		t.Fatalf("receipt replay %v %v", r2, e)
	}
	newLease := acquire(t, s)
	c.Lease = newLease
	c.Update = []byte("changed")
	if _, e = s.Commit(ctx, key, c, allow); !errors.Is(e, ErrDuplicate) {
		t.Fatalf("payload reuse: %v", e)
	}
	c = commit(l, "late", 1)
	if _, e = s.Commit(ctx, key, c, allow); !errors.Is(e, ErrFence) {
		t.Fatalf("old process: %v", e)
	}
	denied := func(context.Context, *sql.Tx) (string, error) { return "", errors.New("revoked") }
	if _, e = s.Commit(ctx, key, commit(l, "request", 0), denied); e == nil || e.Error() != "revoked" {
		t.Fatalf("receipt bypassed authorization: %v", e)
	}
}
func TestConcurrentCASAndWorkspaceIsolation(t *testing.T) {
	s, _, _ := fixture(t, Options{})
	l := acquire(t, s)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := s.Commit(ctx, key, commit(l, fmt.Sprint(i), 0), allow)
			errs <- e
		}(i)
	}
	wg.Wait()
	close(errs)
	ok, conflict := 0, 0
	for e := range errs {
		if e == nil {
			ok++
		} else if errors.Is(e, ErrConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatal(ok, conflict)
	}
	other := Key{"workspace-b", "page-b"}
	if _, e := s.Read(ctx, other, allow); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	if _, e := s.Init(ctx, other, 1, []byte("other baseline"), projection, allow); e != nil {
		t.Fatal(e)
	}
	v, e := s.Read(ctx, other, allow)
	if e != nil || v.HeadRev != 0 || len(v.Updates) != 0 {
		t.Fatalf("isolation %#v %v", v, e)
	}
	if _, e = s.Acquire(ctx, key, "another-process", allow); !errors.Is(e, ErrFence) {
		t.Fatal(e)
	}
}
func TestCheckpointRestoreAndBoundedReplay(t *testing.T) {
	s, db, _ := fixture(t, Options{})
	l := acquire(t, s)
	if _, e := s.Checkpoint(ctx, key, l, "pinned", []byte("pinned full state"), 0, allow); e != nil {
		t.Fatal(e)
	}
	for i := int64(0); i < 40; i++ {
		if _, e := s.Commit(ctx, key, commit(l, fmt.Sprint(i), i), allow); e != nil {
			t.Fatal(e)
		}
	}
	v, e := s.Read(ctx, key, allow)
	if e != nil || v.HeadRev != 40 || len(v.Updates) != 8 || string(v.Snapshot) != "candidate" {
		t.Fatalf("compaction %#v %v", v, e)
	}
	var n int
	if e = db.QueryRow("SELECT count(*) FROM document_receipts").Scan(&n); e != nil || n != 42 {
		t.Fatal(n, e)
	}
	if _, e = s.Restore(ctx, key, l, "pinned", 39, allow); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	stats, e := s.Stats(ctx, key, allow)
	if e != nil || stats.Receipts != 42 || stats.Checkpoints != 1 || stats.ReplayUpdates != 8 || stats.ReplayBytes != int64(8*len("incremental")) {
		t.Fatal(stats, e)
	}
	pinned, e := s.Checkpoint(ctx, key, l, "pinned", []byte("pinned full state"), 0, allow)
	if e != nil || pinned.Rev != 0 {
		t.Fatal(pinned, e)
	}
	r, e := s.RestoreRequest(ctx, key, l, "pinned", "restore-request", 40, allow)
	if e != nil || r.Rev != 41 || r.Epoch != l.Epoch+1 {
		t.Fatal(r, e)
	}
	replayed, e := s.RestoreRequest(ctx, key, l, "pinned", "restore-request", 40, allow)
	if e != nil || replayed != r {
		t.Fatal(replayed, e)
	}
	if _, e := s.RestoreRequest(ctx, key, l, "pinned", "restore-request", 41, allow); !errors.Is(e, ErrDuplicate) {
		t.Fatal(e)
	}
	v, e = s.Read(ctx, key, allow)
	if e != nil || string(v.Snapshot) != "pinned full state" || len(v.Updates) != 0 {
		t.Fatal(v, e)
	}
	if _, e = s.Commit(ctx, key, commit(l, "stale", 41), allow); !errors.Is(e, ErrFence) {
		t.Fatal(e)
	}
	if _, e = s.Acquire(ctx, key, "new-process", allow); e != nil {
		t.Fatal(e)
	}
}
func TestQuotaAndAtomicRollback(t *testing.T) {
	s, db, _ := fixture(t, Options{MaxCommits: 2, MaxDocuments: 2, MaxHistoryBytes: 50})
	l := acquire(t, s)
	if _, e := s.Commit(ctx, key, commit(l, "one", 0), allow); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Commit(ctx, key, commit(l, "two", 1), allow); !errors.Is(e, ErrQuota) {
		t.Fatal(e)
	}
	s = New(db.DB, Options{MaxHistoryBytes: 50, MaxDocuments: 2})
	if _, e := s.Checkpoint(ctx, key, l, "a", []byte("checkpoint"), 1, allow); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Checkpoint(ctx, key, l, "b", []byte("checkpoint"), 1, allow); !errors.Is(e, ErrQuota) {
		t.Fatal(e)
	}
	other := Key{"workspace-a", "page-c"}
	if _, e := s.Init(ctx, other, 1, []byte("other"), projection, allow); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('page-d','workspace-a','d','D','document-owner','{}')`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Init(ctx, Key{"workspace-a", "page-d"}, 1, []byte("third"), projection, allow); !errors.Is(e, ErrQuota) {
		t.Fatal(e)
	}
	// A database failure after update/receipt insertion rolls the entire commit back.
	s = New(db.DB, Options{})
	if _, e := db.Exec("CREATE TRIGGER fail_document_head BEFORE UPDATE OF head_rev ON documents BEGIN SELECT RAISE(ABORT, 'injected storage failure'); END"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Commit(ctx, key, commit(l, "rollback", 1), allow); e == nil {
		t.Fatal("expected injected failure")
	}
	v, e := s.Read(ctx, key, allow)
	if e != nil || v.HeadRev != 1 || len(v.Updates) != 1 {
		t.Fatal(v, e)
	}
	var n int
	if e = db.QueryRow("SELECT count(*) FROM document_receipts WHERE id='rollback'").Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
}

func TestLeaseExpiryAndUnknownSchema(t *testing.T) {
	s, db, _ := fixture(t, Options{})
	old := acquire(t, s)
	if _, e := db.Exec("UPDATE documents SET lease_until=0 WHERE workspace_id=? AND page_id=?", key.WorkspaceID, key.PageID); e != nil {
		t.Fatal(e)
	}
	current := acquire(t, s)
	if current.Writer == old.Writer || current.Epoch <= old.Epoch {
		t.Fatal("lease identity reused")
	}
	if _, e := s.Commit(ctx, key, commit(old, "expired", 0), allow); !errors.Is(e, ErrFence) {
		t.Fatal(e)
	}
	if _, e := db.Exec("UPDATE documents SET schema_version=99,writer='',lease_until=0 WHERE workspace_id=? AND page_id=?", key.WorkspaceID, key.PageID); e != nil {
		t.Fatal(e)
	}
	v, e := s.Read(ctx, key, allow)
	if e != nil || v.Schema != 99 || string(v.Snapshot) != "baseline" {
		t.Fatal(v, e)
	}
	if _, e := s.Acquire(ctx, key, "future-reader", allow); !errors.Is(e, ErrSchema) {
		t.Fatal(e)
	}
}
func TestDatabaseScopeAndPageDeletion(t *testing.T) {
	s, db, _ := fixture(t, Options{})
	wrong := Key{"workspace-b", key.PageID}
	if _, e := s.Init(ctx, wrong, 1, []byte("wrong"), projection, allow); e == nil {
		t.Fatal("cross-workspace page accepted")
	}
	l := acquire(t, s)
	if _, e := s.Commit(ctx, key, commit(l, "one", 0), allow); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Checkpoint(ctx, key, l, "pinned", []byte("checkpoint"), 1, allow); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec("DELETE FROM pages WHERE id=?", key.PageID); e != nil {
		t.Fatal(e)
	}
	for _, table := range []string{"documents", "document_updates", "document_receipts", "document_checkpoints"} {
		var n int
		if e := db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); e != nil || n != 0 {
			t.Fatal(table, n, e)
		}
	}
}
func TestLogQuotaDoesNotLoseHistory(t *testing.T) {
	s, _, _ := fixture(t, Options{MaxLogBytes: 15})
	l := acquire(t, s)
	if _, e := s.Commit(ctx, key, commit(l, "one", 0), allow); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Commit(ctx, key, commit(l, "two", 1), allow); !errors.Is(e, ErrQuota) {
		t.Fatal(e)
	}
	v, e := s.Read(ctx, key, allow)
	if e != nil || v.HeadRev != 1 || len(v.Updates) != 1 || string(v.Snapshot) != "baseline" {
		t.Fatal(v, e)
	}
}

func TestAllMutationReceiptsRetainOriginalFingerprint(t *testing.T) {
	s, _, _ := fixture(t, Options{})
	// Exact initialization retry returns the established document; mismatch cannot overwrite it.
	if _, e := s.InitRequest(ctx, key, "__init", "internal-init", 1, []byte("baseline"), projection, allow); e != nil {
		t.Fatal(e)
	}
	if _, e := s.InitRequest(ctx, key, "__init", "changed", 1, []byte("baseline"), projection, allow); !errors.Is(e, ErrDuplicate) {
		t.Fatal(e)
	}
	l := acquire(t, s)
	if _, e := s.CheckpointRequest(ctx, key, l, "original-checkpoint", "semantic-checkpoint-fingerprint", []byte("checkpoint"), 0, allow); e != nil {
		t.Fatal(e)
	}
	checkpointReceipt, found, e := s.LookupReceipt(ctx, key, "original-checkpoint", "semantic-checkpoint-fingerprint", allow)
	if e != nil || !found || checkpointReceipt.Kind != "checkpoint" {
		t.Fatal(checkpointReceipt, found, e)
	}
	r, e := s.RestoreRequestWithFingerprint(ctx, key, l, "original-checkpoint", "original-restore", "semantic-restore-fingerprint", 0, allow)
	if e != nil {
		t.Fatal(e)
	}
	got, found, e := s.LookupReceipt(ctx, key, "original-restore", "semantic-restore-fingerprint", allow)
	if e != nil || !found || got != r {
		t.Fatal(got, found, e)
	}
	otherActor := func(context.Context, *sql.Tx) (string, error) { return "another-authorized-actor", nil }
	if _, _, e = s.LookupReceipt(ctx, key, "original-restore", "semantic-restore-fingerprint", otherActor); !errors.Is(e, ErrDuplicate) {
		t.Fatalf("cross-actor receipt leaked: %v", e)
	}
	if _, e = s.RestoreRequestWithFingerprint(ctx, key, l, "original-checkpoint", "original-restore", "different", 0, allow); !errors.Is(e, ErrDuplicate) {
		t.Fatalf("changed semantic fingerprint accepted: %v", e)
	}
	if _, e = s.CheckpointRequest(ctx, key, l, "original-checkpoint", "semantic-checkpoint-fingerprint", []byte("changed candidate"), 0, allow); !errors.Is(e, ErrDuplicate) {
		t.Fatalf("changed checkpoint payload accepted: %v", e)
	}
}
func TestReadRejectsMissingReplayUpdate(t *testing.T) {
	s, db, _ := fixture(t, Options{})
	l := acquire(t, s)
	for i := int64(0); i < 2; i++ {
		if _, e := s.Commit(ctx, key, commit(l, fmt.Sprint(i), i), allow); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := db.Exec("DELETE FROM document_updates WHERE rev=1"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Read(ctx, key, allow); e == nil {
		t.Fatal("partial replay returned as a complete document")
	}
}

func TestDocumentTypeAttachmentsAreMutuallyExclusive(t *testing.T) {
	s, db, _ := fixture(t, Options{})
	l := acquire(t, s)
	if _, e := s.Commit(ctx, key, commit(l, "retained-edit", 0), allow); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('document-panel-crew','workspace-a','Panel producer','document-panel-crew')`); e != nil {
		t.Fatal(e)
	}
	attachments := []struct{ name, query string }{
		{"artifact", `INSERT INTO page_artifacts(page_id,head_rev) VALUES(?,1)`},
		{"board", `INSERT INTO page_boards(page_id,seq,format) VALUES(?,0,'crewship-board-excalidraw-0.18.1-v1')`},
		{"project", `INSERT INTO page_project_drafts(page_id,source_digest,revision,spec_json,updated_at) VALUES(?,printf('%064d',0),1,'{}','2026-10-15T00:00:00Z')`},
		{"panel", `INSERT INTO page_panels(id,page_id,panel_id,schema,owner_crew_id,producer_kind,producer_ref,sla_seconds) VALUES('document-collision-panel',?,'panel','metric.v1','document-panel-crew','script','script',60)`},
	}
	for _, attachment := range attachments {
		t.Run(attachment.name, func(t *testing.T) {
			if _, e := db.Exec(attachment.query, key.PageID); e == nil {
				t.Fatal("attached another content type to a document")
			}
			state, e := s.Read(ctx, key, allow)
			if e != nil || state.HeadRev != 1 || string(state.Snapshot) != "baseline" || len(state.Updates) != 1 || string(state.Updates[0].Payload) != "incremental" {
				t.Fatalf("rejected type attachment modified document: %#v %v", state, e)
			}
			// Ordinary human-owned pages still accept their existing attachment type.
			pageID := "existing-" + attachment.name
			if _, e := db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,'workspace-a',?,?,'document-owner','{}')`, pageID, pageID, pageID); e != nil {
				t.Fatal(e)
			}
			if _, e := db.Exec(attachment.query, pageID); e != nil {
				t.Fatalf("ordinary page attachment rejected: %v", e)
			}
			if _, e := s.InitRequest(ctx, Key{key.WorkspaceID, pageID}, "init-collision", "init-collision", 1, []byte("new document"), projection, allow); e == nil {
				t.Fatal("initialized Docs over another existing content type")
			}
			var count int
			if e := db.QueryRow(`SELECT count(*) FROM documents WHERE page_id=?`, pageID).Scan(&count); e != nil || count != 0 {
				t.Fatalf("rejected initialization persisted document: %d %v", count, e)
			}
		})
	}
}

func TestDocumentsRequirePersistentHumanOwner(t *testing.T) {
	s, db, _ := fixture(t, Options{})
	if _, e := db.Exec(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('document-owning-crew','workspace-a','Crew owner','document-owning-crew')`); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_crew_id,spec_json) VALUES('crew-page','workspace-a','crew-page','Crew page','document-owning-crew','{}')`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Init(ctx, Key{"workspace-a", "crew-page"}, 1, []byte("crew document"), projection, allow); e == nil {
		t.Fatal("initialized a crew-owned document")
	}
	if _, e := db.Exec(`UPDATE pages SET owner_user_id=NULL,owner_crew_id='document-owning-crew' WHERE id=?`, key.PageID); e == nil {
		t.Fatal("implicitly transferred document ownership to crew")
	}
	if _, e := db.Exec(`UPDATE pages SET owner_user_id=NULL WHERE id=?`, key.PageID); e == nil {
		t.Fatal("removed persistent document owner")
	}
	if _, e := db.Exec(`INSERT INTO users(id,email) VALUES('document-successor','document-successor@example.test')`); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`UPDATE pages SET owner_user_id='document-successor' WHERE id=?`, key.PageID); e != nil {
		t.Fatalf("explicit human succession rejected: %v", e)
	}
	v, e := s.Read(ctx, key, allow)
	if e != nil || string(v.Snapshot) != "baseline" || v.HeadRev != 0 {
		t.Fatalf("ownership succession changed document: %#v %v", v, e)
	}
	if _, e := db.Exec(`DELETE FROM pages WHERE id=?`, key.PageID); e != nil {
		t.Fatalf("explicit page deletion blocked: %v", e)
	}
	if _, e := s.Read(ctx, key, allow); !errors.Is(e, ErrNotFound) {
		t.Fatalf("document survived explicit page deletion: %v", e)
	}
}
