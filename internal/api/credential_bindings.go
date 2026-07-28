package api

// Credential bindings — (scope, slot) → credential (PRD-CREDENTIALS-V2 §2.5b).
//
// A binding answers "which account lands in this container, under which
// environment variable". Before it existed, credentials.name answered both
// questions at once and carried UNIQUE(workspace_id, name), so a workspace
// could hold exactly one GitHub account: a second one would also have had to
// be called GH_TOKEN.
//
// The name stays the human identity of one account (github-acme) and keeps its
// UNIQUE. The env var — the SLOT — moves here, where it can differ per scope.
// Ten crews bind GH_TOKEN to ten different accounts and every crew's agents
// boot with their own.
//
// The invariant this file exists to protect: within one scope, one slot
// resolves to exactly one credential. It is enforced twice on purpose — by
// idx_credential_bindings_slot in the schema, and by the 409 below. The index
// is what makes it true; the 409 is what makes it explainable. A write that
// collides is REFUSED rather than replacing the previous row, because a silent
// last-write-wins would repoint every agent in a crew at a different GitHub
// account with no request having said so.
//
// Resolution across scopes (agent > crew > workspace) is not here — it lives in
// credential_delivery.go, the single query all three delivery paths already
// call. This file only writes and reads rows, plus one read-only view
// (ResolveForAgent) that reports what that query will decide.

