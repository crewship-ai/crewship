package backup_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

// Fresh synthetic metadata only. These fixtures do not prove app inventory,
// source admission, issuance, physical recovery or a portable burned-ID union.
func seedOriginRestore(t *testing.T, db *sql.DB, lineage string) {
	t.Helper()
	page := "origin-backup-page"
	if lineage == "target-lineage" {
		page = "target-only-origin-page"
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, q := range []string{
		`INSERT INTO users(id,email) VALUES('origin-backup-owner','origin-backup@synthetic.invalid')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('origin-backup-ws','Origin','origin-backup')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('origin-backup-member','origin-backup-ws','origin-backup-owner','OWNER')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('origin-backup-page','origin-backup-ws','page','Origin','origin-backup-owner','{}')`,
	} {
		if _, e := db.Exec(strings.ReplaceAll(q, "origin-backup-page", page)); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := db.Exec(`INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64,origin_lineage_id) VALUES(?,'origin-backup-ws','wordcraft','0','e30=',?)`, page, lineage); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`INSERT INTO mooring_origin_documents(lineage_id,workspace_id,page_id,application_id,binding_version,initial_revision,initial_checkpoint_digest,initial_inventory_digest) VALUES(?,'origin-backup-ws',?,'wordcraft','mooring.origin-document.v1','0',?,?)`, lineage, page, digest, digest); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`INSERT INTO mooring_origin_exclusions(lineage_id,origin,kind,first_clock) VALUES(?,0,'baseline',1)`, lineage); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`INSERT INTO mooring_origin_exclusions(lineage_id,origin,kind,source_id) VALUES(?,1,'issued',?)`, lineage, lineage+"-source"); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`INSERT INTO mooring_ingress_sources(source_id,lineage_id,origin,kind,binding_version,author_binding_digest,runtime_ref,session_id,host_incarnation,owner_generation,owner_token_hash,checkpoint_revision,checkpoint_digest) VALUES(?,?,1,'human','mooring.origin-author-binding.v1',?,'fixture-runtime','fixture-session','fixture-host',1,zeroblob(32),'0',?)`, lineage+"-source", lineage, digest, digest); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`INSERT INTO mooring_ingress_operations(lineage_id,op_id,source_id,ordinal,change_id,membership_hash,fingerprint,raw_digest,raw_bytes,frontier_digest,envelope_digest) VALUES(?,'origin-backup-op',?,1,'change',?,?,?,3,?,?)`, lineage, lineage+"-source", digest, digest, digest, digest, digest); e != nil {
		t.Fatal(e)
	}
}
func TestMooringOriginMetadataActualPortableReplacePreservesTargetBurns(t *testing.T) {
	source := openMigratedDB(t)
	seedOriginRestore(t, source, "source-lineage")
	t.Run("target-cannot-drop-nonempty-marker", func(t *testing.T) {
		target := openMigratedDB(t)
		// Synthetic old target schema only, never an operational database.
		rows, e := target.Query(`SELECT name FROM sqlite_master WHERE type='trigger' AND sql LIKE '%origin_lineage_id%'`)
		if e != nil {
			t.Fatal(e)
		}
		var names []string
		for rows.Next() {
			var name string
			if e := rows.Scan(&name); e != nil {
				t.Fatal(e)
			}
			names = append(names, name)
		}
		if e := rows.Err(); e != nil {
			t.Fatal(e)
		}
		_ = rows.Close()
		for _, name := range names {
			if _, e := target.Exec(`DROP TRIGGER "` + strings.ReplaceAll(name, `"`, `""`) + `"`); e != nil {
				t.Fatal(e)
			}
		}
		if _, e := target.Exec(`ALTER TABLE mooring_collab_documents DROP COLUMN origin_lineage_id`); e != nil {
			t.Fatal(e)
		}
		dump, e := backup.DumpWorkspace(t.Context(), source, "origin-backup-ws")
		if e != nil {
			t.Fatal(e)
		}
		if _, e := backup.RestoreDumpTxHooks(t.Context(), target, dump, nil); e == nil || !strings.Contains(e.Error(), "target cannot preserve origin lineage") {
			t.Fatal("target silently dropped content lineage", e)
		}
		var n int
		if e := target.QueryRow(`SELECT COUNT(*) FROM pages WHERE id='origin-backup-page'`).Scan(&n); e != nil || n != 0 {
			t.Fatal("refused downgrade published partial content", n, e)
		}
	})
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[rollback], func(t *testing.T) {
			target := openMigratedDB(t)
			seedOriginRestore(t, target, "target-lineage")
			if _, e := target.Exec(`INSERT INTO mooring_origin_exclusions(lineage_id,origin,kind,source_id) VALUES('target-lineage',9,'issued','target-unused-reservation')`); e != nil {
				t.Fatal(e)
			}
			dump, e := backup.DumpWorkspace(t.Context(), source, "origin-backup-ws")
			if e != nil {
				t.Fatal(e)
			}
			for _, table := range []string{"mooring_origin_documents", "mooring_origin_exclusions", "mooring_ingress_sources", "mooring_ingress_operations"} {
				if _, found := dump.Tables[table]; found {
					t.Fatal("portable dump carried target-local metadata", table)
				}
				dump.Tables[table] = []map[string]any{{"source_id": "target-lineage-source", "lineage_id": "target-lineage", "closed": int64(0), "next_ordinal": int64(1)}}
			}
			docs := dump.Tables["mooring_collab_documents"]
			if len(docs) != 1 || docs[0]["origin_lineage_id"] != "source-lineage" {
				t.Fatal("portable content lost marker")
			}
			sentinel := errors.New("owned-origin-rollback")
			hooks := &backup.RestoreDumpHooks{PreInsert: func(ctx context.Context, tx *sql.Tx) error {
				deleted, e := backup.ReplaceWorkspaceContents(ctx, tx, "origin-backup-ws", "origin-backup")
				if e != nil {
					return e
				}
				for _, table := range []string{"mooring_origin_documents", "mooring_origin_exclusions", "mooring_ingress_sources", "mooring_ingress_operations"} {
					if deleted[table] != 0 {
						return errors.New("replacement deleted origin tombstones")
					}
				}
				return nil
			}}
			if rollback {
				hooks.PreCommit = func(context.Context) error { return sentinel }
			}
			_, e = backup.RestoreDumpTxHooks(t.Context(), target, dump, hooks)
			if rollback {
				if !errors.Is(e, sentinel) {
					t.Fatal("actual rollback hook not reached", e)
				}
			} else if e != nil {
				t.Fatal(e)
			}
			var closed, next, burns, ops, matched int
			for _, q := range []struct {
				sql string
				to  *int
			}{
				{`SELECT closed FROM mooring_ingress_sources WHERE source_id='target-lineage-source'`, &closed},
				{`SELECT next_ordinal FROM mooring_ingress_sources WHERE source_id='target-lineage-source'`, &next},
				{`SELECT COUNT(*) FROM mooring_origin_exclusions WHERE lineage_id='target-lineage'`, &burns},
				{`SELECT COUNT(*) FROM mooring_ingress_operations WHERE lineage_id='target-lineage'`, &ops},
				{`SELECT COUNT(*) FROM mooring_origin_documents d JOIN mooring_collab_documents c ON c.page_id=d.page_id AND c.workspace_id=d.workspace_id AND c.origin_lineage_id=d.lineage_id`, &matched},
			} {
				if e := target.QueryRow(q.sql).Scan(q.to); e != nil {
					t.Fatal(e)
				}
			}
			wantClosed, wantMatch := 1, 0
			if rollback {
				wantClosed, wantMatch = 0, 1
			}
			if closed != wantClosed || next != 2 || burns != 3 || ops != 1 || matched != wantMatch {
				t.Fatalf("target provenance changed: closed=%d next=%d burns=%d ops=%d matched=%d", closed, next, burns, ops, matched)
			}
			var marker string
			if e := target.QueryRow(`SELECT origin_lineage_id FROM mooring_collab_documents`).Scan(&marker); e != nil {
				t.Fatal(e)
			}
			wantMarker := "source-lineage"
			if rollback {
				wantMarker = "target-lineage"
			}
			if marker != wantMarker {
				t.Fatal("content marker replaced incorrectly", marker)
			}
			// Neither a matching marker nor these surviving rows reconstruct a source
			// capability or prove post-backup issued-unused coverage on another host.
		})
	}
}
