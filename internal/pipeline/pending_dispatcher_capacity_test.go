package pipeline

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

type capacityRetryExecutor struct {
	inputs []RunInput
	run    func(RunInput) (*RunResult, error)
}

func (e *capacityRetryExecutor) Run(_ context.Context, in RunInput) (*RunResult, error) {
	e.inputs = append(e.inputs, in)
	return e.run(in)
}

func TestPendingDispatcherCapacityRetryPreservesOccurrence(t *testing.T) {
	s := NewPendingRunStore(newPendingDB(t))
	pin := 3
	expires := time.Now().Add(time.Hour)
	pr := PendingRun{
		ID: "original", WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s",
		InputsJSON: `{"tenant":"a"}`, MetadataJSON: `{"phase":"start"}`, TagsJSON: `["batch"]`,
		DebounceKey: "tenant-a", FireAt: time.Now().Add(-time.Minute), ExpiresAt: &expires,
		PinnedVersion: &pin, InvokingUserID: "operator", InvocationAuthority: RoutineRunAuthority,
	}
	if _, _, err := s.Enqueue(t.Context(), pr); err != nil {
		t.Fatal(err)
	}
	e := &capacityRetryExecutor{}
	e.run = func(RunInput) (*RunResult, error) {
		// A new debounce window is allowed as soon as the first occurrence
		// was claimed. The retry must neither collide with nor overwrite it.
		newer := pr
		newer.ID = "newer"
		newer.InputsJSON = `{"tenant":"b"}`
		if _, coalesced, err := s.Enqueue(t.Context(), newer); err != nil || coalesced {
			t.Fatalf("enqueue new window: coalesced=%v err=%v", coalesced, err)
		}
		return nil, fmt.Errorf("capacity: %w", ErrConcurrencyLimitReached)
	}
	d := NewPendingRunDispatcher(s, e, nil)
	d.fireOne(t.Context(), pr)
	due, err := s.DueRuns(t.Context(), time.Now(), 25)
	if err != nil || len(due) != 2 {
		t.Fatalf("due after rejection: count=%d err=%v", len(due), err)
	}
	var retry PendingRun
	for _, row := range due {
		if row.ID == pr.ID {
			retry = row
		}
	}
	if retry.InputsJSON != pr.InputsJSON || retry.MetadataJSON != pr.MetadataJSON || retry.TagsJSON != pr.TagsJSON ||
		retry.InvokingUserID != pr.InvokingUserID || retry.InvocationAuthority != pr.InvocationAuthority ||
		retry.PinnedVersion == nil || *retry.PinnedVersion != pin || !retry.FireAt.Equal(pr.FireAt) {
		t.Fatalf("retry changed occurrence: %+v", retry)
	}
	var expiry, debounce string
	if err := s.db.QueryRowContext(t.Context(), `SELECT expires_at, COALESCE(debounce_key,'') FROM pending_runs WHERE id=?`, pr.ID).Scan(&expiry, &debounce); err != nil {
		t.Fatal(err)
	}
	if expiry != formatRFC3339(expires) || debounce != "" {
		t.Fatalf("retry expiry=%q debounce=%q", expiry, debounce)
	}
	e.run = func(RunInput) (*RunResult, error) { return &RunResult{RunID: "accepted", Status: "COMPLETED"}, nil }
	d.fireOne(t.Context(), retry)
	if len(e.inputs) != 2 || e.inputs[0].IdempotencyKey != e.inputs[1].IdempotencyKey {
		t.Fatalf("retry lost occurrence idempotency: %+v", e.inputs)
	}
	var status, runID string
	if err := s.db.QueryRowContext(t.Context(), `SELECT status,fired_run_id FROM pending_runs WHERE id=?`, pr.ID).Scan(&status, &runID); err != nil {
		t.Fatal(err)
	}
	if status != "fired" || runID != "accepted" {
		t.Fatalf("accepted retry status=%s run=%s", status, runID)
	}
}

func TestPendingDispatcherDoesNotRetryUncertainExecution(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  *RunResult
		err  error
	}{
		{"other error", nil, errors.New("execution failed after external effect")},
		{"accepted result", &RunResult{RunID: "accepted"}, ErrConcurrencyLimitReached},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := enqueueDue(t, 1)
			e := &capacityRetryExecutor{run: func(RunInput) (*RunResult, error) { return tc.res, tc.err }}
			d := NewPendingRunDispatcher(s, e, nil)
			d.fireOne(t.Context(), PendingRun{ID: "pa"})
			due, err := s.DueRuns(t.Context(), time.Now(), 25)
			if err != nil || len(due) != 0 {
				t.Fatalf("uncertain work became retryable: count=%d err=%v", len(due), err)
			}
		})
	}
}

func TestPendingRetryCannotChangeAcceptedOrRearmedOccurrence(t *testing.T) {
	for _, mutation := range []string{
		`UPDATE pending_runs SET fired_run_id='accepted' WHERE id='pa'`,
		`UPDATE pending_runs SET fire_at='2099-01-01T00:00:00.000Z' WHERE id='pa'`,
	} {
		t.Run(mutation, func(t *testing.T) {
			s := enqueueDue(t, 1)
			claimed, err := s.ClaimDue(t.Context(), "pa", time.Now())
			if err != nil || claimed == nil {
				t.Fatalf("claim: %v %v", claimed, err)
			}
			if _, err := s.db.ExecContext(t.Context(), mutation); err != nil {
				t.Fatal(err)
			}
			if err := s.retryUnstarted(t.Context(), *claimed); err != nil {
				t.Fatal(err)
			}
			var status string
			if err := s.db.QueryRowContext(t.Context(), `SELECT status FROM pending_runs WHERE id='pa'`).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != "fired" {
				t.Fatalf("stale claim changed status to %s", status)
			}
		})
	}
}
