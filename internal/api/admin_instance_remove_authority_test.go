package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/journal"
)

// Exercise a request whose instance middleware admission has already happened.
// The handler must use current authority and target role inside departure's TX.
func TestInstanceRemoveMembershipCurrentAuthority(t *testing.T) {
	for _, change := range []string{"current-admin", "env-owner", "demoted", "suspended", "restricted", "suspended-env-owner", "email-no-longer-env-owner", "missing-scope", "last-owner", "missing-member"} {
		t.Run(change, func(t *testing.T) {
			f := newInstanceFixture(t) // Includes the real one-time admin bootstrap.
			writer := journal.NewWriter(f.db, newTestLogger(), journal.WriterOptions{
				ChainKey: []byte("instance-removal-current-authority-key"), FlushInterval: time.Hour,
			})
			t.Cleanup(func() { _ = writer.Close() })
			h := NewInstanceAdminHandler(f.db, newTestLogger(), nil, writer, nil)
			// Carol is an OWNER. Another OWNER permits a healthy removal; the
			// last-owner case deliberately retains Carol as the only OWNER.
			if change != "last-owner" {
				seedInstanceUser(t, f.db, "other-owner", "other-owner@ex.com", "ws-new", "OWNER")
			}
			want := http.StatusNoContent
			switch change {
			case "env-owner", "suspended-env-owner", "email-no-longer-env-owner":
				t.Setenv(backup.InstanceOwnerEmailEnv, "boss@ex.com")
				mustExec(t, f.db, `UPDATE users SET instance_role=NULL WHERE id='boss'`)
				if change == "suspended-env-owner" {
					mustExec(t, f.db, `UPDATE users SET suspended_at=CURRENT_TIMESTAMP WHERE id='boss'`)
					want = http.StatusForbidden
				}
				if change == "email-no-longer-env-owner" {
					mustExec(t, f.db, `UPDATE users SET email='former-owner@ex.com' WHERE id='boss'`)
					want = http.StatusForbidden
				}
			case "demoted":
				mustExec(t, f.db, `UPDATE users SET instance_role=NULL WHERE id='boss'`)
				want = http.StatusForbidden
			case "suspended":
				mustExec(t, f.db, `UPDATE users SET suspended_at=CURRENT_TIMESTAMP WHERE id='boss'`)
				want = http.StatusForbidden
			case "restricted":
				// A legal global Guest state: no conflicting workspace ADMIN or
				// OWNER, but the independently named instance role stays ADMIN.
				mustExec(t, f.db, `UPDATE workspace_members SET role='MEMBER',access_mode='restricted' WHERE user_id='boss'`)
				want = http.StatusForbidden
			case "missing-scope":
				want = http.StatusForbidden
			case "last-owner":
				want = http.StatusConflict
			case "missing-member":
				mustExec(t, f.db, `DELETE FROM workspace_members WHERE workspace_id='ws-new' AND user_id='carol'`)
				want = http.StatusNotFound
			}
			count := func(query string) int {
				t.Helper()
				var n int
				if err := f.db.QueryRow(query).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			beforeInstance := count(`SELECT COUNT(*) FROM instance_audit_logs`)
			beforeJournal := count(`SELECT COUNT(*) FROM journal_entries`)
			req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/instance/workspaces/ws-new/members/carol", nil)
			req.SetPathValue("workspaceId", "ws-new")
			req.SetPathValue("userId", "carol")
			ctx := withUser(req.Context(), &AuthUser{ID: "boss", Email: "boss@ex.com"})
			req = req.WithContext(context.WithValue(ctx, ctxInstanceAdmin, true))
			if change == "missing-scope" {
				req = withTokenScopes(req, "agents:write")
			}
			rr := httptest.NewRecorder()
			h.RemoveMembership(rr, req)
			if rr.Code != want {
				t.Errorf("stale admission removal HTTP=%d want=%d: %s", rr.Code, want, rr.Body.String())
			}
			role := f.role("ws-new", "carol")
			if want == http.StatusNoContent {
				if role != "" || count(`SELECT COUNT(*) FROM instance_audit_logs`) != beforeInstance+1 || count(`SELECT COUNT(*) FROM journal_entries`) != beforeJournal+1 {
					t.Error("healthy OWNER removal must commit membership and both audits")
				}
			} else {
				if (change != "missing-member" && role != "OWNER") || count(`SELECT COUNT(*) FROM instance_audit_logs`) != beforeInstance || count(`SELECT COUNT(*) FROM journal_entries`) != beforeJournal {
					t.Error("refusal changed the target OWNER or durable audits")
				}
			}
		})
	}
}
