//go:build linux

package api

import (
	"context"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

type publicationCanaryRunner struct{ calls int }

func (r *publicationCanaryRunner) RunStep(context.Context, pipeline.AgentStepRequest) (pipeline.AgentStepResult, error) {
	r.calls++
	return pipeline.AgentStepResult{Output: "synthetic publication canary"}, nil
}

func TestPublicationHeadRepointCannotAuthorizePhysicalBody(t *testing.T) {
	f := newServerPublicationHTTPFixture(t, true)
	store := pipeline.NewStore(f.db)
	runner := &publicationCanaryRunner{}
	executor := pipeline.NewExecutor(store, pipeline.NewResolver(f.db), runner, nil)
	input := pipeline.RunInput{PipelineID: "publish-routine", WorkspaceID: f.workspace, Mode: pipeline.ModeRun}
	if result, err := executor.Run(t.Context(), input); err != nil || result.Status != "COMPLETED" || runner.calls != 1 {
		t.Fatalf("legacy execution calibration: %v %+v calls=%d", err, result, runner.calls)
	}
	var original string
	if err := f.db.QueryRow(`SELECT definition_json FROM pipeline_versions WHERE pipeline_id='publish-routine' AND version=1`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	f.call(f.publisherToken, "POST", f.path+"/publish", f.request(), 200)
	if _, err := store.SaveVersion(t.Context(), pipeline.SaveVersionInput{PipelineID: "publish-routine", DefinitionJSON: original, AuthorType: "user", AuthorID: f.owner}); err != nil {
		t.Fatal(err)
	}
	var head int
	var physical string
	if err := f.db.QueryRow(`SELECT head_version,definition_json FROM pipelines WHERE id='publish-routine'`).Scan(&head, &physical); err != nil || head != 1 || physical != f.definition {
		t.Fatalf("native repoint calibration: head=%d body_matches=%t error=%v", head, physical == f.definition, err)
	}
	runner.calls = 0
	if result, err := executor.Run(t.Context(), input); !errors.Is(err, pipeline.ErrPublicationGenericRun) || runner.calls != 0 {
		t.Errorf("physical published body reached generic runner: result=%+v error=%v calls=%d", result, err, runner.calls)
	}
	_, err := pipeline.NewScheduleStore(f.db).SaveValidated(t.Context(), pipeline.SaveScheduleInput{WorkspaceID: f.workspace, Name: "Must not persist", TargetPipelineID: "publish-routine", CronExpr: "0 9 * * *", Timezone: "UTC", Enabled: false})
	var schedules int
	if e := f.db.QueryRow(`SELECT COUNT(*) FROM pipeline_schedules WHERE target_pipeline_id='publish-routine'`).Scan(&schedules); e != nil {
		t.Fatal(e)
	}
	if !errors.Is(err, pipeline.ErrInvalidTrigger) || schedules != 0 {
		t.Errorf("repoint admitted a published physical trigger: error=%v schedules=%d", err, schedules)
	}
	legacy := 1
	input.PinnedVersion = &legacy
	runner.calls = 0
	if result, err := executor.Run(t.Context(), input); err != nil || result.Status != "COMPLETED" || runner.calls != 1 {
		t.Fatalf("legacy pin calibration after repoint: %v %+v calls=%d", err, result, runner.calls)
	}
	input.PinnedVersion = nil
	if _, err := store.Rollback(t.Context(), "publish-routine", 2); err != nil {
		t.Fatal(err)
	}
	runner.calls = 0
	if _, err := executor.Run(t.Context(), input); !errors.Is(err, pipeline.ErrPublicationGenericRun) || runner.calls != 0 {
		t.Fatalf("bounded rollback lost authority: %v calls=%d", err, runner.calls)
	}
	if _, err := store.Rollback(t.Context(), "publish-routine", 1); err != nil {
		t.Fatal(err)
	}
	if result, err := executor.Run(t.Context(), input); err != nil || result.Status != "COMPLETED" || runner.calls != 1 {
		t.Fatalf("legacy rollback calibration: %v %+v calls=%d", err, result, runner.calls)
	}

}
