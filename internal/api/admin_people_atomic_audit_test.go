package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestAdminPeopleMutationAtomicAudit(t *testing.T) {
	for _, scope := range []string{"workspace", "instance"} {
		for _, operation := range []string{"one", "all", "unlock"} {
			for _, fault := range []string{"none", "audit_abort", "audit_ignore", "mutation_undo", "reason_changed", "commit_abort"} {
				if operation == "unlock" && fault == "reason_changed" {
					continue
				}
				t.Run(scope+"/"+operation+"/"+fault, func(t *testing.T) {
					f := newPeopleFixture(t)
					t.Setenv(backup.InstanceOwnerEmailEnv, "")
					workspace, role := "ws-a", "ADMIN"
					if scope == "instance" {
						execOrFatal(t, f.db, `UPDATE users SET instance_role='ADMIN' WHERE id='u-admin'`)
						workspace, role = "", ""
					}
					notifier := &recordingNotifier{}
					h := NewAdminUsersHandler(f.db, newTestLogger(), newNotifyingSessionStore(f.store, notifier))
					action := map[string]string{"one": "user.session_revoked", "all": "user.sessions_revoked", "unlock": "user.unlocked"}[operation]
					if fault != "none" {
						table, when := "journal_entries", "NEW.entry_type LIKE 'audit.entity_%'"
						if scope == "instance" {
							table, when = "instance_audit_logs", "NEW.action LIKE 'user.%'"
						}
						timing, body := "BEFORE", "SELECT RAISE(ABORT,'audit unavailable');"
						switch fault {
						case "audit_ignore":
							body = "SELECT RAISE(IGNORE);"
						case "mutation_undo":
							timing = "AFTER"
							body = "UPDATE user_sessions SET revoked_at=NULL,revoked_reason=NULL WHERE user_id='u-member';"
							if operation == "unlock" {
								body = "UPDATE users SET failed_login_count=5 WHERE id='u-member';"
							}
						case "commit_abort":
							timing = "AFTER"
							execOrFatal(t, f.db, `CREATE TABLE rejected_admin_commit(id TEXT REFERENCES users(id) DEFERRABLE INITIALLY DEFERRED)`)
							body = "INSERT INTO rejected_admin_commit(id) VALUES('missing-admin-audit-parent');"
						case "reason_changed":
							timing = "AFTER"
							body = "UPDATE user_sessions SET revoked_reason='logout' WHERE user_id='u-member' AND revoked_at IS NOT NULL;"
						}
						execOrFatal(t, f.db, "CREATE TRIGGER fail_admin_mutation "+timing+" INSERT ON "+table+" WHEN "+when+" BEGIN "+body+" END")
					}
					req := peopleReq("POST", "/api/v1/admin/users/u-member/sessions", "u-admin", "admin@a.test", workspace, role)
					if scope == "instance" {
						req = req.WithContext(context.WithValue(req.Context(), ctxInstanceAdmin, true))
					}
					req.SetPathValue("userId", "u-member")
					var sessionID string
					if err := f.db.QueryRow(`SELECT id FROM user_sessions WHERE user_id='u-member' ORDER BY id LIMIT 1`).Scan(&sessionID); err != nil {
						t.Fatal(err)
					}
					req.SetPathValue("sessionId", sessionID)
					writer := httptest.NewRecorder()
					map[string]http.HandlerFunc{"one": h.RevokeSession, "all": h.RevokeAllSessions, "unlock": h.Unlock}[operation](writer, req)
					wantStatus, wantActive, wantFailures, wantAudit := 204, 2, 5, 1
					if operation == "one" {
						wantActive = 1
					} else if operation == "all" {
						wantStatus, wantActive = 200, 0
					} else {
						wantFailures = 0
					}
					if fault != "none" {
						wantStatus, wantActive, wantFailures, wantAudit = 500, 2, 5, 0
					}
					var active, failures, audit int
					if err := f.db.QueryRow(`SELECT (SELECT COUNT(*) FROM user_sessions WHERE user_id='u-member' AND revoked_at IS NULL), failed_login_count FROM users WHERE id='u-member'`).Scan(&active, &failures); err != nil {
						t.Fatal(err)
					}
					query := `SELECT COUNT(*) FROM journal_entries WHERE workspace_id='ws-a' AND json_extract(payload,'$.action')=? AND seq IS NOT NULL AND entry_hash IS NOT NULL`
					if scope == "instance" {
						query = `SELECT COUNT(*) FROM instance_audit_logs WHERE action=? AND entity_id='u-member' AND chain_seq IS NOT NULL AND entry_hash IS NOT NULL`
					}
					if err := f.db.QueryRow(query, action).Scan(&audit); err != nil {
						t.Fatal(err)
					}
					if writer.Code != wantStatus || active != wantActive || failures != wantFailures || audit != wantAudit {
						t.Errorf("HTTP%d want%d active%d want%d failures%d want%d chainedAudit%d want%d", writer.Code, wantStatus, active, wantActive, failures, wantFailures, audit, wantAudit)
					}
					if fault != "none" {
						notifier.mu.Lock()
						if len(notifier.sessions)+len(notifier.users) != 0 {
							t.Error("failed mutation sent revocation notifications")
						}
						notifier.mu.Unlock()
					}
				})
			}
		}
	}
}
