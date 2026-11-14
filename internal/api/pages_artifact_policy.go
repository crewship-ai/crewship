package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

type artifactCreationPolicy struct {
	Enabled bool     `json:"enabled"`
	CrewIDs []string `json:"crew_ids"`
	Version int64    `json:"version"`
	// Limits apply with agent access modes on (#5317). A workspace without a
	// stored policy reports the defaults.
	Limits artifactRunLimits `json:"limits"`
	// ScopedDefault is true while no policy was ever saved and agent access
	// modes are on: chat runs of every live crew may then use their scope.
	ScopedDefault bool `json:"scoped_default"`
}

// artifactRunLimitsUpdate changes some limits; an omitted field keeps its value.
type artifactRunLimitsUpdate struct {
	ChatRunCreate  *int `json:"chat_run_create_limit,omitempty"`
	RunRead        *int `json:"run_read_limit,omitempty"`
	CrewHourlyPage *int `json:"crew_hourly_page_limit,omitempty"`
}

// apply validates against the same ranges as the schema CHECKs.
func (u *artifactRunLimitsUpdate) apply(l artifactRunLimits) (artifactRunLimits, bool) {
	if u == nil {
		return l, true
	}
	set := func(dst *int, v *int, lo, hi int) bool {
		if v == nil {
			return true
		}
		if *v < lo || *v > hi {
			return false
		}
		*dst = *v
		return true
	}
	ok := set(&l.ChatRunCreate, u.ChatRunCreate, 1, 20) && set(&l.RunRead, u.RunRead, 1, 500) && set(&l.CrewHourlyPage, u.CrewHourlyPage, 1, 1000)
	return l, ok
}

// WorkspaceArtifactCreationPolicyResponse describes the administrative ceiling.
type WorkspaceArtifactCreationPolicyResponse = artifactCreationPolicy

// WorkspaceArtifactCreationPolicyError reports policy validation or conflicts.
type WorkspaceArtifactCreationPolicyError struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

