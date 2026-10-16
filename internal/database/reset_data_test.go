package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func TestResetApplicationDataRetainsIdentityAndCleanMigrationDefaults(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	root := t.TempDir()
	current, err := Open("file:" + filepath.Join(root, "current.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	clean, err := Open("file:" + filepath.Join(root, "clean.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, db := range []*DB{current, clean} {
		if err := Migrate(ctx, db.DB, logger); err != nil {
			t.Fatal(err)
		}
		if err := RunPostDeployMigrations(ctx, db.DB, logger); err != nil {
			t.Fatal(err)
		}
	}
	// Schema-preserving reset must restore default lookup/config rows as well as
	// removing user data. Compare every ordinary table to the clean template.
	if _, err := current.ExecContext(ctx, `INSERT INTO resource_cleanup_installation(id,db_nonce) VALUES(1,?)`, "identity-nonce"); err != nil {
		t.Fatal(err)
	}
	if _, err := current.ExecContext(ctx, `INSERT INTO users(id,email,full_name,hashed_password) VALUES('reset-user','reset@example.test','Reset','hash')`); err != nil {
		t.Fatal(err)
	}
	// Derived native text is application data, including a corrupt cache row.
	// Reset must remove it while restoring the cache writer singleton default.
	if _, err := current.ExecContext(ctx, `INSERT INTO workspaces(id,name,slug) VALUES('reset-search-workspace','Search','reset-search-workspace');
 INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('reset-search-page','reset-search-workspace','reset-search-page','Search','reset-user','{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := current.ExecContext(ctx, `INSERT INTO page_search_projections(workspace_id,page_id,adapter_version,source_type,source_schema,source_rev,source_generation,source_hash,encoded,encoded_sha256) VALUES('reset-search-workspace','reset-search-page',1,'document',2,0,0,?,CAST('derived text canary' AS BLOB),?)`, strings.Repeat("0", 64), strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	if err := clean.Close(); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := ResetApplicationData(ctx, current.DB, filepath.Join(root, "clean.db")); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := current.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE id='reset-user'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatal("application data retained")
		}
		if err := current.QueryRowContext(ctx, `SELECT count(*) FROM page_search_projections`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("derived search retained: %d %v", count, err)
		}
		if err := current.QueryRowContext(ctx, `SELECT count(*) FROM page_search_writer_fence WHERE id=1`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("cache writer default missing: %d %v", count, err)
		}
		var nonce string
		if err := current.QueryRowContext(ctx, `SELECT db_nonce FROM resource_cleanup_installation WHERE id=1`).Scan(&nonce); err != nil {
			t.Fatal(err)
		}
		if nonce != "identity-nonce" {
			t.Fatal("identity changed")
		}
		var migrations int
		if err := current.QueryRowContext(ctx, `SELECT count(*) FROM _migrations`).Scan(&migrations); err != nil {
			t.Fatal(err)
		}
		if migrations == 0 {
			t.Fatal("migration ledger lost")
		}
		if err := Migrate(ctx, current.DB, logger); err != nil {
			t.Fatal("reset DB cannot start", err)
		}
	}
}

func TestResetApplicationDataSchemaMismatchLeavesRows(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	current, err := Open("file:" + filepath.Join(root, "current.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	clean, err := Open("file:" + filepath.Join(root, "clean.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := current.ExecContext(ctx, `CREATE TABLE original(id TEXT); INSERT INTO original VALUES('keep')`); err != nil {
		t.Fatal(err)
	}
	if _, err := clean.ExecContext(ctx, `CREATE TABLE different(id TEXT)`); err != nil {
		t.Fatal(err)
	}
	clean.Close()
	if err := ResetApplicationData(ctx, current.DB, filepath.Join(root, "clean.db")); err == nil {
		t.Fatal("accepted mismatch")
	}
	var value string
	if err := current.QueryRowContext(ctx, `SELECT id FROM original`).Scan(&value); err != nil || value != "keep" {
		t.Fatal(value, err)
	}
}

func TestResetApplicationDataRebuildsExternalSearchAndSequences(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	original, err := Open("file:" + filepath.Join(root, "original.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	cleanPath := filepath.Join(root, "clean.db")
	clean, err := Open("file:" + cleanPath)
	if err != nil {
		t.Fatal(err)
	}
	schema := `CREATE TABLE documents(id INTEGER PRIMARY KEY AUTOINCREMENT, body TEXT);
 CREATE VIRTUAL TABLE documents_fts USING fts5(body,content='documents',content_rowid='id');
 CREATE TRIGGER documents_search AFTER INSERT ON documents BEGIN INSERT INTO documents_fts(rowid,body) VALUES(new.id,new.body); END;`
	for _, db := range []*DB{original, clean} {
		if _, err := db.ExecContext(ctx, schema); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := original.ExecContext(ctx, `INSERT INTO documents(id,body) VALUES(100,'staleresetword')`); err != nil {
		t.Fatal(err)
	}
	if _, err := clean.ExecContext(ctx, `INSERT INTO documents(body) VALUES('cleanbaseline')`); err != nil {
		t.Fatal(err)
	}
	clean.Close()
	for attempt := 0; attempt < 2; attempt++ {
		if err := ResetApplicationData(ctx, original.DB, cleanPath); err != nil {
			t.Fatal(err)
		}
		for token, want := range map[string]int{"staleresetword": 0, "cleanbaseline": 1} {
			var count int
			if err := original.QueryRowContext(ctx, `SELECT count(*) FROM documents_fts WHERE documents_fts MATCH ?`, token).Scan(&count); err != nil || count != want {
				t.Fatalf("search %s: count %d, error %v", token, count, err)
			}
		}
		if _, err := original.ExecContext(ctx, `INSERT INTO documents_fts(documents_fts,rank) VALUES('integrity-check',1)`); err != nil {
			t.Fatal("search integrity", err)
		}
		result, err := original.ExecContext(ctx, `INSERT INTO documents(body) VALUES('staleresetword')`)
		if err != nil {
			t.Fatal(err)
		}
		id, err := result.LastInsertId()
		if err != nil || id != 2 {
			t.Fatalf("sequence was not reset: %d %v", id, err)
		}
	}
}
