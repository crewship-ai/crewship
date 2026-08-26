package api

// Agent-authored page CREATION (the structure gap this file closes) —
// distinct from the routine WRITE path in pages_internal.go, which pushes a
// panel's payload onto a page a human already built. Until this file
// existed, an agent had no way to create a page's metadata + panels at
// all: the setup-agent's own system prompt used to say a Page YAML is "a
// reviewable draft unless a dedicated Crewship apply tool is visibly
// available" — this route, and the save_page MCP tool that forwards to it
// (internal/sidecar/pages.go), is that tool.
//
// Mirrors save_routine's shape and trust model as closely as the two
// domains allow: an internal-token-authed route, identity injected from the
// token binding (never the caller's body), gated on the crew's
// autonomy_level, reusing the exact validation/insert machinery the public
// POST /api/v1/pages already uses (documentFrom, resolveReferences,
// resolveGates, insertPanel, reconcileWakeAutomations, insertPageVersion).
//
// One deliberate difference from save_routine and from CreateCrew/
// skill Author: Pages have no "created but inert" staging state (no
// status/enabled column, no .proposed directory). A HELD decision therefore
// creates nothing at all — the same honest choice skills_author_handler.go
// makes for its own analogous edge ("no promotion path here... stays at its
// stricter behaviour") rather than inventing a new inert-page mechanism
// under the banner of adding a gate.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/policy"
)

// registerInternalPageSaveRoute mounts the agent-authored create route.
// Called alongside registerInternalPageRoutes so both internal page routes
// wire from one place; kept in its own function because this one also
// needs the policy resolver, which the routine write path does not.
func (r *Router) registerInternalPageSaveRoute(internalAuth func(http.Handler) http.Handler) {
	p := NewPageHandler(r.db, r.hub, r.logger).SetJournal(r.Journal())
	p.SetPolicyResolver(r.PolicyResolver())
	r.mux.Handle("POST /api/v1/internal/pages/save",
		internalAuth(http.HandlerFunc(p.InternalSave)))
}

// pageInternalSaveEnvelopeSlack bounds the identity fields (workspace_id,
// crew_id, agent_id) atop the spec cap — same reasoning as
// internalPagePushEnvelopeSlack in pages_internal.go: /api/v1/internal/*
// bypasses the global BodyCap, so this route has to bound its own read.
const pageInternalSaveEnvelopeSlack = 8 << 10

