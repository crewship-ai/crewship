package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestMemberRemovalFinalStateAfterAudit(t *testing.T) {
	for _, fault := range []string{"healthy", "new-workspace-membership-id", "crew-membership"} {
		t.Run(fault, func(t *testing.T) {
			f := newInstanceFixture(t)
			seedInstanceUser(t, f.db, "other-owner", "other-owner@ex.com", "ws-new", "OWNER")
			wmSeedCrew(t, f.db, "ws-new", "removal-crew", "Removal", "removal")
			wmSeedCrewMember(t, f.db, "removal-crew", "carol")
			writer := journal.NewWriter(f.db, newTestLogger(), journal.WriterOptions{ChainKey: []byte("member-removal-final-state-key"), FlushInterval: time.Hour})
			t.Cleanup(func() { _ = writer.Close() })
			notified := 0
			writer.AddCommitObserver(func([]journal.Entry) { notified++ })
			if fault != "healthy" {
				mutation := `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('replacement-membership','ws-new','carol','OWNER');`
				if fault == "crew-membership" {
					mutation = `INSERT INTO crew_members(id,crew_id,user_id) VALUES('replacement-crew-membership','removal-crew','carol');`
				}
				mustExec(t, f.db, `CREATE TRIGGER restore_departed_member AFTER INSERT ON journal_entries WHEN NEW.entry_type='member.removed' BEGIN `+mutation+` END`)
			}
			count := func(query string) int {
				t.Helper()
				var n int
				if err := f.db.QueryRow(query).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			beforeAudit := count(`SELECT COUNT(*) FROM instance_audit_logs`)
			req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/instance/workspaces/ws-new/members/carol", nil)
			req.SetPathValue("workspaceId", "ws-new")
			req.SetPathValue("userId", "carol")
			req = req.WithContext(withUser(req.Context(), &AuthUser{ID: "boss", Email: "boss@ex.com"}))
			rr := httptest.NewRecorder()
			NewInstanceAdminHandler(f.db, newTestLogger(), nil, writer, nil).RemoveMembership(rr, req)
			want := http.StatusNoContent
			if fault != "healthy" {
				want = http.StatusInternalServerError
			}
			if rr.Code != want {
				t.Errorf("HTTP=%d want=%d: %s", rr.Code, want, rr.Body.String())
			}
			if fault == "healthy" {
				if count(`SELECT COUNT(*) FROM workspace_members WHERE workspace_id='ws-new' AND user_id='carol'`) != 0 || count(`SELECT COUNT(*) FROM crew_members WHERE crew_id='removal-crew' AND user_id='carol'`) != 0 || count(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='member.removed'`) != 1 || count(`SELECT COUNT(*) FROM instance_audit_logs`) != beforeAudit+1 || notified != 1 {
					t.Error("healthy removal did not commit both removals and audits with one notification")
				}
			} else if count(`SELECT COUNT(*) FROM workspace_members WHERE id='m-carol-ws-new' AND role='OWNER'`) != 1 || count(`SELECT COUNT(*) FROM crew_members WHERE crew_id='removal-crew' AND user_id='carol'`) != 1 || count(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='member.removed'`) != 0 || count(`SELECT COUNT(*) FROM instance_audit_logs`) != beforeAudit || notified != 0 {
				t.Error("post-audit reinsertion must roll back the original membership, crew removal and both audits without notification")
			}
		})
	}
}
