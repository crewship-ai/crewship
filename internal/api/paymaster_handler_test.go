package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// paymaster_handler.go — cost/spend read endpoints.
//
// Critical contract: workspace isolation. Each handler must 404 on
// cross-tenant crew/mission IDs (per source comment, "no existence leak").
// Tests exercise the HTTP surface; the underlying paymaster.* aggregations
// have their own unit tests in internal/paymaster/paymaster_test.go.
// ---------------------------------------------------------------------------

// ledgerRow is a small helper to keep the test setup tidy.
type ledgerRow struct {
	id, wsID, crewID, agentID, missionID, provider, model string
	cost                                                  float64
	inTok, outTok                                         int
	ts                                                    time.Time
}

func insertLedger(t *testing.T, h *PaymasterHandler, rows ...ledgerRow) {
	t.Helper()
	for _, r := range rows {
		_, err := h.db.Exec(`INSERT INTO cost_ledger
			(id, workspace_id, crew_id, agent_id, mission_id, ts, provider, model,
			 input_tokens, output_tokens, cost_usd)
			VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?, ?)`,
			r.id, r.wsID, r.crewID, r.agentID, r.missionID,
			r.ts.UTC().Format(time.RFC3339), r.provider, r.model,
			r.inTok, r.outTok, r.cost)
		if err != nil {
			t.Fatalf("insert cost_ledger %s: %v", r.id, err)
		}
	}
}

func newPaymasterTestHandler(t *testing.T) *PaymasterHandler {
	t.Helper()
	db := setupTestDB(t)
	return NewPaymasterHandler(db, newTestLogger())
}

// ---- SpendByCrew ----

