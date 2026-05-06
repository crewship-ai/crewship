package journal

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// TestLookupWorkspaceForCrew covers the three branches the helper has
// to distinguish — found, missing, and the soft-skip cases (empty
// crewID and nil DB) the file-watcher hot path relies on to never
// crash a non-yet-wired startup.
func TestLookupWorkspaceForCrew(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE crews (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL)`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id) VALUES ('crew-A', 'ws-1')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	t.Run("found", func(t *testing.T) {
		ws, err := LookupWorkspaceForCrew(context.Background(), db, "crew-A")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if ws != "ws-1" {
			t.Errorf("ws = %q, want ws-1", ws)
		}
	})

	t.Run("missing crew returns empty + nil err (soft-skip)", func(t *testing.T) {
		ws, err := LookupWorkspaceForCrew(context.Background(), db, "ghost")
		if err != nil {
			t.Errorf("err = %v, want nil", err)
		}
		if ws != "" {
			t.Errorf("ws = %q, want empty", ws)
		}
	})

	t.Run("empty crewID short-circuits", func(t *testing.T) {
		ws, err := LookupWorkspaceForCrew(context.Background(), db, "")
		if err != nil {
			t.Errorf("err = %v, want nil", err)
		}
		if ws != "" {
			t.Errorf("ws = %q, want empty", ws)
		}
	})

	t.Run("nil db short-circuits", func(t *testing.T) {
		ws, err := LookupWorkspaceForCrew(context.Background(), nil, "crew-A")
		if err != nil {
			t.Errorf("err = %v, want nil", err)
		}
		if ws != "" {
			t.Errorf("ws = %q, want empty", ws)
		}
	})

	t.Run("query error wrapped with context", func(t *testing.T) {
		// Drop the table to force a real SQL error (not ErrNoRows).
		closed, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		closed.Close()
		_, err = LookupWorkspaceForCrew(context.Background(), closed, "crew-A")
		if err == nil {
			t.Fatal("want non-nil err on closed DB")
		}
		if !contains(err.Error(), "lookup workspace for crew") {
			t.Errorf("err missing wrap context: %v", err)
		}
	})
}

func TestWriter_DB(t *testing.T) {
	if (*Writer)(nil).DB() != nil {
		t.Error("nil receiver should return nil DB")
	}

	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{})
	defer w.Close()
	if w.DB() != db {
		t.Error("DB() should return the *sql.DB passed to NewWriter")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
