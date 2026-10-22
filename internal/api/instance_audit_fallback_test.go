package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/journal"
)

// breakInstanceAuditTail appends one authentic row and then edits it, so the
// chain tail no longer authenticates.
func breakInstanceAuditTail(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := auditInstance(t.Context(), nil, db, "fixture.authentic", "instance", "", "", nil); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, db, `UPDATE instance_audit_logs SET metadata='{"tampered":true}' WHERE chain_seq=(SELECT MAX(chain_seq) FROM instance_audit_logs)`)
}

func instanceAuditRows(t *testing.T, db *sql.DB, action string) (chained, fallback int) {
	t.Helper()
	if err := db.QueryRow(`SELECT
		COUNT(*) FILTER (WHERE chain_seq IS NOT NULL),
		COUNT(*) FILTER (WHERE chain_seq IS NULL AND prev_hash IS NULL AND entry_hash IS NULL AND json_extract(metadata,'$.audit_chain')='unchained_fallback')
		FROM instance_audit_logs WHERE action=?`, action).Scan(&chained, &fallback); err != nil {
		t.Fatal(err)
	}
	return chained, fallback
}

// Middle-of-history defects are reported by full verification and never block
// an append; only a broken tail refuses (and writes nothing).
func TestInstanceAuditAppendChecksTailOnly(t *testing.T) {
	cases := []struct {
		name, change string
		seq          int
		want         error
		historical   string
	}{
		{"middle content", "metadata='changed'", 2, nil, "historical_invalid"},
		{"middle retired key", "chain_key_id='missing-old-key'", 2, nil, "historical_unverified"},
		{"genesis content", "metadata='changed'", 1, nil, "historical_invalid"},
		{"tail content", "metadata='changed'", 3, journal.ErrChainIntegrity, ""},
		{"tail retired key", "chain_key_id='missing-tail-key'", 3, journal.ErrSigningKeyUnavailable, ""},
		{"tail relinked", "prev_hash='forged'", 3, journal.ErrChainIntegrity, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			for range 3 {
				if err := auditInstance(t.Context(), nil, db, "fixture.authentic", "instance", "", "", nil); err != nil {
					t.Fatal(err)
				}
			}
			execOrFatal(t, db, "UPDATE instance_audit_logs SET "+tc.change+" WHERE chain_seq=?", tc.seq)
			err := auditInstance(t.Context(), nil, db, "fixture.next", "instance", "", "", nil)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("historical defect blocked append: %v", err)
				}
				keys, err := journal.SigningKeysForContext(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				result, err := journal.VerifyInstanceAuditWithKeys(t.Context(), db, keys)
				if err != nil || result.OK || !result.ContinuationVerified || result.HistoricalStatus != tc.historical {
					t.Fatalf("historical defect not reported as %s: %+v %v", tc.historical, result, err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("append onto broken tail: %v want %v", err, tc.want)
			}
			if chained, _ := instanceAuditRows(t, db, "fixture.next"); chained != 0 {
				t.Fatal("refused append changed history")
			}
		})
	}
}