func TestPaymaster_SpendByCrew_NoWorkspace_401(t *testing.T) {
	h := newPaymasterTestHandler(t)
	req := httptest.NewRequest("GET", "/api/v1/paymaster/spend/by-crew", nil)
	rr := httptest.NewRecorder()
	h.SpendByCrew(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestPaymaster_SpendByCrew_Happy_AggregatesInWindow(t *testing.T) {
	h := newPaymasterTestHandler(t)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	seedCrewRow(t, h.db, "crew-a", wsID, "A", "alpha")
	seedCrewRow(t, h.db, "crew-b", wsID, "B", "beta")
	// Other workspace's crew + ledger row — MUST be excluded.
	otherWS := "ws-foreign"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'F', 'f')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	seedCrewRow(t, h.db, "crew-foreign", otherWS, "F", "foreign")

	now := time.Now().UTC()
	insertLedger(t, h,
		ledgerRow{id: "l1", wsID: wsID, crewID: "crew-a", provider: "anthropic", model: "claude", cost: 1.50, inTok: 100, outTok: 50, ts: now.Add(-1 * time.Hour)},
		ledgerRow{id: "l2", wsID: wsID, crewID: "crew-a", provider: "anthropic", model: "claude", cost: 2.50, inTok: 200, outTok: 80, ts: now.Add(-30 * time.Minute)},
		ledgerRow{id: "l3", wsID: wsID, crewID: "crew-b", provider: "openai", model: "gpt-4", cost: 0.75, inTok: 30, outTok: 10, ts: now.Add(-2 * time.Hour)},
		// Foreign tenant's row — must not leak.
		ledgerRow{id: "l4", wsID: otherWS, crewID: "crew-foreign", provider: "anthropic", model: "claude", cost: 99, inTok: 1, outTok: 1, ts: now.Add(-1 * time.Hour)},
	)

	req := httptest.NewRequest("GET", "/api/v1/paymaster/spend/by-crew", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.SpendByCrew(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Rows  []map[string]any `json:"rows"`
		Since string           `json:"since"`
		Until string           `json:"until"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Rows) != 2 {
		t.Fatalf("rows = %d, want 2 (crew-a + crew-b; foreign workspace excluded)", len(body.Rows))
	}
	// Sort is DESC by cost — crew-a (4.00) before crew-b (0.75).
	if body.Rows[0]["crew_id"] != "crew-a" {
		t.Errorf("first row crew_id = %v, want crew-a (DESC by cost)", body.Rows[0]["crew_id"])
	}
	if body.Rows[0]["cost_usd"].(float64) != 4.0 {
		t.Errorf("crew-a cost = %v, want 4.00", body.Rows[0]["cost_usd"])
	}
	// Foreign row must not appear.
	for _, row := range body.Rows {
		if row["crew_id"] == "crew-foreign" {
			t.Errorf("foreign-tenant row leaked: %+v", row)
		}
	}
}

// ---- SpendByAgent ----

func TestPaymaster_SpendByAgent_NoWorkspace_401(t *testing.T) {
	h := newPaymasterTestHandler(t)
	req := httptest.NewRequest("GET", "/api/v1/paymaster/spend/by-agent/crew-x", nil)
	req.SetPathValue("crewId", "crew-x")
	rr := httptest.NewRecorder()
	h.SpendByAgent(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestPaymaster_SpendByAgent_MissingCrewID_400(t *testing.T) {
	h := newPaymasterTestHandler(t)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	req := httptest.NewRequest("GET", "/api/v1/paymaster/spend/by-agent/", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.SpendByAgent(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestPaymaster_SpendByAgent_CrossWorkspace_404(t *testing.T) {
	h := newPaymasterTestHandler(t)
	userID := seedTestUser(t, h.db)
	wsA := seedTestWorkspace(t, h.db, userID)

	wsB := "ws-b"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'B', 'b')`, wsB); err != nil {
		t.Fatalf("seed wsB: %v", err)
	}
	seedCrewRow(t, h.db, "crew-foreign", wsB, "B", "beta")

	req := httptest.NewRequest("GET", "/api/v1/paymaster/spend/by-agent/crew-foreign", nil)
	req.SetPathValue("crewId", "crew-foreign")
	req = withWorkspaceUser(req, userID, wsA, "OWNER")
	rr := httptest.NewRecorder()
	h.SpendByAgent(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-workspace status = %d, want 404 (flat, no existence leak)", rr.Code)
	}
	// Body should not reveal the cross-tenant existence — "crew not found"
	if !strings.Contains(rr.Body.String(), "not found") {
		t.Errorf("body = %s, want 'not found'", rr.Body.String())
	}
}

func TestPaymaster_SpendByAgent_Happy(t *testing.T) {
	h := newPaymasterTestHandler(t)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	seedCrewRow(t, h.db, "crew-a", wsID, "A", "alpha")
	seedAgentRow(t, h.db, "ag-1", wsID, "crew-a", "Agent One", "a1", "AGENT")
	seedAgentRow(t, h.db, "ag-2", wsID, "crew-a", "Agent Two", "a2", "AGENT")

	now := time.Now().UTC()
	insertLedger(t, h,
		ledgerRow{id: "l1", wsID: wsID, crewID: "crew-a", agentID: "ag-1", provider: "p", model: "m", cost: 3.0, ts: now.Add(-1 * time.Hour)},
		ledgerRow{id: "l2", wsID: wsID, crewID: "crew-a", agentID: "ag-2", provider: "p", model: "m", cost: 1.0, ts: now.Add(-30 * time.Minute)},
	)

	req := httptest.NewRequest("GET", "/api/v1/paymaster/spend/by-agent/crew-a", nil)
	req.SetPathValue("crewId", "crew-a")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.SpendByAgent(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Rows   []map[string]any `json:"rows"`
		CrewID string           `json:"crew_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rr.Body.String())
	}
	if body.CrewID != "crew-a" {
		t.Errorf("crew_id = %s, want crew-a", body.CrewID)
	}
	if len(body.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(body.Rows))
	}
}

// ---- SpendByMission ----

func TestPaymaster_SpendByMission_NoWorkspace_401(t *testing.T) {
	h := newPaymasterTestHandler(t)
	req := httptest.NewRequest("GET", "/api/v1/paymaster/spend/by-mission/m1", nil)
	req.SetPathValue("missionId", "m1")
	rr := httptest.NewRecorder()
	h.SpendByMission(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestPaymaster_SpendByMission_MissingMissionID_400(t *testing.T) {
	h := newPaymasterTestHandler(t)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	req := httptest.NewRequest("GET", "/api/v1/paymaster/spend/by-mission/", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.SpendByMission(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestPaymaster_SpendByMission_CrossWorkspace_404(t *testing.T) {
	h := newPaymasterTestHandler(t)
	userID := seedTestUser(t, h.db)
	wsA := seedTestWorkspace(t, h.db, userID)
	wsB := "ws-b2"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'B', 'b2')`, wsB); err != nil {
		t.Fatalf("seed wsB: %v", err)
	}
	// Seed mission in wsB; caller queries from wsA. Reuses the
	// FK-aware seedMissionRow helper from eval_handler_test.go.
	seedCrewRow(t, h.db, "crew-foreign-pm", wsB, "F", "f-pm")
	seedMissionRow(t, h.db, "mission-x", wsB, "crew-foreign-pm", "Mission X")

	req := httptest.NewRequest("GET", "/api/v1/paymaster/spend/by-mission/mission-x", nil)
	req.SetPathValue("missionId", "mission-x")
	req = withWorkspaceUser(req, userID, wsA, "OWNER")
	rr := httptest.NewRecorder()
	h.SpendByMission(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-workspace status = %d, want 404", rr.Code)
	}
}

func TestPaymaster_SpendByMission_Happy(t *testing.T) {
	h := newPaymasterTestHandler(t)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	seedCrewRow(t, h.db, "crew-y", wsID, "Y", "y")
	seedMissionRow(t, h.db, "mission-y", wsID, "crew-y", "Mission Y")
	now := time.Now().UTC()
	insertLedger(t, h,
		ledgerRow{id: "l1", wsID: wsID, missionID: "mission-y", provider: "p", model: "m", cost: 5.0, inTok: 100, outTok: 50, ts: now.Add(-1 * time.Hour)},
		ledgerRow{id: "l2", wsID: wsID, missionID: "mission-y", provider: "p", model: "m", cost: 7.0, inTok: 200, outTok: 80, ts: now.Add(-30 * time.Minute)},
	)

	req := httptest.NewRequest("GET", "/api/v1/paymaster/spend/by-mission/mission-y", nil)
	req.SetPathValue("missionId", "mission-y")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.SpendByMission(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

// ---- TopSpenders ----

func TestPaymaster_TopSpenders_NoWorkspace_401(t *testing.T) {
	h := newPaymasterTestHandler(t)
	req := httptest.NewRequest("GET", "/api/v1/paymaster/top-spenders", nil)
	rr := httptest.NewRecorder()
	h.TopSpenders(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestPaymaster_TopSpenders_LimitClamping(t *testing.T) {
	h := newPaymasterTestHandler(t)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)

	cases := []struct {
		name       string
		limitParam string
		want       int
	}{
		{"default", "", 10},
		{"valid-5", "5", 5},
		{"valid-max-100", "100", 100},
		{"zero-falls-back-to-default", "0", 10},
		{"negative-falls-back", "-3", 10},
		{"too-high-falls-back", "101", 10},
		{"non-numeric-falls-back", "abc", 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := "/api/v1/paymaster/top-spenders"
			if tc.limitParam != "" {
				url += "?limit=" + tc.limitParam
			}
			req := httptest.NewRequest("GET", url, nil)
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.TopSpenders(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d", rr.Code)
			}
			var body struct {
				Limit int `json:"limit"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("%s: decode response: %v body=%s", tc.name, err, rr.Body.String())
			}
			if body.Limit != tc.want {
				t.Errorf("%s: limit = %d, want %d", tc.name, body.Limit, tc.want)
			}
		})
	}
}

// ---- SubscriptionUsage ----

func TestPaymaster_SubscriptionUsage_NoWorkspace_401(t *testing.T) {
	h := newPaymasterTestHandler(t)
	req := httptest.NewRequest("GET", "/api/v1/paymaster/subscriptions", nil)
	rr := httptest.NewRecorder()
	h.SubscriptionUsage(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestPaymaster_SubscriptionUsage_EmptyHappyPath(t *testing.T) {
	h := newPaymasterTestHandler(t)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	req := httptest.NewRequest("GET", "/api/v1/paymaster/subscriptions", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.SubscriptionUsage(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	// Empty workspace must still return a window — since/until present.
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rr.Body.String())
	}
	if _, ok := body["since"]; !ok {
		t.Error("response missing 'since'")
	}
	if _, ok := body["until"]; !ok {
		t.Error("response missing 'until'")
	}
}

// ---- parseWindow ----

func TestParseWindow_Defaults(t *testing.T) {
	req := httptest.NewRequest("GET", "/x", nil)
	since, until := parseWindow(req)
	if d := until.Sub(since); d < 6*24*time.Hour || d > 8*24*time.Hour {
		t.Errorf("default window = %v, want ~7d", d)
	}
}

func TestParseWindow_RangePresets(t *testing.T) {
	for _, tc := range []struct {
		name     string
		param    string
		minDelta time.Duration
		maxDelta time.Duration
	}{
		{"1h", "1h", 59 * time.Minute, 61 * time.Minute},
		{"24h", "24h", 23 * time.Hour, 25 * time.Hour},
		{"7d", "7d", 6 * 24 * time.Hour, 8 * 24 * time.Hour},
		{"30d", "30d", 29 * 24 * time.Hour, 31 * 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/x?range="+tc.param, nil)
			since, until := parseWindow(req)
			d := until.Sub(since)
			if d < tc.minDelta || d > tc.maxDelta {
				t.Errorf("range=%s → %v, want %v..%v", tc.param, d, tc.minDelta, tc.maxDelta)
			}
		})
	}
}

func TestParseWindow_ExplicitSinceUntil_OverridesDefault(t *testing.T) {
	since := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2024, 1, 5, 0, 0, 0, 0, time.UTC)
	req := httptest.NewRequest("GET", "/x?since="+since.Format(time.RFC3339)+"&until="+until.Format(time.RFC3339), nil)
	gotSince, gotUntil := parseWindow(req)
	if !gotSince.Equal(since) {
		t.Errorf("since = %v, want %v", gotSince, since)
	}
	if !gotUntil.Equal(until) {
		t.Errorf("until = %v, want %v", gotUntil, until)
	}
}

func TestParseWindow_InvalidValuesAreIgnored(t *testing.T) {
	// Bogus since/until must NOT cause an error — they silently fall back
	// to the defaults so a typo in the URL doesn't break the dashboard.
	req := httptest.NewRequest("GET", "/x?since=not-a-date&until=also-not-a-date&range=bogus", nil)
	since, until := parseWindow(req)
	if d := until.Sub(since); d < 6*24*time.Hour || d > 8*24*time.Hour {
		t.Errorf("invalid values should fall back to 7d default, got %v", d)
	}
}

// ---- crewBelongsToWorkspace / missionBelongsToWorkspace ----

func TestCrewBelongsToWorkspace(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-here", wsID, "Here", "here")
	otherWS := "ws-z"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Z', 'z')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	seedCrewRow(t, db, "crew-other", otherWS, "Other", "other")

	if ok, err := crewBelongsToWorkspace(context.Background(), db, "crew-here", wsID); err != nil || !ok {
		t.Errorf("own crew: ok=%v err=%v, want true,nil", ok, err)
	}
	if ok, err := crewBelongsToWorkspace(context.Background(), db, "crew-other", wsID); err != nil || ok {
		t.Errorf("cross-workspace crew: ok=%v err=%v, want false,nil", ok, err)
	}
	if ok, err := crewBelongsToWorkspace(context.Background(), db, "does-not-exist", wsID); err != nil || ok {
		t.Errorf("missing crew: ok=%v err=%v, want false,nil", ok, err)
	}
}

func TestMissionBelongsToWorkspace(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	// Inline the mission seed (no PaymasterHandler in scope) — same FK chain
	// as seedMissionRow above.
	seedCrewRow(t, db, "crew-here", wsID, "Here", "here")
	seedAgentRow(t, db, "agent-here", wsID, "crew-here", "AH", "ah", "AGENT")
	if _, err := db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at)
		VALUES ('m-here', ?, 'crew-here', 'agent-here', 'trace-here', 'mh', 'PLANNING', datetime('now'))`, wsID); err != nil {
		t.Fatalf("seed mission: %v", err)
	}
	otherWS := "ws-zz"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'ZZ', 'zz')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	seedCrewRow(t, db, "crew-other", otherWS, "Other", "other")
	seedAgentRow(t, db, "agent-other", otherWS, "crew-other", "AO", "ao", "AGENT")
	if _, err := db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at)
		VALUES ('m-other', ?, 'crew-other', 'agent-other', 'trace-other', 'mo', 'PLANNING', datetime('now'))`, otherWS); err != nil {
		t.Fatalf("seed other mission: %v", err)
	}

	if ok, err := missionBelongsToWorkspace(context.Background(), db, "m-here", wsID); err != nil || !ok {
		t.Errorf("own mission: ok=%v err=%v, want true,nil", ok, err)
	}
	if ok, err := missionBelongsToWorkspace(context.Background(), db, "m-other", wsID); err != nil || ok {
		t.Errorf("cross-workspace mission: ok=%v err=%v, want false,nil", ok, err)
	}
	if ok, err := missionBelongsToWorkspace(context.Background(), db, "missing", wsID); err != nil || ok {
		t.Errorf("missing mission: ok=%v err=%v, want false,nil", ok, err)
	}
}
