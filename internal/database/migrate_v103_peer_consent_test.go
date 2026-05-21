package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

// TestMigrateV103_PeerConsentSchema asserts the three GDPR primitives
// land with the right shape: opt-out flag, peer card index, and the
// data-subject-keyed audit log. The actor_kind / action CHECK enums
// are exercised by inserting at every valid value plus one rejected
// value, mirroring the existing v98+v99 test pattern.
func TestMigrateV103_PeerConsentSchema(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v103.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Migrate(context.Background(), db.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Seed FK targets.
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1','W','w')`); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ('u1','a@x'),('u2','b@x')`); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, slug, name, agent_role)
		VALUES ('a1','ws1','dev','Dev','AGENT')`); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	// Consent insert + composite PK.
	if _, err := db.Exec(`INSERT INTO user_peer_consent (user_id, workspace_id, opted_out, opted_out_at)
		VALUES ('u1','ws1',1,'2026-05-21T00:00:00Z')`); err != nil {
		t.Fatalf("consent insert: %v", err)
	}
	// Re-insert same composite PK should fail.
	if _, err := db.Exec(`INSERT INTO user_peer_consent (user_id, workspace_id, opted_out)
		VALUES ('u1','ws1',0)`); err == nil {
		t.Errorf("expected PK violation on duplicate (user_id, workspace_id)")
	}

	// peer_cards UNIQUE (agent_id, user_slug).
	if _, err := db.Exec(`INSERT INTO peer_cards (id, workspace_id, agent_id, user_id, user_slug, path, bytes)
		VALUES ('pc1','ws1','a1','u1','abc123','/peers/abc123.md',200)`); err != nil {
		t.Fatalf("peer_cards insert: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO peer_cards (id, workspace_id, agent_id, user_id, user_slug, path, bytes)
		VALUES ('pc2','ws1','a1','u2','abc123','/peers/abc123.md',300)`); err == nil {
		t.Errorf("expected UNIQUE violation on duplicate (agent_id, user_slug)")
	}

	// audit accepts every documented action.
	for _, action := range []string{"write", "read", "delete", "opt_out", "opt_in"} {
		if _, err := db.Exec(`INSERT INTO peer_card_audit
			(id, workspace_id, actor_user_id, actor_kind, action, target_user_id, agent_id)
			VALUES (?, 'ws1', 'u1', 'user', ?, 'u1', 'a1')`, "au-"+action, action); err != nil {
			t.Errorf("audit action=%q: %v", action, err)
		}
	}
	// Negative: bogus action rejected.
	if _, err := db.Exec(`INSERT INTO peer_card_audit
		(id, workspace_id, actor_kind, action, target_user_id)
		VALUES ('au-bogus','ws1','system','exfiltrate','u1')`); err == nil {
		t.Errorf("expected CHECK violation on action='exfiltrate'")
	}

	// Negative: bogus actor_kind rejected.
	if _, err := db.Exec(`INSERT INTO peer_card_audit
		(id, workspace_id, actor_kind, action, target_user_id)
		VALUES ('au-bad','ws1','robot','write','u1')`); err == nil {
		t.Errorf("expected CHECK violation on actor_kind='robot'")
	}
}
