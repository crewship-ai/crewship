package api

import (
	crand "crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/policy"
)

// PR-E F6 — PERSONA API surface.
//
// Three endpoints, each available in agent + crew flavors:
//
//	GET    /api/v1/agents/{agentId}/persona            — read current
//	PUT    /api/v1/agents/{agentId}/persona            — write (operator-only)
//	DELETE /api/v1/agents/{agentId}/persona            — reset (drop file)
//	GET    /api/v1/agents/{agentId}/persona/history    — version log
//	POST   /api/v1/agents/{agentId}/persona/suggest    — agent-initiated proposal
//	GET    /api/v1/crews/{crewId}/persona              — crew default flavor
//	PUT    /api/v1/crews/{crewId}/persona
//	DELETE /api/v1/crews/{crewId}/persona
//
// Direct writes are operator-driven. Agent-initiated edits flow
// through the suggest endpoint which consults the policy resolver
// (ActionPersonaSuggest) and either creates an inbox proposal
// (strict/guided/trusted) or auto-applies with notification
// (full). ActionPersonaDirectWrite is rejected everywhere in
// Phase 1 — agents never bypass the suggest path.

// PersonaHandler owns the persona endpoints. Holds the disk root
// for resolving per-agent and per-crew memory paths plus the
// policy resolver for the suggest flow.
//
// outputBasePath is the host-side OutputBasePath (the same root
// that buildMounts uses for the /output and /crew bind mounts) —
// see internal/provider/docker/docker_container.go. Resolves to:
//
//	agent layer:  {outputBase}/crews/{crewID}/agents/{slug}/.memory/
//	crew  layer:  {outputBase}/crews/{crewID}/shared/.memory/
//
// We resolve from crew_id (not crew_slug) because slug renames
// would otherwise orphan the persona file. The slug is only used
// for the agent path, which is also slug-based on disk; renaming
// an agent is a non-goal (slugs are immutable in practice).
type PersonaHandler struct {
	db             *sql.DB
	logger         *slog.Logger
	outputBasePath string
	policyResolver *policy.Resolver
}

// NewPersonaHandler builds the handler. outputBasePath should be
// the value of cfg.Storage.BasePath (production) or a temp dir
// (tests). Empty string disables file IO — every endpoint then
// returns 503 with a clear error so the operator knows the binary
// was built without storage configuration rather than a 404 on a
// missing file.
func NewPersonaHandler(db *sql.DB, logger *slog.Logger, outputBasePath string, resolver *policy.Resolver) *PersonaHandler {
	return &PersonaHandler{
		db:             db,
		logger:         logger,
		outputBasePath: outputBasePath,
		policyResolver: resolver,
	}
}

// resolveAgentPaths fetches (workspace_id, crew_id, agent_slug,
// agent_role, role_title) for the agent and builds the memory
// paths. Returns 404 when the agent doesn't exist or belongs to a
// different workspace than the request context.
func (h *PersonaHandler) resolveAgentPaths(r *http.Request, agentID string) (memory.PersonaPaths, string, string, string, string, error) {
	wsID := WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		return memory.PersonaPaths{}, "", "", "", "", fmt.Errorf("workspace context missing")
	}
	var (
		crewID    sql.NullString
		slug      string
		agentRole sql.NullString
		roleTitle sql.NullString
	)
	err := h.db.QueryRowContext(r.Context(), `
		SELECT crew_id, slug, agent_role, role_title
		FROM agents
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL
	`, agentID, wsID).Scan(&crewID, &slug, &agentRole, &roleTitle)
	if err != nil {
		return memory.PersonaPaths{}, "", "", "", "", err
	}
	// Solo agents (crew_id IS NULL) still get a persona, but they can't
	// share the .../crews// prefix — that segment would be empty, so
	// two workspaces with the same slug would collide on disk for the
	// same host bind-mount root. Use a workspace-scoped fallback
	// (.../solo/{workspace_id}/agents/{slug}/.memory) so each workspace
	// owns a disjoint subtree.
	var agentDir string
	if crewID.Valid && crewID.String != "" {
		agentDir = h.agentMemoryDir(crewID.String, slug)
	} else {
		agentDir = h.soloAgentMemoryDir(wsID, slug)
	}
	paths := memory.PersonaPaths{AgentDir: agentDir}
	if crewID.Valid && crewID.String != "" {
		paths.CrewDir = h.crewSharedMemoryDir(crewID.String)
	}
	return paths, crewID.String, slug, agentRole.String, roleTitle.String, nil
}

