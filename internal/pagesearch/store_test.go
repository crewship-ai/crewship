package pagesearch

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

func storeFixture(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "search.db")+"?_pragma=foreign_keys(ON)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_txlock=deferred")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(`CREATE TABLE pages(id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL,UNIQUE(workspace_id,id))`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../database/migrations/20261016040008_page_search_projections.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	return NewStore(db), db
}

func cacheProjection(t *testing.T, db *sql.DB, page, text string) Projection {
	t.Helper()
	source, outline, blocks := documentFixture(t)
	source.PageID = page
	outline[0].Text = "ignored ancestor"
	outline[2].Text = text
	p, err := FromDocument(source, outline, blocks)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO pages(id,workspace_id) VALUES(?,?)`, page, source.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	return p
}

func allowSource(source SourceTag) Guard {
	return func(context.Context, *sql.Tx) (SourceTag, error) { return source, nil }
}

func TestStoreRechecksReadAndPublicationAndRejectsCorruptCache(t *testing.T) {
	store, db := storeFixture(t)
	p := cacheProjection(t, db, "page", "private searchable content")
	if err := store.Publish(t.Context(), p, allowSource(p.Source)); err != nil {
		t.Fatal(err)
	}
	stale := p.Source
	stale.Revision++
	if err := store.Publish(t.Context(), p, allowSource(stale)); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("stale publication admitted: %v", err)
	}
	denied := errors.New("synthetic live credential denied")
	reject := func(context.Context, *sql.Tx) (SourceTag, error) { return SourceTag{}, denied }
	if err := store.Publish(t.Context(), p, reject); !errors.Is(err, denied) {
		t.Fatalf("denied publication admitted: %v", err)
	}
	tx, err := db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Load(t.Context(), tx, p.Source, reject); !errors.Is(err, denied) {
		t.Fatalf("read skipped live guard: %v", err)
	}
	if _, err = store.Load(t.Context(), tx, p.Source, allowSource(stale)); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("read skipped source guard: %v", err)
	}
	if _, err = store.Load(t.Context(), tx, p.Source, allowSource(p.Source)); err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback()
	if _, err = db.Exec(`UPDATE page_search_projections SET encoded=CAST('corrupt' AS BLOB) WHERE page_id='page'`); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = store.Load(t.Context(), tx, p.Source, allowSource(p.Source)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("corrupt cache accepted: %v", err)
	}
}

func TestStoreQuotaChargesEncodedBytesAndReplacement(t *testing.T) {
	store, db := storeFixture(t)
	p := cacheProjection(t, db, "first", strings.Repeat("<", 100))
	second := cacheProjection(t, db, "second", strings.Repeat("<", 100))
	raw, _, _ := p.Encode()
	other, _, _ := second.Encode()
	budget := int64(len(raw) + len(other) - 1)
	if budget <= int64(len(p.Segments[0].Text)+len(second.Segments[0].Text)) {
		t.Fatal("test did not distinguish encoding expansion")
	}
	if err := store.publishBudget(t.Context(), p, allowSource(p.Source), budget); err != nil {
		t.Fatal(err)
	}
	if err := store.publishBudget(t.Context(), second, allowSource(second.Source), budget); !errors.Is(err, ErrBudget) {
		t.Fatalf("encoded quota ignored: %v", err)
	}
	// Replacing the same Page excludes its previous charge, not other Pages.
	p.Segments[0].Text = "smaller replacement"
	if err := store.publishBudget(t.Context(), p, allowSource(p.Source), budget); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM page_search_projections`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("quota failure published rows=%d %v", rows, err)
	}
	if err := store.publishBudget(t.Context(), p, allowSource(p.Source), MaxWorkspaceEncodedBytes+1); !errors.Is(err, ErrBudget) {
		t.Fatal("private path raised production ceiling")
	}
}

func TestStoreConcurrentQuotaIsAtomic(t *testing.T) {
	store, db := storeFixture(t)
	first := cacheProjection(t, db, "first", strings.Repeat("x", 200))
	second := cacheProjection(t, db, "second", strings.Repeat("x", 200))
	raw, _, _ := first.Encode()
	other, _, _ := second.Encode()
	budget := int64(max(len(raw), len(other)) + 1)
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, p := range []Projection{first, second} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			results <- store.publishBudget(t.Context(), p, allowSource(p.Source), budget)
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	success, quota := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrBudget) {
			quota++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || quota != 1 {
		t.Fatalf("concurrent quota outcomes success=%d quota=%d", success, quota)
	}
	var bytes int64
	if err := db.QueryRow(`SELECT sum(length(encoded)) FROM page_search_projections`).Scan(&bytes); err != nil || bytes > budget {
		t.Fatalf("writer quota exceeded %d %v", bytes, err)
	}
}
