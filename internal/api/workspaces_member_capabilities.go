package api

// Admin API for granting / revoking per-member capabilities
// (PRD-SLASH-CAPABILITIES-2026 §6.7 + §6.8).
//
// Two operations on workspace_members.capabilities:
//
//   GET   /api/v1/workspaces/{workspaceId}/members/{memberId}/capabilities
//   PATCH /api/v1/workspaces/{workspaceId}/members/{memberId}/capabilities
//
// PATCH accepts one of three body shapes:
//
//   {"set":     ["chat", "routine.create"]}   — replace entire set
//   {"grant":   ["routine.create"]}           — add these
//   {"revoke":  ["credential.create"]}        — remove these
//   {"preset":  "power"}                      — apply named bundle
//
// "set" is the canonical shape for the Members grid: the UI sends
// the post-edit state of the row. "grant"/"revoke" exist so the
// CLI admin commands (commit 8) can do incremental edits without
// having to read-modify-write. "preset" is the quick-pick chip.
//
// All require caller role ≥ ADMIN. OWNER capability rows are
// locked — the handler 403s any attempt to mutate an OWNER's
// capabilities so a rogue ADMIN can't downgrade an OWNER. Caller
// cannot mutate their own capabilities either (defence against a
// downgrade-then-restore stunt).
//
// Every mutation writes an audit row (workspace_member.capability_*)
// with old + new set so the trail is reconstructable.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
)

type capabilitiesGetResponse struct {
	UserID       string   `json:"user_id"`
	Role         string   `json:"role"`
	Capabilities []string `json:"capabilities"`
}

// capabilitiesBulkResponse is the shape returned by the workspace-
// wide bulk endpoint. One entry per workspace_members row. Order
// stable (membership.created_at ASC) so the Members grid renders
// the same row order between page loads.
type capabilitiesBulkResponse struct {
	Members []capabilitiesGetResponse `json:"members"`
}

type capabilitiesPatchRequest struct {
	Set    []string `json:"set,omitempty"`
	Grant  []string `json:"grant,omitempty"`
	Revoke []string `json:"revoke,omitempty"`
	Preset string   `json:"preset,omitempty"`
}

// patchCapabilitiesMaxBodyBytes caps the PATCH request body.
// Capability strings top out at ~30 chars; even a maximal body
// (all four shapes filled, every capability included) is well
// under 1 KB. 16 KB leaves room for future fields without giving
// an attacker a multi-GB streaming target.
const patchCapabilitiesMaxBodyBytes = 16 * 1024

