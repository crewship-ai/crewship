package api

import (
	"encoding/json"
	"fmt"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/policy"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIssueReviewGate_ActorAndEffectiveStatus(t *testing.T) {
	for _, tc := range []struct {
		name       string
		start      string
		actor      string
		wantStatus string
		legacy     bool
	}{
		{name: "agent enters review", start: "IN_PROGRESS", actor: "agent", wantStatus: "REVIEW"},
		{name: "agent stays in review", start: "REVIEW", actor: "agent", wantStatus: "REVIEW"},
		{name: "internal caller enters review", start: "IN_PROGRESS", actor: "system", wantStatus: "REVIEW"},
		{name: "human accepts", start: "IN_PROGRESS", actor: "human", wantStatus: "DONE"},
		{name: "legacy issue defaults to review", start: "IN_PROGRESS", actor: "agent", wantStatus: "REVIEW", legacy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, user, ws, crew, lead, _ := newTestIssueHandler(t)
			id := seedIssue(t, h.db, ws, crew, lead, "ENG-77", tc.start)
			if tc.legacy {
				execOrFatal(t, h.db, "DELETE FROM issue_work WHERE mission_id=?", id)
			}
			execOrFatal(t, h.db, `INSERT INTO issue_executions
				(id,mission_id,work_revision,brief_revision,stage,reviewer_agent_id,created_at,updated_at)
				VALUES ('execution',?,0,0,'working',?,'2026-11-18','2026-11-18')`, id, lead)
			rec := httptest.NewRecorder()
			if tc.actor == "human" {
				h.Update(rec, covIWReq(user, ws, "OWNER", "PATCH", `{"status":"DONE"}`, crew, "ENG-77"))
			} else {
				agent := ""
				if tc.actor == "agent" {
					agent = lead
				}
				body := fmt.Sprintf(`{"workspace_id":%q,"agent_id":%q,"status":"DONE"}`, ws, agent)
				req := withInternalCrewToken(httptest.NewRequest(http.MethodPatch, "/api/v1/internal/issues/ENG-77", strings.NewReader(body)), ws, crew)
				req.SetPathValue("identifier", "ENG-77")
				NewInternalIssueHandler(h.db, nil, newTestLogger()).UpdateStatus(rec, req)
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var status, execution string
			if err := h.db.QueryRow(`SELECT m.status,e.stage FROM missions m JOIN issue_executions e ON e.mission_id=m.id WHERE m.id=?`, id).Scan(&status, &execution); err != nil {
				t.Fatal(err)
			}
			if status != tc.wantStatus {
				t.Fatalf("issue status=%q want %q (execution=%q, response=%s)", status, tc.wantStatus, execution, rec.Body.String())
			}
			if tc.wantStatus == "REVIEW" {
				var result struct {
					AcceptanceRequired bool   `json:"acceptance_required"`
					Status             string `json:"status"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if execution != "working" || result.Status != "REVIEW" || !result.AcceptanceRequired {
					t.Fatalf("execution=%q response=%s", execution, rec.Body.String())
				}
			}
		})
	}
}

func TestIssueReviewGate_RoutineIssueUpdate(t *testing.T) {
	h, _, ws, crew, lead, _ := newTestIssueHandler(t)
	id := seedIssue(t, h.db, ws, crew, lead, "ENG-81", "IN_PROGRESS")
	execOrFatal(t, h.db, `INSERT INTO issue_executions
		(id,mission_id,work_revision,brief_revision,stage,reviewer_agent_id,created_at,updated_at)
		VALUES ('execution',?,0,0,'working',?,'2026-11-18','2026-11-18')`, id, lead)
	idPipeline := seedTestPipeline(t, h, ws, "review-gate")
	execOrFatal(t, h.db, "UPDATE crews SET autonomy_level='full' WHERE id=?", crew)
	execOrFatal(t, h.db, "UPDATE pipelines SET author_crew_id=?,author_agent_id=? WHERE id=?", crew, lead, idPipeline)
	execOrFatal(t, h.db, `INSERT INTO pipeline_runs(id,workspace_id,pipeline_id,pipeline_slug,status,started_at)
		VALUES ('routine-review-run',?,?,'review-gate','running','2026-11-18T00:00:00Z')`, ws, idPipeline)
	const internalKey = "synthetic-routine-review-key"
	issues := NewInternalIssueHandler(h.db, nil, newTestLogger())
	auth := NewInternalHandler(h.db, internalKey, newTestLogger())
	mux := http.NewServeMux()
	mux.Handle("PATCH /api/v1/internal/issues/{identifier}", auth.requireInternal(withHostChatOrigin(http.HandlerFunc(issues.UpdateStatus))))
	server := httptest.NewServer(mux)
	defer server.Close()
	actions := newCrewshipActions(server.URL, internalKey, policy.NewResolver(h.db), h.db, newTestLogger())
	request := pipeline.CrewshipRequest{
		Verb: "issue.update", Args: map[string]any{"identifier": "ENG-81", "status": "DONE"},
		WorkspaceID: ws, CrewID: crew, AgentID: lead, RunID: "routine-review-run",
		StepID: "done", OperationKey: "routine-review-update",
	}
	for i := 0; i < 2; i++ {
		output, err := actions.Do(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			AcceptanceRequired bool   `json:"acceptance_required"`
			Status             string `json:"status"`
		}
		if err := json.Unmarshal([]byte(output), &result); err != nil || result.Status != "REVIEW" || !result.AcceptanceRequired {
			t.Fatalf("routine result=%s err=%v", output, err)
		}
	}
	var status, stage string
	if err := h.db.QueryRow(`SELECT m.status,e.stage FROM missions m JOIN issue_executions e ON e.mission_id=m.id WHERE m.id=?`, id).Scan(&status, &stage); err != nil {
		t.Fatal(err)
	}
	if status != "REVIEW" || stage != "working" {
		t.Fatalf("stored status=%q execution=%q", status, stage)
	}
}
