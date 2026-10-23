//go:build !clionly

package main

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/journal"
)

// Real local recovery against a disposable migrated file, with no server or
// in-process stream registry. This cannot prove a live socket disconnect.
func TestAdminLocalPasswordResetAtomicAudit(t *testing.T) {
	for _, fault := range []string{"none", "audit_abort", "session_ignore", "password_undo", "token_audit"} {
		t.Run(fault, func(t *testing.T) {
			dbURL := initTestDB(t)
			t.Setenv("DATABASE_URL", dbURL)
			db, err := database.Open(dbURL)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			oldHash, err := bcrypt.GenerateFromPassword([]byte("old-local-password"), bcrypt.MinCost)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO users(id,email,hashed_password,failed_login_count,locked_until,last_failed_login_at,updated_at)
VALUES('local-reset-user','local-reset@example.invalid',?,5,'2099-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
INSERT INTO user_sessions(id,user_id,expires_at,created_at,last_used_at)
VALUES('local-reset-session','local-reset-user','2099-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('local-reset-token','local-reset-user','cli','local-reset-fingerprint','2026-01-01T00:00:00Z')`, string(oldHash)); err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "token_audit":
				_, err = db.Exec(`CREATE TRIGGER local_reset_token_abort BEFORE INSERT ON instance_audit_logs
WHEN NEW.action='auth.cli_token_revoked' BEGIN SELECT RAISE(ABORT,'synthetic audit failure'); END`)
			case "audit_abort":
				_, err = db.Exec(`CREATE TRIGGER local_reset_audit_abort BEFORE INSERT ON instance_audit_logs
WHEN NEW.action='auth.password_reset' BEGIN SELECT RAISE(ABORT,'synthetic audit failure'); END`)
			case "session_ignore":
				_, err = db.Exec(`CREATE TRIGGER local_reset_session_ignore BEFORE UPDATE OF revoked_at ON user_sessions
WHEN OLD.id='local-reset-session' BEGIN SELECT RAISE(IGNORE); END`)
			case "password_undo":
				_, err = db.Exec(`CREATE TABLE local_reset_expected(hash TEXT NOT NULL);
INSERT INTO local_reset_expected(hash) VALUES(?);
CREATE TRIGGER local_reset_password_undo AFTER UPDATE OF revoked_at ON user_sessions
WHEN NEW.id='local-reset-session' AND NEW.revoked_at IS NOT NULL
BEGIN UPDATE users SET hashed_password=(SELECT hash FROM local_reset_expected) WHERE id='local-reset-user'; END`, string(oldHash))
			}
			if err != nil {
				t.Fatal(err)
			}
			cmd, out := newAdminCovCmd(runAdminResetPassword)
			cmd.SetIn(strings.NewReader("new-local-password\n"))
			cmd.SetArgs([]string{"--local", "--email=local-reset@example.invalid", "--password-stdin"})
			err = cmd.Execute()
			if (err != nil) != (fault != "none") {
				t.Errorf("local reset error=%v; fault=%s", err != nil, fault)
			}
			var hash, updated string
			var failed int
			var locked, lastFailed, revoked, reason sql.NullString
			if err := db.QueryRow(`SELECT hashed_password,failed_login_count,locked_until,last_failed_login_at,updated_at FROM users WHERE id='local-reset-user'`).Scan(&hash, &failed, &locked, &lastFailed, &updated); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT revoked_at,revoked_reason FROM user_sessions WHERE id='local-reset-session'`).Scan(&revoked, &reason); err != nil {
				t.Fatal(err)
			}
			var audits, tokenAudits int
			if err := db.QueryRow(`SELECT count(*) FROM instance_audit_logs WHERE action='auth.password_reset'`).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM instance_audit_logs WHERE action='auth.cli_token_revoked' AND entity_id='local-reset-token' AND user_id IS NULL`).Scan(&tokenAudits); err != nil {
				t.Fatal(err)
			}
			var tokenRevoked sql.NullString
			if err := db.QueryRow(`SELECT revoked_at FROM cli_tokens WHERE id='local-reset-token'`).Scan(&tokenRevoked); err != nil {
				t.Fatal(err)
			}
			if fault != "none" {
				if hash != string(oldHash) || failed != 5 || !locked.Valid || locked.String != "2099-01-01T00:00:00Z" || !lastFailed.Valid || lastFailed.String != "2026-01-01T00:00:00Z" || updated != "2026-01-01T00:00:00Z" || revoked.Valid || reason.Valid || audits != 0 || tokenRevoked.Valid || tokenAudits != 0 {
					t.Error("failed local reset did not preserve password, lockout, session and audit together")
				}
				if strings.Contains(out.String(), "Password reset for") {
					t.Error("failed local reset printed success")
				}
				return
			}
			if bcrypt.CompareHashAndPassword([]byte(hash), []byte("new-local-password")) != nil || failed != 0 || locked.Valid || lastFailed.Valid || !revoked.Valid || reason.String != "password_change" || !tokenRevoked.Valid || tokenAudits != 1 {
				t.Fatal("healthy local reset failed business calibration")
			}
			if audits != 1 {
				t.Fatalf("chained local password reset audit=%d want1", audits)
			}
			var f journal.InstanceAuditFields
			var seq int64
			var previous, mac string
			var keyID sql.NullString
			var version sql.NullInt64
			if err := db.QueryRow(`SELECT id,user_id,action,entity_type,entity_id,target_workspace_id,metadata,ip_address,user_agent,created_at,chain_seq,prev_hash,entry_hash,chain_key_id,chain_mac_version FROM instance_audit_logs WHERE action='auth.password_reset'`).Scan(&f.ID, &f.UserID, &f.Action, &f.EntityType, &f.EntityID, &f.TargetWorkspaceID, &f.Metadata, &f.IPAddress, &f.UserAgent, &f.CreatedAt, &seq, &previous, &mac, &keyID, &version); err != nil {
				t.Fatal(err)
			}
			target, err := resolveLocalDBTarget()
			if err != nil {
				t.Fatal(err)
			}
			ctx, err := localAuditSigningContext(t.Context(), target)
			if err != nil {
				t.Fatal(err)
			}
			keys, err := journal.SigningKeysForContext(ctx)
			if err != nil {
				t.Fatal(err)
			}
			wanted, err := journal.InstanceAuditMACForKeys(keys, keyID, version, seq, previous, f)
			if err != nil || !keyID.Valid || !version.Valid || version.Int64 != 2 {
				t.Fatalf("local audit key identity: %v", err)
			}
			var metadata map[string]any
			if json.Unmarshal([]byte(f.Metadata.String), &metadata) != nil || len(metadata) != 4 || metadata["access_revocation_marked"] != true || metadata["source"] != "local_recovery" || metadata["sessions_revoked"] != float64(1) || metadata["cli_tokens_revoked"] != float64(1) || f.UserID.Valid || f.EntityType != "user" || f.EntityID.String != "local-reset-user" || f.TargetWorkspaceID.Valid || f.IPAddress.Valid || f.UserAgent.Valid || seq != 2 || mac != wanted {
				t.Error("local reset lost system attribution, nonsecret counts or configured chain integrity")
			}
		})
	}
}
