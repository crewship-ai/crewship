package database

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

func TestInstanceAuditChainUpgradePreservesLegacy(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := MigrateSkipping(ctx, db.DB, logger, "instance_audit_chain"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO instance_audit_logs(id,user_id,action,entity_type,entity_id,target_workspace_id,metadata,ip_address,user_agent,created_at) VALUES('legacy','erased-user','legacy','test','old','erased-workspace',NULL,'ip','agent','2000-01-01')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db.DB, logger); err != nil {
		t.Fatal(err)
	}
	var id, user, action, kind, entity, ws, ip, ua, at string
	var meta, prev, hash sql.NullString
	var seq sql.NullInt64
	if err := db.QueryRow(`SELECT id,user_id,action,entity_type,entity_id,target_workspace_id,metadata,ip_address,user_agent,created_at,chain_seq,prev_hash,entry_hash FROM instance_audit_logs WHERE id='legacy'`).Scan(&id, &user, &action, &kind, &entity, &ws, &meta, &ip, &ua, &at, &seq, &prev, &hash); err != nil {
		t.Fatal(err)
	}
	if id != "legacy" || user != "erased-user" || action != "legacy" || kind != "test" || entity != "old" || ws != "erased-workspace" || meta.Valid || ip != "ip" || ua != "agent" || at != "2000-01-01" || seq.Valid || prev.Valid || hash.Valid {
		t.Fatal("legacy stored values or unverified state changed")
	}
	if _, err := db.Exec(`INSERT INTO instance_audit_logs(id,action,entity_type,created_at,chain_seq) VALUES('one','test','test','now',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO instance_audit_logs(id,action,entity_type,created_at,chain_seq) VALUES('duplicate','test','test','now',1)`); err == nil {
		t.Fatal("duplicate sequence accepted")
	}
}
