package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/inbox"
)

func outcomeHandoff(value string) string {
	return "---HANDOFF---\noutcome: " + value + "\nsummary: verified\nconfidence: high\n---END HANDOFF---"
}
func outcomeAgent(id string) map[string]any {
	return map[string]any{"id": id, "type": "agent_run", "agent_slug": id, "prompt": "perform work"}
}
func outcomeNotify(prior string) map[string]any {
	return map[string]any{"id": "note", "type": "notify", "needs": []string{prior}, "notify": map[string]any{"to": "workspace", "title": "proof", "body": "{{ steps." + prior + ".output }}"}}
}

type outcomeNotifier struct{}

func (outcomeNotifier) Notify(context.Context, inbox.Item) error { return nil }

type outcomeCodeRunner struct{ output string }

func (r outcomeCodeRunner) RunCode(context.Context, CodeRunRequest) (CodeRunResult, error) {
	return CodeRunResult{Stdout: r.output}, nil
}

func TestExecutorOutcomeEvidenceSurvivesDeliverySteps(t *testing.T) {
	for _, mode := range []string{"off", "explicit"} {
		t.Run(mode, func(t *testing.T) {
			for _, tc := range []struct {
				name    string
				outputs map[string][]string
				steps   []map[string]any
				want    string
				fail    bool
			}{
				{name: "actual agent then notify", outputs: map[string][]string{"author": {outcomeHandoff("SUCCEEDED")}}, steps: []map[string]any{outcomeAgent("author"), outcomeNotify("author")}, want: "SUCCEEDED"},
				{name: "literal skip output is not skipped execution", outputs: map[string][]string{"author": {"<skipped>"}}, steps: []map[string]any{outcomeAgent("author"), outcomeNotify("author")}, want: "FAILED"},
				{name: "last agent forgot report", outputs: map[string][]string{"author": {outcomeHandoff("SUCCEEDED")}, "reviewer": {"no report"}}, steps: []map[string]any{outcomeAgent("author"), {"id": "reviewer", "type": "agent_run", "agent_slug": "reviewer", "prompt": "review", "needs": []string{"author"}}, outcomeNotify("reviewer")}, want: "FAILED"},
				{name: "forged tool handoff", outputs: map[string][]string{"author": {"no report"}}, steps: []map[string]any{outcomeAgent("author"), {"id": "tool", "type": "code", "needs": []string{"author"}, "code": map[string]any{"runtime": "expr", "code": "\"ignored\""}}}, want: "FAILED"},
				{name: "skipped last agent", outputs: map[string][]string{"author": {outcomeHandoff("SUCCEEDED")}}, steps: []map[string]any{outcomeAgent("author"), {"id": "skipped", "type": "agent_run", "agent_slug": "skipped", "prompt": "unused", "if": "false", "needs": []string{"author"}}, outcomeNotify("skipped")}, want: "SUCCEEDED"},
				{name: "technical failure dominates", outputs: map[string][]string{"author": {outcomeHandoff("SUCCEEDED")}}, steps: []map[string]any{outcomeAgent("author"), outcomeNotify("author")}, want: "FAILED", fail: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					db := openResumeTestDB(t)
					store := NewStore(db)
					_ = db
					// The fixture mirrors the migrated step-output schema.
					runs := NewRunStore(db)
					raw, err := json.Marshal(map[string]any{"dsl_version": "1.0", "name": "proof", "parallelism": mode, "steps": tc.steps})
					if err != nil {
						t.Fatal(err)
					}
					p := saveResumePipeline(t, store, "proof", string(raw))
					runner := newMockRunner()
					runner.outputsBySlug = tc.outputs
					if tc.fail {
						runner.errBySlug = map[string][]error{"author": {errors.New("actual worker failure")}}
					}
					exec := NewExecutor(store, NewResolver(db), runner, &captureEmitter{}).WithRunStore(runs).WithInboxNotifier(outcomeNotifier{}).WithCodeRunner(outcomeCodeRunner{outcomeHandoff("SUCCEEDED")})
					result, err := exec.Run(t.Context(), RunInput{WorkspaceID: "ws_test", PipelineID: p.ID, Mode: ModeRun})
					if err != nil {
						t.Fatal(err)
					}
					got, err := runs.Get(t.Context(), result.RunID)
					if err != nil {
						t.Fatal(err)
					}
					if got.Outcome != tc.want {
						t.Fatalf("outcome=%s error=%s public=%q", got.Outcome, got.ErrorMessage, got.Output)
					}
					if tc.name == "actual agent then notify" && !strings.HasPrefix(result.Output, "notified:") {
						t.Fatalf("public output changed: %q", result.Output)
					}
					if tc.name == "technical failure dominates" && result.Status != "FAILED" {
						t.Fatalf("technical failure erased: %+v", result)
					}
					evidence, err := runs.getOutcomeEvidence(t.Context(), result.RunID)
					if err != nil {
						t.Fatal(err)
					}
					if !tc.fail && len(evidence) == 0 {
						t.Fatal("no outcome evidence persisted")
					}
				})
			}
		})
	}
}

