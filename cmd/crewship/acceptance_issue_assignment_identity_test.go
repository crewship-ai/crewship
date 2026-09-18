package main

import (
	"strings"
	"testing"
)

// Drive the actual CLI and router: a type-only PATCH used to change the
// legacy identity without validating its ID or updating either typed slot.
func TestAcceptance_IssueUpdate_TypeOnlyPreservesAssignment(t *testing.T) {
	cfg, db := startIssueWorkAcceptanceServer(t)
	for _, existing := range []struct{ kind, id string }{{"agent", "iw-agent"}, {"user", "iw-owner"}} {
		for _, requested := range []string{"user", "agent", "", " "} {
			t.Run(existing.kind+"/"+requested, func(t *testing.T) {
				if _, err := db.ExecContext(t.Context(), `UPDATE missions SET owner_user_id='iw-owner', delegate_agent_id='iw-agent', assignee_type=?, assignee_id=? WHERE id='iw-mission'`, existing.kind, existing.id); err != nil {
					t.Fatal(err)
				}
				out, commandErr := runIssueWorkCLI(t, cfg, "issue", "update", "IW-1", "--assignee-type", requested)
				var owner, delegate, kind, id string
				if err := db.QueryRowContext(t.Context(), `SELECT COALESCE(owner_user_id,''), COALESCE(delegate_agent_id,''), COALESCE(assignee_type,''), COALESCE(assignee_id,'') FROM missions WHERE id='iw-mission'`).Scan(&owner, &delegate, &kind, &id); err != nil {
					t.Fatal(err)
				}
				if commandErr == nil || !strings.Contains(out, "--assignee-type requires --assignee") {
					t.Errorf("want actionable refusal, error=%v output=%s", commandErr, out)
				}
				if owner != "iw-owner" || delegate != "iw-agent" || kind != existing.kind || id != existing.id {
					t.Errorf("refused update changed identity: owner=%q delegate=%q legacy=(%q,%q)", owner, delegate, kind, id)
				}
			})
		}
	}
}
