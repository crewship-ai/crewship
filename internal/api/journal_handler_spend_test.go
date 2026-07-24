package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// seedCostIncurredRow inserts a cost.incurred journal row directly
// (Spend's ByAgent query source), mirroring seedJournalRow's
// direct-INSERT convention so tests don't need a live Writer.
func seedCostIncurredRow(t *testing.T, h *JournalHandler, id, wsID, crewID, agentID string, costUSD float64, ts time.Time) {
	t.Helper()
	payload := `{"cost_usd":` + jsonFloat(costUSD) + `,"provider":"anthropic","model":"claude-haiku-4-5"}`
	// NULLIF turns an empty crew/agent id into NULL rather than tripping
	// the FK constraint on crews(id)/agents(id) — callers that don't
	// care about the crew/agent grouping (e.g. cross-workspace
	// isolation checks) can pass "" without seeding fixture rows.
	_, err := h.db.ExecContext(context.Background(), `
		INSERT INTO journal_entries (id, workspace_id, crew_id, agent_id, ts, entry_type, severity, priority, actor_type, summary, payload, refs)
		VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, 'cost.incurred', 'info', 'normal', 'system', 'spend', ?, '{}')`,
		id, wsID, crewID, agentID, ts.UTC().Format("2006-01-02T15:04:05.000Z"), payload)
	if err != nil {
		t.Fatalf("seed cost.incurred %s: %v", id, err)
	}
}

func jsonFloat(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func TestJournalHandler_Spend_RequiresWorkspace(t *testing.T) {
	h, _, _, _ := newJournalHandlerTest(t)
	req := httptest.NewRequest("GET", "/api/v1/journal/spend", nil)
	rr := httptest.NewRecorder()
	h.Spend(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestJournalHandler_Spend_RejectsBadWindow(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	req := httptest.NewRequest("GET", "/api/v1/journal/spend?window=3years", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Spend(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestJournalHandler_Spend_RejectsBadTop(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	for _, top := range []string{"0", "-1", "51", "not-a-number"} {
		req := httptest.NewRequest("GET", "/api/v1/journal/spend?top="+top, nil)
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.Spend(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("top=%s: status = %d, want 400", top, rr.Code)
		}
	}
}

func TestJournalHandler_Spend_HappyPath(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	if _, err := h.db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew_a', ?, 'Crew A', 'crew-a')`, wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, status) VALUES ('agent_a', ?, 'crew_a', 'Agent A', 'agent-a', 'IDLE')`, wsID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	now := time.Now().UTC()
	seedCostIncurredRow(t, h, "je-spend-1", wsID, "crew_a", "agent_a", 1.25, now.Add(-1*time.Hour))
	seedCostIncurredRow(t, h, "je-spend-2", wsID, "crew_a", "agent_a", 0.75, now.Add(-2*time.Hour))

	req := httptest.NewRequest("GET", "/api/v1/journal/spend?window=24h", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Spend(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}

	var res journal.SpendResult
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	if res.Window != "24h" {
		t.Errorf("Window = %q, want 24h", res.Window)
	}
	if res.TotalCostUSD < 1.9999 || res.TotalCostUSD > 2.0001 {
		t.Errorf("TotalCostUSD = %v, want 2.0", res.TotalCostUSD)
	}
	if len(res.ByAgent) != 1 || res.ByAgent[0].AgentID != "agent_a" {
		t.Errorf("ByAgent = %+v, want one agent_a bucket", res.ByAgent)
	}
}

// TestJournalHandler_Spend_CrossWorkspaceIsolation confirms a
// workspace's spend never includes another tenant's cost.incurred
// rows — Spend's query is workspace_id-scoped like every other
// journal read.
func TestJournalHandler_Spend_CrossWorkspaceIsolation(t *testing.T) {
	h, userID, wsA, _ := newJournalHandlerTest(t)
	wsB := "ws-spend-foreign"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'F', 'f-spend')`, wsB); err != nil {
		t.Fatalf("seed wsB: %v", err)
	}
	seedCostIncurredRow(t, h, "je-spend-foreign", wsB, "", "", 999.0, time.Now().UTC())

	req := httptest.NewRequest("GET", "/api/v1/journal/spend", nil)
	req = withWorkspaceUser(req, userID, wsA, "OWNER")
	rr := httptest.NewRecorder()
	h.Spend(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var res journal.SpendResult
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.TotalCostUSD != 0 {
		t.Errorf("TotalCostUSD = %v, want 0 (foreign workspace's spend leaked in)", res.TotalCostUSD)
	}
}
