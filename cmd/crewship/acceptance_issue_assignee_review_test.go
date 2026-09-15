package main

// Acceptance for two gaps the 2026-09-15 CLI audit found on `issue`
// (#2587): the CLI refused `--assignee-type user` although the API has
// written owner_user_id for it since #2297, and `issue review` sent the
// revision it had just read without letting the caller pin the one they
// actually reviewed. Same harness as the work-contract tests: real router,
// migrated DB, the built binary as the client.

import (
	"strconv"
	"strings"
	"testing"
)

func TestAcceptance_IssueUpdate_AssigneeTypeUserSetsTheOwner(t *testing.T) {
	cfgPath, db := startIssueWorkAcceptanceServer(t)

	// Start from a delegated issue so the test can see that assigning a
	// human owner leaves the delegate alone (invariant I5).
	if _, err := db.Exec(`UPDATE missions SET assignee_type='agent', assignee_id='iw-agent', delegate_agent_id='iw-agent' WHERE id='iw-mission'`); err != nil {
		t.Fatalf("seed delegate: %v", err)
	}

	out, err := runIssueWorkCLI(t, cfgPath, "issue", "update", "IW-1", "--assignee", "owner@iw-ex.com", "--assignee-type", "user")
	if err != nil {
		t.Fatalf("issue update --assignee-type user: %v\n%s", err, out)
	}
	var owner, delegate, legacyType, legacyID string
	if err := db.QueryRow(`SELECT COALESCE(owner_user_id,''), COALESCE(delegate_agent_id,''), COALESCE(assignee_type,''), COALESCE(assignee_id,'') FROM missions WHERE id='iw-mission'`).
		Scan(&owner, &delegate, &legacyType, &legacyID); err != nil {
		t.Fatalf("read mission: %v", err)
	}
	if owner != "iw-owner" {
		t.Errorf("owner_user_id = %q, want iw-owner (the email must resolve to the member's user id)", owner)
	}
	if delegate != "iw-agent" {
		t.Errorf("delegate_agent_id = %q after assigning a human owner, want iw-agent untouched", delegate)
	}
	if legacyType != "user" || legacyID != "iw-owner" {
		t.Errorf("legacy pair = (%q,%q), want (user, iw-owner)", legacyType, legacyID)
	}

	// `issue get` shows the owner the same way the web card does.
	out, err = runIssueWorkCLI(t, cfgPath, "issue", "get", "IW-1", "-f", "json")
	if err != nil {
		t.Fatalf("issue get: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"owner"`) || !strings.Contains(out, `"iw-owner"`) {
		t.Errorf("issue get -f json does not show the new owner:\n%s", out)
	}

	// A user id (cuid-shaped) is forwarded as-is — the fast path once the
	// caller already has the id from `workspace member list -f json`.
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('cuseriw000000000000000002', 'second@iw-ex.com', 'Second')`); err != nil {
		t.Fatalf("seed second user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('iwm-second', ?, 'cuseriw000000000000000002', 'MEMBER')`, issueWorkAcceptanceWorkspaceID); err != nil {
		t.Fatalf("seed second member: %v", err)
	}
	out, err = runIssueWorkCLI(t, cfgPath, "issue", "update", "IW-1", "--assignee", "cuseriw000000000000000002", "--assignee-type", "user")
	if err != nil {
		t.Fatalf("issue update --assignee <cuid> --assignee-type user: %v\n%s", err, out)
	}
	if err := db.QueryRow(`SELECT COALESCE(owner_user_id,''), COALESCE(delegate_agent_id,'') FROM missions WHERE id='iw-mission'`).Scan(&owner, &delegate); err != nil {
		t.Fatalf("read mission: %v", err)
	}
	if owner != "cuseriw000000000000000002" || delegate != "iw-agent" {
		t.Errorf("after --assignee <cuid>: owner=%q delegate=%q, want the cuid and an untouched delegate", owner, delegate)
	}

	// --assignee "" is the explicit unassign: both slots and the legacy pair
	// go empty. (Sent as assignee_id "", which is the only shape the
	// server's clear branch recognises — a JSON null did nothing.)
	out, err = runIssueWorkCLI(t, cfgPath, "issue", "update", "IW-1", "--assignee", "")
	if err != nil {
		t.Fatalf("issue update --assignee \"\": %v\n%s", err, out)
	}
	if err := db.QueryRow(`SELECT COALESCE(owner_user_id,''), COALESCE(delegate_agent_id,''), COALESCE(assignee_type,''), COALESCE(assignee_id,'') FROM missions WHERE id='iw-mission'`).
		Scan(&owner, &delegate, &legacyType, &legacyID); err != nil {
		t.Fatalf("read mission: %v", err)
	}
	if owner != "" || delegate != "" || legacyType != "" || legacyID != "" {
		t.Errorf("after --assignee \"\": owner=%q delegate=%q legacy=(%q,%q), want all empty", owner, delegate, legacyType, legacyID)
	}

	// An email nobody in the workspace has is refused before any request.
	out, err = runIssueWorkCLI(t, cfgPath, "issue", "update", "IW-1", "--assignee", "nobody@iw-ex.com", "--assignee-type", "user")
	if err == nil {
		t.Fatalf("an unknown member must be refused, got success:\n%s", out)
	}
	if !strings.Contains(out, "nobody@iw-ex.com") {
		t.Errorf("the refusal must name the value it could not resolve:\n%s", out)
	}
}

