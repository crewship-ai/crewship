package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestUpdateProject_LeadPairValidatedWhenEitherHalfMoves closes the gap the
// lead_id fix left behind.
//
// lead_type/lead_id is a polymorphic reference: lead_id is resolved against
// `users` or `agents` depending on lead_type, so the database cannot enforce
// it and the application is the only guard. The fence work added that guard —
// but only on the branch that runs when lead_id moves. A PATCH carrying just
// lead_type was written straight through, which meant:
//
//   - `{"lead_type":"agent"}` over a stored USER id left the pair desynced,
//     pointing at a row of the wrong kind that neither read path resolves; and
//   - `{"lead_type":"banana"}` persisted, because the enum is only checked
//     inside projectLeadInWorkspaceOrReject and that was never reached.
//
// The reference has to be validated whenever EITHER half moves, against the
// pair the write will leave behind rather than the fragment the request
// carried.
func TestUpdateProject_LeadPairValidatedWhenEitherHalfMoves(t *testing.T) {
	db := setupTestDB(t)
	a := fenceSeedTenant(t, db, "a")
	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	patch := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch,
			"/api/v1/projects/proj-fence-a?workspace_id="+a.wsID, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+a.token)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		return rr
	}
	storedLead := func(t *testing.T) (string, string) {
		t.Helper()
		var lt, lid string
		if err := db.QueryRow(
			`SELECT COALESCE(lead_type,''), COALESCE(lead_id,'') FROM projects WHERE id = ?`,
			"proj-fence-a").Scan(&lt, &lid); err != nil {
			t.Fatalf("read stored lead: %v", err)
		}
		return lt, lid
	}

	// Positive control: the legitimate pair still lands. Without this the
	// assertions below would pass on a handler that rejects everything.
	if rr := patch(`{"lead_type":"user","lead_id":"` + a.userID + `"}`); rr.Code != http.StatusOK {
		t.Fatalf("setting a valid user lead: status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if lt, lid := storedLead(t); lt != "user" || lid != a.userID {
		t.Fatalf("stored lead = (%q, %q), want (user, %s)", lt, lid, a.userID)
	}

	t.Run("lead_type alone cannot desync the pair", func(t *testing.T) {
		rr := patch(`{"lead_type":"agent"}`)
		if rr.Code == http.StatusOK {
			t.Errorf("status = %d, want a rejection — 'agent' over a stored user id "+
				"leaves a reference no read path resolves", rr.Code)
		}
		lt, lid := storedLead(t)
		if lt == "agent" {
			t.Errorf("stored lead = (%q, %q): the type moved to 'agent' while the id is still a user id", lt, lid)
		}
	})

	t.Run("lead_type alone must be in the enum", func(t *testing.T) {
		rr := patch(`{"lead_type":"banana"}`)
		if rr.Code == http.StatusOK {
			t.Errorf("status = %d, want 400 for a lead_type outside the enum", rr.Code)
		}
		if lt, _ := storedLead(t); lt == "banana" {
			t.Errorf("stored lead_type = %q — an arbitrary string reached the column", lt)
		}
	})

	t.Run("moving both halves together still works", func(t *testing.T) {
		agentID := a.ids["agentId"]
		if agentID == "" {
			t.Fatal("fixture regression: no seeded agent id for this tenant")
		}
		if rr := patch(`{"lead_type":"agent","lead_id":"` + agentID + `"}`); rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
		}
		if lt, lid := storedLead(t); lt != "agent" || lid != agentID {
			t.Errorf("stored lead = (%q, %q), want (agent, %s)", lt, lid, agentID)
		}
	})

	t.Run("lead_id alone still resolves its type from the row", func(t *testing.T) {
		// The behaviour the original fix was careful to preserve: moving only
		// the id must not 400 a legitimate edit.
		agentID := a.ids["agentId"]
		if agentID == "" {
			t.Fatal("fixture regression: no seeded agent id for this tenant")
		}
		if rr := patch(`{"lead_id":"` + agentID + `"}`); rr.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 — the stored lead_type should resolve this; body=%s",
				rr.Code, rr.Body.String())
		}
	})

	t.Run("a project with no lead still cannot take a bogus type", func(t *testing.T) {
		// The pair check short-circuits when no id survives the PATCH — there
		// is nothing to resolve against a table. The enum still has to hold,
		// or the column collects values no read path understands and the next
		// caller to set an id inherits them.
		if rr := patch(`{"lead_type":"","lead_id":""}`); rr.Code != http.StatusOK {
			t.Fatalf("clearing the lead: status = %d, want 200; body=%s", rr.Code, rr.Body.String())
		}
		rr := patch(`{"lead_type":"banana"}`)
		if rr.Code == http.StatusOK {
			t.Errorf("status = %d, want 400 — lead_type outside the enum on a lead-less project", rr.Code)
		}
		if lt, _ := storedLead(t); lt == "banana" {
			t.Errorf("stored lead_type = %q on a project with no lead", lt)
		}
	})
}
