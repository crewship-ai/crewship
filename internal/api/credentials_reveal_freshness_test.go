package api

import (
	"net/http"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestReveal_FreshnessCannotBeRenewedByActivity(t *testing.T) {
	for _, mutate := range []string{
		`UPDATE user_sessions SET created_at='2020-01-01T00:00:00Z'`,
		`UPDATE user_sessions SET created_at='2099-01-01T00:00:00Z'`,
		`UPDATE user_sessions SET revoked_at='2026-01-01T00:00:00Z'`,
		`UPDATE user_sessions SET expires_at='2020-01-01T00:00:00Z'`,
		`DELETE FROM user_sessions`,
	} {
		t.Run(mutate, func(t *testing.T) {
			r := revealAuthPathRig(t)
			if _, err := r.db.Exec(mutate); err != nil {
				t.Fatal(err)
			}
			if _, err := r.db.Exec(`UPDATE user_sessions SET last_used_at=?`, time.Now().UTC().Format(time.RFC3339)); err != nil {
				t.Fatal(err)
			}
			rec := r.doReveal(r.revealReq("cred-1", "ws-auth", "u-owner", "OWNER", validRevealReason))
			if rec.Code != http.StatusForbidden || revealValue(t, rec) != "" {
				t.Fatal("stale/revoked session revealed a secret")
			}
			if len(r.j.all()) != 0 {
				t.Fatal("denied request recorded as successful reveal")
			}
		})
	}
}

func TestReveal_DefaultPasswordDenied(t *testing.T) {
	r := revealAuthPathRig(t)
	hash, err := bcrypt.GenerateFromPassword([]byte(seedAccountPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.db.Exec("UPDATE users SET hashed_password=? WHERE id='u-owner'", string(hash)); err != nil {
		t.Fatal(err)
	}
	rec := r.doReveal(r.revealReq("cred-1", "ws-auth", "u-owner", "OWNER", validRevealReason))
	if rec.Code != http.StatusForbidden || revealValue(t, rec) != "" {
		t.Fatal("default password account revealed a secret")
	}
}
