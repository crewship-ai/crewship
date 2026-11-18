package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
)

func TestAgentInbox_CountsProductionAssignmentStates(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, user)
	seedCrewRow(t, db, "count-crew", workspace, "Count", "count")
	seedAgentRow(t, db, "count-agent", workspace, "count-crew", "Worker", "worker", "AGENT")
	seedAgentRow(t, db, "other-agent", workspace, "count-crew", "Other", "other", "AGENT")
	if _, err := db.Exec(`INSERT INTO chats(id,workspace_id,agent_id) VALUES('count-chat',?,'count-agent')`, workspace); err != nil {
		t.Fatal(err)
	}
	for i, status := range []string{"PENDING", "RUNNING", "QUEUED", "queued", "running", "COMPLETED", "FAILED", "CANCELLED", "TIMEOUT"} {
		if _, err := db.Exec(`INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,assigned_to_id,task,status)
 VALUES(?,?,'count-chat','other-agent','count-agent','Task',?)`, fmt.Sprint("count-", i), workspace, status); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,assigned_to_id,task)
 VALUES('other-pending',?,'count-chat','count-agent','other-agent','Other recipient')`, workspace); err != nil {
		t.Fatal(err)
	}
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/agents/count-agent/inbox", nil), user, workspace, "OWNER")
	req.SetPathValue("agentId", "count-agent")
	rr := httptest.NewRecorder()
	NewAgentInboxHandler(db, newTestLogger()).Handle(rr, req)
	var got agentInboxResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.AssignmentsOpen != 5 {
		t.Fatalf("open assignments = %d, want pending/running/queued plus two legacy states; body=%s", got.AssignmentsOpen, rr.Body.String())
	}
}