import (
	"database/sql"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Binding scopes, most specific first.
const (
	bindingScopeAgent     = "AGENT"
	bindingScopeCrew      = "CREW"
	bindingScopeWorkspace = "WORKSPACE"
)

// Source labels for the resolution view. These name the ROW that won, which is
// the question a user debugging "why is this agent pushing as the wrong bot?"
// is actually asking.
const (
	bindingSourceAgent      = "agent_binding"
	bindingSourceCrew       = "crew_binding"
	bindingSourceWorkspace  = "workspace_binding"
	bindingSourceAgentGrant = "agent_grant"
	bindingSourceCrewLink   = "crew_link"
)

// slotNameRE is the shape of a POSIX-ish environment variable name. Enforced
// because a slot is exported into a container: a name with a space or an `=` is
// silently dropped by the runtime, which presents to the user as "the binding
// is configured and the tool still can't see it".
var slotNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// CredentialBindingHandler serves the binding CRUD and the per-agent
// resolution view.
type CredentialBindingHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewCredentialBindingHandler(db *sql.DB, logger *slog.Logger) *CredentialBindingHandler {
	return &CredentialBindingHandler{db: db, logger: logger}
}

type credentialBindingResponse struct {
	ID           string `json:"id"`
	CredentialID string `json:"credential_id"`
	// CredentialName is denormalised into the response deliberately. Without
	// it the slot map reads as a list of opaque ids, and the entire point of
	// §2.5b is that a human can tell github-acme from github-globex.
	CredentialName string  `json:"credential_name"`
	Scope          string  `json:"scope"`
	CrewID         *string `json:"crew_id"`
	AgentID        *string `json:"agent_id"`
	Slot           string  `json:"slot"`
	CreatedAt      string  `json:"created_at"`
}

type createBindingRequest struct {
	CredentialID string `json:"credential_id"`
	Scope        string `json:"scope"`
	CrewID       string `json:"crew_id"`
	AgentID      string `json:"agent_id"`
	Slot         string `json:"slot"`
}

// resolvedSlot is one line of the per-agent view: the env var, the account that
// will fill it, and which rule decided. Never the value — this route is a map,
// not a reveal (§2.6 L9: agents and listings never disclose).
type resolvedSlot struct {
	Slot           string `json:"slot"`
	CredentialID   string `json:"credential_id"`
	CredentialName string `json:"credential_name"`
	Source         string `json:"source"`
}

// List GET /api/v1/credentials/bindings
//
// Optional filters: scope, crew_id, agent_id, credential_id, slot. The
// workspace predicate is not optional — a binding list is a description of
// where a tenant's secrets go, so a query that forgot it would enumerate every
// tenant's slot map.
func (h *CredentialBindingHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusBadRequest, "Workspace context required")
		return
	}

	q := r.URL.Query()
	where := []string{"b.workspace_id = ?"}
	args := []any{workspaceID}
	// Crew-scope the list the same way GET /credentials does. A binding list is
	// a map of where a tenant's secrets go, and without this a MEMBER of one
	// crew reads the account name, slot and crew of a credential scoped to a
	// crew they don't belong to — a cross-crew metadata leak the rest of the
	// credential surface (List, Get, fields, reveal) is careful to prevent. The
	// filter is a no-op for MANAGER+ (canRole "update"), matching those paths.
	if vis, visArgs := credentialVisibilityFilter(RoleFromContext(r.Context()), UserFromContext(r.Context())); vis != "" {
		where = append(where, "1=1"+vis) // vis begins " AND (...)"
		args = append(args, visArgs...)
	}
	// A slice and not a map: map iteration is randomised, so the same request
	// would produce a different SQL string every call — different plan cache
	// entry, and a query that cannot be recognised in a slow-query log.
	for _, f := range []struct{ param, column string }{
		{"scope", "b.scope"},
		{"crew_id", "b.crew_id"},
		{"agent_id", "b.agent_id"},
		{"credential_id", "b.credential_id"},
		{"slot", "b.slot"},
	} {
		if v := strings.TrimSpace(q.Get(f.param)); v != "" {
			where = append(where, f.column+" = ?")
			args = append(args, v)
		}
	}
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT b.id, b.credential_id, c.name, b.scope, b.crew_id, b.agent_id, b.slot, b.created_at
		FROM credential_bindings b
		JOIN credentials c ON c.id = b.credential_id
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY b.slot, b.scope, b.created_at, b.id`, args...)
	if err != nil {
		h.logger.Error("list credential bindings", "error", err, "workspace_id", workspaceID)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	out := []credentialBindingResponse{}
	for rows.Next() {
		var b credentialBindingResponse
		if err := rows.Scan(&b.ID, &b.CredentialID, &b.CredentialName, &b.Scope,
			&b.CrewID, &b.AgentID, &b.Slot, &b.CreatedAt); err != nil {
			h.logger.Error("scan credential binding", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("iterate credential bindings", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bindings": out})
}

// Create POST /api/v1/credentials/bindings
//
// 409 on a slot already bound in the same scope. 400 on anything the caller can
// fix — including a credential, crew or agent belonging to another tenant,
// which is deliberately indistinguishable from "not found" so the response
// cannot be used to probe another workspace's ids.
func (h *CredentialBindingHandler) Create(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusBadRequest, "Workspace context required")
		return
	}
	if !canRole(RoleFromContext(r.Context()), roleManage) {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	var req createBindingRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	scope := strings.ToUpper(strings.TrimSpace(req.Scope))
	if scope == "" {
		scope = bindingScopeWorkspace
	}
	slot := strings.TrimSpace(req.Slot)
	crewID := strings.TrimSpace(req.CrewID)
	agentID := strings.TrimSpace(req.AgentID)
	credentialID := strings.TrimSpace(req.CredentialID)

	if !slotNameRE.MatchString(slot) {
		replyError(w, http.StatusBadRequest,
			"slot must be an environment variable name (letters, digits, underscore; not starting with a digit)")
		return
	}

	// Scope and owner must agree. The same rule is a CHECK constraint in the
	// schema; it is repeated here so the caller gets a sentence instead of a
	// SQLite constraint string, not because either copy is redundant.
	switch scope {
	case bindingScopeWorkspace:
		if crewID != "" || agentID != "" {
			replyError(w, http.StatusBadRequest, "WORKSPACE scope takes neither crew_id nor agent_id")
			return
		}
	case bindingScopeCrew:
		if crewID == "" || agentID != "" {
			replyError(w, http.StatusBadRequest, "CREW scope requires crew_id and no agent_id")
			return
		}
	case bindingScopeAgent:
		if agentID == "" || crewID != "" {
			replyError(w, http.StatusBadRequest, "AGENT scope requires agent_id and no crew_id")
			return
		}
	default:
		replyError(w, http.StatusBadRequest, "scope must be WORKSPACE, CREW or AGENT")
		return
	}

	// Every referenced row must live in the caller's workspace. Checked here
	// AND by trg_credential_bindings_workspace_check: this copy produces the
	// error message, the trigger is what survives a future write path that
	// forgets to.
	var credName string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT name FROM credentials WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		credentialID, workspaceID).Scan(&credName)
	if err == sql.ErrNoRows {
		replyError(w, http.StatusBadRequest, "Credential not found")
		return
	}
	if err != nil {
		h.logger.Error("binding: resolve credential", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if crewID != "" && !h.ownedByWorkspace(r, "crews", crewID, workspaceID) {
		replyError(w, http.StatusBadRequest, "Crew not found")
		return
	}
	if agentID != "" && !h.ownedByWorkspace(r, "agents", agentID, workspaceID) {
		replyError(w, http.StatusBadRequest, "Agent not found")
		return
	}

	id := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	var actor any
	if u := UserFromContext(r.Context()); u != nil {
		actor = u.ID
	}
	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO credential_bindings
			(id, workspace_id, credential_id, scope, crew_id, agent_id, slot, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		// nullIfEmpty and not the raw string: an empty crew_id is not NULL, and
		// a WORKSPACE binding with crew_id='' fails the scope/owner CHECK with
		// an error the user cannot act on.
		id, workspaceID, credentialID, scope,
		nullIfEmpty(crewID), nullIfEmpty(agentID), slot, actor, now, now)
	if err != nil {
		// The unique index is the invariant; anything else is ours to explain.
		// Matching on the index NAME rather than "UNIQUE" alone keeps a future
		// unrelated constraint from being reported to the user as a slot
		// conflict it can neither see nor fix.
		if strings.Contains(err.Error(), "idx_credential_bindings_slot") {
			replyError(w, http.StatusConflict,
				"slot "+slot+" is already bound in this scope — delete the existing binding first")
			return
		}
		h.logger.Error("create credential binding", "error", err, "workspace_id", workspaceID)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusCreated, credentialBindingResponse{
		ID: id, CredentialID: credentialID, CredentialName: credName,
		Scope: scope, CrewID: nullIfEmpty(crewID), AgentID: nullIfEmpty(agentID),
		Slot: slot, CreatedAt: now,
	})
}

// Delete DELETE /api/v1/credentials/bindings/{bindingId}
//
// A real delete, not a flag: the row IS the slot claim, so anything that left
// it behind would keep the slot occupied and 409 every subsequent write.
func (h *CredentialBindingHandler) Delete(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusBadRequest, "Workspace context required")
		return
	}
	if !canRole(RoleFromContext(r.Context()), roleManage) {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	bindingID := r.PathValue("bindingId")
	if bindingID == "" {
		replyError(w, http.StatusBadRequest, "bindingId is required")
		return
	}

	// workspace_id in the DELETE, not in a prior SELECT: one statement means
	// there is no window in which the tenant check has passed and the delete
	// has not.
	res, err := h.db.ExecContext(r.Context(),
		`DELETE FROM credential_bindings WHERE id = ? AND workspace_id = ?`, bindingID, workspaceID)
	if err != nil {
		h.logger.Error("delete credential binding", "error", err, "binding_id", bindingID)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		replyError(w, http.StatusNotFound, "Binding not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ResolveForAgent GET /api/v1/agents/{agentId}/credential-bindings
//
// The read-only answer to "which account will this agent actually use, and
// why". It exists because that question previously had no answer short of
// starting the container and looking — which is a large part of why the fused
// name/env-var went unnoticed for as long as it did.
//
// It reads the delivery chokepoint itself rather than re-deriving resolution,
// so the report cannot drift from what the container receives. Values are
// dropped before anything is written; only the mapping leaves the server.
func (h *CredentialBindingHandler) ResolveForAgent(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	agentID := r.PathValue("agentId")
	if agentID == "" {
		replyError(w, http.StatusBadRequest, "agentId is required")
		return
	}
	var exists string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		agentID, workspaceID).Scan(&exists)
	if err == sql.ErrNoRows {
		replyError(w, http.StatusNotFound, "Agent not found")
		return
	}
	if err != nil {
		h.logger.Error("resolve bindings: agent lookup", "error", err, "agent_id", agentID)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	delivered, err := loadDeliveredCredentials(r.Context(), h.db, agentID)
	if err != nil {
		h.logger.Error("resolve bindings: load delivered", "error", err, "agent_id", agentID)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	names, err := h.credentialNames(r, workspaceID)
	if err != nil {
		h.logger.Error("resolve bindings: credential names", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// First match per slot wins, mirroring the orchestrator: the delivery
	// query already orders so that the winner comes first, and reproducing the
	// ranking here would be a second opinion nobody keeps in sync.
	seen := map[string]struct{}{}
	slots := []resolvedSlot{}
	for _, d := range delivered {
		if _, dup := seen[d.EnvVar]; dup {
			continue
		}
		seen[d.EnvVar] = struct{}{}
		slots = append(slots, resolvedSlot{
			Slot:           d.EnvVar,
			CredentialID:   d.ID,
			CredentialName: names[d.ID],
			Source:         bindingSourceLabel(d.Source),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_id": agentID, "slots": slots})
}

// bindingSourceLabel turns a delivery rank into the rule that produced it.
func bindingSourceLabel(source int) string {
	switch source {
	case credSourceAgentGrant:
		return bindingSourceAgentGrant
	case credSourceBindingAgent:
		return bindingSourceAgent
	case credSourceBindingCrew:
		return bindingSourceCrew
	case credSourceBindingWorkspace:
		return bindingSourceWorkspace
	default:
		return bindingSourceCrewLink
	}
}

func (h *CredentialBindingHandler) credentialNames(r *http.Request, workspaceID string) (map[string]string, error) {
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, name FROM credentials WHERE workspace_id = ?`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// ownedByWorkspace answers "does this id exist in this tenant". Table names are
// literals from the two call sites above, never request input.
func (h *CredentialBindingHandler) ownedByWorkspace(r *http.Request, table, id, workspaceID string) bool {
	var found string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM `+table+` WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		id, workspaceID).Scan(&found)
	if err != nil && err != sql.ErrNoRows {
		h.logger.Error("binding: owner lookup", "error", err, "table", table)
	}
	return err == nil
}
