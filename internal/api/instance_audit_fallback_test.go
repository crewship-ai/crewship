package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
// an append; only a broken tail refuses (and writes nothing). Rows from before
// the keyring upgrade (legacy MACs) are inherited history; a defect in a
// keyring-signed row is post_upgrade_invalid.
func TestInstanceAuditAppendChecksTailOnly(t *testing.T) {
	cases := []struct {
		name, change string
		seq          int
		want         error
		historical   string
		legacy       bool
	}{
		{"legacy middle content", "metadata='changed'", 2, nil, "historical_invalid", true},
		{"legacy genesis content", "metadata='changed'", 1, nil, "historical_invalid", true},
		{"signed middle content", "metadata='changed'", 2, nil, "post_upgrade_invalid", false},
		{"signed middle retired key", "chain_key_id='missing-old-key'", 2, nil, "post_upgrade_invalid", false},
		{"tail content", "metadata='changed'", 3, journal.ErrChainIntegrity, "", false},
		{"tail retired key", "chain_key_id='missing-tail-key'", 3, journal.ErrSigningKeyUnavailable, "", false},
		{"tail relinked", "prev_hash='forged'", 3, journal.ErrChainIntegrity, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			history := t.Context()
			if tc.legacy {
				history = journal.WithSigningKeys(history, journal.LegacySigningKeys(journal.ChainKeyFromEnv()))
			}
			for range 3 {
				if err := auditInstance(history, nil, db, "fixture.authentic", "instance", "", "", nil); err != nil {
					t.Fatal(err)
				}
			}
			if tc.legacy {
				// The upgraded writer's first row is the authentic tail.
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

// Revocations and suspension still complete when the instance chain tail is
// broken; their record is a marked unchained fallback. Token creation and a
// password change (whoever sets a password gains access with it) stay
// refused and leave no record; the password change says why (503).
func TestSecurityActionsSurviveBrokenInstanceTail(t *testing.T) {
	cases := []struct {
		door    string
		actions []string
		refused int
	}{
		{"revoke", []string{"auth.cli_token_revoked"}, 0},
		{"suspend", []string{"auth.cli_token_revoked", "instance.user_suspended"}, 0},
		{"password", []string{"auth.cli_token_revoked", "auth.password_changed"}, http.StatusServiceUnavailable},
		{"create", []string{"auth.cli_token_created"}, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.door, func(t *testing.T) {
			f := newTokenAuditFixture(t, tc.door)
			breakInstanceAuditTail(t, f.db)
			rr := f.invoke()
			if tc.refused != 0 {
				if rr.Code != tc.refused {
					t.Fatalf("access-granting action with broken chain: HTTP %d want %d", rr.Code, tc.refused)
				}
				f.assertMutation(t, tc.door, false)
				for _, action := range tc.actions {
					if chained, fallback := instanceAuditRows(t, f.db, action); chained+fallback != 0 {
						t.Fatalf("refused action left a %s record", action)
					}
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

// Web reset sets a password, so it fails closed with a clear 503 and changes
// nothing — not even the CLI-token revocation that recovery performs in the
// same transaction. Host-side local recovery (shell access to the server)
// completes: tokens revoked and both records written as unchained fallback.
func TestPasswordResetWithBrokenInstanceTail(t *testing.T) {
	t.Run("web", func(t *testing.T) {
		h, store, user := newProfileRig(t)
		if _, err := store.Create(t.Context(), user, "stolen", "", time.Hour); err != nil {
			t.Fatal(err)
		}
		execOrFatal(t, h.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier) VALUES('reset-token',?,'old-password-token','synthetic-reset-fingerprint','STANDARD')`, user)
		recovery := newRecoveryHandler(t, h.db, &stubMailer{configured: true}, h.sessions)
		covARMintToken(t, recovery, "me@example.com", "broken-tail-reset")
		breakInstanceAuditTail(t, h.db)
		req, rr := covARPost("POST", "/api/v1/auth/reset", `{"token":"broken-tail-reset","new_password":"newpassword123"}`)
		recovery.Reset(rr, req)
		if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), "audit trail") {
			t.Fatalf("web reset with broken chain: HTTP %d %s, want 503 naming the audit trail", rr.Code, rr.Body.String())
		}
		var active int
		if err := h.db.QueryRow(`SELECT COUNT(*) FROM user_sessions WHERE user_id=? AND revoked_at IS NULL`, user).Scan(&active); err != nil || active != 1 {
			t.Fatalf("refused reset changed sessions: %d active %v", active, err)
		}
		var revoked sql.NullString
		if err := h.db.QueryRow(`SELECT revoked_at FROM cli_tokens WHERE id='reset-token'`).Scan(&revoked); err != nil || revoked.Valid {
			t.Fatalf("refused reset revoked the CLI token: %v %v", revoked, err)
		}
		for _, action := range []string{"auth.password_reset", "auth.cli_token_revoked"} {
			if chained, fallback := instanceAuditRows(t, h.db, action); chained+fallback != 0 {
				t.Fatalf("refused reset left a %s record: chained=%d fallback=%d", action, chained, fallback)
			}
		}
	})
	t.Run("local", func(t *testing.T) {
		db := setupTestDB(t)
		user := seedTestUserWithPassword(t, db, "local@example.com", "oldpassword1")
		execOrFatal(t, db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier) VALUES('local-token',?,'old-password-token','synthetic-local-fingerprint','STANDARD')`, user)
		breakInstanceAuditTail(t, db)
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback() //nolint:errcheck
		if _, err := tx.Exec(`UPDATE users SET hashed_password='synthetic-reset',failed_login_count=0,locked_until=NULL,last_failed_login_at=NULL WHERE id=?`, user); err != nil {
			t.Fatal(err)
		}
		// The order `crewship admin reset-password` uses.
		tokens, err := RevokeRecoveryCLITokensTx(t.Context(), tx, "", user, "2026-10-22T00:00:00Z")
		if err != nil || len(tokens) != 1 {
			t.Fatalf("local recovery token revocation held hostage: %v %v", tokens, err)
		}
		if err := AuditLocalPasswordResetTx(t.Context(), tx, user, "synthetic-reset", "2026-10-22T00:00:00Z", 0, len(tokens)); err != nil {
			t.Fatalf("local reset held hostage by audit chain: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		for _, action := range []string{"auth.password_reset", "auth.cli_token_revoked"} {
			if chained, fallback := instanceAuditRows(t, db, action); chained != 0 || fallback != 1 {
				t.Fatalf("%s: chained=%d fallback=%d, want one fallback row", action, chained, fallback)
			}
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
