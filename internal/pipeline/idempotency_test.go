package pipeline

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

const idempotencySchemaSQL = `
CREATE TABLE IF NOT EXISTS pipeline_run_idempotency (
    workspace_id    TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    run_id          TEXT NOT NULL,
    pipeline_id     TEXT NOT NULL,
    created_at      TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    expires_at      TEXT NOT NULL,
    PRIMARY KEY (workspace_id, idempotency_key)
);`

func openIdempotencyTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(context.Background(), idempotencySchemaSQL); err != nil {
		_ = db.Close()
		t.Fatalf("schema: %v", err)
	}
	return db
}

func TestIdempotency_LookupOrReserve_FreshKey(t *testing.T) {
	db := openIdempotencyTestDB(t)
	defer db.Close()
	store := NewIdempotencyStore(db)

	got, isNew, err := store.LookupOrReserve(context.Background(),
		"ws_test", "key_1", "run_a", "pipe_1", time.Hour)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if !isNew {
		t.Errorf("expected isNew=true on fresh key")
	}
	if got != "run_a" {
		t.Errorf("expected reserved run_a, got %s", got)
	}
}

func TestIdempotency_LookupOrReserve_DuplicateReturnsOriginal(t *testing.T) {
	db := openIdempotencyTestDB(t)
	defer db.Close()
	store := NewIdempotencyStore(db)
	ctx := context.Background()

	// First request reserves run_a
	_, _, err := store.LookupOrReserve(ctx, "ws_test", "key_1", "run_a", "pipe_1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	// Second request with same key but different run_id should
	// return the original
	got, isNew, err := store.LookupOrReserve(ctx, "ws_test", "key_1", "run_b", "pipe_1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if isNew {
		t.Errorf("expected isNew=false on duplicate key")
	}
	if got != "run_a" {
		t.Errorf("expected original run_a returned, got %s", got)
	}
}

func TestIdempotency_LookupOrReserve_DifferentWorkspacesIndependent(t *testing.T) {
	db := openIdempotencyTestDB(t)
	defer db.Close()
	store := NewIdempotencyStore(db)
	ctx := context.Background()

	got1, isNew1, err := store.LookupOrReserve(ctx, "ws_a", "key_x", "run_1", "pipe", time.Hour)
	if err != nil || !isNew1 || got1 != "run_1" {
		t.Errorf("ws_a fresh: got %s isNew=%v err=%v", got1, isNew1, err)
	}
	// Same key in different workspace must NOT collide
	got2, isNew2, err := store.LookupOrReserve(ctx, "ws_b", "key_x", "run_2", "pipe", time.Hour)
	if err != nil || !isNew2 || got2 != "run_2" {
		t.Errorf("ws_b fresh: got %s isNew=%v err=%v", got2, isNew2, err)
	}
}

func TestIdempotency_LookupOrReserve_ExpiredKeyReplaces(t *testing.T) {
	db := openIdempotencyTestDB(t)
	defer db.Close()
	store := NewIdempotencyStore(db)
	ctx := context.Background()

	// Reserve with a TTL so short the row is already expired by next call
	_, _, err := store.LookupOrReserve(ctx, "ws_test", "key_1", "run_a", "pipe_1", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)

	// Sweep + re-reserve should treat the key as fresh
	got, isNew, err := store.LookupOrReserve(ctx, "ws_test", "key_1", "run_b", "pipe_1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !isNew {
		t.Errorf("expected expired key to count as fresh, got isNew=%v", isNew)
	}
	if got != "run_b" {
		t.Errorf("expected new reservation run_b, got %s", got)
	}
}

func TestIdempotency_Forget_AllowsRetry(t *testing.T) {
	db := openIdempotencyTestDB(t)
	defer db.Close()
	store := NewIdempotencyStore(db)
	ctx := context.Background()

	_, _, _ = store.LookupOrReserve(ctx, "ws_test", "key_1", "run_a", "pipe_1", time.Hour)
	if err := store.Forget(ctx, "ws_test", "key_1"); err != nil {
		t.Fatalf("forget: %v", err)
	}
	got, isNew, err := store.LookupOrReserve(ctx, "ws_test", "key_1", "run_b", "pipe_1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !isNew || got != "run_b" {
		t.Errorf("after Forget expected fresh reservation; got %s isNew=%v", got, isNew)
	}
}

func TestIdempotency_LookupOrReserve_ValidatesArgs(t *testing.T) {
	db := openIdempotencyTestDB(t)
	defer db.Close()
	store := NewIdempotencyStore(db)
	ctx := context.Background()

	cases := []struct{ ws, key, run string }{
		{"", "k", "r"},
		{"ws", "", "r"},
		{"ws", "k", ""},
	}
	for _, c := range cases {
		if _, _, err := store.LookupOrReserve(ctx, c.ws, c.key, c.run, "pipe", time.Hour); err == nil {
			t.Errorf("expected error for missing field: %+v", c)
		}
	}
}
