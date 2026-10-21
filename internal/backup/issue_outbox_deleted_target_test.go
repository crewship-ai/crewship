package backup_test

import (
	"encoding/json"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestIssueAuditOutboxRestoresWithHistoricalDeletedTarget(t *testing.T) {
	source := openMigratedDB(t)
	workspace := seedChatEntityHistory(t, source)
	// seedChatEntityHistory hard-deletes history-gone. Pending audit intent
	// remains valid historical data, rather than an orphan to skip on restore.
	payload, _ := json.Marshal(map[string]any{"journal": map[string]any{"ID": "deleted-issue-audit", "WorkspaceID": workspace, "MissionID": "history-gone", "TS": "2026-10-14T08:00:00Z", "Refs": map[string]any{"mission_id": "history-gone"}}})
	if _, err := source.Exec(`INSERT INTO issue_mutation_outbox(id,workspace_id,mission_id,kind,payload,next_attempt_at,created_at) VALUES('deleted-issue-outbox',?,'history-gone','journal',?,'2026-10-14T08:00:00.000Z','2026-10-14T08:00:00.000Z')`, workspace, string(payload)); err != nil {
		t.Fatal(err)
	}
	dump, err := backup.DumpWorkspace(t.Context(), source, workspace)
	if err != nil {
		t.Fatal(err)
	}
	target := openMigratedDB(t)
	if err = backup.RestoreDump(t.Context(), target, dump); err != nil {
		t.Fatal(err)
	}
	var historical, state string
	if err = target.QueryRow(`SELECT mission_id,state FROM issue_mutation_outbox WHERE id='deleted-issue-outbox'`).Scan(&historical, &state); err != nil || historical != "history-gone" || state != "pending" {
		t.Fatalf("historical audit intent lost: %s %s %v", historical, state, err)
	}
	if err = backup.RemapIDs(t.Context(), source, dump); err != nil {
		t.Fatal(err)
	}
	dump.Tables["workspaces"][0]["slug"] = "deleted-issue-outbox-fork"
	if err = backup.RestoreDump(t.Context(), source, dump); err != nil {
		t.Fatal(err)
	}
	forkWorkspace := dump.Tables["workspaces"][0]["id"].(string)
	var body string
	if err = source.QueryRow(`SELECT mission_id,state,payload FROM issue_mutation_outbox WHERE workspace_id=?`, forkWorkspace).Scan(&historical, &state, &body); err != nil || historical != "history-gone" || state != "done" || body != "{}" {
		t.Fatalf("historical fork resumed effect or lost target: %s %s %s %v", historical, state, body, err)
	}
}
