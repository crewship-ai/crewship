package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
)

// --- PATCH: due_date is parsed, not just stored ----------------------------

// due_date is a TEXT column. A human uses a date picker and cannot send prose;
// an agent writes the JSON itself and will happily send "tomorrow". Every
// reader downstream parses the stored string, so garbage in is an Invalid Date
// rendered to a person.
func TestInternalIssueUpdate_DueDateValidation(t *testing.T) {
	cases := []struct {
		value string
		want  int
	}{
		{"2026-09-01", http.StatusOK},
		{"2026-09-01T10:30:00Z", http.StatusOK},
		{"tomorrow", http.StatusBadRequest},
		{"next sprint", http.StatusBadRequest},
		{"2026-13-45", http.StatusBadRequest},
		{"2026-02-30", http.StatusBadRequest},
		{"01/09/2026", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
			issueID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

			rr := httptest.NewRecorder()
			h.UpdateStatus(rr, internalPatch("ENG-1",
				`{"workspace_id":"`+wsID+`","agent_id":"agent-worker","due_date":"`+tc.value+`"}`,
				crewBoundCtx1186(wsID, crewID)))

			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, tc.want, rr.Body.String())
			}
			var stored sql.NullString
			if err := h.db.QueryRow(`SELECT due_date FROM missions WHERE id = ?`, issueID).Scan(&stored); err != nil {
				t.Fatalf("read due_date: %v", err)
			}
			if tc.want == http.StatusBadRequest && stored.Valid {
				t.Errorf("a rejected due_date must not be persisted, found %q", stored.String)
			}
			if tc.want == http.StatusOK && stored.String != tc.value {
				t.Errorf("due_date = %q, want %q", stored.String, tc.value)
			}
		})
	}
}

// An empty string still clears the column — validation must not turn "unset the
// due date" into a 400.
func TestInternalIssueUpdate_DueDateClearing(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	issueID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	if _, err := h.db.Exec(`UPDATE missions SET due_date = '2026-09-01' WHERE id = ?`, issueID); err != nil {
		t.Fatalf("seed due_date: %v", err)
	}

	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, internalPatch("ENG-1",
		`{"workspace_id":"`+wsID+`","agent_id":"agent-worker","due_date":""}`,
		crewBoundCtx1186(wsID, crewID)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var stored sql.NullString
	if err := h.db.QueryRow(`SELECT due_date FROM missions WHERE id = ?`, issueID).Scan(&stored); err != nil {
		t.Fatalf("read due_date: %v", err)
	}
	if stored.Valid {
		t.Errorf("due_date = %q, want NULL", stored.String)
	}
}

// --- PATCH: the column update and the labels land together -----------------

// A validation failure returns before any write, so it proves early rejection,
// not atomicity. To exercise the transaction the label write itself has to
// fail: the table is dropped underneath the handler, which is the closest
// stand-in for the driver/constraint error that used to be logged and answered
// with 200.
//
// Two things must hold afterwards. The request must FAIL — a partial write the
// caller records as a success is the worse outcome of the two. And the missions
// UPDATE in the same request must be rolled back, because "status changed,
// labels didn't" is a state no caller asked for.
func TestInternalIssueUpdate_LabelFailureRollsBackTheColumnWrite(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	issueID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	if _, err := h.db.Exec(`DROP TABLE mission_labels`); err != nil {
		t.Fatalf("drop mission_labels: %v", err)
	}

	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, internalPatch("ENG-1",
		`{"workspace_id":"`+wsID+`","agent_id":"agent-worker","status":"TODO","labels":["whatever"]}`,
		crewBoundCtx1186(wsID, crewID)))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 — a failed label write must not read as success; body=%s",
			rr.Code, rr.Body.String())
	}
	var status string
	if err := h.db.QueryRow(`SELECT status FROM missions WHERE id = ?`, issueID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "BACKLOG" {
		t.Errorf("status = %q, want BACKLOG — the column write must roll back with the labels", status)
	}
}

// A rejected request writes nothing at all, labels included. Cheaper property
// than the one above and the common case: validation runs before the tx opens.
func TestInternalIssueUpdate_RejectedRequestLeavesLabelsIntact(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	issueID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	bug := seedLabel(t, h.db, wsID, "bug")
	if _, err := h.db.Exec(
		`INSERT INTO mission_labels (mission_id, label_id) VALUES (?, ?)`, issueID, bug); err != nil {
		t.Fatalf("seed label link: %v", err)
	}

	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, internalPatch("ENG-1",
		`{"workspace_id":"`+wsID+`","agent_id":"agent-worker","status":"TODO","due_date":"whenever","labels":[]}`,
		crewBoundCtx1186(wsID, crewID)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}

	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM mission_labels WHERE mission_id = ?`, issueID).Scan(&n); err != nil {
		t.Fatalf("count labels: %v", err)
	}
	if n != 1 {
		t.Errorf("a rejected request must not clear labels; got %d, want 1", n)
	}
	var status string
	if err := h.db.QueryRow(`SELECT status FROM missions WHERE id = ?`, issueID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "BACKLOG" {
		t.Errorf("status = %q, want BACKLOG — the column write must not survive either", status)
	}
}
