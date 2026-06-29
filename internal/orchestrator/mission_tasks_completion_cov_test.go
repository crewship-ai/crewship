package orchestrator

// Coverage tests for mission_tasks_completion.go: OnAssignmentCompleted
// handoff persistence + comments + retry + approval hold, the
// checkMissionCompletionWithTasks lead-planning fast path, the inbox
// notification for issue missions, and the dependent-task failure cascade.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestOnAssignmentCompleted_HandoffConfidencePersisted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		confidence string
		wantConf   float64
		wantReview int
	}{
		{"high", "high", 0.9, 0},
		{"medium", "medium", 0.7, 0},
		{"low", "low", 0.4, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db := covMissionDB(t)
			covSeed(t, db)
			covMission(t, db, "m1", "IN_PROGRESS")
			now := time.Now().UTC()
			started := now.Add(-2 * time.Second).Format(time.RFC3339)
			mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, assignment_id, started_at, created_at, updated_at)
				VALUES ('t1', 'm1', 'agent-worker', 'Work', 'IN_PROGRESS', 1, '[]', 'as1', ?, ?, ?)`,
				started, started, started)

			e := newLifecycleEngine(t, db)
			result := fmt.Sprintf("did stuff\n---HANDOFF---\nsummary: finished the task\nconfidence: %s\nartifacts: out.md\n---END HANDOFF---", tc.confidence)
			if err := e.OnAssignmentCompleted(context.Background(), "as1", "COMPLETED", result, ""); err != nil {
				t.Fatalf("OnAssignmentCompleted: %v", err)
			}

			var status, handoffCtx string
			var conf float64
			var review int
			if err := db.QueryRow(`SELECT status, confidence, needs_review, handoff_context FROM mission_tasks WHERE id = 't1'`).
				Scan(&status, &conf, &review, &handoffCtx); err != nil {
				t.Fatalf("scan task: %v", err)
			}
			if status != "COMPLETED" {
				t.Errorf("status = %q, want COMPLETED", status)
			}
			if conf != tc.wantConf {
				t.Errorf("confidence = %v, want %v", conf, tc.wantConf)
			}
			if review != tc.wantReview {
				t.Errorf("needs_review = %d, want %d", review, tc.wantReview)
			}
			if handoffCtx != "finished the task" {
				t.Errorf("handoff_context = %q", handoffCtx)
			}

			// Agent comment with confidence + artifacts must be posted.
			var body string
			if err := db.QueryRow(`SELECT body FROM mission_comments WHERE mission_id = 'm1'`).Scan(&body); err != nil {
				t.Fatalf("expected a mission comment: %v", err)
			}
			if !strings.Contains(body, "Bob completed their work") || !strings.Contains(body, tc.confidence) {
				t.Errorf("comment body wrong: %q", body)
			}
			if !strings.Contains(body, "**Artifacts:** out.md") {
				t.Errorf("comment must list artifacts: %q", body)
			}
			var action string
			if err := db.QueryRow(`SELECT action FROM mission_activity WHERE mission_id = 'm1'`).Scan(&action); err != nil {
				t.Fatalf("expected an activity row: %v", err)
			}
			if action != "task_completed" {
				t.Errorf("activity action = %q", action)
			}
			// duration_ms must be derived from started_at.
			var dur int64
			db.QueryRow(`SELECT COALESCE(duration_ms, 0) FROM mission_tasks WHERE id = 't1'`).Scan(&dur)
			if dur <= 0 {
				t.Errorf("duration_ms = %d, want > 0", dur)
			}
		})
	}
}

func TestOnAssignmentCompleted_CommentVariants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		status     string
		result     string
		errMsg     string
		wantInBody []string
	}{
		{
			name: "completed without summary", status: "COMPLETED", result: "",
			wantInBody: []string{"**Bob completed their work.**"},
		},
		{
			name: "completed long summary truncated", status: "COMPLETED",
			result:     strings.Repeat("z", 600),
			wantInBody: []string{"Bob completed their work", "..."},
		},
		{
			name: "failed with error", status: "FAILED", errMsg: "container OOM",
			wantInBody: []string{"**Bob encountered an issue.**", "Error: container OOM"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db := covMissionDB(t)
			covSeed(t, db)
			covMission(t, db, "m1", "IN_PROGRESS")
			now := time.Now().UTC().Format(time.RFC3339)
			mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, assignment_id, created_at, updated_at)
				VALUES ('t1', 'm1', 'agent-worker', 'Work', 'IN_PROGRESS', 1, '[]', 'as1', ?, ?)`, now, now)

			e := newLifecycleEngine(t, db)
			if err := e.OnAssignmentCompleted(context.Background(), "as1", tc.status, tc.result, tc.errMsg); err != nil {
				t.Fatalf("OnAssignmentCompleted: %v", err)
			}
			var body string
			if err := db.QueryRow(`SELECT body FROM mission_comments WHERE mission_id = 'm1'`).Scan(&body); err != nil {
				t.Fatalf("expected a mission comment: %v", err)
			}
			for _, want := range tc.wantInBody {
				if !strings.Contains(body, want) {
					t.Errorf("comment %q missing %q", body, want)
				}
			}
			if tc.name == "failed with error" {
				var action string
				db.QueryRow(`SELECT action FROM mission_activity WHERE mission_id = 'm1'`).Scan(&action)
				if action != "task_failed" {
					t.Errorf("activity action = %q, want task_failed", action)
				}
			}
		})
	}
}

