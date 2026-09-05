package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/missionactivity"
)

// TestInternalIssueUpdateStatus_EmitsJournalEntry is the red-first test for
// #1768 F1.
//
// Notifications are routed PER JOURNAL ENTRY TYPE (internal/notifyroute's
// journalCategories bridge maps mission.status_change → issues.state, and so
// on). Before this change only IssueHandler — the human path — emitted a
// journal entry; InternalIssueHandler, which is every mutation an AGENT makes
// through the sidecar, wrote the mission_activity row and stopped there. The
// consequence was not "a missing row in a table": it was that no action an
// agent took on the issue board could ever produce a notification.
//
// The assertion is deliberately on the JOURNAL, not on mission_activity —
// the activity row was already being written, so asserting on it would pass
// against the bug.
func TestInternalIssueUpdateStatus_EmitsJournalEntry(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	rec := &recordingEmitter{}
	h.SetJournal(rec)

	body := bytes.NewBufferString(
		`{"workspace_id":"` + wsID + `","status":"TODO","agent_id":"agent-worker"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	// The mutation landed — that part was never broken.
	var activityCount int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM mission_activity WHERE action = ?`,
		string(actionStatusChanged)).Scan(&activityCount); err != nil {
		t.Fatalf("count activity: %v", err)
	}
	if activityCount != 1 {
		t.Fatalf("mission_activity rows = %d, want 1", activityCount)
	}

	// …and the journal entry that the notification router actually reads.
	if len(rec.entries) != 1 {
		t.Fatalf("journal entries = %d, want 1 (agent status change must be notifiable)", len(rec.entries))
	}
	e := rec.entries[0]
	if e.Type != journal.EntryMissionStatus {
		t.Errorf("entry type = %q, want %q", e.Type, journal.EntryMissionStatus)
	}
	if e.ActorType != journal.ActorAgent {
		t.Errorf("actor type = %q, want %q", e.ActorType, journal.ActorAgent)
	}
	if e.WorkspaceID != wsID {
		t.Errorf("workspace = %q, want %q", e.WorkspaceID, wsID)
	}
	if e.CrewID != crewID {
		t.Errorf("crew = %q, want %q", e.CrewID, crewID)
	}
}

// TestInternalIssueSetJournal_NilFallsBackToNoop mirrors the contract every
// other SetJournal in this package is held to: nil must collapse to the noop
// emitter, never be stored as a nil interface that panics on first Emit.
func TestInternalIssueSetJournal_NilFallsBackToNoop(t *testing.T) {
	h, _, _, _, _ := newInternalIssueHandler(t)
	h.SetJournal(&recordingEmitter{})
	h.SetJournal(nil)
	if _, ok := h.journal.(noopEmitter); !ok {
		t.Fatalf("journal = %T, want noopEmitter after SetJournal(nil)", h.journal)
	}
}

// TestCodeLinkSetJournal_NilFallsBackToNoop — same contract, other handler.
func TestCodeLinkSetJournal_NilFallsBackToNoop(t *testing.T) {
	h := NewCodeLinkHandler(setupTestDB(t), nil, newTestLogger())
	h.SetJournal(&recordingEmitter{})
	h.SetJournal(nil)
	if _, ok := h.journal.(noopEmitter); !ok {
		t.Fatalf("journal = %T, want noopEmitter after SetJournal(nil)", h.journal)
	}
}