// pageInternalSaveRequest is the agent-facing body for /pages/save,
// forwarded by the sidecar's savePage helper. WorkspaceID/CrewID/AgentID are
// injected by the sidecar from IPC — never trusted from the container's own
// claim beyond what assertInternalTokenWorkspace / assertBoundCrewWorkspaceDB
// prove against the token binding.
type pageInternalSaveRequest struct {
	WorkspaceID string          `json:"workspace_id"`
	CrewID      string          `json:"crew_id"`
	AgentID     string          `json:"agent_id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Panels      []pagePanelWire `json:"panels"`
	// TargetCrewSlug re-points ownership at the crew this page is being built
	// FOR — accepted only from the onboarding setup crew, which in exchange
	// may own nothing itself. See internal_delegated_crew.go.
	TargetCrewSlug string `json:"target_crew_slug,omitempty"`
}

// pageCreateHeldInboxSource is the (kind, source_id) dedup key for a held
// page-creation request — mirrors skillProposalInboxSource's shape. Keyed
// on the crew and the requested slug so a routine retrying the same
// creation every few minutes does not bury the inbox with duplicates.
func pageCreateHeldInboxSource(crewID, slug string) string {
	return "pagecreate-held:" + crewID + ":" + slug
}

// InternalSave is the trusted endpoint the sidecar's save_page tool forwards
// to. X-Internal-Token authentication runs upstream; here the caller's claim
// about identity is only trusted after assertInternalTokenWorkspace /
// assertBoundCrewWorkspaceDB prove it against the token's own binding.
//
// POST /api/v1/internal/pages/save
func (h *PageHandler) InternalSave(w http.ResponseWriter, r *http.Request) {
	raw, ok := readCapped(w, r, pages.MaxSpecBytes+pageInternalSaveEnvelopeSlack, "page spec")
	if !ok {
		return
	}
	var req pageInternalSaveRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		replyError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.WorkspaceID = strings.TrimSpace(req.WorkspaceID)
	req.CrewID = strings.TrimSpace(req.CrewID)
	req.AgentID = strings.TrimSpace(req.AgentID)
	req.Name = strings.TrimSpace(req.Name)
	if req.WorkspaceID == "" {
		replyError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	if req.Name == "" {
		replyError(w, http.StatusBadRequest, "name is required")
		return
	}
	if len(req.Panels) == 0 {
		replyError(w, http.StatusBadRequest, "panels is required — a page needs at least one panel")
		return
	}

	// The two tenancy fences every internal write runs (pages_internal.go's
	// own comment on PushDataInternal makes the same two calls, for the
	// same reason).
	if !assertInternalTokenWorkspace(w, r, req.WorkspaceID) {
		return
	}
	if !assertBoundCrewWorkspaceDB(w, r, h.db, h.logger, &req.CrewID) {
		return
	}
	// Unlike the routine write path, a page has exactly one owner and it is
	// always the authoring crew here — there is no user to fall back to on
	// this route, so an empty crew (a workspace-bound/master-token caller
	// that never named one) has nothing to attribute the page to.
	if req.CrewID == "" {
		replyError(w, http.StatusBadRequest, "crew_id is required — an agent-created page is always owned by its authoring crew")
		return
	}
	// The ACTOR, kept before delegation moves req.CrewID to the owner. The
	// autonomy gate below asks "may this actor create a page?", which is a
	// question about the caller; ownership is a separate question about where
	// the result lives. Collapsing the two is what put every onboarding page
	// in the Guide's own crew in the first place, and collapsing them the
	// other way would hold a page the Guide is plainly permitted to create
	// because the brand-new crew it is FOR defaults to `guided`.
	actorCrewID := req.CrewID
	if !resolveDelegatedAuthorCrew(w, r, h.db, h.logger, req.WorkspaceID, req.TargetCrewSlug, &req.CrewID) {
		return
	}
	// Same reasoning as the routine path: the acting agent is in the caller's
	// crew, so it cannot be the author of a page owned by another one.
	if req.TargetCrewSlug != "" {
		req.AgentID = ""
	}
	if !h.assertAuthorAgentInCrewPages(w, r, req.WorkspaceID, req.CrewID, req.AgentID) {
		return
	}

	var crewSlug string
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT slug FROM crews WHERE id = ? AND workspace_id = ?`, req.CrewID, req.WorkspaceID).Scan(&crewSlug); err != nil {
		replyInternalError(w, h.logger, "resolve author crew slug", err)
		return
	}

	// The gate, before any validation work: a held request should not have
	// to pass full spec validation to learn it will not be created, and the
	// held inbox item only needs the requested name/slug, both already in
	// hand.
	gate, ok := gateInternalAction(w, r, h.policyResolver, h.logger, actorCrewID,
		policy.ActionPageCreate, "Page creation")
	if !ok {
		return
	}
	slug := slugify(req.Name)
	if slug == "" {
		replyError(w, http.StatusBadRequest, "name does not produce a valid slug")
		return
	}
	if gate.held() {
		h.reportHeldPageCreate(r.Context(), req, gate, slug)
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error":          "Page creation held by policy",
			"reason":         "autonomy_level=" + string(gate.Level) + " requires operator approval for page_create",
			"crew_id":        gate.CrewID,
			"autonomy_level": string(gate.Level),
			"policy_action":  string(gate.Action),
			"pending_review": true,
			"requested_name": req.Name,
			"requested_slug": slug,
		})
		return
	}

	wreq := &pageWriteRequest{
		Name:        &req.Name,
		Description: &req.Description,
		Panels:      req.Panels,
	}
	doc, ok := h.documentFrom(w, wreq, slug)
	if !ok {
		return
	}

	var count int
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM pages WHERE workspace_id = ?`, req.WorkspaceID).Scan(&count); err != nil {
		replyInternalError(w, h.logger, "count pages", err)
		return
	}
	if count >= pages.MaxPagesPerWorkspace {
		writeRejection(w, pageRejection{
			Kind:    "cap",
			Message: fmt.Sprintf("this workspace holds %d pages; the limit is %d", count, pages.MaxPagesPerWorkspace),
			Detail: map[string]any{
				"pages_existing": count,
				"pages_limit":    pages.MaxPagesPerWorkspace,
			},
		})
		return
	}

	resolved, ok := h.resolveReferences(w, r, req.WorkspaceID, doc)
	if !ok {
		return
	}
	gates, ok := h.resolveGates(w, r, req.WorkspaceID, doc)
	if !ok {
		return
	}

	specJSON, err := json.Marshal(doc)
	if err != nil {
		replyInternalError(w, h.logger, "marshal page spec", err)
		return
	}

	pageID := generateCUID()
	now := h.evaluator().Now().UTC().Format(time.RFC3339)

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin agent page create", err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(r.Context(), `
		INSERT INTO pages (id, workspace_id, slug, name, description, owner_crew_id, spec_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?)`,
		pageID, req.WorkspaceID, doc.Metadata.Slug, doc.Metadata.Name, doc.Metadata.Description,
		req.CrewID, string(specJSON), now, now); err != nil {
		if isUniqueViolation(err) {
			replyError(w, http.StatusConflict, fmt.Sprintf("a page with slug %q already exists in this workspace", doc.Metadata.Slug))
			return
		}
		replyInternalError(w, h.logger, "insert agent-authored page", err)
		return
	}
	for i := range doc.Spec.Panels {
		if err := insertPanel(r.Context(), tx, pageID, &doc.Spec.Panels[i], resolved[doc.Spec.Panels[i].ID], now); err != nil {
			replyInternalError(w, h.logger, "insert agent-authored page panel", err)
			return
		}
	}
	// Empty author, NOT req.AgentID. That parameter is `authorUserID` and lands
	// in automations.created_by, a user column — the same distinction
	// insertPageVersionAuthoredByAgent draws for itself. There is no
	// authorising human on this route, and no FK to catch the mistake, so an
	// agent id here would sit in the column forever resolving to nobody.
	if err := reconcileWakeAutomations(r.Context(), tx, req.WorkspaceID, pageID, doc.Metadata.Slug, gates, "", now); err != nil {
		replyInternalError(w, h.logger, "compile page wake gates", err)
		return
	}
	if err := insertPageVersionAuthoredByAgent(r.Context(), tx, pageID, 1, string(specJSON), req.AgentID, now); err != nil {
		replyInternalError(w, h.logger, "insert page version", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit agent page create", err)
		return
	}

	rec, err := h.loadPage(r.Context(), req.WorkspaceID, doc.Metadata.Slug)
	if err != nil {
		replyInternalError(w, h.logger, "reload agent-created page", err)
		return
	}
	panels, err := h.loadPanels(r.Context(), req.WorkspaceID, rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "reload agent-created panels", err)
		return
	}
	h.refreshAutomations(r.Context())
	h.emitPageSpecChanged(r.Context(), req.WorkspaceID, rec, doc, true, pageArrangementFingerprint(doc))
	broadcastWorkspaceEvent(h.hub, req.WorkspaceID, "page.updated", map[string]any{"page_id": rec.ID, "slug": rec.Slug})

	// Full autonomy (the only non-held decision this action's matrix cell
	// returns, policy/types.go) proceeds but still wants a non-blocking
	// notice — the same "worth a glance" reasoning ActionSkillCreate's own
	// arm gives for its AutoLogInbox cell.
	if gate.wantsInbox() {
		_ = inbox.Insert(r.Context(), h.db, h.logger, inbox.Item{
			WorkspaceID: req.WorkspaceID,
			Kind:        inbox.KindMessage,
			SourceID:    "pagecreate-notice:" + rec.ID,
			TargetRole:  "ADMIN",
			Title:       "Page created by agent: " + rec.Name,
			BodyMD:      fmt.Sprintf("An agent in crew `%s` created page `%s` (%d panel(s)).", crewSlug, rec.Slug, len(doc.Spec.Panels)),
			SenderType:  "agent",
			SenderName:  "Agent page author",
			Priority:    "low",
			Blocking:    false,
			Payload: map[string]any{
				"kind":    "page_created",
				"crew_id": req.CrewID,
				"page_id": rec.ID,
				"slug":    rec.Slug,
			},
		})
	}

	writeJSON(w, http.StatusCreated, h.pageDocument(r.Context(), rec, panels, nil))
}

// reportHeldPageCreate files the blocking, ADMIN-addressed inbox item a held
// page-creation request leaves behind — the record of what an operator
// would need to approve (by raising the crew's autonomy level) or create
// themselves, since nothing was staged for them to promote.
func (h *PageHandler) reportHeldPageCreate(ctx context.Context, req pageInternalSaveRequest, gate autonomyDecision, slug string) {
	_ = inbox.Insert(ctx, h.db, h.logger, inbox.Item{
		WorkspaceID: req.WorkspaceID,
		Kind:        inbox.KindEscalation,
		SourceID:    pageCreateHeldInboxSource(req.CrewID, slug),
		TargetRole:  "ADMIN",
		Title:       "Page creation held for approval: " + req.Name,
		BodyMD: fmt.Sprintf(
			"An agent tried to create the page **%s**, but this crew's autonomy level (`%s`) "+
				"requires an operator to approve page creation.\n\nRaise autonomy with "+
				"`crewship policy set --crew <slug> --level trusted` (or higher), or create "+
				"the page yourself with `crewship page create`.", req.Name, gate.Level),
		SenderType: "agent",
		SenderName: "Agent page author",
		Priority:   "high",
		Blocking:   true,
		Payload: map[string]any{
			"kind":           "page_create_held",
			"crew_id":        req.CrewID,
			"requested_name": req.Name,
			"requested_slug": slug,
			"autonomy_level": string(gate.Level),
		},
	})
}

// assertAuthorAgentInCrewPages proves agent_id names a live agent of the
// authoring crew — same check pipelines_crud.go's assertAuthorAgentInCrew
// makes for routines, kept as its own small copy rather than shared across
// handler types (registerInternalPageRoutes's own doc comment gives the
// same reason: two route groups should not be coupled for a five-line
// check neither of them owns).
func (h *PageHandler) assertAuthorAgentInCrewPages(w http.ResponseWriter, r *http.Request, workspaceID, crewID, agentID string) bool {
	if agentID == "" {
		return true
	}
	if crewID == "" {
		replyError(w, http.StatusBadRequest,
			"agent_id requires crew_id — the authoring agent is resolved in its own crew")
		return false
	}
	var found string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM agents WHERE id = ? AND crew_id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		agentID, crewID, workspaceID).Scan(&found)
	if err == sql.ErrNoRows {
		replyError(w, http.StatusBadRequest, "agent_id does not name an agent in this page's authoring crew")
		return false
	}
	if err != nil {
		h.logger.Error("page save: resolve author agent", "error", err, "crew", crewID)
		replyError(w, http.StatusInternalServerError, "Failed to resolve author agent")
		return false
	}
	return true
}

// insertPageVersionAuthoredByAgent is insertPageVersion's agent-authored
// sibling: it stamps author_agent_id, never author_user_id, because there is
// no user on this path and author_user_id's FK (page_versions.author_user_id
// REFERENCES users(id)) would refuse an agent id written into it. Additive
// rather than a change to insertPageVersion's signature — the two human
// call sites (Create, Update) are unaffected.
func insertPageVersionAuthoredByAgent(ctx context.Context, tx *sql.Tx, pageID string, seq int64, specJSON, agentID, now string) error {
	var agentCol any
	if agentID != "" {
		agentCol = agentID
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO page_versions (page_id, seq, spec_json, author_agent_id, created_at)
		VALUES (?, ?, ?, ?, ?)`, pageID, seq, specJSON, agentCol, now)
	return err
}
