package api

import (
	"bytes"
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/automation"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

type commentLoopEnqueuer struct {
	store *pipeline.PendingRunStore
	runs  []pipeline.PendingRun
}

func (e *commentLoopEnqueuer) Enqueue(ctx context.Context, pr pipeline.PendingRun) (string, bool, error) {
	id, coalesced, err := e.store.Enqueue(ctx, pr)
	if err == nil {
		pr.ID = id
		e.runs = append(e.runs, pr)
	}
	return id, coalesced, err
}

// Exercise the actual comment handlers, durable outbox, journal observer and
// database chain reader together. Only executing the routine is simulated:
// its recorded run writes a comment through the production crewship body.
func TestCommentingAutomationStopsAtChainDepth(t *testing.T) {
	r := newChainsListRig(t)
	mission := seedIssue(t, r.db, r.ws, r.crew, r.lead, "ENG-1", "TODO")
	now := time.Now().UTC()
	enq := &commentLoopEnqueuer{store: pipeline.NewPendingRunStore(r.db)}
	reg := automation.NewRegistry(nil, enq, automation.Options{Journal: r.jw, Now: func() time.Time { return now }})
	reg.SetChainSource(pipeline.NewRunChainReader(r.db))
	reg.Load([]automation.Resolved{{
		Automation: automation.Automation{
			ID: "comment-loop", WorkspaceID: r.ws, Name: "Comment on every comment", Enabled: true,
			EventType: string(journal.EntryMissionComment), ActionKind: automation.ActionKindRoutine,
			Action: automation.Action{RoutineSlug: "deploy"}, DebounceSeconds: 1, MaxPerHour: 10000,
		},
		PipelineID: "p1", PipelineSlug: "deploy",
	}})
	r.jw.AddCommitObserver(reg.Observer)
	human := NewIssueHandler(r.db, nil, nil, newTestLogger())
	human.SetJournal(r.jw)
	req := httptest.NewRequest("POST", "/", jsonBody(map[string]string{"body": "Please report progress"}))
	req.SetPathValue("crewId", r.crew)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	human.CreateComment(rr, withWorkspaceUser(req, r.user, r.ws, "OWNER"))
	if rr.Code != 201 {
		t.Fatalf("human comment: %d %s", rr.Code, rr.Body.String())
	}
	if err := r.jw.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := reg.Flush(t.Context()); got != 1 {
		t.Fatalf("initial human comment enqueued %d runs, want 1", got)
	}
	for i := 0; i < 30; i++ {
		if i >= len(enq.runs) {
			break
		}
		pr := enq.runs[i]
		if pr.ChainDepth != i+1 || (i > 0 && pr.ChainOrigin != enq.runs[0].ID) {
			t.Fatalf("automation hop %d lost its chain: depth=%d origin=%q", i, pr.ChainDepth, pr.ChainOrigin)
		}
		if marked, err := enq.store.MarkFired(t.Context(), pr.ID, pr.ID); err != nil || !marked {
			t.Fatalf("fire pending automation: marked=%v err=%v", marked, err)
		}
		r.seedRun(t, runSpec{id: pr.ID, depth: pr.ChainDepth, origin: pr.ChainOrigin, via: pipeline.TriggeredViaAutomation})
		seedAutonomousIssueRun(t, r.issues, r.ws, r.lead, pr.ID)
		now = now.Add(2 * time.Second)
		body := r.crewshipCall(t, "issue.comment", pr.ID, map[string]any{"body": "Automated progress update"})
		req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
		req.SetPathValue("identifier", "ENG-1")
		rr := httptest.NewRecorder()
		r.issues.CreateComment(rr, req)
		if rr.Code != 201 {
			t.Fatalf("automation comment at depth %d: %d %s", pr.ChainDepth, rr.Code, rr.Body.String())
		}
		if err := r.jw.Flush(t.Context()); err != nil {
			t.Fatal(err)
		}
		reg.Flush(t.Context())
	}
	if got := len(enq.runs); got != pipeline.MaxChainDepth {
		t.Fatalf("comment loop enqueued %d runs, want exactly %d; must trigger normally and stop at the depth cap", got, pipeline.MaxChainDepth)
	}
	assertIssueRows(t, r.db, `SELECT COUNT(*) FROM mission_comments WHERE mission_id=?`, pipeline.MaxChainDepth+1, mission)
	if err := r.jw.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertIssueRows(t, r.db, `SELECT COUNT(*) FROM journal_entries WHERE workspace_id=? AND entry_type=?`, 1, r.ws, journal.EntryAutomationDepthExceeded)
}
