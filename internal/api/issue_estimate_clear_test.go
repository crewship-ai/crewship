package api

import (
	"database/sql"
	"net/http"
	"testing"
)

// Clearing an estimate had no wire representation at all.
//
// `estimate` decoded into a *int, so a JSON null landed as nil — which is
// exactly what an OMITTED field lands as. "Clear estimate" therefore reached
// the handler as an empty patch, fell through to "No fields to update", and
// came back 400: a red toast on a control that looked like it worked.
//
// Found while promoting the issue card to the production detail, where the
// estimate picker had to keep its clear arm. The picker is the only caller
// that can produce this body, which is why nothing caught it before.
func TestIssueUpdate_EstimateNullClearsIt(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	h := NewIssueHandler(db, nil, nil, newTestLogger())
	seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	execOrFatal(t, db, `UPDATE missions SET estimate = 5 WHERE identifier = 'ENG-1'`)

	rr := covIHUPatch(h, userID, wsID, crewID, "ENG-1", map[string]any{"estimate": nil})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var got sql.NullInt64
	if err := db.QueryRow(`SELECT estimate FROM missions WHERE identifier = 'ENG-1'`).Scan(&got); err != nil {
		t.Fatalf("read estimate: %v", err)
	}
	if got.Valid {
		t.Errorf("estimate = %d, want NULL", got.Int64)
	}
}

// The ordinary path must keep working — a number still sets the column.
func TestIssueUpdate_EstimateNumberStillSets(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	h := NewIssueHandler(db, nil, nil, newTestLogger())
	seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	rr := covIHUPatch(h, userID, wsID, crewID, "ENG-1", map[string]any{"estimate": 8})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var got sql.NullInt64
	if err := db.QueryRow(`SELECT estimate FROM missions WHERE identifier = 'ENG-1'`).Scan(&got); err != nil {
		t.Fatalf("read estimate: %v", err)
	}
	if !got.Valid || got.Int64 != 8 {
		t.Errorf("estimate = %v, want 8", got)
	}
}

// Reading the field as raw JSON gives up the type check the decoder used to
// do for free, so the handler has to do it — an agent writes this body itself
// and "eight" is the shape it will send.
func TestIssueUpdate_EstimateRejectsNonNumber(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	h := NewIssueHandler(db, nil, nil, newTestLogger())
	seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	rr := covIHUPatch(h, userID, wsID, crewID, "ENG-1", map[string]any{"estimate": "eight"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}
