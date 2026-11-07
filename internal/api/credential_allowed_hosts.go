package api

// Human-approved hosts for a credential (#5045).
//
// WHAT THIS GUARANTEES. A credential with a non-empty allowed_hosts list is
// "host-bound". The runtime delivers a host-bound credential only when the
// agent's crew is in restricted network mode AND every approved host is
// permitted by the crew's effective egress allowlist. Otherwise the credential
// is withheld from delivery (fail closed) and the readiness report says why.
//
// WHAT THIS DOES NOT GUARANTEE. Delivery is a file in /secrets and, for
// non-model HTTPS, an opaque tunnel through the proxy. An agent holding the
// delivered token can still send it to any host the crew allowlist permits.
// This is a delivery precondition on the crew's network policy; it is not a
// control on where the credential is used. Do not describe it as one.
//
// WHO CAN SET IT. Only a human operator (interactive session or an unscoped
// CLI token held by an OWNER/ADMIN). A run token, a scoped token, or a context
// that did not pass RequireAuth cannot. Agent-proposed hosts (escalation
// `hosts`) are review metadata and are never copied into this column.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/credhostgate"
	"github.com/crewship-ai/crewship/internal/credhosts"
)

const allowedHostsLimitNote = "Allowed hosts decide whether this credential is delivered: it is withheld unless the crew " +
	"is restricted and its egress allowlist permits every listed host. This does not stop an agent that holds the " +
	"token from sending it to any other host the crew allowlist permits. A change applies to the next run or config " +
	"read; a running sidecar drops its copy within about 60 seconds and /secrets files are removed from running crews " +
	"that no longer qualify, but environment variables already set in a running process are not recalled."

type allowedHostsResponse struct {
	CredentialID string   `json:"credential_id"`
	AllowedHosts []string `json:"allowed_hosts"`
	Previous     []string `json:"previous,omitempty"`
	Note         string   `json:"note"`
}

// humanOperatorRequest reports whether the request was authenticated as a
// person: an interactive session or an unrestricted CLI token. Absence of an
// auth kind, a scoped token and an internal token all deny.
func humanOperatorRequest(r *http.Request) bool {
	ctx := r.Context()
	u := UserFromContext(ctx)
	if u == nil || u.ID == "" {
		return false
	}
	kind := AuthKindFromContext(ctx)
	if kind != AuthKindSession && kind != AuthKindCLIToken {
		return false
	}
	if ctx.Value(ctxTokenScopes) != nil && !canScope(ctx, "*") {
		return false
	}
	return InternalTokenWorkspaceFromContext(ctx) == ""
}

func parseStoredHosts(raw string) ([]string, error) { return credhostgate.ParseStored(raw) }

// GetAllowedHosts handles GET /api/v1/credentials/{credentialId}/allowed-hosts.
func (h *CredentialHandler) GetAllowedHosts(w http.ResponseWriter, r *http.Request) {
	credID := r.PathValue("credentialId")
	var raw string
	// Same visibility as reading the credential itself.
	visFilter, visArgs := credentialVisibilityFilter(RoleFromContext(r.Context()), UserFromContext(r.Context()))
	err := h.db.QueryRowContext(r.Context(),
		`SELECT COALESCE(c.allowed_hosts, '[]') FROM credentials c WHERE c.id = ? AND c.workspace_id = ? AND c.deleted_at IS NULL`+visFilter,
		append([]any{credID, WorkspaceIDFromContext(r.Context())}, visArgs...)...).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "Credential not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "allowed hosts: load", err)
		return
	}
	hosts, perr := parseStoredHosts(raw)
	if perr != nil {
		// Show the raw state rather than hide it; delivery treats it as bound
		// and unreadable, so the operator needs to see something is wrong.
		replyError(w, http.StatusInternalServerError, "Stored allowed_hosts is unreadable; set it again")
		return
	}
	if hosts == nil {
		hosts = []string{}
	}
	writeJSON(w, http.StatusOK, allowedHostsResponse{CredentialID: credID, AllowedHosts: hosts, Note: allowedHostsLimitNote})
}

