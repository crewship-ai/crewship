package api

// Issue #1222: a crew-bound (crwv1) internal token could skip crew
// attribution entirely by OMITTING the crew field. #1202 (#1186) made
// assertBoundCrewWorkspaceDB require an exact crew match for crew-bound
// tokens — but only for non-empty values; empty crew IDs were skipped as
// "optional fields". Four endpoints never require the field, so a
// crew-bound token could land a cost row / journal entry / MCP-tool-call
// audit row / saved pipeline with NO crew attribution, i.e. "own crew, or
// no crew" instead of "own crew" full stop.
//
// The fix injects the token's bound crew at the same chokepoint
// (assertBoundCrewWorkspaceDB) whenever a crew-bound caller omits the
// field, mirroring how requireInternal injects the bound workspace_id.
// These tests prove the invariant end-to-end on each affected endpoint:
// an omitted crew field must attribute the write to the token's OWN crew.
// Workspace-bound (wsv1) and master-token callers keep today's semantics
// (empty stays empty — genuinely optional).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// crewBoundCtx1222 builds a request context as requireInternal would for a
// crwv1 token bound to (wsID, crewID).
func crewBoundCtx1222(wsID, crewID string) context.Context {
	ctx := context.WithValue(context.Background(), ctxInternalTokenWS, wsID)
	return context.WithValue(ctx, ctxInternalTokenCrew, crewID)
}

// TestCrewBoundToken_OmittedCrew_CostRecord_AttributedToBoundCrew drives
// POST /api/v1/internal/cost/record (internal_cost.go) with a crew-bound
// token and NO crew_id in the body. The resulting cost_ledger row must be
// attributed to the token's own crew, not left crew-less.
func TestCrewBoundToken_OmittedCrew_CostRecord_AttributedToBoundCrew(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "c-1222-cost", wsID, "Eng", "eng-1222")
	agentID := seedAgentRow(t, db, "a-1222-cost", wsID, crewID, "Cassie", "cassie-1222", "AGENT")
	r := &Router{db: db, logger: newTestLogger(), journal: &emitRecorder{}}

	body := `{"workspace_id":"` + wsID + `","agent_id":"` + agentID + `",` +
		`"provider":"anthropic","model":"claude-sonnet-4-6","input_tokens":1000,"output_tokens":500}`
	req := httptest.NewRequest("POST", "/api/v1/internal/cost/record",
		strings.NewReader(body)).WithContext(crewBoundCtx1222(wsID, crewID))
	rr := httptest.NewRecorder()
	r.handleSidecarCostRecord(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	var gotCrew string
	if err := db.QueryRow(`SELECT COALESCE(crew_id,'') FROM cost_ledger WHERE workspace_id = ?`, wsID).Scan(&gotCrew); err != nil {
		t.Fatalf("read ledger row: %v", err)
	}
	if gotCrew != crewID {
		t.Errorf("cost_ledger.crew_id = %q, want %q (omitted crew_id must be attributed to the token's bound crew, #1222)", gotCrew, crewID)
	}
}

// TestCrewBoundToken_OmittedCrew_JournalEmit_AttributedToBoundCrew drives
// POST /api/v1/internal/journal/emit (internal_journal.go) with a
// crew-bound token and NO crew_id: the emitted journal entry must carry
// the token's own crew.
func TestCrewBoundToken_OmittedCrew_JournalEmit_AttributedToBoundCrew(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "c-1222-journal", wsID, "Eng", "eng-1222-j")
	em := &emitRecorder{}
	r := &Router{db: db, logger: newTestLogger(), journal: em}

	body := `{"workspace_id":"` + wsID + `","agent_id":"a1","type":"network.egress",` +
		`"summary":"GET api.anthropic.com → 200"}`
	req := httptest.NewRequest("POST", "/api/v1/internal/journal/emit",
		strings.NewReader(body)).WithContext(crewBoundCtx1222(wsID, crewID))
	rr := httptest.NewRecorder()
	r.handleSidecarEmit(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	if len(em.entries) != 1 {
		t.Fatalf("expected 1 entry emitted, got %d", len(em.entries))
	}
	if em.entries[0].CrewID != crewID {
		t.Errorf("journal entry CrewID = %q, want %q (omitted crew_id must be attributed to the token's bound crew, #1222)", em.entries[0].CrewID, crewID)
	}
}

