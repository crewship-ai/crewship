package api

import (
	"fmt"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestAuthorizationAuditCanonicalPostInsertFaultsRollBack(t *testing.T) {
	for _, operation := range []string{"role", "capabilities"} {
		for _, fault := range []string{"delete", "summary", "actor_type"} {
			t.Run(operation+"/"+fault, func(t *testing.T) {
				f := newAuthorizationAuditFixture(t)
				path, healthy, mutation, typ := "/members/"+f.member, `{"role":"VIEWER"}`, `{"role":"MANAGER"}`, "member.role_changed"
				if operation == "capabilities" {
					path = "/members/" + f.target + "/capabilities"
					healthy = `{"grant":["issue.create"]}`
					mutation = `{"revoke":["issue.create"]}`
					typ = "member.capabilities_changed"
				}
				if rr := f.do("PATCH", path, healthy); rr.Code != 200 {
					t.Fatalf("healthy configured mutation status %d", rr.Code)
				}
				f.checkChain(t)
				if f.published.Load() != 1 {
					t.Fatal("healthy notification calibration failed")
				}
				var beforeRole, beforeCaps, beforeAt string
				if err := f.db.QueryRow(`SELECT role,capabilities,updated_at FROM workspace_members WHERE id=?`, f.member).Scan(&beforeRole, &beforeCaps, &beforeAt); err != nil {
					t.Fatal(err)
				}
				var beforeSettings int
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='member.role_change'`).Scan(&beforeSettings); err != nil {
					t.Fatal(err)
				}
				var beforeEvents int
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type=?`, typ).Scan(&beforeEvents); err != nil || beforeEvents != 1 {
					t.Fatalf("healthy canonical event count %d err%v", beforeEvents, err)
				}
				effect := `DELETE FROM journal_entries WHERE id=NEW.id;`
				if fault == "summary" {
					effect = `UPDATE journal_entries SET summary='synthetic altered summary' WHERE id=NEW.id;`
				}
				if fault == "actor_type" {
					effect = `UPDATE journal_entries SET actor_type='system' WHERE id=NEW.id;`
				}
				execOrFatal(t, f.db, fmt.Sprintf(`CREATE TRIGGER alter_authorization_audit AFTER INSERT ON journal_entries WHEN NEW.entry_type='%s' BEGIN %s END`, typ, effect))
				rr := f.do("PATCH", path, mutation)
				if rr.Code != 500 {
					t.Errorf("canonical audit post-insert fault accepted: status%d want500", rr.Code)
				}
				var role, caps, at string
				if err := f.db.QueryRow(`SELECT role,capabilities,updated_at FROM workspace_members WHERE id=?`, f.member).Scan(&role, &caps, &at); err != nil {
					t.Fatal(err)
				}
				if role != beforeRole || caps != beforeCaps || at != beforeAt {
					t.Error("canonical audit fault committed membership mutation")
				}
				var events, settings int
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type=?`, typ).Scan(&events); err != nil {
					t.Fatal(err)
				}
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='member.role_change'`).Scan(&settings); err != nil {
					t.Fatal(err)
				}
				if events != beforeEvents || settings != beforeSettings {
					t.Errorf("canonical audit fault retained rows events%d/%d settings%d/%d", events, beforeEvents, settings, beforeSettings)
				}
				if f.published.Load() != 1 {
					t.Error("canonical audit fault notified observers")
				}
				check, err := journal.VerifyChainWithKey(t.Context(), f.db, f.workspace, f.key)
				if err != nil || !check.OK {
					t.Errorf("canonical audit fault left invalid chain: %+v err%v", check, err)
				}
			})
		}
	}
}
