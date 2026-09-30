package backup_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

// A custom bundle carries only its categories. Memory-only (agents kept as the
// dependency memory needs): the crews and agents rows and the memory tables
// ride; chats, issues and attachment files do not — and a file the bundle
// never meant to carry is not reported missing either.
func TestCreateBackup_CustomCategoriesFilterTheBundle(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDB(t)
	workspaceID := seedWorkspace(t, db)
	root := filepath.Join(t.TempDir(), "storage")
	seedAttachment(t, db, root, workspaceID, "att_1", "a file a memory backup leaves out")

	var phases []string
	created, err := backup.CreateBackup(ctx, db, backup.CreateOptions{
		Scope: backup.ScopeWorkspace, WorkspaceID: workspaceID, OutputDir: t.TempDir(),
		Actor:      backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		Passphrase: "custom-contents-passphrase-123", AttachmentRoot: root,
		Categories: []string{backup.CategoryMemory, backup.CategoryAgents},
		Progress:   func(p string) { phases = append(phases, p) },
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	m := created.Manifest
	if m.Kind != backup.KindCustom || !reflect.DeepEqual(m.Categories, []string{"agents", "memory"}) {
		t.Fatalf("manifest kind=%q categories=%v, want custom [agents memory]", m.Kind, m.Categories)
	}
	counts := m.Contents.TableRowCounts
	for _, want := range []string{"workspaces", "crews", "agents", "memory_versions"} {
		if _, ok := counts[want]; !ok {
			t.Errorf("memory-only bundle is missing table %s (counts %v)", want, counts)
		}
	}
	for _, gone := range []string{"attachments", "missions", "chats", "journal_entries", "credentials"} {
		if _, ok := counts[gone]; ok {
			t.Errorf("memory-only bundle carries table %s", gone)
		}
	}
	if m.Contents.AttachmentsIncluded != 0 || m.Contents.AttachmentsMissing != 0 || len(m.Contents.Incomplete) != 0 {
		t.Fatalf("attachments included=%d missing=%d incomplete=%+v; a memory-only bundle neither carries nor misses files",
			m.Contents.AttachmentsIncluded, m.Contents.AttachmentsMissing, m.Contents.Incomplete)
	}
	if want := []string{"copy", "pack", "encrypt", "write"}; !reflect.DeepEqual(phases, want) {
		t.Fatalf("progress phases %v, want %v", phases, want)
	}
	if e := backup.CatalogEntryFromResult(created, m); e.Kind != backup.KindCustom {
		t.Fatalf("catalog kind = %q, want custom", e.Kind)
	}

	// --replace would wipe everything the bundle does not hold.
	_, err = backup.RestoreBackup(ctx, db, backup.RestoreOptions{
		Path: created.Path, Passphrase: "custom-contents-passphrase-123", Replace: true,
		Actor: backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
	})
	if !errors.Is(err, backup.ErrInvalidScope) {
		t.Fatalf("replace from a custom bundle: err = %v, want ErrInvalidScope", err)
	}
}

func TestCreateBackup_UnknownCategoryIsRefused(t *testing.T) {
	db := openMigratedDB(t)
	workspaceID := seedWorkspace(t, db)
	_, err := backup.CreateBackup(context.Background(), db, backup.CreateOptions{
		Scope: backup.ScopeWorkspace, WorkspaceID: workspaceID, OutputDir: t.TempDir(),
		Actor:      backup.Actor{UserID: "u_admin", Role: "ADMIN"},
		Passphrase: "unknown-category-passphrase-1", Categories: []string{"everything"},
	})
	if err == nil {
		t.Fatal("unknown category accepted")
	}
}
