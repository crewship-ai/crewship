package mooring

import "testing"

// The callback remains synthetic like deliveryFixture. This proves unavoidable
// same-writer invalidation from a real canonical SQL mutation, not live RBAC.
func TestAuthorityEpochCanonicalGrantDeletionClosesGate(t *testing.T) {
	s, db, _, owner := deliveryFixture(t)
	if _, err := db.Exec(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('p','user','u','write','u')`); err != nil {
		t.Fatal(err)
	}
	g := deliveryOpen(t, s, owner, "human-a", "before-canonical-revoke")
	if _, err := db.Exec(`DELETE FROM page_grants WHERE page_id='p' AND subject_type='user' AND subject_id='u'`); err != nil {
		t.Fatal(err)
	}
	var closed bool
	if err := db.QueryRow(`SELECT closed FROM mooring_delivery_gates WHERE id=?`, g.GateID).Scan(&closed); err != nil || !closed {
		t.Fatal("canonical grant deletion left issued gate open", closed, err)
	}
}

func TestAuthorityEpochCanonicalMutationsPreserveIssuedEvidence(t *testing.T) {
	s, db, _, owner := deliveryFixture(t)
	s.maxGates = 100
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(q, err)
		}
	}
	epoch := func(table, id string) int64 {
		t.Helper()
		var v int64
		if err := db.QueryRow("SELECT authority_revision FROM "+table+" WHERE id=?", id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('m','w','u','MEMBER')`)
	exec(`INSERT INTO workspaces(id,name,slug) VALUES('other','Other','other')`)
	exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('m-other','other','u','MEMBER')`)
	exec(`INSERT INTO user_sessions(id,user_id,expires_at,created_at) VALUES('session','u','2099-01-01T00:00:00Z','2026-01-02T00:00:00Z')`)
	exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('token','u','Test','test-only-hash','2026-01-02T00:00:00Z')`)
	exec(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('p','user','u','write','u')`)
	for _, tc := range []struct {
		name, sql string
		user      bool
	}{
		{"grant removal", `DELETE FROM page_grants WHERE page_id='p'`, false},
		{"grant readdition", `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('p','user','u','write','u')`, false},
		{"session revoke", `UPDATE user_sessions SET revoked_at='2026-10-23T00:00:00Z' WHERE id='session'`, true},
		{"session restore", `UPDATE user_sessions SET revoked_at=NULL WHERE id='session'`, true},
		{"session creation identity", `UPDATE user_sessions SET created_at='2026-01-01T00:00:00Z' WHERE id='session'`, true},
		{"session creation restoration", `UPDATE user_sessions SET created_at='2026-01-02T00:00:00Z' WHERE id='session'`, true},
		{"session horizon", `UPDATE user_sessions SET expires_at='2098-01-01T00:00:00Z' WHERE id='session'`, true},
		{"token revoke", `UPDATE cli_tokens SET revoked_at='2026-10-23T00:00:00Z' WHERE id='token'`, true},
		{"token restore", `UPDATE cli_tokens SET revoked_at=NULL WHERE id='token'`, true},
		{"token creation identity", `UPDATE cli_tokens SET created_at='2026-01-01T00:00:00Z' WHERE id='token'`, true},
		{"token creation restoration", `UPDATE cli_tokens SET created_at='2026-01-02T00:00:00Z' WHERE id='token'`, true},
		{"token identity", `UPDATE cli_tokens SET token_hash='replacement-test-only-hash' WHERE id='token'`, true},
		{"user revoke with old counter", `UPDATE users SET access_revoked_at='2026-10-23T00:00:00Z',authority_revision=authority_revision WHERE id='u'`, true},
		{"user restore", `UPDATE users SET access_revoked_at=NULL WHERE id='u'`, true},
		{"global restriction elsewhere", `UPDATE workspace_members SET access_mode='restricted' WHERE id='m-other'`, true},
		{"global restriction removal", `UPDATE workspace_members SET access_mode='trusted' WHERE id='m-other'`, true},
		{"page audience", `UPDATE pages SET private_audience=1 WHERE id='p'`, false},
		{"page audience restore", `UPDATE pages SET private_audience=0 WHERE id='p'`, false},
		{"workspace soft delete with old counter", `UPDATE workspaces SET deleted_at='2026-10-23T00:00:00Z',authority_revision=authority_revision WHERE id='w'`, false},
		{"workspace restore", `UPDATE workspaces SET deleted_at=NULL WHERE id='w'`, false},
	} {
		func() {
			t.Log(tc.name)
			g := deliveryOpen(t, s, owner, "human-a", tc.name)
			oldW, oldU := epoch("workspaces", "w"), epoch("users", "u")
			var expires int64
			if err := db.QueryRow(`SELECT expires_ns FROM mooring_delivery_gates WHERE id=?`, g.GateID).Scan(&expires); err != nil {
				t.Fatal(err)
			}
			exec(tc.sql)
			if epoch("workspaces", "w") <= oldW {
				t.Fatal("canonical mutation did not advance workspace epoch")
			}
			if tc.user && epoch("users", "u") <= oldU {
				t.Fatal("original-user fence survived authority ABA")
			}
			var closed, blocked bool
			var after int64
			if err := db.QueryRow(`SELECT closed,blocked,expires_ns FROM mooring_delivery_gates WHERE id=?`, g.GateID).Scan(&closed, &blocked, &after); err != nil {
				t.Fatal(err)
			}
			if !closed || blocked || after != expires {
				t.Fatal("mutation altered issued evidence or fabricated ACK", closed, blocked, expires, after)
			}
		}()
	}
	// Ordinary content/audit activity must not continually revoke collaborators.
	g := deliveryOpen(t, s, owner, "human-a", "audit-only")
	oldW, oldU := epoch("workspaces", "w"), epoch("users", "u")
	for _, q := range []string{
		`UPDATE pages SET name='Renamed',spec_json='{"test":true}' WHERE id='p'`,
		`UPDATE user_sessions SET last_used_at='2026-10-23T01:00:00Z' WHERE id='session'`,
		`UPDATE cli_tokens SET last_used_at='2026-10-23T01:00:00Z' WHERE id='token'`,
		`UPDATE page_grants SET level=level WHERE page_id='p'`,
	} {
		exec(q)
	}
	var closed bool
	if err := db.QueryRow(`SELECT closed FROM mooring_delivery_gates WHERE id=?`, g.GateID).Scan(&closed); err != nil {
		t.Fatal(err)
	}
	if closed || epoch("workspaces", "w") != oldW || epoch("users", "u") != oldU {
		t.Fatal("audit/no-op mutation invalidated admission")
	}
	// The same writer observes closure, but rollback must restore both the ACL
	// and its generation. It cannot manufacture durable revocation completion.
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`DELETE FROM page_grants WHERE page_id='p'`); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(`SELECT closed FROM mooring_delivery_gates WHERE id=?`, g.GateID).Scan(&closed); err != nil || !closed {
		t.Fatal(closed, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT closed FROM mooring_delivery_gates WHERE id=?`, g.GateID).Scan(&closed); err != nil || closed {
		t.Fatal(closed, err)
	}
	if epoch("workspaces", "w") != oldW {
		t.Fatal("rolled-back revocation advanced epoch")
	}
	// Exhaustion fails the canonical mutation atomically, rather than wrapping
	// an authority generation or partially closing an issued gate.
	exec(`UPDATE workspaces SET authority_revision=9223372036854775807 WHERE id='w'`)
	g = deliveryOpen(t, s, owner, "human-a", "at-counter-limit")
	if _, err = db.Exec(`DELETE FROM page_grants WHERE page_id='p'`); err == nil {
		t.Fatal("counter exhaustion allowed canonical mutation")
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM page_grants WHERE page_id='p'`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err = db.QueryRow(`SELECT closed FROM mooring_delivery_gates WHERE id=?`, g.GateID).Scan(&closed); err != nil || closed {
		t.Fatal("failed mutation partially closed gate", closed, err)
	}
	if _, err = db.Exec(`UPDATE workspaces SET authority_revision=1 WHERE id='w'`); err == nil {
		t.Fatal("generation rollback allowed")
	}
}

func TestAuthorityEpochUserExhaustionRollsBackCredentialMutation(t *testing.T) {
	s, db, _, owner := deliveryFixture(t)
	for _, q := range []string{
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('m','w','u','MEMBER')`,
		`INSERT INTO user_sessions(id,user_id,expires_at,created_at) VALUES('session','u','2099-01-01T00:00:00Z','2026-01-02T00:00:00Z')`,
		`UPDATE users SET authority_revision=9223372036854775807 WHERE id='u'`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	g := deliveryOpen(t, s, owner, "human-a", "user-limit")
	if _, err := db.Exec(`UPDATE user_sessions SET revoked_at='2026-10-23T00:00:00Z' WHERE id='session'`); err == nil {
		t.Fatal("exhausted user fence allowed credential mutation")
	}
	var clean, closed bool
	if err := db.QueryRow(`SELECT revoked_at IS NULL FROM user_sessions WHERE id='session'`).Scan(&clean); err != nil || !clean {
		t.Fatal("failed mutation partially revoked credential", clean, err)
	}
	if err := db.QueryRow(`SELECT closed FROM mooring_delivery_gates WHERE id=?`, g.GateID).Scan(&closed); err != nil || closed {
		t.Fatal("failed mutation partially closed gate", closed, err)
	}
	if _, err := db.Exec(`UPDATE users SET authority_revision=1 WHERE id='u'`); err == nil {
		t.Fatal("user authority generation rollback allowed")
	}
}
