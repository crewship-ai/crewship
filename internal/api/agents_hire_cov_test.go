package api

// Coverage-focused tests for agents_hire.go. The existing
// agents_hire_test.go / agents_rehire_test.go cover the headline
// matrix cells; this file fills in the validation branches, the
// slug-based crew lookup, parent_lead_id resolution, TTL clamping,
// the "full" autonomy journal-only path, and the Rehire input-guard
// branches that those suites skip.
//
// Reuses the shared fixtures from agents_hire_test.go (seedHireCrew,
// newHireHandler, postHire) and agents_rehire_test.go (postRehire,
// seedEphemeralAgent). New helpers here are prefixed covHire to avoid
// clashing with anything in the package.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// covHirePostRaw posts an arbitrary raw body (not JSON-marshalled) so
// we can exercise the invalid-JSON branch that postHire can't reach.
func covHirePostRaw(t *testing.T, h *AgentHandler, userID, wsID, role, raw string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/agents/hire", bytes.NewReader([]byte(raw)))
	req.Header.Set("Content-Type", "application/json")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, role)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Hire(rr, req)
	return rr
}

// covHirePostRehireRaw is the rehire analogue for the invalid-JSON path.
func covHirePostRehireRaw(t *testing.T, h *AgentHandler, userID, wsID, role, agentID, raw string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/rehire", bytes.NewReader([]byte(raw)))
	req.SetPathValue("agentId", agentID)
	req.Header.Set("Content-Type", "application/json")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, role)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Rehire(rr, req)
	return rr
}

// ---- Hire: validation branches ----

func TestCovHireForbiddenForViewer(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)

	rr := postHire(t, h, userID, wsID, "VIEWER", map[string]any{
		"crew_id":       crewID,
		"template_slug": "docs-writer",
		"reason":        "rbac",
	})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", rr.Code, rr.Body.String())
	}
}

func TestCovHireInvalidJSONIs400(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, _ := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)

	rr := covHirePostRaw(t, h, userID, wsID, "MANAGER", "{not valid json")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
}

func TestCovHireMissingCrewRefIs400(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, _ := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)

	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"template_slug": "docs-writer",
		"reason":        "no crew ref",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
}

func TestCovHireBothCrewRefsMutuallyExclusive(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)

	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":       crewID,
		"crew_slug":     "hire-crew",
		"template_slug": "docs-writer",
		"reason":        "both refs",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "mutually exclusive") {
		t.Errorf("body = %s; want mutually-exclusive message", rr.Body.String())
	}
}

func TestCovHireMissingTemplateSlugIs400(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)

	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":       crewID,
		"template_slug": "   ",
		"reason":        "blank template",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
}

func TestCovHireTemplateNotFoundIs404(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)

	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":       crewID,
		"template_slug": "no-such-template",
		"reason":        "missing template",
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rr.Code, rr.Body.String())
	}
}

// ---- Hire: crew_slug resolution path ----

func TestCovHireResolvesByCrewSlug(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, _ := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)

	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_slug":     "hire-crew",
		"template_slug": "docs-writer",
		"reason":        "by slug",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	var resp hireResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ID == "" || !resp.Ephemeral {
		t.Errorf("unexpected response: %+v", resp)
	}
}

// ---- Hire: TTL clamping to max ----

func TestCovHireTTLClampedToMax(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)

	// Way over the 24h cap; the handler clamps to maxHireTTLMinutes.
	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":       crewID,
		"template_slug": "docs-writer",
		"reason":        "huge ttl",
		"ttl_minutes":   999999,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	var resp hireResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ExpiresAt == nil {
		t.Fatalf("expires_at nil")
	}
	parsed, err := time.Parse(time.RFC3339, *resp.ExpiresAt)
	if err != nil {
		t.Fatalf("parse expires_at: %v", err)
	}
	// Clamped to 24h, so expires_at must be well under now+25h and
	// at/over now+23h.
	if parsed.After(time.Now().Add(25 * time.Hour)) {
		t.Errorf("expires_at %v exceeds clamp window", parsed)
	}
	if parsed.Before(time.Now().Add(23 * time.Hour)) {
		t.Errorf("expires_at %v below expected ~24h clamp", parsed)
	}
}