func TestOnAssignmentCompleted_RetryViaMaxIterations(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	ms := covMission(t, db, "m1", "IN_PROGRESS")
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, assignment_id, iteration, max_iterations, created_at, updated_at)
		VALUES ('t-retry', 'm1', 'agent-worker', 'Flaky', 'IN_PROGRESS', 1, '[]', 'as1', 1, 3, ?, ?)`, now, now)

	e := newLifecycleEngine(t, db)
	e.mu.Lock()
	e.active["m1"] = ms
	e.mu.Unlock()

	if err := e.OnAssignmentCompleted(context.Background(), "as1", "FAILED", "", "flaked"); err != nil {
		t.Fatalf("OnAssignmentCompleted: %v", err)
	}

	var status string
	var iteration int
	if err := db.QueryRow(`SELECT status, iteration FROM mission_tasks WHERE id = 't-retry'`).Scan(&status, &iteration); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if status != "PENDING" {
		t.Errorf("retried task status = %q, want PENDING", status)
	}
	if iteration != 2 {
		t.Errorf("iteration = %d, want 2", iteration)
	}
	// Circuit breaker must have tracked the failure.
	e.cbMu.Lock()
	fails := e.failures["agent-worker"]
	e.cbMu.Unlock()
	if fails != 1 {
		t.Errorf("failure count = %d, want 1", fails)
	}
}

func TestOnAssignmentCompleted_ApprovalGateHoldsTask(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	ms := covMission(t, db, "m1", "IN_PROGRESS")
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, assignment_id, approval_required, created_at, updated_at)
		VALUES ('t-gate', 'm1', 'agent-worker', 'Gated', 'IN_PROGRESS', 1, '[]', 'as1', 1, ?, ?)`, now, now)

	e := newLifecycleEngine(t, db)
	e.mu.Lock()
	e.active["m1"] = ms
	e.mu.Unlock()

	if err := e.OnAssignmentCompleted(context.Background(), "as1", "COMPLETED", "done", ""); err != nil {
		t.Fatalf("OnAssignmentCompleted: %v", err)
	}
	if got := covTaskStatus(t, db, "t-gate"); got != "AWAITING_APPROVAL" {
		t.Errorf("approval_required task = %q, want AWAITING_APPROVAL", got)
	}
}

func TestOnAssignmentCompleted_UnlinkedAssignmentIsNoop(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	e := newLifecycleEngine(t, db)
	if err := e.OnAssignmentCompleted(context.Background(), "no-such-assignment", "COMPLETED", "", ""); err != nil {
		t.Fatalf("unlinked assignment must be a nil no-op, got %v", err)
	}
}

// ---- checkMissionCompletionWithTasks: lead-planning fast path ----

