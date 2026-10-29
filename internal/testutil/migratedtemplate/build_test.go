package migratedtemplate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildRefusesExistingDatabaseAndSidecarsWithoutChangingThem(t *testing.T) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		t.Run("existing"+suffix, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fixture.db")
			marker := []byte("preserve existing bytes")
			if err := os.WriteFile(path+suffix, marker, 0600); err != nil {
				t.Fatal(err)
			}
			if Build(t.Context(), path) == nil {
				t.Fatal("existing database/sidecar accepted")
			}
			raw, err := os.ReadFile(path + suffix)
			if err != nil || string(raw) != string(marker) {
				t.Fatal("existing data changed")
			}
		})
	}
}

func TestBuildCanceledMigrationLeavesNoPartialFixture(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.db")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if Build(ctx, path) == nil {
		t.Fatal("canceled migration acknowledged")
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, err := os.Lstat(path + suffix); !os.IsNotExist(err) {
			t.Fatalf("partial fixture remains %s: %v", suffix, err)
		}
	}
}
