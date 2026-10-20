package main

// Acceptance for `crewship issue create --owner ... --delegate ...`: a person
// who is accountable and an agent that does the work, set in one create
// (the web composer's Owner and Delegate pickers send the same two fields).
// Real router, migrated DB, the built binary as the client.

import (
	"strings"
	"testing"
)

func TestAcceptance_IssueCreate_OwnerAndDelegateTogether(t *testing.T) {
	cfgPath, db := startIssueWorkAcceptanceServer(t)
	// Creating needs a crew with a lead agent; the shared seed has only a plain agent.
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status,
		cli_adapter, tool_profile, timeout_seconds, memory_enabled)
		VALUES ('iw-lead', ?, 'iw-crew', 'Lead', 'iw-lead', 'LEAD', 'IDLE', 'CLAUDE_CODE', 'CODING', 1800, 0)`,
		issueWorkAcceptanceWorkspaceID); err != nil {
		t.Fatalf("seed lead: %v", err)
	}

	out, err := runIssueWorkCLI(t, cfgPath, "issue", "create", "--crew", "iw-crew", "--title", "Owner and delegate",
		"--owner", "owner@iw-ex.com", "--delegate", "riley")
	if err != nil {
		t.Fatalf("issue create --owner --delegate: %v\n%s", err, out)
	}
	var owner, delegate, mode string
	if err := db.QueryRow(`SELECT COALESCE(m.owner_user_id,''), COALESCE(m.delegate_agent_id,''), COALESCE(iw.mode,'')
		FROM missions m LEFT JOIN issue_work iw ON iw.mission_id = m.id WHERE m.title='Owner and delegate'`).
		Scan(&owner, &delegate, &mode); err != nil {
		t.Fatalf("read created issue: %v", err)
	}
	if owner != "iw-owner" || delegate != "iw-agent" {
		t.Errorf("owner/delegate = (%q,%q), want (iw-owner, iw-agent)", owner, delegate)
	}
	if mode != "agent" {
		t.Errorf("work mode = %q, want agent: the delegate holds the work", mode)
	}

	// Owner alone is a person-held issue, exactly like --assignee-type user.
	out, err = runIssueWorkCLI(t, cfgPath, "issue", "create", "--crew", "iw-crew", "--title", "Owner only", "--owner", "owner@iw-ex.com")
	if err != nil {
		t.Fatalf("issue create --owner: %v\n%s", err, out)
	}
	if err := db.QueryRow(`SELECT COALESCE(owner_user_id,''), COALESCE(delegate_agent_id,'') FROM missions WHERE title='Owner only'`).
		Scan(&owner, &delegate); err != nil {
		t.Fatalf("read created issue: %v", err)
	}
	if owner != "iw-owner" || delegate != "" {
		t.Errorf("owner-only = (%q,%q), want (iw-owner, \"\")", owner, delegate)
	}
}

func TestAcceptance_IssueCreate_OwnerDelegateExcludeAssignee(t *testing.T) {
	cfgPath, _ := startIssueWorkAcceptanceServer(t)
	out, err := runIssueWorkCLI(t, cfgPath, "issue", "create", "--crew", "iw-crew", "--title", "Both ways",
		"--assignee", "riley", "--delegate", "riley")
	if err == nil || !strings.Contains(out, "--assignee") {
		t.Fatalf("want a refusal naming --assignee, got err=%v\n%s", err, out)
	}
}