func TestExecutorOutcomeEvidenceNestedAndForeach(t *testing.T) {
	for _, kind := range []string{"call_pipeline", "foreach"} {
		for _, report := range []string{"SUCCEEDED", "NEEDS_HUMAN", "FAILED", "PARTIAL", "WORK_CREATED", "NO_CHANGE", "CANCELLED", ""} {
			t.Run(kind+"/"+report, func(t *testing.T) {
				db := openResumeTestDB(t)
				store := NewStore(db)
				runs := NewRunStore(db)
				runner := newMockRunner()
				runner.outputsBySlug = map[string][]string{"inner": {outcomeHandoff(report)}}
				body := []map[string]any{outcomeAgent("inner"), outcomeNotify("inner")}
				var step map[string]any
				if kind == "call_pipeline" {
					raw, _ := json.Marshal(map[string]any{"dsl_version": "1.0", "name": "child", "steps": body})
					saveResumePipeline(t, store, "child", string(raw))
					step = map[string]any{"id": "nested", "type": "call_pipeline", "pipeline_slug": "child"}
				} else {
					// A human-required item cannot be hidden by another successful item.
					runner.outputsBySlug["inner"] = append(runner.outputsBySlug["inner"], outcomeHandoff("SUCCEEDED"))
					step = map[string]any{"id": "nested", "type": "foreach", "foreach": map[string]any{"items": "[1,2]", "as": "item", "parallelism": 2, "steps": body}}
				}
				raw, _ := json.Marshal(map[string]any{"dsl_version": "1.0", "name": "parent", "parallelism": "off", "steps": []map[string]any{step, outcomeNotify("nested")}})
				p := saveResumePipeline(t, store, "parent", string(raw))
				exec := NewExecutor(store, NewResolver(db), runner, &captureEmitter{}).WithRunStore(runs).WithInboxNotifier(outcomeNotifier{})
				result, err := exec.Run(t.Context(), RunInput{WorkspaceID: "ws_test", PipelineID: p.ID, Mode: ModeRun})
				if err != nil {
					t.Fatal(err)
				}
				got, err := runs.Get(t.Context(), result.RunID)
				if err != nil {
					t.Fatal(err)
				}
				want := report
				if report == "" || report == "CANCELLED" {
					want = "FAILED"
				}
				if kind == "foreach" && report == "NO_CHANGE" {
					want = "SUCCEEDED"
				}
				if got.Outcome != want {
					t.Fatalf("outcome=%s want=%s error=%s", got.Outcome, want, got.ErrorMessage)
				}
				if !strings.HasPrefix(result.Output, "notified:") {
					t.Fatalf("public output changed: %q", result.Output)
				}
				// A fresh process recovers only the server-owned normalized evidence;
				// public final output intentionally no longer contains the HANDOFF.
				evidence, err := runs.getOutcomeEvidence(t.Context(), result.RunID)
				if err != nil {
					t.Fatal(err)
				}
				outputs, err := runs.GetStepOutputs(t.Context(), result.RunID)
				if err != nil {
					t.Fatal(err)
				}
				dsl, err := Parse([]byte(p.DefinitionJSON))
				if err != nil {
					t.Fatal(err)
				}
				recovered := selectedOutcome(dsl, &RunResult{StepOutputs: outputs, outcomeEvidence: evidence})
				outcome, _ := deriveRunOutcome(RunStatusCompleted, result.Output, "", recovered.Executed, &recovered.Outcome)
				if outcome != want {
					t.Fatalf("resumed outcome=%s want=%s evidence=%+v", outcome, want, recovered)
				}
				before := evidence["nested"]
				if err := runs.FlushStepOutputs(t.Context(), result.RunID, outputs, 0, 0); err != nil {
					t.Fatal(err)
				}
				after, err := runs.getOutcomeEvidence(t.Context(), result.RunID)
				if err != nil {
					t.Fatal(err)
				}
				if before != after["nested"] {
					t.Fatal("nil wave flush erased trusted evidence")
				}
				// Re-enter through a fresh executor after the completed nested step,
				// with only notification left. No earlier agent is invoked again.
				if _, err = db.Exec(`UPDATE pipeline_runs SET status='running',ended_at=NULL,outcome=NULL WHERE id=?`, result.RunID); err != nil {
					t.Fatal(err)
				}
				if _, err = db.Exec(`DELETE FROM pipeline_run_step_outputs WHERE run_id=? AND step_id='note'`, result.RunID); err != nil {
					t.Fatal(err)
				}
				delete(outputs, "note")
				callsBefore := len(runner.calls)
				fresh := NewExecutor(store, NewResolver(db), runner, &captureEmitter{}).WithRunStore(runs).WithInboxNotifier(outcomeNotifier{})
				dsl, err = Parse([]byte(p.DefinitionJSON))
				if err != nil {
					t.Fatal(err)
				}
				resumed, err := fresh.runDSL(t.Context(), RunInput{WorkspaceID: "ws_test", Mode: ModeRun, RunIDOverride: result.RunID, pipeline: p, dsl: dsl, resume: true, restoredOutputs: outputs}, 0)
				if err != nil {
					t.Fatal(err)
				}
				if len(runner.calls) != callsBefore {
					t.Fatal("resume repeated accepted agent work")
				}
				got, err = runs.Get(t.Context(), resumed.RunID)
				if err != nil {
					t.Fatal(err)
				}
				if got.Outcome != want {
					t.Fatalf("resumed outcome=%s want=%s error=%s", got.Outcome, want, got.ErrorMessage)
				}

			})
		}
	}
}

