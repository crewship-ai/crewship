package database

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open("file:" + filepath.Join(t.TempDir(), "txn_test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Create a simple test table.
	if _, err := db.Exec("CREATE TABLE txn_test (id TEXT PRIMARY KEY, val TEXT)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	return db
}

func TestWithTx_Commit(t *testing.T) {
	db := openTestDB(t)

	err := WithTx(context.Background(), db.DB, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), "INSERT INTO txn_test (id, val) VALUES ('1', 'hello')")
		return err
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}

	var val string
	if err := db.QueryRow("SELECT val FROM txn_test WHERE id = '1'").Scan(&val); err != nil {
		t.Fatalf("query after commit: %v", err)
	}
	if val != "hello" {
		t.Errorf("val = %q, want %q", val, "hello")
	}
}

func TestWithTx_Rollback(t *testing.T) {
	db := openTestDB(t)
	sentinel := errors.New("deliberate error")

	err := WithTx(context.Background(), db.DB, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(context.Background(), "INSERT INTO txn_test (id, val) VALUES ('1', 'hello')"); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WithTx error = %v, want %v", err, sentinel)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM txn_test").Scan(&count); err != nil {
		t.Fatalf("query after rollback: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0 (rollback should discard insert)", count)
	}
}

func TestWithTxResult_Success(t *testing.T) {
	db := openTestDB(t)

	id, err := WithTxResult(context.Background(), db.DB, func(tx *sql.Tx) (string, error) {
		_, err := tx.ExecContext(context.Background(), "INSERT INTO txn_test (id, val) VALUES ('42', 'world')")
		if err != nil {
			return "", err
		}
		return "42", nil
	})
	if err != nil {
		t.Fatalf("WithTxResult: %v", err)
	}
	if id != "42" {
		t.Errorf("id = %q, want %q", id, "42")
	}

	var val string
	if err := db.QueryRow("SELECT val FROM txn_test WHERE id = '42'").Scan(&val); err != nil {
		t.Fatalf("query: %v", err)
	}
	if val != "world" {
		t.Errorf("val = %q, want %q", val, "world")
	}
}

func TestWithTxResult_Error(t *testing.T) {
	db := openTestDB(t)
	sentinel := errors.New("fail")

	result, err := WithTxResult(context.Background(), db.DB, func(tx *sql.Tx) (string, error) {
		if _, err := tx.ExecContext(context.Background(), "INSERT INTO txn_test (id, val) VALUES ('1', 'x')"); err != nil {
			return "", err
		}
		return "", sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
	if result != "" {
		t.Errorf("result = %q, want empty", result)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM txn_test").Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
}

func TestWithTx_CancelledContext(t *testing.T) {
	db := openTestDB(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := WithTx(ctx, db.DB, func(tx *sql.Tx) error {
		t.Fatal("fn should not be called with cancelled context")
		return nil
	})
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}
