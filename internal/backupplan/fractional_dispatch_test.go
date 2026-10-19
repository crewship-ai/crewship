package backupplan

import (
	"context"
	"sync"
	"testing"
	"time"
)

// The first read is manual admission; all later reads are dispatch/execution.
// Both instants are real chronological values, without a wall-clock sleep.
type fractionalDispatchClock struct {
	mu           sync.Mutex
	first, later time.Time
	reads        int
}

func (c *fractionalDispatchClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads++
	if c.reads == 1 {
		return c.first
	}
	return c.later
}

func TestManualBackupDispatchOrdersFractionalDueTimes(t *testing.T) {
	for _, tc := range []struct{ name, dispatch string }{
		{"equal_time", "2026-09-30T12:00:00.1Z"},
		{"later_nanosecond", "2026-09-30T12:00:00.100000001Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "2026-09-30T12:00:00.1Z")
			admitted := h.clock.Now()
			dispatched := mustTime(t, tc.dispatch)
			if dispatched.Before(admitted) {
				t.Fatal("SETUP: dispatch precedes admission")
			}
			h.svc.Clock = &fractionalDispatchClock{first: admitted, later: dispatched}
			ids, err := h.svc.StartManual(context.Background(), ManualRequest{
				Scope: ScopeWorkspaces, WorkspaceIDs: []string{"ws_a"}, RecipientIDs: []string{h.key},
			}, "u1")
			if err != nil || len(ids) != 1 {
				t.Fatalf("SETUP: admission ids=%v err=%v", ids, err)
			}
			h.svc.Wait()
			r, err := GetRun(context.Background(), h.db, ids[0])
			if err != nil {
				t.Fatal(err)
			}
			if !r.StartedAt.Equal(admitted) || r.Trigger != TriggerManual || r.WorkspaceID != "ws_a" {
				t.Fatalf("SETUP: unexpected admitted run: %+v", r)
			}
			if r.Status != StatusDone || len(h.exec.calls()) != 1 {
				t.Errorf("due manual backup was not executed: status=%s phase=%s calls=%d", r.Status, r.Phase, len(h.exec.calls()))
			}
		})
	}
}

func TestManualBackupDispatchDoesNotStartFutureFractionalRetry(t *testing.T) {
	h := newHarness(t, "2026-09-30T12:00:00.100000001Z")
	h.busy.set(true)
	ids, err := h.svc.StartManual(context.Background(), ManualRequest{
		Scope: ScopeWorkspaces, WorkspaceIDs: []string{"ws_a"}, RecipientIDs: []string{h.key},
	}, "u1")
	if err != nil || len(ids) != 1 {
		t.Fatalf("SETUP: admission ids=%v err=%v", ids, err)
	}
	h.svc.Wait()
	r, err := GetRun(context.Background(), h.db, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusRunning || r.Phase != PhaseBusyWait || r.NextAttemptAt == nil || len(h.exec.calls()) != 0 {
		t.Fatalf("SETUP: native busy retry not parked: %+v calls=%d", r, len(h.exec.calls()))
	}
	due := *r.NextAttemptAt
	if !due.Equal(h.clock.Now().Add(15 * time.Minute)) {
		t.Fatalf("SETUP: unexpected retry time %s", due)
	}
	// Advance monotonically to one nanosecond before the native retry is due.
	h.clock.Set(due.Add(-time.Nanosecond))
	h.busy.set(false)
	h.svc.Kick()
	h.svc.Wait()
	r, err = GetRun(context.Background(), h.db, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusRunning || r.Phase != PhaseBusyWait || len(h.exec.calls()) != 0 {
		t.Errorf("future retry executed before its deadline: status=%s phase=%s calls=%d", r.Status, r.Phase, len(h.exec.calls()))
	}
	// The same real run must become eligible at equality, without another admission.
	h.clock.Set(due)
	h.svc.Kick()
	h.svc.Wait()
	r, err = GetRun(context.Background(), h.db, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusDone || len(h.exec.calls()) != 1 {
		t.Errorf("retry did not complete at its deadline: status=%s phase=%s calls=%d", r.Status, r.Phase, len(h.exec.calls()))
	}
}
