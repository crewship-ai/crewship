package backup_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/documents"
)

func TestDocumentEncryptedForkPreservesOperationIdentity(t *testing.T) {
	source := openMigratedDB(t)
	workspace := seedWorkspace(t, source)
	const page = "operation-document"
	if _, err := source.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,?,?,'Document','u_admin','{}')`, page, workspace, page); err != nil {
		t.Fatal(err)
	}
	denied := errors.New("current human no longer owns document")
	authorize := func(key documents.Key) documents.Authorize {
		return func(ctx context.Context, tx *sql.Tx) (string, error) {
			var owner string
			if err := tx.QueryRowContext(ctx, `SELECT owner_user_id FROM pages WHERE workspace_id=? AND id=?`, key.WorkspaceID, key.PageID).Scan(&owner); err != nil {
				return "", err
			}
			if owner != "u_admin" {
				return "", denied
			}
			return "user:u_admin", nil
		}
	}
	key := documents.Key{WorkspaceID: workspace, PageID: page}
	snapshot := []byte{0, 255, 128, 1}
	projection := []byte(`{"type":"doc","content":[]}`)
	const requestID = "original-operation"
	const fingerprint = "original-request-fingerprint"
	store := documents.New(source, documents.Options{})
	if _, err := store.InitRequest(t.Context(), key, requestID, fingerprint, 1, snapshot, projection, authorize(key)); err != nil {
		t.Fatal(err)
	}
	// C1 has no lease/checkpoint mutation API yet. Populate source runtime lease
	// fields and a saved history row directly, then exercise the real bundle path.
	if _, err := source.Exec(`UPDATE documents SET epoch=41,writer='source-engine',owner='source-owner',lease_until=4102444800000 WHERE workspace_id=? AND page_id=?`, workspace, page); err != nil {
		t.Fatal(err)
	}
	const checkpointID = "original-checkpoint"
	if _, err := source.Exec(`INSERT INTO document_checkpoints(workspace_id,page_id,id,rev,schema_version,snapshot,projection,actor) VALUES(?,?,?,0,1,?,?,'user:u_admin')`, workspace, page, checkpointID, snapshot, projection); err != nil {
		t.Fatal(err)
	}
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	const passphrase = "document-operation-fork-fixture"
	bundle, err := backup.CreateBackup(t.Context(), source, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: workspace, OutputDir: t.TempDir(), Passphrase: passphrase, Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	target := openMigratedDB(t)
	restored, err := backup.RestoreBackup(t.Context(), target, backup.RestoreOptions{Path: bundle.Path, Passphrase: passphrase, AsWorkspace: "operation-fork", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	var fork documents.Key
	var epoch, lease int64
	var writer, owner string
	if err := target.QueryRow(`SELECT workspace_id,page_id,epoch,writer,owner,lease_until FROM documents`).Scan(&fork.WorkspaceID, &fork.PageID, &epoch, &writer, &owner, &lease); err != nil {
		t.Fatal(err)
	}
	if fork.WorkspaceID == workspace || fork.WorkspaceID != restored.RestoredWorkspaceID || fork.PageID == page {
		t.Fatal("fork did not mint new workspace/Page anchors")
	}
	if epoch != 42 || writer != "" || owner != "" || lease != 0 {
		t.Fatal("fork restored source writer credentials or failed epoch fence")
	}
	for table, want := range map[string]string{"document_receipts": requestID, "document_checkpoints": checkpointID} {
		var id string
		if err := target.QueryRow(`SELECT id FROM `+table+` WHERE workspace_id=? AND page_id=?`, fork.WorkspaceID, fork.PageID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if id != want {
			t.Errorf("%s changed original scoped operation/history identity", table)
		}
	}
	forkStore := documents.New(target, documents.Options{})
	state, err := forkStore.InitRequest(t.Context(), fork, requestID, fingerprint, 1, snapshot, projection, authorize(fork))
	if err != nil {
		t.Errorf("currently authorized exact init replay after fork: %v", err)
	} else if state.HeadRev != 0 || state.Epoch != 42 || !bytes.Equal(state.Snapshot, snapshot) {
		t.Error("exact replay changed content or returned old epoch authority")
	}
	if _, err := forkStore.InitRequest(t.Context(), fork, requestID, fingerprint, 1, []byte("changed"), projection, authorize(fork)); !errors.Is(err, documents.ErrDuplicate) {
		t.Error("changed payload did not conflict with original operation identity")
	}
	if _, err := target.Exec(`INSERT INTO users(id,email) VALUES('successor','successor@example.test')`); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec(`UPDATE pages SET owner_user_id='successor' WHERE workspace_id=? AND id=?`, fork.WorkspaceID, fork.PageID); err != nil {
		t.Fatal(err)
	}
	if _, err := forkStore.InitRequest(t.Context(), fork, requestID, fingerprint, 1, snapshot, projection, authorize(fork)); !errors.Is(err, denied) {
		t.Error("preserved receipt bypassed current authorization")
	}
	for _, table := range []string{"documents", "document_receipts", "document_checkpoints"} {
		var count int
		if err := target.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("replay changed %s row count: %d %v", table, count, err)
		}
	}
	var sourceEpoch int64
	if err := source.QueryRow(`SELECT epoch FROM documents WHERE workspace_id=? AND page_id=?`, workspace, page).Scan(&sourceEpoch); err != nil || sourceEpoch != 41 {
		t.Fatal("fork changed source runtime authority")
	}
}
