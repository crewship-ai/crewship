package api

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/work"
)

// A completed execution may settle after its requester loses permission. The
// durable projection must settle that execution without publishing its text as
// an autonomous issue comment. Exercise Flush, not the legacy finish path.
func TestAssignmentProjectionRechecksRequesterBeforePublishing(t *testing.T) {
	for _, mode := range []string{"revoked_requester", "human_missing_origin", "valid_origin"} {
		t.Run(mode, func(t *testing.T) {
			f := newChatIssueFixture(t)
			issue := seedIssue(t, f.h.db, f.workspace, f.crew, f.agent, "ENG-PROJECTION-AUTHORITY", "TODO")
			origin := ""
			if mode != "human_missing_origin" {
				origin = captureReviewOrigin(t, f.h, f.workspace, f.agent, f.run)
			}
			execOrFatal(t, f.h.db, `INSERT INTO assignments(id,workspace_id,chat_id,group_id,mission_id,assigned_by_id,assigned_to_id,task,status,created_at,created_by_user_id,chat_origin_id,lead_planning) VALUES('delayed-planning',?,?,?,?,?,?,'plan work','PENDING',datetime('now'),?,NULLIF(?,''),1)`, f.workspace, f.chat, issue, issue, f.agent, f.agent, f.user, origin)
			var tasks int
			if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM mission_tasks WHERE assignment_id='delayed-planning'`).Scan(&tasks); err != nil || tasks != 0 {
				t.Fatalf("expected root planning without mission task: tasks=%d err=%v", tasks, err)
			}
			tx, err := f.h.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			receipt, err := acceptAssignmentTx(t.Context(), tx, "delayed-planning")
			if err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			store := work.NewStore(f.h.db)
			a, err := store.Claim(t.Context(), work.ClaimOptions{LeaseOwner: "projection-authority", WorkID: receipt.WorkID})
			if err != nil {
				t.Fatal(err)
			}
			code := 0
			if err = store.StageRunResult(t.Context(), a.Item.ID, a.RunID, a.Generation, work.RunResult{ExitCode: &code, Metadata: map[string]any{"assignment_result": "---HANDOFF---\nsummary: requester scoped result\nconfidence: high\noutcome: SUCCEEDED\n---END HANDOFF---"}}); err != nil {
				t.Fatal(err)
			}
			if err = store.MarkStarting(t.Context(), a.Item.ID, a.RunID, a.Generation, "agent-run:"+a.RunID); err != nil {
				t.Fatal(err)
			}
			if err = store.StartRunning(t.Context(), a.Item.ID, a.RunID, a.Generation, "agent-run:"+a.RunID); err != nil {
				t.Fatal(err)
			}
			if err = store.Transition(t.Context(), work.TransitionRequest{WorkID: a.Item.ID, RunID: a.RunID, Generation: a.Generation, To: work.StateSucceeded, Reason: "observed exit"}); err != nil {
				t.Fatal(err)
			}
			// Revocation happens after terminal work, before delayed projection.
			if mode == "revoked_requester" {
				execOrFatal(t, f.h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, f.workspace, f.user)
			}
			h := NewAssignmentHandler(f.h.db, nil, nil, "host", newTestLogger())
			writer := journal.NewWriter(f.h.db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
			t.Cleanup(func() { _ = writer.Close() })
			h.SetJournal(writer)
			rt := &AssignmentRuntime{h: h, base: &WebhookRuntime{}}
			for range 3 {
				if err = rt.Flush(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			var status string
			var acknowledged bool
			if err = f.h.db.QueryRow(`SELECT status,work_projected_at IS NOT NULL FROM assignments WHERE id='delayed-planning'`).Scan(&status, &acknowledged); err != nil || status != "COMPLETED" || !acknowledged {
				t.Fatalf("lifecycle must settle: status=%s acknowledged=%v err=%v", status, acknowledged, err)
			}
			want := 0
			if mode == "valid_origin" {
				want = 1
			}
			for _, table := range []string{"mission_comments", "mission_activity"} {
				var count int
				if err = f.h.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE mission_id=?`, issue).Scan(&count); err != nil || count != want {
					t.Errorf("%s count=%d want=%d after repeated Flush, err=%v", table, count, want, err)
				}
			}
			if mode == "valid_origin" {
				var storedOrigin, commentID string
				if err = f.h.db.QueryRow(`SELECT id,COALESCE(chat_origin_id,'') FROM mission_comments WHERE mission_id=?`, issue).Scan(&commentID, &storedOrigin); err != nil || storedOrigin != origin || commentID != "assignment-comment:delayed-planning" {
					t.Errorf("comment lost stable provenance: id=%q origin=%q want=%q err=%v", commentID, storedOrigin, origin, err)
				}
			}
		})
	}
}
