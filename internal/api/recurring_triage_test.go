package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func newRecurringHandler(t *testing.T) (*RecurringIssueHandler, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	userID, wsID, crewID, _, _ := seedIssueFixtures(t, db)
	return NewRecurringIssueHandler(db, nil, logger), userID, wsID, crewID
}

func TestRecurring_Create(t *testing.T) {
	h, userID, wsID, crewID := newRecurringHandler(t)

	body := bytes.NewBufferString(`{"crew_id":"` + crewID + `","title":"Daily standup","cron_expression":"0 9 * * *","priority":"medium"}`)
	req := httptest.NewRequest("POST", "/", body)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d body=%s", rr.Code, rr.Body.String())
	}
	var resp recurringIssueResponse
	mustUnmarshal(t, rr, &resp)
	if resp.NextRun == nil {
		t.Error("next_run not set")
	}
}

func TestRecurring_Create_Validations(t *testing.T) {
	h, userID, wsID, crewID := newRecurringHandler(t)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"no crew", `{"title":"x","cron_expression":"0 9 * * *"}`, 400},
		{"no title", `{"crew_id":"` + crewID + `","cron_expression":"0 9 * * *"}`, 400},
		{"no cron", `{"crew_id":"` + crewID + `","title":"x"}`, 400},
		{"bad cron", `{"crew_id":"` + crewID + `","title":"x","cron_expression":"not-cron"}`, 400},
		{"bad json", `not json`, 400},
		{"missing crew", `{"crew_id":"missing","title":"x","cron_expression":"0 9 * * *"}`, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/", bytes.NewBufferString(tc.body))
			req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
			rr := httptest.NewRecorder()
			h.Create(rr, req)
			if rr.Code != tc.want {
				t.Errorf("got %d want %d body=%s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

func TestRecurring_Create_Forbidden(t *testing.T) {
	h, userID, wsID, _ := newRecurringHandler(t)
	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{}`))
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "VIEWER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestRecurring_List_Update_Delete(t *testing.T) {
	h, userID, wsID, crewID := newRecurringHandler(t)

	// Create one
	body := bytes.NewBufferString(`{"crew_id":"` + crewID + `","title":"x","cron_expression":"0 9 * * *"}`)
	req := httptest.NewRequest("POST", "/", body)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	var resp recurringIssueResponse
	mustUnmarshal(t, rr, &resp)

	// List
	req2 := httptest.NewRequest("GET", "/?crew_id="+crewID, nil)
	req2 = req2.WithContext(withWorkspace(withUser(req2.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr2 := httptest.NewRecorder()
	h.List(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("list: %d", rr2.Code)
	}

	// Update
	body3 := bytes.NewBufferString(`{"title":"renamed","cron_expression":"30 9 * * *","enabled":false,"description":"d","priority":"high","project_id":"","milestone_id":"","assignee_type":"agent","assignee_id":"agent-worker","labels_json":"[]"}`)
	req3 := httptest.NewRequest("PATCH", "/", body3)
	req3.SetPathValue("recurringId", resp.ID)
	req3 = req3.WithContext(withWorkspace(withUser(req3.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr3 := httptest.NewRecorder()
	h.Update(rr3, req3)
	if rr3.Code != http.StatusOK {
		t.Fatalf("update: %d body=%s", rr3.Code, rr3.Body.String())
	}

	// Delete
	req4 := httptest.NewRequest("DELETE", "/", nil)
	req4.SetPathValue("recurringId", resp.ID)
	req4 = req4.WithContext(withWorkspace(withUser(req4.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr4 := httptest.NewRecorder()
	h.Delete(rr4, req4)
	if rr4.Code != http.StatusNoContent {
		t.Errorf("delete: %d", rr4.Code)
	}
}

func TestRecurring_Update_NotFound(t *testing.T) {
	h, userID, wsID, _ := newRecurringHandler(t)
	req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{"title":"x"}`))
	req.SetPathValue("recurringId", "missing")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestRecurring_Update_BadCron(t *testing.T) {
	h, userID, wsID, crewID := newRecurringHandler(t)

	body := bytes.NewBufferString(`{"crew_id":"` + crewID + `","title":"x","cron_expression":"0 9 * * *"}`)
	req := httptest.NewRequest("POST", "/", body)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	var resp recurringIssueResponse
	mustUnmarshal(t, rr, &resp)

	req2 := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{"cron_expression":"bogus"}`))
	req2.SetPathValue("recurringId", resp.ID)
	req2 = req2.WithContext(withWorkspace(withUser(req2.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr2 := httptest.NewRecorder()
	h.Update(rr2, req2)
	if rr2.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr2.Code)
	}
}

func TestRecurring_Update_NoFields(t *testing.T) {
	h, userID, wsID, crewID := newRecurringHandler(t)

	body := bytes.NewBufferString(`{"crew_id":"` + crewID + `","title":"x","cron_expression":"0 9 * * *"}`)
	req := httptest.NewRequest("POST", "/", body)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	var resp recurringIssueResponse
	mustUnmarshal(t, rr, &resp)

	req2 := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{}`))
	req2.SetPathValue("recurringId", resp.ID)
	req2 = req2.WithContext(withWorkspace(withUser(req2.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr2 := httptest.NewRecorder()
	h.Update(rr2, req2)
	if rr2.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr2.Code)
	}
}

func TestRecurring_Delete_NotFound(t *testing.T) {
	h, userID, wsID, _ := newRecurringHandler(t)
	req := httptest.NewRequest("DELETE", "/", nil)
	req.SetPathValue("recurringId", "missing")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d", rr.Code)
	}
}

