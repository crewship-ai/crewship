package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
)

type runRecordGuardContainer struct {
	mockContainer
	execs atomic.Int64
}

func (c *runRecordGuardContainer) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	c.execs.Add(1)
	return c.mockContainer.Exec(ctx, cfg)
}

func TestTriggerAgent_RunRecordFailurePreventsExecutionAndPreservesReservation(t *testing.T) {
	db := testDB(t)
	seedCrew(t, db, "crew1", "ws1", "Alpha", "alpha")
	seedAgent(t, db, "a1", "bob", "Bob", "crew1", "ws1", "* * * * *", "do work", true)
	const due = "2026-07-06T08:00:00Z"
	setNextRun(t, db, "a1", due)
	s, resolver := newIdemFireScheduler(t, db, func() time.Time {
		return time.Date(2026, 7, 6, 8, 0, 30, 0, time.UTC)
	})
	c := &runRecordGuardContainer{mockContainer: mockContainer{ensureID: "container-123"}}
	s.container = c
	s.orch = orchestrator.New(c, newMemState(), testLogger())
	resolver.createRunErr = errors.New("run write response lost; commit is unknown")
	ag := scheduledAgent{ID: "a1", Slug: "bob", Name: "Bob", CrewID: "crew1", CrewSlug: "alpha", Cron: "* * * * *", Prompt: "do work", Workspace: "ws1"}

	s.triggerAgent(ag)
	if n := c.execs.Load(); n != 0 {
		t.Fatalf("run record creation failed but invoked container exec %d times", n)
	}
	if len(resolver.updatedRuns) != 0 {
		t.Fatal("unstarted run received a terminal update")
	}
	var next string
	var last sql.NullString
	if err := db.QueryRowContext(t.Context(), `SELECT schedule_next_run, schedule_last_run FROM agents WHERE id = 'a1'`).Scan(&next, &last); err != nil {
		t.Fatal(err)
	}
	if next != due || last.Valid {
		t.Fatalf("failed run creation advanced schedule: next=%q last=%v", next, last)
	}
	var reserved string
	if err := db.QueryRowContext(t.Context(), `SELECT run_id FROM pipeline_run_idempotency WHERE workspace_id = 'ws1' AND pipeline_id = 'a1'`).Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if len(resolver.createdRuns) != 1 || reserved != resolver.createdRuns[0].RunID {
		t.Fatal("ambiguous write lost its stable run identity")
	}
	// A repeated tick must not mint a second run after an ambiguous write.
	resolver.createRunErr = nil
	s.triggerAgent(ag)
	if len(resolver.createdRuns) != 1 || c.execs.Load() != 0 {
		t.Fatal("duplicate occurrence retried an ambiguous run write")
	}
	// Positive control: a distinct, recorded occurrence reaches the runtime.
	setNextRun(t, db, "a1", "2026-07-06T08:01:00Z")
	s.triggerAgent(ag)
	if len(resolver.createdRuns) != 2 || c.execs.Load() == 0 {
		t.Fatal("guard prevented a successfully recorded distinct occurrence")
	}
}