// SetAllowedHosts handles PUT /api/v1/credentials/{credentialId}/allowed-hosts.
// The body replaces the list; an empty list unbinds the credential.
func (h *CredentialHandler) SetAllowedHosts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	credID := r.PathValue("credentialId")
	workspaceID := WorkspaceIDFromContext(ctx)
	role := RoleFromContext(ctx)
	callerID := ""
	if u := UserFromContext(ctx); u != nil {
		callerID = u.ID
	}
	// Setting, widening and clearing all change what a human approved, so one
	// bar applies to every direction: OWNER/ADMIN acting as a human.
	if !requireRoleOrForbid(w, h.logger, callerID, role, "credential.allowed_hosts", "credential:"+credID, "manage") {
		return
	}
	if !humanOperatorRequest(r) {
		h.logger.Warn("credential allowed_hosts change refused: caller is not a human operator",
			"credential_id", credID, "user_id", callerID, "auth_kind", AuthKindFromContext(ctx))
		replyError(w, http.StatusForbidden, "Allowed hosts can only be set by a signed-in person or an unrestricted operator token")
		return
	}

	var body struct {
		AllowedHosts *[]string `json:"allowed_hosts"`
	}
	if err := readJSON(r, &body); err != nil || body.AllowedHosts == nil {
		replyError(w, http.StatusBadRequest, `Body must be {"allowed_hosts": [...]}`)
		return
	}
	hosts, err := credhosts.NormalizeList(*body.AllowedHosts)
	if err != nil {
		replyError(w, http.StatusBadRequest, "Invalid allowed_hosts: "+err.Error())
		return
	}

	var prevRaw string
	err = h.db.QueryRowContext(ctx,
		`SELECT COALESCE(allowed_hosts, '[]') FROM credentials WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		credID, workspaceID).Scan(&prevRaw)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "Credential not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "allowed hosts: load", err)
		return
	}
	prev, prevErr := parseStoredHosts(prevRaw)

	enc, _ := json.Marshal(hosts)
	// An unreadable list was fail-closed, so replacing it is always a change.
	changed := prevErr != nil || !equalHostLists(prev, hosts)
	// The write and its audit row commit together: a change a person made is
	// never missing from the timeline, and an unrecordable change is refused.
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		replyInternalError(w, h.logger, "allowed hosts: begin", err)
		return
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE credentials SET allowed_hosts = ?, updated_at = ? WHERE id = ? AND workspace_id = ?`,
		string(enc), time.Now().UTC().Format(time.RFC3339), credID, workspaceID); err != nil {
		_ = tx.Rollback()
		replyInternalError(w, h.logger, "allowed hosts: write", err)
		return
	}
	if changed {
		action := "changed"
		switch {
		case len(prev) == 0:
			action = "set"
		case len(hosts) == 0:
			action = "cleared"
		}
		if recErr := RecordCredentialEventTx(ctx, tx, credID, AuditEventAllowedHosts, "", clientIP(r), map[string]any{
			"action":        action,
			"previous":      nonNilHosts(prev),
			"allowed_hosts": nonNilHosts(hosts),
			"changed_by":    callerID,
			"auth_kind":     AuthKindFromContext(ctx),
		}); recErr != nil {
			_ = tx.Rollback()
			replyInternalError(w, h.logger, "allowed hosts: audit", recErr)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "allowed hosts: commit", err)
		return
	}
	if changed {
		// audit_logs is the long-lived trail (credential_audit is pruned by the
		// retention sweep); both carry the old and the new list.
		auditFromRequest(r, h.db, "credential.allowed_hosts", "CREDENTIAL", credID, map[string]interface{}{
			"previous": nonNilHosts(prev), "allowed_hosts": nonNilHosts(hosts),
		})
		// A tightened list must not leave the value in running containers.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		h.reconcileHostBoundCredential(rctx, credID, workspaceID)
	}
	if hosts == nil {
		hosts = []string{}
	}
	writeJSON(w, http.StatusOK, allowedHostsResponse{CredentialID: credID, AllowedHosts: hosts, Previous: prev, Note: allowedHostsLimitNote})
}

// hostBoundWithholds adapts the shared credhostgate evaluation to the
// readiness notice shape. The precondition itself lives in credhostgate so
// every delivery path (container, Keeper, MCP, OAuth, service env, routines,
// restricted runtime) evaluates the same rule.
func hostBoundWithholds(ctx context.Context, db sqlQuerier, agentID string, ids []string) (map[string]deliveredSlotNotice, error) {
	w, err := credhostgate.Withholds(ctx, db, agentID, ids)
	if err != nil {
		return nil, err
	}
	return noticesFromWithholds(w), nil
}

const hostBoundNoticeSuffix = "; it is withheld rather than delivered unbound. This is a delivery precondition, not a control on where an agent can send the token"

func noticesFromWithholds(w map[string]credhostgate.Withhold) map[string]deliveredSlotNotice {
	out := map[string]deliveredSlotNotice{}
	for id, n := range w {
		out[id] = deliveredSlotNotice{
			CredentialID: id, HostBound: true, Hosts: n.Hosts,
			Reason: "it is bound to approved hosts but " + n.Reason + hostBoundNoticeSuffix,
		}
	}
	return out
}

func nonNilHosts(h []string) []string {
	if h == nil {
		return []string{}
	}
	return h
}

func equalHostLists(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
