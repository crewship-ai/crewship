package api

// escalation_segregation_test.go — issue #1084: workspace-opt-in strict
// four-eyes rule for resolving CREDENTIAL escalations. When
// keeper_governance_settings.require_second_approver is set, the user
// recorded as the initiating agent's owner (agents.created_by_user_id, v100)
// may not resolve a CREDENTIAL escalation that agent raised — approver must
// differ from initiator. OWNER is NOT exempt: the check is independent of
// role, so even the workspace OWNER is refused when they are also the
// initiator. Default OFF preserves existing single-approver behavior.

import (
	"context"
	"database/sql"
	"net/http"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper/governance"
)

// seedOwnedAgent inserts an agent attributed to ownerUserID via
// created_by_user_id (v100 per-agent owner) — the "initiator" identity the
// segregation-of-duties check reads.
func seedOwnedAgent(t *testing.T, db *sql.DB, id, wsID, crewID, ownerUserID string) string {
	t.Helper()
	execOrFatal(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status,
		cli_adapter, tool_profile, timeout_seconds, memory_enabled, created_by_user_id)
		VALUES (?, ?, ?, 'Agent', ?, 'AGENT', 'IDLE', 'CLAUDE_CODE', 'CODING', 1800, 0, ?)`,
		id, wsID, crewID, id, ownerUserID)
	return id
}

func seedSoDEscalation(t *testing.T, db *sql.DB, escID, wsID, crewID, agentID string) {
	t.Helper()
	execOrFatal(t, db, `INSERT INTO escalations
		(id, workspace_id, crew_id, chat_id, from_agent_id, reason, type, status, created_at)
		VALUES (?, ?, ?, 'sod-chat', ?, 'store cred', 'CREDENTIAL', 'PENDING', datetime('now'))`,
		escID, wsID, crewID, agentID)
}

func TestResolveEscalation_SegregationOfDuties(t *testing.T) {
	cases := []struct {
		name       string
		toggleOn   bool
		useOwner   bool // true = approver is the initiator's owner; false = a different eligible approver
		action     string
		wantStatus int
	}{
		{
			name:       "toggle OFF: initiator can still approve (unchanged behavior)",
			toggleOn:   false,
			useOwner:   true,
			action:     "approve",
			wantStatus: http.StatusOK,
		},
		{
			name:       "toggle ON: initiator/owner rejected on approve (no OWNER bypass)",
			toggleOn:   true,
			useOwner:   true,
			action:     "approve",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "toggle ON: initiator/owner rejected on reject too (approver != initiator, any action)",
			toggleOn:   true,
			useOwner:   true,
			action:     "reject",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "toggle ON: a different eligible approver is allowed",
			toggleOn:   true,
			useOwner:   false,
			action:     "approve",
			wantStatus: http.StatusOK,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ensureEncryptionKey(t)
			db := setupTestDB(t)
			ownerID := seedTestUser(t, db) // "test-user-id" — also the workspace OWNER
			wsID := seedTestWorkspace(t, db, ownerID)

			const otherID = "other-approver"
			execOrFatal(t, db, `INSERT INTO users (id, email) VALUES (?, 'other@example.com')`, otherID)
			execOrFatal(t, db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m2', ?, ?, 'MANAGER')`, wsID, otherID)

			crewID := seedCrewRow(t, db, "sod-crew", wsID, "Crew", "sod-crew")
			agentID := seedOwnedAgent(t, db, "sod-agent", wsID, crewID, ownerID)
			seedSoDEscalation(t, db, "sod-esc", wsID, crewID, agentID)

			if tc.toggleOn {
				if err := governance.Upsert(context.Background(), db, wsID,
					governance.Settings{RequireSecondApprover: true}, ownerID); err != nil {
					t.Fatalf("enable require_second_approver: %v", err)
				}
			}

			h := NewQueryHandler(db, nil, nil, "", newTestLogger())
			approver := otherID
			if tc.useOwner {
				approver = ownerID
			}
			rr := covEscResolve(h, approver, wsID, "sod-esc", map[string]string{
				"resolution": "test resolution", "action": tc.action,
			})
			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, tc.wantStatus, rr.Body.String())
			}

			var status string
			if err := db.QueryRow(`SELECT status FROM escalations WHERE id = 'sod-esc'`).Scan(&status); err != nil {
				t.Fatalf("load escalation status: %v", err)
			}
			if tc.wantStatus == http.StatusOK {
				if status != "RESOLVED" {
					t.Errorf("escalation status = %q, want RESOLVED after a successful resolve", status)
				}
			} else if status != "PENDING" {
				t.Errorf("escalation status = %q, want still PENDING — rejection must happen BEFORE any mutation", status)
			}
		})
	}
}

