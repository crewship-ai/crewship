package database

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// Synthetic correlation rows only. No app inventory, author admission, issuer,
// source capability, participant route or Saved is qualified by these tests.
func seedOriginMetadata(t *testing.T, tx *sql.Tx) {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('origin-ws','Origin','origin-ws')`,
		`INSERT INTO users(id,email) VALUES('origin-human','origin@synthetic.invalid')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('origin-page','origin-ws','origin-page','Origin','origin-human','{}')`,
		`INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64,origin_lineage_id) VALUES('origin-page','origin-ws','wordcraft','0','e30=','origin-lineage')`,
		`INSERT INTO mooring_origin_documents(lineage_id,workspace_id,page_id,application_id,binding_version,initial_revision,initial_checkpoint_digest,initial_inventory_digest) VALUES('origin-lineage','origin-ws','origin-page','wordcraft','mooring.origin-document.v1','0','` + originMetadataDigest() + `','` + originMetadataDigest() + `')`,
		`INSERT INTO mooring_origin_exclusions(lineage_id,origin,kind,first_clock) VALUES('origin-lineage',0,'baseline',1)`,
		`INSERT INTO mooring_origin_exclusions(lineage_id,origin,kind,source_id) VALUES('origin-lineage',1,'issued','origin-source')`,
		`INSERT INTO mooring_ingress_sources(source_id,lineage_id,origin,kind,binding_version,author_binding_digest,runtime_ref,session_id,host_incarnation,owner_generation,owner_token_hash,checkpoint_revision,checkpoint_digest) VALUES('origin-source','origin-lineage',1,'human','mooring.origin-author-binding.v1','` + originMetadataDigest() + `','fixture-runtime','fixture-session','fixture-host',1,zeroblob(32),'0','` + originMetadataDigest() + `')`,
	} {
		if _, err := tx.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}
func originMetadataDigest() string { return "sha256:" + strings.Repeat("a", 64) }
func originMetadataOp(id string, ordinal int64) string {
	return fmt.Sprintf(`('origin-lineage','%s','origin-source',%d,'change','%s','%s','%s',3,'%s','%s')`, id, ordinal, originMetadataDigest(), originMetadataDigest(), originMetadataDigest(), originMetadataDigest(), originMetadataDigest())
}

const originMetadataInsert = `INSERT INTO mooring_ingress_operations(lineage_id,op_id,source_id,ordinal,change_id,membership_hash,fingerprint,raw_digest,raw_bytes,frontier_digest,envelope_digest) VALUES`

func originMetadataTx(t *testing.T, db *sql.DB) *sql.Tx {
	t.Helper()
	tx, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	seedOriginMetadata(t, tx)
	return tx
}
func originMetadataInt(t *testing.T, tx *sql.Tx, q string) int64 {
	t.Helper()
	var n int64
	if e := tx.QueryRow(q).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}
func TestMooringOriginMetadataIdentityOrderAndTombstones(t *testing.T) {
	db := openMigratedTestDB(t)
	t.Run("ordered-resolution", func(t *testing.T) {
		tx := originMetadataTx(t, db.DB)
		if _, e := tx.Exec(originMetadataInsert + originMetadataOp("op-one", 1) + "," + originMetadataOp("op-two", 2)); e != nil {
			t.Fatal(e)
		}
		if n := originMetadataInt(t, tx, `SELECT next_ordinal FROM mooring_ingress_sources`); n != 3 {
			t.Fatal(n)
		}
		if _, e := tx.Exec(`UPDATE mooring_ingress_operations SET state='resolved',resolution_digest=? WHERE op_id='op-two'`, originMetadataDigest()); e == nil {
			t.Fatal("later operation skipped unresolved prefix")
		}
		for _, id := range []string{"op-one", "op-two"} {
			if _, e := tx.Exec(`UPDATE mooring_ingress_operations SET state='resolved',resolution_digest=? WHERE op_id=?`, originMetadataDigest(), id); e != nil {
				t.Fatal(e)
			}
		}
		if n := originMetadataInt(t, tx, `SELECT resolved_prefix FROM mooring_ingress_sources`); n != 2 {
			t.Fatal(n)
		}
		if _, e := tx.Exec(`UPDATE mooring_ingress_sources SET closed=1`); e != nil {
			t.Fatal(e)
		}
		if _, e := tx.Exec(originMetadataInsert + originMetadataOp("closed-op", 3)); e == nil {
			t.Fatal("closed source admitted new ordinal")
		}
		for _, q := range []string{
			`UPDATE mooring_ingress_sources SET closed=0`, `UPDATE mooring_ingress_sources SET next_ordinal=1`, `UPDATE mooring_ingress_sources SET resolved_prefix=0`,
			`UPDATE mooring_ingress_sources SET next_ordinal=3.5`, `UPDATE mooring_ingress_sources SET author_binding_digest='` + "sha256:" + strings.Repeat("b", 64) + `'`,
			`UPDATE mooring_ingress_operations SET state='accepted',resolution_digest=NULL`, `UPDATE mooring_ingress_operations SET raw_bytes=4`,
			`UPDATE mooring_origin_documents SET operation_count=0`, `UPDATE mooring_origin_documents SET operation_bytes=0`,
			`UPDATE mooring_collab_documents SET origin_lineage_id=''`,
		} {
			if _, e := tx.Exec(q); e == nil {
				t.Fatalf("immutable metadata updated: %s", q)
			}
		}
	})
	t.Run("replace-with-recursion-off", func(t *testing.T) {
		// PRAGMA setting is connection-local and set BEFORE starting the writer.
		if _, e := db.Exec(`PRAGMA recursive_triggers=OFF`); e != nil {
			t.Fatal(e)
		}
		tx := originMetadataTx(t, db.DB)
		if n := originMetadataInt(t, tx, `PRAGMA recursive_triggers`); n != 0 {
			t.Fatal("replacement probe requires recursion disabled", n)
		}
		// Initial counters make these replacements otherwise structurally valid;
		// DELETE triggers cannot protect REPLACE while recursion is disabled.
		for _, table := range []string{"mooring_origin_documents", "mooring_origin_exclusions", "mooring_ingress_sources"} {
			if _, e := tx.Exec(`INSERT OR REPLACE INTO ` + table + ` SELECT * FROM ` + table); e == nil {
				t.Fatal("fresh metadata identity replaced", table)
			}
		}
		if _, e := tx.Exec(originMetadataInsert + originMetadataOp("op-one", 1)); e != nil {
			t.Fatal(e)
		}
		for _, marker := range []string{"origin-lineage", "foreign-lineage", ""} {
			if _, e := tx.Exec(`INSERT OR REPLACE INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64,origin_lineage_id) VALUES('origin-page','origin-ws','wordcraft','0','e30=',?)`, marker); e == nil {
				t.Fatal("content REPLACE bypassed lineage retirement", marker)
			}
		}
		for _, table := range []string{"mooring_origin_documents", "mooring_origin_exclusions", "mooring_ingress_sources", "mooring_ingress_operations"} {
			for _, q := range []string{`DELETE FROM ` + table, `INSERT OR REPLACE INTO ` + table + ` SELECT * FROM ` + table} {
				if _, e := tx.Exec(q); e == nil {
					t.Fatalf("deleted/replaced original metadata: %s", q)
				}
			}
		}
		if _, e := tx.Exec(`INSERT INTO mooring_origin_exclusions(lineage_id,origin,kind,source_id) VALUES('origin-lineage',2,'issued','origin-source-two')`); e != nil {
			t.Fatal(e)
		}
		if _, e := tx.Exec(`INSERT INTO mooring_ingress_sources(source_id,lineage_id,origin,kind,binding_version,author_binding_digest,runtime_ref,session_id,host_incarnation,owner_generation,owner_token_hash,checkpoint_revision,checkpoint_digest) SELECT 'origin-source-two',lineage_id,2,kind,binding_version,author_binding_digest,runtime_ref,session_id,host_incarnation,owner_generation,owner_token_hash,checkpoint_revision,checkpoint_digest FROM mooring_ingress_sources WHERE source_id='origin-source'`); e != nil {
			t.Fatal(e)
		}
		// Same op ID with an otherwise valid fresh source/ordinal must not
		// replace the original operation when implicit DELETE hooks are off.
		replacement := strings.ReplaceAll(originMetadataInsert+originMetadataOp("op-one", 1), "'origin-source'", "'origin-source-two'")
		replacement = strings.Replace(replacement, "INSERT INTO", "INSERT OR REPLACE INTO", 1)
		if _, e := tx.Exec(replacement); e == nil {
			t.Fatal("fresh source replaced original op identity")
		}
		if n := originMetadataInt(t, tx, `SELECT COUNT(*) FROM mooring_origin_exclusions`); n != 3 {
			t.Fatal("burned exclusions changed", n)
		}
		if n := originMetadataInt(t, tx, `SELECT COUNT(*) FROM mooring_ingress_operations`); n != 1 {
			t.Fatal("operation overwritten", n)
		}
	})
	t.Run("content-only-retirement", func(t *testing.T) {
		tx := originMetadataTx(t, db.DB)
		if _, e := tx.Exec(`DELETE FROM mooring_collab_documents WHERE page_id='origin-page'`); e != nil {
			t.Fatal(e)
		}
		if n := originMetadataInt(t, tx, `SELECT closed FROM mooring_ingress_sources`); n != 1 {
			t.Fatal("content retirement left source open")
		}
		if n := originMetadataInt(t, tx, `SELECT COUNT(*) FROM mooring_origin_exclusions`); n != 2 {
			t.Fatal("content retirement erased burns")
		}
		if _, e := tx.Exec(`INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64,origin_lineage_id) VALUES('origin-page','origin-ws','wordcraft','0','e30=','origin-lineage')`); e != nil {
			t.Fatal("delete then restore exact content failed", e)
		}
		if n := originMetadataInt(t, tx, `SELECT closed FROM mooring_ingress_sources`); n != 1 {
			t.Fatal("reinsert reopened source")
		}
	})
	t.Run("page-and-workspace-retirement", func(t *testing.T) {
		tx := originMetadataTx(t, db.DB)
		if _, e := tx.Exec(`DELETE FROM pages WHERE id='origin-page'`); e != nil {
			t.Fatal(e)
		}
		if n := originMetadataInt(t, tx, `SELECT closed FROM mooring_ingress_sources`); n != 1 {
			t.Fatal("page retirement left original source open")
		}
		if _, e := tx.Exec(`DELETE FROM workspaces WHERE id='origin-ws'`); e != nil {
			t.Fatal(e)
		}
		if n := originMetadataInt(t, tx, `SELECT COUNT(*) FROM mooring_origin_exclusions`); n != 2 {
			t.Fatal("parent deletion erased tombstones", n)
		}
	})
	t.Run("ordinal-overflow-rolls-back-whole-statement", func(t *testing.T) {
		tx := originMetadataTx(t, db.DB)
		if _, e := tx.Exec(`UPDATE mooring_ingress_sources SET next_ordinal=9223372036854775806`); e != nil {
			t.Fatal(e)
		}
		if _, e := tx.Exec(originMetadataInsert + originMetadataOp("last-bounded", 9223372036854775806) + "," + originMetadataOp("overflow", 9223372036854775807)); e == nil {
			t.Fatal("overflow accepted")
		}
		if n := originMetadataInt(t, tx, `SELECT COUNT(*) FROM mooring_ingress_operations`); n != 0 {
			t.Fatal("overflow retained earlier operation", n)
		}
		if n := originMetadataInt(t, tx, `SELECT next_ordinal FROM mooring_ingress_sources`); n != 9223372036854775806 {
			t.Fatal("overflow consumed ordinal", n)
		}
		if n := originMetadataInt(t, tx, `SELECT operation_count FROM mooring_origin_documents`); n != 0 {
			t.Fatal("overflow consumed quota", n)
		}
	})
	t.Run("bounded-canonical-data", func(t *testing.T) {
		tx := originMetadataTx(t, db.DB)
		for _, values := range []string{`2.5,'baseline',1`, `9007199254740992,'baseline',1`, `-1,'baseline',1`, `2,'baseline',0`, `2,'baseline',4294967296`} {
			if _, e := tx.Exec(`INSERT INTO mooring_origin_exclusions(lineage_id,origin,kind,first_clock) VALUES('origin-lineage',` + values + `)`); e == nil {
				t.Fatal("invalid inventory scalar accepted", values)
			}
		}
		if _, e := tx.Exec(`INSERT INTO mooring_origin_exclusions(lineage_id,origin,kind,first_clock) VALUES('origin-lineage',9007199254740991,'baseline',4294967295)`); e != nil {
			t.Fatal(e)
		}
		if _, e := tx.Exec(`UPDATE mooring_origin_documents SET operation_count=100000`); e != nil {
			t.Fatal(e)
		}
		if _, e := tx.Exec(originMetadataInsert + originMetadataOp("over-capacity", 1)); e == nil {
			t.Fatal("exhausted operation quota admitted")
		}
		if n := originMetadataInt(t, tx, `SELECT next_ordinal FROM mooring_ingress_sources`); n != 1 {
			t.Fatal("failed quota consumed ordinal", n)
		}
	})
	t.Run("logical-byte-and-origin-capacity", func(t *testing.T) {
		tx := originMetadataTx(t, db.DB)
		if _, e := tx.Exec(`UPDATE mooring_origin_documents SET operation_bytes=67108864`); e != nil {
			t.Fatal(e)
		}
		if _, e := tx.Exec(originMetadataInsert + originMetadataOp("byte-overflow", 1)); e == nil {
			t.Fatal("logical metadata byte budget bypassed")
		}
		if n := originMetadataInt(t, tx, `SELECT next_ordinal FROM mooring_ingress_sources`); n != 1 {
			t.Fatal("byte refusal consumed ordinal", n)
		}
		if _, e := tx.Exec(`WITH RECURSIVE seq(n) AS (VALUES(2) UNION ALL SELECT n+1 FROM seq WHERE n<4095) INSERT INTO mooring_origin_exclusions(lineage_id,origin,kind,first_clock) SELECT 'origin-lineage',n,'baseline',1 FROM seq`); e != nil {
			t.Fatal(e)
		}
		if n := originMetadataInt(t, tx, `SELECT COUNT(*) FROM mooring_origin_exclusions`); n != 4096 {
			t.Fatal(n)
		}
		if _, e := tx.Exec(`INSERT INTO mooring_origin_exclusions(lineage_id,origin,kind,source_id) VALUES('origin-lineage',4096,'issued','capacity-source')`); e == nil {
			t.Fatal("origin bound ignored issued exclusions")
		}
	})
	t.Run("legacy-marker-not-backfilled", func(t *testing.T) {
		tx := originMetadataTx(t, db.DB)
		if _, e := tx.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('legacy-origin-page','origin-ws','legacy','Legacy','origin-human','{}'); INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64) VALUES('legacy-origin-page','origin-ws','wordcraft','0','e30=')`); e != nil {
			t.Fatal(e)
		}
		if _, e := tx.Exec(`UPDATE mooring_collab_documents SET origin_lineage_id='invented' WHERE page_id='legacy-origin-page'`); e == nil {
			t.Fatal("legacy content was upgraded to lineage")
		}
	})
}
