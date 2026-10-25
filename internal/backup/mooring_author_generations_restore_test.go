package backup_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

func seedGenerationRestoreFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO users(id,email) VALUES('generation-backup-owner','generation-owner@synthetic.invalid'),('generation-backup-peer','generation-peer@synthetic.invalid')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('generation-backup-ws','Generation backup','generation-backup')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('generation-backup-owner-member','generation-backup-ws','generation-backup-owner','OWNER'),('generation-backup-peer-member','generation-backup-ws','generation-backup-peer','MEMBER')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('generation-backup-crew','generation-backup-ws','Crew','crew')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('generation-backup-agent','generation-backup-ws','generation-backup-crew','Agent','agent')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('generation-backup-page','generation-backup-ws','page','Page','generation-backup-owner','{}')`,
		`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('generation-backup-page','user','generation-backup-peer','write','generation-backup-owner')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	// Synthetic fresh-admission tracking; real capture is a separate API change.
	for _, k := range [][2]string{{"user", "generation-backup-owner"}, {"user", "generation-backup-peer"}, {"page", "generation-backup-page"}, {"agent", "generation-backup-agent"}, {"app", "generation-backup-crew"}} {
		if _, err := db.Exec(`INSERT INTO mooring_authority_generations(workspace_id,scope_kind,scope_id) VALUES('generation-backup-ws',?,?)`, k[0], k[1]); err != nil {
			t.Fatal(err)
		}
	}

}

func generationRestoreCounter(t *testing.T, db *sql.DB, kind, id string) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(`SELECT revision FROM mooring_authority_generations WHERE workspace_id='generation-backup-ws' AND scope_kind=? AND scope_id=?`, kind, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMooringAuthorGenerationsSurviveActualLogicalReplaceWithoutBundleReset(t *testing.T) {
	source := openMigratedDB(t)
	seedGenerationRestoreFixture(t, source)
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[rollback], func(t *testing.T) {
			target := openMigratedDB(t)
			seedGenerationRestoreFixture(t, target)
			for range 3 {
				if _, err := target.Exec(`DELETE FROM page_grants WHERE subject_id='generation-backup-peer'; INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('generation-backup-page','user','generation-backup-peer','write','generation-backup-owner')`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := target.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('generation-backup-deleted','generation-backup-ws','deleted','Target-only page','generation-backup-owner','{}')`); err != nil {
				t.Fatal(err)
			}
			if _, err := target.Exec(`INSERT INTO mooring_authority_generations(workspace_id,scope_kind,scope_id) VALUES('generation-backup-ws','page','generation-backup-deleted')`); err != nil {
				t.Fatal(err)
			}
			peerBefore := generationRestoreCounter(t, target, "user", "generation-backup-peer")
			pageBefore := generationRestoreCounter(t, target, "page", "generation-backup-page")
			tombstoneBefore := generationRestoreCounter(t, target, "page", "generation-backup-deleted")
			dump, err := backup.DumpWorkspace(t.Context(), source, "generation-backup-ws")
			if err != nil {
				t.Fatal(err)
			}
			if _, exists := dump.Tables["mooring_authority_generations"]; exists {
				t.Fatal("portable dump carried target-local generations")
			}
			// An old or hostile bundle cannot lower target generations even when it
			// explicitly supplies the otherwise excluded table.
			dump.Tables["mooring_authority_generations"] = []map[string]any{{"workspace_id": "generation-backup-ws", "scope_kind": "user", "scope_id": "generation-backup-peer", "revision": int64(1)}}
			sentinel := errors.New("synthetic-generation-precommit-failure")
			hooks := &backup.RestoreDumpHooks{PreInsert: func(ctx context.Context, tx *sql.Tx) error {
				deleted, e := backup.ReplaceWorkspaceContents(ctx, tx, "generation-backup-ws", "generation-backup")
				if e != nil {
					return e
				}
				if deleted["mooring_authority_generations"] != 0 {
					return errors.New("replacement deleted generation tombstones")
				}
				return nil
			}}
			if rollback {
				hooks.PreCommit = func(context.Context) error { return sentinel }
			}
			_, err = backup.RestoreDumpTxHooks(t.Context(), target, dump, hooks)
			if rollback {
				if !errors.Is(err, sentinel) {
					t.Fatal("fixture did not fail actual precommit", err)
				}
				if generationRestoreCounter(t, target, "user", "generation-backup-peer") != peerBefore || generationRestoreCounter(t, target, "page", "generation-backup-page") != pageBefore || generationRestoreCounter(t, target, "page", "generation-backup-deleted") != tombstoneBefore {
					t.Fatal("rolled-back replacement changed generations")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if generationRestoreCounter(t, target, "user", "generation-backup-peer") <= peerBefore || generationRestoreCounter(t, target, "page", "generation-backup-page") <= pageBefore {
				t.Fatal("canonical replace/restore did not invalidate original generations")
			}
			if generationRestoreCounter(t, target, "page", "generation-backup-deleted") <= tombstoneBefore {
				t.Fatal("target-only Page tombstone was lost or unchanged")
			}
			var exists bool
			if err = target.QueryRow(`SELECT EXISTS(SELECT 1 FROM pages WHERE id='generation-backup-deleted')`).Scan(&exists); err != nil || exists {
				t.Fatal("target-only Page was not actually replaced", err)
			}
		})
	}
}
