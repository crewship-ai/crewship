package artifacts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestVersionStoreWorkspaceBudgetCombinesPagesAndRetainsConfirmedHistory(t *testing.T) {
	s, k, a, db := versionFixture(t, VersionOptions{MaxWorkspaceHistoryBytes: 1800})
	if _, err := db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('second','ws','second','Second','owner','{}')`); err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("a", 600)
	if _, err := s.Apply(t.Context(), k, versionRequest(0, content), a); err != nil {
		t.Fatal(err)
	}
	second := VersionKey{"ws", "second"}
	if _, err := s.Apply(t.Context(), second, versionRequest(0, strings.Repeat("b", 600)), a); !errors.Is(err, ErrVersionQuota) {
		t.Fatalf("second Page exceeded shared budget but was not refused: %v", err)
	}
	confirmed, err := s.Read(t.Context(), k, a)
	if err != nil || confirmed.LatestVersion != 1 || string(confirmed.Manifest.Files["index.html"].Content) != content {
		t.Fatalf("refusal changed confirmed data: %+v %v", confirmed, err)
	}
	var versions, orphans int
	if err = db.QueryRow(`SELECT count(*) FROM artifact_versions`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM artifact_version_blobs WHERE page_id='second'`).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if versions != 1 || orphans != 0 {
		t.Fatalf("refused upload changed stored history: versions=%d orphan blobs=%d", versions, orphans)
	}
}

