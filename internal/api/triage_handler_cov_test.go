package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// Coverage tests for triage_handler.go: ListRules, CreateRule, UpdateRule,
// DeleteRule, Process, and triageMatchCompiled. Auth/role failures, invalid
// JSON, invalid rule definitions, not-found, happy paths, and the Process
// matching logic (matched + unmatched branches).
//
// Skipped: nothing Docker/network-related lives in this file. The only
// uncovered branches are DB-error paths (QueryContext/Exec failures) which
// would require an injected failing *sql.DB and aren't worth simulating.

// covTriHandler builds a TriageHandler against a fresh test DB with a seeded
// owner user + workspace. Returns the handler, db, userID, and wsID.
func covTriHandler(t *testing.T) (*TriageHandler, *sql.DB, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewTriageHandler(db, nil, newTestLogger())
	return h, db, userID, wsID
}

// covTriSeedRule inserts a triage rule directly and returns its ID.
func covTriSeedRule(t *testing.T, db *sql.DB, wsID, name, pattern, matchType string, enabled bool, position int) string {
	t.Helper()
	id := generateCUID()
	en := 0
	if enabled {
		en = 1
	}
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO triage_rules (id, workspace_id, name, pattern, match_type,
		    position, enabled, match_count, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, datetime('now'))`,
		id, wsID, name, pattern, matchType, position, en)
	if err != nil {
		t.Fatalf("seed triage rule: %v", err)
	}
	return id
}

// covTriSeedIssueTitle inserts a BACKLOG issue with a NULL assignee and the
// given title (so Process matching can be exercised against arbitrary titles).
var covTriIssueSeq int

func covTriSeedIssueTitle(t *testing.T, db *sql.DB, wsID, crewID, leadID, title string) string {
	t.Helper()
	covTriIssueSeq++
	n := covTriIssueSeq
	id := generateCUID()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title,
		    status, number, identifier, priority, sort_order, mission_type,
		    assignee_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'BACKLOG', ?, ?, 'medium', 0, 'issue',
		    NULL, datetime('now'), datetime('now'))`,
		id, wsID, crewID, leadID, "trace-"+id, title, n, "ISS-"+id)
	if err != nil {
		t.Fatalf("seed issue title: %v", err)
	}
	return id
}

// ── ListRules ──────────────────────────────────────────────────────────────

func TestCovTriListRules_Empty(t *testing.T) {
	h, _, userID, wsID := covTriHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/triage/rules", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.ListRules(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty list should be []; got %q", rec.Body.String())
	}
}

func TestCovTriListRules_Happy(t *testing.T) {
	h, db, userID, wsID := covTriHandler(t)
	covTriSeedRule(t, db, wsID, "Bugs", "bug", "contains", true, 1)
	covTriSeedRule(t, db, wsID, "Exact", "Feature", "exact", false, 2)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/triage/rules", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.ListRules(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	var out []triageRuleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d rules, want 2", len(out))
	}
	if out[0].Name != "Bugs" || out[1].Name != "Exact" {
		t.Fatalf("unexpected order/names: %+v", out)
	}
}

// ── CreateRule ─────────────────────────────────────────────────────────────

func TestCovTriCreateRule_Forbidden(t *testing.T) {
	h, _, userID, wsID := covTriHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/triage/rules", jsonBody(map[string]string{
		"name": "x", "pattern": "y", "match_type": "contains",
	}))
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rec := httptest.NewRecorder()
	h.CreateRule(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", rec.Code)
	}
}

