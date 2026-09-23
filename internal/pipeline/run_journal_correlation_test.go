package pipeline

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Follow the producer through the real writer, persisted journal, and the
// same run_id filter used by Activity. Both runs deliberately use one agent.
func TestRunDefinition_JournalToolEventsStayWithTheirRun(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws_correlation', 'Correlation', 'correlation')`); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	writer := journal.NewWriter(db, logger, journal.WriterOptions{FlushSize: 1})
	defer writer.Close()
	runner := runnerFunc(func(ctx context.Context, req AgentStepRequest) (AgentStepResult, error) {
		_, err := writer.Emit(ctx, journal.Entry{
			WorkspaceID: req.WorkspaceID,
			AgentID:     "same-agent",
			Type:        journal.EntryExecCommand,
			ActorType:   journal.ActorAgent,
			Summary:     "tool call for " + req.PipelineRunID,
		})
		if err != nil {
			return AgentStepResult{}, err
		}
		return AgentStepResult{Output: "ok"}, nil
	})
	exec := NewExecutor(NewStore(db), NewResolver(db), runner, writer)
	dsl := &DSL{DSLVersion: "1.0", Name: "correlation", Steps: []Step{{ID: "a", Type: StepAgentRun, AgentSlug: "same-agent", Prompt: "go"}}}
	ids := []string{"run_parallel_a", "run_parallel_b"}
	var wg sync.WaitGroup
	errCh := make(chan error, len(ids))
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := exec.RunDefinition(context.Background(), dsl, RunInput{WorkspaceID: "ws_correlation", AuthorCrewID: "crew_owner", Mode: ModeRun, RunIDOverride: id})
			errCh <- err
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		entries, _, err := journal.List(context.Background(), db, journal.Query{WorkspaceID: "ws_correlation", RunID: id})
		if err != nil {
			t.Fatal(err)
		}
		toolCalls := 0
		for _, entry := range entries {
			if entry.Type != journal.EntryExecCommand {
				continue
			}
			toolCalls++
			if entry.TraceID != id || !strings.HasSuffix(entry.Summary, id) {
				t.Fatalf("run %s included another run's tool event: %+v", id, entry)
			}
		}
		if toolCalls != 1 {
			t.Fatalf("run %s: want its one tool event, got %d", id, toolCalls)
		}
	}
}
