package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestRunStep_BusyAgentLogsJournalFailuresAndContinues(t *testing.T) {
	r, lock := busyAgentRig(t)
	var logs bytes.Buffer
	r.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	r.journalE = retentionBoundaryEmitter{
		events: make(chan journal.Entry, 2),
		err:    errors.New("journal unavailable"),
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		lock.End("agent_cov")
	}()
	_, err := r.RunStep(t.Context(), busyAgentStep)
	if err == nil || !strings.Contains(err.Error(), "reached agent resolution") {
		t.Fatalf("journal failure changed the step result: %v", err)
	}
	decoder := json.NewDecoder(&logs)
	for _, phase := range []string{"waiting", "acquired"} {
		var record map[string]any
		if err := decoder.Decode(&record); err != nil {
			t.Fatalf("decode %s warning: %v", phase, err)
		}
		if record["level"] != "WARN" || record["run_id"] != busyAgentStep.PipelineRunID ||
			record["step_id"] != busyAgentStep.StepID || record["phase"] != phase ||
			record["error"] != "journal unavailable" {
			t.Fatalf("unexpected %s warning: %v", phase, record)
		}
	}
}

func TestAgentStepCarriesRoutineSlugToWorkerAndGrader(t *testing.T) {
	var agents []string
	exec := retryHarness(t, func(_ context.Context, req AgentStepRequest) (AgentStepResult, error) {
		if req.PipelineSlug != "accounting" {
			t.Errorf("%s received routine slug %q", req.AgentSlug, req.PipelineSlug)
		}
		agents = append(agents, req.AgentSlug)
		if req.AgentSlug == "judge" {
			return AgentStepResult{Output: `{"passed":true,"per_criterion":{"quality":true}}`}, nil
		}
		return AgentStepResult{Output: "classified"}, nil
	})
	step := Step{
		ID: "classify", Type: StepAgentRun, AgentSlug: "worker",
		Outcomes: &Outcomes{GraderAgentSlug: "judge", Criteria: []OutcomeCriterion{{Name: "quality", Rule: "correct"}}},
	}
	_, _, _, err := exec.runAgentStep(t.Context(), step, "Classify", AdapterModel{}, nil,
		RunInput{WorkspaceID: "ws_test"}, "run", "pipeline",
		&pipelineEmitContext{emitter: nopEmitter{}, pipelineSlug: "accounting"})
	if err != nil || strings.Join(agents, ",") != "worker,judge" {
		t.Fatalf("expected worker and grader calls, got %v, error %v", agents, err)
	}
}
