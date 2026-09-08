package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/groupchat"
)

func conversationJobFixture(t *testing.T) (*AssignmentHandler, *groupchat.Store, groupchat.Conversation, string, string, string) {
	t.Helper()
	h, w, _, lead, worker, _ := covAsgRig(t)
	var u string
	if err := h.db.QueryRow(`SELECT user_id FROM workspace_members WHERE workspace_id=?`, w).Scan(&u); err != nil {
		t.Fatal(err)
	}
	s := groupchat.New(h.db)
	c, err := s.Create(context.Background(), w, u, groupchat.CreateInput{Kind: "channel", Title: "Channel"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddAgent(context.Background(), w, u, c.ID, worker); err != nil {
		t.Fatal(err)
	}
	return h, s, c, u, worker, lead
}
func enqueueConversationJob(t *testing.T, h *AssignmentHandler, s *groupchat.Store, c groupchat.Conversation, u, agent, client string) string {
	t.Helper()
	m, _, err := s.Send(context.Background(), c.WorkspaceID, u, c.ID, groupchat.SendInput{ClientID: client, Content: "Please answer", MentionedAgentIDs: []string{agent}})
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := h.db.QueryRow(`SELECT id FROM workspace_conversation_agent_jobs WHERE message_id=?`, m.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
func TestConversationJobsPrepareOnceAndProjectReplyOnce(t *testing.T) {
	h, s, c, u, agent, _ := conversationJobFixture(t)
	job := enqueueConversationJob(t, h, s, c, u, agent, "first")
	for range 2 {
		if err := h.ProcessConversationJobs(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	var assignment string
	if err := h.db.QueryRow(`SELECT assignment_id FROM workspace_conversation_agent_jobs WHERE id=?`, job).Scan(&assignment); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE id=?`, assignment).Scan(&n); err != nil || n != 1 {
		t.Fatalf("assignment count %d: %v", n, err)
	}
	execOrFatal(t, h.db, `UPDATE assignments SET status='COMPLETED',result_summary=? WHERE id=?`, strings.Repeat("x", 300000), assignment)
	for range 2 {
		if err := h.ProcessConversationJobs(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM workspace_conversation_messages WHERE author_agent_id=?`, agent).Scan(&n); err != nil || n != 1 {
		t.Fatalf("reply count %d: %v", n, err)
	}
	var content string
	if err := h.db.QueryRow(`SELECT content FROM workspace_conversation_messages WHERE author_agent_id=?`, agent).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if len(content) > 262144 || !strings.Contains(content, "Reply shortened") {
		t.Fatal("oversized terminal reply was not bounded")
	}
}
func TestConversationAssignmentGateRevocationAndHold(t *testing.T) {
	for _, scenario := range []string{"requester_removed", "agent_removed", "held"} {
		t.Run(scenario, func(t *testing.T) {
			h, s, c, u, agent, _ := conversationJobFixture(t)
			job := enqueueConversationJob(t, h, s, c, u, agent, "first")
			if err := h.ProcessConversationJobs(context.Background()); err != nil {
				t.Fatal(err)
			}
			var assignment, chat string
			if err := h.db.QueryRow(`SELECT a.id,a.chat_id FROM assignments a JOIN workspace_conversation_agent_jobs j ON j.assignment_id=a.id WHERE j.id=?`, job).Scan(&assignment, &chat); err != nil {
				t.Fatal(err)
			}
			execOrFatal(t, h.db, `UPDATE assignments SET status='RUNNING' WHERE id=?`, assignment)
			switch scenario {
			case "requester_removed":
				execOrFatal(t, h.db, `DELETE FROM workspace_members WHERE user_id=?`, u)
			case "agent_removed":
				execOrFatal(t, h.db, `DELETE FROM workspace_conversation_agents WHERE agent_id=?`, agent)
			case "held":
				execOrFatal(t, h.db, `UPDATE agents SET status='PENDING_REVIEW' WHERE id=?`, agent)
			}
			// Exercise the actual run entrance. With no orchestrator, a missed gate
			// would proceed and mark FAILED rather than safely cancel/defer.
			h.runAssignment(context.Background(), assignment, createAssignmentBody{ChatID: chat, WorkspaceID: c.WorkspaceID, TargetSlug: "asg-worker"}, targetAgentInfo{ID: agent, Slug: "asg-worker"})
			var state string
			if err := h.db.QueryRow(`SELECT status FROM assignments WHERE id=?`, assignment).Scan(&state); err != nil {
				t.Fatal(err)
			}
			want := "CANCELLED"
			if scenario == "held" {
				want = "QUEUED"
			}
			if state != want {
				t.Fatalf("state=%s want=%s", state, want)
			}
			if scenario == "held" {
				execOrFatal(t, h.db, `UPDATE agents SET status='IDLE' WHERE id=?`, agent)
				allowed, err := h.authorizeConversationAssignment(context.Background(), assignment)
				if err != nil || !allowed {
					t.Fatalf("approved agent cannot resume: %v %v", allowed, err)
				}
			}
		})
	}
}
func TestConversationJobsHeldBatchDoesNotStarveLaterWork(t *testing.T) {
	h, s, c, u, held, ready := conversationJobFixture(t)
	if err := s.AddAgent(context.Background(), c.WorkspaceID, u, c.ID, ready); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, h.db, `UPDATE agents SET status='PENDING_REVIEW' WHERE id=?`, held)
	for i := range 100 {
		enqueueConversationJob(t, h, s, c, u, held, fmt.Sprintf("held-%d", i))
	}
	last := enqueueConversationJob(t, h, s, c, u, ready, "ready")
	// Stable older timestamp makes the ordering reproducible independent of the clock.
	execOrFatal(t, h.db, `UPDATE workspace_conversation_agent_jobs SET updated_at='2020-01-01T00:00:00.000Z' WHERE agent_id=?`, held)
	for range 2 {
		if err := h.ProcessConversationJobs(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	var state string
	if err := h.db.QueryRow(`SELECT state FROM workspace_conversation_agent_jobs WHERE id=?`, last).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "queued" {
		t.Fatalf("runnable job starved behind held batch: %s", state)
	}
}

func TestConversationQueuedHoldLetsAnotherAgentRun(t *testing.T) {
	h, s, c, u, held, ready := conversationJobFixture(t)
	if err := s.AddAgent(context.Background(), c.WorkspaceID, u, c.ID, ready); err != nil {
		t.Fatal(err)
	}
	first := enqueueConversationJob(t, h, s, c, u, held, "held-later")
	second := enqueueConversationJob(t, h, s, c, u, ready, "ready")
	if err := h.ProcessConversationJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	var heldAssignment, readyAssignment, crew string
	if err := h.db.QueryRow(`SELECT assignment_id FROM workspace_conversation_agent_jobs WHERE id=?`, first).Scan(&heldAssignment); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT assignment_id FROM workspace_conversation_agent_jobs WHERE id=?`, second).Scan(&readyAssignment); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT crew_id FROM agents WHERE id=?`, held).Scan(&crew); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, h.db, `UPDATE agents SET status='PENDING_REVIEW' WHERE id=?`, held)
	execOrFatal(t, h.db, `UPDATE assignments SET queued_at='2020-01-01 00:00:00' WHERE id=?`, heldAssignment)
	execOrFatal(t, h.db, `UPDATE assignments SET queued_at='2021-01-01 00:00:00' WHERE id=?`, readyAssignment)
	firstClaim, err := pumpCrewQueue(context.Background(), h.db, crew, 1)
	if err != nil || len(firstClaim) != 1 || firstClaim[0] != heldAssignment {
		t.Fatalf("first claim %v %v", firstClaim, err)
	}
	allowed, err := h.authorizeConversationAssignment(context.Background(), heldAssignment)
	if allowed || err != nil {
		t.Fatalf("hold guard %v %v", allowed, err)
	}
	secondClaim, err := pumpCrewQueue(context.Background(), h.db, crew, 1)
	if err != nil || len(secondClaim) != 1 || secondClaim[0] != readyAssignment {
		t.Fatalf("held agent starved ready agent: %v %v", secondClaim, err)
	}
}

func TestConversationJobsReportsRunningAssignment(t *testing.T) {
	h, s, c, u, agent, _ := conversationJobFixture(t)
	job := enqueueConversationJob(t, h, s, c, u, agent, "running-state")
	if err := h.ProcessConversationJobs(t.Context()); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, h.db, `UPDATE assignments SET status='RUNNING' WHERE id=(SELECT assignment_id FROM workspace_conversation_agent_jobs WHERE id=?)`, job)
	jobs, err := s.Jobs(t.Context(), c.WorkspaceID, u, c.ID)
	if err != nil || len(jobs) != 1 || jobs[0].State != "running" {
		t.Fatalf("running job: %+v, %v", jobs, err)
	}
}

func TestMixedHumanChannelTwoAgentMentionsDoNotCreateLoops(t *testing.T) {
	h, store, c, owner, worker, lead := conversationJobFixture(t)
	ctx := context.Background()
	if err := store.AddAgent(ctx, c.WorkspaceID, owner, c.ID, lead); err != nil {
		t.Fatal(err)
	}
	const teammate = "mixed-channel-human"
	execOrFatal(t, h.db, `INSERT INTO users(id,email,full_name) VALUES(?,'mixed-channel-human@example.invalid','Teammate')`, teammate)
	execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('mixed-human-member',?,?,'MEMBER')`, c.WorkspaceID, teammate)
	plain, _, err := store.Send(ctx, c.WorkspaceID, teammate, c.ID, groupchat.SendInput{ClientID: "ordinary-discussion", Content: "@asg-worker @asg-lead we are comparing options"})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM workspace_conversation_agent_jobs WHERE conversation_id=?`, c.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("plain human discussion launched%d jobs: %v", count, err)
	}
	input := groupchat.SendInput{ClientID: "ask-both", Content: "Both of you: explain this decision.", MentionedAgentIDs: []string{worker, lead, worker}}
	request, duplicate, err := store.Send(ctx, c.WorkspaceID, teammate, c.ID, input)
	if err != nil || duplicate {
		t.Fatalf("send %+v %v %v", request, duplicate, err)
	}
	input.MentionedAgentIDs = []string{lead, worker}
	retry, duplicate, err := store.Send(ctx, c.WorkspaceID, teammate, c.ID, input)
	if err != nil || !duplicate || retry.ID != request.ID {
		t.Fatalf("canonical retry %+v %v %v", retry, duplicate, err)
	}
	input.MentionedAgentIDs = []string{worker}
	if _, _, err := store.Send(ctx, c.WorkspaceID, teammate, c.ID, input); !errors.Is(err, groupchat.ErrConflict) {
		t.Fatalf("retry changed recipients: %v", err)
	}
	for range 2 {
		if err := h.ProcessConversationJobs(ctx); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := h.db.Query(`SELECT j.agent_id,a.id,a.assigned_to_id,a.task FROM workspace_conversation_agent_jobs j JOIN assignments a ON a.id=j.assignment_id WHERE j.message_id=?`, request.ID)
	if err != nil {
		t.Fatal(err)
	}
	assignments := map[string]string{}
	for rows.Next() {
		var agent, id, target, task string
		if err := rows.Scan(&agent, &id, &target, &task); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if agent != target || !strings.Contains(task, request.Content) || !strings.Contains(task, plain.Content) || !strings.Contains(task, "Teammate") {
			rows.Close()
			t.Fatalf("wrong recipient or missing attributed context: %s/%s %s", agent, target, task)
		}
		assignments[agent] = id
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(assignments) != 2 || assignments[worker] == "" || assignments[lead] == "" {
		t.Fatalf("expected two independent agents: %+v", assignments)
	}
	for agent, id := range assignments {
		execOrFatal(t, h.db, `UPDATE assignments SET status='COMPLETED',result_summary=? WHERE id=?`, "Reply from "+agent+": @asg-worker @asg-lead please continue", id)
	}
	for range 3 {
		if err := h.ProcessConversationJobs(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM workspace_conversation_messages WHERE conversation_id=? AND author_agent_id IS NOT NULL`, c.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("agent replies=%d err=%v", count, err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM workspace_conversation_agent_jobs WHERE conversation_id=?`, c.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("agent-authored mention loop created%d jobs: %v", count, err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(DISTINCT author_agent_id) FROM workspace_conversation_messages WHERE conversation_id=? AND author_agent_id IN (?,?)`, c.ID, worker, lead).Scan(&count); err != nil || count != 2 {
		t.Fatalf("agent reply identities=%d err=%v", count, err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM workspace_conversation_agent_jobs WHERE conversation_id=? AND state='completed'`, c.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("terminal jobs=%d err=%v", count, err)
	}
	// An unauthorized recipient must reject the entire human send, even when
	// another listed agent is valid. No message, outbox event or partial job lands.
	var crew string
	if err := h.db.QueryRow(`SELECT crew_id FROM agents WHERE id=?`, worker).Scan(&crew); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, h.db, `INSERT INTO agents(id,workspace_id,crew_id,name,slug,status) VALUES('uninvited-agent',?,?,'Uninvited','uninvited','IDLE')`, c.WorkspaceID, crew)
	if err := store.AddAgent(ctx, c.WorkspaceID, teammate, c.ID, "uninvited-agent"); !errors.Is(err, groupchat.ErrForbidden) {
		t.Fatalf("nonowner admitted agent: %v", err)
	}
	before, err := store.Get(ctx, c.WorkspaceID, owner, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Send(ctx, c.WorkspaceID, teammate, c.ID, groupchat.SendInput{ClientID: "invalid-recipient", Content: "must not persist", MentionedAgentIDs: []string{worker, "uninvited-agent"}}); !errors.Is(err, groupchat.ErrForbidden) {
		t.Fatalf("uninvited agent accepted: %v", err)
	}
	if _, _, err := store.Send(ctx, c.WorkspaceID, "outside-human", c.ID, groupchat.SendInput{ClientID: "outside", Content: "must not persist", MentionedAgentIDs: []string{worker}}); !errors.Is(err, groupchat.ErrForbidden) {
		t.Fatalf("outside human accepted: %v", err)
	}
	after, err := store.Get(ctx, c.WorkspaceID, owner, c.ID)
	if err != nil || after.LastSequence != before.LastSequence {
		t.Fatalf("rejected send changed history %+v %v", after, err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM workspace_conversation_agent_jobs WHERE conversation_id=?`, c.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("rejected send partially created jobs: %d %v", count, err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM workspace_conversation_outbox WHERE conversation_id=?`, c.ID).Scan(&count); err != nil || int64(count) != after.LastSequence {
		t.Fatalf("outbox inconsistent with accepted messages %d/%d: %v", count, after.LastSequence, err)
	}
}