func TestAcceptance_IssueCreate_AssigneeTypeUserSetsTheOwner(t *testing.T) {
	cfgPath, db := startIssueWorkAcceptanceServer(t)
	// Create allocates the identifier under the crew's Lead; the shared
	// seed has only a plain agent.
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status,
		cli_adapter, tool_profile, timeout_seconds, memory_enabled)
		VALUES ('iw-lead', ?, 'iw-crew', 'Lead', 'iw-lead', 'LEAD', 'IDLE', 'CLAUDE_CODE', 'CODING', 1800, 0)`,
		issueWorkAcceptanceWorkspaceID); err != nil {
		t.Fatalf("seed lead: %v", err)
	}

	out, err := runIssueWorkCLI(t, cfgPath, "issue", "create", "--crew", "iw-crew", "--title", "Owned by a person",
		"--assignee", "owner@iw-ex.com", "--assignee-type", "user")
	if err != nil {
		t.Fatalf("issue create --assignee-type user: %v\n%s", err, out)
	}
	var owner, delegate string
	if err := db.QueryRow(`SELECT COALESCE(owner_user_id,''), COALESCE(delegate_agent_id,'') FROM missions WHERE title='Owned by a person'`).
		Scan(&owner, &delegate); err != nil {
		t.Fatalf("read created issue: %v", err)
	}
	if owner != "iw-owner" || delegate != "" {
		t.Errorf("created issue owner/delegate = (%q,%q), want (iw-owner, \"\")", owner, delegate)
	}
}

func TestAcceptance_IssueReview_PinnedRevisionIsCheckedByTheServer(t *testing.T) {
	cfgPath, db := startIssueWorkAcceptanceServer(t)

	// Put IW-1 into REVIEW through the contract itself: a person takes it
	// over and submits, so the work row carries a submitted brief.
	for _, action := range []string{"take_over", "submit"} {
		if out, err := runIssueWorkCLI(t, cfgPath, "issue", "work", "IW-1", "--action", action, "--note", "Done by hand."); err != nil {
			t.Fatalf("issue work %s: %v\n%s", action, err, out)
		}
	}
	var revision, brief int
	if err := db.QueryRow(`SELECT revision, brief_revision FROM issue_work WHERE mission_id='iw-mission'`).Scan(&revision, &brief); err != nil {
		t.Fatalf("read issue_work: %v", err)
	}
	status := func() string {
		t.Helper()
		var s string
		if err := db.QueryRow(`SELECT status FROM missions WHERE id='iw-mission'`).Scan(&s); err != nil {
			t.Fatalf("read mission: %v", err)
		}
		return s
	}
	if s := status(); s != "REVIEW" {
		t.Fatalf("status after submit = %q, want REVIEW", s)
	}

	// A revision that is not the one on the row is refused, and nothing moves.
	out, err := runIssueWorkCLI(t, cfgPath, "issue", "review", "IW-1", "--action", "approve", "--revision", "99")
	if err == nil {
		t.Fatalf("a stale --revision must be refused, got success:\n%s", out)
	}
	if !strings.Contains(out, "changed") {
		t.Errorf("the refusal must be the server's 409 text:\n%s", out)
	}
	if s := status(); s != "REVIEW" {
		t.Errorf("a refused review moved the issue to %q", s)
	}

	// The same for the brief revision.
	out, err = runIssueWorkCLI(t, cfgPath, "issue", "review", "IW-1", "--action", "approve", "--brief-revision", "99")
	if err == nil {
		t.Fatalf("a stale --brief-revision must be refused, got success:\n%s", out)
	}
	if s := status(); s != "REVIEW" {
		t.Errorf("a refused review moved the issue to %q", s)
	}

	// Pinning the values actually read is accepted and lands the issue.
	out, err = runIssueWorkCLI(t, cfgPath, "issue", "review", "IW-1", "--action", "approve",
		"--revision", strconv.Itoa(revision), "--brief-revision", strconv.Itoa(brief))
	if err != nil {
		t.Fatalf("issue review --revision %d --brief-revision %d: %v\n%s", revision, brief, err, out)
	}
	if !strings.Contains(out, "Approved IW-1") {
		t.Errorf("output did not confirm the approval:\n%s", out)
	}
	if s := status(); s != "DONE" {
		t.Errorf("status after approve = %q, want DONE", s)
	}
}
