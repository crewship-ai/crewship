package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// runs_cov_test.go covers the remaining RunHandler branches: pure
// helpers, ListRuns 500s, the deep-page Get search, and enrichRuns'
// optional-field arms. Helpers prefixed covRN.

func TestCovRN_StringPtrOrNil(t *testing.T) {
	if stringPtrOrNil("") != nil {
		t.Errorf("empty string must map to nil")
	}
	if p := stringPtrOrNil("x"); p == nil || *p != "x" {
		t.Errorf("got %v, want pointer to x", p)
	}
}

func TestCovRN_FormatRFC3339(t *testing.T) {
	if got := formatRFC3339(time.Time{}); got != "" {
		t.Errorf("zero time = %q, want empty", got)
	}
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got := formatRFC3339(ts); got != "2026-01-02T03:04:05Z" {
		t.Errorf("got %q", got)
	}
}

func TestCovRN_Get_ListDBError(t *testing.T) {
	db := setupTestDB(t)
	h := NewRunHandler(db, newTestLogger())
	db.Close()
	req := httptest.NewRequest("GET", "/api/v1/runs/r1", nil)
	req.SetPathValue("id", "r1")
	req = req.WithContext(withWorkspace(req.Context(), "ws", "OWNER"))
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

func TestCovRN_List_DBError(t *testing.T) {
	db := setupTestDB(t)
	h := NewRunHandler(db, newTestLogger())
	db.Close()
	req := httptest.NewRequest("GET", "/api/v1/runs?page=0&limit=999", nil)
	req = req.WithContext(withWorkspace(req.Context(), "ws", "OWNER"))
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

// covRNSeedRun inserts a single run.started entry with a controlled
// timestamp so ordering across 100+ runs is deterministic.
func covRNSeedRun(t *testing.T, db *sql.DB, wsID, agentID, traceID, ts string) {
	t.Helper()
	execOrFatal(t, db, `INSERT INTO journal_entries
		(id, workspace_id, agent_id, ts, entry_type, severity, actor_type, actor_id,
		 summary, payload, refs, trace_id, span_id, expires_at, priority)
		VALUES (?, ?, ?, ?, 'run.started', 'info', 'sidecar', NULL,
		        'run started', '{"trigger_type":"USER"}', '{}', ?, NULL, NULL, 'normal')`,
		"covrn-je-"+traceID, wsID, agentID, ts, traceID)
}

func TestCovRN_Get_DeepPageSearch(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('covrn-crew', ?, 'C', 'covrn-c')`, wsID)
	seedAgentRow(t, db, "covrn-ag", wsID, "covrn-crew", "RunAgent", "covrn-a", "AGENT")

	// 105 runs; the oldest one lands beyond the first 100-row page.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 105; i++ {
		ts := base.Add(time.Duration(i) * time.Minute).Format("2006-01-02T15:04:05.000Z")
		covRNSeedRun(t, db, wsID, "covrn-ag", fmt.Sprintf("covrn-run-%03d", i), ts)
	}

	h := NewRunHandler(db, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/runs/covrn-run-000", nil)
	req.SetPathValue("id", "covrn-run-000")
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp runResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ID != "covrn-run-000" {
		t.Errorf("id = %q, want covrn-run-000", resp.ID)
	}
	if resp.AgentName == nil || *resp.AgentName != "RunAgent" {
		t.Errorf("agent_name = %v, want RunAgent (enrichment)", resp.AgentName)
	}
}

func TestCovRN_EnrichRuns_Branches(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('covrn-e-crew', ?, 'ECrew', 'covrn-ec')`, wsID)
	seedAgentRow(t, db, "covrn-e-ag1", wsID, "covrn-e-crew", "E1", "covrn-e1", "AGENT")
	seedAgentRow(t, db, "covrn-e-ag2", wsID, "covrn-e-crew", "E2", "covrn-e2", "AGENT")
	h := NewRunHandler(db, newTestLogger())

	if got := h.enrichRuns(context.Background(), wsID, nil); got == nil || len(got) != 0 {
		t.Errorf("nil input = %v, want empty slice", got)
	}

	started := time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC)
	finished := started.Add(time.Minute)
	runs := []journal.RunAggregated{
		{ID: "r1", WorkspaceID: wsID, AgentID: "covrn-e-ag1", ChatID: "chat-1",
			TriggeredBy: "u1", Status: journal.RunStatus("COMPLETED"),
			StartedAt: started, CreatedAt: started, FinishedAt: &finished,
			Metadata: map[string]any{"k": "v"}},
		{ID: "r2", WorkspaceID: wsID, AgentID: "covrn-e-ag2", Status: journal.RunStatus("RUNNING"),
			CreatedAt: started},
		{ID: "r3", WorkspaceID: wsID, AgentID: "", CreatedAt: started},            // agentless: skipped in lookup
		{ID: "r4", WorkspaceID: wsID, AgentID: "covrn-e-ag1", CreatedAt: started}, // dup agent id
	}
	out := h.enrichRuns(context.Background(), wsID, runs)
	if len(out) != 4 {
		t.Fatalf("len = %d, want 4", len(out))
	}
	if out[0].ChatID == nil || *out[0].ChatID != "chat-1" {
		t.Errorf("chat_id = %v", out[0].ChatID)
	}
	if out[0].TriggeredBy == nil || *out[0].TriggeredBy != "u1" {
		t.Errorf("triggered_by = %v", out[0].TriggeredBy)
	}
	if out[0].StartedAt == nil || out[0].FinishedAt == nil {
		t.Errorf("timestamps missing: %+v", out[0])
	}
	if out[0].AgentName == nil || *out[0].AgentName != "E1" {
		t.Errorf("agent name = %v, want E1", out[0].AgentName)
	}
	if out[1].AgentName == nil || *out[1].AgentName != "E2" {
		t.Errorf("agent name = %v, want E2", out[1].AgentName)
	}
	if out[2].AgentName != nil {
		t.Errorf("agentless run must not be enriched, got %v", *out[2].AgentName)
	}
}

func TestCovRN_EnrichRuns_LookupFailureDegrades(t *testing.T) {
	db := setupTestDB(t)
	h := NewRunHandler(db, newTestLogger())
	db.Close()
	runs := []journal.RunAggregated{{ID: "r1", WorkspaceID: "ws", AgentID: "a1", CreatedAt: time.Now()}}
	out := h.enrichRuns(context.Background(), "ws", runs)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1 (lookup failure must not drop runs)", len(out))
	}
	if out[0].AgentName != nil {
		t.Errorf("agent name = %v, want nil on lookup failure", out[0].AgentName)
	}
}
