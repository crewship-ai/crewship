package backup_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"filippo.io/age"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/documents"
)

var documentSnapshot = []byte{0, 255, 128, 192, 1, 237, 160, 128, 0}
var documentUpdate = []byte{255, 0, 254, 192, 128, 17}
var documentProjection = []byte(`{"type":"doc","content":[]}`)

func seedBackupDocument(t *testing.T, db *sql.DB, ws, page, owner string) {
	t.Helper()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,?,?,?,?,'{}')`, []any{page, ws, page, page, owner}},
		{`INSERT INTO documents(workspace_id,page_id,schema_version,head_rev,epoch,snapshot,snapshot_rev,projection,writer,owner,lease_until) VALUES(?,?,1,2,41,?,1,?,'source-engine','source-owner',4102444800000)`, []any{ws, page, documentSnapshot, documentProjection}},
		{`INSERT INTO document_updates(workspace_id,page_id,rev,payload) VALUES(?,?,2,?)`, []any{ws, page, documentUpdate}},
		{`INSERT INTO document_receipts(workspace_id,page_id,id,digest,fingerprint,rev,epoch,actor,kind,created_at) VALUES(?,?,'original-request','request-digest','original-fingerprint',2,41,'user:historical-actor','human','2026-10-15T16:00:00.000Z')`, []any{ws, page}},
		{`INSERT INTO document_checkpoints(workspace_id,page_id,id,rev,schema_version,snapshot,projection,actor,created_at) VALUES(?,?,'original-checkpoint',2,1,?,?,'user:historical-actor','2026-10-15T16:00:00.000Z')`, []any{ws, page, documentSnapshot, documentProjection}},
	} {
		if _, err := db.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
}
func assertBackupDocument(t *testing.T, db *sql.DB, expected int) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM documents`).Scan(&count); err != nil || count != expected {
		t.Fatalf("documents=%d want %d: %v", count, expected, err)
	}
	rows, err := db.Query(`SELECT d.workspace_id,d.page_id,d.epoch,d.writer,d.owner,d.lease_until,d.snapshot,d.projection,u.payload,c.snapshot,c.projection,r.digest,r.epoch,r.actor,r.kind,r.rev,c.rev,r.fingerprint,c.actor,r.created_at,c.created_at FROM documents d JOIN document_updates u ON u.workspace_id=d.workspace_id AND u.page_id=d.page_id JOIN document_checkpoints c ON c.workspace_id=d.workspace_id AND c.page_id=d.page_id JOIN document_receipts r ON r.workspace_id=d.workspace_id AND r.page_id=d.page_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	var restoredKeys []documents.Key
	for rows.Next() {
		var epoch, lease, receiptEpoch, rev, checkpointRev int64
		var workspace, page, writer, owner, digest, actor, kind, fingerprint, checkpointActor, receiptCreated, checkpointCreated string
		var snapshot, projection, update, checkpoint, checkpointProjection []byte
		if err := rows.Scan(&workspace, &page, &epoch, &writer, &owner, &lease, &snapshot, &projection, &update, &checkpoint, &checkpointProjection, &digest, &receiptEpoch, &actor, &kind, &rev, &checkpointRev, &fingerprint, &checkpointActor, &receiptCreated, &checkpointCreated); err != nil {
			t.Fatal(err)
		}
		if epoch != 42 || writer != "" || owner != "" || lease != 0 {
			t.Fatalf("restored live lease/epoch: %d %q %q %d", epoch, writer, owner, lease)
		}
		if !bytes.Equal(snapshot, documentSnapshot) || !bytes.Equal(checkpoint, documentSnapshot) || !bytes.Equal(update, documentUpdate) || !bytes.Equal(projection, documentProjection) || !bytes.Equal(checkpointProjection, documentProjection) {
			t.Fatal("document binary bytes changed")
		}
		if digest != "request-digest" || receiptEpoch != 41 || actor != "user:historical-actor" || kind != "human" || rev != 2 || checkpointRev != 2 || fingerprint != "original-fingerprint" || checkpointActor != actor || receiptCreated != "2026-10-15T16:00:00.000Z" || checkpointCreated != receiptCreated {
			t.Fatal("receipt/checkpoint audit changed")
		}
		restoredKeys = append(restoredKeys, documents.Key{WorkspaceID: workspace, PageID: page})
		seen++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if seen != expected {
		t.Fatalf("joined document history=%d", seen)
	}
	rows.Close()
	store := documents.New(db, documents.Options{})
	authorize := func(context.Context, *sql.Tx) (string, error) { return "user:historical-actor", nil }
	for _, key := range restoredKeys {
		if _, err := store.Renew(t.Context(), key, documents.Lease{Writer: "source-engine", Owner: "source-owner", Epoch: 41}, authorize); !errors.Is(err, documents.ErrFence) {
			t.Fatalf("source lease revived after restore: %v", err)
		}
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM document_writer_fence WHERE id=1`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("runtime singleton=%d: %v", count, err)
	}
}
func TestDocumentBackupWorkspaceRoundtrip(t *testing.T) {
	source := openMigratedDB(t)
	ws := seedWorkspace(t, source)
	if _, err := source.Exec(`INSERT INTO workspaces(id,name,slug) VALUES('foreign-documents','Foreign','foreign-documents')`); err != nil {
		t.Fatal(err)
	}
	seedBackupDocument(t, source, ws, "document-page", "u_admin")
	seedBackupDocument(t, source, "foreign-documents", "foreign-document-page", "u_admin")
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	bundle, err := backup.CreateBackup(t.Context(), source, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Passphrase: "document-backup-roundtrip-passphrase", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	for _, fork := range []bool{false, true} {
		t.Run(map[bool]string{false: "same", true: "fork"}[fork], func(t *testing.T) {
			target := openMigratedDB(t)
			options := backup.RestoreOptions{Path: bundle.Path, Passphrase: "document-backup-roundtrip-passphrase", Actor: actor}
			if fork {
				options.AsWorkspace = "document-fork"
			}
			if _, err := backup.RestoreBackup(t.Context(), target, options); err != nil {
				t.Fatal(err)
			}
			assertBackupDocument(t, target, 1)
			var restoredWS, page string
			if err := target.QueryRow(`SELECT workspace_id,page_id FROM documents`).Scan(&restoredWS, &page); err != nil {
				t.Fatal(err)
			}
			if fork && (restoredWS == ws || page == "document-page") {
				t.Fatal("fork did not remap document scope")
			}
			if !fork && (restoredWS != ws || page != "document-page") {
				t.Fatal("ordinary restore changed scope")
			}
		})
	}
	// RestoreDump itself must fence, even outside the bundle runner, and must not
	// resurrect a lease when INSERT OR IGNORE finds an existing document.
	dump, err := backup.DumpWorkspace(t.Context(), source, ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := dump.Tables["document_writer_fence"]; ok {
		t.Fatal("runtime singleton in workspace dump")
	}
	if err := backup.RestoreDump(t.Context(), source, dump); err != nil {
		t.Fatal(err)
	}
	var epoch int
	var writer string
	if err := source.QueryRow(`SELECT epoch,writer FROM documents WHERE workspace_id=?`, ws).Scan(&epoch, &writer); err != nil || epoch != 42 || writer != "" {
		t.Fatalf("existing writer not fenced: %d %q %v", epoch, writer, err)
	}
	if err := source.QueryRow(`SELECT epoch,writer FROM documents WHERE workspace_id='foreign-documents'`).Scan(&epoch, &writer); err != nil || epoch != 41 || writer != "source-engine" {
		t.Fatal("restore changed foreign writer")
	}
}
func TestDocumentInstanceBackupRecovery(t *testing.T) {
	f := newInstanceFixture(t)
	seedBackupDocument(t, f.db, "ws_e2e_1", "instance-document", "u_admin")
	seedBackupDocument(t, f.db, "ws_lab", "foreign-instance-document", "u_lab")
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
	assertBackupDocument(t, target.DB, 2)
}

func TestDocumentRestoreCorruptBinaryRollsBackFence(t *testing.T) {
	source := openMigratedDB(t)
	workspace := seedWorkspace(t, source)
	seedBackupDocument(t, source, workspace, "corrupt-document", "u_admin")
	dump, err := backup.DumpWorkspace(t.Context(), source, workspace)
	if err != nil {
		t.Fatal(err)
	}
	dump.Tables["document_updates"][0]["payload"] = map[string]any{"encoding": "document-base64-v1", "data": "%%%"}
	if err := backup.RestoreDump(t.Context(), source, dump); err == nil {
		t.Fatal("corrupt binary accepted")
	}
	var epoch, lease int64
	var writer, owner string
	if err := source.QueryRow(`SELECT epoch,writer,owner,lease_until FROM documents WHERE workspace_id=?`, workspace).Scan(&epoch, &writer, &owner, &lease); err != nil {
		t.Fatal(err)
	}
	if epoch != 41 || writer != "source-engine" || owner != "source-owner" || lease != 4102444800000 {
		t.Fatal("failed restore changed live authority")
	}
}
