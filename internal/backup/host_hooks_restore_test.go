package backup_test

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"filippo.io/age"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/hooks"
)

func TestRestoreHostHookRequiresLocalReview(t *testing.T) {
	for _, fault := range []string{"", "ABORT", "IGNORE"} {
		auditFailure := fault != ""
		t.Run("import/"+fault, func(t *testing.T) {
			db, err := database.Open(filepath.Join(t.TempDir(), "restore.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := database.Migrate(t.Context(), db.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO workspaces(id,name,slug) VALUES('ws-hook','Hooks','ws-hook')`); err != nil {
				t.Fatal(err)
			}
			if auditFailure {
				statement := `CREATE TRIGGER fail_hook_audit BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(IGNORE); END`
				if fault == "ABORT" {
					statement = `CREATE TRIGGER fail_hook_audit BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`
				}
				if _, err := db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			dump := &backup.DBDump{WorkspaceID: "ws-hook", Tables: map[string][]map[string]any{"hooks_config": {
				{"id": "imported-shell", "workspace_id": "ws-hook", "event": "pre_agent_start", "handler_kind": "shell", "handler_config": `{"command":"never run"}`, "enabled": int64(1), "host_authorization": "forged-or-imported-approval"},
				{"id": "imported-http", "workspace_id": "ws-hook", "event": "pre_agent_start", "handler_kind": "http", "handler_config": `{"url":"https://example.test"}`, "enabled": int64(1)},
			}}}
			_, err = backup.RestoreDumpTxHooks(t.Context(), db.DB, dump, nil)
			source := dump.Tables["hooks_config"][0]
			if source["enabled"] != int64(1) || source["host_authorization"] != "forged-or-imported-approval" {
				t.Fatalf("restore mutated source: %+v", source)
			}
			if auditFailure {
				if err == nil {
					t.Fatal("restore acknowledged missing audit")
				}
				var count int
				if err := db.QueryRow(`SELECT count(*) FROM hooks_config`).Scan(&count); err != nil || count != 0 {
					t.Fatalf("partial restore: rows=%d err=%v", count, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var good, audits int
			if err := db.QueryRow(`SELECT count(*) FROM hooks_config WHERE (id='imported-shell' AND enabled=0 AND host_authorization='' AND handler_config='{"command":"never run"}') OR (id='imported-http' AND enabled=1)`).Scan(&good); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM audit_logs WHERE entity_id='imported-shell' AND action='hook.host_review_required'`).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			// An existing destination approval is not an imported grant. INSERT
			// OR IGNORE must preserve it and emit no new quarantine receipt.
			if _, err := db.Exec(`UPDATE hooks_config SET enabled=1,host_authorization='destination-approval' WHERE id='imported-shell'`); err != nil {
				t.Fatal(err)
			}
			if _, err := backup.RestoreDumpTxHooks(t.Context(), db.DB, dump, nil); err != nil {
				t.Fatal(err)
			}
			var approval string
			if err := db.QueryRow(`SELECT host_authorization FROM hooks_config WHERE id='imported-shell'`).Scan(&approval); err != nil || approval != "destination-approval" {
				t.Fatalf("ignored target changed: %q %v", approval, err)
			}
			var afterAudits int
			if err := db.QueryRow(`SELECT count(*) FROM audit_logs WHERE entity_id='imported-shell' AND action='hook.host_review_required'`).Scan(&afterAudits); err != nil || afterAudits != audits {
				t.Fatalf("ignored target audited again: %d %v", afterAudits, err)
			}
			if good != 2 || audits != 1 {
				t.Fatalf("restore boundaries: rows=%d audits=%d", good, audits)
			}
		})
	}
}

func TestHostHookInstanceRecoveryStripsApproval(t *testing.T) {
	f := newInstanceFixture(t)
	id, err := hooks.Register(t.Context(), f.db, hooks.Hook{WorkspaceID: "ws_e2e_1", Event: hooks.EventPreAgentStart, HandlerKind: hooks.HandlerKindShell, HandlerConfig: map[string]any{"command": "never run"}, Enabled: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	original, err := hooks.Get(t.Context(), f.db, "ws_e2e_1", id)
	if err != nil || !hooks.HostAuthorized(*original) {
		t.Fatalf("source approval: %v", err)
	}
	bundle := f.create(t, true)
	dir := filepath.Join(t.TempDir(), "recovered")
	rep, err := backup.RecoverInstance(t.Context(), backup.RecoverOptions{BundlePath: bundle.Path, Identities: []age.Identity{f.identity}, DataDir: dir, Actor: "u_admin"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(filepath.Join(dir, "crewship.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	restored, err := hooks.Get(t.Context(), db.DB, "ws_e2e_1", id)
	if err != nil || restored == nil || restored.Enabled || hooks.HostAuthorized(*restored) || restored.HandlerConfig["command"] != "never run" {
		t.Fatalf("recovered approval/configuration: %+v %v", restored, err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM audit_logs WHERE entity_id=? AND action='hook.host_review_required'`, id).Scan(&count); err != nil || count < 1 {
		t.Fatalf("missing recovery audit: %d %v", count, err)
	}
	if len(rep.Warnings) == 0 {
		t.Fatal("missing operator guidance")
	}
}
