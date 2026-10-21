package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/policy"
)

func TestCrewshipBodyStripsUnicodeFoldedAuthority(t *testing.T) {
	body := crewshipBody(pipeline.CrewshipRequest{WorkspaceID: "trusted", CrewID: "crew", AgentID: "agent", RunID: "run", Args: map[string]any{"Source_Run_Id": "other", "ſource_agent_id": "other", "WORKSPACE_ID": "foreign", "author_agent_id": "foreign"}})
	encoded, _ := json.Marshal(body)
	var decoded struct {
		Workspace   string `json:"workspace_id"`
		SourceRun   string `json:"source_run_id"`
		SourceAgent string `json:"source_agent_id"`
		Agent       string `json:"author_agent_id"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Workspace != "trusted" || decoded.SourceRun != "" || decoded.SourceAgent != "" || decoded.Agent != "agent" {
		t.Fatalf("authority alias survived: %s", encoded)
	}
}
func TestRoutineScheduleSourceRequiresAuthenticatedRunProof(t *testing.T) {
	f := newChatIssueFixture(t)
	h := NewPipelineHandler(f.h.db, newTestLogger(), nil, nil)
	a := NewRoutineInternalAdapter(h)
	a.SetRunTokenSecret("review-master")
	valid := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey("review-master", f.workspace, f.crew), f.workspace, f.agent, f.run)
	for _, tc := range []struct{ name, token, agent, run string }{{"missing proof", "", f.agent, f.run}, {"wrong actor", valid, "foreign", f.run}, {"wrong run", valid, f.agent, "other-run"}} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/?workspace_id="+f.workspace+"&crew_id="+f.crew, jsonBody(map[string]any{"source_agent_id": tc.agent, "source_run_id": tc.run}))
			req.Header.Set("X-Crewship-Run-Token", tc.token)
			rr := httptest.NewRecorder()
			a.CreateSchedule(rr, req)
			if rr.Code != 403 {
				t.Fatalf("%d %s", rr.Code, rr.Body.String())
			}
		})
	}
}
func TestRoutineScheduleValidSourceProofStoresHumanOrigin(t *testing.T) {
	f := newChatIssueFixture(t)
	h := NewPipelineHandler(f.h.db, newTestLogger(), nil, nil)
	h.SetScheduleStore(pipeline.NewScheduleStore(f.h.db))
	now := time.Now()
	_, err := h.store.Save(t.Context(), pipeline.SaveInput{WorkspaceID: f.workspace, Slug: "proof-target", Name: "Proof", DefinitionJSON: `{"name":"Proof","steps":[]}`, Author: pipeline.AuthorMeta{CrewID: f.crew, AgentID: f.agent, Via: pipeline.AuthoredViaAgent}, LastTestRunAt: &now, LastTestRunPassed: true})
	if err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, f.h.db, `UPDATE crews SET autonomy_level='full' WHERE id=?`, f.crew)
	a := NewRoutineInternalAdapter(h)
	a.SetRunTokenSecret("proof-master")
	a.SetAutonomyGate(policy.NewResolver(f.h.db), nil)
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey("proof-master", f.workspace, f.crew), f.workspace, f.agent, f.run)
	req := httptest.NewRequest("POST", "/?workspace_id="+f.workspace+"&crew_id="+f.crew, jsonBody(map[string]any{"name": "Proof schedule", "target_pipeline_slug": "proof-target", "cron_expr": "0 3 * * *", "timezone": "UTC", "source_run_id": f.run, "source_agent_id": f.agent}))
	req.Header.Set("X-Crewship-Run-Token", token)
	rr := httptest.NewRecorder()
	a.CreateSchedule(rr, req)
	if rr.Code != 201 {
		t.Fatalf("valid source proof: %d %s", rr.Code, rr.Body.String())
	}
	var count int
	if err = f.h.db.QueryRow(`SELECT COUNT(*) FROM pipeline_schedules s JOIN execution_origins o ON o.id=s.chat_origin_id WHERE s.workspace_id=? AND o.root_run_id=?`, f.workspace, f.run).Scan(&count); err != nil || count != 1 {
		t.Fatalf("origin missing count=%d %v", count, err)
	}
}

func TestRoutineOperatorClearOriginIsAuditedCASAndPreservesVersion(t *testing.T) {
	f := newChatIssueFixture(t)
	h := NewPipelineHandler(f.h.db, newTestLogger(), nil, nil)
	store := pipeline.NewStore(f.h.db)
	now := time.Now()
	recipe, err := store.Save(t.Context(), pipeline.SaveInput{WorkspaceID: f.workspace, Slug: "review-origin", Name: "Review", DefinitionJSON: `{"name":"Review","steps":[{"id":"create","type":"crewship","action":"issue.create","args":{"title":"Operator recovered automation"}}]}`, Author: pipeline.AuthorMeta{CrewID: f.crew, AgentID: f.agent, Via: pipeline.AuthoredViaAgent}, SourceRunID: f.run, SourceAgentID: f.agent, LastTestRunAt: &now, LastTestRunPassed: true})
	if err != nil {
		t.Fatal(err)
	}
	var origin string
	if err = f.h.db.QueryRow(`SELECT chat_origin_id FROM pipelines WHERE id=?`, recipe.ID).Scan(&origin); err != nil {
		t.Fatal(err)
	}
	schedules := pipeline.NewScheduleStore(f.h.db)
	schedule, err := schedules.Save(t.Context(), pipeline.SaveScheduleInput{WorkspaceID: f.workspace, Name: "Explicit", TargetPipelineID: recipe.ID, CronExpr: "0 1 * * *", Timezone: "UTC", ChatOriginID: origin})
	if err != nil {
		t.Fatal(err)
	}
	unpinned, err := schedules.Save(t.Context(), pipeline.SaveScheduleInput{WorkspaceID: f.workspace, Name: "Head", TargetPipelineID: recipe.ID, CronExpr: "0 2 * * *", Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	if unpinned.ChatOriginID != "" {
		t.Fatal("unpinned schedule froze old head")
	}
	call := func(body any, scoped bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/", jsonBody(body))
		req.SetPathValue("slug", recipe.Slug)
		ctx := withWorkspace(withUser(req.Context(), &AuthUser{ID: f.user}), f.workspace, "OWNER")
		ctx = context.WithValue(ctx, ctxAuthKind, AuthKindSession)
		if scoped {
			ctx = context.WithValue(ctx, ctxTokenScopes, stringSet{"*": {}})
		}
		rr := httptest.NewRecorder()
		h.ClearOriginBinding(rr, req.WithContext(ctx))
		return rr
	}
	input := map[string]any{"action": "clear", "reason": "Operator replaces departed author grant", "expected_origin_id": origin, "schedules": []any{map[string]any{"id": schedule.ID, "expected_origin_id": origin}}}
	if rr := call(input, true); rr.Code != 403 {
		t.Fatalf("scoped allowed %d", rr.Code)
	}
	if rr := call(map[string]any{"action": "clear", "reason": "missing CAS"}, false); rr.Code != 400 {
		t.Fatalf("missing CAS %d", rr.Code)
	}
	if rr := call(input, false); rr.Code != 200 {
		t.Fatalf("clear %d %s", rr.Code, rr.Body.String())
	}
	if rr := call(input, false); rr.Code != 409 {
		t.Fatalf("stale clear %d %s", rr.Code, rr.Body.String())
	}
	var head, version, scheduled string
	var audits int
	if err = f.h.db.QueryRow(`SELECT COALESCE(chat_origin_id,'') FROM pipelines WHERE id=?`, recipe.ID).Scan(&head); err != nil {
		t.Fatal(err)
	}
	if err = f.h.db.QueryRow(`SELECT chat_origin_id FROM pipeline_versions WHERE pipeline_id=? AND version=1`, recipe.ID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err = f.h.db.QueryRow(`SELECT COALESCE(chat_origin_id,'') FROM pipeline_schedules WHERE id=?`, schedule.ID).Scan(&scheduled); err != nil {
		t.Fatal(err)
	}
	if err = f.h.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='routine.origin_binding.clear' AND user_id=?`, f.user).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if head != "" || scheduled != "" || version != origin || audits != 1 {
		t.Fatalf("clear boundaries head=%s schedule=%s version=%s audit=%d", head, scheduled, version, audits)
	}
	// Unpinned runs record no pinned pipeline_version. Clearing the head
	// therefore restores real writes even while version 1 retains its receipt.
	host := NewInternalHandler(f.h.db, "recovery-host", newTestLogger())
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/internal/issues", host.requireInternal(withHostChatOrigin(http.HandlerFunc(f.h.Create))))
	server := httptest.NewServer(mux)
	defer server.Close()
	execOrFatal(t, f.h.db, `UPDATE users SET suspended_at='2026-10-13' WHERE id=?`, f.user)
	actions := newCrewshipActions(server.URL, "recovery-host", policy.NewResolver(f.h.db), f.h.db, newTestLogger())
	executor := pipeline.NewExecutor(store, pipeline.NewResolver(f.h.db), nil, nil).WithRunStore(pipeline.NewRunStore(f.h.db)).WithCrewshipActions(actions)
	result, err := executor.Run(t.Context(), pipeline.RunInput{PipelineID: recipe.ID, WorkspaceID: f.workspace, Mode: pipeline.ModeRun})
	if err != nil || result.Status != "COMPLETED" {
		t.Fatalf("operator clear did not recover unpinned DSL: %+v %v", result, err)
	}
	var count int
	if err = f.h.db.QueryRow(`SELECT COUNT(*) FROM missions WHERE title='Operator recovered automation'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("recovered write count=%d %v", count, err)
	}
	pinnedVersion := 1
	if _, err = executor.Run(t.Context(), pipeline.RunInput{PipelineID: recipe.ID, WorkspaceID: f.workspace, Mode: pipeline.ModeRun, PinnedVersion: &pinnedVersion}); err == nil {
		t.Fatal("immutable pinned origin cleared")
	}

}

func TestRoutineDraftUnicodeGateAliasesAreRemoved(t *testing.T) {
	fields := map[string]json.RawMessage{"ſkip_test_gate": json.RawMessage(`true`), "SKIP_GOVERNANCE_GATE": json.RawMessage(`true`), "Source_Run_ID": json.RawMessage(`"foreign"`), "name": json.RawMessage(`"safe"`)}
	stripRoutineJSONFields(fields, "skip_test_gate", "skip_governance_gate", "source_run_id")
	if len(fields) != 1 || fields["name"] == nil {
		t.Fatalf("draft gate aliases survived: %+v", fields)
	}
}

func TestCrewshipOperationKeyAliasesCannotOverrideCanonical(t *testing.T) {
	for _, tc := range []struct{ name, origin, canonical, want string }{{"host", "human-origin", "caller-key", "host-step-key"}, {"autonomous-explicit", "", "caller-key", "caller-key"}, {"autonomous-keyless", "", "", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"operation_Key": "forged", "OPERATION_KEY": "forged"}
			if tc.canonical != "" {
				args["operation_key"] = tc.canonical
			}
			body := crewshipBody(pipeline.CrewshipRequest{ChatOriginID: tc.origin, OperationKey: "host-step-key", Args: args})
			raw, _ := json.Marshal(body)
			var decoded struct {
				Key string `json:"operation_key"`
			}
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Key != tc.want {
				t.Fatalf("operation key alias won: %s", raw)
			}
		})
	}
}