// Revocations, suspension and password change/reset still complete when the
// instance chain tail is broken; their record is a marked unchained fallback.
// Token creation (access-granting) stays refused and leaves no record.
func TestSecurityActionsSurviveBrokenInstanceTail(t *testing.T) {
	cases := []struct {
		door    string
		actions []string
		granted bool
	}{
		{"revoke", []string{"auth.cli_token_revoked"}, false},
		{"password", []string{"auth.cli_token_revoked", "auth.password_changed"}, false},
		{"suspend", []string{"auth.cli_token_revoked", "instance.user_suspended"}, false},
		{"create", []string{"auth.cli_token_created"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.door, func(t *testing.T) {
			f := newTokenAuditFixture(t, tc.door)
			breakInstanceAuditTail(t, f.db)
			rr := f.invoke()
			if tc.granted {
				if rr.Code != http.StatusInternalServerError {
					t.Fatalf("access-granting action bypassed broken chain: HTTP %d", rr.Code)
				}
				f.assertMutation(t, tc.door, false)
				if chained, fallback := instanceAuditRows(t, f.db, tc.actions[0]); chained+fallback != 0 {
					t.Fatal("refused grant left an audit record")
				}
				return
			}
			if rr.Code != http.StatusOK {
				t.Fatalf("security action held hostage by audit chain: HTTP %d %s", rr.Code, rr.Body.String())
			}
			f.assertMutation(t, tc.door, true)
			for _, action := range tc.actions {
				if chained, fallback := instanceAuditRows(t, f.db, action); chained != 0 || fallback != 1 {
					t.Errorf("%s: chained=%d fallback=%d, want one fallback row", action, chained, fallback)
				}
			}
			keys, err := journal.SigningKeysForContext(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			result, err := journal.VerifyInstanceAuditWithKeys(t.Context(), f.db, keys)
			if err != nil || result.OK || result.UnchainedFallback != len(tc.actions) {
				t.Fatalf("fallback not reported by verification: %+v %v", result, err)
			}
		})
	}
}

func TestPasswordResetSurvivesBrokenInstanceTail(t *testing.T) {
	t.Run("web", func(t *testing.T) {
		h, store, user := newProfileRig(t)
		if _, err := store.Create(t.Context(), user, "stolen", "", time.Hour); err != nil {
			t.Fatal(err)
		}
		recovery := newRecoveryHandler(t, h.db, &stubMailer{configured: true}, h.sessions)
		covARMintToken(t, recovery, "me@example.com", "broken-tail-reset")
		breakInstanceAuditTail(t, h.db)
		req, rr := covARPost("POST", "/api/v1/auth/reset", `{"token":"broken-tail-reset","new_password":"newpassword123"}`)
		recovery.Reset(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("password reset held hostage by audit chain: HTTP %d %s", rr.Code, rr.Body.String())
		}
		var active int
		if err := h.db.QueryRow(`SELECT COUNT(*) FROM user_sessions WHERE user_id=? AND revoked_at IS NULL`, user).Scan(&active); err != nil || active != 0 {
			t.Fatalf("reset left %d active sessions: %v", active, err)
		}
		if chained, fallback := instanceAuditRows(t, h.db, "auth.password_reset"); chained != 0 || fallback != 1 {
			t.Fatalf("chained=%d fallback=%d, want one fallback row", chained, fallback)
		}
	})
	t.Run("local", func(t *testing.T) {
		db := setupTestDB(t)
		user := seedTestUserWithPassword(t, db, "local@example.com", "oldpassword1")
		breakInstanceAuditTail(t, db)
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback() //nolint:errcheck
		if _, err := tx.Exec(`UPDATE users SET hashed_password='synthetic-reset',failed_login_count=0,locked_until=NULL,last_failed_login_at=NULL WHERE id=?`, user); err != nil {
			t.Fatal(err)
		}
		if err := AuditLocalPasswordResetTx(t.Context(), tx, user, "synthetic-reset", "2026-10-22T00:00:00Z", 0); err != nil {
			t.Fatalf("local reset held hostage by audit chain: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if chained, fallback := instanceAuditRows(t, db, "auth.password_reset"); chained != 0 || fallback != 1 {
			t.Fatalf("chained=%d fallback=%d, want one fallback row", chained, fallback)
		}
	})
}

func TestAdminSessionRevokeSurvivesBrokenInstanceTail(t *testing.T) {
	for _, operation := range []string{"one", "all", "unlock"} {
		t.Run(operation, func(t *testing.T) {
			f := newPeopleFixture(t)
			t.Setenv(backup.InstanceOwnerEmailEnv, "")
			execOrFatal(t, f.db, `UPDATE users SET instance_role='ADMIN' WHERE id='u-admin'`)
			breakInstanceAuditTail(t, f.db)
			h := NewAdminUsersHandler(f.db, newTestLogger(), f.store)
			req := peopleReq("POST", "/api/v1/admin/users/u-member/sessions", "u-admin", "admin@a.test", "", "")
			req = req.WithContext(context.WithValue(req.Context(), ctxInstanceAdmin, true))
			req.SetPathValue("userId", "u-member")
			var sessionID string
			if err := f.db.QueryRow(`SELECT id FROM user_sessions WHERE user_id='u-member' ORDER BY id LIMIT 1`).Scan(&sessionID); err != nil {
				t.Fatal(err)
			}
			req.SetPathValue("sessionId", sessionID)
			rr := httptest.NewRecorder()
			map[string]http.HandlerFunc{"one": h.RevokeSession, "all": h.RevokeAllSessions, "unlock": h.Unlock}[operation](rr, req)
			action := map[string]string{"one": "user.session_revoked", "all": "user.sessions_revoked", "unlock": "user.unlocked"}[operation]
			chained, fallback := instanceAuditRows(t, f.db, action)
			if operation == "unlock" {
				// Unlock restores access: it stays behind the chain.
				if rr.Code != http.StatusInternalServerError || chained+fallback != 0 {
					t.Fatalf("unlock bypassed broken chain: HTTP %d chained=%d fallback=%d", rr.Code, chained, fallback)
				}
				return
			}
			if rr.Code >= 300 {
				t.Fatalf("admin revoke held hostage by audit chain: HTTP %d %s", rr.Code, rr.Body.String())
			}
			if chained != 0 || fallback != 1 {
				t.Fatalf("chained=%d fallback=%d, want one fallback row", chained, fallback)
			}
		})
	}
}
