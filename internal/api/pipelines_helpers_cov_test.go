package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// ---------------------------------------------------------------------------
// pipelines.go — coverage for the DB/pure helper methods not exercised by the
// CRUD/exec handler tests: toPipelineResponse, enrichPipelineListAuthorNames,
// enrichPipelineListLinkedIssues, parseRFC3339, lookupAgentSlugs,
// lookupPipelineSlugs, cycleResolver, newExecutor.
//
// SKIPPED (orchestrator/Docker exec branches): none of the helpers here touch
// the executor's RunStep path; newExecutor is covered only for its wiring
// branches (waitpoints/ws/runs/runStore), not for actually executing a run.
//
// Naming: covPH* helpers, TestCovPH* test funcs — distinct from the covPC*
// prefix used by pipelines_crud_cov_test.go.
// ---------------------------------------------------------------------------

// covPHSeedMission inserts a missions row bound to routine (pipeline) routineID
// with the given issue identifier. crew_id / lead_agent_id are seeded as the
// passed IDs (FKs are enforced), trace_id is derived from the mission id.
func covPHSeedMission(t *testing.T, h *PipelineHandler, id, wsID, crewID, agentID, routineID, identifier string) {
	t.Helper()
	if _, err := h.db.Exec(`INSERT INTO missions
		(id, workspace_id, crew_id, lead_agent_id, trace_id, title, routine_id, identifier)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, wsID, crewID, agentID, "trace-"+id, "m "+id, routineID, identifier); err != nil {
		t.Fatalf("seed mission %s: %v", id, err)
	}
}

// --- toPipelineResponse ---

func TestCovPHToPipelineResponse_FullMapping(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	updated := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	invoked := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	p := &pipeline.Pipeline{
		ID:                   "pl_1",
		Slug:                 "deploy",
		Name:                 "Deploy",
		Description:          "ships it",
		DSLVersion:           "v1",
		DefinitionHash:       "abc123",
		Ephemeral:            true,
		WorkspaceVisible:     true,
		InvocationCount:      7,
		LastInvokedAt:        &invoked,
		LastInvocationStatus: "COMPLETED",
		AuthorCrewID:         "crew_1",
		AuthorAgentID:        "agent_1",
		AuthorUserID:         "user_1",
		AuthoredVia:          pipeline.AuthoredViaAgent,
		DefinitionJSON:       `{"name":"deploy","steps":[]}`,
		CreatedAt:            created,
		UpdatedAt:            updated,
	}

	out := toPipelineResponse(p, true)

	if out.ID != "pl_1" || out.Slug != "deploy" || out.Name != "Deploy" {
		t.Errorf("identity fields mismatched: %+v", out)
	}
	if out.Description != "ships it" || out.DSLVersion != "v1" || out.DefinitionHash != "abc123" {
		t.Errorf("descriptive fields mismatched: %+v", out)
	}
	if !out.Ephemeral || !out.WorkspaceVisible || out.InvocationCount != 7 {
		t.Errorf("flags/count mismatched: %+v", out)
	}
	if out.LastInvocationStatus != "COMPLETED" {
		t.Errorf("LastInvocationStatus = %q", out.LastInvocationStatus)
	}
	if out.AuthorCrewID != "crew_1" || out.AuthorAgentID != "agent_1" || out.AuthorUserID != "user_1" {
		t.Errorf("author fields mismatched: %+v", out)
	}
	if out.AuthoredVia != string(pipeline.AuthoredViaAgent) {
		t.Errorf("AuthoredVia = %q, want %q", out.AuthoredVia, pipeline.AuthoredViaAgent)
	}
	if out.LastInvokedAt == nil {
		t.Fatal("LastInvokedAt = nil, want populated pointer")
	}
	if want := invoked.Format("2006-01-02T15:04:05.999999999Z07:00"); *out.LastInvokedAt != want {
		t.Errorf("LastInvokedAt = %q, want %q", *out.LastInvokedAt, want)
	}
	if out.CreatedAt != created.Format("2006-01-02T15:04:05.999999999Z07:00") {
		t.Errorf("CreatedAt = %q", out.CreatedAt)
	}
	if out.UpdatedAt != updated.Format("2006-01-02T15:04:05.999999999Z07:00") {
		t.Errorf("UpdatedAt = %q", out.UpdatedAt)
	}
	if string(out.Definition) != p.DefinitionJSON {
		t.Errorf("Definition = %q, want %q", out.Definition, p.DefinitionJSON)
	}
}

func TestCovPHToPipelineResponse_OmitsDefinitionAndNilInvoked(t *testing.T) {
	p := &pipeline.Pipeline{
		ID:             "pl_2",
		Slug:           "s",
		Name:           "N",
		DefinitionJSON: `{"name":"n","steps":[]}`,
		LastInvokedAt:  nil,
		AuthoredVia:    pipeline.AuthoredViaUser,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	out := toPipelineResponse(p, false)

	if out.Definition != nil {
		t.Errorf("Definition should be omitted when includeDefinition=false; got %q", out.Definition)
	}
	if out.LastInvokedAt != nil {
		t.Errorf("LastInvokedAt should stay nil when source is nil; got %v", out.LastInvokedAt)
	}
	if out.AuthoredVia != string(pipeline.AuthoredViaUser) {
		t.Errorf("AuthoredVia = %q", out.AuthoredVia)
	}
}

// --- enrichPipelineListAuthorNames ---

func TestCovPHEnrichAuthorNames_StitchesNames(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-en", wsID, "Crew", "crew")
	agentID := seedAgentRow(t, db, "agent-en", wsID, crewID, "Eva", "eva", "LEAD")

	rows := []pipelineResponse{
		{ID: "p1", AuthorAgentID: agentID},
		{ID: "p2", AuthorAgentID: ""},           // no author → left empty
		{ID: "p3", AuthorAgentID: "missing-id"}, // unknown agent → left empty
	}

	enrichPipelineListAuthorNames(context.Background(), db, newTestLogger(), rows)

	if rows[0].AuthorAgentName != "Eva" {
		t.Errorf("row0 AuthorAgentName = %q, want Eva", rows[0].AuthorAgentName)
	}
	if rows[1].AuthorAgentName != "" {
		t.Errorf("row1 AuthorAgentName = %q, want empty", rows[1].AuthorAgentName)
	}
	if rows[2].AuthorAgentName != "" {
		t.Errorf("row2 AuthorAgentName = %q, want empty (unknown agent)", rows[2].AuthorAgentName)
	}
}

func TestCovPHEnrichAuthorNames_EmptyAndNoAuthorEarlyReturns(t *testing.T) {
	db := setupTestDB(t)

	// len(rows) == 0 → early return, no panic.
	enrichPipelineListAuthorNames(context.Background(), db, newTestLogger(), nil)

	// rows present but none has an author → idSet empty → early return.
	rows := []pipelineResponse{{ID: "p1"}, {ID: "p2"}}
	enrichPipelineListAuthorNames(context.Background(), db, newTestLogger(), rows)
	for _, r := range rows {
		if r.AuthorAgentName != "" {
			t.Errorf("AuthorAgentName = %q, want empty", r.AuthorAgentName)
		}
	}
}

func TestCovPHEnrichAuthorNames_ExcludesSoftDeletedAgent(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-sd", wsID, "Crew", "crew")
	agentID := seedAgentRow(t, db, "agent-sd", wsID, crewID, "Gone", "gone", "LEAD")
	if _, err := db.Exec(`UPDATE agents SET deleted_at = datetime('now') WHERE id = ?`, agentID); err != nil {
		t.Fatalf("soft-delete agent: %v", err)
	}

	rows := []pipelineResponse{{ID: "p1", AuthorAgentID: agentID}}
	enrichPipelineListAuthorNames(context.Background(), db, newTestLogger(), rows)
	if rows[0].AuthorAgentName != "" {
		t.Errorf("soft-deleted agent name should not surface; got %q", rows[0].AuthorAgentName)
	}
}

// --- enrichPipelineListLinkedIssues ---

func TestCovPHEnrichLinkedIssues_CountsAndTruncates(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-li", wsID, "Crew", "crew")
	agentID := seedAgentRow(t, db, "agent-li", wsID, crewID, "Lead", "lead", "LEAD")
	seedPipelineRow(t, db, wsID, "pl-li", "routine-li")
	h := NewPipelineHandler(db, newTestLogger(), nil, nil)

	// 4 issues bound to the routine — windowed query caps inlined IDs at 3
	// but COUNT must report 4.
	for _, id := range []string{"m1", "m2", "m3", "m4"} {
		covPHSeedMission(t, h, id, wsID, crewID, agentID, "pl-li", "ENG-"+id)
	}
	// A mission with NULL identifier must be excluded from the inlined IDs
	// (WHERE identifier IS NOT NULL) but still counted by the bare COUNT.
	covPHSeedMission(t, h, "m5null", wsID, crewID, agentID, "pl-li", "")
	if _, err := db.Exec(`UPDATE missions SET identifier = NULL WHERE id = 'm5null'`); err != nil {
		t.Fatalf("null identifier: %v", err)
	}

	rows := []pipelineResponse{{ID: "pl-li"}}
	enrichPipelineListLinkedIssues(context.Background(), db, newTestLogger(), wsID, rows)

	if rows[0].LinkedIssueCount != 5 {
		t.Errorf("LinkedIssueCount = %d, want 5 (includes null-identifier mission)", rows[0].LinkedIssueCount)
	}
	if len(rows[0].LinkedIssues) != 3 {
		t.Errorf("LinkedIssues len = %d, want 3 (capped)", len(rows[0].LinkedIssues))
	}
}

func TestCovPHEnrichLinkedIssues_NoBindingsLeavesZero(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedPipelineRow(t, db, wsID, "pl-nb", "routine-nb")

	rows := []pipelineResponse{{ID: "pl-nb"}}
	enrichPipelineListLinkedIssues(context.Background(), db, newTestLogger(), wsID, rows)
	if rows[0].LinkedIssueCount != 0 || rows[0].LinkedIssues != nil {
		t.Errorf("expected zero linked issues; got count=%d ids=%v", rows[0].LinkedIssueCount, rows[0].LinkedIssues)
	}

	// Empty rows → early return.
	enrichPipelineListLinkedIssues(context.Background(), db, newTestLogger(), wsID, nil)
}

// --- parseRFC3339 ---

func TestCovPHParseRFC3339_Cases(t *testing.T) {
	if _, err := parseRFC3339(""); err == nil {
		t.Error("empty string should error")
	}
	if _, err := parseRFC3339("not-a-time"); err == nil {
		t.Error("garbage should error")
	}

	// RFC3339Nano path.
	nano := "2026-01-02T03:04:05.123456789Z"
	tn, err := parseRFC3339(nano)
	if err != nil {
		t.Fatalf("nano parse err: %v", err)
	}
	if tn.Nanosecond() != 123456789 {
		t.Errorf("nano lost: %d", tn.Nanosecond())
	}

	// Plain RFC3339 (no fractional seconds) — falls through to the second
	// branch.
	plain := "2026-01-02T03:04:05Z"
	tp, err := parseRFC3339(plain)
	if err != nil {
		t.Fatalf("plain parse err: %v", err)
	}
	if tp.Year() != 2026 || tp.Second() != 5 {
		t.Errorf("plain parse mismatch: %v", tp)
	}
}

// --- lookupAgentSlugs ---

func TestCovPHLookupAgentSlugs(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-as", wsID, "Crew", "crew")
	seedAgentRow(t, db, "ag-a", wsID, crewID, "Alpha", "alpha", "LEAD")
	seedAgentRow(t, db, "ag-b", wsID, crewID, "Bravo", "bravo", "AGENT")
	delID := seedAgentRow(t, db, "ag-c", wsID, crewID, "Charlie", "charlie", "AGENT")
	if _, err := db.Exec(`UPDATE agents SET deleted_at = datetime('now') WHERE id = ?`, delID); err != nil {
		t.Fatalf("soft-delete: %v", err)
	}
	h := NewPipelineHandler(db, newTestLogger(), nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	// Empty crewID → non-nil empty set, no query.
	empty, err := h.lookupAgentSlugs(req, "")
	if err != nil {
		t.Fatalf("empty crew err: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Errorf("empty crew: want non-nil empty set, got %v", empty)
	}

	got, err := h.lookupAgentSlugs(req, crewID)
	if err != nil {
		t.Fatalf("lookup err: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("want 2 slugs (soft-deleted excluded), got %d: %v", len(got), got)
	}
	if _, ok := got["alpha"]; !ok {
		t.Error("missing alpha")
	}
	if _, ok := got["bravo"]; !ok {
		t.Error("missing bravo")
	}
	if _, ok := got["charlie"]; ok {
		t.Error("soft-deleted charlie should be excluded")
	}
}

// --- lookupPipelineSlugs ---

func TestCovPHLookupPipelineSlugs(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedPipelineRow(t, db, wsID, "pl-x", "alpha-pl")
	seedPipelineRow(t, db, wsID, "pl-y", "bravo-pl")
	seedPipelineRow(t, db, wsID, "pl-z", "gone-pl")
	if _, err := db.Exec(`UPDATE pipelines SET deleted_at = datetime('now') WHERE id = 'pl-z'`); err != nil {
		t.Fatalf("soft-delete pipeline: %v", err)
	}
	h := NewPipelineHandler(db, newTestLogger(), nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	// Empty workspaceID → non-nil empty set.
	empty, err := h.lookupPipelineSlugs(req, "")
	if err != nil {
		t.Fatalf("empty ws err: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Errorf("empty ws: want non-nil empty set, got %v", empty)
	}

	got, err := h.lookupPipelineSlugs(req, wsID)
	if err != nil {
		t.Fatalf("lookup err: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("want 2 slugs (soft-deleted excluded), got %d: %v", len(got), got)
	}
	if _, ok := got["alpha-pl"]; !ok {
		t.Error("missing alpha-pl")
	}
	if _, ok := got["gone-pl"]; ok {
		t.Error("soft-deleted gone-pl should be excluded")
	}
}

// --- cycleResolver ---

func TestCovPHCycleResolver(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedPipelineRow(t, db, wsID, "pl-cr", "resolve-me") // definition_json = {"name":"x","steps":[]}
	h := NewPipelineHandler(db, newTestLogger(), nil, nil)

	resolve := h.cycleResolver(context.Background(), wsID)

	// Known slug → parses the stored DSL.
	dsl, err := resolve("resolve-me")
	if err != nil {
		t.Fatalf("resolve known slug err: %v", err)
	}
	if dsl == nil {
		t.Fatal("resolve known slug returned nil DSL")
	}

	// Unknown slug → error falls through (GetBySlug not-found).
	if _, err := resolve("does-not-exist"); err == nil {
		t.Error("unknown slug should return an error")
	}
}

// --- newExecutor ---

func TestCovPHNewExecutor_WiringBranches(t *testing.T) {
	db := setupTestDB(t)
	h := NewPipelineHandler(db, newTestLogger(), pipelineAgentRunnerStub{}, pipelineEmitterStub{})

	// Baseline: only the db-backed idempotency store branch fires (waitpoints
	// / ws / runs / runStore all nil). Must not panic and must return a
	// non-nil executor.
	if exec := h.newExecutor(); exec == nil {
		t.Fatal("baseline newExecutor returned nil")
	}

	// Wire the optional dependencies so every `if h.X != nil` branch runs.
	h.SetRunRegistry(pipeline.NewRunRegistry())
	h.SetRunStore(pipeline.NewRunStore(db))
	if exec := h.newExecutor(); exec == nil {
		t.Fatal("fully-wired newExecutor returned nil")
	}
}