// soloAgentMemoryDir is the per-workspace fallback for agents that
// don't belong to a crew. crew_id IS NULL would collapse to
// .../crews//agents/{slug}/.memory and collide across workspaces;
// using workspace_id keeps every solo agent's memory in its own
// subtree.
func (h *PersonaHandler) soloAgentMemoryDir(workspaceID, slug string) string {
	return filepath.Join(h.outputBasePath, "solo", workspaceID, "agents", slug, ".memory")
}

// agentMemoryDir + crewSharedMemoryDir mirror the bind-mount layout
// from internal/provider/docker/docker_container.go:
//
//	outputPath    = {OutputBase}/{crewID}          → /output
//	crewPath      = {OutputBase}/crews/{crewID}    → /crew
//
// So /crew/agents/{slug}/.memory/ on the container is
// {OutputBase}/crews/{crewID}/agents/{slug}/.memory/ on the host.
func (h *PersonaHandler) agentMemoryDir(crewID, slug string) string {
	return filepath.Join(h.outputBasePath, "crews", crewID, "agents", slug, ".memory")
}

func (h *PersonaHandler) crewSharedMemoryDir(crewID string) string {
	return filepath.Join(h.outputBasePath, "crews", crewID, "shared", ".memory")
}

// GetAgentPersona returns the resolved persona — agent layer if
// non-empty, else crew layer, else the synthesized default. The
// "source" field tells the caller (and the UI) which layer rendered.
//
// GET /api/v1/agents/{agentId}/persona
func (h *PersonaHandler) GetAgentPersona(w http.ResponseWriter, r *http.Request) {
	if !h.requireStorage(w) {
		return
	}
	agentID := r.PathValue("agentId")
	paths, _, _, agentRole, roleTitle, err := h.resolveAgentPaths(r, agentID)
	if err != nil {
		h.replyAgentLookup(w, err)
		return
	}
	resolved, err := memory.LoadPersona(paths)
	if err != nil {
		h.logger.Warn("load persona failed", "agent_id", agentID, "err", err)
		replyError(w, http.StatusInternalServerError, "load persona")
		return
	}
	resp := map[string]any{
		"agent_id":     agentID,
		"layer":        string(resolved.Layer),
		"from_default": resolved.FromDefault,
		"content":      resolved.Content,
		"bytes":        len(resolved.Content),
		"cap_bytes":    memory.PersonaCapBytes,
	}
	if resolved.Content == "" {
		def := memory.DefaultPersona(agentRole, roleTitle)
		resp["layer"] = string(def.Layer)
		resp["from_default"] = true
		resp["content"] = def.Content
		resp["bytes"] = len(def.Content)
	}
	writeJSON(w, http.StatusOK, resp)
}