// ListMembersCapabilities is the bulk variant — one SELECT, one
// response, all members of the workspace. Drives the Members
// capability grid in a single round-trip instead of the N+1 fan-out
// the per-member endpoint would force (a 500-member workspace = 500
// HTTP calls + 500 admin role checks).
//
// Admin-only, mirrors GetMemberCapabilities. resolveCapabilitiesFromRow
// runs per row so the same edge-case semantics (explicit-empty,
// malformed, role fallback) apply to both surfaces.
//
// Order is stable: ORDER BY wm.created_at ASC so the dashboard renders
// rows in the same order across page loads.
func (h *WorkspaceHandler) ListMembersCapabilities(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	caller := UserFromContext(r.Context())

	callerID := ""
	if caller != nil {
		callerID = caller.ID
	}
	if !requireRoleOrForbid(w, h.logger, callerID, role,
		"capability.read", "workspace:"+workspaceID, "manage") {
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT user_id, role, capabilities
		FROM workspace_members
		WHERE workspace_id = ?
		ORDER BY created_at ASC
	`, workspaceID)
	if err != nil {
		h.logger.Error("list members capabilities", "error", err, "workspace_id", workspaceID)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	out := capabilitiesBulkResponse{Members: []capabilitiesGetResponse{}}
	for rows.Next() {
		var userID, memberRole string
		var capsJSON sql.NullString
		if err := rows.Scan(&userID, &memberRole, &capsJSON); err != nil {
			h.logger.Error("scan member row", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		caps := resolveCapabilitiesFromRow(capsJSON, memberRole)
		out.Members = append(out.Members, capabilitiesGetResponse{
			UserID:       userID,
			Role:         memberRole,
			Capabilities: capsAsSortedSlice(caps),
		})
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// GetMemberCapabilities returns the parsed capability set + role.
// Admin-only — capability listing reveals the workspace's
// permission topology, which is operator-confidential.
func (h *WorkspaceHandler) GetMemberCapabilities(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	memberID := r.PathValue("memberId")
	role := RoleFromContext(r.Context())
	caller := UserFromContext(r.Context())

	callerID := ""
	if caller != nil {
		callerID = caller.ID
	}
	if !requireRoleOrForbid(w, h.logger, callerID, role,
		"capability.read", "workspace:"+workspaceID, "manage") {
		return
	}

	caps, memberRole, err := loadMemberCapabilitiesByMemberID(r.Context(), h.db, workspaceID, memberID)
	if err != nil {
		h.logger.Error("load member capabilities", "error", err, "member_id", memberID)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if memberRole == "" {
		replyError(w, http.StatusNotFound, "Member not found")
		return
	}
	writeJSON(w, http.StatusOK, capabilitiesGetResponse{
		UserID:       memberID,
		Role:         memberRole,
		Capabilities: capsAsSortedSlice(caps),
	})
}

// PatchMemberCapabilities mutates the capability set per the body
// shape. Locks: caller must be ADMIN+; target cannot be OWNER (UI
// shows OWNER columns as immutable); caller cannot mutate self.
func (h *WorkspaceHandler) PatchMemberCapabilities(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	memberID := r.PathValue("memberId")
	role := RoleFromContext(r.Context())
	caller := UserFromContext(r.Context())

	if caller == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if !requireRoleOrForbid(w, h.logger, caller.ID, role,
		"capability.manage", "workspace:"+workspaceID, "manage") {
		return
	}
	if memberID == caller.ID {
		// Defence: don't let the caller mutate their own row even
		// if they're ADMIN. Prevents a stunt where an admin removes
		// their own admin-only audit visibility before doing
		// something they don't want logged against them.
		replyError(w, http.StatusForbidden, "cannot modify own capabilities")
		return
	}

	current, targetRole, err := loadMemberCapabilitiesByMemberID(r.Context(), h.db, workspaceID, memberID)
	if err != nil {
		h.logger.Error("load member capabilities", "error", err, "member_id", memberID)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if targetRole == "" {
		replyError(w, http.StatusNotFound, "Member not found")
		return
	}
	if targetRole == "OWNER" {
		// OWNER capabilities are immutable — see wireframe
		// annotation. A workspace can have multiple OWNERs and we
		// don't want one OWNER pulling capabilities from another.
		replyError(w, http.StatusForbidden, "OWNER capabilities are immutable")
		return
	}

	// Cap the body before decoding — without this an ADMIN could
	// stream a multi-GB JSON document and pin the server. 16 KB is
	// generous for the actual shape (a maximally-filled body is well
	// under 1 KB).
	r.Body = http.MaxBytesReader(w, r.Body, patchCapabilitiesMaxBodyBytes)
	var body capabilitiesPatchRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			replyError(w, http.StatusRequestEntityTooLarge, "Request body too large")
			return
		}
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	next, err := applyCapabilityMutation(current, body)
	if err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Diff-no-op short-circuit: if the post-mutation set equals
	// the current set, skip the write + audit. Saves an audit-log
	// row when the UI's optimistic update sends a no-op.
	if capabilitySetsEqual(current, next) {
		writeJSON(w, http.StatusOK, capabilitiesGetResponse{
			UserID:       memberID,
			Role:         targetRole,
			Capabilities: capsAsSortedSlice(next),
		})
		return
	}

	serialized := SerializeCapabilities(next)
	result, err := h.db.ExecContext(r.Context(),
		`UPDATE workspace_members SET capabilities = ?, updated_at = datetime('now') WHERE workspace_id = ? AND user_id = ?`,
		serialized, workspaceID, memberID)
	if err != nil {
		h.logger.Error("update capabilities", "error", err, "member_id", memberID)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	// between the load (above) and the UPDATE, the
	// member row can have been deleted by a concurrent RemoveMember
	// call. SQL UPDATE on a no-longer-existing row returns nil error
	// but 0 rows affected; without this check we'd 200 + emit an
	// audit row claiming the mutation succeeded. 404 makes the
	// race visible to the operator. RowsAffected itself can
	// theoretically error on a future driver — we surface that as
	// 500 rather than guess.
	affected, err := result.RowsAffected()
	if err != nil {
		h.logger.Error("rows affected", "error", err, "member_id", memberID)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if affected == 0 {
		// Member row vanished between the load (line above) and the
		// UPDATE (concurrent RemoveMember). Emit the audit BEFORE
		// returning so an operator scanning the log sees the
		// mutation attempt and its outcome — silent failure here
		// would hide capability changes from forensic review.
		h.logger.Info("rbac: capability mutation aborted (race)",
			"actor_user_id", caller.ID,
			"target_user_id", memberID,
			"workspace_id", workspaceID,
			"action", "capability.patch.race_aborted",
			"old", capsAsSortedSlice(current),
			"attempted", capsAsSortedSlice(next),
		)
		replyError(w, http.StatusNotFound, "Member not found (deleted concurrently)")
		return
	}

	// Cache invalidate so the target user sees the new set on their
	// next palette open without waiting up to 30 s.
	InvalidateCapabilityCache(workspaceID, memberID)

	h.logger.Info("rbac: capability mutation",
		"actor_user_id", caller.ID,
		"target_user_id", memberID,
		"workspace_id", workspaceID,
		"action", "capability.patch",
		"old", capsAsSortedSlice(current),
		"new", capsAsSortedSlice(next),
	)

	writeJSON(w, http.StatusOK, capabilitiesGetResponse{
		UserID:       memberID,
		Role:         targetRole,
		Capabilities: capsAsSortedSlice(next),
	})
}

// loadMemberCapabilitiesByMemberID reads the parsed capability set
// and role for one membership row. Returns empty role when no row
// exists so the caller can 404 distinctly from a member with empty
// caps.
//
// Thin wrapper around loadCapabilitiesForMember — both used to have
// independent SELECT + resolve logic that diverged on the
// explicit-empty edge case (read API returned role bundle, runtime
// gate returned chat-only). Sharing the implementation guarantees
// the read API shows exactly what the runtime will enforce.
func loadMemberCapabilitiesByMemberID(ctx context.Context, db *sql.DB, workspaceID, userID string) (map[string]struct{}, string, error) {
	return loadCapabilitiesForMember(ctx, db, workspaceID, userID)
}

// applyCapabilityMutation interprets a patch body and returns the
// resulting capability set. Exactly one of set/grant/revoke/preset
// may be present — multiple is a 400 (intent ambiguous).
//
// Empty arrays are rejected as bad shape, not silently treated as
// no-ops. A previous version counted `body.Set != nil` (which fires
// for `[]`) but `len(body.Grant) > 0` (which doesn't), so
// `{"set":[]}` slipped past the "multiple shapes" check, hit the
// set branch, and wrote chat-only — silently resetting the member's
// capabilities. Now every shape uses len() so the counter is
// symmetric: `set:[]`, `grant:[]`, `revoke:[]` all error as "missing
// shape" rather than executing as a destructive no-op. Operators
// who really want chat-only must send `{"set":["chat"]}` (explicit).
func applyCapabilityMutation(current map[string]struct{}, body capabilitiesPatchRequest) (map[string]struct{}, error) {
	// Tally which fields were used so we can reject "multiple
	// shapes in one request" cleanly. len() everywhere so an empty
	// array counts as absent — preventing the silent-reset footgun.
	shapes := 0
	if len(body.Set) > 0 {
		shapes++
	}
	if len(body.Grant) > 0 {
		shapes++
	}
	if len(body.Revoke) > 0 {
		shapes++
	}
	if body.Preset != "" {
		shapes++
	}
	if shapes == 0 {
		return nil, errBadCapabilityBody("body must contain one of: set, grant, revoke, preset (with non-empty value)")
	}
	if shapes > 1 {
		return nil, errBadCapabilityBody("body must contain exactly one of: set, grant, revoke, preset")
	}

	switch {
	case len(body.Set) > 0:
		// Whole-row replace. Validate every entry.
		next := make(map[string]struct{}, len(body.Set))
		for _, c := range body.Set {
			if !IsValidCapability(c) {
				return nil, errBadCapabilityBody("unknown capability: " + c)
			}
			next[c] = struct{}{}
		}
		// chat is always implied. Strip explicit chat to keep the
		// stored form canonical (the implied path in HasCapability
		// already grants chat).
		next[CapabilityChat] = struct{}{}
		return next, nil

	case len(body.Grant) > 0:
		next := copyCapSet(current)
		for _, c := range body.Grant {
			if !IsValidCapability(c) {
				return nil, errBadCapabilityBody("unknown capability: " + c)
			}
			next[c] = struct{}{}
		}
		return next, nil

	case len(body.Revoke) > 0:
		next := copyCapSet(current)
		for _, c := range body.Revoke {
			if !IsValidCapability(c) {
				return nil, errBadCapabilityBody("unknown capability: " + c)
			}
			if c == CapabilityChat {
				// Revoking chat is a no-op (always implied); reject
				// explicitly so the caller knows their intent has no
				// effect rather than silently doing nothing.
				return nil, errBadCapabilityBody("chat is implied and cannot be revoked; remove the member instead")
			}
			delete(next, c)
		}
		return next, nil

	case body.Preset != "":
		caps := BundleCapabilities(CapabilityBundle(body.Preset))
		if caps == nil {
			return nil, errBadCapabilityBody("unknown preset: " + body.Preset)
		}
		next := make(map[string]struct{}, len(caps))
		for _, c := range caps {
			next[c] = struct{}{}
		}
		return next, nil
	}
	// Unreachable per shapes check above.
	return nil, errBadCapabilityBody("internal: no shape branch matched")
}

// errBadCapabilityBody is a tiny sentinel so the handler can route
// validation errors uniformly back as 400 without leaking server
// internals.
type errBadCapabilityBody string

func (e errBadCapabilityBody) Error() string { return string(e) }

func copyCapSet(s map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(s))
	for k := range s {
		out[k] = struct{}{}
	}
	return out
}

func capabilitySetsEqual(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func capsAsSortedSlice(s map[string]struct{}) []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