// ── Triage ────────────────────────────────────────────────────────────

func newTriageHandler(t *testing.T) (*TriageHandler, string, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	return NewTriageHandler(db, nil, logger), userID, wsID, crewID, leadID
}

func TestTriage_CreateRule_Validations(t *testing.T) {
	h, userID, wsID, _, _ := newTriageHandler(t)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"no name", `{"pattern":"x","match_type":"contains"}`, 400},
		{"no pattern", `{"name":"r","match_type":"contains"}`, 400},
		{"bad match_type", `{"name":"r","pattern":"x","match_type":"foo"}`, 400},
		{"bad regex", `{"name":"r","pattern":"[","match_type":"regex"}`, 400},
		{"bad json", `not`, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/", bytes.NewBufferString(tc.body))
			req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
			rr := httptest.NewRecorder()
			h.CreateRule(rr, req)
			if rr.Code != tc.want {
				t.Errorf("got %d want %d", rr.Code, tc.want)
			}
		})
	}
}

func TestTriage_CRUD(t *testing.T) {
	h, userID, wsID, _, _ := newTriageHandler(t)

	// Create
	body := bytes.NewBufferString(`{"name":"bug-rule","pattern":"bug","match_type":"contains"}`)
	req := httptest.NewRequest("POST", "/", body)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.CreateRule(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d body=%s", rr.Code, rr.Body.String())
	}
	var tr triageRuleResponse
	mustUnmarshal(t, rr, &tr)

	// List
	req2 := httptest.NewRequest("GET", "/", nil)
	req2 = req2.WithContext(withWorkspace(withUser(req2.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr2 := httptest.NewRecorder()
	h.ListRules(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("list: %d", rr2.Code)
	}

	// Update
	body3 := bytes.NewBufferString(`{"name":"renamed","pattern":"crash","match_type":"contains","priority":"high","labels_json":"[]","position":1,"enabled":true,"crew_id":"","assignee_id":"","project_id":""}`)
	req3 := httptest.NewRequest("PATCH", "/", body3)
	req3.SetPathValue("ruleId", tr.ID)
	req3 = req3.WithContext(withWorkspace(withUser(req3.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr3 := httptest.NewRecorder()
	h.UpdateRule(rr3, req3)
	if rr3.Code != http.StatusOK {
		t.Fatalf("update: %d body=%s", rr3.Code, rr3.Body.String())
	}

	// Delete
	req4 := httptest.NewRequest("DELETE", "/", nil)
	req4.SetPathValue("ruleId", tr.ID)
	req4 = req4.WithContext(withWorkspace(withUser(req4.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr4 := httptest.NewRecorder()
	h.DeleteRule(rr4, req4)
	if rr4.Code != http.StatusNoContent {
		t.Errorf("delete: %d", rr4.Code)
	}
}

func TestTriage_UpdateRule_BadMatchType(t *testing.T) {
	h, userID, wsID, _, _ := newTriageHandler(t)

	body := bytes.NewBufferString(`{"name":"r","pattern":"x","match_type":"contains"}`)
	req := httptest.NewRequest("POST", "/", body)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.CreateRule(rr, req)
	var tr triageRuleResponse
	mustUnmarshal(t, rr, &tr)

	req2 := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{"match_type":"yolo"}`))
	req2.SetPathValue("ruleId", tr.ID)
	req2 = req2.WithContext(withWorkspace(withUser(req2.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr2 := httptest.NewRecorder()
	h.UpdateRule(rr2, req2)
	if rr2.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr2.Code)
	}
}

func TestTriage_UpdateRule_BadRegex(t *testing.T) {
	h, userID, wsID, _, _ := newTriageHandler(t)

	body := bytes.NewBufferString(`{"name":"r","pattern":"x","match_type":"contains"}`)
	req := httptest.NewRequest("POST", "/", body)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.CreateRule(rr, req)
	var tr triageRuleResponse
	mustUnmarshal(t, rr, &tr)

	req2 := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{"pattern":"[","match_type":"regex"}`))
	req2.SetPathValue("ruleId", tr.ID)
	req2 = req2.WithContext(withWorkspace(withUser(req2.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr2 := httptest.NewRecorder()
	h.UpdateRule(rr2, req2)
	if rr2.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr2.Code)
	}
}

func TestTriage_UpdateRule_NoFields(t *testing.T) {
	h, userID, wsID, _, _ := newTriageHandler(t)

	body := bytes.NewBufferString(`{"name":"r","pattern":"x","match_type":"contains"}`)
	req := httptest.NewRequest("POST", "/", body)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.CreateRule(rr, req)
	var tr triageRuleResponse
	mustUnmarshal(t, rr, &tr)

	req2 := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{}`))
	req2.SetPathValue("ruleId", tr.ID)
	req2 = req2.WithContext(withWorkspace(withUser(req2.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr2 := httptest.NewRecorder()
	h.UpdateRule(rr2, req2)
	if rr2.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr2.Code)
	}
}

func TestTriage_UpdateRule_NotFound(t *testing.T) {
	h, userID, wsID, _, _ := newTriageHandler(t)
	req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{"name":"x"}`))
	req.SetPathValue("ruleId", "missing")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.UpdateRule(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestTriage_DeleteRule_NotFound(t *testing.T) {
	h, userID, wsID, _, _ := newTriageHandler(t)
	req := httptest.NewRequest("DELETE", "/", nil)
	req.SetPathValue("ruleId", "missing")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.DeleteRule(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestTriage_Process(t *testing.T) {
	h, userID, wsID, crewID, leadID := newTriageHandler(t)
	// Create rule that matches "bug"
	body := bytes.NewBufferString(`{"name":"bug","pattern":"bug","match_type":"contains","priority":"high"}`)
	req := httptest.NewRequest("POST", "/", body)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.CreateRule(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create rule: %d", rr.Code)
	}

	// Seed an issue with no assignee
	id := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	if _, err := h.db.Exec(`UPDATE missions SET title='this is a bug', assignee_id=NULL WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}

	// Process
	req2 := httptest.NewRequest("POST", "/", nil)
	req2 = req2.WithContext(withWorkspace(withUser(req2.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr2 := httptest.NewRecorder()
	h.Process(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("process: %d body=%s", rr2.Code, rr2.Body.String())
	}
	var resp map[string]int
	mustUnmarshal(t, rr2, &resp)
	if resp["matched"] != 1 {
		t.Errorf("matched = %d, want 1", resp["matched"])
	}
}

func TestTriage_Process_NoRules(t *testing.T) {
	h, userID, wsID, _, _ := newTriageHandler(t)

	req := httptest.NewRequest("POST", "/", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Process(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
}

func TestTriage_Process_Forbidden(t *testing.T) {
	h, userID, wsID, _, _ := newTriageHandler(t)
	req := httptest.NewRequest("POST", "/", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "VIEWER"))
	rr := httptest.NewRecorder()
	h.Process(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

// TestRecurring_Update_Delete_ThroughRealRoute drives Update/Delete through a
// mux carrying the PRODUCTION route pattern ({recurringId}), so the path param
// is populated by the router exactly as it is in prod. A handler that reads the
// wrong PathValue key returns "" → 404 on an existing record. Regression guard
// for the route/handler param-name mismatch.
func TestRecurring_Update_Delete_ThroughRealRoute(t *testing.T) {
	h, userID, wsID, crewID := newRecurringHandler(t)

	// Create one.
	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(
		`{"crew_id":"`+crewID+`","title":"x","cron_expression":"0 9 * * *"}`))
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	var resp recurringIssueResponse
	mustUnmarshal(t, rr, &resp)

	inject := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			r = r.WithContext(withWorkspace(withUser(r.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
			next(w, r)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /api/v1/recurring-issues/{recurringId}", inject(h.Update))
	mux.HandleFunc("DELETE /api/v1/recurring-issues/{recurringId}", inject(h.Delete))

	// PATCH via the real URL path.
	body := `{"title":"renamed","cron_expression":"30 9 * * *","enabled":false,"description":"d","priority":"high","project_id":"","milestone_id":"","assignee_type":"agent","assignee_id":"agent-worker","labels_json":"[]"}`
	preq := httptest.NewRequest("PATCH", "/api/v1/recurring-issues/"+resp.ID, bytes.NewBufferString(body))
	prr := httptest.NewRecorder()
	mux.ServeHTTP(prr, preq)
	if prr.Code != http.StatusOK {
		t.Fatalf("PATCH through route: %d body=%s", prr.Code, prr.Body.String())
	}

	// DELETE via the real URL path.
	dreq := httptest.NewRequest("DELETE", "/api/v1/recurring-issues/"+resp.ID, nil)
	drr := httptest.NewRecorder()
	mux.ServeHTTP(drr, dreq)
	if drr.Code != http.StatusNoContent {
		t.Fatalf("DELETE through route: %d body=%s", drr.Code, drr.Body.String())
	}
}

// TestTriage_Update_Delete_ThroughRealRoute is the triage-rule analogue:
// the production route uses {ruleId}; a handler reading another key 404s.
func TestTriage_Update_Delete_ThroughRealRoute(t *testing.T) {
	h, userID, wsID, _, _ := newTriageHandler(t)

	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(
		`{"name":"bug-rule","pattern":"bug","match_type":"contains"}`))
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.CreateRule(rr, req)
	var tr triageRuleResponse
	mustUnmarshal(t, rr, &tr)

	inject := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			r = r.WithContext(withWorkspace(withUser(r.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
			next(w, r)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /api/v1/triage-rules/{ruleId}", inject(h.UpdateRule))
	mux.HandleFunc("DELETE /api/v1/triage-rules/{ruleId}", inject(h.DeleteRule))

	body := `{"name":"renamed","pattern":"crash","match_type":"contains","priority":"high","labels_json":"[]","position":1,"enabled":true,"crew_id":"","assignee_id":"","project_id":""}`
	preq := httptest.NewRequest("PATCH", "/api/v1/triage-rules/"+tr.ID, bytes.NewBufferString(body))
	prr := httptest.NewRecorder()
	mux.ServeHTTP(prr, preq)
	if prr.Code != http.StatusOK {
		t.Fatalf("PATCH through route: %d body=%s", prr.Code, prr.Body.String())
	}

	dreq := httptest.NewRequest("DELETE", "/api/v1/triage-rules/"+tr.ID, nil)
	drr := httptest.NewRecorder()
	mux.ServeHTTP(drr, dreq)
	if drr.Code != http.StatusNoContent {
		t.Fatalf("DELETE through route: %d body=%s", drr.Code, drr.Body.String())
	}
}

func TestTriage_MatchHelper(t *testing.T) {
	if !triageMatchCompiled("contains", "bug", "this is a Bug fix", nil) {
		t.Error("contains case-insensitive must match")
	}
	if triageMatchCompiled("exact", "bug", "abugc", nil) {
		t.Error("exact must not match substring")
	}
	if !triageMatchCompiled("exact", "bug", "bug", nil) {
		t.Error("exact must match")
	}
	if triageMatchCompiled("regex", "x", "abc", nil) {
		t.Error("regex with nil compiled must be false")
	}
	if triageMatchCompiled("unknown", "x", "abc", nil) {
		t.Error("unknown match_type must be false")
	}
}
