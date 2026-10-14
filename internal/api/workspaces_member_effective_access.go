package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/restrictedworkflow"
)

type memberAccessDecision struct {
	access.Right
	Name   string `json:"name,omitempty"`
	State  string `json:"state"`
	Reason string `json:"reason"`
}

type memberEffectiveAccess struct {
	access.Membership
	AccountRestricted bool                   `json:"account_restricted"`
	RestrictionScope  string                 `json:"restriction_scope"`
	Decisions         []memberAccessDecision `json:"decisions"`
	Explanation       string                 `json:"explanation"`
}

// GetMemberEffectiveAccess explains current resource grants without granting
// the administrator the target's private conversations, prompts or results.
// GET /api/v1/workspaces/{workspaceId}/members/{memberId}/access/effective
func (h *WorkspaceHandler) GetMemberEffectiveAccess(w http.ResponseWriter, r *http.Request) {
	actor, user, workspace, ok := h.memberAccessTarget(w, r)
	if !ok {
		return
	}
	store := access.Store{DB: h.db}
	p, err := store.Policy(r.Context(), actor, user, workspace)
	if err != nil {
		h.memberAccessError(w, err)
		return
	}
	if p.ID != r.PathValue("memberId") {
		h.memberAccessError(w, access.ErrDenied)
		return
	}
	h.writeEffectiveAccess(w, r, actor, user, workspace, p, false)
}

// GetOwnEffectiveAccess explains only the authenticated member's access.
// GET /api/v1/workspaces/{workspaceId}/access/me
func (h *WorkspaceHandler) GetOwnEffectiveAccess(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	caller := UserFromContext(r.Context())
	workspace, matches := memberAccessWorkspace(r)
	if caller == nil || !matches {
		h.memberAccessError(w, access.ErrDenied)
		return
	}
	p, err := (access.Store{DB: h.db}).OwnPolicy(r.Context(), caller.ID, workspace)
	if err != nil {
		h.memberAccessError(w, err)
		return
	}
	h.writeEffectiveAccess(w, r, caller.ID, caller.ID, workspace, p, true)
}

func (h *WorkspaceHandler) writeEffectiveAccess(w http.ResponseWriter, r *http.Request, actor, user, workspace string, p access.Policy, own bool) {
	store := access.Store{DB: h.db}
	restricted, err := store.HasRestrictedMembership(r.Context(), user)
	if err != nil {
		h.memberAccessError(w, err)
		return
	}
	result := memberEffectiveAccess{Membership: p.Membership, AccountRestricted: restricted, RestrictionScope: "account", Decisions: []memberAccessDecision{}}
	result.Explanation = "Trusted access follows the workspace role and capabilities. Private conversations keep their own audience."
	if restricted {
		result.Explanation = "Guest restrictions apply across this account. Unshared resources and unintegrated routes are denied. A routine requires its own run grant and every agent dependency; a grant does not install an execution runtime."
	}
	authorized := map[string]bool{}
	catalogDenied := false
	if p.Mode == "restricted" {
		catalog, e := restrictedworkflow.AuthorizedCatalog(r.Context(), h.db, user, workspace)
		if e != nil && !errors.Is(e, restrictedworkflow.ErrDenied) {
			h.memberAccessError(w, e)
			return
		}
		catalogDenied = errors.Is(e, restrictedworkflow.ErrDenied)
		for _, item := range catalog {
			authorized[item.ID] = true
		}
	}
	var targetRole string
	if err = h.db.QueryRowContext(r.Context(), `SELECT role FROM workspace_members WHERE id=? AND workspace_id=?`, p.ID, workspace).Scan(&targetRole); err != nil {
		h.memberAccessError(w, err)
		return
	}
	for _, right := range p.Rights {
		decision := memberAccessDecision{Right: right, State: "allowed", Reason: "explicit_resource_grant"}
		// Names are read only for currently granted local resources. The final
		// membership fence also covers changes while building this explanation.
		grantErr := store.Check(r.Context(), user, workspace, right)
		if grantErr == nil {
			decision.Name, grantErr = effectiveAccessResourceName(r.Context(), h.db, workspace, right)
		}
		if err = grantErr; err != nil {
			if !errors.Is(err, access.ErrDenied) {
				h.memberAccessError(w, err)
				return
			}
			decision.State = "denied"
			decision.Reason = "resource_unavailable"
			if errors.Is(err, access.ErrIssuerDenied) {
				decision.Reason = "The grant issuer no longer has active trusted administrator authority in this workspace."
			}
		} else if right.Kind == "routine" && !authorized[right.ID] {
			decision.State = "denied"
			decision.Reason = "Requires current agent run/delegate grants, provider authority and a supported active definition."
			if catalogDenied {
				decision.Reason = "Current account or invocation authority is unavailable or changed; reload the policy and account status."
			}
		} else if right.Kind == "routine" && (h.restrictedWorkflow == nil || h.restrictedWorkflow() == nil) {
			decision.State = "unavailable"
			decision.Reason = "Authorized; private execution is not installed on this server."
		} else if right.Kind == "routine" {
			decision.State = "conditional"
			decision.Reason = "Routine and agent grants are current; inputs, budget and runtime readiness are checked at invocation."
		} else if right.Kind == "project" && (right.Operation == "write" || right.Operation == "delete") && !canRole(targetRole, "create") {
			decision.State = "denied"
			decision.Reason = "Project file writes still require the MANAGER role or higher."
		} else if right.Kind == "project" {
			decision.Reason = "Project operation granted. List opens the project’s issue catalog; read opens its issue details. Write/delete currently apply to project files."
		} else if right.Kind == "agent" && right.Operation == "chat" {
			decision.State = "conditional"
			decision.Reason = "Chat granted for this agent; discovery is separate and other people's conversations stay private."
		} else if right.Kind == "agent" && (right.Operation == "run" || right.Operation == "delegate") {
			decision.State = "conditional"
			decision.Reason = "Agent operation granted; a supported execution profile, current provider authority and invocation policy remain required."
		}
		result.Decisions = append(result.Decisions, decision)
	}
	var current access.Policy
	if own {
		current, err = store.OwnPolicy(r.Context(), user, workspace)
	} else {
		current, err = store.Policy(r.Context(), actor, user, workspace)
	}
	if err != nil {
		h.memberAccessError(w, err)
		return
	}
	if current.Membership != p.Membership {
		h.memberAccessError(w, access.ErrConflict)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// A resource can disappear after Check succeeds without changing membership
// revision. Treat a missing name as a denied row, not an endpoint failure.
func effectiveAccessResourceName(ctx context.Context, db *sql.DB, workspace string, right access.Right) (string, error) {
	query := ""
	switch right.Kind {
	case "agent":
		query = `SELECT name FROM agents WHERE id=? AND workspace_id=? AND deleted_at IS NULL`
	case "routine":
		query = `SELECT name FROM pipelines WHERE id=? AND workspace_id=? AND deleted_at IS NULL`
	case "project":
		query = `SELECT name FROM projects WHERE id=? AND workspace_id=?`
	}
	if query == "" {
		return "", access.ErrDenied
	}
	var name string
	err := db.QueryRowContext(ctx, query, right.ID, workspace).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", access.ErrDenied
	}
	return name, err
}
