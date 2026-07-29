package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// An audit row said WHO did WHAT KIND of thing and gave the first eight
// characters of an id. It never said WHICH thing: "create AGENT cms35ksv"
// does not distinguish creating Riley from creating Sam, so the log reads as
// undifferentiated churn and the only way to identify a row is to go look the
// id up somewhere else.
//
// entity_id is polymorphic, so the name is resolved per entity_type. A row
// whose target is gone for good keeps a null name and the UI falls back to the
// id — better a missing name than a wrong one.

func seedAuditRowFor(t *testing.T, db *sql.DB, wsID, userID, action, entityType, entityID string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO audit_logs (id, workspace_id, user_id, action, entity_type, entity_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, datetime('now'))`,
		generateCUID(), wsID, userID, action, entityType, entityID); err != nil {
		t.Fatalf("seed audit row: %v", err)
	}
}

func auditList(t *testing.T, h *AuditHandler, wsID string) []auditResponse {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/audit", nil)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var out auditListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out.Data
}

func nameFor(rows []auditResponse, entityID string) string {
	for _, r := range rows {
		if r.EntityID != nil && *r.EntityID == entityID {
			if r.EntityName == nil {
				return ""
			}
			return *r.EntityName
		}
	}
	return "<no row>"
}

func TestAudit_List_NamesTheEntityEachRowTouched(t *testing.T) {
	h, db := newAuditHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-a", wsID, "Engineering", "engineering")
	agentID := seedAgentRow(t, db, "agent-a", wsID, crewID, "Riley", "riley", "AGENT")

	if _, err := db.Exec(
		`INSERT INTO credentials (id, workspace_id, name, encrypted_value, created_by) VALUES ('cred-a', ?, 'GH_TOKEN', 'x', ?)`,
		wsID, userID); err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	seedAuditRowFor(t, db, wsID, userID, "create", "AGENT", agentID)
	seedAuditRowFor(t, db, wsID, userID, "crew.create", "CREW", crewID)
	seedAuditRowFor(t, db, wsID, userID, "credential.create", "CREDENTIAL", "cred-a")

	rows := auditList(t, h, wsID)
	if got := nameFor(rows, agentID); got != "Riley" {
		t.Errorf("agent row name = %q, want Riley", got)
	}
	if got := nameFor(rows, crewID); got != "Engineering" {
		t.Errorf("crew row name = %q, want Engineering", got)
	}
	if got := nameFor(rows, "cred-a"); got != "GH_TOKEN" {
		t.Errorf("credential row name = %q, want GH_TOKEN", got)
	}
}

// The names have to survive a delete, or the log goes blank exactly where it
// matters most — "who deleted Riley" is the question you ask after Riley is
// gone. Crews and agents are soft-deleted, so the row is still there to join.
func TestAudit_List_NamesADeletedEntity(t *testing.T) {
	h, db := newAuditHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-b", wsID, "Ops", "ops")
	agentID := seedAgentRow(t, db, "agent-b", wsID, crewID, "Morgan", "morgan", "LEAD")
	seedAuditRowFor(t, db, wsID, userID, "delete", "AGENT", agentID)

	if _, err := db.Exec(`UPDATE agents SET deleted_at = datetime('now') WHERE id = ?`, agentID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	if got := nameFor(auditList(t, h, wsID), agentID); got != "Morgan" {
		t.Errorf("name after delete = %q, want Morgan", got)
	}
}

// Entity types are written inconsistently by the call sites that exist —
// "AGENT" from the agent handlers, "backup"/"connector" lower-case elsewhere.
// The lookup must not depend on which one a caller happened to use.
func TestAudit_List_EntityTypeCasingDoesNotDecideTheLookup(t *testing.T) {
	h, db := newAuditHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-c", wsID, "Quality", "quality")
	agentID := seedAgentRow(t, db, "agent-c", wsID, crewID, "Casey", "casey", "AGENT")
	seedAuditRowFor(t, db, wsID, userID, "update", "agent", agentID)

	if got := nameFor(auditList(t, h, wsID), agentID); got != "Casey" {
		t.Errorf("name for lower-case entity_type = %q, want Casey", got)
	}
}

func TestAudit_List_UnknownEntityLeavesTheNameEmpty(t *testing.T) {
	h, db := newAuditHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedAuditRowFor(t, db, wsID, userID, "backup.create", "backup", "/var/backups/x.tar")

	rows := auditList(t, h, wsID)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].EntityName != nil && *rows[0].EntityName != "" {
		t.Errorf("entity_name = %q, want empty — a backup path is not a named row", *rows[0].EntityName)
	}
}
