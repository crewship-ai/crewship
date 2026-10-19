package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

type tokenAuditFixture struct {
	db                   *sql.DB
	actor, owner, action string
	create               bool
	invoke               func() *httptest.ResponseRecorder
	stream               context.Context
}

func newTokenAuditFixture(t *testing.T, door string) tokenAuditFixture {
	t.Helper()
	f := tokenAuditFixture{action: "auth.cli_token_revoked"}
	switch door {
	case "bootstrap":
		f.db = setupTestDB(t)
		f.create = true
		f.action = "auth.cli_token_created"
		h := NewAuthHandler(f.db, newTestLogger(), newTestJWTValidator(t), sessions.NewDBStore(f.db), false)
		armBootstrapForTest(t, h)
		f.invoke = func() *httptest.ResponseRecorder {
			r := httptest.NewRequest("POST", "/api/v1/auth/bootstrap", strings.NewReader(`{"full_name":"Audit Owner","email":"audit-owner@example.com","password":"audit-password123"}`))
			rr := httptest.NewRecorder()
			h.Bootstrap(rr, r)
			return rr
		}
	case "pair":
		f.db = setupTestDB(t)
		f.owner = seedTestUser(t, f.db)
		f.actor = f.owner
		f.create = true
		f.action = "auth.cli_token_created"
		h := newPairHandler(t, f.db)
		covCPSeedPairing(t, h, f.owner, "AUDT-PAIR")
		f.invoke = func() *httptest.ResponseRecorder { return covCPRedeem(h, "AUDT-PAIR") }
	case "suspend":
		rig := newInstanceFixture(t)
		f.db = rig.db
		f.actor = "boss"
		f.owner = "carol"
		f.invoke = func() *httptest.ResponseRecorder {
			return rig.do(rig.boss, "POST", "/api/v1/admin/instance/people/carol/suspend", `{"reason":"audit test"}`)
		}
		var release func()
		f.stream, release = rig.r.authMw.streams.Track(t.Context(), rig.r.authMw.streams.Epoch(), f.owner, "", "f1-token", time.Time{})
		t.Cleanup(release)
	case "password":
		h, _, user := newProfileRig(t)
		f.db = h.db
		f.owner = user
		f.actor = user
		h.streams = &auth.StreamCredentials{}
		var release func()
		f.stream, release = h.streams.Track(t.Context(), h.streams.Epoch(), user, "", "f1-token", time.Time{})
		t.Cleanup(release)
		f.invoke = func() *httptest.ResponseRecorder {
			rr := httptest.NewRecorder()
			h.ChangePassword(rr, selfReq(t, "POST", "/api/v1/users/me/password", map[string]any{"current_password": "oldpassword1", "new_password": "newpassword123", "revoke_cli_tokens": true}, user, ""))
			return rr
		}
	default:
		f.db = setupTestDB(t)
		f.owner = seedTestUser(t, f.db)
		f.actor = f.owner
		h := NewCLITokenHandler(f.db, newTestLogger())
		h.streams = &auth.StreamCredentials{}
		if door == "create" {
			f.create = true
			f.action = "auth.cli_token_created"
		}
		if !f.create {
			var release func()
			f.stream, release = h.streams.Track(t.Context(), h.streams.Epoch(), f.owner, "", "f1-token", time.Time{})
			t.Cleanup(release)
		}
		f.invoke = func() *httptest.ResponseRecorder {
			r := httptest.NewRequest("POST", "/api/v1/auth/cli-token", strings.NewReader(`{"name":"not-in-audit"}`))
			r = r.WithContext(withUser(r.Context(), &AuthUser{ID: f.owner, Email: "test@example.com"}))
			r.Header.Set("User-Agent", "not-in-token-audit")
			rr := httptest.NewRecorder()
			if f.create {
				h.Create(rr, r)
			} else {
				r.SetPathValue("tokenId", "f1-token")
				h.Revoke(rr, r)
			}
			return rr
		}
	}
	if !f.create {
		execOrFatal(t, f.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier) VALUES('f1-token',?,'not-in-audit',?,'STANDARD')`, f.owner, hashStandard("crewship_cli_synthetic-audit-test"))
	}
	return f
}

func (f tokenAuditFixture) assertMutation(t *testing.T, door string, changed bool) {
	t.Helper()
	var n int
	q := `SELECT COUNT(*) FROM cli_tokens WHERE user_id=?`
	if !f.create {
		q += ` AND revoked_at IS NOT NULL`
	}
	if door == "bootstrap" {
		q = `SELECT COUNT(*) FROM cli_tokens`
		if err := f.db.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
	} else if err := f.db.QueryRow(q, f.owner).Scan(&n); err != nil {
		t.Fatal(err)
	}
	want := 0
	if changed {
		want = 1
	}
	if n != want {
		t.Errorf("token mutation count=%d want%d", n, want)
	}
	if f.stream != nil && auth.StreamCredentialCurrent(f.stream) == changed {
		t.Error("stream notification did not match committed token mutation")
	}
	if door == "pair" {
		var state string
		if err := f.db.QueryRow(`SELECT status FROM cli_pairings WHERE code='AUDT-PAIR'`).Scan(&state); err != nil {
			t.Fatal(err)
		}
		want := "pending"
		if changed {
			want = "consumed"
		}
		if state != want {
			t.Errorf("pairing=%s want%s", state, want)
		}
	}
	if door == "bootstrap" && !changed {
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Error("bootstrap user survived failed token audit")
		}
	}
	if door == "suspend" {
		var at sql.NullString
		if err := f.db.QueryRow(`SELECT suspended_at FROM users WHERE id='carol'`).Scan(&at); err != nil {
			t.Fatal(err)
		}
		if at.Valid != changed {
			t.Error("suspension did not match token transaction")
		}
	}
	// Password/session effects precede the token transaction in the existing contract.
}

func TestCLITokenLifecycleWritesInstanceAudit(t *testing.T) {
	for _, door := range []string{"create", "pair", "bootstrap", "revoke", "password", "suspend"} {
		t.Run(door, func(t *testing.T) {
			f := newTokenAuditFixture(t, door)
			f.assertMutation(t, door, false)
			rr := f.invoke()
			want := http.StatusOK
			if door == "bootstrap" {
				want = http.StatusCreated
			}
			if rr.Code != want {
				t.Fatalf("SETUP: actual lifecycle status=%d want%d", rr.Code, want)
			}
			f.assertMutation(t, door, true)
			var tokenID, owner string
			query := `SELECT id,user_id FROM cli_tokens WHERE id='f1-token'`
			var args []any
			if f.create {
				query = `SELECT id,user_id FROM cli_tokens WHERE user_id=?`
				args = []any{f.owner}
			}
			if door == "bootstrap" {
				query = `SELECT id,user_id FROM cli_tokens WHERE name='bootstrap'`
				args = nil
			}
			if err := f.db.QueryRow(query, args...).Scan(&tokenID, &owner); err != nil {
				t.Fatal(err)
			}
			actor := f.actor
			if door == "bootstrap" {
				actor = owner
			}
			var n int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM instance_audit_logs WHERE action=? AND entity_type='cli_token' AND entity_id=? AND user_id=?`, f.action, tokenID, actor).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Fatalf("missing atomic token lifecycle audit: count=%d want1", n)
			}
			var metadata, ip, ua sql.NullString
			if err := f.db.QueryRow(`SELECT metadata,ip_address,user_agent FROM instance_audit_logs WHERE action=? AND entity_id=?`, f.action, tokenID).Scan(&metadata, &ip, &ua); err != nil {
				t.Fatal(err)
			}
			if metadata.Valid || ip.Valid || ua.Valid {
				t.Fatal("token event must contain only actor/action/opaque token identity")
			}
		})
	}
}

