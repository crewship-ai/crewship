package api

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
)

// assertInvokingIdentity is the second half of InternalRun's provenance
// check. assertBoundCrewWorkspaceDB has already pinned invoking_crew_id to a
// crew-bound token's own crew (or to the bound workspace for a wsv1 token);
// this closes what it leaves open:
//
//   - a master-token caller (no binding at all) passes that guard without a
//     single lookup, so the crew was never proven to exist, let alone to
//     live in body.WorkspaceID;
//   - invoking_agent_id was never looked at by anyone. The sidecar resolves
//     it from a per-agent bearer token, but the server has no way to tell a
//     sidecar's honest value from a caller's invented one unless it checks.
//
// Both values become the run's persisted provenance: the "From" line on the
// approval card a wait(approval) step raises, the journal's cross-crew
// signal, and — through routineTrust — the crew whose autonomy dial decides
// whether a standing trust grant may fire instead of a human. So the rule
// is: an empty value is fine (unattributed, the executor falls back to the
// author crew), a non-empty crew must be a live row in the run's workspace,
// and a non-empty agent must be a live row in that workspace AND a member of
// the invoking crew. A caller with no crew binding that names an agent but
// no crew gets the crew FILLED from the agent's row (the same completion
// #1222 does for a crew-bound token), so the persisted "From" line never
// pairs an agent with a crew it is not in. Anything else is 403; a supplied
// value is never rewritten, because a rewritten provenance is still a lie.
func assertInvokingIdentity(w http.ResponseWriter, r *http.Request, db *sql.DB, logger *slog.Logger, workspaceID string, crewID *string, agentID string) bool {
	if db == nil {
		return true
	}
	ctx := r.Context()
	if agentID != "" {
		var agentWS string
		var agentCrew sql.NullString
		err := db.QueryRowContext(ctx,
			`SELECT workspace_id, crew_id FROM agents WHERE id = ? AND deleted_at IS NULL`, agentID).Scan(&agentWS, &agentCrew)
		if err == nil && agentWS == workspaceID && *crewID == "" && agentCrew.String != "" {
			*crewID = agentCrew.String
		}
		if err != nil || agentWS != workspaceID || (*crewID != "" && agentCrew.String != *crewID) {
			if err != nil && !errors.Is(err, sql.ErrNoRows) && logger != nil {
				logger.Error("resolve invoking agent", "agent_id", agentID, "error", err)
			}
			if logger != nil {
				logger.Warn("internal run refused: invoking agent is not a member of the invoking crew",
					"path", r.URL.Path, "remote_addr", r.RemoteAddr,
					"workspace_id", workspaceID, "invoking_crew_id", *crewID, "invoking_agent_id", agentID)
			}
			replyError(w, http.StatusForbidden, "invoking_agent_id is not an agent of the invoking crew in this workspace")
			return false
		}
	}
	if *crewID != "" {
		var crewWS string
		err := db.QueryRowContext(ctx,
			`SELECT workspace_id FROM crews WHERE id = ? AND deleted_at IS NULL`, *crewID).Scan(&crewWS)
		if err != nil || crewWS != workspaceID {
			if err != nil && !errors.Is(err, sql.ErrNoRows) && logger != nil {
				logger.Error("resolve invoking crew", "crew_id", *crewID, "error", err)
			}
			if logger != nil {
				logger.Warn("internal run refused: invoking crew is not in the run's workspace",
					"path", r.URL.Path, "remote_addr", r.RemoteAddr,
					"workspace_id", workspaceID, "invoking_crew_id", *crewID)
			}
			replyError(w, http.StatusForbidden, "invoking_crew_id does not belong to the workspace of this run")
			return false
		}
	}
	return true
}
