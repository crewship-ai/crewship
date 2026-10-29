package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Account recovery is the moment a stolen credential must stop working. The
// emailed reset revokes every CLI token of the account in the same commit as
// the password, its sessions and the audit chain, and closes open token
// streams only after that commit.
func TestPasswordResetRevokesCLITokens(t *testing.T) {
	for _, fault := range []string{"none", "token_audit"} {
		t.Run(fault, func(t *testing.T) {
			rig := newInstanceFixture(t)
			carol := mintTokenFor(t, rig.db, "carol", "resetcarol000000000000000000")
			mustExec(t, rig.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at,revoked_at) VALUES('clt-carol-old','carol','old','old-fingerprint',datetime('now'),'2020-01-01T00:00:00Z')`)
			if rr := rig.do(carol, "GET", "/api/v1/auth/sessions", ""); rr.Code != http.StatusOK {
				t.Fatalf("calibration: token rejected before reset: %d", rr.Code)
			}
			stream, release := rig.r.authMw.streams.Track(t.Context(), rig.r.authMw.streams.Epoch(), "carol", "", "clt-carol", time.Time{})
			t.Cleanup(release)
			mustExec(t, rig.db, `INSERT INTO verification_tokens(identifier,token,expires,purpose) VALUES('carol@ex.com',?,?,'password_reset')`,
				hashResetToken("carol-reset-proof"), time.Now().UTC().Add(time.Hour).Format(time.RFC3339))
			if fault == "token_audit" {
				mustExec(t, rig.db, `CREATE TRIGGER fail_reset_token_audit BEFORE INSERT ON instance_audit_logs WHEN NEW.action='auth.cli_token_revoked' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
			}

			req := httptest.NewRequest("POST", "/api/v1/auth/reset", strings.NewReader(`{"token":"carol-reset-proof","new_password":"carol-new-password"}`))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			rig.r.ServeHTTP(rr, req)

			var active, oldKept, tokenAudits int
			if err := rig.db.QueryRow(`SELECT COUNT(*) FROM cli_tokens WHERE user_id='carol' AND revoked_at IS NULL`).Scan(&active); err != nil {
				t.Fatal(err)
			}
			if err := rig.db.QueryRow(`SELECT COUNT(*) FROM cli_tokens WHERE id='clt-carol-old' AND revoked_at='2020-01-01T00:00:00Z'`).Scan(&oldKept); err != nil {
				t.Fatal(err)
			}
			if err := rig.db.QueryRow(`SELECT COUNT(*) FROM instance_audit_logs WHERE action='auth.cli_token_revoked' AND entity_id='clt-carol' AND user_id='carol' AND chain_seq IS NOT NULL`).Scan(&tokenAudits); err != nil {
				t.Fatal(err)
			}
			if oldKept != 1 {
				t.Error("reset rewrote an already revoked token")
			}
			if fault != "none" {
				if rr.Code != http.StatusInternalServerError || active != 1 || tokenAudits != 0 || stream.Err() != nil {
					t.Errorf("failed reset leaked a partial token revocation: HTTP=%d active=%d audits=%d stream=%v", rr.Code, active, tokenAudits, stream.Err())
				}
				return
			}
			if rr.Code != http.StatusOK {
				t.Fatalf("HTTP=%d body=%s", rr.Code, rr.Body.String())
			}
			if active != 0 || tokenAudits != 1 {
				t.Errorf("reset left CLI tokens active=%d token audits=%d", active, tokenAudits)
			}
			var raw string
			if err := rig.db.QueryRow(`SELECT metadata FROM instance_audit_logs WHERE action='auth.password_reset' AND entity_id='carol'`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var meta map[string]int
			if err := json.Unmarshal([]byte(raw), &meta); err != nil || meta["cli_tokens_revoked"] != 1 {
				t.Errorf("password reset audit metadata=%s", raw)
			}
			select {
			case <-stream.Done():
			case <-time.After(5 * time.Second):
				t.Error("open CLI token stream survived the committed reset")
			}
			if rr := rig.do(carol, "GET", "/api/v1/auth/sessions", ""); rr.Code != http.StatusUnauthorized {
				t.Errorf("revoked token still authenticates: %d", rr.Code)
			}
		})
	}
}