// ---- Hire: parent_lead_id resolution ----

// covHireSeedLead inserts a LEAD agent in the given crew/workspace and
// returns its id.
func covHireSeedLead(t *testing.T, db *sql.DB, wsID, crewID, id string) string {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`
		INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role, status,
		    cli_adapter, tool_profile, memory_enabled, ephemeral, created_at, updated_at)
		VALUES (?, ?, ?, 'Lead', ?, 'LEAD', 'IDLE',
		        'CLAUDE_CODE', 'CODING', 1, 0, ?, ?)`,
		id, crewID, wsID, id, now, now)
	if err != nil {
		t.Fatalf("seed lead: %v", err)
	}
	return id
}

func TestCovHireParentLeadHappyPath(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)
	leadID := covHireSeedLead(t, db, wsID, crewID, "lead-1")

	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":        crewID,
		"template_slug":  "docs-writer",
		"reason":         "with parent",
		"parent_lead_id": leadID,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	var resp hireResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ParentLeadID == nil || *resp.ParentLeadID != leadID {
		t.Errorf("parent_lead_id = %v, want %q", resp.ParentLeadID, leadID)
	}
	// Persisted on the row.
	var got sql.NullString
	if err := db.QueryRow(`SELECT parent_lead_id FROM agents WHERE id = ?`, resp.ID).Scan(&got); err != nil {
		t.Fatalf("verify row: %v", err)
	}
	if !got.Valid || got.String != leadID {
		t.Errorf("agents.parent_lead_id = %v, want %q", got, leadID)
	}
}

func TestCovHireParentLeadNotFoundIs400(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)

	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":        crewID,
		"template_slug":  "docs-writer",
		"reason":         "bad parent",
		"parent_lead_id": "no-such-lead",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "parent_lead_id") {
		t.Errorf("body = %s; want parent_lead_id message", rr.Body.String())
	}
}

func TestCovHireParentLeadDifferentCrewIs400(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)

	// A second crew in the SAME workspace, with a LEAD belonging to it.
	otherCrew := "crew-hire-2"
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, autonomy_level, behavior_mode, max_ephemeral_agents)
		VALUES (?, ?, 'Other Crew', 'other-crew', 'trusted', 'warn', 5)`, otherCrew, wsID); err != nil {
		t.Fatalf("seed other crew: %v", err)
	}
	leadID := covHireSeedLead(t, db, wsID, otherCrew, "lead-other")

	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":        crewID,
		"template_slug":  "docs-writer",
		"reason":         "cross-crew parent",
		"parent_lead_id": leadID,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "different crew") {
		t.Errorf("body = %s; want different-crew message", rr.Body.String())
	}
}

// ---- Hire: full autonomy = journal-only (no inbox row) ----

func TestCovHireFullAutonomyNoInbox(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "full", 5)
	h := newHireHandler(t, db)

	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":       crewID,
		"template_slug": "docs-writer",
		"reason":        "full autonomy journal-only",
		"model":         "claude-opus-4",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	var resp hireResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.InboxItemID != "" {
		t.Errorf("inbox_item_id = %q on full autonomy; want empty (journal-only)", resp.InboxItemID)
	}
	var inboxCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE source_id = ?`, resp.ID).Scan(&inboxCount); err != nil {
		t.Fatalf("scan inbox count: %v", err)
	}
	if inboxCount != 0 {
		t.Errorf("inbox rows = %d on full autonomy; want 0", inboxCount)
	}
	// Model passed through → llm_provider inferred + llm_model stored.
	var provider, model sql.NullString
	if err := db.QueryRow(`SELECT llm_provider, llm_model FROM agents WHERE id = ?`, resp.ID).Scan(&provider, &model); err != nil {
		t.Fatalf("verify llm cols: %v", err)
	}
	if !provider.Valid || provider.String != "ANTHROPIC" {
		t.Errorf("llm_provider = %v, want ANTHROPIC", provider)
	}
	if !model.Valid || model.String != "claude-opus-4" {
		t.Errorf("llm_model = %v, want claude-opus-4", model)
	}
}

