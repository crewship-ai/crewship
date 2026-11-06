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
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/credhosts"
	"github.com/crewship-ai/crewship/internal/egresspolicy"
)

const allowedHostsLimitNote = "Allowed hosts decide whether this credential is delivered: it is withheld unless the crew " +
	"is restricted and its egress allowlist permits every listed host. This does not stop an agent that holds the " +
	"token from sending it to any other host the crew allowlist permits."

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

func parseStoredHosts(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var hosts []string
	if err := json.Unmarshal([]byte(raw), &hosts); err != nil {
		return nil, err
	}
	return credhosts.NormalizeList(hosts)
}

// GetAllowedHosts handles GET /api/v1/credentials/{credentialId}/allowed-hosts.
func (h *CredentialHandler) GetAllowedHosts(w http.ResponseWriter, r *http.Request) {
	credID := r.PathValue("credentialId")
	var raw string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT COALESCE(allowed_hosts, '[]') FROM credentials WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		credID, WorkspaceIDFromContext(r.Context())).Scan(&raw)
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
	prev, _ := parseStoredHosts(prevRaw)

	enc, _ := json.Marshal(hosts)
	if _, err := h.db.ExecContext(ctx,
		`UPDATE credentials SET allowed_hosts = ?, updated_at = ? WHERE id = ? AND workspace_id = ?`,
		string(enc), time.Now().UTC().Format(time.RFC3339), credID, workspaceID); err != nil {
		replyInternalError(w, h.logger, "allowed hosts: write", err)
		return
	}
	h.logger.Info("credential allowed_hosts changed", "credential_id", credID, "user_id", callerID,
		"from", len(prev), "to", len(hosts))
	if hosts == nil {
		hosts = []string{}
	}
	writeJSON(w, http.StatusOK, allowedHostsResponse{CredentialID: credID, AllowedHosts: hosts, Previous: prev, Note: allowedHostsLimitNote})
}

// hostBoundWithholds returns, for the delivered credentials that carry an
// approved host list, the ones that must NOT be delivered and why. It is the
// only place the precondition is evaluated, called from the delivery
// chokepoint so every path (boot, delegation, peer query, readiness) agrees.
func hostBoundWithholds(ctx context.Context, db sqlQuerier, agentID string, ids []string) (map[string]deliveredSlotNotice, error) {
	out := map[string]deliveredSlotNotice{}
	if len(ids) == 0 {
		return out, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, COALESCE(allowed_hosts, '[]') FROM credentials WHERE id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("load credential allowed hosts: %w", err)
	}
	type bound struct {
		id    string
		hosts []string
		bad   bool
	}
	var boundCreds []bound
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "[]" {
			continue
		}
		hosts, perr := parseStoredHosts(raw)
		boundCreds = append(boundCreds, bound{id: id, hosts: hosts, bad: perr != nil})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(boundCreds) == 0 {
		return out, nil
	}

	// Resolve the crew boundary once. Any failure to read it withholds.
	var pol egresspolicy.Effective
	var polErr error
	crewRows, err := db.QueryContext(ctx, `SELECT COALESCE(crew_id, '') FROM agents WHERE id = ?`, agentID)
	if err != nil {
		return nil, fmt.Errorf("load agent crew: %w", err)
	}
	crewID := ""
	if crewRows.Next() {
		_ = crewRows.Scan(&crewID)
	}
	crewRows.Close()
	if crewID != "" {
		pol, polErr = egresspolicy.ResolveEffective(ctx, db, crewID)
	}

	const suffix = "; it is withheld rather than delivered unbound. This is a delivery precondition, not a control on where an agent can send the token"
	for _, b := range boundCreds {
		reason := ""
		switch {
		case b.bad || len(b.hosts) == 0:
			reason = "its approved host list is unreadable"
		case crewID == "":
			reason = "the agent is not in a crew, so no egress allowlist applies to it"
		case polErr != nil:
			reason = "the crew's network policy could not be read"
		case !pol.Restricted:
			reason = "the crew has no egress allowlist (network mode is free)"
		default:
			var missing []string
			for _, h := range b.hosts {
				if !credhosts.Covered(pol.Allow, h) {
					missing = append(missing, h)
				}
			}
			if len(missing) > 0 {
				reason = "these approved hosts are not in the crew's effective egress allowlist: " + strings.Join(missing, ", ")
			}
		}
		if reason == "" {
			continue
		}
		out[b.id] = deliveredSlotNotice{
			CredentialID: b.id, HostBound: true, Hosts: b.hosts,
			Reason: "it is bound to approved hosts but " + reason + suffix,
		}
	}
	return out, nil
}
