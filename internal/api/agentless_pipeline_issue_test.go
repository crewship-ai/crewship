package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/policy"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

// Public human-authored recipes can legitimately have no author agent. Exercise
// the actual executor, dispatcher, host authentication and issue mutation.
func TestAgentlessPipelineIssueUpdateAndKeyedReplay(t *testing.T) {
	f := newChatIssueFixture(t)
	created := chatIssueJSON(t, chatIssueCall(t, f.h.Create, "POST", "", map[string]any{"workspace_id": f.workspace, "crew_id": f.crew, "title": "Automation target"}), 201)
	identifier := created["identifier"].(string)
	host := NewInternalHandler(f.h.db, "agentless-host", newTestLogger())
	mux := http.NewServeMux()
	mux.Handle("PATCH /api/v1/internal/issues/{identifier}", host.requireInternal(withHostChatOrigin(http.HandlerFunc(f.h.UpdateStatus))))
	mux.Handle("POST /api/v1/internal/issues", host.requireInternal(withHostChatOrigin(http.HandlerFunc(f.h.Create))))
	server := httptest.NewServer(mux)
	defer server.Close()
	store := pipeline.NewStore(f.h.db)
	now := time.Now()
	definition, _ := json.Marshal(map[string]any{"name": "Automation", "steps": []any{map[string]any{"id": "toggle", "type": "crewship", "action": "issue.update", "args": map[string]any{"identifier": identifier, "status": "IN_PROGRESS"}}}})
	recipe, err := store.Save(t.Context(), pipeline.SaveInput{WorkspaceID: f.workspace, Slug: "agentless", Name: "Automation", DefinitionJSON: string(definition), Author: pipeline.AuthorMeta{CrewID: f.crew, UserID: f.user, Via: pipeline.AuthoredViaUser}, LastTestRunAt: &now, LastTestRunPassed: true})
	if err != nil {
		t.Fatal(err)
	}
	actions := newCrewshipActions(server.URL, "agentless-host", policy.NewResolver(f.h.db), f.h.db, newTestLogger())
	executor := pipeline.NewExecutor(store, pipeline.NewResolver(f.h.db), nil, nil).WithRunStore(pipeline.NewRunStore(f.h.db)).WithCrewshipActions(actions)
	res, err := executor.Run(t.Context(), pipeline.RunInput{PipelineID: recipe.ID, WorkspaceID: f.workspace, Mode: pipeline.ModeRun})
	if err != nil || res.Status != "COMPLETED" {
		t.Fatalf("real agentless DSL: %+v %v", res, err)
	}
	var status string
	if err = f.h.db.QueryRow(`SELECT status FROM missions WHERE id=?`, created["id"]).Scan(&status); err != nil || status != "IN_PROGRESS" {
		t.Fatalf("mutation absent: %s %v", status, err)
	}
	// Simulate a still-running pipeline step retry at the authenticated transport
	// boundary. The previous run remains terminal and must not authorize writes.
	execOrFatal(t, f.h.db, `INSERT INTO pipeline_runs(id,workspace_id,pipeline_id,pipeline_slug,pipeline_version,status,started_at) VALUES('agentless-retry',?,?,?,1,'running','2026-10-13')`, f.workspace, recipe.ID, recipe.Slug)
	call := func(path, marker string, body map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		method := "PATCH"
		if path == "/api/v1/internal/issues" {
			method = "POST"
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("X-Internal-Token", "agentless-host")
		req.Header.Set("X-Crewship-Pipeline-Run", marker)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		return rr
	}
	body := map[string]any{"workspace_id": f.workspace, "author_run_id": "agentless-retry", "operation_key": "stable-agentless-update", "status": "TODO"}
	first := chatIssueJSON(t, call("/api/v1/internal/issues/"+identifier, "agentless-retry", body), 200)
	var before int
	f.h.db.QueryRow(`SELECT COUNT(*) FROM mission_activity WHERE mission_id=?`, created["id"]).Scan(&before)
	second := chatIssueJSON(t, call("/api/v1/internal/issues/"+identifier, "agentless-retry", body), 200)
	if first["operation_id"] == nil || first["operation_id"] != second["operation_id"] || second["replayed"] != true {
		t.Fatalf("unstable system replay: %v %v", first, second)
	}
	var after, refs int
	var actorType, actorID string
	f.h.db.QueryRow(`SELECT COUNT(*) FROM mission_activity WHERE mission_id=?`, created["id"]).Scan(&after)
	f.h.db.QueryRow(`SELECT COUNT(*) FROM chat_entity_refs`).Scan(&refs)
	if err = f.h.db.QueryRow(`SELECT actor_type,actor_id FROM agent_operations WHERE id=?`, first["operation_id"]).Scan(&actorType, &actorID); err != nil {
		t.Fatal(err)
	}
	if before != after || refs != 0 || actorType != "system" || actorID != "pipeline:"+recipe.ID+":crew:"+f.crew {
		t.Fatalf("system scope/audit: %d %d refs=%d actor=%s/%s", before, after, refs, actorType, actorID)
	}

	createBody := map[string]any{"workspace_id": f.workspace, "crew_id": f.crew, "author_run_id": "agentless-retry", "operation_key": "stable-system-create", "title": "System created once"}
	systemCreated := chatIssueJSON(t, call("/api/v1/internal/issues", "agentless-retry", createBody), 201)
	systemReplay := chatIssueJSON(t, call("/api/v1/internal/issues", "agentless-retry", createBody), 200)
	if systemCreated["id"] != systemReplay["id"] || systemReplay["replayed"] != true {
		t.Fatalf("system create duplicated: %v %v", systemCreated, systemReplay)
	}
	changed := map[string]any{"workspace_id": f.workspace, "author_run_id": "agentless-retry", "operation_key": "stable-agentless-update", "status": "IN_PROGRESS"}
	if rr := call("/api/v1/internal/issues/"+identifier, "agentless-retry", changed); rr.Code != 409 {
		t.Fatalf("changed key payload: %d %s", rr.Code, rr.Body.String())
	}
	// A persisted human origin never becomes autonomous just because an
	// agentless recipe or missing transport header is presented.
	tx, err := f.h.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := runauthority.CaptureOrigin(t.Context(), tx, f.run, f.agent, f.workspace)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, f.h.db, `INSERT INTO pipeline_runs(id,workspace_id,pipeline_id,pipeline_slug,pipeline_version,status,started_at,chat_origin_id) VALUES('agentless-human',?,?,?,1,'running','2026-10-13',?)`, f.workspace, recipe.ID, recipe.Slug, origin.ID)
	for _, marker := range []string{"agentless-human", ""} {
		bad := map[string]any{"workspace_id": f.workspace, "crew_id": f.crew, "author_run_id": "agentless-human", "title": "No downgrade"}
		if rr := call("/api/v1/internal/issues", marker, bad); rr.Code != 403 {
			t.Fatalf("human downgrade marker=%q: %d %s", marker, rr.Code, rr.Body.String())
		}
	}
	for _, tc := range []struct{ name, marker, workspace string }{{"missing run", "missing", f.workspace}, {"terminal run", res.RunID, f.workspace}, {"wrong workspace", "agentless-retry", "wrong-workspace"}} {
		t.Run(tc.name, func(t *testing.T) {
			bad := map[string]any{"workspace_id": tc.workspace, "crew_id": f.crew, "author_run_id": tc.marker, "title": "Denied"}
			rr := call("/api/v1/internal/issues", tc.marker, bad)
			if rr.Code != 403 {
				t.Fatalf("%d %s", rr.Code, rr.Body.String())
			}
		})
	}
	// Even the host cannot create in a crew different from the persisted recipe.
	execOrFatal(t, f.h.db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES(?,?,?,?)`, "other-live-crew", f.workspace, "Other", "other-live")
	wrong := map[string]any{"workspace_id": f.workspace, "crew_id": "other-live-crew", "author_run_id": "agentless-retry", "title": "Denied"}
	if rr := call("/api/v1/internal/issues", "agentless-retry", wrong); rr.Code != 403 {
		t.Fatalf("cross-crew create: %d %s", rr.Code, rr.Body.String())
	}
}

func TestAutonomousPipelineAgentRetainsCrossCrewIssueUpdates(t *testing.T) {
	f := newChatIssueFixture(t)
	execOrFatal(t, f.h.db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('automation-other',?,'Other','automation-other')`, f.workspace)
	seedAgentRow(t, f.h.db, "automation-other-agent", f.workspace, "automation-other", "Other", "other", "LEAD")
	seedIssue(t, f.h.db, f.workspace, "automation-other", "automation-other-agent", "OTHER-1", "BACKLOG")
	store := pipeline.NewStore(f.h.db)
	now := time.Now()
	recipe, err := store.Save(t.Context(), pipeline.SaveInput{WorkspaceID: f.workspace, Slug: "cross-crew-automation", Name: "Automation", DefinitionJSON: `{"name":"Automation","steps":[{"id":"update","type":"crewship","action":"issue.update","args":{"identifier":"OTHER-1","status":"TODO"}}]}`, Author: pipeline.AuthorMeta{CrewID: f.crew, AgentID: f.agent, Via: pipeline.AuthoredViaAgent}, LastTestRunAt: &now, LastTestRunPassed: true})
	if err != nil {
		t.Fatal(err)
	}
	host := NewInternalHandler(f.h.db, "cross-crew-host", newTestLogger())
	mux := http.NewServeMux()
	mux.Handle("PATCH /api/v1/internal/issues/{identifier}", host.requireInternal(withHostChatOrigin(http.HandlerFunc(f.h.UpdateStatus))))
	server := httptest.NewServer(mux)
	defer server.Close()
	executor := pipeline.NewExecutor(store, pipeline.NewResolver(f.h.db), nil, nil).WithRunStore(pipeline.NewRunStore(f.h.db)).WithCrewshipActions(newCrewshipActions(server.URL, "cross-crew-host", policy.NewResolver(f.h.db), f.h.db, newTestLogger()))
	result, err := executor.Run(t.Context(), pipeline.RunInput{PipelineID: recipe.ID, WorkspaceID: f.workspace, Mode: pipeline.ModeRun})
	if err != nil || result.Status != "COMPLETED" {
		t.Fatalf("standing automation regression: %+v %v", result, err)
	}
	var status string
	if err = f.h.db.QueryRow(`SELECT status FROM missions WHERE identifier='OTHER-1' AND workspace_id=?`, f.workspace).Scan(&status); err != nil || status != "TODO" {
		t.Fatalf("foreign crew issue not updated: %s %v", status, err)
	}
}