// PutAgentPersona writes the agent layer (operator-only). Content
// passed in via JSON {"content": "..."} so the body shape stays
// consistent with the suggest endpoint.
//
// PUT /api/v1/agents/{agentId}/persona
func (h *PersonaHandler) PutAgentPersona(w http.ResponseWriter, r *http.Request) {
	if !h.requireStorage(w) {
		return
	}
	agentID := r.PathValue("agentId")
	paths, _, _, _, _, err := h.resolveAgentPaths(r, agentID)
	if err != nil {
		h.replyAgentLookup(w, err)
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := memory.WritePersona(paths, memory.PersonaAgent, body.Content); err != nil {
		if strings.Contains(err.Error(), "exceeds cap") {
			replyError(w, http.StatusRequestEntityTooLarge, err.Error())
			return
		}
		// Other WritePersona failures are storage/IO problems (mkdir,
		// write, fsync) — 500, not 400. Reporting them as 400 hides
		// real outages from monitoring.
		h.logger.Warn("write agent persona failed", "agent_id", agentID, "err", err)
		replyError(w, http.StatusInternalServerError, "write persona")
		return
	}
	h.recordVersion(r, agentID, "agent", paths.AgentPath(), body.Content)
	writeJSON(w, http.StatusOK, map[string]any{
		"layer":   "agent",
		"bytes":   len(body.Content),
		"updated": time.Now().UTC().Format(time.RFC3339),
	})
}

// DeleteAgentPersona resets the agent layer (the crew layer or the
// synthesized default takes over on the next read).
//
// DELETE /api/v1/agents/{agentId}/persona
func (h *PersonaHandler) DeleteAgentPersona(w http.ResponseWriter, r *http.Request) {
	if !h.requireStorage(w) {
		return
	}
	agentID := r.PathValue("agentId")
	paths, _, _, _, _, err := h.resolveAgentPaths(r, agentID)
	if err != nil {
		h.replyAgentLookup(w, err)
		return
	}
	if err := memory.ResetPersona(paths, memory.PersonaAgent); err != nil {
		replyError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetAgentPersonaHistory returns rows from memory_versions filtered
// to this agent's PERSONA.md path. Pairs with the existing
// memory_versions_content endpoint for content drill-down — we
// don't duplicate the content here, only the version list.
//
// GET /api/v1/agents/{agentId}/persona/history?limit=20
func (h *PersonaHandler) GetAgentPersonaHistory(w http.ResponseWriter, r *http.Request) {
	if !h.requireStorage(w) {
		return
	}
	agentID := r.PathValue("agentId")
	paths, _, _, _, _, err := h.resolveAgentPaths(r, agentID)
	if err != nil {
		h.replyAgentLookup(w, err)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, sha256, bytes, written_at, written_by, parent_sha
		FROM memory_versions
		WHERE path = ? AND workspace_id = ?
		ORDER BY written_at DESC
		LIMIT ?
	`, paths.AgentPath(), WorkspaceIDFromContext(r.Context()), limit)
	if err != nil {
		h.logger.Warn("persona history query", "err", err)
		replyError(w, http.StatusInternalServerError, "history query")
		return
	}
	defer rows.Close()
	type entry struct {
		ID        string `json:"id"`
		SHA256    string `json:"sha256"`
		Bytes     int    `json:"bytes"`
		WrittenAt string `json:"written_at"`
		WrittenBy string `json:"written_by"`
		ParentSHA string `json:"parent_sha,omitempty"`
	}
	out := []entry{}
	for rows.Next() {
		var e entry
		var writtenBy, parent sql.NullString
		if err := rows.Scan(&e.ID, &e.SHA256, &e.Bytes, &e.WrittenAt, &writtenBy, &parent); err != nil {
			continue
		}
		e.WrittenBy = writtenBy.String
		e.ParentSHA = parent.String
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": out})
}

// SuggestAgentPersona is the agent-initiated proposal flow. The
// agent POSTs a candidate persona and the policy resolver decides
// what happens:
//
//	strict/guided/trusted → DecisionInboxApprove: write an inbox
//	  item with the proposed content; agent learns "pending" so it
//	  can refer to the proposal in subsequent runs.
//
//	full → DecisionAutoJournal: write immediately + journal entry
//	  so the operator can see what landed via the timeline.
//
// Direct writes by the agent (bypassing this endpoint) are blocked
// by ActionPersonaDirectWrite which is DecisionRejected across all
// autonomy levels in Phase 1.
//
// POST /api/v1/agents/{agentId}/persona/suggest
// Body: {"content": "...", "rationale": "..."}
func (h *PersonaHandler) SuggestAgentPersona(w http.ResponseWriter, r *http.Request) {
	if !h.requireStorage(w) {
		return
	}
	agentID := r.PathValue("agentId")
	paths, crewID, _, _, _, err := h.resolveAgentPaths(r, agentID)
	if err != nil {
		h.replyAgentLookup(w, err)
		return
	}
	var body struct {
		Content   string `json:"content"`
		Rationale string `json:"rationale"`
	}
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if strings.TrimSpace(body.Content) == "" {
		replyError(w, http.StatusBadRequest, "content required")
		return
	}
	if len(body.Content) > memory.PersonaCapBytes {
		replyError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("content exceeds cap (%d bytes)", memory.PersonaCapBytes))
		return
	}

	// Resolve policy. The fallback when no resolver is wired (tests,
	// startup race) is to treat the suggestion as inbox-approve —
	// safest default, never silently auto-applies.
	decision := policy.DecisionInboxApprove
	if h.policyResolver != nil && crewID != "" {
		pol, perr := h.policyResolver.Resolve(r.Context(), crewID)
		if perr != nil {
			h.logger.Warn("policy resolve failed; defaulting to inbox approve",
				"crew_id", crewID, "err", perr)
		} else {
			decision = pol.DecideAction(policy.ActionPersonaSuggest)
		}
	}

	// PR-G F4.1 UX — per-agent self_learning gate on the auto-apply
	// path. The crew policy may say "auto-apply this persona suggestion"
	// (full autonomy → DecisionAutoJournal), but a per-agent override
	// can demand operator approval first. Even on a trusted/full crew,
	// an agent with self_learning_enabled=0 should NOT silently mutate
	// its own PERSONA.md.
	//
	//   self_learning=1 → keep the auto-apply decision (no change)
	//   self_learning=0 → demote auto-apply to DecisionInboxApprove
	//
	// Reject / inbox decisions pass through unchanged — the gate only
	// closes the silent-auto-apply path, it never opens one.
	//
	// Look up the flag once. On lookup error, default OFF (require
	// approval) for the same reason as the F4.4 path: silently auto-
	// applying when we couldn't verify the gate is the worse failure
	// mode than a false-positive operator review.
	gateDemoted := false
	if isPersonaAutoApply(decision) {
		enabled, lerr := loadSelfLearningEnabled(r.Context(), h.db, agentID)
		if lerr != nil {
			h.logger.Warn("persona suggest: self_learning lookup failed; defaulting to OFF",
				"agent_id", agentID, "err", lerr)
			enabled = false
		}
		if !enabled {
			decision = policy.DecisionInboxApprove
			gateDemoted = true
		}
	}

	resp := map[string]any{
		"agent_id":  agentID,
		"decision":  string(decision),
		"bytes":     len(body.Content),
		"rationale": body.Rationale,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}
	if gateDemoted {
		resp["self_learning_gate"] = "off"
	}

	switch decision {
	case policy.DecisionAutoJournal, policy.DecisionAutoLogJournal, policy.DecisionAutoLogInbox:
		// Auto-apply. Persist + record version. The journal /
		// inbox emission is the orchestrator's responsibility
		// (the journal emitter we'd need is wired at the crew /
		// orchestration layer); we record the version so the
		// timeline tooling has the row to link back to.
		if err := memory.WritePersona(paths, memory.PersonaAgent, body.Content); err != nil {
			replyError(w, http.StatusInternalServerError, err.Error())
			return
		}
		h.recordVersion(r, agentID, "agent", paths.AgentPath(), body.Content)
		resp["applied"] = true
		writeJSON(w, http.StatusOK, resp)
		return
	case policy.DecisionRejected:
		resp["applied"] = false
		resp["reason"] = "policy rejected (autonomy floor)"
		writeJSON(w, http.StatusForbidden, resp)
		return
	default:
		// DecisionInboxApprove or any other gate → enqueue, don't apply.
		// We store the proposal as a JSON metadata blob in
		// audit_logs (so the inbox UI can pick it up without a
		// new table); the inbox handler in PR-C wires the
		// approval side which calls back to PutAgentPersona.
		meta := map[string]any{
			"content":   body.Content,
			"rationale": body.Rationale,
			"bytes":     len(body.Content),
		}
		if gateDemoted {
			meta["self_learning_gate"] = "off"
		}
		metaBytes, _ := json.Marshal(meta)
		auditID := newAuditID()
		_, err := h.db.ExecContext(r.Context(), `
			INSERT INTO audit_logs (id, workspace_id, action, entity_type, entity_id, metadata, created_at)
			VALUES (?, ?, 'persona.suggest_pending', 'agent', ?, ?, ?)
		`, auditID, WorkspaceIDFromContext(r.Context()), agentID, string(metaBytes),
			time.Now().UTC().Format(time.RFC3339))
		if err != nil {
			h.logger.Warn("persona suggestion enqueue failed", "err", err)
			replyError(w, http.StatusInternalServerError, "enqueue proposal")
			return
		}
		// When the gate demoted an auto-apply to inbox approval, also
		// surface a blocking inbox_items row so the operator gets a
		// first-class queue entry (not just an audit_logs row). The
		// audit_logs row above remains the proposal record the
		// approve-handler reads; the inbox row is the visibility
		// surface. self_learning_gate=off in the payload lets the UI
		// distinguish "demoted by per-agent override" from "policy
		// said inbox in the first place".
		if gateDemoted {
			_ = inbox.Insert(r.Context(), h.db, h.logger, inbox.Item{
				WorkspaceID: WorkspaceIDFromContext(r.Context()),
				Kind:        inbox.KindEscalation,
				SourceID:    auditID,
				TargetRole:  "MANAGER",
				Title:       fmt.Sprintf("Persona proposal: %s (gated by self_learning=OFF)", agentID),
				BodyMD: fmt.Sprintf(
					"**Proposed PERSONA.md** (auto-apply blocked by self_learning=OFF):\n\n%s\n\n_Rationale: %s_",
					body.Content, body.Rationale,
				),
				SenderType: "agent",
				SenderID:   agentID,
				SenderName: "Persona Suggest",
				Priority:   "low",
				Blocking:   true,
				Payload: map[string]interface{}{
					"audit_id":           auditID,
					"agent_id":           agentID,
					"crew_id":            crewID,
					"action":             "persona.suggest_pending",
					"bytes":              len(body.Content),
					"rationale":          body.Rationale,
					"self_learning_gate": "off",
				},
			})
		}
		resp["applied"] = false
		resp["pending"] = true
		writeJSON(w, http.StatusOK, resp)
		return
	}
}

// isPersonaAutoApply reports whether a policy Decision means "apply
// the persona suggestion without operator review". Centralized so the
// self_learning gate stays in one place — if the policy matrix ever
// adds another auto-apply variant, only this predicate has to change.
func isPersonaAutoApply(d policy.Decision) bool {
	switch d {
	case policy.DecisionAutoJournal,
		policy.DecisionAutoLogJournal,
		policy.DecisionAutoLogInbox:
		return true
	}
	return false
}

// newAuditID returns a hex audit_logs.id. We need the id at call-site
// so it can also become the inbox row's source_id (the audit row is
// the authoritative proposal record; the inbox row is just the visibility
// projection). The previous `lower(hex(randomblob(16)))` inline form
// generated the id inside SQL, where we couldn't read it back without
// a follow-up SELECT.
func newAuditID() string {
	b := make([]byte, 16)
	_, _ = crand.Read(b)
	return hex.EncodeToString(b)
}

// recordVersion writes a memory_versions row for the persona change
// so the history endpoint has rows to surface. Best-effort: a write
// failure here is logged but doesn't fail the persona PUT (the file
// already landed on disk; losing the audit row is recoverable, a
// failed write isn't).
func (h *PersonaHandler) recordVersion(r *http.Request, agentID, layer, path, content string) {
	wsID := WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		return
	}
	user := UserFromContext(r.Context())
	writtenBy := "operator"
	if user != nil {
		writtenBy = user.ID
	}
	sha := hashPersona(content)
	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO memory_versions
		(id, workspace_id, path, tier, sha256, bytes, written_by, payload_ref)
		VALUES (lower(hex(randomblob(16))), ?, ?, 'persona', ?, ?, ?, ?)
	`, wsID, path, sha, len(content), writtenBy, path)
	if err != nil {
		h.logger.Warn("persona version row insert failed",
			"agent_id", agentID, "layer", layer, "err", err)
	}
}

// hashPersona returns a sha256 hex digest of the content for the
// memory_versions.sha256 column. Inlined rather than pulling in a
// wider helper because the only consumer is the version recorder
// above.
func hashPersona(content string) string {
	h := sha256.New()
	h.Write([]byte(content))
	return hex.EncodeToString(h.Sum(nil))
}

// requireStorage short-circuits with 503 when outputBasePath is
// empty. Without it, every persona endpoint would 404 because the
// resolved path points into an unset root — operators would see
// "file not found" rather than the actual configuration issue.
func (h *PersonaHandler) requireStorage(w http.ResponseWriter) bool {
	if h.outputBasePath == "" {
		replyError(w, http.StatusServiceUnavailable,
			"persona storage not configured (set cfg.storage.base_path)")
		return false
	}
	return true
}

func (h *PersonaHandler) replyAgentLookup(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "agent not found")
		return
	}
	h.logger.Warn("persona handler: agent lookup", "err", err)
	replyError(w, http.StatusInternalServerError, err.Error())
}

// --- Crew flavor ------------------------------------------------------------

// resolveCrewPaths fetches the crew row and builds the PersonaPaths
// pointing at the crew shared memory dir only. AgentDir is left
// empty because the crew endpoint shouldn't reach into per-agent
// files.
func (h *PersonaHandler) resolveCrewPaths(r *http.Request, crewID string) (memory.PersonaPaths, error) {
	wsID := WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		return memory.PersonaPaths{}, fmt.Errorf("workspace context missing")
	}
	var seen string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		crewID, wsID).Scan(&seen)
	if err != nil {
		return memory.PersonaPaths{}, err
	}
	return memory.PersonaPaths{CrewDir: h.crewSharedMemoryDir(crewID)}, nil
}

