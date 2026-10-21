package backup_test

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

// This tests encrypted durable identity transport, not Yjs materialization.
// Copy receipts are historical deduplication, never restored live authority.
func TestDocumentCopyReceiptEncryptedRoundTripAndFork(t *testing.T) {
	source := openMigratedDB(t)
	workspace := seedWorkspace(t, source)
	seedBackupDocument(t, source, workspace, "copy-source", "u_admin")
	seedBackupDocument(t, source, workspace, "copy-result", "u_admin")
	if _, err := source.Exec(`INSERT INTO document_copy_receipts(workspace_id,user_id,mutation_id,request_digest,source_page_id,result_page_id,result_slug,created_at) VALUES(?,'u_admin','copy-action',?,'copy-source','copy-result','copy-result','2026-10-21T11:30:25Z')`, workspace, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	bundle, err := backup.CreateBackup(t.Context(), source, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: workspace, OutputDir: t.TempDir(), Passphrase: "synthetic-copy-backup", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	for _, fork := range []bool{false, true} {
		t.Run(map[bool]string{false: "same", true: "fork"}[fork], func(t *testing.T) {
			target := openMigratedDB(t)
			options := backup.RestoreOptions{Path: bundle.Path, Passphrase: "synthetic-copy-backup", Actor: actor}
			if fork {
				options.AsWorkspace = "copy-fork"
			}
			if _, err := backup.RestoreBackup(t.Context(), target, options); err != nil {
				t.Fatal(err)
			}
			var ws, original, result, user, sourceOwner, copyOwner, digest, key string
			err := target.QueryRow(`SELECT r.workspace_id,r.source_page_id,r.result_page_id,r.user_id,s.owner_user_id,p.owner_user_id,r.request_digest,r.mutation_id FROM document_copy_receipts r JOIN pages s ON s.id=r.source_page_id AND s.workspace_id=r.workspace_id JOIN pages p ON p.id=r.result_page_id AND p.workspace_id=r.workspace_id`).Scan(&ws, &original, &result, &user, &sourceOwner, &copyOwner, &digest, &key)
			if err != nil || user != sourceOwner || user != copyOwner || digest != strings.Repeat("a", 64) || key != "copy-action" {
				t.Fatalf("receipt provenance: %s %s %s %s %v", ws, original, result, user, err)
			}
			if fork {
				if ws == workspace || original == "copy-source" || result == "copy-result" {
					t.Fatal("fork retained source identities")
				}
			} else if ws != workspace || original != "copy-source" || result != "copy-result" {
				t.Fatal("recovery lost exact copy identities")
			}
			if !fork {
				if _, err := target.Exec(`INSERT INTO document_copy_receipts(workspace_id,user_id,mutation_id,request_digest,source_page_id,result_page_id,result_slug,created_at) VALUES(?,?,'after-backup',?,?,?,'copy-result','2026-10-21T12:00:00Z')`, ws, user, strings.Repeat("b", 64), original, result); err != nil {
					t.Fatal(err)
				}
				options.Replace = true
				if _, err := backup.RestoreBackup(t.Context(), target, options); err != nil {
					t.Fatalf("replace durable copy receipts: %v", err)
				}
				var count int
				if err := target.QueryRow(`SELECT COUNT(*) FROM document_copy_receipts WHERE workspace_id=?`, ws).Scan(&count); err != nil || count != 1 {
					t.Fatalf("replacement left incompatible future copy keys: %d %v", count, err)
				}
			}
			var live int
			if err := target.QueryRow(`SELECT COUNT(*) FROM documents WHERE writer<>'' OR owner<>'' OR lease_until<>0`).Scan(&live); err != nil || live != 0 {
				t.Fatalf("restored copy revived engine authority %d %v", live, err)
			}
		})
	}
}
