package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

func TestInternalRunFreshInvocationSource(t *testing.T) {
	for _, mode := range []string{"legacy-no-header", "direct-human", "wrong-run", "wrong-agent", "wrong-workspace", "malformed", "autonomous", "inherited-scheduled", "inherited-peer", "recipe-origin-without-source"} {
		t.Run(mode, func(t *testing.T) {
			f := newChatIssueFixture(t)
			h := NewPipelineHandler(f.h.db, newTestLogger(), pipelineAgentRunnerStub{}, nil)
			h.runTokenSecret = "invocation-test-secret"
			h.SetRunStore(pipeline.NewRunStore(f.h.db))
			seedVersionedPipeline(t, f.h.db, f.workspace, "invocation-recipe", "invocation-recipe")
			run := f.run
			if mode == "autonomous" {
				run = "invocation-scheduled"
				seedAutonomousIssueRun(t, f.h, f.workspace, f.agent, run)
			}

			if mode == "inherited-scheduled" || mode == "inherited-peer" || mode == "recipe-origin-without-source" {
				tx, err := f.h.db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				origin, err := runauthority.CaptureOrigin(t.Context(), tx, f.run, f.agent, f.workspace)
				if err != nil {
					tx.Rollback()
					t.Fatal(err)
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
				if mode == "recipe-origin-without-source" {
					execOrFatal(t, f.h.db, `UPDATE pipelines SET chat_origin_id=? WHERE id='invocation-recipe'`, origin.ID)
					execOrFatal(t, f.h.db, `UPDATE pipeline_versions SET chat_origin_id=? WHERE pipeline_id='invocation-recipe'`, origin.ID)
					run = ""
				} else {
					run = "invocation-inherited"
					trigger := "SCHEDULED"
					if mode == "inherited-peer" {
						trigger = "PEER_QUERY"
					}
					host := NewInternalHandler(f.h.db, "host", newTestLogger())
					wireTestJournalForHandler(t, f.h.db, host)
					started := httptest.NewRecorder()
					host.CreateRun(started, httptest.NewRequest("POST", "/api/v1/internal/runs", jsonBody(map[string]any{"id": run, "workspace_id": f.workspace, "agent_id": f.agent, "trigger_type": trigger, "chat_origin_id": origin.ID})))
					if started.Code != 201 {
						t.Fatalf("host inherited start: %d %s", started.Code, started.Body.String())
					}
				}
			}
			body := map[string]any{"workspace_id": f.workspace, "slug": "invocation-recipe", "invoking_crew_id": f.crew, "invoking_agent_id": f.agent, "source_run_id": run}
			req := httptest.NewRequest("POST", "/api/v1/internal/pipelines/run", jsonBody(body))
			if mode != "legacy-no-header" && mode != "recipe-origin-without-source" {
				ws, agent, proofRun := f.workspace, f.agent, run
				switch mode {
				case "wrong-run":
					proofRun = "foreign-run"
				case "wrong-agent":
					agent = "foreign-agent"
				case "wrong-workspace":
					ws = "foreign-workspace"
				}
				proof := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.runTokenSecret, f.workspace, f.crew), ws, agent, proofRun)
				if mode == "malformed" {
					proof = "invalid-credential"
				}
				req.Header.Set("X-Crewship-Run-Token", proof)
			}
			rr := httptest.NewRecorder()
			h.InternalRun(rr, req)
			denied := mode == "wrong-run" || mode == "wrong-agent" || mode == "wrong-workspace" || mode == "malformed"
			if denied {
				if rr.Code != 403 {
					t.Fatalf("invalid invocation accepted: %d %s", rr.Code, rr.Body.String())
				}
				var count int
				if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM pipeline_runs`).Scan(&count); err != nil || count != 0 {
					t.Fatalf("invalid proof left effects: count=%d err=%v", count, err)
				}
				return
			}
			if rr.Code != 200 {
				t.Fatalf("legitimate invocation: %d %s", rr.Code, rr.Body.String())
			}
			var result struct {
				RunID string `json:"run_id"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil || result.RunID == "" {
				t.Fatalf("run receipt %s", rr.Body.String())
			}
			rec, err := h.runStore.Get(t.Context(), result.RunID)
			if err != nil {
				t.Fatal(err)
			}
			expected := ""
			if mode == "direct-human" {
				expected = f.run
			}
			if rec.InvocationSourceRunID != expected {
				t.Fatalf("invocation source %q want %q", rec.InvocationSourceRunID, expected)
			}
			if (mode == "legacy-no-header" || mode == "recipe-origin-without-source") && rec.ChatOriginID == "" {
				t.Fatal("legacy authority provenance was lost")
			}
			// Private correlation never becomes a generic pipeline response field.
			raw, _ := json.Marshal(rec)
			var exposed map[string]any
			_ = json.Unmarshal(raw, &exposed)
			if _, ok := exposed["InvocationSourceRunID"]; ok {
				t.Fatal("invocation provenance exposed in public run record")
			}
		})
	}
}

func TestRecipeOriginScheduledRunHasNoFreshInvocationSource(t *testing.T) {
	f := newChatIssueFixture(t)
	h := NewPipelineHandler(f.h.db, newTestLogger(), pipelineAgentRunnerStub{}, nil)
	h.SetRunStore(pipeline.NewRunStore(f.h.db))
	seedVersionedPipeline(t, f.h.db, f.workspace, "scheduled-origin-recipe", "scheduled-origin-recipe")
	tx, err := f.h.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := runauthority.CaptureOrigin(t.Context(), tx, f.run, f.agent, f.workspace)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, f.h.db, `UPDATE pipelines SET chat_origin_id=? WHERE id='scheduled-origin-recipe'`, origin.ID)
	execOrFatal(t, f.h.db, `UPDATE pipeline_versions SET chat_origin_id=? WHERE pipeline_id='scheduled-origin-recipe'`, origin.ID)
	result, err := h.newExecutor().Run(t.Context(), pipeline.RunInput{WorkspaceID: f.workspace, PipelineID: "scheduled-origin-recipe", InvokingCrewID: f.crew, InvokingAgentID: f.agent, TriggeredVia: pipeline.TriggeredViaSchedule, Mode: pipeline.ModeRun})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := h.runStore.Get(t.Context(), result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.ChatOriginID != origin.ID || rec.InvocationSourceRunID != "" || rec.TriggeredVia != pipeline.TriggeredViaSchedule {
		t.Fatalf("scheduled provenance: origin=%q source=%q trigger=%q", rec.ChatOriginID, rec.InvocationSourceRunID, rec.TriggeredVia)
	}
}