func TestVersionStoreWorkspaceBudgetSerializesDifferentPageWriters(t *testing.T) {
	s, first, _, db := versionFixture(t, VersionOptions{MaxWorkspaceHistoryBytes: 1800})
	if _, err := db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('second','ws','second','Second','owner','{}')`); err != nil {
		t.Fatal(err)
	}
	keys := []VersionKey{first, {"ws", "second"}}
	start := make(chan struct{})
	results := make(chan error, len(keys))
	var ready sync.WaitGroup
	for _, key := range keys {
		ready.Add(1)
		go func() {
			ready.Done()
			<-start
			authorize := func(ctx context.Context, tx *sql.Tx) (string, error) {
				var actor string
				err := tx.QueryRowContext(ctx, `SELECT owner_user_id FROM pages WHERE workspace_id=? AND id=?`, key.WorkspaceID, key.PageID).Scan(&actor)
				return actor, err
			}
			_, err := s.Apply(t.Context(), key, versionRequest(0, strings.Repeat(key.PageID, 600/len(key.PageID))), authorize)
			results <- err
		}()
	}
	ready.Wait()
	close(start)
	accepted, refused := 0, 0
	for range keys {
		err := <-results
		if err == nil {
			accepted++
		} else if errors.Is(err, ErrVersionQuota) {
			refused++
		} else {
			t.Fatal(err)
		}
	}
	if accepted != 1 || refused != 1 {
		t.Fatalf("workspace reservation admitted %d and refused %d; want one of each", accepted, refused)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM artifact_versions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("committed %d versions above shared limit", count)
	}
}

func TestStorageUploadReservationCountsBeforePublicationAndReleasesOnlyItsOwnBytes(t *testing.T) {
	s, k, a, db := versionFixture(t, VersionOptions{MaxWorkspaceHistoryBytes: 2400})
	first, err := s.ReserveUpload(t.Context(), k, 1500, a)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	var pending int64
	if err = db.QueryRow(`SELECT reserved_bytes FROM artifact_storage_workspaces WHERE workspace_id='ws'`).Scan(&pending); err != nil || pending != 1500 {
		t.Fatalf("pending upload not charged: %d %v", pending, err)
	}
	if _, err = s.ReserveUpload(t.Context(), k, 1000, a); !errors.Is(err, ErrVersionQuota) {
		t.Fatalf("second upload exceeded pending budget: %v", err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := s.ReserveUpload(t.Context(), k, 1500, a)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT reserved_bytes FROM artifact_storage_global WHERE id=1`).Scan(&pending); err != nil || pending != 1500 {
		t.Fatalf("old cleanup erased newer reservation: %d %v", pending, err)
	}
	if _, err = s.Apply(second.Context(t.Context()), k, versionRequest(0, strings.Repeat("a", 600)), a); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT reserved_bytes FROM artifact_storage_global WHERE id=1`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("committed upload remained double-charged: %d %v", pending, err)
	}
	if _, err = s.Read(t.Context(), k, a); err != nil {
		t.Fatalf("confirmed upload unreadable: %v", err)
	}
}

func TestStorageReservationRollbackAndScopeCannotEraseConfirmedData(t *testing.T) {
	s, k, a, db := versionFixture(t, VersionOptions{MaxWorkspaceHistoryBytes: 5000})
	if _, err := s.Apply(t.Context(), k, versionRequest(0, "confirmed"), a); err != nil {
		t.Fatal(err)
	}
	upload, err := s.ReserveUpload(t.Context(), k, 1500, a)
	if err != nil {
		t.Fatal(err)
	}
	defer upload.Close()
	if _, err = s.Apply(upload.Context(t.Context()), k, versionRequest(0, "stale"), a); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale request: %v", err)
	}
	var pending int64
	if err = db.QueryRow(`SELECT reserved_bytes FROM artifact_storage_global WHERE id=1`).Scan(&pending); err != nil || pending != 1500 {
		t.Fatalf("rollback lost held reservation: %d %v", pending, err)
	}
	if _, err = db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('second','ws','second','Second','owner','{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(upload.Context(t.Context()), VersionKey{"ws", "second"}, versionRequest(0, "wrong target"), a); !errors.Is(err, ErrVersionQuota) {
		t.Fatalf("reservation crossed Page identity: %v", err)
	}
	state, err := s.Read(t.Context(), k, a)
	if err != nil || state.LatestVersion != 1 || string(state.Manifest.Files["index.html"].Content) != "confirmed" {
		t.Fatalf("failed admission erased confirmed data: %+v %v", state, err)
	}
}

func TestStorageBudgetIncludesExistingC1PayloadBeforeManifestAdmission(t *testing.T) {
	s, k, a, db := versionFixture(t, VersionOptions{MaxWorkspaceHistoryBytes: 1400})
	if _, err := db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('legacy-c1','ws','legacy-c1','Existing C1','owner','{}')`); err != nil {
		t.Fatal(err)
	}
	// The accounting layer must preserve opaque committed payload bytes; it
	// does not interpret the editor format or convert the existing document.
	if _, err := db.Exec(`INSERT INTO documents(workspace_id,page_id,schema_version,snapshot,projection) VALUES('ws','legacy-c1',3,zeroblob(800),x'7b7d')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(t.Context(), k, versionRequest(0, strings.Repeat("a", 100)), a); !errors.Is(err, ErrVersionQuota) {
		t.Fatalf("C1 payload bypassed the common storage limit: %v", err)
	}
	var retained int
	if err := db.QueryRow(`SELECT length(snapshot) FROM documents WHERE page_id='legacy-c1'`).Scan(&retained); err != nil || retained != 800 {
		t.Fatalf("quota refusal changed C1 data: %d %v", retained, err)
	}
}

func TestQuotaPublicResponseDoesNotDiscloseWorkspaceOrInstanceUsage(t *testing.T) {
	q := VersionQuota{UsedBytes: 2, LimitBytes: 100, Retention: "keep-all", Storage: AggregateStorageQuota{WorkspaceUsedBytes: 1234567, WorkspaceReservedBytes: 2345678, WorkspaceLimitBytes: 3456789, UsedBytes: 4567890, ReservedBytes: 5678901, LimitBytes: 6789012}}
	body, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"storage", "workspace_used_bytes", "reserved_bytes", "1234567", "4567890"} {
		if strings.Contains(string(body), private) {
			t.Fatalf("Page quota exposed another scope's storage activity: %s", body)
		}
	}
}

// An aggregate refusal carries its own sentinel so the API can tell the user
// that workspace capacity, not this Page's history, is full. A Page-level
// history refusal must never be reported as aggregate storage.
func TestStorageRefusalIsDistinctFromPageHistoryQuota(t *testing.T) {
	s, k, a, db := versionFixture(t, VersionOptions{MaxWorkspaceHistoryBytes: 1800})
	if _, err := db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('second','ws','second','Second','owner','{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(t.Context(), k, versionRequest(0, strings.Repeat("a", 600)), a); err != nil {
		t.Fatal(err)
	}
	_, err := s.Apply(t.Context(), VersionKey{"ws", "second"}, versionRequest(0, strings.Repeat("b", 600)), a)
	if !errors.Is(err, ErrStorageQuota) || !errors.Is(err, ErrVersionQuota) {
		t.Fatalf("aggregate refusal lacks storage sentinel: %v", err)
	}
	small, sk, sa, _ := versionFixture(t, VersionOptions{MaxHistoryBytes: 1500})
	if _, err = small.Apply(t.Context(), sk, versionRequest(0, strings.Repeat("c", 600)), sa); err != nil {
		t.Fatal(err)
	}
	_, err = small.Apply(t.Context(), sk, versionRequest(1, strings.Repeat("d", 600)), sa)
	if !errors.Is(err, ErrVersionQuota) || errors.Is(err, ErrStorageQuota) {
		t.Fatalf("page history refusal misreported: %v", err)
	}
}

// Concurrent admissions are serialized by the SQLite writer and the capacity
// trigger: the sum of admitted reservations never exceeds the workspace limit.
func TestConcurrentStorageReservationsNeverExceedWorkspaceLimit(t *testing.T) {
	s, k, a, db := versionFixture(t, VersionOptions{MaxWorkspaceHistoryBytes: 2500})
	const workers = 6
	start := make(chan struct{})
	results := make(chan *StorageUpload, workers)
	errs := make(chan error, workers)
	var ready sync.WaitGroup
	for i := 0; i < workers; i++ {
		ready.Add(1)
		go func() {
			ready.Done()
			<-start
			u, err := s.ReserveUpload(t.Context(), k, 1000, a)
			if err != nil {
				errs <- err
				return
			}
			results <- u
		}()
	}
	ready.Wait()
	close(start)
	admitted := 0
	for i := 0; i < workers; i++ {
		select {
		case u := <-results:
			admitted++
			defer u.Close()
		case err := <-errs:
			if !errors.Is(err, ErrStorageQuota) {
				t.Fatalf("unexpected admission error: %v", err)
			}
		}
	}
	var reserved, limit int64
	if err := db.QueryRow(`SELECT reserved_bytes,max_bytes FROM artifact_storage_workspaces WHERE workspace_id='ws'`).Scan(&reserved, &limit); err != nil {
		t.Fatal(err)
	}
	if admitted != 2 || reserved != 2000 || reserved > limit {
		t.Fatalf("admitted %d reserved %d limit %d; want 2 admissions within the limit", admitted, reserved, limit)
	}
}

// The trigger-maintained counters must equal a full recomputation over the
// payload tables (the migration's seed query), and deleting the Pages must
// return every counter to zero without touching other workspaces.
func TestStorageAccountingMatchesSeedRecomputationAndDrainsOnPageDelete(t *testing.T) {
	s, k, a, db := versionFixture(t, VersionOptions{})
	if _, err := db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('legacy-c1','ws','legacy-c1','Existing C1','owner','{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(t.Context(), k, versionRequest(0, strings.Repeat("a", 700)), a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(t.Context(), k, versionRequest(1, strings.Repeat("b", 300)), a); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO documents(workspace_id,page_id,schema_version,snapshot,projection) VALUES('ws','legacy-c1',3,zeroblob(800),x'7b7d')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE documents SET snapshot=zeroblob(400) WHERE page_id='legacy-c1'`); err != nil {
		t.Fatal(err)
	}
	seed := storageSeedQuery(t)
	var expected int64
	if err := db.QueryRow(`SELECT coalesce(sum(bytes),0) FROM (` + seed + `) WHERE workspace_id='ws'`).Scan(&expected); err != nil {
		t.Fatal(err)
	}
	var used, global int64
	if err := db.QueryRow(`SELECT w.used_bytes,g.used_bytes FROM artifact_storage_workspaces w, artifact_storage_global g WHERE w.workspace_id='ws' AND g.id=1`).Scan(&used, &global); err != nil {
		t.Fatal(err)
	}
	if expected == 0 || used != expected || global != expected {
		t.Fatalf("trigger accounting drifted from recomputation: workspace %d global %d recomputed %d", used, global, expected)
	}
	if _, err := db.Exec(`DELETE FROM pages WHERE workspace_id='ws'`); err != nil {
		t.Fatalf("deleting Pages must never be blocked by accounting: %v", err)
	}
	if err := db.QueryRow(`SELECT w.used_bytes,g.used_bytes FROM artifact_storage_workspaces w, artifact_storage_global g WHERE w.workspace_id='ws' AND g.id=1`).Scan(&used, &global); err != nil {
		t.Fatal(err)
	}
	if used != 0 || global != 0 {
		t.Fatalf("Page delete left charges: workspace %d global %d", used, global)
	}
}

