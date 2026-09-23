package api

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

type accessMeDecision struct {
	State  string `json:"state"` // denied | conditional | allowed
	Reason string `json:"reason"`
}

func accessDecision(state, reason string) accessMeDecision {
	return accessMeDecision{State: state, Reason: reason}
}

// MyRoutineAccess describes the same role/capability and status gates as
// POST /run. A positive answer is conditional: dependency, input, spend and
// concurrency checks happen at dispatch and must not be guessed here.
func (h *PipelineHandler) MyRoutineAccess(w http.ResponseWriter, r *http.Request) {
	wsID, slug := WorkspaceIDFromContext(r.Context()), r.PathValue("slug")
	p, err := h.store.GetBySlug(r.Context(), wsID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "load routine access", err)
		return
	}
	role := RoleFromContext(r.Context())
	run := accessDecision("denied", "missing_role_or_capability")
	callerID := ""
	if user := UserFromContext(r.Context()); user != nil {
		callerID = user.ID
	}
	mayRun, accessErr := roleOrCapability(r.Context(), h.db, wsID, callerID, role, CapabilityRoutineRun, "create")
	if accessErr != nil {
		replyInternalError(w, h.logger, "load routine capability", accessErr)
		return
	}
	if mayRun {
		if pipeline.StatusRunnable(p.Status) {
			run = accessDecision("conditional", "runtime_preflight_required")
		} else {
			run = accessDecision("denied", "routine_"+p.Status)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	roleDecision := func(action string) accessMeDecision {
		if canRole(role, action) {
			return accessDecision("allowed", "role")
		}
		return accessDecision("denied", "missing_role")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"routine": slug,
		"actions": map[string]accessMeDecision{
			"read":             accessDecision("allowed", "workspace_visible"),
			"run":              run,
			"edit":             roleDecision("create"),
			"approve":          roleDecision("create"),
			"disable":          roleDecision("manage"),
			"replay":           roleDecision("create"),
			"manage_schedules": roleDecision("create"),
		},
	})
}

// MyCredentialAccess only answers for a row visible under the same SQL filter
// as GET /credentials/{id}; a hidden row and a missing row are both 404.
// It never reads or returns the encrypted value.
func (h *CredentialHandler) MyCredentialAccess(w http.ResponseWriter, r *http.Request) {
	wsID, id := WorkspaceIDFromContext(r.Context()), r.PathValue("credentialId")
	role, user := RoleFromContext(r.Context()), UserFromContext(r.Context())
	filter, filterArgs := credentialVisibilityFilter(role, user)
	args := append([]any{id, wsID}, filterArgs...)
	var credType, provider, sensitivity string
	err := h.db.QueryRowContext(r.Context(), `SELECT c.type, COALESCE(c.provider,''), COALESCE(c.sensitivity,'STANDARD')
		FROM credentials c WHERE c.id=? AND c.workspace_id=? AND c.deleted_at IS NULL`+filter, args...).Scan(&credType, &provider, &sensitivity)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "Credential not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "load credential access", err)
		return
	}

	edit := accessDecision("denied", "missing_role")
	if canRole(role, "update") && (!isLoginRow(credType, provider) || canRole(role, "manage")) {
		edit = accessDecision("allowed", "role")
	}
	bind := accessDecision("denied", "missing_role")
	if canRole(role, "manage") {
		bind = accessDecision("allowed", "role")
	}
	rotate := accessDecision("denied", "missing_role_or_capability")
	callerID := ""
	if user != nil {
		callerID = user.ID
	}
	mayRotate, rotateErr := roleOrCapability(r.Context(), h.db, wsID, callerID, role, CapabilityCredentialRotate, "manage")
	if rotateErr != nil {
		replyInternalError(w, h.logger, "load rotate capability", rotateErr)
		return
	}
	if mayRotate {
		rotate = accessDecision("conditional", "runtime_preflight_required")
	}
	deleteDecision := accessDecision("denied", "missing_role")
	if canRole(role, "manage") {
		deleteDecision = accessDecision("conditional", "runtime_preflight_required")
	}
	reveal := accessDecision("denied", "non_interactive_auth")
	if user != nil && AuthKindFromContext(r.Context()) == AuthKindSession {
		enabled, policyErr := revealEnabledForWorkspace(r.Context(), h.db, wsID)
		if policyErr != nil {
			replyInternalError(w, h.logger, "load reveal policy", policyErr)
			return
		}
		switch {
		case !enabled:
			reveal = accessDecision("denied", "workspace_switch_off")
		case !canRole(role, revealRoleFloor):
			reveal = accessDecision("denied", "below_role_floor")
		default:
			caps, _, capErr, member := CapabilitiesForMemberE(r.Context(), h.db, wsID, user.ID)
			if capErr != nil {
				replyInternalError(w, h.logger, "load reveal capability", capErr)
				return
			}
			if !member || !HasCapability(caps, CapabilityCredentialReveal) {
				reveal = accessDecision("denied", "missing_capability")
			} else {
				inScope, scopeErr := revealScopeAllows(r.Context(), h.db, role, user, wsID, id)
				if scopeErr != nil {
					replyInternalError(w, h.logger, "load reveal scope", scopeErr)
					return
				}
				switch {
				case !inScope:
					reveal = accessDecision("denied", "outside_crew_scope")
				case sensitivity == SensitivitySealed:
					reveal = accessDecision("denied", "sealed")
				default:
					reveal = accessDecision("conditional", "fresh_login_reason_and_audit_required")
				}
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"credential_id": id,
		"actions": map[string]accessMeDecision{
			"read":              accessDecision("allowed", "visible_metadata"),
			"edit":              edit,
			"manage_bindings":   bind,
			"rotate":            rotate,
			"reveal":            reveal,
			"delete":            deleteDecision,
			"lower_sensitivity": bind,
		},
	})
}