// TestIssueEvents_NilJournalAndHub proves the emitter is safe for the
// handlers tests construct by hand — those pass a nil *ws.Hub and, on the
// struct-literal path, can leave the emitter's journal a nil interface. The
// mission_activity row must still land.
func TestIssueEvents_NilJournalAndHub(t *testing.T) {
	db := setupTestDB(t)
	_, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	id := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	e := issueEvents{db: db, hub: nil, logger: newTestLogger(), journal: nil}
	e.record(context.Background(), wsID, map[string]string{"id": id}, issueEvent{
		MissionID: id, ActorType: "system", ActorID: "system",
		Action: actionStatusChanged, Details: "BACKLOG → TODO",
	})

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mission_activity WHERE mission_id = ?`, id).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("activity rows = %d, want 1", n)
	}
}

// TestLogEvent_ReturnsNoIDWhenTheActivityWriteFails is the red-first test
// for a review finding on #2338 (B2, PRD-ISSUES-AND-ROUTINES-2026 §9.3):
// logEvent generated the activity row's id BEFORE calling missionactivity.Emit
// and returned that id even when Emit failed and rolled back nothing was
// ever written. Since B2 mission_comment_mentions.event_id is a real foreign
// key into mission_activity(id), a caller (mentionRecorder.record) that
// stored the phantom id would point a delivery row at a mission_activity row
// that does not exist — createDelivery's own INSERT would then fail its FK
// too, and a mention would dispatch a run while leaving NO trace anywhere,
// worse than the pre-B2 behaviour it replaced.
//
// Forces Emit to fail with an invalid `action` — mission_activity's CHECK
// constraint (20260904095700_mission_activity_widen.sql) rejects anything
// outside its enumerated vocabulary, which is a reliable, deterministic way
// to make Emit roll back without needing a lower-level fault injection.
func TestLogEvent_ReturnsNoIDWhenTheActivityWriteFails(t *testing.T) {
	db := setupTestDB(t)
	_, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	id := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	e := issueEvents{db: db, hub: nil, logger: newTestLogger(), journal: nil}
	gotID, written := e.logEvent(context.Background(), issueEvent{
		MissionID: id, ActorType: "user", ActorID: "user-1",
		Action: issueAction("not_a_real_action"), Details: "x",
	})
	if gotID != "" {
		t.Errorf("logEvent id = %q, want empty — the activity row was never written", gotID)
	}
	if written != (missionactivity.Written{}) {
		t.Errorf("logEvent Written = %+v, want the zero value", written)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mission_activity WHERE mission_id = ?`, id).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("activity rows = %d, want 0 — the CHECK constraint should have rejected the insert", n)
	}
}

