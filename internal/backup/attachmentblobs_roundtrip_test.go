package backup_test

// Attachment blobs must ride the bundle. Before attachmentblobs.go existed a
// workspace bundle carried every `attachments` row and none of the files those
// rows name — the files live under <root>/attachments/<workspace>/<sha[:2]>/<sha>
// on the host, outside the DB and outside every crew container. A restore then
// landed rows whose download 404s, and nothing in the bundle, the manifest or
// the restore report said so.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

// seedAttachment writes one issue attachment (row + blob) the way
// internal/api's storeAttachmentBlob does, and returns its sha256.
func seedAttachment(t *testing.T, db *sql.DB, root, workspaceID, id, content string) string {
	t.Helper()
	ctx := context.Background()
	sum := sha256.Sum256([]byte(content))
	sha := hex.EncodeToString(sum[:])
	missionID := "m_" + id
	if _, err := db.ExecContext(ctx, `INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,title,status,mission_type)
		VALUES(?,?,'c_alpha','a_alice',?,'With a file','TODO','issue')`, missionID, workspaceID, "trace-"+id); err != nil {
		t.Fatalf("seed mission: %v", err)
	}
	key := "attachments/" + workspaceID + "/" + sha[:2] + "/" + sha
	if _, err := db.ExecContext(ctx, `INSERT INTO attachments
		(id, workspace_id, owner_type, mission_id, filename, content_type, size_bytes, sha256, storage_key, uploaded_by_user_id)
		VALUES (?, ?, 'issue', ?, 'notes.txt', 'text/plain', ?, ?, ?, 'u_admin')`,
		id, workspaceID, missionID, len(content), sha, key); err != nil {
		t.Fatalf("seed attachment: %v", err)
	}
	if root != "" {
		p := filepath.Join(root, "attachments", workspaceID, sha[:2], sha)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return sha
}

func TestBackupRestore_AttachmentBlobsRoundTrip(t *testing.T) {
	ctx := context.Background()
	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)
	sourceRoot := filepath.Join(t.TempDir(), "source-storage")
	const content = "the deploy log the issue was about"
	sha := seedAttachment(t, source, sourceRoot, workspaceID, "att_1", content)

	const passphrase = "attachment-roundtrip-passphrase-123"
	created, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:          backup.ScopeWorkspace,
		WorkspaceID:    workspaceID,
		OutputDir:      t.TempDir(),
		Actor:          backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		Passphrase:     passphrase,
		AttachmentRoot: sourceRoot,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	if got := created.Manifest.Contents.AttachmentsIncluded; got != 1 {
		t.Errorf("manifest attachments_included = %d, want 1", got)
	}
	if got := created.Manifest.Contents.AttachmentsMissing; got != 0 {
		t.Errorf("manifest attachments_missing = %d, want 0", got)
	}
	if len(created.Manifest.Contents.Incomplete) != 0 {
		t.Errorf("manifest incomplete = %+v, want none", created.Manifest.Contents.Incomplete)
	}
	// Disaster: the source storage is gone.
	if err := os.RemoveAll(sourceRoot); err != nil {
		t.Fatal(err)
	}

	target := openMigratedDB(t)
	targetRoot := filepath.Join(t.TempDir(), "target-storage")
	res, err := backup.RestoreBackup(ctx, target, backup.RestoreOptions{
		Path:           created.Path,
		Passphrase:     passphrase,
		Actor:          backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		AttachmentRoot: targetRoot,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(targetRoot, "attachments", workspaceID, sha[:2], sha))
	if err != nil {
		t.Fatalf("attachment blob not restored: %v", err)
	}
	if string(got) != content {
		t.Fatalf("restored blob = %q, want %q", got, content)
	}
	if res.AttachmentsRestored != 1 || res.AttachmentsMissing != 0 {
		t.Errorf("restore report attachments restored=%d missing=%d, want 1/0", res.AttachmentsRestored, res.AttachmentsMissing)
	}
}

// A fork (--as-workspace) regenerates the workspace id, and downloads resolve
// the blob from (workspace_id, sha256) — so the blob must land under the NEW id.
func TestBackupRestore_AttachmentBlobsLandUnderForkedWorkspace(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDB(t)
	workspaceID := seedWorkspace(t, db)
	root := filepath.Join(t.TempDir(), "storage")
	const content = "forked attachment"
	sha := seedAttachment(t, db, root, workspaceID, "att_f", content)

	created, err := backup.CreateBackup(ctx, db, backup.CreateOptions{
		Scope: backup.ScopeWorkspace, WorkspaceID: workspaceID, OutputDir: t.TempDir(),
		Actor:      backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		Passphrase: "fork-attachment-passphrase-123", AttachmentRoot: root,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	res, err := backup.RestoreBackup(ctx, db, backup.RestoreOptions{
		Path: created.Path, Passphrase: "fork-attachment-passphrase-123", AsWorkspace: "e2e-fork",
		Actor:          backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		AttachmentRoot: root,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if res.RestoredWorkspaceID == "" || res.RestoredWorkspaceID == workspaceID {
		t.Fatalf("fork kept the source id %q", res.RestoredWorkspaceID)
	}
	got, err := os.ReadFile(filepath.Join(root, "attachments", res.RestoredWorkspaceID, sha[:2], sha))
	if err != nil {
		t.Fatalf("blob not under the forked workspace: %v", err)
	}
	if string(got) != content {
		t.Fatalf("forked blob = %q", got)
	}
}

// A row whose blob is gone at create time is recorded, never silent.
func TestCreateBackup_MissingAttachmentBlobIsRecorded(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDB(t)
	workspaceID := seedWorkspace(t, db)
	root := filepath.Join(t.TempDir(), "storage")
	seedAttachment(t, db, root, workspaceID, "att_ok", "present")
	seedAttachment(t, db, "", workspaceID, "att_gone", "never written to disk")

	created, err := backup.CreateBackup(ctx, db, backup.CreateOptions{
		Scope: backup.ScopeWorkspace, WorkspaceID: workspaceID, OutputDir: t.TempDir(),
		Actor:      backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		Passphrase: "missing-attachment-passphrase-1", AttachmentRoot: root,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	c := created.Manifest.Contents
	if c.AttachmentsIncluded != 1 || c.AttachmentsMissing != 1 {
		t.Fatalf("included=%d missing=%d, want 1/1", c.AttachmentsIncluded, c.AttachmentsMissing)
	}
	var found bool
	for _, it := range c.Incomplete {
		if it.Kind == backup.IncompleteAttachmentMissing && it.Count == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("incomplete = %+v, want an attachment_missing item with count 1", c.Incomplete)
	}

	// The restore report says the same thing again.
	target := openMigratedDB(t)
	res, err := backup.RestoreBackup(ctx, target, backup.RestoreOptions{
		Path: created.Path, Passphrase: "missing-attachment-passphrase-1",
		Actor:          backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		AttachmentRoot: filepath.Join(t.TempDir(), "target"),
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if res.AttachmentsRestored != 1 || res.AttachmentsMissing != 1 {
		t.Fatalf("restore restored=%d missing=%d, want 1/1", res.AttachmentsRestored, res.AttachmentsMissing)
	}
}

// Restore never overwrites a different file already sitting at the path.
func TestRestore_AttachmentBlobNeverOverwritesADifferentFile(t *testing.T) {
	ctx := context.Background()
	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)
	root := filepath.Join(t.TempDir(), "storage")
	sha := seedAttachment(t, source, root, workspaceID, "att_c", "the real bytes")
	created, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope: backup.ScopeWorkspace, WorkspaceID: workspaceID, OutputDir: t.TempDir(),
		Actor:      backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		Passphrase: "conflict-attachment-passphrase", AttachmentRoot: root,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	target := openMigratedDB(t)
	targetRoot := filepath.Join(t.TempDir(), "target")
	dst := filepath.Join(targetRoot, "attachments", workspaceID, sha[:2], sha)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("something else"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := backup.RestoreBackup(ctx, target, backup.RestoreOptions{
		Path: created.Path, Passphrase: "conflict-attachment-passphrase",
		Actor:          backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		AttachmentRoot: targetRoot,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "something else" {
		t.Fatalf("restore overwrote a different file: %q", got)
	}
	if res.AttachmentsConflicts != 1 {
		t.Fatalf("attachments_conflicts = %d, want 1", res.AttachmentsConflicts)
	}
}
