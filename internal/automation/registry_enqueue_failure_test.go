package automation

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
)

// PRD-ISSUES-AND-ROUTINES-2026.md §17 A4 ("Trigger failure is visible for
// all three trigger kinds") / F20: an automation enqueue failure — a rule
// matched, coalesced, and Flush's Enqueue call returned an error, so the
// run the rule decided to make genuinely never exists — used to produce a
// bare `logger.Error` and nothing durable, while the SAME file already
// emits journal entries for depth-exceeded and throttle. These tests pin
// the fixed contract:
//
//   - EVERY enqueue failure emits journal.EntryAutomationEnqueueFailed
//     (durable, queryable "should have run, didn't").
//   - Only once the SAME automation's consecutive-failure streak reaches
//     automationEnqueueFailureAlertThreshold (3) does exactly ONE MANAGER
//     inbox card get raised — further failures past the threshold do not
//     pile up more cards (A4 point d: no inbox noise from a repeat
//     failure), and a later SUCCESS resets the streak so a future run of
//     failures can alert again.

// recordingInboxAlerter captures every inbox.Item Insert receives, for
// assertions without a database.
type recordingInboxAlerter struct {
	mu    sync.Mutex
	items []inbox.Item
}

func (a *recordingInboxAlerter) Insert(_ context.Context, item inbox.Item) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.items = append(a.items, item)
	return nil
}

func (a *recordingInboxAlerter) n() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.items)
}

// flushOneFailure drives one Observer+Flush cycle for a distinct mission so
// the debounce key differs and each call produces its OWN pending intent
// (see debounceKey) rather than coalescing into a single one — mirrors how
// the schedule circuit-breaker test fires 3 DISTINCT occurrences rather
// than one occurrence three times.
func flushOneFailure(t *testing.T, reg *Registry, mission string) {
	t.Helper()
	reg.Observer([]journal.Entry{entry("ws_1", "mission.status_change", mission)})
	reg.Flush(context.Background())
}

// TestFlush_EnqueueFailure_JournalsEveryFailure_AlertsOnRepetition is the
// RED-on-main proof for A4's automation half: on current main, Flush's
// enqueue-error branch is a bare `r.logger.Error(...)` with no journal.Emit
// and no inbox write — journal.EntryAutomationEnqueueFailed and
// inbox.KindAutomationEnqueueFailed do not exist on main at all, so this
// test does not even COMPILE against pre-change code. Against the fix, it
// must pass.
func TestFlush_EnqueueFailure_JournalsEveryFailure_AlertsOnRepetition(t *testing.T) {
	enq := &recordingEnqueuer{err: errors.New("db is locked")}
	j := &recordingJournal{}
	alerter := &recordingInboxAlerter{}
	reg := NewRegistry(nil, enq, Options{Journal: j})
	reg.SetInboxAlerter(alerter)
	reg.Load([]Resolved{rule("a1", "ws_1", "mission.status_change")})

	// Failures 1 and 2: journaled, no alert yet (streak < threshold 3).
	flushOneFailure(t, reg, missionID(1))
	if n := j.count(journal.EntryAutomationEnqueueFailed); n != 1 {
		t.Fatalf("after failure 1: journal entries = %d, want 1", n)
	}
	if n := alerter.n(); n != 0 {
		t.Fatalf("after failure 1: inbox items = %d, want 0 (single failure must not page anyone)", n)
	}

	flushOneFailure(t, reg, missionID(2))
	if n := j.count(journal.EntryAutomationEnqueueFailed); n != 2 {
		t.Fatalf("after failure 2: journal entries = %d, want 2", n)
	}
	if n := alerter.n(); n != 0 {
		t.Fatalf("after failure 2: inbox items = %d, want 0", n)
	}

	// Failure 3 crosses the threshold — exactly one inbox card raised.
	flushOneFailure(t, reg, missionID(3))
	if n := j.count(journal.EntryAutomationEnqueueFailed); n != 3 {
		t.Fatalf("after failure 3: journal entries = %d, want 3", n)
	}
	if n := alerter.n(); n != 1 {
		t.Fatalf("after failure 3 (crossed threshold): inbox items = %d, want exactly 1", n)
	}

	// Failures 4 and 5: journal keeps growing (durable per-attempt
	// record), but the inbox is NOT re-alerted.
	flushOneFailure(t, reg, missionID(4))
	flushOneFailure(t, reg, missionID(5))
	if n := j.count(journal.EntryAutomationEnqueueFailed); n != 5 {
		t.Fatalf("after failure 5: journal entries = %d, want 5", n)
	}
	if n := alerter.n(); n != 1 {
		t.Fatalf("after failure 5: inbox items = %d, want exactly 1 (repetition past threshold must not pile up cards)", n)
	}

	item := alerter.items[0]
	if item.Kind != inbox.KindAutomationEnqueueFailed {
		t.Errorf("kind = %q, want %q", item.Kind, inbox.KindAutomationEnqueueFailed)
	}
	if item.SourceID != "a1" {
		t.Errorf("source_id = %q, want automation id a1", item.SourceID)
	}
	if item.TargetRole != "MANAGER" {
		t.Errorf("target_role = %q, want MANAGER", item.TargetRole)
	}
	if item.Priority != "high" {
		t.Errorf("priority = %q, want high", item.Priority)
	}
}

// TestFlush_EnqueueFailure_SuccessResetsStreak_SoALaterStreakAlertsAgain
// pins the reset half of the contract: a successful enqueue clears the
// streak, so a FRESH run of failures can cross the threshold and alert a
// second time — the same "COMPLETED resets consecutive_failures" shape
// pipeline_schedules uses (recordRun).
func TestFlush_EnqueueFailure_SuccessResetsStreak_SoALaterStreakAlertsAgain(t *testing.T) {
	failing := &recordingEnqueuer{err: errors.New("db is locked")}
	j := &recordingJournal{}
	alerter := &recordingInboxAlerter{}
	reg := NewRegistry(nil, failing, Options{Journal: j})
	reg.SetInboxAlerter(alerter)
	reg.Load([]Resolved{rule("a1", "ws_1", "mission.status_change")})

	flushOneFailure(t, reg, missionID(1))
	flushOneFailure(t, reg, missionID(2))
	flushOneFailure(t, reg, missionID(3))
	if n := alerter.n(); n != 1 {
		t.Fatalf("after first streak of 3: inbox items = %d, want 1", n)
	}

	// Swap in a working enqueuer for one success (registry.enq is
	// unexported, so this test lives in the same package to reach it).
	reg.enq = &recordingEnqueuer{}
	flushOneFailure(t, reg, missionID(4))
	if n := alerter.n(); n != 1 {
		t.Fatalf("after the success: inbox items = %d, want still 1 (success is not a failure)", n)
	}

	// Back to failing — a FRESH streak of 3 must alert again.
	reg.enq = failing
	flushOneFailure(t, reg, missionID(5))
	flushOneFailure(t, reg, missionID(6))
	if n := alerter.n(); n != 1 {
		t.Fatalf("after 2 of the new streak: inbox items = %d, want still 1", n)
	}
	flushOneFailure(t, reg, missionID(7))
	if n := alerter.n(); n != 2 {
		t.Fatalf("after the new streak crossed the threshold again: inbox items = %d, want 2", n)
	}
}
