package api

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/work"
)

// settleOwnedAssignment admits an inserted assignment into durable work and
// drives its single attempt to a successful terminal state with result as the
// staged assignment output, leaving only the domain projection outstanding.
func settleOwnedAssignment(t *testing.T, db *sql.DB, assignmentID, result string) {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	receipt, err := acceptAssignmentTx(t.Context(), tx, assignmentID)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	store := work.NewStore(db)
	a, err := store.Claim(t.Context(), work.ClaimOptions{LeaseOwner: "liveness", WorkID: receipt.WorkID})
	if err != nil {
		t.Fatal(err)
	}
	code := 0
	if err = store.StageRunResult(t.Context(), a.Item.ID, a.RunID, a.Generation, work.RunResult{ExitCode: &code, Metadata: map[string]any{"assignment_result": result}}); err != nil {
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
}

// flushUntilProjected asserts the terminal projection is acknowledged and that
// later passes are no-ops rather than a retry loop.
func flushUntilProjected(t *testing.T, rt *AssignmentRuntime, db *sql.DB, assignmentID string) {
	t.Helper()
	for i := range 2 {
		if err := rt.Flush(t.Context()); err != nil {
			t.Fatalf("flush pass %d: %v", i+1, err)
		}
	}
	var status string
	var projected bool
	if err := db.QueryRow(`SELECT status,work_projected_at IS NOT NULL FROM assignments WHERE id=?`, assignmentID).Scan(&status, &projected); err != nil || status != "COMPLETED" || !projected {
		t.Fatalf("projection not acknowledged: status=%s projected=%v err=%v", status, projected, err)
	}
}

const livenessHandoff = "---HANDOFF---\nsummary: done\nconfidence: high\noutcome: SUCCEEDED\n---END HANDOFF---"

// A follow-up whose target was deleted can never be dispatched. That refusal is
// recorded on the deliveries and told to the human; it must not hold the
// finished assignment's projection hostage.
func TestAssignmentProjectionCompletesWhenFollowUpRefusedPermanently(t *testing.T) {
	f := setupMentionFixture(t)
	if err := ensureMissionChat(t.Context(), f.db, f.missionID, f.wsID, f.target, "Test issue"); err != nil {
		t.Fatal(err)
	}
	deliveries := seedFollowUpFixture(t, f, "sess_live_refused", "asg_live_refused", "PENDING", 2)
	settleOwnedAssignment(t, f.db, "asg_live_refused", livenessHandoff)
	execOrFatal(t, f.db, `UPDATE agents SET deleted_at=datetime('now') WHERE id=?`, f.target)
	writer := journal.NewWriter(f.db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	f.assign.SetJournal(writer)
	flushUntilProjected(t, &AssignmentRuntime{h: f.assign, base: &WebhookRuntime{}}, f.db, "asg_live_refused")
	for _, id := range deliveries {
		var state, dispatchState, detail string
		if err := f.db.QueryRow(`SELECT state,dispatch_state,COALESCE(dispatch_detail,'') FROM mission_comment_mentions WHERE id=?`, id).Scan(&state, &dispatchState, &detail); err != nil {
			t.Fatal(err)
		}
		if state != "failed" || dispatchState != "refused" || detail == "" {
			t.Fatalf("refused follow-up %s not recorded: state=%s dispatch=%s detail=%q", id, state, dispatchState, detail)
		}
	}
	var notices int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE workspace_id=? AND target_user_id=? AND json_extract(payload_json,'$.dispatch_state')='refused'`, f.wsID, f.userID).Scan(&notices); err != nil || notices == 0 {
		t.Fatalf("human author was not told the follow-up was refused: notices=%d err=%v", notices, err)
	}
}

func deliverableProjectionRig(t *testing.T, artifacts string, prepare func(shared, root string)) (*AssignmentRuntime, *sql.DB, string, string) {
	t.Helper()
	h, _, ws, crew, lead, _ := newTestIssueHandler(t)
	issue := seedIssue(t, h.db, ws, crew, lead, "ENG-LIVE", "TODO")
	execOrFatal(t, h.db, `INSERT INTO chats(id,workspace_id,agent_id,title) VALUES('live-deliverable-chat',?,?,'Artifacts')`, ws, lead)
	execOrFatal(t, h.db, `INSERT INTO assignments(id,workspace_id,mission_id,chat_id,assigned_by_id,assigned_to_id,task,status,created_at) VALUES('live-deliverable',?,?,'live-deliverable-chat',?,?,'Report','PENDING',datetime('now'))`, ws, issue, lead, lead)
	root := t.TempDir()
	shared := filepath.Join(root, "crews", crew, "shared")
	if prepare != nil {
		prepare(shared, root)
	}
	result := "---HANDOFF---\nsummary: verified\nconfidence: high\nartifacts: " + artifacts + "\noutcome: SUCCEEDED\n---END HANDOFF---"
	settleOwnedAssignment(t, h.db, "live-deliverable", result)
	ah := NewAssignmentHandler(h.db, nil, nil, "host", h.logger)
	att := NewAttachmentHandler(h.db, nil, h.logger)
	att.SetStoragePath(root)
	ah.attachments = att
	writer := journal.NewWriter(h.db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	ah.SetJournal(writer)
	return &AssignmentRuntime{h: ah, base: &WebhookRuntime{}}, h.db, issue, shared
}

// Declared deliverables are agent-written text. A hallucinated, missing or
// escaping path is a rejected deliverable, never a projection failure.
func TestAssignmentProjectionSkipsUnpublishableDeliverables(t *testing.T) {
	writeShared := func(shared, root string) {
		if err := os.MkdirAll(shared, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(shared, "report.md"), []byte("Verified result"), 0o600); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(root, "private.md")
		if err := os.WriteFile(outside, []byte("Private"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(shared, "escape.md")); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name      string
		artifacts string
		prepare   func(shared, root string)
		want      int
	}{
		{"nonexistent_path", "/crew/shared/does-not-exist.md, /crew/shared/report.md", writeShared, 1},
		{"escaping_symlink", "/crew/shared/escape.md, /crew/shared/report.md", writeShared, 1},
		{"rejected_type", "/crew/shared/report.exe-not-allowed, /crew/shared/report.md", func(shared, root string) {
			writeShared(shared, root)
			if err := os.WriteFile(filepath.Join(shared, "report.exe-not-allowed"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, 1},
		{"missing_shared_directory", "/crew/shared/report.md", nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, db, issue, _ := deliverableProjectionRig(t, tc.artifacts, tc.prepare)
			flushUntilProjected(t, rt, db, "live-deliverable")
			var names []string
			rows, err := db.Query(`SELECT filename FROM attachments WHERE mission_id=?`, issue)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			for rows.Next() {
				var n string
				if err = rows.Scan(&n); err != nil {
					t.Fatal(err)
				}
				names = append(names, n)
			}
			if len(names) != tc.want || (tc.want == 1 && names[0] != "report.md") {
				t.Fatalf("published deliverables=%v want %d report.md", names, tc.want)
			}
		})
	}
}

// The converse: a host fault while folding follow-ups is not a refusal. The
// projection stays unacknowledged, deliveries stay pending, and the next pass
// dispatches them once the fault clears.
func TestAssignmentProjectionRetriesTransientFollowUpFault(t *testing.T) {
	f := setupMentionFixture(t)
	if err := ensureMissionChat(t.Context(), f.db, f.missionID, f.wsID, f.target, "Test issue"); err != nil {
		t.Fatal(err)
	}
	deliveries := seedFollowUpFixture(t, f, "sess_live_fault", "asg_live_fault", "PENDING", 1)
	settleOwnedAssignment(t, f.db, "asg_live_fault", livenessHandoff)
	writer := journal.NewWriter(f.db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	f.assign.SetJournal(writer)
	rt := &AssignmentRuntime{h: f.assign, base: &WebhookRuntime{}}
	execOrFatal(t, f.db, `CREATE TRIGGER live_fault BEFORE INSERT ON assignments BEGIN SELECT RAISE(ABORT,'simulated database fault'); END`)
	if err := rt.Flush(t.Context()); err == nil {
		t.Fatal("transient follow-up fault acknowledged the projection")
	}
	var projected bool
	var state string
	if err := f.db.QueryRow(`SELECT work_projected_at IS NOT NULL FROM assignments WHERE id='asg_live_fault'`).Scan(&projected); err != nil || projected {
		t.Fatalf("projected=%v err=%v", projected, err)
	}
	if err := f.db.QueryRow(`SELECT state FROM mission_comment_mentions WHERE id=?`, deliveries[0]).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("delivery state=%s err=%v", state, err)
	}
	execOrFatal(t, f.db, `DROP TRIGGER live_fault`)
	flushUntilProjected(t, rt, f.db, "asg_live_fault")
	if err := f.db.QueryRow(`SELECT state FROM mission_comment_mentions WHERE id=?`, deliveries[0]).Scan(&state); err != nil || state != "claimed" {
		t.Fatalf("follow-up not dispatched after the fault cleared: state=%s err=%v", state, err)
	}
}
