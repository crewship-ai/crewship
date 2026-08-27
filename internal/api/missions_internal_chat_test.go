package api

// missions_internal_chat_test.go pins the other half of the sidecar
// mission-creation fix: InternalMissionHandler.Create must stamp the
// synthetic chats row assignments.chat_id needs, in the same transaction as
// the mission row, the same way mission_handler_mutate.go's Create and
// issue_handler_workflow.go's Start already do. The dispatcher-level
// regression (mission_chat_fk_test.go in internal/orchestrator) is what
// proves the class is closed even when a door forgets this; this test
// proves this specific door no longer forgets it.

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestInternalMissionCreate_StampsSyntheticChatRow(t *testing.T) {
	f := setupMissionCapFixture(t)

	w := f.createMission(t, "agent-planned mission", 1, nil)
	if w.Code != http.StatusCreated && w.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want 201 or 202; body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, w.Body.String())
	}
	if resp.ID == "" {
		t.Fatalf("response carried no mission id; body=%s", w.Body.String())
	}

	var n int
	if err := f.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM chats WHERE id = ?`, resp.ID).Scan(&n); err != nil {
		t.Fatalf("count chats: %v", err)
	}
	if n != 1 {
		t.Errorf("chats rows for mission %s = %d, want 1 — Create must stamp the synthetic chat "+
			"row before the first task dispatch needs it (assignments.chat_id NOT NULL REFERENCES chats(id))",
			resp.ID, n)
	}
}
