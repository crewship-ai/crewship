package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/quartermaster"
)

// EvalHandler fronts the quartermaster replay + regression endpoints.
// Both mutating calls return 202 and run in a goroutine — a full
// Extract + Compute + Emit across a long mission's journal can take
// multiple seconds and would block the HTTP worker otherwise.
type EvalHandler struct {
	db      *sql.DB
	logger  *slog.Logger
	journal journal.Emitter
}

func NewEvalHandler(db *sql.DB, logger *slog.Logger) *EvalHandler {
	return &EvalHandler{db: db, logger: logger, journal: noopEmitter{}}
}

// SetJournal wires the journal emitter once the Router has resolved it.
// quartermaster.Replay / DetectRegression REQUIRE a non-nil emitter
// (they return an error otherwise), so the setter collapses nil to
// the no-op rather than letting a server misconfig nuke every eval.
func (h *EvalHandler) SetJournal(j journal.Emitter) {
	if j == nil {
		h.journal = noopEmitter{}
		return
	}
	h.journal = j
}

// Replay serves POST /api/v1/eval/replay.
//
//	Body: {"mission_id": "...", "seed": 42}
//	Resp: {"run_id": "...", "status": "queued"}
//
// Workspace-scoped: mission_id must belong to the caller's workspace,
// otherwise we return 404 with the "mission not found" shape that
// matches other cross-tenant checks in this package.
func (h *EvalHandler) Replay(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "workspace required"})
		return
	}
	role := RoleFromContext(r.Context())
	if role != "OWNER" && role != "ADMIN" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "eval requires OWNER or ADMIN role"})
		return
	}
	user := UserFromContext(r.Context())
	createdBy := ""
	if user != nil {
		createdBy = user.ID
	}

	var body struct {
		MissionID string `json:"mission_id"`
		Seed      int64  `json:"seed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if body.MissionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mission_id required"})
		return
	}
	if !missionBelongsToWorkspace(r.Context(), h.db, body.MissionID, workspaceID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "mission not found"})
		return
	}

	runID := "er_" + newEvalToken()
	rec := quartermaster.RunRecord{
		ID:          runID,
		WorkspaceID: workspaceID,
		MissionID:   body.MissionID,
		Kind:        "replay",
		Seed:        body.Seed,
		CreatedBy:   createdBy,
	}
	if err := quartermaster.InsertReplayRun(r.Context(), h.db, rec); err != nil {
		h.logger.Error("eval replay insert", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "insert failed"})
		return
	}

	// Detach the goroutine from the request context: the HTTP handler
	// returns 202 immediately, and the caller's context cancels the
	// moment the response is flushed. We use a fresh context with a
	// generous budget so the run can actually finish.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		if err := updateRun(ctx, h.db, runID, "running", "", "", 0, 0, false); err != nil {
			h.logger.Error("eval replay: updateRun(running) failed", "err", err, "run_id", runID)
			// Continue — the work is still worth doing and a second
			// DB blip may succeed on the terminal update below.
		}
		run, err := quartermaster.Replay(ctx, h.db, h.journal, workspaceID, body.MissionID, body.Seed)
		if err != nil {
			h.logger.Warn("eval replay failed", "err", err, "mission_id", body.MissionID)
			if uerr := updateRun(ctx, h.db, runID, "failed", safeStr(err), run.SeedSignature,
				run.Metrics.TotalTokens, run.Metrics.TotalCostUSD, false); uerr != nil {
				h.logger.Error("eval replay: updateRun(failed) failed", "err", uerr, "run_id", runID)
			}
			return
		}
		if err := updateRun(ctx, h.db, runID, "completed", run.Result, run.SeedSignature,
			run.Metrics.TotalTokens, run.Metrics.TotalCostUSD, false); err != nil {
			h.logger.Error("eval replay: updateRun(completed) failed", "err", err, "run_id", runID)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"run_id": runID,
		"status": "queued",
	})
}

// Regression serves POST /api/v1/eval/regression.
//
//	Body: {"baseline_mission_id": "...", "candidate_mission_id": "..."}
//	Resp: {"run_id": "...", "status": "queued"}
//
// Both mission IDs must belong to the caller's workspace. We check
// them independently so a partial spoof (valid baseline + foreign
// candidate) still 404s.
func (h *EvalHandler) Regression(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "workspace required"})
		return
	}
	role := RoleFromContext(r.Context())
	if role != "OWNER" && role != "ADMIN" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "eval requires OWNER or ADMIN role"})
		return
	}
	user := UserFromContext(r.Context())
	createdBy := ""
	if user != nil {
		createdBy = user.ID
	}

	var body struct {
		BaselineMissionID  string `json:"baseline_mission_id"`
		CandidateMissionID string `json:"candidate_mission_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if body.BaselineMissionID == "" || body.CandidateMissionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "baseline_mission_id and candidate_mission_id required"})
		return
	}
	if !missionBelongsToWorkspace(r.Context(), h.db, body.BaselineMissionID, workspaceID) ||
		!missionBelongsToWorkspace(r.Context(), h.db, body.CandidateMissionID, workspaceID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "mission not found"})
		return
	}

	runID := "er_" + newEvalToken()
	rec := quartermaster.RunRecord{
		ID:                 runID,
		WorkspaceID:        workspaceID,
		Kind:               "regression",
		BaselineMissionID:  body.BaselineMissionID,
		CandidateMissionID: body.CandidateMissionID,
		CreatedBy:          createdBy,
	}
	if err := quartermaster.InsertRegressionRun(r.Context(), h.db, rec); err != nil {
		h.logger.Error("eval regression insert", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "insert failed"})
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		if err := updateRun(ctx, h.db, runID, "running", "", "", 0, 0, false); err != nil {
			h.logger.Error("eval regression: updateRun(running) failed", "err", err, "run_id", runID)
		}
		report, err := quartermaster.DetectRegression(ctx, h.db, h.journal, workspaceID,
			body.BaselineMissionID, body.CandidateMissionID)
		if err != nil {
			h.logger.Warn("eval regression failed", "err", err)
			if uerr := updateRun(ctx, h.db, runID, "failed", safeStr(err), "", 0, 0, false); uerr != nil {
				h.logger.Error("eval regression: updateRun(failed) failed", "err", uerr, "run_id", runID)
			}
			return
		}
		result := "no_regression"
		if report.Regressed {
			result = "regressed: " + report.DeltaSummary
		}
		if err := updateRun(ctx, h.db, runID, "completed", result, "",
			report.Candidate.TotalTokens, report.Candidate.TotalCostUSD, report.Regressed); err != nil {
			h.logger.Error("eval regression: updateRun(completed) failed", "err", err, "run_id", runID)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"run_id": runID,
		"status": "queued",
	})
}

// ListRuns serves GET /api/v1/eval/runs?limit=50. Workspace-scoped.
func (h *EvalHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "workspace required"})
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	runs, err := quartermaster.ListRuns(r.Context(), h.db, workspaceID, limit)
	if err != nil {
		h.logger.Error("eval list runs", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rows":  runs,
		"count": len(runs),
		"limit": limit,
	})
}

// updateRun is a thin wrapper around quartermaster.UpdateRunStatus that
// also logs the error, if any, so we don't lose audit signal in the
// background goroutine.
func updateRun(ctx context.Context, db *sql.DB, id, status, result, signature string, tokens int64, cost float64, regressed bool) error {
	return quartermaster.UpdateRunStatus(ctx, db, id, status, result, signature, tokens, cost, regressed)
}

// newEvalToken returns 8 random bytes hex-encoded, matching the
// quartermaster-internal newRunID. Kept here so the handler doesn't
// import a private helper.
func newEvalToken() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// safeStr is a small nil-guard so we can stash an error message in the
// eval_runs.result column without pulling in fmt for a one-liner.
func safeStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
