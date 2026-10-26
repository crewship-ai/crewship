package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

func TestHostHookUpgradeRequiresAuditedReview(t *testing.T) {
	for _, fault := range []string{"", "ABORT", "IGNORE", "UPDATE_IGNORE"} {
		auditFailure := fault != ""
		t.Run("upgrade/"+fault, func(t *testing.T) {
			db, err := Open(filepath.Join(t.TempDir(), "upgrade.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx := context.Background()
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			if err := MigrateSkipping(ctx, db.DB, logger, "host_hook_authorization"); err != nil {
				t.Fatal(err)
			}
			execMigrationFixture(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('ws-hook','Hooks','ws-hook')`)
			execMigrationFixture(t, db, `INSERT INTO hooks_config(id,workspace_id,event,handler_kind,handler_config,enabled,created_by,updated_at) VALUES
    ('legacy','ws-hook','pre_agent_start','shell','{"command":"original"}',1,'historical-owner','2026-01-01'),
    ('disabled','ws-hook','pre_agent_start','shell','{"command":"disabled"}',0,NULL,'2026-01-01'),
    ('http','ws-hook','pre_agent_start','http','{"url":"https://example.test"}',1,NULL,'2026-01-01')`)
			if auditFailure {
				statement := `CREATE TRIGGER fail_hook_audit BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(IGNORE); END`
				if fault == "UPDATE_IGNORE" {
					statement = `CREATE TRIGGER reject_quarantine BEFORE UPDATE OF enabled ON hooks_config BEGIN SELECT RAISE(IGNORE); END`
				}
				if fault == "ABORT" {
					statement = `CREATE TRIGGER fail_hook_audit BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`
				}
				execMigrationFixture(t, db, statement)
			}
			err = Migrate(ctx, db.DB, logger)
			if auditFailure {
				if err == nil {
					t.Fatal("upgrade acknowledged missing audit")
				}
				var enabled, recorded int
				if err := db.QueryRow(`SELECT enabled FROM hooks_config WHERE id='legacy'`).Scan(&enabled); err != nil {
					t.Fatal(err)
				}
				if err := db.QueryRow(`SELECT count(*) FROM _migrations WHERE name='host_hook_authorization'`).Scan(&recorded); err != nil {
					t.Fatal(err)
				}
				if enabled != 1 || recorded != 0 {
					t.Fatalf("partial upgrade: enabled=%d recorded=%d", enabled, recorded)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var good, audits int
			if err := db.QueryRow(`SELECT count(*) FROM hooks_config WHERE (id='legacy' AND enabled=0 AND handler_config='{"command":"original"}' AND created_by='historical-owner' AND updated_at='2026-01-01' AND host_authorization='') OR (id='disabled' AND enabled=0 AND updated_at='2026-01-01') OR (id='http' AND enabled=1 AND updated_at='2026-01-01')`).Scan(&good); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='hook.host_review_required' AND workspace_id='ws-hook' AND user_id IS NULL`).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			if good != 3 || audits != 2 {
				t.Fatalf("upgrade boundaries: rows=%d audits=%d", good, audits)
			}
			execMigrationFixture(t, db, `UPDATE hooks_config SET enabled=1,host_authorization='reviewed' WHERE id='legacy'`)
			if err := Migrate(ctx, db.DB, logger); err != nil {
				t.Fatal(err)
			}
			var enabled int
			if err := db.QueryRow(`SELECT enabled FROM hooks_config WHERE id='legacy'`).Scan(&enabled); err != nil || enabled != 1 {
				t.Fatalf("migration replay disabled reviewed hook: %v", err)
			}
		})
	}
}