// TestCovHireEmptyModelStoresNull exercises the model=="" branch that
// leaves llm_provider / llm_model NULL.
func TestCovHireEmptyModelStoresNull(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "full", 5)
	h := newHireHandler(t, db)

	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":       crewID,
		"template_slug": "docs-writer",
		"reason":        "no model",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	var resp hireResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var provider, model sql.NullString
	if err := db.QueryRow(`SELECT llm_provider, llm_model FROM agents WHERE id = ?`, resp.ID).Scan(&provider, &model); err != nil {
		t.Fatalf("verify llm cols: %v", err)
	}
	if provider.Valid {
		t.Errorf("llm_provider = %q, want NULL on empty model", provider.String)
	}
	if model.Valid {
		t.Errorf("llm_model = %q, want NULL on empty model", model.String)
	}
}

// ---- Rehire: input-guard branches ----

func TestCovRehireForbiddenForMember(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)
	past := "2024-01-01T00:00:00Z"
	agentID := seedEphemeralAgent(t, db, wsID, crewID, "rh-eph-aaa", &past, &past, "[old] hire: x")

	rr := postRehire(t, h, userID, wsID, "MEMBER", agentID, map[string]any{
		"ttl_minutes": 30,
		"reason":      "rbac",
	})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", rr.Code, rr.Body.String())
	}
}

func TestCovRehireMissingAgentIDIs400(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, _ := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)

	// Empty path value → 400 before any DB lookup.
	rr := postRehire(t, h, userID, wsID, "MANAGER", "", map[string]any{
		"ttl_minutes": 30,
		"reason":      "no id",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
}

func TestCovRehireInvalidJSONIs400(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, _ := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)

	rr := covHirePostRehireRaw(t, h, userID, wsID, "MANAGER", "some-agent", "{broken")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
}

func TestCovRehireMissingReasonIs400(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)
	past := "2024-01-01T00:00:00Z"
	agentID := seedEphemeralAgent(t, db, wsID, crewID, "rh-eph-bbb", &past, &past, "[old] hire: x")

	rr := postRehire(t, h, userID, wsID, "MANAGER", agentID, map[string]any{
		"ttl_minutes": 30,
		"reason":      "   ",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
}

// TestCovRehireTTLClampedToMax exercises the ttl > max clamp on the
// rehire path (the live-rehire variant, no quota math involved).
func TestCovRehireTTLClampedToMax(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)
	// Live ephemeral (expired_at nil) so the quota path is skipped and
	// the UPDATE runs straight through.
	future := time.Now().Add(time.Hour).Format(time.RFC3339)
	agentID := seedEphemeralAgent(t, db, wsID, crewID, "rh-eph-ccc", &future, nil, "[old] hire: live")

	rr := postRehire(t, h, userID, wsID, "MANAGER", agentID, map[string]any{
		"ttl_minutes": 999999,
		"reason":      "extend live with clamp",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var resp hireResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ExpiresAt == nil {
		t.Fatalf("expires_at nil")
	}
	parsed, err := time.Parse(time.RFC3339, *resp.ExpiresAt)
	if err != nil {
		t.Fatalf("parse expires_at: %v", err)
	}
	if parsed.After(time.Now().Add(25 * time.Hour)) {
		t.Errorf("expires_at %v exceeds 24h clamp window", parsed)
	}
}

// TestCovRehireLiveAgentDefaultTTL exercises the ttl<=0 default branch
// on the rehire path and asserts status echoes the persisted column.
func TestCovRehireLiveAgentDefaultTTL(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)
	future := time.Now().Add(time.Hour).Format(time.RFC3339)
	agentID := seedEphemeralAgent(t, db, wsID, crewID, "rh-eph-ddd", &future, nil, "[old] hire: live")
	// Force a distinctive status so the echo-back assertion is meaningful.
	if _, err := db.Exec(`UPDATE agents SET status = 'RUNNING' WHERE id = ?`, agentID); err != nil {
		t.Fatalf("set status: %v", err)
	}

	rr := postRehire(t, h, userID, wsID, "MANAGER", agentID, map[string]any{
		"reason": "default ttl, no ttl_minutes field",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var resp hireResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Status != "RUNNING" {
		t.Errorf("response status = %q, want RUNNING (echoed from row)", resp.Status)
	}
}
