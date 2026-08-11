package api

import (
	"context"
	"database/sql"
	"testing"
)

// credential_audit carries its own workspace_id so the admin audit view can be
// answered by an index instead of a join plus a sort (migration
// 20260810153104). The column is only worth anything if it is actually
// populated, and the writer derives it from the credential rather than taking
// it as a parameter — so the failure mode is a silent NULL: the row is written,
// nothing errors, and it simply never appears in a workspace-scoped read.
//
// These tests are that guard. They also pin the derivation itself: the value
// must come from the credential, not from whatever the caller believed.

// TestRecordCredentialEvent_PopulatesWorkspace covers every event type the
// writer accepts, because the workspace derivation lives in the shared INSERT
// and a future special-case for one event type would bypass it.
func TestRecordCredentialEvent_PopulatesWorkspace(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	tests := []struct {
		name  string
		event CredentialAuditEvent
		agent string
		ip    string
	}{
		{"use", AuditEventUse, "", "1.2.3.4"},
		{"rotate", AuditEventRotate, "", ""},
		{"created", AuditEventCreated, "", ""},
	}

	ctx := context.Background()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			credID := "cred-ws-" + tc.name
			seedCredentialEnc(t, db, wsID, userID, credID, "KEY_"+tc.name, "v")

			if err := RecordCredentialEvent(ctx, db, auditLogger(), credID, tc.event, tc.agent, tc.ip, nil); err != nil {
				t.Fatalf("record %s: %v", tc.event, err)
			}

			var got sql.NullString
			if err := db.QueryRow(
				`SELECT workspace_id FROM credential_audit WHERE credential_id = ? ORDER BY occurred_at DESC LIMIT 1`,
				credID).Scan(&got); err != nil {
				t.Fatalf("read audit row: %v", err)
			}
			if !got.Valid {
				t.Fatalf("%s wrote a NULL workspace_id — the row exists but is invisible to every workspace-scoped audit read", tc.event)
			}
			if got.String != wsID {
				t.Errorf("workspace_id = %q, want %q", got.String, wsID)
			}
		})
	}
}

// TestRecordCredentialEvent_WorkspaceFollowsTheCredential pins WHERE the value
// comes from. Two workspaces, one credential in each: the audit row must take
// the workspace of the credential it names. Deriving it in the INSERT is what
// makes disagreement unrepresentable — this test fails if someone later
// "simplifies" it into a caller-supplied parameter.
func TestRecordCredentialEvent_WorkspaceFollowsTheCredential(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)

	// seedTestWorkspace hard-codes a single id, so the second tenant is
	// inserted directly rather than by calling it twice.
	wsA := seedTestWorkspace(t, db, userID)
	const wsB = "test-workspace-b"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Test B', 'test-b')`, wsB); err != nil {
		t.Fatalf("insert second workspace: %v", err)
	}
	if wsA == wsB {
		t.Fatal("expected two distinct workspaces")
	}
	seedCredentialEnc(t, db, wsA, userID, "cred-in-a", "KEY_A", "v")
	seedCredentialEnc(t, db, wsB, userID, "cred-in-b", "KEY_B", "v")

	ctx := context.Background()
	for _, credID := range []string{"cred-in-a", "cred-in-b"} {
		if err := RecordCredentialEvent(ctx, db, auditLogger(), credID, AuditEventUse, "", "", nil); err != nil {
			t.Fatalf("record for %s: %v", credID, err)
		}
	}

	tests := []struct {
		cred string
		want string
	}{
		{"cred-in-a", wsA},
		{"cred-in-b", wsB},
	}
	for _, tc := range tests {
		var got sql.NullString
		if err := db.QueryRow(
			`SELECT workspace_id FROM credential_audit WHERE credential_id = ?`, tc.cred).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", tc.cred, err)
		}
		if got.String != tc.want {
			t.Errorf("%s: workspace_id = %q, want %q — an audit row attributed to the wrong tenant is worse than none", tc.cred, got.String, tc.want)
		}
	}

	// And the scoped read the audit page issues sees exactly one row per
	// workspace — the isolation the column exists to make cheap.
	for _, tc := range tests {
		var n int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM credential_audit WHERE workspace_id = ?`, tc.want).Scan(&n); err != nil {
			t.Fatalf("count for %s: %v", tc.want, err)
		}
		if n != 1 {
			t.Errorf("workspace %s: %d audit rows, want 1", tc.want, n)
		}
	}
}
