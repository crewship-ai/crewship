package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReindexMetadataFailureRollsBackChunksAndDoesNotCacheNewHash(t *testing.T) {
	for _, incremental := range []bool{false, true} {
		dir, e := setupTestMemory(t)
		name := filepath.Join(dir, "AGENT.md")
		if err := os.WriteFile(name, []byte("originalword"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := e.Reindex(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte("replacementword"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := e.db.Exec(`CREATE TRIGGER fail_index_meta BEFORE INSERT ON memory_meta BEGIN SELECT RAISE(ABORT,'fixture index unavailable'); END`); err != nil {
			t.Fatal(err)
		}
		reindex := func() error {
			if incremental {
				_, err := e.ReindexPath(t.Context(), "AGENT.md")
				return err
			}
			return e.Reindex()
		}
		if err := reindex(); err == nil || !strings.Contains(err.Error(), "fixture index unavailable") {
			t.Fatalf("metadata failure hidden: %v", err)
		}
		var stored string
		if err := e.db.QueryRow(`SELECT content FROM memory_chunks WHERE file='AGENT.md'`).Scan(&stored); err != nil || stored != "originalword" {
			t.Fatalf("rollback lost original index: %q %v", stored, err)
		}
		if _, err := e.db.Exec(`DROP TRIGGER fail_index_meta`); err != nil {
			t.Fatal(err)
		}
		if err := reindex(); err != nil {
			t.Fatal(err)
		}
		if err := e.db.QueryRow(`SELECT content FROM memory_chunks WHERE file='AGENT.md'`).Scan(&stored); err != nil || stored != "replacementword" {
			t.Fatalf("failed transaction poisoned hash cache: %q %v", stored, err)
		}
	}
}

func TestMemoryIndexStorageErrorsAreNotEmptySuccessfulResults(t *testing.T) {
	dir, e := setupTestMemory(t)
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("new word"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := e.Reindex(); err == nil {
		t.Fatal("closed index rebuilt")
	}
	if _, err := e.ReindexPath(t.Context(), "AGENT.md"); err == nil {
		t.Fatal("closed index accepted existing path")
	}
	if _, err := e.ReindexPath(t.Context(), "missing.md"); err == nil {
		t.Fatal("closed index accepted deletion")
	}
	if _, err := e.Status(t.Context()); err == nil {
		t.Fatal("closed index reported status")
	}
	if err := initSchema(e.db); err == nil {
		t.Fatal("closed database initialized schema")
	}
	if _, err := storedChunksDDL(e.db); err == nil {
		t.Fatal("closed database treated schema as absent")
	}
	if err := rebuildChunksSchema(e.db); err == nil {
		t.Fatal("closed database rebuilt schema")
	}
}

func TestWorkspaceMemoryRejectsInvalidAndUnavailableStorage(t *testing.T) {
	dir := t.TempDir()
	for _, path := range []string{"", "relative", dir + "/../memory"} {
		if engine, err := NewWorkspaceMemory(path); err == nil || engine != nil {
			t.Fatalf("invalid workspace root accepted: %s %v", path, err)
		}
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	if engine, err := NewWorkspaceMemory(file); err == nil || engine != nil {
		t.Fatalf("file workspace root accepted: %v", err)
	}
	root := filepath.Join(dir, "memory")
	if err := os.MkdirAll(filepath.Join(root, "index.sqlite"), 0700); err != nil {
		t.Fatal(err)
	}
	if engine, err := NewWorkspaceMemory(root); err == nil || engine != nil {
		t.Fatalf("directory database accepted: %v", err)
	}
}