// GET /api/v1/crews/{crewId}/persona
func (h *PersonaHandler) GetCrewPersona(w http.ResponseWriter, r *http.Request) {
	if !h.requireStorage(w) {
		return
	}
	crewID := r.PathValue("crewId")
	paths, err := h.resolveCrewPaths(r, crewID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "crew not found")
			return
		}
		replyError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resolved, err := memory.LoadPersona(paths)
	if err != nil {
		replyError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"crew_id":   crewID,
		"layer":     "crew",
		"content":   resolved.Content,
		"bytes":     len(resolved.Content),
		"cap_bytes": memory.PersonaCapBytes,
	})
}

// PUT /api/v1/crews/{crewId}/persona
func (h *PersonaHandler) PutCrewPersona(w http.ResponseWriter, r *http.Request) {
	if !h.requireStorage(w) {
		return
	}
	crewID := r.PathValue("crewId")
	paths, err := h.resolveCrewPaths(r, crewID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "crew not found")
			return
		}
		replyError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := memory.WritePersona(paths, memory.PersonaCrew, body.Content); err != nil {
		if strings.Contains(err.Error(), "exceeds cap") {
			replyError(w, http.StatusRequestEntityTooLarge, err.Error())
			return
		}
		// Storage/IO failures = 500. See PutAgentPersona above for the
		// same rationale (hides real outages from monitoring otherwise).
		h.logger.Warn("write crew persona failed", "crew_id", crewID, "err", err)
		replyError(w, http.StatusInternalServerError, "write persona")
		return
	}
	h.recordCrewVersion(r, crewID, paths.CrewPath(), body.Content)
	writeJSON(w, http.StatusOK, map[string]any{
		"layer": "crew", "bytes": len(body.Content),
		"updated": time.Now().UTC().Format(time.RFC3339),
	})
}

// DELETE /api/v1/crews/{crewId}/persona
func (h *PersonaHandler) DeleteCrewPersona(w http.ResponseWriter, r *http.Request) {
	if !h.requireStorage(w) {
		return
	}
	crewID := r.PathValue("crewId")
	paths, err := h.resolveCrewPaths(r, crewID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "crew not found")
			return
		}
		replyError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := memory.ResetPersona(paths, memory.PersonaCrew); err != nil {
		replyError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *PersonaHandler) recordCrewVersion(r *http.Request, crewID, path, content string) {
	wsID := WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		return
	}
	user := UserFromContext(r.Context())
	writtenBy := "operator"
	if user != nil {
		writtenBy = user.ID
	}
	sha := hashPersona(content)
	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO memory_versions
		(id, workspace_id, path, tier, sha256, bytes, written_by, payload_ref)
		VALUES (lower(hex(randomblob(16))), ?, ?, 'persona', ?, ?, ?, ?)
	`, wsID, path, sha, len(content), writtenBy, path)
	if err != nil {
		h.logger.Warn("crew persona version insert failed",
			"crew_id", crewID, "err", err)
	}
}