// Drifted counters (for example rows restored before accounting existed) must
// never block deletion; decrements saturate at zero.
func TestStorageAccountingDriftNeverBlocksDeletion(t *testing.T) {
	s, k, a, db := versionFixture(t, VersionOptions{})
	if _, err := s.Apply(t.Context(), k, versionRequest(0, strings.Repeat("a", 700)), a); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE artifact_storage_workspaces SET used_bytes=0; UPDATE artifact_storage_global SET used_bytes=0`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM pages WHERE id='page'`); err != nil {
		t.Fatalf("drifted accounting blocked deletion: %v", err)
	}
}

// Reads stay available when capacity is full so the user can recover work.
func TestStorageFullKeepsReadsAndReportsLevel(t *testing.T) {
	s, k, a, db := versionFixture(t, VersionOptions{})
	if _, err := s.Apply(t.Context(), k, versionRequest(0, "confirmed"), a); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE artifact_storage_workspaces SET max_bytes=used_bytes WHERE workspace_id='ws'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(t.Context(), k, versionRequest(1, "more"), a); !errors.Is(err, ErrStorageQuota) {
		t.Fatalf("full workspace accepted growth: %v", err)
	}
	state, err := s.Read(t.Context(), k, a)
	if err != nil || string(state.Manifest.Files["index.html"].Content) != "confirmed" {
		t.Fatalf("read unavailable when full: %v", err)
	}
	if state.Quota.StorageLevel != QuotaLevelFull {
		t.Fatalf("storage level %q, want full", state.Quota.StorageLevel)
	}
	usage, err := s.WorkspaceStorage(t.Context(), "ws")
	if err != nil || usage.UsedBytes == 0 || usage.UsedBytes != usage.LimitBytes || usage.Level != QuotaLevelFull {
		t.Fatalf("workspace usage %+v %v", usage, err)
	}
}

func storageSeedQuery(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "database", "migrations", storageMigrationFile))
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "CREATE TEMP TABLE artifact_storage_seed AS "
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSuffix(strings.TrimPrefix(line, prefix), ";")
		}
	}
	t.Fatal("seed query not found in storage migration")
	return ""
}

const storageMigrationFile = "20261030224950_artifact_aggregate_storage_budget.sql"
