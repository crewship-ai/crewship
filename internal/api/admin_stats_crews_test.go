package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The admin overview's capacity block reads crews against the licensed
// ceiling ("3 of 15"), and the stats endpoint counted everything except
// crews — the one number the license actually caps first.
func TestAdminStats_CountsCrews(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-s1", wsID, "One", "one")
	gone := seedCrewRow(t, db, "crew-s2", wsID, "Two", "two")
	if _, err := db.Exec(`UPDATE crews SET deleted_at = datetime('now') WHERE id = ?`, gone); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	h := NewAdminHandler(db, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/admin/stats", nil)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Stats(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}

	var out struct {
		Crews int `json:"crews"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// A deleted crew no longer occupies a licensed slot, so it must not be
	// counted against the ceiling.
	if out.Crews != 1 {
		t.Errorf("crews = %d, want 1 (the soft-deleted one does not count)", out.Crews)
	}
}
