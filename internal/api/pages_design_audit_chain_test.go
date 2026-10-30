package api

import (
	"bytes"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestDesignWorkspaceAdminChainedAudit(t *testing.T) {
	f := newDesignAuthorityFixture(t)
	user := "chained-design-admin"
	f.addUser(t, user)
	mustExec(t, f.h.db, `UPDATE workspace_members SET role='ADMIN' WHERE workspace_id=? AND user_id=?`, f.workspace, user)
	key := []byte("configured-design-audit-regression-key")
	writer := journal.NewWriter(f.h.db, newTestLogger(), journal.WriterOptions{ChainKey: key, FlushInterval: time.Hour})
	t.Cleanup(func() {
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
	})
	f.h.journal = writer
	var observed atomic.Int64
	writer.AddCommitObserver(func(entries []journal.Entry) { observed.Add(int64(len(entries))) })
	for _, op := range []string{"get", "versions", "apply"} {
		body := ""
		if op == "apply" {
			body = `{"operations":[{"op":"upsert","node":{"id":"chained","type":"text","text":"Audited edit","x":0,"y":0,"width":100,"height":30}}]}`
		}
		if w := f.call(t, user, op, body); w.Code != 200 {
			t.Fatalf("%s: %d %s", op, w.Code, w.Body)
		}
	}
	var records int
	if err := f.h.db.QueryRow(`SELECT count(*) FROM audit_logs a JOIN journal_entries j ON j.id=a.id WHERE a.action='design.workspace_admin_accessed' AND a.user_id=? AND j.workspace_id=a.workspace_id AND j.actor_id=a.user_id AND json_extract(j.payload,'$.action')=a.action AND json_extract(j.payload,'$.entity_id')=a.entity_id AND json_extract(j.payload,'$.metadata.operation')=json_extract(a.metadata,'$.operation')`, user).Scan(&records); err != nil {
		t.Fatal(err)
	}
	if records != 3 || observed.Load() != 3 {
		t.Fatalf("accesses lack exactly one committed chained receipt: rows=%d observed=%d", records, observed.Load())
	}
	verified, err := journal.VerifyChainWithKey(t.Context(), f.h.db, f.workspace, key)
	if err != nil || !verified.OK {
		t.Fatalf("configured chain does not verify: %+v %v", verified, err)
	}
}

func TestDesignWorkspaceAdminChainedAuditFailurePreservesData(t *testing.T) {
	for _, failure := range []string{"abort", "ignore", "erase", "rewrite", "legacy_rewrite", "late_content_rewrite"} {
		t.Run(failure, func(t *testing.T) {
			f := newDesignAuthorityFixture(t)
			user := "chained-design-admin"
			f.addUser(t, user)
			mustExec(t, f.h.db, `UPDATE workspace_members SET role='ADMIN' WHERE workspace_id=? AND user_id=?`, f.workspace, user)
			var before []byte
			if err := f.h.db.QueryRow(`SELECT state FROM artifact_design_documents WHERE page_id=?`, f.page).Scan(&before); err != nil {
				t.Fatal(err)
			}
			trigger := map[string]string{
				"abort":                `CREATE TRIGGER break_design_chain BEFORE INSERT ON journal_entries WHEN json_extract(NEW.payload,'$.action')='design.workspace_admin_accessed' BEGIN SELECT RAISE(ABORT,'chain unavailable'); END`,
				"ignore":               `CREATE TRIGGER break_design_chain BEFORE INSERT ON journal_entries WHEN json_extract(NEW.payload,'$.action')='design.workspace_admin_accessed' BEGIN SELECT RAISE(IGNORE); END`,
				"erase":                `CREATE TRIGGER break_design_chain AFTER INSERT ON journal_entries WHEN json_extract(NEW.payload,'$.action')='design.workspace_admin_accessed' BEGIN DELETE FROM journal_entries WHERE id=NEW.id; END`,
				"rewrite":              `CREATE TRIGGER break_design_chain AFTER INSERT ON journal_entries WHEN json_extract(NEW.payload,'$.action')='design.workspace_admin_accessed' BEGIN UPDATE journal_entries SET payload='{}' WHERE id=NEW.id; END`,
				"legacy_rewrite":       `CREATE TRIGGER break_design_chain AFTER INSERT ON journal_entries WHEN json_extract(NEW.payload,'$.action')='design.workspace_admin_accessed' BEGIN UPDATE audit_logs SET metadata='{}' WHERE id=NEW.id; END`,
				"late_content_rewrite": `CREATE TRIGGER break_design_chain AFTER UPDATE OF state ON artifact_design_documents BEGIN UPDATE journal_entries SET payload='{}' WHERE json_extract(payload,'$.action')='design.workspace_admin_accessed'; END`,
			}[failure]
			mustExec(t, f.h.db, trigger)
			if failure != "late_content_rewrite" {
				if w := f.call(t, user, "get", ""); w.Code != 500 {
					t.Errorf("read escaped unavailable chain: %d %s", w.Code, w.Body)
				}
			}
			if w := f.call(t, user, "apply", `{"operations":[{"op":"upsert","node":{"id":"unsafe","type":"text","text":"Unconfirmed","x":0,"y":0,"width":100,"height":30}}]}`); w.Code != 500 {
				t.Errorf("edit escaped unavailable chain: %d %s", w.Code, w.Body)
			}
			var after []byte
			if err := f.h.db.QueryRow(`SELECT state FROM artifact_design_documents WHERE page_id=?`, f.page).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) || f.versions(t) != 1 {
				t.Error("chain refusal changed the confirmed canvas or history")
			}
			var records int
			if err := f.h.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='design.workspace_admin_accessed' AND user_id=?`, user).Scan(&records); err != nil {
				t.Fatal(err)
			}
			want := 0
			if failure == "late_content_rewrite" {
				want = 1
			} // The preflight access committed before the refused edit.
			if records != want {
				t.Errorf("audit receipt count=%d, want %d", records, want)
			}
		})
	}
}
