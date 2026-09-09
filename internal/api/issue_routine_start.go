package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type issueRoutineDispatchKey struct{}
type issueRoutineDispatch struct {
	RunID  string
	Active func(context.Context) bool
	Failed func(error)
}
type issueRoutineResponse struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func (r *issueRoutineResponse) Header() http.Header  { return r.header }
func (r *issueRoutineResponse) WriteHeader(code int) { r.code = code }
func (r *issueRoutineResponse) Write(p []byte) (int, error) {
	if r.code == 0 {
		r.code = 200
	}
	return r.body.Write(p)
}

// Uses the routine's public execution gates under the original caller identity.
// A private context value requests asynchronous execution; clients cannot opt
// into it or force a run ID using JSON or headers.
func (h *IssueHandler) startBoundRoutine(w http.ResponseWriter, r *http.Request, tx *sql.Tx, missionID, ident, leadID, routineID string) {
	if h.routines == nil {
		writeProblem(w, r, 503, "Routine execution is unavailable")
		return
	}
	var slug, definition, inputsJSON string
	err := tx.QueryRowContext(r.Context(), `SELECT p.slug,p.definition_json,COALESCE(m.routine_inputs_json,'{}') FROM pipelines p JOIN missions m ON m.routine_id=p.id WHERE m.id=? AND p.id=? AND p.workspace_id=?`, missionID, routineID, WorkspaceIDFromContext(r.Context())).Scan(&slug, &definition, &inputsJSON)
	if err != nil {
		writeProblem(w, r, 400, "The bound routine is unavailable in this workspace")
		return
	}
	inputs := map[string]any{}
	if json.Unmarshal([]byte(inputsJSON), &inputs) != nil {
		writeProblem(w, r, 400, "Stored routine inputs are invalid")
		return
	}
	dsl, err := pipeline.Parse([]byte(definition))
	if err != nil {
		writeProblem(w, r, 400, "The bound routine definition is invalid")
		return
	}
	for _, spec := range dsl.Inputs {
		if spec.Required && spec.Default == nil && inputs[spec.Name] == nil {
			writeProblem(w, r, 422, "Missing required routine input: "+spec.Name)
			return
		}
	}
	executionID, runID := generateCUID(), generateCUID()
	now := tsformat.Format(time.Now())
	_, err = tx.ExecContext(r.Context(), `INSERT INTO issue_executions(id,mission_id,work_revision,brief_revision,stage,reviewer_agent_id,routine_run_id,created_at,updated_at) SELECT ?,mission_id,revision,brief_revision,'working',?,?,?,? FROM issue_work WHERE mission_id=?`, executionID, leadID, runID, now, now, missionID)
	if err != nil {
		internalError(w, r, h.logger, "start routine: execution", err)
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE missions SET status='IN_PROGRESS',updated_at=?,completed_at=NULL WHERE id=? AND status IN ('TODO','BACKLOG')`, now, missionID); err != nil {
		internalError(w, r, h.logger, "start routine: status", err)
		return
	}
	if _, err = tx.ExecContext(r.Context(), `INSERT OR IGNORE INTO chats(id,agent_id,workspace_id,title,mode,status,started_at,created_at,updated_at) SELECT id,lead_agent_id,workspace_id,'Issue: '||title,'MISSION','ACTIVE',?,?,? FROM missions WHERE id=?`, now, now, now, missionID); err != nil {
		internalError(w, r, h.logger, "start routine: chat", err)
		return
	}

	if err = tx.Commit(); err != nil {
		internalError(w, r, h.logger, "start routine: commit", err)
		return
	}
	fail := func(cause error) {
		ctx := context.Background()
		stamp := tsformat.Format(time.Now())
		// Guard the execution so a late error cannot overwrite a human handoff.
		_, updateErr := h.db.ExecContext(ctx, `UPDATE missions SET status='FAILED',updated_at=? WHERE id=? AND status='IN_PROGRESS' AND EXISTS(SELECT 1 FROM issue_executions WHERE id=? AND stage='working')`, stamp, missionID, executionID)
		if updateErr != nil {
			h.logger.Error("issue routine failure state", "error", updateErr)
		}
		_, _ = h.db.ExecContext(ctx, `UPDATE issue_executions SET stage='failed',review_note=?,updated_at=? WHERE id=? AND stage='working'`, cause.Error(), stamp, executionID)
		h.broadcastIssueEvent(WorkspaceIDFromContext(r.Context()), "issue.updated", map[string]string{"id": missionID, "identifier": ident})
	}
	body, _ := json.Marshal(map[string]any{"inputs": inputs, "triggered_via": "issue", "triggered_by_id": ident, "metadata": map[string]string{"issue_execution_id": executionID, "issue_id": missionID}})
	request := r.Clone(context.WithValue(r.Context(), issueRoutineDispatchKey{}, issueRoutineDispatch{RunID: runID, Failed: fail, Active: func(ctx context.Context) bool {
		var active bool
		err := h.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_executions x JOIN missions m ON m.id=x.mission_id JOIN issue_work w ON w.mission_id=m.id WHERE x.id=? AND x.stage='working' AND m.status='IN_PROGRESS' AND w.mode='agent')`, executionID).Scan(&active)
		return err == nil && active
	}}))
	request.Header.Del("Idempotency-Key")
	request.SetPathValue("slug", slug)
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))
	response := &issueRoutineResponse{header: make(http.Header)}
	h.routines.Run(response, request)
	if response.code >= 400 {
		fail(fmt.Errorf("routine start rejected: %s", response.body.String()))
		for key, values := range response.header {
			w.Header()[key] = values
		}
		// The proxied body is this API's own JSON error document, and it can
		// quote a value the caller supplied (a slug inside a validation
		// message). Naming the type rather than letting net/http sniff the
		// bytes keeps a browser that reaches this endpoint directly from
		// reading such a quote as markup. SecurityHeaders already sends
		// nosniff and a default-src 'none' CSP; this is the same rule stated
		// where the bytes are written.
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
		}
		w.WriteHeader(response.code)
		_, _ = w.Write(response.body.Bytes())
		return
	}
	if h.missionEngine != nil {
		finish := beginBackgroundWork()
		go func() {
			defer finish()
			if err := h.missionEngine.StartMission(context.Background(), missionID); err != nil {
				fail(err)
			}
		}()
	}
	h.broadcastIssueEvent(WorkspaceIDFromContext(r.Context()), "issue.started", map[string]string{"id": missionID, "identifier": ident, "status": "IN_PROGRESS"})
	writeJSON(w, 202, map[string]string{"identifier": ident, "status": "IN_PROGRESS", "run_id": runID})
}
