package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// #1426 (3.1) — cancelling a parked WAITING run must succeed. The run
// released its slot + registry entry when it parked, so the in-memory scan
// 404s; the handler falls back to the run store, marks the row cancelled and
// cancels its pending waitpoint so the inbox approval card stops being
// actionable.
func TestPipelineRuns_CancelRun_ParkedWaitingRun(t *testing.T) {
	h, db, userID, wsID := runsHandlerRig(t)
	ctx := context.Background()

	registry := pipeline.NewRunRegistry() // empty — the run is not live here
	h.SetRunRegistry(registry)
	runStore := pipeline.NewRunStore(db)
	h.SetRunStore(runStore)
	wpStore := pipeline.NewSQLWaitpointStore(db)
	defer wpStore.Close()
	h.SetWaitpointStore(wpStore)

	const runID = "prn_parked"
	// A real pipelines row (pipeline_runs.pipeline_id FKs to it).
	if _, err := db.ExecContext(ctx, `
INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, ephemeral, workspace_visible, author_crew_id, author_agent_id, authored_via, last_test_run_at, last_test_run_passed, created_at, updated_at)
VALUES ('pln_x', ?, 'x', 'x', '{"name":"x","steps":[]}', 'h', 0, 1, NULL, NULL, 'agent_tool_call', datetime('now'), 1, datetime('now'), datetime('now'))`, wsID); err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
	if err := runStore.Insert(ctx, &pipeline.RunRecord{
		ID: runID, WorkspaceID: wsID, PipelineID: "pln_x", PipelineSlug: "x",
		Status: pipeline.RunStatusWaiting, Mode: pipeline.ModeRun,
	}); err != nil {
		t.Fatalf("insert waiting run: %v", err)
	}
	token, err := wpStore.CreateApproval(ctx, pipeline.WaitpointApprovalRequest{
		WorkspaceID: wsID, PipelineRunID: runID, StepID: "gate", Prompt: "ship it?",
	})
	if err != nil {
		t.Fatalf("create approval: %v", err)
	}

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipelines/runs/"+runID+"/cancel", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("runId", runID)
	rr := httptest.NewRecorder()
	h.CancelRun(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	rec, gerr := runStore.Get(ctx, runID)
	if gerr != nil {
		t.Fatalf("get run: %v", gerr)
	}
	if rec.Status != pipeline.RunStatusCancelled {
		t.Errorf("run status = %q, want cancelled", rec.Status)
	}
	var wpStatus string
	if err := db.QueryRowContext(ctx,
		`SELECT status FROM pipeline_waitpoints WHERE token = ?`, token).Scan(&wpStatus); err != nil {
		t.Fatalf("read waitpoint: %v", err)
	}
	if wpStatus != "cancelled" {
		t.Errorf("waitpoint status = %q, want cancelled", wpStatus)
	}
}

// seedParkedRun inserts a pipeline and a WAITING run of it with one pending
// approval, the state an approval-parked run leaves behind.
func seedParkedRun(t *testing.T, h *PipelineHandler, wsID, runID string) (*pipeline.RunStore, *pipeline.SQLWaitpointStore, string) {
	t.Helper()
	ctx := context.Background()
	runStore := pipeline.NewRunStore(h.db)
	h.SetRunStore(runStore)
	wpStore := pipeline.NewSQLWaitpointStore(h.db)
	t.Cleanup(func() { wpStore.Close() })
	h.SetWaitpointStore(wpStore)
	if _, err := h.db.ExecContext(ctx, `
INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, ephemeral, workspace_visible, author_crew_id, author_agent_id, authored_via, last_test_run_at, last_test_run_passed, created_at, updated_at)
VALUES ('pln_race', ?, 'race', 'race', '{"name":"race","steps":[]}', 'h', 0, 1, NULL, NULL, 'agent_tool_call', datetime('now'), 1, datetime('now'), datetime('now'))`, wsID); err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
	if err := runStore.Insert(ctx, &pipeline.RunRecord{
		ID: runID, WorkspaceID: wsID, PipelineID: "pln_race", PipelineSlug: "race",
		Status: pipeline.RunStatusWaiting, Mode: pipeline.ModeRun,
	}); err != nil {
		t.Fatalf("insert waiting run: %v", err)
	}
	token, err := wpStore.CreateApproval(ctx, pipeline.WaitpointApprovalRequest{
		WorkspaceID: wsID, PipelineRunID: runID, StepID: "gate", Prompt: "ship it?",
	})
	if err != nil {
		t.Fatalf("create approval: %v", err)
	}
	return runStore, wpStore, token
}

// #2910 — cancelling a parked run races every resume source (approval,
// signal, event sweeper, boot). Both re-enter through the run's registry
// entry, so exactly one side may win, and the response must say which.
func TestPipelineRuns_CancelRun_ParkedRunRacesResume(t *testing.T) {
	t.Run("cancel holds the entry: a concurrent resume is refused and the row is cancelled", func(t *testing.T) {
		h, db, userID, wsID := runsHandlerRig(t)
		registry := pipeline.NewRunRegistry()
		h.SetRunRegistry(registry)
		const runID = "prn_cancel_wins"
		runStore, _, token := seedParkedRun(t, h, wsID, runID)

		var resumeErr error
		h.parkedCancelStage = func(stage, id string) {
			if stage != "fenced" {
				return
			}
			// The resume Executor.Run would perform here: take the run's
			// registry entry before re-reading the persisted status.
			_, release, err := registry.Acquire(context.Background(), pipeline.AcquireOpts{RunID: id, WorkspaceID: wsID})
			release()
			resumeErr = err
		}
		rr := cancelRunRequest(t, h, userID, wsID, runID)
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"parked":true`) {
			t.Fatalf("status = %d body=%s, want 200 parked", rr.Code, rr.Body.String())
		}
		if !errors.Is(resumeErr, pipeline.ErrDuplicateRunID) {
			t.Fatalf("resume admitted while the cancel held the run: %v", resumeErr)
		}
		rec, err := runStore.Get(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Status != pipeline.RunStatusCancelled {
			t.Errorf("run status = %q, want cancelled", rec.Status)
		}
		var wpStatus string
		if err := db.QueryRow(`SELECT status FROM pipeline_waitpoints WHERE token = ?`, token).Scan(&wpStatus); err != nil {
			t.Fatal(err)
		}
		if wpStatus != "cancelled" {
			t.Errorf("waitpoint status = %q, want cancelled", wpStatus)
		}
		// The entry is released: the next resume is admitted and reads the
		// committed CANCELLED row (Executor.Run then returns it as terminal).
		_, release, err := registry.Acquire(context.Background(), pipeline.AcquireOpts{RunID: runID, WorkspaceID: wsID})
		if err != nil {
			t.Fatalf("cancel kept the run's registry entry: %v", err)
		}
		release()
	})

	// A lifetime takes the entry after the cancel's registry scan missed it.
	// onCancel is what that lifetime does once its context is cancelled,
	// before it releases the entry.
	holdEntry := func(t *testing.T, h *PipelineHandler, registry *pipeline.RunRegistry, wsID string, onCancel func(runID string)) *bool {
		cancelled := new(bool)
		var once sync.Once
		h.parkedCancelStage = func(stage, id string) {
			if stage != "before-fence" {
				return
			}
			once.Do(func() {
				ctx, release, err := registry.Acquire(context.Background(), pipeline.AcquireOpts{RunID: id, WorkspaceID: wsID})
				if err != nil {
					t.Errorf("lifetime acquire: %v", err)
					return
				}
				go func() {
					<-ctx.Done()
					*cancelled = registry.IsCancelRequested(id)
					onCancel(id)
					release()
				}()
			})
		}
		return cancelled
	}

	t.Run("a resumed lifetime holds the entry: it is cancelled and records the outcome itself", func(t *testing.T) {
		h, _, userID, wsID := runsHandlerRig(t)
		registry := pipeline.NewRunRegistry()
		h.SetRunRegistry(registry)
		const runID = "prn_resume_wins"
		runStore, _, _ := seedParkedRun(t, h, wsID, runID)
		// What Executor.Run does when a resumed lifetime is cancelled.
		cancelled := holdEntry(t, h, registry, wsID, func(id string) {
			if err := runStore.MarkTerminal(context.Background(), pipeline.MarkTerminalInput{RunID: id, Status: pipeline.RunStatusCancelled, ErrorMessage: "run cancelled while resuming"}); err != nil {
				t.Error(err)
			}
		})
		rr := cancelRunRequest(t, h, userID, wsID, runID)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s, want 200", rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), `"parked"`) {
			t.Errorf("a resumed run was reported as a parked cancel: %s", rr.Body.String())
		}
		if !*cancelled {
			t.Fatal("the resumed lifetime was not cancelled")
		}
		rec, err := runStore.Get(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Status != pipeline.RunStatusCancelled || rec.ErrorMessage != "run cancelled while resuming" {
			t.Errorf("run = %q (%s), want the lifetime's own cancelled row", rec.Status, rec.ErrorMessage)
		}
	})

	t.Run("the parking lifetime still holds the entry: the row is cancelled once it lets go", func(t *testing.T) {
		h, _, userID, wsID := runsHandlerRig(t)
		registry := pipeline.NewRunRegistry()
		h.SetRunRegistry(registry)
		const runID = "prn_still_parking"
		runStore, _, token := seedParkedRun(t, h, wsID, runID)
		// The lifetime that parked the run returns WAITING and writes nothing.
		cancelled := holdEntry(t, h, registry, wsID, func(string) {})
		rr := cancelRunRequest(t, h, userID, wsID, runID)
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"parked":true`) {
			t.Fatalf("status = %d body=%s, want 200 parked", rr.Code, rr.Body.String())
		}
		if !*cancelled {
			t.Error("the parking lifetime was not cancelled")
		}
		rec, err := runStore.Get(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Status != pipeline.RunStatusCancelled {
			t.Errorf("run status = %q, want cancelled (a reported cancel left the run parked)", rec.Status)
		}
		var wpStatus string
		if err := h.db.QueryRow(`SELECT status FROM pipeline_waitpoints WHERE token = ?`, token).Scan(&wpStatus); err != nil {
			t.Fatal(err)
		}
		if wpStatus != "cancelled" {
			t.Errorf("waitpoint status = %q, want cancelled", wpStatus)
		}
	})

	// The cancel can arrive after the run is persisted WAITING but before the
	// lifetime that parked it returns: the registry scan still lists the run.
	// Cancelling only the in-memory lifetime would answer 200 while the row
	// stays WAITING and a later approve resumes the cancelled run.
	t.Run("the parking lifetime is still listed active: the row is cancelled once it lets go", func(t *testing.T) {
		h, _, userID, wsID := runsHandlerRig(t)
		registry := pipeline.NewRunRegistry()
		h.SetRunRegistry(registry)
		const runID = "prn_listed_parking"
		runStore, _, token := seedParkedRun(t, h, wsID, runID)
		lifetime, release, err := registry.Acquire(context.Background(), pipeline.AcquireOpts{RunID: runID, WorkspaceID: wsID})
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			<-lifetime.Done()
			release() // returns WAITING and writes nothing terminal
		}()
		rr := cancelRunRequest(t, h, userID, wsID, runID)
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"parked":true`) {
			t.Fatalf("status = %d body=%s, want 200 parked", rr.Code, rr.Body.String())
		}
		rec, err := runStore.Get(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Status != pipeline.RunStatusCancelled {
			t.Errorf("run status = %q, want cancelled (a reported cancel left the run parked)", rec.Status)
		}
		var wpStatus string
		if err := h.db.QueryRow(`SELECT status FROM pipeline_waitpoints WHERE token = ?`, token).Scan(&wpStatus); err != nil {
			t.Fatal(err)
		}
		if wpStatus != "cancelled" {
			t.Errorf("waitpoint status = %q, want cancelled", wpStatus)
		}
	})

	t.Run("a run store failure is an error, not a cancel or a 404", func(t *testing.T) {
		h, _, userID, wsID := runsHandlerRig(t)
		h.SetRunRegistry(pipeline.NewRunRegistry())
		const runID = "prn_store_down"
		seedParkedRun(t, h, wsID, runID)
		if _, err := h.db.Exec(`ALTER TABLE pipeline_runs RENAME TO pipeline_runs_unavailable`); err != nil {
			t.Fatal(err)
		}
		rr := cancelRunRequest(t, h, userID, wsID, runID)
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d body=%s, want 500", rr.Code, rr.Body.String())
		}
	})

	t.Run("a resume that finished before the fence is not overwritten", func(t *testing.T) {
		h, _, userID, wsID := runsHandlerRig(t)
		h.SetRunRegistry(pipeline.NewRunRegistry())
		const runID = "prn_resume_finished"
		runStore, _, _ := seedParkedRun(t, h, wsID, runID)
		h.parkedCancelStage = func(stage, id string) {
			if stage != "before-fence" {
				return
			}
			if err := runStore.MarkTerminal(context.Background(), pipeline.MarkTerminalInput{RunID: id, Status: pipeline.RunStatusCompleted}); err != nil {
				t.Fatal(err)
			}
		}
		rr := cancelRunRequest(t, h, userID, wsID, runID)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d body=%s, want 404 (already finished)", rr.Code, rr.Body.String())
		}
		rec, err := runStore.Get(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Status != pipeline.RunStatusCompleted {
			t.Errorf("run status = %q, want completed", rec.Status)
		}
	})
}

func cancelRunRequest(t *testing.T, h *PipelineHandler, userID, wsID, runID string) *httptest.ResponseRecorder {
	t.Helper()
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipelines/runs/"+runID+"/cancel", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("runId", runID)
	rr := httptest.NewRecorder()
	h.CancelRun(rr, req)
	return rr
}
