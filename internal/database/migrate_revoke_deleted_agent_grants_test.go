package database

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
)

// revokeDeletedAgentGrantsVersion is the repair migration under test (#5529).
const revokeDeletedAgentGrantsVersion = 20261119175019

// Agent delete tombstoned the agent and left its agent_credentials rows behind
// (#5529). The delete paths now revoke them; this migration repairs the rows
// written before that. The pre-fix state is reconstructed by seeding the
// orphans, clearing the migration's ledger row and migrating again.
func TestMigrateRevokesDeletedAgentCredentialGrants(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	db := openMigratedTestDB(t)

	for _, s := range []string{
		`INSERT INTO workspaces (id, name, slug) VALUES ('ws_g','G','ws-g')`,
		`INSERT INTO users (id, email) VALUES ('u_g','g@example.com')`,
		`INSERT INTO credentials (id, workspace_id, name, encrypted_value, created_by)
		   VALUES ('cred_g','ws_g','CLAUDE_CODE_OAUTH_TOKEN','opaque-material','u_g')`,
		`INSERT INTO agents (id, workspace_id, name, slug, deleted_at)
		   VALUES ('ag_deleted','ws_g','Deleted','deleted','2026-11-01T00:00:00Z'),
		          ('ag_live','ws_g','Live','live',NULL)`,
		`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, expires_at, lease_source)
		   VALUES ('ac_orphan','ag_deleted','cred_g','CLAUDE_CODE_OAUTH_TOKEN','2026-11-01T02:00:00Z','manual'),
		          ('ac_live','ag_live','cred_g','CLAUDE_CODE_OAUTH_TOKEN',NULL,NULL)`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}
	// A grant whose agent row is gone entirely: only reachable with foreign
	// keys off, as on databases that predate their enforcement.
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		`PRAGMA foreign_keys = OFF`,
		`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name) VALUES ('ac_ghost','ag_missing','cred_g','GH_TOKEN')`,
		`PRAGMA foreign_keys = ON`,
	} {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}
	conn.Close()

	reapply := func() {
		t.Helper()
		if _, err := db.Exec(`DELETE FROM _migrations WHERE version = ?`, revokeDeletedAgentGrantsVersion); err != nil {
			t.Fatalf("clear migration marker: %v", err)
		}
		if err := Migrate(ctx, db.DB, silent); err != nil {
			t.Fatalf("re-Migrate: %v", err)
		}
	}
	grantExists := func(id string) bool {
		t.Helper()
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM agent_credentials WHERE id = ?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	type audit struct {
		agentID *string
		meta    map[string]any
	}
	auditsFor := func(assignment string) []audit {
		t.Helper()
		rows, err := db.Query(`SELECT agent_id, metadata_json FROM credential_audit
			WHERE event_type = 'REVOKE' AND json_extract(metadata_json, '$.assignment_id') = ?`, assignment)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []audit
		for rows.Next() {
			var a audit
			var raw string
			if err := rows.Scan(&a.agentID, &raw); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(raw), &a.meta); err != nil {
				t.Fatal(err)
			}
			out = append(out, a)
		}
		return out
	}

	reapply()

	tests := []struct {
		grant     string
		wantKept  bool
		wantAgent string // "" = NULL agent_id on the audit row
	}{
		{"ac_orphan", false, "ag_deleted"},
		{"ac_ghost", false, ""},
		{"ac_live", true, ""},
	}
	for _, tc := range tests {
		if got := grantExists(tc.grant); got != tc.wantKept {
			t.Errorf("%s: exists = %v, want %v", tc.grant, got, tc.wantKept)
		}
		audits := auditsFor(tc.grant)
		if tc.wantKept {
			if len(audits) != 0 {
				t.Errorf("%s: a live agent's grant got %d REVOKE rows", tc.grant, len(audits))
			}
			continue
		}
		if len(audits) != 1 {
			t.Fatalf("%s: %d REVOKE rows, want 1", tc.grant, len(audits))
		}
		a := audits[0]
		if a.meta["reason"] != "agent_deleted_repair" || a.meta["grant_source"] != "explicit" {
			t.Errorf("%s: audit metadata = %v", tc.grant, a.meta)
		}
		gotAgent := ""
		if a.agentID != nil {
			gotAgent = *a.agentID
		}
		if gotAgent != tc.wantAgent {
			t.Errorf("%s: audit agent_id = %q, want %q", tc.grant, gotAgent, tc.wantAgent)
		}
	}
	if m := auditsFor("ac_orphan"); len(m) == 1 && m[0].meta["lease_source"] != "manual" {
		t.Errorf("lease provenance lost in the audit: %v", m[0].meta)
	}

	// The credential is never touched: row, material and tombstone.
	var material string
	var deletedAt *string
	if err := db.QueryRow(`SELECT encrypted_value, deleted_at FROM credentials WHERE id = 'cred_g'`).Scan(&material, &deletedAt); err != nil {
		t.Fatalf("credential vanished: %v", err)
	}
	if material != "opaque-material" || deletedAt != nil {
		t.Errorf("credential changed: material=%q deleted_at=%v", material, deletedAt)
	}

	// Re-applying (a restore whose ledger was rolled back) is a no-op.
	var before, after int
	if err := db.QueryRow(`SELECT COUNT(*) FROM credential_audit`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	reapply()
	if err := db.QueryRow(`SELECT COUNT(*) FROM credential_audit`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("re-apply wrote %d more audit rows, want 0", after-before)
	}
	if !grantExists("ac_live") {
		t.Error("re-apply removed the live agent's grant")
	}
}