func TestCheckMissionCompletionWithTasks_EmptyTaskVariants(t *testing.T) {
	t.Parallel()

	t.Run("planning not dispatched yet", func(t *testing.T) {
		t.Parallel()
		db := covMissionDB(t)
		covSeed(t, db)
		ms := covMission(t, db, "m1", "IN_PROGRESS")
		e := newLifecycleEngine(t, db)
		if err := e.checkMissionCompletionWithTasks(context.Background(), ms, nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := covMissionStatus(t, db, "m1"); got != "IN_PROGRESS" {
			t.Errorf("status = %q, want IN_PROGRESS (untouched)", got)
		}
	})

	t.Run("assignments still running", func(t *testing.T) {
		t.Parallel()
		db := covMissionDB(t)
		covSeed(t, db)
		ms := covMission(t, db, "m1", "IN_PROGRESS")
		ms.planningDispatched = true
		now := time.Now().UTC().Format(time.RFC3339)
		mustExec(t, db, `INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, group_id, created_at)
			VALUES ('a1', 'ws-1', 'm1', 'agent-lead', 'agent-worker', 'work', 'RUNNING', 'm1', ?)`, now)
		e := newLifecycleEngine(t, db)
		if err := e.checkMissionCompletionWithTasks(context.Background(), ms, nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := covMissionStatus(t, db, "m1"); got != "IN_PROGRESS" {
			t.Errorf("status = %q, want IN_PROGRESS while assignments run", got)
		}
	})

	t.Run("no assignments at all", func(t *testing.T) {
		t.Parallel()
		db := covMissionDB(t)
		covSeed(t, db)
		ms := covMission(t, db, "m1", "IN_PROGRESS")
		ms.planningDispatched = true
		e := newLifecycleEngine(t, db)
		if err := e.checkMissionCompletionWithTasks(context.Background(), ms, nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := covMissionStatus(t, db, "m1"); got != "IN_PROGRESS" {
			t.Errorf("status = %q, want IN_PROGRESS while waiting for assignments", got)
		}
	})

	t.Run("all assignments done moves to REVIEW with inbox item", func(t *testing.T) {
		t.Parallel()
		db := covMissionDB(t)
		covSeed(t, db)
		ms := covMission(t, db, "m1", "IN_PROGRESS")
		ms.planningDispatched = true
		mustExec(t, db, `UPDATE missions SET mission_type = 'issue', identifier = 'CRE-42' WHERE id = 'm1'`)
		now := time.Now().UTC().Format(time.RFC3339)
		mustExec(t, db, `INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, group_id, created_at)
			VALUES ('a1', 'ws-1', 'm1', 'agent-lead', 'agent-worker', 'work', 'COMPLETED', 'm1', ?)`, now)
		e := newLifecycleEngine(t, db)
		if err := e.checkMissionCompletionWithTasks(context.Background(), ms, nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := covMissionStatus(t, db, "m1"); got != "REVIEW" {
			t.Errorf("status = %q, want REVIEW", got)
		}
		var title string
		if err := db.QueryRow(`SELECT title FROM inbox_items WHERE id = 'ibx_message_issue_review_m1'`).Scan(&title); err != nil {
			t.Fatalf("expected inbox item for issue review: %v", err)
		}
		if title != "CRE-42 ready for review" {
			t.Errorf("inbox title = %q", title)
		}
	})

	t.Run("all assignments FAILED marks mission FAILED, not REVIEW", func(t *testing.T) {
		t.Parallel()
		db := covMissionDB(t)
		covSeed(t, db)
		ms := covMission(t, db, "m1", "IN_PROGRESS")
		ms.planningDispatched = true
		mustExec(t, db, `UPDATE missions SET mission_type = 'issue', identifier = 'CRE-43' WHERE id = 'm1'`)
		now := time.Now().UTC().Format(time.RFC3339)
		// The lead-planning assignment failed (e.g. crew couldn't be
		// provisioned) and produced no mission_tasks. The mission must surface
		// as FAILED, not silently move to REVIEW.
		mustExec(t, db, `INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, group_id, created_at)
			VALUES ('a1', 'ws-1', 'm1', 'agent-lead', 'agent-lead', '[PLANNING] x', 'FAILED', 'm1', ?)`, now)
		e := newLifecycleEngine(t, db)
		if err := e.checkMissionCompletionWithTasks(context.Background(), ms, nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := covMissionStatus(t, db, "m1"); got != "FAILED" {
			t.Errorf("status = %q, want FAILED when the only assignment failed", got)
		}
		// No "ready for review" inbox item should be created for a failed mission.
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE id = 'ibx_message_issue_review_m1'`).Scan(&n)
		if n != 0 {
			t.Errorf("review inbox item created for a FAILED mission (count=%d)", n)
		}
	})

	t.Run("partial success (one completed) still moves to REVIEW", func(t *testing.T) {
		t.Parallel()
		db := covMissionDB(t)
		covSeed(t, db)
		ms := covMission(t, db, "m1", "IN_PROGRESS")
		ms.planningDispatched = true
		now := time.Now().UTC().Format(time.RFC3339)
		mustExec(t, db, `INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, group_id, created_at)
			VALUES ('a1', 'ws-1', 'm1', 'agent-lead', 'agent-worker', 'work', 'COMPLETED', 'm1', ?),
			       ('a2', 'ws-1', 'm1', 'agent-lead', 'agent-worker', 'work', 'FAILED', 'm1', ?)`, now, now)
		e := newLifecycleEngine(t, db)
		if err := e.checkMissionCompletionWithTasks(context.Background(), ms, nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := covMissionStatus(t, db, "m1"); got != "REVIEW" {
			t.Errorf("status = %q, want REVIEW when at least one assignment succeeded", got)
		}
	})
}

func TestCheckMissionCompletionWithTasks_AnyFailedMarksMissionFailed(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	ms := covMission(t, db, "m1", "IN_PROGRESS")
	e := newLifecycleEngine(t, db)
	tasks := []TaskInfo{
		{ID: "t1", Status: "COMPLETED"},
		{ID: "t2", Status: "FAILED"},
		{ID: "t3", Status: "SKIPPED"},
	}
	if err := e.checkMissionCompletionWithTasks(context.Background(), ms, tasks); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := covMissionStatus(t, db, "m1"); got != "FAILED" {
		t.Errorf("status = %q, want FAILED", got)
	}
}

func TestCheckMissionCompletion_WrapperAndReviewNotification(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	ms := covMission(t, db, "m1", "IN_PROGRESS")
	mustExec(t, db, `UPDATE missions SET mission_type = 'issue', identifier = 'CRE-7' WHERE id = 'm1'`)
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t1', 'm1', 'agent-worker', 'Done', 'COMPLETED', 1, '[]', ?, ?)`, now, now)

	e := newLifecycleEngine(t, db)
	if err := e.checkMissionCompletion(context.Background(), ms); err != nil {
		t.Fatalf("checkMissionCompletion: %v", err)
	}
	if got := covMissionStatus(t, db, "m1"); got != "REVIEW" {
		t.Errorf("status = %q, want REVIEW", got)
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE source_id = 'issue_review_m1'`).Scan(&count)
	if count != 1 {
		t.Errorf("REVIEW via the all-terminal path must also fire the inbox notification, got %d rows", count)
	}
}

func TestCheckMissionCompletion_LoadTasksError(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	ms := covMission(t, db, "m1", "IN_PROGRESS")
	e := newLifecycleEngine(t, db)
	db.Close()
	if err := e.checkMissionCompletion(context.Background(), ms); err == nil {
		t.Fatal("expected error from closed DB")
	}
}

// ---- fireIssueReviewInboxNotification ----

func TestFireIssueReviewInboxNotification_Branches(t *testing.T) {
	t.Parallel()

	t.Run("mission lookup failure is swallowed", func(t *testing.T) {
		t.Parallel()
		db := covMissionDB(t)
		e := newLifecycleEngine(t, db)
		e.fireIssueReviewInboxNotification(context.Background(), &missionState{ID: "ghost", WorkspaceID: "ws-1"})
		var count int
		db.QueryRow(`SELECT COUNT(*) FROM inbox_items`).Scan(&count)
		if count != 0 {
			t.Errorf("no inbox rows expected, got %d", count)
		}
	})

	t.Run("non-issue mission skipped", func(t *testing.T) {
		t.Parallel()
		db := covMissionDB(t)
		covSeed(t, db)
		ms := covMission(t, db, "m1", "REVIEW")
		mustExec(t, db, `UPDATE missions SET mission_type = 'standard', identifier = 'X-1' WHERE id = 'm1'`)
		e := newLifecycleEngine(t, db)
		e.fireIssueReviewInboxNotification(context.Background(), ms)
		var count int
		db.QueryRow(`SELECT COUNT(*) FROM inbox_items`).Scan(&count)
		if count != 0 {
			t.Errorf("non-issue mission must not create inbox rows, got %d", count)
		}
	})

	t.Run("issue without identifier skipped", func(t *testing.T) {
		t.Parallel()
		db := covMissionDB(t)
		covSeed(t, db)
		ms := covMission(t, db, "m1", "REVIEW")
		mustExec(t, db, `UPDATE missions SET mission_type = 'issue' WHERE id = 'm1'`)
		e := newLifecycleEngine(t, db)
		e.fireIssueReviewInboxNotification(context.Background(), ms)
		var count int
		db.QueryRow(`SELECT COUNT(*) FROM inbox_items`).Scan(&count)
		if count != 0 {
			t.Errorf("issue without identifier must not create inbox rows, got %d", count)
		}
	})

	t.Run("issue with identifier creates row", func(t *testing.T) {
		t.Parallel()
		db := covMissionDB(t)
		covSeed(t, db)
		ms := covMission(t, db, "m1", "REVIEW")
		mustExec(t, db, `UPDATE missions SET mission_type = 'issue', identifier = 'CRE-99' WHERE id = 'm1'`)
		e := newLifecycleEngine(t, db)
		e.fireIssueReviewInboxNotification(context.Background(), ms)
		var title, body, role string
		if err := db.QueryRow(`SELECT title, body_md, target_role FROM inbox_items WHERE source_id = 'issue_review_m1'`).
			Scan(&title, &body, &role); err != nil {
			t.Fatalf("inbox row missing: %v", err)
		}
		if title != "CRE-99 ready for review" || body != "Cov Mission" || role != "MANAGER" {
			t.Errorf("inbox row wrong: title=%q body=%q role=%q", title, body, role)
		}
	})
}

// ---- failDependentTasks cascade ----

func TestFailDependentTasks_CascadesThroughChainOnly(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	ms := covMission(t, db, "m1", "IN_PROGRESS")
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t1', 'm1', 'agent-worker', 'Root', 'FAILED', 1, '[]', ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t2', 'm1', 'agent-worker', 'Child', 'BLOCKED', 2, '["t1"]', ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t3', 'm1', 'agent-worker', 'Grandchild', 'BLOCKED', 3, '["t2"]', ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t4', 'm1', 'agent-worker', 'Unrelated', 'BLOCKED', 4, '["other"]', ?, ?)`, now, now)

	e := newLifecycleEngine(t, db)
	e.mu.Lock()
	e.active["m1"] = ms
	e.mu.Unlock()

	e.failDependentTasks(context.Background(), "m1", "t1", "upstream task rejected")

	for _, id := range []string{"t2", "t3"} {
		if got := covTaskStatus(t, db, id); got != "FAILED" {
			t.Errorf("task %s = %q, want FAILED (cascade)", id, got)
		}
		var msg string
		db.QueryRow(`SELECT error_message FROM mission_tasks WHERE id = ?`, id).Scan(&msg)
		if msg != "upstream task rejected" {
			t.Errorf("task %s error_message = %q", id, msg)
		}
	}
	if got := covTaskStatus(t, db, "t4"); got != "BLOCKED" {
		t.Errorf("unrelated task = %q, must stay BLOCKED", got)
	}
}