// TestIssueEvents_ZeroEventsStillBroadcasts pins the reason record takes a
// variadic: a label-only or comment-only PATCH audits nothing but every open
// board still has to redraw. If this ever regressed to "no events, no
// broadcast", the boards would go stale on exactly the edits that are
// hardest to notice are missing.
func TestIssueEvents_ZeroEventsStillBroadcasts(t *testing.T) {
	db := setupTestDB(t)
	_, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	id := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	rec := &recordingEmitter{}
	e := issueEvents{db: db, hub: nil, logger: newTestLogger(), journal: rec}
	e.record(context.Background(), wsID, map[string]string{"id": id})

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mission_activity WHERE mission_id = ?`, id).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("activity rows = %d, want 0", n)
	}
	if len(rec.entries) != 0 {
		t.Errorf("journal entries = %d, want 0", len(rec.entries))
	}
}

// TestJournalTypeForIssueAction covers the whole enumeration. The point is
// not the switch — it is that every kind in knownIssueActions resolves to a
// type internal/notifyroute can route, so a new action can never be added
// that is audited but silently unnotifiable.
func TestJournalTypeForIssueAction(t *testing.T) {
	want := map[issueAction]journal.EntryType{
		actionCreated:                journal.EntryMissionCreated,
		actionAssigneeChanged:        journal.EntryMissionAssigned,
		actionCommented:              journal.EntryMissionComment,
		actionMentioned:              journal.EntryAgentMentioned,
		actionStatusChanged:          journal.EntryMissionStatus,
		actionPriorityChanged:        journal.EntryMissionStatus,
		actionParentChanged:          journal.EntryMissionStatus,
		actionRelationAdded:          journal.EntryMissionStatus,
		actionReviewApproved:         journal.EntryMissionStatus,
		actionReviewChangesRequested: journal.EntryMissionStatus,
		actionTaskCompleted:          journal.EntryMissionStatus,
		actionTaskFailed:             journal.EntryMissionStatus,
		actionTaskCancelled:          journal.EntryMissionStatus,
		actionDescriptionChanged:     journal.EntryMissionStatus,
		actionAttachmentAdded:        journal.EntryMissionStatus,
		actionAttachmentRemoved:      journal.EntryMissionStatus,
		actionCodeLinkAdded:          journal.EntryMissionStatus,
		actionCodeLinkRemoved:        journal.EntryMissionStatus,
		actionInboxActed:             journal.EntryMissionStatus,
	}
	for _, a := range knownIssueActions {
		exp, ok := want[a]
		if !ok {
			t.Errorf("action %q is in knownIssueActions but this test has no expectation for it — decide its journal type deliberately", a)
			continue
		}
		if got := journalTypeForIssueAction(a); got != exp {
			t.Errorf("journalTypeForIssueAction(%q) = %q, want %q", a, got, exp)
		}
	}
	if len(want) != len(knownIssueActions) {
		t.Errorf("expectations = %d, knownIssueActions = %d — the two lists have drifted", len(want), len(knownIssueActions))
	}
}

// TestDescribeDescriptionChange pins the details payload: counts, never
// content. A regression to "log the text" would show up here as a failing
// substring assertion, not as a quiet privacy/size problem in production.
func TestDescribeDescriptionChange(t *testing.T) {
	secret := "the quick brown fox"
	tests := []struct {
		name     string
		old, new string
		want     string
	}{
		{"set", "", secret, "description set (19 chars)"},
		{"cleared", secret, "", "description cleared (was 19 chars)"},
		{"updated", "ab", secret, "description updated (2 → 19 chars)"},
		// Runes, not bytes — a description of emoji or CJK must not report a
		// length three times what the author sees.
		{"counts runes not bytes", "", "žluťoučký", "description set (9 chars)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := describeDescriptionChange(tc.old, tc.new)
			if got != tc.want {
				t.Errorf("= %q, want %q", got, tc.want)
			}
			if strings.Contains(got, secret) {
				t.Errorf("details leaked description content: %q", got)
			}
		})
	}
}

// TestIssueUpdate_DescriptionChangedActivity is the Step 4 pair: a real
// description edit is audited, and a PATCH that resends the SAME description
// is not.
//
// The second half is the one that matters. `Update` accepts a description
// and writes it, but before this change nothing logged it — and the naive fix
// (log whenever the field is present) turns every "save the whole issue"
// round-trip from the detail panel into a bogus audit row.
func TestIssueUpdate_DescriptionChangedActivity(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	rec := &recordingEmitter{}
	h.SetJournal(rec)
	id := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	patch := func(t *testing.T, body string) {
		t.Helper()
		req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(body))
		req.SetPathValue("crewId", crewID)
		req.SetPathValue("identifier", "ENG-1")
		req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
		rr := httptest.NewRecorder()
		h.Update(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
	}

	descriptionRows := func(t *testing.T) []string {
		t.Helper()
		rows, err := h.db.Query(
			`SELECT details FROM mission_activity WHERE mission_id = ? AND action = ? ORDER BY id`,
			id, string(actionDescriptionChanged))
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var d string
			if err := rows.Scan(&d); err != nil {
				t.Fatalf("scan: %v", err)
			}
			out = append(out, d)
		}
		return out
	}

	// 1. A real change is audited.
	patch(t, `{"description":"a new statement of the work"}`)
	got := descriptionRows(t)
	if len(got) != 1 {
		t.Fatalf("description_changed rows = %d, want 1", len(got))
	}
	if got[0] != "description set (27 chars)" {
		t.Errorf("details = %q, want %q", got[0], "description set (27 chars)")
	}
	if strings.Contains(got[0], "statement of the work") {
		t.Errorf("details leaked the description text: %q", got[0])
	}

	// 2. Re-sending the SAME description is not a change and must not log.
	patch(t, `{"description":"a new statement of the work"}`)
	if got := descriptionRows(t); len(got) != 1 {
		t.Fatalf("description_changed rows after a no-op PATCH = %d, want 1", len(got))
	}

	// 3. …and the change that WAS real reached the journal, so it can notify.
	var journalled int
	for _, e := range rec.entries {
		if e.Payload["action"] == string(actionDescriptionChanged) {
			journalled++
		}
	}
	if journalled != 1 {
		t.Errorf("description_changed journal entries = %d, want 1", journalled)
	}
}

// TestCodeLinkDelete_EmitsJournalEntry covers the third converged writer.
// Attach needs a live forge and a stored credential; Delete needs neither, so
// it is the honest unit-level proof that CodeLinkHandler now journals.
func TestCodeLinkDelete_EmitsJournalEntry(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	missionID := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	linkID := generateCUID()
	if _, err := db.Exec(`
		INSERT INTO mission_code_links
			(id, workspace_id, mission_id, provider, host, owner, repo, number, kind, url, created_at, updated_at)
		VALUES (?, ?, ?, 'github', 'github.com', 'crewship-ai', 'crewship', 1, 'pull',
			'https://github.com/crewship-ai/crewship/pull/1', datetime('now'), datetime('now'))`,
		linkID, wsID, missionID); err != nil {
		t.Fatalf("seed code link: %v", err)
	}

	h := NewCodeLinkHandler(db, nil, newTestLogger())
	rec := &recordingEmitter{}
	h.SetJournal(rec)

	req := httptest.NewRequest("DELETE", "/", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", "ENG-1")
	req.SetPathValue("linkId", linkID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	if len(rec.entries) != 1 {
		t.Fatalf("journal entries = %d, want 1", len(rec.entries))
	}
	if got := rec.entries[0].Payload["action"]; got != string(actionCodeLinkRemoved) {
		t.Errorf("journalled action = %v, want %q", got, actionCodeLinkRemoved)
	}
	if rec.entries[0].ActorType != journal.ActorUser {
		t.Errorf("actor = %q, want %q", rec.entries[0].ActorType, journal.ActorUser)
	}
}