// TestResolveEscalation_SegregationOfDuties_NonCredentialUnaffected — the
// four-eyes rule is scoped to CREDENTIAL escalations (issue #1084's stated
// concern is credential approvals). A TEXT escalation raised by the same
// agent must still be resolvable by its owner even with the toggle on.
func TestResolveEscalation_SegregationOfDuties_NonCredentialUnaffected(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	crewID := seedCrewRow(t, db, "sod-crew2", wsID, "Crew", "sod-crew2")
	agentID := seedOwnedAgent(t, db, "sod-agent2", wsID, crewID, ownerID)
	execOrFatal(t, db, `INSERT INTO escalations
		(id, workspace_id, crew_id, chat_id, from_agent_id, reason, type, status, created_at)
		VALUES ('sod-esc2', ?, ?, 'sod-chat2', ?, 'need help', 'TEXT', 'PENDING', datetime('now'))`,
		wsID, crewID, agentID)

	if err := governance.Upsert(context.Background(), db, wsID,
		governance.Settings{RequireSecondApprover: true}, ownerID); err != nil {
		t.Fatalf("enable require_second_approver: %v", err)
	}

	h := NewQueryHandler(db, nil, nil, "", newTestLogger())
	rr := covEscResolve(h, ownerID, wsID, "sod-esc2", map[string]string{
		"resolution": "handled", "action": "approve",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (TEXT escalations are out of scope); body=%s", rr.Code, rr.Body.String())
	}
}

// TestResolveEscalation_SegregationOfDuties_NoRecordedOwner — a legacy agent
// with no created_by_user_id (pre-v99 row, NULL owner) can't have the rule
// enforced against it; resolution proceeds even with the toggle on, since
// there is no initiator identity to compare against.
func TestResolveEscalation_SegregationOfDuties_NoRecordedOwner(t *testing.T) {
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	crewID := seedCrewRow(t, db, "sod-crew3", wsID, "Crew", "sod-crew3")
	// seedAgentRow (existing helper) does not set created_by_user_id — NULL owner.
	agentID := seedAgentRow(t, db, "sod-agent3", wsID, crewID, "Agent", "sod-agent3", "AGENT")
	execOrFatal(t, db, `INSERT INTO escalations
		(id, workspace_id, crew_id, chat_id, from_agent_id, reason, type, status, created_at)
		VALUES ('sod-esc3', ?, ?, 'sod-chat3', ?, 'store cred', 'CREDENTIAL', 'PENDING', datetime('now'))`,
		wsID, crewID, agentID)

	if err := governance.Upsert(context.Background(), db, wsID,
		governance.Settings{RequireSecondApprover: true}, ownerID); err != nil {
		t.Fatalf("enable require_second_approver: %v", err)
	}

	h := NewQueryHandler(db, nil, nil, "", newTestLogger())
	rr := covEscResolve(h, ownerID, wsID, "sod-esc3", map[string]string{
		"resolution": "handled", "action": "approve",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no recorded owner -> rule can't be enforced); body=%s", rr.Code, rr.Body.String())
	}
}
