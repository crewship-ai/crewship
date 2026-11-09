package backup_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"filippo.io/age"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
)

func documentDatabaseIdentity(t *testing.T, db *sql.DB) string {
	t.Helper()
	var value string
	if err := db.QueryRow(`SELECT value FROM document_database_identity WHERE id=1`).Scan(&value); err != nil || len(value) != 32 {
		t.Fatalf("document database identity %q: %v", value, err)
	}
	return value
}

func documentContentGeneration(t *testing.T, db *sql.DB, ws string) int64 {
	t.Helper()
	var generation int64
	if err := db.QueryRow(`SELECT content_generation FROM documents WHERE workspace_id=?`, ws).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	return generation
}

// A backup restore rewinds content, so it must rotate the database identity
// the public Docs epoch is derived from (#5163): browsers then never replay
// pending edits onto restored content. Nothing else rotates it.
func TestDocumentBackupRestoreRotatesDatabaseIdentity(t *testing.T) {
	source := openMigratedDB(t)
	ws := seedWorkspace(t, source)
	seedBackupDocument(t, source, ws, "lineage-document", "u_admin")
	if _, err := source.Exec(`UPDATE documents SET content_generation=3 WHERE workspace_id=?`, ws); err != nil {
		t.Fatal(err)
	}
	sourceIdentity := documentDatabaseIdentity(t, source)
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	bundle, err := backup.CreateBackup(t.Context(), source, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Passphrase: "document-lineage-passphrase", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	if documentDatabaseIdentity(t, source) != sourceIdentity {
		t.Fatal("creating a backup rotated the database identity")
	}
	dump, err := backup.DumpWorkspace(t.Context(), source, ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := dump.Tables["document_database_identity"]; ok {
		t.Fatal("installation identity in workspace dump")
	}

	t.Run("bundle restore", func(t *testing.T) {
		target := openMigratedDB(t)
		before := documentDatabaseIdentity(t, target)
		if _, err := backup.RestoreBackup(t.Context(), target, backup.RestoreOptions{Path: bundle.Path, Passphrase: "document-lineage-passphrase", Actor: actor}); err != nil {
			t.Fatal(err)
		}
		after := documentDatabaseIdentity(t, target)
		if after == before || after == sourceIdentity {
			t.Fatalf("restore kept a database identity: before=%s source=%s after=%s", before, sourceIdentity, after)
		}
		if got := documentContentGeneration(t, target, ws); got != 4 {
			t.Fatalf("restored content generation=%d want 4", got)
		}
	})
	t.Run("in-place restore over existing document", func(t *testing.T) {
		if err := backup.RestoreDump(t.Context(), source, dump); err != nil {
			t.Fatal(err)
		}
		if documentDatabaseIdentity(t, source) == sourceIdentity {
			t.Fatal("in-place restore kept the database identity")
		}
		if got := documentContentGeneration(t, source, ws); got != 4 {
			t.Fatalf("in-place content generation=%d want 4", got)
		}
	})
	t.Run("failed restore keeps identity", func(t *testing.T) {
		current := documentDatabaseIdentity(t, source)
		broken, err := backup.DumpWorkspace(t.Context(), source, ws)
		if err != nil {
			t.Fatal(err)
		}
		broken.Tables["document_updates"][0]["payload"] = map[string]any{"encoding": "document-base64-v1", "data": "%%%"}
		if err := backup.RestoreDump(t.Context(), source, broken); err == nil {
			t.Fatal("corrupt binary accepted")
		}
		if documentDatabaseIdentity(t, source) != current {
			t.Fatal("failed restore rotated the database identity")
		}
	})
}

func TestDocumentInstanceRecoveryRotatesDatabaseIdentity(t *testing.T) {
	f := newInstanceFixture(t)
	seedBackupDocument(t, f.db, "ws_e2e_1", "instance-lineage-document", "u_admin")
	sourceIdentity := documentDatabaseIdentity(t, f.db)
	bundle := f.create(t, true)
	dir := filepath.Join(t.TempDir(), "recovered")
	if _, err := backup.RecoverInstance(t.Context(), backup.RecoverOptions{BundlePath: bundle.Path, Identities: []age.Identity{f.identity}, DataDir: dir, Actor: "u_admin"}); err != nil {
		t.Fatal(err)
	}
	target, err := database.Open("file:" + filepath.Join(dir, "crewship.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if documentDatabaseIdentity(t, target.DB) == sourceIdentity {
		t.Fatal("instance recovery kept the source database identity")
	}
}
