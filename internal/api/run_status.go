package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
)

// RunStatusHandler answers one question for the sidecar: is this run still the
// live attempt of its work item?
//
// It exists because revocation inside the sidecar cannot be complete on its
// own. A durable journal survives a sidecar restart, but not the container
// being recreated, and it cannot notice a run-end notification that was
// dropped in flight — nothing local can, because the loss leaves no local
// trace. Both gaps close the same way: ask the process that owns the ledger.
//
// The predicate is deliberately the SAME one the memory mutation endpoint
// enforces (runIsLiveAttempt), not a second reading of the same tables. Two
// copies of "is this run current" would drift, and the direction they drift is
// the dangerous one — a sidecar admitting a run the ledger considers finished.
type RunStatusHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewRunStatusHandler(db *sql.DB, logger *slog.Logger) *RunStatusHandler {
	return &RunStatusHandler{db: db, logger: logger}
}

// runStatusResponse is the whole contract. `active` is the only field the
// caller acts on; the reason is for a human reading logs, and carries no
// authority.
type runStatusResponse struct {
	RunID  string `json:"run_id"`
	Active bool   `json:"active"`
	Reason string `json:"reason,omitempty"`
}

// Status reports whether runID is the current, open attempt of live work.
//
// A run that is absent from the ledger is reported `active: false`, not 404.
// The distinction matters: on this surface a 404 is indistinguishable from the
// router's deliberate "unregistered path or refused caller" answer, so a
// missing run returned as 404 would be read by an older sidecar as "this host
// does not have the endpoint" and by a revoked one as the same thing. Answering
// 200 with active=false says exactly what is true and cannot be confused with
// either.
func (h *RunStatusHandler) Status(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runId")
	if runID == "" {
		replyError(w, http.StatusBadRequest, "Run id is required")
		return
	}

	// Scope to the caller's workspace. requireInternal pins workspace_id into
	// the query for a crew-bound token — the sidecar's token is exactly that —
	// so the binding is server-side and unforgeable, and a caller-supplied
	// value that disagrees has already been refused before reaching here.
	//
	// Without this the endpoint answered about any run id in any workspace. Run
	// ids are unguessable, so it was a narrow oracle rather than an open door,
	// but it was the one place on this surface where a bound token bought no
	// binding — and "you cannot guess the identifier" is not an access control.
	workspaceID := r.URL.Query().Get("workspace_id")
	if workspaceID == "" {
		// A token that carries no workspace cannot be told about a run. Refusing
		// is not a verdict on the run: 403 is distinguishable from the 200s
		// below, so a caller cannot read it as "inactive" and revoke on it.
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	active, reason, err := runIsLiveAttempt(r.Context(), h.db, workspaceID, runID)
	if err != nil {
		// An unavailable ledger is not an answer. Saying "inactive" here would
		// revoke every live run in the crew during a database blip; saying
		// "active" would defeat the check. The caller has its own policy for an
		// unreachable authority, and a 503 is what routes it there.
		replyError(w, http.StatusServiceUnavailable, "The work ledger is unavailable; run status is unknown")
		return
	}
	writeJSON(w, http.StatusOK, runStatusResponse{RunID: runID, Active: active, Reason: reason})
}

// runIsLiveAttempt is the single definition of "this run may still act".
//
// Four conditions, all necessary: the attempt exists, it has not ended, its
// work item is in a state that holds a runtime, and the attempt's generation is
// still the item's. The last one is the fencing term — an attempt superseded by
// a retry is not current even though its own row looks untouched.
//
// The reason string is for a human reading logs. It is returned alongside the
// boolean rather than encoded in it, because a caller that has to parse prose
// to learn whether a token is valid will eventually parse it wrong.
func runIsLiveAttempt(ctx context.Context, db *sql.DB, workspaceID, runID string) (bool, string, error) {
	var (
		itemState      string
		itemGeneration int64
		attemptGen     int64
		endedAt        sql.NullString
	)
	err := db.QueryRowContext(ctx, `
		SELECT wi.state, wi.generation, wa.generation, wa.ended_at
		  FROM work_attempts wa
		  JOIN work_items wi ON wi.id = wa.work_id
		 WHERE wa.run_id = ? AND wi.workspace_id = ?`, runID, workspaceID).
		Scan(&itemState, &itemGeneration, &attemptGen, &endedAt)
	if errors.Is(err, sql.ErrNoRows) {
		// Absent, or present in another workspace — deliberately the same
		// answer, so this cannot be used to probe for runs the caller has no
		// business knowing about.
		return false, "no such run in this workspace's work ledger", nil
	}
	if err != nil {
		return false, "", err
	}
	if endedAt.Valid && endedAt.String != "" {
		return false, "the attempt has ended", nil
	}
	if !memoryRunStateIsLive(itemState) {
		return false, "the work item is " + itemState, nil
	}
	if attemptGen != itemGeneration {
		return false, "the attempt has been superseded by a later one", nil
	}
	return true, "", nil
}
