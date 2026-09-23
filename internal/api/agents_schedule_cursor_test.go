package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type heldScheduleUpdate struct {
	entered chan struct{}
	release chan struct{}
}

func (s *heldScheduleUpdate) UpdateSchedule(context.Context, string, string, string, bool) error {
	close(s.entered)
	<-s.release
	return nil
}

func TestAgentScheduleCursor_DisableRemovesOldDueIdentity(t *testing.T) {
	h, userID, wsID, _, agentID := covAU2Fixture(t)
	execOrFatal(t, h.db, `UPDATE agents SET schedule_cron='* * * * *', schedule_enabled=1,
		schedule_next_run='2020-01-01T00:00:00Z' WHERE id=?`, agentID)
	h.SetScheduler(&covAU2Sched{})

	rr := covAU2Patch(h, userID, wsID, agentID, "OWNER", map[string]any{"schedule_enabled": false})
	if rr.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", rr.Code, rr.Body.String())
	}
	var next sql.NullString
	if err := h.db.QueryRow(`SELECT schedule_next_run FROM agents WHERE id=?`, agentID).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if next.Valid {
		t.Fatalf("disabled schedule kept due identity %q; re-enable can accept an old occurrence", next.String)
	}
}

func TestAgentScheduleCursor_EnableReplacesOldDueIdentityBeforeSchedulerCallback(t *testing.T) {
	h, userID, wsID, _, agentID := covAU2Fixture(t)
	execOrFatal(t, h.db, `UPDATE agents SET schedule_cron='* * * * *', schedule_enabled=0,
		schedule_next_run='2020-01-01T00:00:00Z' WHERE id=?`, agentID)
	// Hold the callback open after the SQL update. The overdue sweep may read
	// the enabled row at this exact boundary, so it must already have the new
	// due identity without relying on a later scheduler write.
	held := &heldScheduleUpdate{entered: make(chan struct{}), release: make(chan struct{})}
	h.SetScheduler(held)
	t.Cleanup(func() {
		select {
		case <-held.release:
		default:
			close(held.release)
		}
	})
	before := time.Now().UTC()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- covAU2Patch(h, userID, wsID, agentID, "OWNER", map[string]any{"schedule_enabled": true})
	}()
	select {
	case <-held.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("schedule callback was not reached")
	}
	var nextText string
	if err := h.db.QueryRow(`SELECT schedule_next_run FROM agents WHERE id=?`, agentID).Scan(&nextText); err != nil {
		t.Fatal(err)
	}
	next, err := time.Parse(time.RFC3339Nano, nextText)
	if err != nil || !next.After(before) {
		t.Fatalf("enabled schedule retained stale cursor %q (parse=%v), want a future occurrence", nextText, err)
	}
	close(held.release)
	select {
	case rr := <-done:
		if rr.Code != http.StatusOK {
			t.Fatalf("enable status=%d body=%s", rr.Code, rr.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("schedule update did not finish after callback was released")
	}
}

func TestAgentScheduleCursor_InvalidEnabledCronDoesNotPersist(t *testing.T) {
	h, userID, wsID, _, agentID := covAU2Fixture(t)
	h.SetScheduler(&covAU2Sched{})
	rr := covAU2Patch(h, userID, wsID, agentID, "OWNER", map[string]any{
		"schedule_cron": "not-a-cron", "schedule_enabled": true,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid cron status=%d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	var enabled int
	if err := h.db.QueryRow(`SELECT schedule_enabled FROM agents WHERE id=?`, agentID).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 0 {
		t.Fatalf("invalid cron persisted schedule_enabled=%d", enabled)
	}
}

func TestAgentScheduleCursor_InvalidCronDoesNotDemoteLead(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	crewID := seedCrewRow(t, h.db, "crew-schedule-lead", wsID, "Crew", "crew-schedule-lead")
	seedAgentRow(t, h.db, "ag-schedule-oldlead", wsID, crewID, "Old", "schedule-oldlead", "LEAD")
	seedAgentRow(t, h.db, "ag-schedule-newlead", wsID, crewID, "New", "schedule-newlead", "AGENT")
	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-schedule-newlead",
		`{"agent_role":"LEAD","schedule_enabled":true,"schedule_cron":"not-a-cron"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid cron status=%d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	var oldRole, newRole string
	if err := h.db.QueryRowContext(t.Context(), `SELECT agent_role FROM agents WHERE id='ag-schedule-oldlead'`).Scan(&oldRole); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRowContext(t.Context(), `SELECT agent_role FROM agents WHERE id='ag-schedule-newlead'`).Scan(&newRole); err != nil {
		t.Fatal(err)
	}
	if oldRole != "LEAD" || newRole != "AGENT" {
		t.Fatalf("failed PATCH changed crew leadership: old=%q new=%q", oldRole, newRole)
	}
}

func TestAgentScheduleCursor_LeadMoveDemotesDestinationCrew(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	oldCrew := seedCrewRow(t, h.db, "crew-schedule-origin", wsID, "Origin", "crew-schedule-origin")
	newCrew := seedCrewRow(t, h.db, "crew-schedule-destination", wsID, "Destination", "crew-schedule-destination")
	seedAgentRow(t, h.db, "ag-schedule-moved", wsID, oldCrew, "Moved", "schedule-moved", "AGENT")
	seedAgentRow(t, h.db, "ag-schedule-destination-lead", wsID, newCrew, "Old Lead", "schedule-destination-lead", "LEAD")
	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-schedule-moved",
		`{"crew_id":"crew-schedule-destination","agent_role":"LEAD","schedule_cron":"0 9 * * *","schedule_enabled":true}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("promotion status=%d body=%s", rr.Code, rr.Body.String())
	}
	var oldRole, newRole string
	if err := h.db.QueryRowContext(t.Context(), `SELECT agent_role FROM agents WHERE id='ag-schedule-destination-lead'`).Scan(&oldRole); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRowContext(t.Context(), `SELECT agent_role FROM agents WHERE id='ag-schedule-moved'`).Scan(&newRole); err != nil {
		t.Fatal(err)
	}
	if oldRole != "AGENT" || newRole != "LEAD" {
		t.Fatalf("destination crew leadership old=%q new=%q", oldRole, newRole)
	}
}

func TestAgentScheduleCursor_IdempotentPatchKeepsPendingOccurrence(t *testing.T) {
	h, userID, wsID, _, agentID := covAU2Fixture(t)
	const due = "2020-01-01T00:00:00Z"
	execOrFatal(t, h.db, `UPDATE agents SET schedule_cron='* * * * *', schedule_enabled=1,
		schedule_next_run=? WHERE id=?`, due, agentID)
	h.SetScheduler(&covAU2Sched{})

	rr := covAU2Patch(h, userID, wsID, agentID, "OWNER", map[string]any{"schedule_enabled": true})
	if rr.Code != http.StatusOK {
		t.Fatalf("idempotent patch status=%d body=%s", rr.Code, rr.Body.String())
	}
	var next string
	if err := h.db.QueryRow(`SELECT schedule_next_run FROM agents WHERE id=?`, agentID).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if next != due {
		t.Fatalf("no-op patch skipped pending occurrence: due=%q, want %q", next, due)
	}
}