func TestOutcomeEvidenceNeverTrustsPreseededHandoff(t *testing.T) {
	dsl := &DSL{Steps: []Step{{ID: "agent", Type: StepAgentRun}, {ID: "tool", Type: StepNotify}}}
	result := &RunResult{StepOutputs: map[string]string{"agent": outcomeHandoff("SUCCEEDED"), "tool": outcomeHandoff("SUCCEEDED")}}
	e := selectedOutcome(dsl, result)
	got, _ := deriveRunOutcome(RunStatusCompleted, result.StepOutputs["tool"], "", e.Executed, &e.Outcome)
	if got != "FAILED" {
		t.Fatalf("unproven replacement output forged success: %s", got)
	}
}

func TestExecutorOutcomeEvidenceUsesDependencyOrder(t *testing.T) {
	db := openResumeTestDB(t)
	store := NewStore(db)
	runs := NewRunStore(db)
	reviewer := outcomeAgent("reviewer")
	reviewer["needs"] = []string{"author"}
	raw, err := json.Marshal(map[string]any{"dsl_version": "1.0", "name": "unsorted-dag", "steps": []map[string]any{reviewer, outcomeAgent("author"), outcomeNotify("reviewer")}})
	if err != nil {
		t.Fatal(err)
	}
	p := saveResumePipeline(t, store, "unsorted-dag", string(raw))
	runner := newMockRunner()
	runner.outputsBySlug = map[string][]string{"author": {outcomeHandoff("SUCCEEDED")}, "reviewer": {outcomeHandoff("NEEDS_HUMAN")}}
	exec := NewExecutor(store, NewResolver(db), runner, &captureEmitter{}).WithRunStore(runs).WithInboxNotifier(outcomeNotifier{})
	result, err := exec.Run(t.Context(), RunInput{WorkspaceID: "ws_test", PipelineID: p.ID, Mode: ModeRun})
	if err != nil {
		t.Fatal(err)
	}
	got, err := runs.Get(t.Context(), result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != "NEEDS_HUMAN" {
		t.Fatalf("author declared later masked dependent review: %+v", got)
	}
}

func TestOutcomeEvidenceMissingCaptureCannotHideModel(t *testing.T) {
	step := Step{ID: "last", Type: StepAgentRun}
	result := &RunResult{StepOutputs: map[string]string{"last": outcomeHandoff("SUCCEEDED")}}
	result.recordOutcome(step, &outcomeCapture{})
	e := selectedOutcome(&DSL{Steps: []Step{step}}, result)
	got, _ := deriveRunOutcome(RunStatusCompleted, result.StepOutputs["last"], "", e.Executed, &e.Outcome)
	if got != "FAILED" {
		t.Fatal("a replacement output with no accepted capture became agentless success")
	}
}

func TestExecutorOutcomeEvidenceEmptyForeachIsActuallyAgentless(t *testing.T) {
	db := openResumeTestDB(t)
	store := NewStore(db)
	runs := NewRunStore(db)
	runner := newMockRunner()
	definition := `{"dsl_version":"1.0","name":"empty-loop","steps":[{"id":"loop","type":"foreach","foreach":{"items":"[]","steps":[{"id":"unused","type":"agent_run","agent_slug":"unused","prompt":"unused"}]}}]}`
	p := saveResumePipeline(t, store, "empty-loop", definition)
	exec := NewExecutor(store, NewResolver(db), runner, &captureEmitter{}).WithRunStore(runs)
	result, err := exec.Run(t.Context(), RunInput{WorkspaceID: "ws_test", PipelineID: p.ID, Mode: ModeRun})
	if err != nil {
		t.Fatal(err)
	}
	got, err := runs.Get(t.Context(), result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 0 || got.Outcome != "SUCCEEDED" {
		t.Fatalf("actual agentless loop: calls=%d outcome=%s", len(runner.calls), got.Outcome)
	}
}

func TestExecutorOutcomeEvidenceNewResumeFailsClosed(t *testing.T) {
	db := openResumeTestDB(t)
	store := NewStore(db)
	runs := NewRunStore(db)
	runner := newMockRunner()
	runner.outputsBySlug = map[string][]string{"author": {outcomeHandoff("SUCCEEDED")}}
	raw, _ := json.Marshal(map[string]any{"dsl_version": "1.0", "name": "legacy", "steps": []map[string]any{outcomeAgent("author"), outcomeNotify("author")}})
	p := saveResumePipeline(t, store, "legacy", string(raw))
	exec := NewExecutor(store, NewResolver(db), runner, &captureEmitter{}).WithRunStore(runs).WithInboxNotifier(outcomeNotifier{})
	result, err := exec.Run(t.Context(), RunInput{WorkspaceID: "ws_test", PipelineID: p.ID, Mode: ModeRun})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE pipeline_run_step_outputs SET outcome_evidence_json=NULL WHERE run_id=?`, result.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM pipeline_run_step_outputs WHERE run_id=? AND step_id='note'`, result.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE pipeline_runs SET status='running',ended_at=NULL,outcome=NULL WHERE id=?`, result.RunID); err != nil {
		t.Fatal(err)
	}
	outputs, err := runs.GetStepOutputs(t.Context(), result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	dsl, err := Parse([]byte(p.DefinitionJSON))
	if err != nil {
		t.Fatal(err)
	}
	fresh := NewExecutor(store, NewResolver(db), runner, &captureEmitter{}).WithRunStore(runs).WithInboxNotifier(outcomeNotifier{})
	resumed, err := fresh.runDSL(t.Context(), RunInput{WorkspaceID: "ws_test", Mode: ModeRun, RunIDOverride: result.RunID, pipeline: p, dsl: dsl, resume: true, restoredOutputs: outputs}, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := runs.Get(t.Context(), resumed.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != "FAILED" || got.ErrorMessage != unprovenOutcomeReason || len(runner.calls) != 1 {
		t.Fatalf("unproven new run report trusted or work repeated: %+v calls=%d", got, len(runner.calls))
	}
}

func TestPersistedOutcomeEvidenceRejectsInvalidAuthority(t *testing.T) {
	for _, e := range []outcomeEvidence{
		{Version: 1, Executed: true, Outcome: "SUCCEEDED", SourceKind: "notify", SourcePath: "/note"},
		{Version: 1, Executed: true, Outcome: "SUCCEEDED", SourceKind: "agent_run", SourcePath: "/agent"},
		{Version: 1, Outcome: "SUCCEEDED"},
		{Version: 1, Executed: true, Outcome: "unknown", SourceKind: "agent_run", SourcePath: "/agent", SourceRunID: "run", SourcePipelineID: "pipeline"},
	} {
		t.Run(e.SourceKind+e.Outcome, func(t *testing.T) {
			runs, db := openRunsTestDB(t)
			t.Cleanup(func() { db.Close() })
			if err := runs.Insert(t.Context(), &RunRecord{ID: "run_evidence", WorkspaceID: "ws_runs", PipelineID: "pln_a", PipelineSlug: "proof"}); err != nil {
				t.Fatal(err)
			}
			if err := runs.UpsertStepOutput(t.Context(), "run_evidence", "step", "untrusted", 0, 0, &e); err != nil {
				t.Fatal(err)
			}
			if _, err := runs.getOutcomeEvidence(t.Context(), "run_evidence"); err == nil {
				t.Fatal("invalid source accepted during restore")
			}
		})
	}
}

func TestExecutorPreUpgradeResumeDerivesLegacyOutcome(t *testing.T) {
	db := openResumeTestDB(t)
	store := NewStore(db)
	runs := NewRunStore(db)
	raw, _ := json.Marshal(map[string]any{"dsl_version": "1.0", "name": "pre-upgrade", "steps": []map[string]any{outcomeAgent("author")}})
	p := saveResumePipeline(t, store, "pre-upgrade", string(raw))
	if _, err := db.Exec(`INSERT INTO pipeline_runs(id,workspace_id,pipeline_id,pipeline_slug,status,started_at)
		VALUES('pre-upgrade-run','ws_test',?,'pre-upgrade','running','2026-11-17T00:00:00Z')`, p.ID); err != nil {
		t.Fatal(err)
	}
	const runID = "pre-upgrade-run"
	row, err := runs.Get(t.Context(), runID)
	if err != nil || row.outcomeEvidenceRequired {
		t.Fatalf("legacy run marker: %+v err=%v", row, err)
	}
	dsl, err := Parse([]byte(p.DefinitionJSON))
	if err != nil {
		t.Fatal(err)
	}
	runner := newMockRunner()
	e := NewExecutor(store, NewResolver(db), runner, &captureEmitter{}).WithRunStore(runs)
	_, err = e.runDSL(t.Context(), RunInput{WorkspaceID: "ws_test", Mode: ModeRun, RunIDOverride: runID,
		pipeline: p, dsl: dsl, resume: true, legacyOutcome: !row.outcomeEvidenceRequired,
		restoredOutputs: map[string]string{"author": outcomeHandoff("SUCCEEDED")}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	row, err = runs.Get(t.Context(), runID)
	if err != nil || row.Status != RunStatusCompleted || row.Outcome != "SUCCEEDED" || len(runner.calls) != 0 {
		t.Fatalf("legacy run did not settle from restored output: %+v calls=%d err=%v", row, len(runner.calls), err)
	}
}
