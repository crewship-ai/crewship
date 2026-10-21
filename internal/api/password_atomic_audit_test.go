package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestPasswordMutationAtomicAudit(t *testing.T) {
	for _, door := range []string{"change", "reset"} {
		faults := []string{"none", "audit", "session", "audit_undo", "audit_new_session"}
		if door == "change" {
			faults = append(faults, "audit_new_token")
		}
		for _, fault := range faults {
			t.Run(door+"/"+fault, func(t *testing.T) {
				h, store, user := newProfileRig(t)
				notifier := &recordingNotifier{}
				h.sessions = newNotifyingSessionStore(store, notifier)
				current, err := store.Create(t.Context(), user, "current", "", time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				sibling, err := store.Create(t.Context(), user, "sibling", "", time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				var expiredID string
				if door == "change" && fault == "none" {
					expired, err := store.Create(t.Context(), user, "expired", "", time.Hour)
					if err != nil {
						t.Fatal(err)
					}
					expiredID = expired.ID
					execOrFatal(t, h.db, `UPDATE user_sessions SET expires_at='2000-01-01T00:00:00Z' WHERE id=?`, expiredID)
				}
				action := "auth.password_changed"
				invoke := func() *httptest.ResponseRecorder {
					rr := httptest.NewRecorder()
					h.ChangePassword(rr, selfReq(t, "POST", "/api/v1/users/me/password", map[string]any{"current_password": "oldpassword1", "new_password": "newpassword123", "revoke_cli_tokens": fault == "audit_new_token"}, user, current.ID))
					return rr
				}
				if door == "reset" {
					action = "auth.password_reset"
					recovery := newRecoveryHandler(t, h.db, &stubMailer{configured: true}, h.sessions)
					covARMintToken(t, recovery, "me@example.com", "atomic-reset-fixture")
					invoke = func() *httptest.ResponseRecorder {
						req, rr := covARPost("POST", "/api/v1/auth/reset", `{"token":"atomic-reset-fixture","new_password":"newpassword123"}`)
						recovery.Reset(rr, req)
						return rr
					}
				}
				if fault == "audit" {
					execOrFatal(t, h.db, fmt.Sprintf(`CREATE TRIGGER fail_password_audit BEFORE INSERT ON instance_audit_logs WHEN NEW.action=%q BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`, action))
				}
				if fault == "audit_undo" {
					var oldHash string
					if err := h.db.QueryRow(`SELECT hashed_password FROM users WHERE id=?`, user).Scan(&oldHash); err != nil {
						t.Fatal(err)
					}
					execOrFatal(t, h.db, `CREATE TABLE atomic_reset_copy AS SELECT * FROM verification_tokens`)
					execOrFatal(t, h.db, fmt.Sprintf(`CREATE TRIGGER undo_password_after_audit AFTER INSERT ON instance_audit_logs WHEN NEW.action=%q BEGIN UPDATE users SET hashed_password=CAST(X'%x' AS TEXT) WHERE id='test-user-recovery'; UPDATE user_sessions SET revoked_at=NULL,revoked_reason=NULL WHERE user_id='test-user-recovery'; INSERT OR IGNORE INTO verification_tokens SELECT * FROM atomic_reset_copy; END`, action, []byte(oldHash)))
				}
				if fault == "audit_new_session" {
					execOrFatal(t, h.db, fmt.Sprintf(`CREATE TRIGGER insert_session_after_password_audit AFTER INSERT ON instance_audit_logs WHEN NEW.action=%q BEGIN INSERT INTO user_sessions(id,user_id,expires_at) VALUES('unexpected-session','test-user-recovery',datetime('now','+1 hour')); END`, action))
				}
				if fault == "audit_new_token" {
					execOrFatal(t, h.db, `CREATE TRIGGER insert_token_after_password_audit AFTER INSERT ON instance_audit_logs WHEN NEW.action='auth.password_changed' BEGIN INSERT INTO cli_tokens(id,user_id,name,token_hash,tier) VALUES('unexpected-token','test-user-recovery','fixture','synthetic-fingerprint','STANDARD'); END`)
				}
				if fault == "session" {
					execOrFatal(t, h.db, `CREATE TRIGGER fail_password_session BEFORE UPDATE OF revoked_at ON user_sessions BEGIN SELECT RAISE(ABORT,'session unavailable'); END`)
				}
				rr := invoke()
				wantStatus, password := http.StatusOK, "newpassword123"
				if fault != "none" {
					wantStatus, password = http.StatusInternalServerError, "oldpassword1"
				}
				if rr.Code != wantStatus {
					t.Errorf("HTTP=%d want=%d body=%s", rr.Code, wantStatus, rr.Body.String())
				}
				var hash string
				if err := h.db.QueryRow(`SELECT hashed_password FROM users WHERE id=?`, user).Scan(&hash); err != nil {
					t.Fatal(err)
				}
				if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
					t.Error("password did not commit/roll back with sessions and audit")
				}
				for _, id := range []string{current.ID, sibling.ID} {
					s, err := store.Get(t.Context(), id)
					if err != nil {
						t.Fatal(err)
					}
					wantRevoked := fault == "none" && (door == "reset" || id == sibling.ID)
					if (s.RevokedAt != nil) != wantRevoked {
						t.Errorf("session %s revoked=%v want=%v", id, s.RevokedAt != nil, wantRevoked)
					}
				}
				if expiredID != "" {
					expired, err := store.Get(t.Context(), expiredID)
					if err != nil {
						t.Fatal(err)
					}
					if expired.RevokedAt != nil {
						t.Error("password change unnecessarily revoked an expired sibling")
					}
				}
				if fault == "audit_new_session" {
					var inserted int
					if err := h.db.QueryRow(`SELECT COUNT(*) FROM user_sessions WHERE id='unexpected-session'`).Scan(&inserted); err != nil {
						t.Fatal(err)
					}
					if inserted != 0 {
						t.Error("new session from final audit did not roll back")
					}
				}
				if fault == "audit_new_token" {
					var inserted int
					if err := h.db.QueryRow(`SELECT COUNT(*) FROM cli_tokens WHERE id='unexpected-token'`).Scan(&inserted); err != nil {
						t.Fatal(err)
					}
					if inserted != 0 {
						t.Error("new token from final audit did not roll back")
					}
				}
				notifier.mu.Lock()
				wantSignals := 0
				if fault == "none" {
					wantSignals = 1
					if door == "reset" {
						wantSignals = 2
					}
				}
				if len(notifier.sessions) != wantSignals || len(notifier.users) != 0 {
					t.Errorf("session notifications=%v user notifications=%v want %d exact sessions", notifier.sessions, notifier.users, wantSignals)
				}
				notifier.mu.Unlock()
				var count int
				if err := h.db.QueryRow(`SELECT COUNT(*) FROM instance_audit_logs WHERE action=? AND user_id=? AND entity_type='user' AND entity_id=? AND chain_seq IS NOT NULL AND entry_hash IS NOT NULL`, action, user, user).Scan(&count); err != nil {
					t.Fatal(err)
				}
				wantCount := 0
				if fault == "none" {
					wantCount = 1
				}
				if count != wantCount {
					t.Errorf("chained password audit=%d want=%d", count, wantCount)
				}
				if door == "reset" {
					if err := h.db.QueryRow(`SELECT COUNT(*) FROM verification_tokens WHERE token=?`, hashResetToken("atomic-reset-fixture")).Scan(&count); err != nil {
						t.Fatal(err)
					}
					wantCount = 1
					if fault == "none" {
						wantCount = 0
					}
					if count != wantCount {
						t.Errorf("one-time token count=%d want=%d", count, wantCount)
					}
				}
			})
		}
	}
}

func TestPasswordMutationDefaultSessionStore(t *testing.T) {
	for _, door := range []string{"change", "reset"} {
		t.Run(door, func(t *testing.T) {
			rig, store, user := newProfileRig(t)
			sibling, err := store.Create(t.Context(), user, "sibling", "", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			rr := httptest.NewRecorder()
			if door == "change" {
				h := NewUserProfileHandler(rig.db, rig.logger, nil)
				h.ChangePassword(rr, selfReq(t, "POST", "/api/v1/users/me/password", map[string]string{"current_password": "oldpassword1", "new_password": "newpassword123"}, user, ""))
			} else {
				h := newRecoveryHandler(t, rig.db, &stubMailer{configured: true}, nil)
				covARMintToken(t, h, "me@example.com", "default-store-proof")
				req, response := covARPost("POST", "/api/v1/auth/reset", `{"token":"default-store-proof","new_password":"newpassword123"}`)
				rr = response
				h.Reset(rr, req)
			}
			if rr.Code != http.StatusOK {
				t.Fatalf("HTTP=%d want200", rr.Code)
			}
			row, err := store.Get(t.Context(), sibling.ID)
			if err != nil {
				t.Fatal(err)
			}
			if row.RevokedAt == nil {
				t.Error("default session store left prior browser session active")
			}
		})
	}
}