// TestCrewBoundToken_OmittedCrew_MCPToolCall_AttributedToBoundCrew drives
// RecordMCPToolCall (internal_status.go) with a crew-bound token and NO
// crew_id: the mcp_tool_calls audit row must carry the token's own crew.
func TestCrewBoundToken_OmittedCrew_MCPToolCall_AttributedToBoundCrew(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "c-1222-mcp", wsID, "Eng", "eng-1222-m")
	h := NewInternalHandler(db, "tok", testLogger())

	body := `{"workspace_id":"` + wsID + `","agent_id":"a1","mcp_server_id":"srv1",` +
		`"tool_name":"search","status":"success","duration_ms":12}`
	req := httptest.NewRequest("POST", "/api/v1/internal/mcp/tool-calls",
		strings.NewReader(body)).WithContext(crewBoundCtx1222(wsID, crewID))
	rr := httptest.NewRecorder()
	h.RecordMCPToolCall(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var gotCrew string
	if err := db.QueryRow(`SELECT COALESCE(crew_id,'') FROM mcp_tool_calls WHERE workspace_id = ?`, wsID).Scan(&gotCrew); err != nil {
		t.Fatalf("read audit row: %v", err)
	}
	if gotCrew != crewID {
		t.Errorf("mcp_tool_calls.crew_id = %q, want %q (omitted crew_id must be attributed to the token's bound crew, #1222)", gotCrew, crewID)
	}
}

// TestCrewBoundToken_OmittedCrew_PipelineInternalSave_AttributedToBoundCrew
// drives InternalSave (pipelines_crud.go) with a crew-bound token and NO
// author_crew_id. The definition deliberately uses a call_pipeline step —
// an agent_run step would already 422 on the empty-crew slug lookup, which
// would mask the attribution gap. The saved pipeline must be authored by
// the token's own crew.
func TestCrewBoundToken_OmittedCrew_PipelineInternalSave_AttributedToBoundCrew(t *testing.T) {
	h, db, _, wsID, crewID := cov2PCRig(t)

	def := `{"name":"omit-crew-1222","steps":[{"id":"a","type":"call_pipeline","pipeline_slug":"some-other"}]}`
	// #1371: the store gate clears only against a save_token. author_crew_id is
	// omitted here (assertBoundCrewWorkspaceDB fills it from the bound token),
	// so the token's subject binds to that same bound crew.
	token := signInternalSaveTokenForTest([]byte(testSaveTokenSecret1371), wsID, crewID, def)
	body := `{"workspace_id":"` + wsID + `","slug":"omit-crew-1222","name":"omit-crew-1222",` +
		`"definition":` + def + `,"save_token":"` + token + `"}`
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body)).WithContext(crewBoundCtx1222(wsID, crewID))
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var gotCrew string
	if err := db.QueryRow(`SELECT COALESCE(author_crew_id,'') FROM pipelines WHERE workspace_id = ? AND slug = ?`,
		wsID, "omit-crew-1222").Scan(&gotCrew); err != nil {
		t.Fatalf("read pipeline row: %v", err)
	}
	if gotCrew != crewID {
		t.Errorf("pipelines.author_crew_id = %q, want %q (omitted author_crew_id must be attributed to the token's bound crew, #1222)", gotCrew, crewID)
	}
}

// TestUnboundCallers_OmittedCrew_StaysOptional pins the non-crew-bound
// semantics: a workspace-bound (wsv1) or master-token caller omitting the
// crew field keeps today's behaviour — the write lands with no crew
// attribution (genuinely optional field), no injection, no rejection.
func TestUnboundCallers_OmittedCrew_StaysOptional(t *testing.T) {
	tests := []struct {
		name string
		ctx  func(wsID string) context.Context
	}{
		{"workspace_bound_token", func(wsID string) context.Context {
			return context.WithValue(context.Background(), ctxInternalTokenWS, wsID)
		}},
		{"master_token", func(string) context.Context { return context.Background() }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := setupTestDB(t)
			userID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, userID)
			em := &emitRecorder{}
			r := &Router{db: db, logger: newTestLogger(), journal: em}

			body := `{"workspace_id":"` + wsID + `","agent_id":"a1","type":"network.egress","summary":"s"}`
			req := httptest.NewRequest("POST", "/api/v1/internal/journal/emit",
				strings.NewReader(body)).WithContext(tt.ctx(wsID))
			rr := httptest.NewRecorder()
			r.handleSidecarEmit(rr, req)

			if rr.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
			}
			if len(em.entries) != 1 {
				t.Fatalf("expected 1 entry emitted, got %d", len(em.entries))
			}
			if em.entries[0].CrewID != "" {
				t.Errorf("CrewID = %q, want empty (crew stays optional for non-crew-bound callers)", em.entries[0].CrewID)
			}
		})
	}
}
