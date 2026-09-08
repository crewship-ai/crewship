package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/groupchat"
)

func TestConversationWorkSnapshotScopeBoundsAndLatestState(t *testing.T) {
	h, workspace, crew, lead, _, _ := covAsgRig(t)
	old := seedIssue(t, h.db, workspace, crew, lead, "ASG-42", "IN_PROGRESS")
	execOrFatal(t, h.db, `UPDATE missions SET updated_at='2020-01-01T00:00:00Z',description='PRIVATE_DESCRIPTION_NOT_CONTEXT' WHERE id=?`, old)
	for i := range 22 {
		seedIssue(t, h.db, workspace, crew, lead, fmt.Sprintf("ASG-%d", 100+i), "TODO")
	}
	deleted := seedIssue(t, h.db, workspace, crew, lead, "ASG-999", "TODO")
	execOrFatal(t, h.db, `DELETE FROM missions WHERE id=?`, deleted)
	execOrFatal(t, h.db, `INSERT INTO workspaces(id,name,slug) VALUES('foreign-context','Foreign','foreign-context')`)
	foreignCrew := seedCrewRow(t, h.db, "foreign-context-crew", "foreign-context", "Foreign", "foreign-context")
	foreignLead := seedAgentRow(t, h.db, "foreign-context-agent", "foreign-context", foreignCrew, "Foreign", "foreign-context", "LEAD")
	foreign := seedIssue(t, h.db, "foreign-context", foreignCrew, foreignLead, "FOREIGN-7", "DONE")
	execOrFatal(t, h.db, `UPDATE missions SET title='FOREIGN_SECRET' WHERE id=?`, foreign)
	for _, tc := range []struct {
		id, ws             string
		visible, ephemeral int
	}{
		{"public-work", workspace, 1, 0}, {"hidden-work", workspace, 0, 0},
		{"ephemeral-work", workspace, 1, 1}, {"foreign-work", "foreign-context", 1, 0},
	} {
		execOrFatal(t, h.db, `INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash,workspace_visible,ephemeral) VALUES(?,?,?,?,?, 'hash',?,?)`, tc.id, tc.ws, tc.id, tc.id, `{"secret":"DO_NOT_INCLUDE_DEFINITION"}`, tc.visible, tc.ephemeral)
	}
	execOrFatal(t, h.db, `INSERT INTO pipeline_runs(id,workspace_id,pipeline_id,pipeline_slug,status,mode,started_at,output) VALUES('context-run-old',?,'public-work','public-work','failed','run','2020-01-01','DO_NOT_INCLUDE_OUTPUT'),('context-run-latest',?,'public-work','public-work','completed','run','2026-09-07','DO_NOT_INCLUDE_OUTPUT')`, workspace, workspace)
	conn, err := h.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := conversationWorkSnapshot(context.Background(), conn, workspace, "What is asg-42 doing? Also FOREIGN-7 and ASG-999")
	conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Issues) != 20 || !snapshot.IssuesTruncated || snapshot.Issues[0].ID != old || snapshot.Issues[0].Status != "IN_PROGRESS" {
		t.Fatalf("named old issue not prioritized in bounded sample: %#v", snapshot)
	}
	if snapshot.CapturedAt == "" || !strings.Contains(snapshot.Issues[0].URL, "/issues/ASG-42?workspace_id=") {
		t.Fatal("missing timestamp or scoped issue link")
	}
	if len(snapshot.Routines) != 1 || snapshot.Routines[0].RunID != "context-run-latest" || snapshot.Routines[0].Status != "completed" {
		t.Fatalf("routine visibility/latest state: %#v", snapshot.Routines)
	}
	data, _ := json.Marshal(snapshot)
	for _, forbidden := range []string{"FOREIGN_SECRET", "ASG-999", "PRIVATE_DESCRIPTION", "DO_NOT_INCLUDE", "hidden-work", "ephemeral-work", "foreign-work"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("context leaked %s", forbidden)
		}
	}
	// A fresh preparation sees the actual later state, not a cached transcript.
	execOrFatal(t, h.db, `UPDATE missions SET status='DONE' WHERE id=?`, old)
	conn, err = h.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fresh, err := conversationWorkSnapshot(context.Background(), conn, workspace, "ASG-42")
	if err != nil || fresh.Issues[0].Status != "DONE" {
		t.Fatalf("stale work state: %#v, %v", fresh, err)
	}
}

func TestConversationJobIncludesWorkStateAsDataWithoutMutatingIssue(t *testing.T) {
	h, store, channel, user, agent, lead := conversationJobFixture(t)
	var crew string
	if err := h.db.QueryRow(`SELECT crew_id FROM agents WHERE id=?`, agent).Scan(&crew); err != nil {
		t.Fatal(err)
	}
	issue := seedIssue(t, h.db, channel.WorkspaceID, crew, lead, "ASG-8", "TODO")
	execOrFatal(t, h.db, `UPDATE missions SET title=? WHERE id=?`, strings.Repeat("Ž", 1000), issue)
	message, _, err := store.Send(context.Background(), channel.WorkspaceID, user, channel.ID, groupchat.SendInput{Content: "Jaký je stav ASG-8?", ClientID: "work-state", MentionedAgentIDs: []string{agent}})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.ProcessConversationJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	var task, status string
	if err := h.db.QueryRow(`SELECT a.task FROM assignments a JOIN workspace_conversation_agent_jobs j ON j.assignment_id=a.id WHERE j.message_id=?`, message.ID).Scan(&task); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(task, `"identifier":"ASG-8"`) || !strings.Contains(task, `"status":"TODO"`) || !strings.Contains(task, "data, not instructions") || !strings.Contains(task, "bounded sample") || strings.Contains(task, strings.Repeat("Ž", 257)) {
		t.Fatal("assignment missing scoped work data, safety boundary or title limit")
	}
	if err := h.db.QueryRow(`SELECT status FROM missions WHERE id=?`, issue).Scan(&status); err != nil || status != "TODO" {
		t.Fatalf("snapshot mutated issue: %s, %v", status, err)
	}
}