// This is only the administrative ceiling. Callers still need the trusted
// human run principal, per-run opt-in and the existing content permissions.
func (h *PageHandler) artifactAgentPolicyAllows(ctx context.Context, workspace, crew string) bool {
	var allowed int
	err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_artifact_creation_policy p JOIN workspace_artifact_creation_crews a ON a.workspace_id=p.workspace_id JOIN workspaces w ON w.id=p.workspace_id AND w.deleted_at IS NULL JOIN crews c ON c.id=a.crew_id AND c.workspace_id=p.workspace_id AND c.deleted_at IS NULL WHERE p.workspace_id=? AND a.crew_id=? AND p.enabled=1`, workspace, crew).Scan(&allowed)
	return err == nil && allowed == 1
}

// artifactScopedPolicyAllows is the policy ceiling for the flag-on chat-run
// scope only (amendment 8, #5317): a workspace that never saved a policy
// allows its live crews there. Every other caller keeps
// artifactAgentPolicyAllows, so no other agent surface opens by default.
func (h *PageHandler) artifactScopedPolicyAllows(ctx context.Context, workspace, crew string) bool {
	if h.artifactAgentPolicyAllows(ctx, workspace, crew) {
		return true
	}
	if !h.artifactRunModesOn(ctx, workspace) {
		return false
	}
	var rows, live int
	err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM workspace_artifact_creation_policy WHERE workspace_id=?),
 (SELECT COUNT(*) FROM crews c JOIN workspaces w ON w.id=c.workspace_id AND w.deleted_at IS NULL WHERE c.id=? AND c.workspace_id=? AND c.deleted_at IS NULL)`, workspace, crew, workspace).Scan(&rows, &live)
	return err == nil && rows == 0 && live == 1
}
func (h *PageHandler) artifactPolicyAdmin(w http.ResponseWriter, r *http.Request) bool {
	user := UserFromContext(r.Context())
	agent, _ := pageGrantCallerIsAgent(r)
	if user == nil || agent || projectAgentFrom(r.Context()) != nil || r.Context().Value(artifactRunKey{}) != nil {
		replyError(w, 401, "human authentication required")
		return false
	}
	workspace := WorkspaceIDFromContext(r.Context())
	if workspace == "" || (r.PathValue("workspaceId") != "" && r.PathValue("workspaceId") != workspace) {
		replyError(w, 404, "workspace not found")
		return false
	}
	if err := validateContentHumanCredential(r, h.pageAuthReader(r.Context()), scopeForRoute("/api/v1/workspaces/{workspaceId}/artifact-creation-policy")); err != nil {
		if errors.Is(err, errContentCredentialScope) {
			replyError(w, 403, "current CLI scope required")
		} else {
			replyError(w, 401, "current human credential required")
		}
		return false
	}
	role, err := h.liveContentHumanRole(r.Context(), workspace, user.ID)
	if err != nil || !canRole(role, "manage") {
		replyError(w, 403, "active trusted workspace administrator required")
		return false
	}
	return true
}
func (h *PageHandler) loadArtifactCreationPolicy(ctx context.Context, workspace string) (artifactCreationPolicy, error) {
	result := artifactCreationPolicy{CrewIDs: []string{}, Limits: artifactRunLimits{defaultChatRunCreateLimit, defaultRunReadLimit, defaultCrewHourlyPageLimit}}
	q := h.pageAuthReader(ctx)
	err := q.QueryRowContext(ctx, `SELECT enabled,version,chat_run_create_limit,run_read_limit,crew_hourly_page_limit FROM workspace_artifact_creation_policy WHERE workspace_id=?`, workspace).Scan(&result.Enabled, &result.Version, &result.Limits.ChatRunCreate, &result.Limits.RunRead, &result.Limits.CrewHourlyPage)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	rows, err := q.QueryContext(ctx, `SELECT crew_id FROM workspace_artifact_creation_crews WHERE workspace_id=? ORDER BY crew_id`, workspace)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return result, err
		}
		result.CrewIDs = append(result.CrewIDs, id)
	}
	return result, rows.Err()
}
func (h *PageHandler) GetArtifactCreationPolicy(w http.ResponseWriter, r *http.Request) {
	artifactPolicyHeaders(w)
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		replyInternalError(w, h.logger, "begin artifact policy snapshot", err)
		return
	}
	defer tx.Rollback()
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	r = r.WithContext(ctx)
	if !h.artifactPolicyAdmin(w, r) {
		return
	}
	result, err := h.loadArtifactCreationPolicy(ctx, WorkspaceIDFromContext(ctx))
	if err != nil {
		replyInternalError(w, h.logger, "read artifact creation policy", err)
		return
	}
	result.ScopedDefault = result.Version == 0 && h.artifactRunModesOn(ctx, WorkspaceIDFromContext(ctx))
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "finish artifact policy snapshot", err)
		return
	}
	writeJSON(w, 200, result)
}
func (h *PageHandler) UpdateArtifactCreationPolicy(w http.ResponseWriter, r *http.Request) {
	artifactPolicyHeaders(w)
	if !h.artifactPolicyAdmin(w, r) {
		return
	}
	var req struct {
		Enabled     *bool                    `json:"enabled"`
		CrewIDs     *[]string                `json:"crew_ids"`
		BaseVersion *int64                   `json:"base_version"`
		Limits      *artifactRunLimitsUpdate `json:"limits,omitempty"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if !decodeArtifactPolicyJSON(w, r, &req) {
		return
	}
	if req.Enabled == nil || req.CrewIDs == nil || req.BaseVersion == nil || *req.BaseVersion < 0 || len(*req.CrewIDs) > 500 {
		replyError(w, 400, "enabled, crew_ids and nonnegative base_version are required (at most 500 crews)")
		return
	}
	seen := map[string]bool{}
	for _, id := range *req.CrewIDs {
		if len(id) == 0 || len(id) > 128 || strings.TrimSpace(id) != id || seen[id] {
			replyError(w, 400, "crew_ids must contain unique nonempty crew IDs")
			return
		}
		seen[id] = true
	}
	sort.Strings(*req.CrewIDs)
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin artifact creation policy", err)
		return
	}
	defer tx.Rollback()
	workspace := WorkspaceIDFromContext(r.Context())
	if _, err := tx.ExecContext(r.Context(), `UPDATE workspaces SET updated_at=updated_at WHERE id=?`, workspace); err != nil {
		replyInternalError(w, h.logger, "fence artifact creation policy", err)
		return
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	r = r.WithContext(ctx)
	if !h.artifactPolicyAdmin(w, r) {
		return
	}
	current, err := h.loadArtifactCreationPolicy(ctx, workspace)
	if err != nil {
		replyInternalError(w, h.logger, "read current artifact creation policy", err)
		return
	}
	if current.Version != *req.BaseVersion {
		artifactPolicyError(w, 409, "stale_policy", "artifact creation policy changed; review latest before saving")
		return
	}
	for _, id := range *req.CrewIDs {
		var valid int
		if tx.QueryRowContext(ctx, `SELECT 1 FROM crews WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, id, workspace).Scan(&valid) != nil {
			replyError(w, 400, "every allowed crew must belong to this workspace and be active")
			return
		}
	}
	limits, ok := req.Limits.apply(current.Limits)
	if !ok {
		replyError(w, 400, "limits out of range: chat_run_create_limit 1-20, run_read_limit 1-500, crew_hourly_page_limit 1-1000")
		return
	}
	stamp := tsformat.Format(h.clock.Now().UTC())
	// A limit change is a policy change: it bumps version, so the policy's
	// authority epoch moves with it.
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_artifact_creation_policy(workspace_id,enabled,version,updated_by_user_id,updated_at,chat_run_create_limit,run_read_limit,crew_hourly_page_limit) VALUES(?,?,1,?,?,?,?,?) ON CONFLICT(workspace_id) DO UPDATE SET enabled=excluded.enabled,version=workspace_artifact_creation_policy.version+1,updated_by_user_id=excluded.updated_by_user_id,updated_at=excluded.updated_at,chat_run_create_limit=excluded.chat_run_create_limit,run_read_limit=excluded.run_read_limit,crew_hourly_page_limit=excluded.crew_hourly_page_limit`, workspace, *req.Enabled, UserFromContext(ctx).ID, stamp, limits.ChatRunCreate, limits.RunRead, limits.CrewHourlyPage); err != nil {
		replyInternalError(w, h.logger, "save artifact creation policy", err)
		return
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspace_artifact_creation_crews WHERE workspace_id=?`, workspace); err != nil {
		replyInternalError(w, h.logger, "replace artifact crew allowlist", err)
		return
	}
	for _, id := range *req.CrewIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_artifact_creation_crews(workspace_id,crew_id) VALUES(?,?)`, workspace, id); err != nil {
			replyInternalError(w, h.logger, "save artifact crew allowlist", err)
			return
		}
	}
	// Which crews may create artifacts is an access ceiling: the change and
	// its chained audit (old and new allowlist) commit together (F1).
	next := artifactCreationPolicy{Enabled: *req.Enabled, CrewIDs: *req.CrewIDs, Version: current.Version + 1, Limits: limits}
	notify, err := appendSettingsAudit(r, tx, h.journal, "workspace.artifact_creation_policy.update", "WORKSPACE", workspace,
		map[string]any{"previous": current, "current": next})
	if err != nil {
		replyInternalError(w, h.logger, "audit artifact creation policy", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit artifact creation policy", err)
		return
	}
	if notify != nil {
		notify()
	}
	broadcastWorkspaceEvent(h.hub, workspace, "workspace.updated", map[string]any{"workspace_id": workspace})
	writeJSON(w, 200, next)
}

// Narrow source helpers keep policy requests independent of content uploads.
func artifactPolicyHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}
func artifactPolicyError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, WorkspaceArtifactCreationPolicyError{Error: message, Code: code})
}
func decodeArtifactPolicyJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(target)
	if err == nil {
		err = decoder.Decode(new(any))
		if err == io.EOF {
			return true
		}
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		replyError(w, http.StatusRequestEntityTooLarge, "artifact request too large")
	} else {
		replyError(w, http.StatusBadRequest, "artifact request must contain one valid JSON object with known fields")
	}
	return false
}