func TestCovTriCreateRule_InvalidJSON(t *testing.T) {
	h, _, userID, wsID := covTriHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/triage/rules", strings.NewReader("{not json"))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.CreateRule(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}

func TestCovTriCreateRule_ValidationErrors(t *testing.T) {
	h, _, userID, wsID := covTriHandler(t)
	cases := []struct {
		name string
		body map[string]string
	}{
		{"missing name", map[string]string{"pattern": "p", "match_type": "contains"}},
		{"missing pattern", map[string]string{"name": "n", "match_type": "contains"}},
		{"bad match_type", map[string]string{"name": "n", "pattern": "p", "match_type": "nope"}},
		{"bad regex", map[string]string{"name": "n", "pattern": "(", "match_type": "regex"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/triage/rules", jsonBody(tc.body))
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rec := httptest.NewRecorder()
			h.CreateRule(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("got %d, want 400", rec.Code)
			}
		})
	}
}

func TestCovTriCreateRule_Happy(t *testing.T) {
	h, db, userID, wsID := covTriHandler(t)
	crewID := seedCrewRow(t, db, "crew-tri-1", wsID, "Eng", "eng")
	priority := "high"
	body := map[string]any{
		"name":       "Bug router",
		"pattern":    "^bug:",
		"match_type": "regex",
		"crew_id":    crewID,
		"priority":   priority,
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/triage/rules", jsonBody(body))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.CreateRule(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var out triageRuleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ID == "" || out.Position != 1 || !out.Enabled || out.MatchCount != 0 {
		t.Fatalf("unexpected response: %+v", out)
	}
	// Second create bumps position.
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/triage/rules", jsonBody(map[string]string{
		"name": "n2", "pattern": "p2", "match_type": "contains",
	}))
	req2 = withWorkspaceUser(req2, userID, wsID, "OWNER")
	rec2 := httptest.NewRecorder()
	h.CreateRule(rec2, req2)
	var out2 triageRuleResponse
	if err := json.Unmarshal(rec2.Body.Bytes(), &out2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out2.Position != 2 {
		t.Fatalf("second rule position = %d, want 2", out2.Position)
	}
}

// ── UpdateRule ─────────────────────────────────────────────────────────────

func TestCovTriUpdateRule_Forbidden(t *testing.T) {
	h, db, userID, wsID := covTriHandler(t)
	id := covTriSeedRule(t, db, wsID, "n", "p", "contains", true, 1)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/triage/rules/"+id, jsonBody(map[string]string{"name": "x"}))
	req.SetPathValue("id", id)
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rec := httptest.NewRecorder()
	h.UpdateRule(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", rec.Code)
	}
}

func TestCovTriUpdateRule_NotFound(t *testing.T) {
	h, _, userID, wsID := covTriHandler(t)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/triage/rules/missing", jsonBody(map[string]string{"name": "x"}))
	req.SetPathValue("id", "missing")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.UpdateRule(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rec.Code)
	}
}

