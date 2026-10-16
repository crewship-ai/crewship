package backup

import (
	"encoding/json"
	"testing"
)

func TestRemapChatProposalsRewritesOnlyTypedReferences(t *testing.T) {
	source := map[string]any{"id": "new-proposal", "operation_id": "operation", "run_id": "run", "trigger_message_id": "trigger", "items": `[{"id":"crew","title":"crew","crew_id":"crew","project_id":"project","assignee_id":"agent"},{"id":"user-item","assignee_id":"user"}]`, "outcome": `{"confirmed_by":"user","issues":[{"id":"issue","identifier":"ENG-1"}]}`}
	receipt := map[string]any{"workspace_id": "workspace", "proposal_id": "new-proposal", "actor_type": "agent", "actor_id": "agent", "request_hash": "immutable-hash", "run_id": "revision-run", "result": `{"operation_id":"operation","entity":{"id":"proposal","workspace_id":"workspace"},"provenance":{"actor":{"type":"agent","id":"agent"}},"anchor":{"run_id":"run","trigger_message_id":"trigger"},"proposal":{"items":[{"id":"crew","title":"crew","crew_id":"crew","project_id":"project","assignee_id":"agent"}],"outcome":{"issues":[{"id":"issue","identifier":"ENG-1"}]}}}`}
	issueOp := map[string]any{"source_proposal_id": "proposal"}
	dump := &DBDump{Tables: map[string][]map[string]any{"chat_issue_proposals": {source}, "chat_proposal_operations": {receipt}, "agent_operations": {issueOp}}}
	ids := map[string]map[string]string{"workspaces": {"workspace": "new-workspace"}, "chat_issue_proposals": {"proposal": "new-proposal"}, "chat_proposal_operations": {"operation": "new-operation"}, "agents": {"agent": "new-agent"}, "crews": {"crew": "new-crew"}, "projects": {"project": "new-project"}, "missions": {"issue": "new-issue"}}
	if err := remapChatProposals(dump, ids); err != nil {
		t.Fatal(err)
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(source["items"].(string)), &items); err != nil {
		t.Fatal(err)
	}
	if items[0]["id"] != "crew" || items[0]["title"] != "crew" || items[0]["crew_id"] != "new-crew" || items[0]["project_id"] != "new-project" || items[0]["assignee_id"] != "new-agent" || items[1]["assignee_id"] != "user" {
		t.Fatalf("typed item remap: %+v", items)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(receipt["result"].(string)), &result); err != nil {
		t.Fatal(err)
	}
	entity := result["entity"].(map[string]any)
	actor := result["provenance"].(map[string]any)["actor"].(map[string]any)
	if entity["id"] != "new-proposal" || entity["workspace_id"] != "new-workspace" || actor["id"] != "new-agent" || result["operation_id"] != "new-operation" {
		t.Fatalf("receipt envelope remap: %+v", result)
	}
	outcome := result["proposal"].(map[string]any)["outcome"].(map[string]any)
	if outcome["issues"].([]any)[0].(map[string]any)["id"] != "new-issue" || issueOp["source_proposal_id"] != "new-proposal" {
		t.Fatal("issue outcome/source reference not remapped")
	}
	if source["operation_id"] != "new-operation" || source["run_id"] != "run" || source["trigger_message_id"] != "trigger" || receipt["request_hash"] != "immutable-hash" || receipt["run_id"] != "revision-run" {
		t.Fatal("historical identity changed")
	}
}