func TestCLITokenLifecycleAuditFailureRollsBack(t *testing.T) {
	for _, mode := range []string{"abort", "ignore", "commit"} {
		for _, door := range []string{"create", "pair", "bootstrap", "revoke", "password", "suspend"} {
			t.Run(mode+"/"+door, func(t *testing.T) {
				f := newTokenAuditFixture(t, door)
				f.assertMutation(t, door, false)
				if mode == "commit" {
					execOrFatal(t, f.db, `CREATE TABLE f1_deferred_audit_failure (parent TEXT REFERENCES users(id) DEFERRABLE INITIALLY DEFERRED)`)
					execOrFatal(t, f.db, `CREATE TRIGGER fail_token_audit AFTER INSERT ON instance_audit_logs BEGIN INSERT INTO f1_deferred_audit_failure VALUES('nonexistent-f1-parent'); END`)
				} else {
					execOrFatal(t, f.db, fmt.Sprintf(`CREATE TRIGGER fail_token_audit BEFORE INSERT ON instance_audit_logs BEGIN SELECT RAISE(%s); END`, map[string]string{"abort": "ABORT, 'audit unavailable'", "ignore": "IGNORE"}[mode]))
				}
				rr := f.invoke()
				if rr.Code != http.StatusInternalServerError {
					t.Errorf("audit %s returned%d want500", mode, rr.Code)
				}
				f.assertMutation(t, door, false)
				var n int
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM instance_audit_logs WHERE action=?`, f.action).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 0 {
					t.Error("failed token transaction retained an audit event")
				}
			})
		}
	}
}

// Final acknowledgement must follow the audit's triggers, not just the token SQL.
func TestCLITokenFinalMutationAcknowledgement(t *testing.T) {
	for _, door := range []string{"revoke", "password", "suspend", "create", "pair", "bootstrap"} {
		stages := []string{"after-audit"}
		if door == "revoke" {
			stages = append(stages, "after-token")
		}
		if door == "suspend" {
			stages = append(stages, "after-suspension-audit")
		}
		for _, stage := range stages {
			t.Run(stage+"/"+door, func(t *testing.T) {
				f := newTokenAuditFixture(t, door)
				f.assertMutation(t, door, false)
				if stage == "after-token" {
					execOrFatal(t, f.db, `CREATE TRIGGER undo_token_update AFTER UPDATE OF revoked_at ON cli_tokens WHEN NEW.id='f1-token' AND NEW.revoked_at IS NOT NULL BEGIN UPDATE cli_tokens SET revoked_at=NULL WHERE id=NEW.id; END`)
				} else {
					undo := `UPDATE cli_tokens SET revoked_at=NULL WHERE id=NEW.entity_id;`
					if f.create {
						undo = `DELETE FROM cli_tokens WHERE id=NEW.entity_id;`
					}
					when := `NEW.entity_type='cli_token'`
					if stage == "after-suspension-audit" {
						when = `NEW.action='instance.user_suspended'`
						undo = `UPDATE cli_tokens SET revoked_at=NULL WHERE user_id=NEW.entity_id;`
					}
					execOrFatal(t, f.db, `CREATE TRIGGER undo_token_from_audit AFTER INSERT ON instance_audit_logs WHEN `+when+` BEGIN `+undo+` END`)
				}
				rr := f.invoke()
				if rr.Code != 500 {
					t.Errorf("unacknowledged final mutation returned%d want500", rr.Code)
				}
				f.assertMutation(t, door, false)
				var n int
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM instance_audit_logs WHERE action=?`, f.action).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 0 {
					t.Error("audit survived unacknowledged token mutation")
				}
			})
		}
	}
}