func TestCovTriUpdateRule_InvalidJSON(t *testing.T) {
	h, db, userID, wsID := covTriHandler(t)
	id := covTriSeedRule(t, db, wsID, "n", "p", "contains", true, 1)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/triage/rules/"+id, strings.NewReader("{bad"))
	req.SetPathValue("id", id)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.UpdateRule(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}

func TestCovTriUpdateRule_BadMatchType(t *testing.T) {
	h, db, userID, wsID := covTriHandler(t)
	id := covTriSeedRule(t, db, wsID, "n", "p", "contains", true, 1)
	mt := "invalid"
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/triage/rules/"+id, jsonBody(map[string]any{"match_type": mt}))
	req.SetPathValue("id", id)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.UpdateRule(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}

func TestCovTriUpdateRule_BadRegex(t *testing.T) {
	h, db, userID, wsID := covTriHandler(t)
	id := covTriSeedRule(t, db, wsID, "n", "p", "contains", true, 1)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/triage/rules/"+id, jsonBody(map[string]any{
		"pattern": "(", "match_type": "regex",
	}))
	req.SetPathValue("id", id)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.UpdateRule(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}

func TestCovTriUpdateRule_NoFields(t *testing.T) {
	h, db, userID, wsID := covTriHandler(t)
	id := covTriSeedRule(t, db, wsID, "n", "p", "contains", true, 1)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/triage/rules/"+id, jsonBody(map[string]any{}))
	req.SetPathValue("id", id)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.UpdateRule(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}

func TestCovTriUpdateRule_Happy(t *testing.T) {
	h, db, userID, wsID := covTriHandler(t)
	id := covTriSeedRule(t, db, wsID, "n", "p", "contains", true, 1)
	newName := "renamed"
	newPattern := "^x$"
	newMatch := "regex"
	pos := 5
	enabled := false
	body := map[string]any{
		"name":        newName,
		"pattern":     newPattern,
		"match_type":  newMatch,
		"crew_id":     "",       // exercises SetNull
		"assignee_id": "",       // exercises SetNull
		"project_id":  "",       // exercises SetNull
		"priority":    "urgent", // exercises Set
		"labels_json": "[]",
		"position":    pos,
		"enabled":     enabled,
	}
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/triage/rules/"+id, jsonBody(body))
	req.SetPathValue("id", id)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.UpdateRule(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var out triageRuleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Name != newName || out.Pattern != newPattern || out.MatchType != newMatch {
		t.Fatalf("update not applied: %+v", out)
	}
	if out.Position != pos || out.Enabled {
		t.Fatalf("position/enabled not applied: %+v", out)
	}
}

// ── DeleteRule ─────────────────────────────────────────────────────────────

func TestCovTriDeleteRule_Forbidden(t *testing.T) {
	h, db, userID, wsID := covTriHandler(t)
	id := covTriSeedRule(t, db, wsID, "n", "p", "contains", true, 1)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/triage/rules/"+id, nil)
	req.SetPathValue("id", id)
	req = withWorkspaceUser(req, userID, wsID, "MANAGER") // manage requires OWNER/ADMIN
	rec := httptest.NewRecorder()
	h.DeleteRule(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", rec.Code)
	}
}

func TestCovTriDeleteRule_NotFound(t *testing.T) {
	h, _, userID, wsID := covTriHandler(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/triage/rules/missing", nil)
	req.SetPathValue("id", "missing")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.DeleteRule(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rec.Code)
	}
}

func TestCovTriDeleteRule_Happy(t *testing.T) {
	h, db, userID, wsID := covTriHandler(t)
	id := covTriSeedRule(t, db, wsID, "n", "p", "contains", true, 1)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/triage/rules/"+id, nil)
	req.SetPathValue("id", id)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.DeleteRule(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got %d, want 204", rec.Code)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM triage_rules WHERE id = ?`, id).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("rule not deleted")
	}
}

// ── Process ────────────────────────────────────────────────────────────────

func TestCovTriProcess_Forbidden(t *testing.T) {
	h, _, userID, wsID := covTriHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/triage/process", nil)
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rec := httptest.NewRecorder()
	h.Process(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", rec.Code)
	}
}

func TestCovTriProcess_NoRules(t *testing.T) {
	h, _, userID, wsID := covTriHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/triage/process", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Process(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	var out map[string]int
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["processed"] != 0 || out["matched"] != 0 {
		t.Fatalf("no-rules should be 0/0; got %+v", out)
	}
}

func TestCovTriProcess_MatchedAndUnmatched(t *testing.T) {
	h, db, userID, wsID := covTriHandler(t)
	crewID := seedCrewRow(t, db, "crew-tri-proc", wsID, "Eng", "eng")
	leadID := seedAgentRow(t, db, "agent-tri-lead", wsID, crewID, "Lead", "lead", "LEAD")
	agentID := seedAgentRow(t, db, "agent-tri-proc", wsID, crewID, "A", "a", "AGENT")

	// Rule that assigns crew+assignee+priority on "contains bug".
	ruleID := covTriSeedRule(t, db, wsID, "bug rule", "bug", "contains", true, 1)
	if _, err := db.Exec(`UPDATE triage_rules SET crew_id = ?, assignee_id = ?, priority = 'high' WHERE id = ?`,
		crewID, agentID, ruleID); err != nil {
		t.Fatalf("set rule actions: %v", err)
	}
	// A disabled rule and an invalid-regex rule that should be skipped.
	covTriSeedRule(t, db, wsID, "disabled", "feature", "contains", false, 2)
	covTriSeedRule(t, db, wsID, "badre", "(", "regex", true, 3)

	matchedIssue := covTriSeedIssueTitle(t, db, wsID, crewID, leadID, "Fix login bug now")
	unmatchedIssue := covTriSeedIssueTitle(t, db, wsID, crewID, leadID, "Add a new dashboard")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/triage/process", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Process(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var out map[string]int
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["processed"] != 2 {
		t.Fatalf("processed = %d, want 2", out["processed"])
	}
	if out["matched"] != 1 {
		t.Fatalf("matched = %d, want 1", out["matched"])
	}

	// Matched issue should now have assignee/crew/priority set.
	var assignee, assigneeType, priority sql.NullString
	if err := db.QueryRow(`SELECT assignee_id, assignee_type, priority FROM missions WHERE id = ?`, matchedIssue).
		Scan(&assignee, &assigneeType, &priority); err != nil {
		t.Fatalf("read matched issue: %v", err)
	}
	if assignee.String != agentID || assigneeType.String != "agent" || priority.String != "high" {
		t.Fatalf("matched issue not updated: assignee=%v type=%v prio=%v", assignee, assigneeType, priority)
	}

	// Unmatched issue should still be unassigned.
	var ua sql.NullString
	if err := db.QueryRow(`SELECT assignee_id FROM missions WHERE id = ?`, unmatchedIssue).Scan(&ua); err != nil {
		t.Fatalf("read unmatched issue: %v", err)
	}
	if ua.Valid {
		t.Fatalf("unmatched issue should remain unassigned; got %v", ua)
	}

	// match_count incremented on the bug rule.
	var mc int
	if err := db.QueryRow(`SELECT match_count FROM triage_rules WHERE id = ?`, ruleID).Scan(&mc); err != nil {
		t.Fatalf("read match_count: %v", err)
	}
	if mc != 1 {
		t.Fatalf("match_count = %d, want 1", mc)
	}
}

func TestCovTriProcess_RegexAndExactMatch(t *testing.T) {
	h, db, userID, wsID := covTriHandler(t)
	crewID := seedCrewRow(t, db, "crew-tri-re", wsID, "Eng", "eng")
	leadID := seedAgentRow(t, db, "agent-tri-re-lead", wsID, crewID, "Lead", "lead", "LEAD")

	covTriSeedRule(t, db, wsID, "regex rule", "^URGENT:", "regex", true, 1)
	covTriSeedRule(t, db, wsID, "exact rule", "Deploy prod", "exact", true, 2)

	covTriSeedIssueTitle(t, db, wsID, crewID, leadID, "URGENT: server down")
	covTriSeedIssueTitle(t, db, wsID, crewID, leadID, "Deploy prod")
	covTriSeedIssueTitle(t, db, wsID, crewID, leadID, "deploy prod") // exact is case-sensitive, no match

	req := httptest.NewRequest(http.MethodPost, "/api/v1/triage/process", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Process(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	var out map[string]int
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["processed"] != 3 || out["matched"] != 2 {
		t.Fatalf("got processed=%d matched=%d, want 3/2", out["processed"], out["matched"])
	}
}

// ── triageMatchCompiled (direct unit) ───────────────────────────────────────

func TestCovTriMatchCompiled(t *testing.T) {
	re := regexp.MustCompile("^bug:")
	cases := []struct {
		name      string
		matchType string
		pattern   string
		title     string
		re        *regexp.Regexp
		want      bool
	}{
		{"contains hit", "contains", "BUG", "fix a bug", nil, true},
		{"contains miss", "contains", "feature", "fix a bug", nil, false},
		{"regex hit", "regex", "^bug:", "bug: broken", re, true},
		{"regex miss", "regex", "^bug:", "a bug: broken", re, false},
		{"regex nil compiled", "regex", "^bug:", "bug: broken", nil, false},
		{"exact hit", "exact", "Deploy", "Deploy", nil, true},
		{"exact miss", "exact", "Deploy", "deploy", nil, false},
		{"unknown type", "weird", "x", "x", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := triageMatchCompiled(tc.matchType, tc.pattern, tc.title, tc.re); got != tc.want {
				t.Fatalf("triageMatchCompiled(%q,%q,%q) = %v, want %v",
					tc.matchType, tc.pattern, tc.title, got, tc.want)
			}
		})
	}
}
