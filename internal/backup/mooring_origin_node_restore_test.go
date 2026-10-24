package backup_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestMooringOriginNodeAnchorPortableReplaceRetainsAndCloses(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[rollback], func(t *testing.T) {
			source := openMigratedDB(t)
			seedOriginRestore(t, source, "source-lineage")
			db := openMigratedDB(t)
			seedOriginRestore(t, db, "target-lineage")
			digest := "sha256:" + strings.Repeat("a", 64)
			if _, err := db.Exec(`INSERT INTO mooring_collab_checkpoints(page_id,workspace_id,revision,checkpoint_base64,decoded_bytes,checkpoint_generation) VALUES('target-only-origin-page','origin-backup-ws','0','e30=',2,'1')`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO mooring_origin_node_anchors(page_id,workspace_id,application_id,lineage_id,scope_hash,journal_id,checkpoint_revision,checkpoint_digest,creator_generation,original_runtime_ref,original_owner_token_hash) VALUES('target-only-origin-page','origin-backup-ws','wordcraft','target-lineage',?,?,'0',?,'1','synthetic-runtime',zeroblob(32))`, strings.Repeat("a", 64), strings.Repeat("b", 64), digest); err != nil {
				t.Fatal(err)
			}
			dump, err := backup.DumpWorkspace(t.Context(), source, "origin-backup-ws")
			if err != nil {
				t.Fatal(err)
			}
			if _, found := dump.Tables["mooring_origin_node_anchors"]; found {
				t.Fatal("portable dump carried local node anchor")
			}
			// Even a malicious explicit excluded table must not reopen/relabel the target.
			dump.Tables["mooring_origin_node_anchors"] = []map[string]any{{"page_id": "origin-backup-page", "closed": int64(0), "lineage_id": "forged"}}
			refused := errors.New("node portable rollback")
			hooks := &backup.RestoreDumpHooks{PreInsert: func(ctx context.Context, tx *sql.Tx) error {
				deleted, e := backup.ReplaceWorkspaceContents(ctx, tx, "origin-backup-ws", "origin-backup")
				if deleted["mooring_origin_node_anchors"] != 0 {
					return errors.New("anchor deleted")
				}
				return e
			}}
			if rollback {
				hooks.PreCommit = func(context.Context) error { return refused }
			}
			_, err = backup.RestoreDumpTxHooks(t.Context(), db, dump, hooks)
			if rollback && !errors.Is(err, refused) || !rollback && err != nil {
				t.Fatal(err)
			}
			var closed int
			var lineage string
			if err = db.QueryRow(`SELECT closed,lineage_id FROM mooring_origin_node_anchors`).Scan(&closed, &lineage); err != nil {
				t.Fatal(err)
			}
			want := 1
			if rollback {
				want = 0
			}
			if closed != want || lineage != "target-lineage" {
				t.Fatal("anchor replaced/reopened", closed, lineage)
			}
			var marker string
			page, expected := "origin-backup-page", "source-lineage"
			if rollback {
				page, expected = "target-only-origin-page", "target-lineage"
			}
			if err = db.QueryRow(`SELECT origin_lineage_id FROM mooring_collab_documents WHERE page_id=?`, page).Scan(&marker); err != nil || marker != expected {
				t.Fatal(marker, err)
			}
			// SQL rollback honestly restores the old DATA state. A real restore manager
			// must irreversibly retire the independent local journal BEFORE SQL mutation.
		})
	}
}
