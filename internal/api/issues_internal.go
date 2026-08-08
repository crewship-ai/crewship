package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/untrusted"
	"github.com/crewship-ai/crewship/internal/ws"
)

// InternalIssueHandler handles issue endpoints called by the sidecar
// on behalf of agents. Uses internal token auth, not JWT.
type InternalIssueHandler struct {
	db     *sql.DB
	hub    *ws.Hub
	logger *slog.Logger
	// journal is what makes an agent's issue change NOTIFIABLE. It was
	// missing until #1768 F1: notifications route per journal entry type
	// (internal/notifyroute), so a handler that wrote only the
	// mission_activity row produced a timeline entry nobody could subscribe
	// to. Defaults to noopEmitter so tests that construct the handler
	// directly keep working.
	journal journal.Emitter
	// mentionDispatch is the @mention trigger's dispatch door, shared with the
	// human comment path (issue_handler_comments.go). Both doors must parse or
	// the feature half-works. nil = record and audit, dispatch nothing.
	mentionDispatch mentionDispatcher
}

// NewInternalIssueHandler creates a new InternalIssueHandler.
func NewInternalIssueHandler(db *sql.DB, hub *ws.Hub, logger *slog.Logger) *InternalIssueHandler {
	return &InternalIssueHandler{db: db, hub: hub, logger: logger, journal: noopEmitter{}}
}

// SetJournal wires a journal emitter after construction, mirroring
// IssueHandler.SetJournal — same contract, including nil collapsing to the
// noop emitter rather than being stored as a nil interface.
func (h *InternalIssueHandler) SetJournal(j journal.Emitter) {
	if j == nil {
		h.journal = noopEmitter{}
		return
	}
	h.journal = j
}

// events builds the shared emitter from the fields this handler already
// holds. Built per call so SetJournal after construction is picked up.
func (h *InternalIssueHandler) events() issueEvents {
	return issueEvents{db: h.db, hub: h.hub, logger: h.logger, journal: h.journal}
}

// SetMentionDispatcher wires the @mention trigger's dispatch door. Mirrors
// IssueHandler.SetMentionDispatcher, including nil being supported.
func (h *InternalIssueHandler) SetMentionDispatcher(d mentionDispatcher) {
	h.mentionDispatch = d
}

// mentionRecorder builds the shared mention write path. Per call, so a
// dispatcher wired after construction is picked up.
func (h *InternalIssueHandler) mentionRecorder() mentionRecorder {
	return mentionRecorder{db: h.db, logger: h.logger, events: h.events(), dispatcher: h.mentionDispatch}
}

// logActivity is the compatibility shim for the call sites that still pass
// a bare action string (issues_internal_relations.go). It records the audit
// row and the journal entry but does not broadcast — those call sites own
// their own broadcast.
func (h *InternalIssueHandler) logActivity(ctx context.Context, missionID, actorType, actorID, action, details string) {
	h.events().log(ctx, issueEvent{
		MissionID: missionID,
		ActorType: actorType,
		ActorID:   actorID,
		Action:    issueAction(action),
		Details:   details,
	})
}

// assertAuthorAgentInWorkspace proves the agent named as a comment's author
// belongs to the caller's workspace, and writes the refusal if it does not.
// Returns true when the caller may proceed, mirroring the shape of
// assertInternalTokenWorkspace so both guards read the same at a call site.
//
// Both comment doors already scope the ISSUE to the workspace, and both then
// accepted any non-empty agent_id as the author. That is one guard short: the
// id is written to mission_comments.author_id and handed to the mention
// recorder as the author, so a foreign id produced a comment attributed to an
// agent this tenant does not have.
//
// 404, not 403: an agent id that does not resolve here is indistinguishable
// from one that does not exist, and saying which would answer the question the
// probe was asking. Same reasoning the issue lookup above it uses.
func (h *InternalIssueHandler) assertAuthorAgentInWorkspace(w http.ResponseWriter, r *http.Request, workspaceID, agentID string) bool {
	if agentID == "" {
		return true // callers reject an empty author separately, with a clearer message
	}
	var exists int
	err := h.db.QueryRowContext(r.Context(),
		`SELECT 1 FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		agentID, workspaceID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		writeProblem(w, r, http.StatusNotFound, "author agent not found in this workspace")
		return false
	}
	if err != nil {
		internalError(w, r, h.logger, "resolve comment author", err)
		return false
	}
	return true
}

// resolveCommentAuthorName reads an agent's display name for the mention
// brief, scoped to the workspace.
//
// The scope is not belt-and-braces even though the callers now prove the
// binding first. A display name is chosen by whoever created the agent, so an
// unscoped read here is a way to pull a string out of another tenant through a
// field nobody thinks of as a read — and it lands inside the task brief handed
// to the woken agent, which is the one place attacker-chosen text is most
// worth having. Keeping the predicate on the query means the guard cannot be
// removed from a caller without this failing too.
//
// Best-effort by design: an unreadable name yields "", and the brief renders
// without it rather than failing a comment that already landed.
func (h *InternalIssueHandler) resolveCommentAuthorName(ctx context.Context, workspaceID, agentID string) string {
	if agentID == "" {
		return ""
	}
	var name string
	_ = h.db.QueryRowContext(ctx,
		`SELECT name FROM agents WHERE id = ? AND workspace_id = ?`, agentID, workspaceID).Scan(&name)
	return name
}

// List handles GET /api/v1/internal/issues
// Returns issues for a workspace, filtered by crew_id, status, assignee, etc.
func (h *InternalIssueHandler) List(w http.ResponseWriter, r *http.Request) {
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		writeProblem(w, r, http.StatusBadRequest, "workspace_id is required")
		return
	}

	limit, offset := parsePagination(r, 50, 100)

	query := `
		SELECT m.id, m.workspace_id, m.crew_id, COALESCE(c.name, ''), COALESCE(c.slug, ''),
		       m.number, m.identifier, m.title, m.description, m.status,
		       COALESCE(m.priority, 'none'), m.assignee_type, m.assignee_id,
		       m.due_date, COALESCE(m.sort_order, 0), COALESCE(m.mission_type, 'mission'),
		       m.lead_agent_id, m.created_at, m.updated_at, m.completed_at,
		       m.author_agent_id, m.created_by_user_id, m.authored_via,
		       CASE
		           WHEN m.author_agent_id IS NOT NULL THEN (SELECT name FROM agents WHERE id = m.author_agent_id)
		           WHEN m.created_by_user_id IS NOT NULL THEN (SELECT full_name FROM users WHERE id = m.created_by_user_id)
		       END
		FROM missions m
		LEFT JOIN crews c ON m.crew_id = c.id
		WHERE m.workspace_id = ?`
	args := []interface{}{wsID}

	// #1186: for a crew-bound (crwv1) token the binding constrains the
	// listing — an omitted ?crew_id returns the token's own crew's issues,
	// not the workspace-wide backlog. Unbound callers keep the optional
	// query filter.
	if crewID := effectiveCrewFilter(r); crewID != "" {
		query += " AND m.crew_id = ?"
		args = append(args, crewID)
	}
	if status := r.URL.Query().Get("status"); status != "" {
		vals := strings.Split(status, ",")
		for _, v := range vals {
			args = append(args, strings.TrimSpace(v))
		}
		query += " AND m.status IN (" + sqlPlaceholders(len(vals)) + ")"
	}
	if assignee := r.URL.Query().Get("assignee_id"); assignee != "" {
		query += " AND m.assignee_id = ?"
		args = append(args, assignee)
	}
	// ?q= free-text search over title and description. An agent looking for
	// "the flaky login issue" has an identifier only if a human already told
	// it one; without this the sidecar's list verb can filter but not FIND.
	//
	// escapeLikeWildcards is not optional here: `%` and `_` are LIKE
	// metacharacters, so an unescaped q=% matches every row and turns a
	// search into a full board dump (TestSecInternalIssueList_WildcardIsLiteral).
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		query += ` AND (m.title LIKE ? ESCAPE '\' OR COALESCE(m.description, '') LIKE ? ESCAPE '\')`
		like := "%" + escapeLikeWildcards(q) + "%"
		args = append(args, like, like)
	}
	if mtype := r.URL.Query().Get("mission_type"); mtype != "" {
		query += " AND m.mission_type = ?"
		args = append(args, mtype)
	} else {
		query += " AND COALESCE(m.mission_type, 'orchestration') = 'issue'"
	}

	query += " ORDER BY m.created_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		internalError(w, r, h.logger, "internal list issues", err)
		return
	}
	defer rows.Close()

	var result []issueResponse
	for rows.Next() {
		var i issueResponse
		var authorAgentID, createdByUserID, authoredVia, creatorName sql.NullString
		if err := rows.Scan(
			&i.ID, &i.WorkspaceID, &i.CrewID, &i.CrewName, &i.CrewSlug,
			&i.Number, &i.Identifier, &i.Title, &i.Description, &i.Status,
			&i.Priority, &i.AssigneeType, &i.AssigneeID,
			&i.DueDate, &i.SortOrder, &i.MissionType,
			&i.LeadAgentID, &i.CreatedAt, &i.UpdatedAt, &i.CompletedAt,
			&authorAgentID, &createdByUserID, &authoredVia, &creatorName,
		); err != nil {
			h.logger.Error("scan internal issue", "error", err)
			continue
		}
		i.Labels = []labelResponse{}
		i.CreatedBy = buildIssueCreator(authorAgentID, createdByUserID, creatorName)
		if authoredVia.Valid && authoredVia.String != "" {
			i.AuthoredVia = &authoredVia.String
		}
		result = append(result, i)
	}
	if result == nil {
		result = []issueResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

// Get handles GET /api/v1/internal/issues/{identifier}
func (h *InternalIssueHandler) Get(w http.ResponseWriter, r *http.Request) {
	ident := r.PathValue("identifier")
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		writeProblem(w, r, http.StatusBadRequest, "workspace_id is required")
		return
	}

	var issue issueResponse
	var authorAgentID, createdByUserID, authoredVia, creatorName sql.NullString
	err := h.db.QueryRowContext(r.Context(), `
		SELECT m.id, m.workspace_id, m.crew_id, COALESCE(c.name, ''), COALESCE(c.slug, ''),
		       m.number, m.identifier, m.title, m.description, m.status,
		       COALESCE(m.priority, 'none'), m.assignee_type, m.assignee_id,
		       m.due_date, COALESCE(m.sort_order, 0), COALESCE(m.mission_type, 'mission'),
		       m.lead_agent_id, m.created_at, m.updated_at, m.completed_at,
		       m.author_agent_id, m.created_by_user_id, m.authored_via,
		       CASE
		           WHEN m.author_agent_id IS NOT NULL THEN (SELECT name FROM agents WHERE id = m.author_agent_id)
		           WHEN m.created_by_user_id IS NOT NULL THEN (SELECT full_name FROM users WHERE id = m.created_by_user_id)
		       END
		FROM missions m
		LEFT JOIN crews c ON m.crew_id = c.id
		WHERE m.identifier = ? AND m.workspace_id = ?`,
		ident, wsID).Scan(
		&issue.ID, &issue.WorkspaceID, &issue.CrewID, &issue.CrewName, &issue.CrewSlug,
		&issue.Number, &issue.Identifier, &issue.Title, &issue.Description, &issue.Status,
		&issue.Priority, &issue.AssigneeType, &issue.AssigneeID,
		&issue.DueDate, &issue.SortOrder, &issue.MissionType,
		&issue.LeadAgentID, &issue.CreatedAt, &issue.UpdatedAt, &issue.CompletedAt,
		&authorAgentID, &createdByUserID, &authoredVia, &creatorName,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		internalError(w, r, h.logger, "internal get issue", err)
		return
	}
	issue.CreatedBy = buildIssueCreator(authorAgentID, createdByUserID, creatorName)
	if authoredVia.Valid && authoredVia.String != "" {
		issue.AuthoredVia = &authoredVia.String
	}

	// Load labels
	issue.Labels = []labelResponse{}
	labelRows, err := h.db.QueryContext(r.Context(), `
		SELECT l.id, l.name, l.color, l.label_group
		FROM mission_labels ml JOIN labels l ON ml.label_id = l.id
		WHERE ml.mission_id = ?`, issue.ID)
	if err == nil {
		defer labelRows.Close()
		for labelRows.Next() {
			var lbl labelResponse
			if err := labelRows.Scan(&lbl.ID, &lbl.Name, &lbl.Color, &lbl.LabelGroup); err == nil {
				issue.Labels = append(issue.Labels, lbl)
			}
		}
	}

	// Load comment count
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM mission_comments WHERE mission_id = ?`,
		issue.ID).Scan(&issue.CommentCount); err != nil {
		h.logger.Error("load comment count", "issue_id", issue.ID, "error", err)
	}

	// Attached pull requests, with the provider-supplied free text fenced.
	// This is the agent-facing read; see loadAgentCodeLinks.
	issue.CodeLinks = h.loadAgentCodeLinks(r.Context(), issue.ID)

	writeJSON(w, http.StatusOK, issue)
}

// agentCodeLink is one attached pull request as an AGENT sees it.
//
// URL, Provider and State are ours: the URL survived gitlink.Parse's
// character-restricted grammar, and State is a four-value enum we derived.
// Everything the pull-request AUTHOR typed — its title, their display name,
// the branch names — is external content that arrives inside an agent prompt,
// which is the ingress-side prompt-injection surface internal/untrusted
// exists for (OWASP LLM01). Anyone who can open a PR against a linked
// repository can put "ignore your previous instructions" in its title; on a
// public repo, that is anyone at all.
//
// It is collapsed into ONE fenced block per link rather than four, because
// the fence's contract is a nonce-delimited region of pure data and four of
// them per pull request would be noise the model has to re-parse. The block
// carries a per-call random nonce, so a title containing a literal
// </untrusted> cannot close it.
type agentCodeLink struct {
	URL      string `json:"url"`
	Provider string `json:"provider"`
	State    string `json:"state,omitempty"`
	// Details is the fenced block. It is NOT a plain string to be
	// concatenated blind: it already carries its own <untrusted …> wrapper.
	Details string `json:"details,omitempty"`
}

// loadAgentCodeLinks reads an issue's code links for agent consumption.
// Best-effort: a failure here must not take the issue read down with it, so
// it logs and returns nothing.
func (h *InternalIssueHandler) loadAgentCodeLinks(ctx context.Context, missionID string) []agentCodeLink {
	rows, err := h.db.QueryContext(ctx, `
		SELECT url, provider, COALESCE(state, ''), COALESCE(title, ''), COALESCE(author, ''),
		       COALESCE(source_branch, ''), COALESCE(target_branch, '')
		FROM mission_code_links
		WHERE mission_id = ?
		ORDER BY created_at DESC, id DESC`, missionID)
	if err != nil {
		h.logger.Error("load agent code links", "issue_id", missionID, "error", err)
		return nil
	}
	defer rows.Close()

	var out []agentCodeLink
	for rows.Next() {
		var url, provider, state, title, author, src, dst string
		if err := rows.Scan(&url, &provider, &state, &title, &author, &src, &dst); err != nil {
			h.logger.Error("scan agent code link", "issue_id", missionID, "error", err)
			continue
		}
		var b strings.Builder
		if title != "" {
			fmt.Fprintf(&b, "Title: %s\n", title)
		}
		if author != "" {
			fmt.Fprintf(&b, "Author: %s\n", author)
		}
		if src != "" || dst != "" {
			fmt.Fprintf(&b, "Branch: %s -> %s\n", src, dst)
		}
		link := agentCodeLink{URL: url, Provider: provider, State: state}
		if b.Len() > 0 {
			link.Details = untrusted.Wrap("git_pull_request", strings.TrimRight(b.String(), "\n"))
		}
		out = append(out, link)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (agent code links)", "issue_id", missionID, "error", err)
	}
	return out
}

// Create handles POST /api/v1/internal/issues
// Allows agents to create issues via the sidecar.
func (h *InternalIssueHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WorkspaceID   string   `json:"workspace_id"`
		CrewID        string   `json:"crew_id"`
		Title         string   `json:"title"`
		Description   *string  `json:"description"`
		Priority      string   `json:"priority"`
		AssigneeType  *string  `json:"assignee_type"`
		AssigneeID    *string  `json:"assignee_id"`
		AuthorAgentID string   `json:"author_agent_id"`
		AuthorChatID  string   `json:"author_chat_id"`
		AuthorRunID   string   `json:"author_run_id"`
		Labels        []string `json:"labels"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Title == "" || req.CrewID == "" || req.WorkspaceID == "" {
		writeProblem(w, r, http.StatusBadRequest, "title, crew_id, and workspace_id are required")
		return
	}
	// PR-F24 F-4: a bound token may only write into its own workspace.
	// requireInternal sees only the query string; this guards the
	// body-carried workspace_id (403 on a foreign tenant).
	if !assertInternalTokenWorkspace(w, r, req.WorkspaceID) {
		return
	}
	// #1186: crew_id is body-carried, so requireInternal's ?crew_id gate
	// never sees it. A crew-bound (crwv1) token may only create issues in
	// its OWN crew (the author-agent check below runs only when an
	// author_agent_id is supplied, so it alone cannot close this); a
	// workspace-bound token's crew must resolve to the bound workspace
	// (PR-F24 foreign-ID closure).
	if !assertBoundCrewWorkspaceDB(w, r, h.db, h.logger, &req.CrewID) {
		return
	}
	if req.Priority == "" {
		req.Priority = "none"
	}

	// SECURITY (defense-in-depth): when an author agent is supplied, verify it
	// actually belongs to the supplied crew+workspace. Without this, a
	// compromised agent could create an issue in another crew (cross-crew
	// override). The sidecar always forwards its trusted IPC agent identity.
	if req.AuthorAgentID != "" {
		var exists int
		err := h.db.QueryRowContext(r.Context(),
			`SELECT 1 FROM agents WHERE id = ? AND crew_id = ? AND workspace_id = ? AND deleted_at IS NULL`,
			req.AuthorAgentID, req.CrewID, req.WorkspaceID).Scan(&exists)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeProblem(w, r, http.StatusBadRequest, "author agent does not belong to the specified crew/workspace")
				return
			}
			internalError(w, r, h.logger, "validate author agent", err)
			return
		}
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		internalError(w, r, h.logger, "begin tx", err)
		return
	}
	defer tx.Rollback() //nolint:errcheck

	// Creator attribution (v129): this endpoint is only reachable through
	// the sidecar's trusted IPC identity, so a supplied author_agent_id
	// (validated against crew+workspace above) is THE creator and the
	// channel is always the agent tool call. Chat/run provenance rides
	// along when the sidecar has it (v108 columns). All issue creation goes
	// through insertIssueTx — the single chokepoint shared with the
	// recurring-issue dispatcher.
	//
	// req.AssigneeID/req.AssigneeType are request-body-supplied and were NOT
	// validated against req.WorkspaceID anywhere on this path until this
	// endpoint was flagged as the 6th unguarded assignee_id write by
	// assignee_write_invariant_test.go — insertIssueTx now validates it (see
	// that function's comment); this handler only maps the resulting sentinel
	// errors below.
	id, identifier, err := insertIssueTx(r.Context(), tx, h.logger, issueSpec{
		WorkspaceID:   req.WorkspaceID,
		CrewID:        req.CrewID,
		Title:         req.Title,
		Description:   req.Description,
		Priority:      req.Priority,
		AssigneeType:  req.AssigneeType,
		AssigneeID:    req.AssigneeID,
		Labels:        req.Labels,
		AuthoredVia:   "agent_tool_call",
		AuthorAgentID: req.AuthorAgentID,
		AuthorChatID:  req.AuthorChatID,
		AuthorRunID:   req.AuthorRunID,
	})
	switch {
	case errors.Is(err, errIssueCrewNotFound):
		writeProblem(w, r, http.StatusNotFound, "Crew not found")
		return
	case errors.Is(err, errIssueNoLeadAgent):
		h.logger.Error("find lead agent", "crew_id", req.CrewID)
		writeProblem(w, r, http.StatusBadRequest, "Crew has no LEAD agent")
		return
	case errors.Is(err, errIssueAssigneeTypeInvalid), errors.Is(err, errIssueAssigneeNotInWorkspace):
		writeProblem(w, r, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		internalError(w, r, h.logger, "insert issue", err)
		return
	}

	if err := tx.Commit(); err != nil {
		internalError(w, r, h.logger, "commit issue", err)
		return
	}

	broadcastWorkspaceEvent(h.hub, req.WorkspaceID, "issue.created", map[string]string{"id": id, "identifier": identifier, "title": req.Title})

	writeJSON(w, http.StatusCreated, map[string]string{
		"id":         id,
		"identifier": identifier,
		"status":     "BACKLOG",
	})
}

// UpdateStatus handles PATCH /api/v1/internal/issues/{identifier}
//
// Despite the name (kept so the route registration, the docs and the existing
// #1365 security suite stay pointing at the same symbol) this is the general
// agent-facing issue PATCH: status, priority, assignee, labels, estimate, due
// date, and an optional comment in the same call.
//
// Every field beyond status/priority names a row in ANOTHER table, and each one
// is fenced to the token's workspace the same way the public handler fences the
// session's:
//
//   - assignee_id → validateAssigneeWorkspace / resolveAssigneeType. A foreign
//     id is a 400, not a silent write: the read path resolves a display name
//     for whatever id is stored, so persisting a foreign one leaks that
//     tenant's user/agent name into this one.
//   - labels → the INSERT itself is workspace-scoped (SELECT … WHERE
//     workspace_id = ?), so a foreign label id simply attaches nothing.
//
// The fields this handler deliberately does NOT accept are the ones whose
// meaning is structural rather than editorial: title/description (rewriting the
// issue a human wrote is not a progress report), project_id, milestone_id,
// routine_id, sort_order, and parent_issue_id — the last one is reachable, but
// only through CreateRelation's sub_issue_of, where it is gated as a link
// rather than a free-form column write.
func (h *InternalIssueHandler) UpdateStatus(w http.ResponseWriter, r *http.Request) {
	ident := r.PathValue("identifier")

	var req struct {
		WorkspaceID  string    `json:"workspace_id"`
		Status       string    `json:"status"`
		Priority     string    `json:"priority"`
		Comment      *string   `json:"comment"`
		AgentID      string    `json:"agent_id"`
		AssigneeType *string   `json:"assignee_type"`
		AssigneeID   *string   `json:"assignee_id"`
		DueDate      *string   `json:"due_date"`
		Estimate     *int      `json:"estimate"`
		Labels       *[]string `json:"labels"`
		// The routine run making this call. crewshipBody has always sent it;
		// this struct did not decode it, so it was dropped on the floor and
		// the resulting journal entry named no cause. That is what made the
		// composition depth cap unreachable — see
		// TestInternalIssue_UpdateStatus_StampsTheCausingRunOnTheJournalEntry.
		AuthorRunID string `json:"author_run_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.WorkspaceID == "" {
		writeProblem(w, r, http.StatusBadRequest, "workspace_id is required")
		return
	}
	// PR-F24 F-4: bound token may only update its own workspace's issues.
	if !assertInternalTokenWorkspace(w, r, req.WorkspaceID) {
		return
	}

	// Find the issue
	var missionID, currentStatus, crewID string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id, status, crew_id FROM missions WHERE identifier = ? AND workspace_id = ?`,
		ident, req.WorkspaceID).Scan(&missionID, &currentStatus, &crewID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		internalError(w, r, h.logger, "find issue for update", err)
		return
	}
	// #1365: a crew-bound (crwv1) token may only mutate its OWN crew's issues.
	// The workspace check above is necessary but not sufficient — a sibling
	// crew shares the tenant, and issue CREATE already enforces this boundary.
	// Fold the issue's own crew_id through the same helper the create path uses
	// so crwv1 tokens are held to their crew and wsv1 tokens keep workspace reach.
	if !assertBoundCrewWorkspaceDB(w, r, h.db, h.logger, &crewID) {
		return
	}

	// Comments must carry a real author. mission_comments' CHECK only
	// allows ('user','agent'), so an empty agent_id can't be attributed —
	// pre-fix it was misfiled as an agent literally named "system".
	// Reject up front, before any mutation lands.
	if req.Comment != nil && *req.Comment != "" && req.AgentID == "" {
		writeProblem(w, r, http.StatusBadRequest, "agent_id is required when adding a comment")
		return
	}
	// …and it must be an agent of THIS workspace. The issue is already scoped
	// (F-4 below); the author was not, so an inline comment could be attributed
	// to a stranger — see resolveCommentAuthorName for why that is a read of
	// another tenant and not only a mis-attribution.
	if req.Comment != nil && *req.Comment != "" &&
		!h.assertAuthorAgentInWorkspace(w, r, req.WorkspaceID, req.AgentID) {
		return
	}

	// Actor identity for the audit trail. The sidecar forwards its trusted
	// IPC agent identity as agent_id; an empty value means a non-agent
	// internal caller, attributed to "system" (mission_activity's CHECK
	// allows it) rather than misfiled as an agent named "system".
	actorType := "agent"
	actorID := req.AgentID
	if actorID == "" {
		actorType = "system"
		actorID = "system"
	}

	ub := newUpdate()
	statusChanged := false

	if req.Status != "" && req.Status != currentStatus {
		allowed := validIssueTransitions[currentStatus]
		valid := false
		for _, a := range allowed {
			if a == req.Status {
				valid = true
				break
			}
		}
		if !valid {
			writeProblem(w, r, http.StatusBadRequest, "Invalid status transition from "+currentStatus+" to "+req.Status)
			return
		}
		ub.Set("status", req.Status)
		statusChanged = true
		if req.Status == "DONE" || req.Status == "CANCELLED" || req.Status == "DUPLICATE" {
			ub.Set("completed_at", time.Now().UTC().Format(time.RFC3339))
		}
	}
	if req.Priority != "" {
		ub.Set("priority", req.Priority)
	}

	// Assignee. An empty string is an explicit UNASSIGN (both columns go NULL
	// together — a dangling assignee_type with no id renders as "assigned to
	// nobody, of type agent" in every read path).
	assigneeChanged := false
	if req.AssigneeID != nil {
		if *req.AssigneeID == "" {
			ub.SetNull("assignee_id")
			ub.SetNull("assignee_type")
			assigneeChanged = true
		} else {
			assigneeType := ""
			if req.AssigneeType != nil {
				assigneeType = *req.AssigneeType
				if assigneeType != "user" && assigneeType != "agent" {
					writeProblem(w, r, http.StatusBadRequest,
						"assignee_type must be 'user' or 'agent' when assignee_id is set")
					return
				}
				ok, vErr := validateAssigneeWorkspace(r.Context(), h.db, assigneeType, *req.AssigneeID, req.WorkspaceID)
				if vErr != nil {
					internalError(w, r, h.logger, "validate assignee_id", vErr)
					return
				}
				if !ok {
					writeProblem(w, r, http.StatusBadRequest, "assignee_id does not exist in this workspace")
					return
				}
			} else {
				// Type omitted: resolve it rather than inheriting the row's
				// stale value. Trusting the stale type false-rejects a
				// legitimate user→agent reassignment (the public handler's
				// bug; do not reintroduce it here).
				var ok bool
				var rErr error
				assigneeType, ok, rErr = resolveAssigneeType(r.Context(), h.db, *req.AssigneeID, req.WorkspaceID)
				if rErr != nil {
					internalError(w, r, h.logger, "resolve assignee_type", rErr)
					return
				}
				if !ok {
					writeProblem(w, r, http.StatusBadRequest, "assignee_id does not exist in this workspace")
					return
				}
			}
			ub.Set("assignee_type", assigneeType)
			ub.Set("assignee_id", *req.AssigneeID)
			assigneeChanged = true
		}
	}
	if req.DueDate != nil {
		if *req.DueDate == "" {
			ub.SetNull("due_date")
		} else {
			// due_date is a TEXT column, so an unvalidated write persists
			// whatever the model produced. A human picking a date in the UI
			// cannot send "tomorrow", "next sprint" or "2026-13-45"; an agent
			// writing JSON by hand can, and every reader downstream
			// (parseISODateAsLocal in the issue panel, the overdue filters)
			// then gets an Invalid Date it renders as garbage. Parse it here,
			// where the value enters, rather than defending in each reader.
			if !validIssueDueDate(*req.DueDate) {
				writeProblem(w, r, http.StatusBadRequest,
					"due_date must be YYYY-MM-DD or an RFC 3339 timestamp")
				return
			}
			ub.Set("due_date", *req.DueDate)
		}
	}
	if req.Estimate != nil {
		ub.Set("estimate", *req.Estimate)
	}

	hasComment := req.Comment != nil && *req.Comment != ""
	// Set inside the transaction, read after it commits, by the @mention
	// trigger. Empty means no inline comment was written, so nothing to parse.
	var inlineCommentID string

	// The column update, the label replacement and the comment go in ONE
	// transaction, the same shape Create already uses. Label replacement is
	// DELETE-then-INSERT: run outside a transaction, a failure between the two
	// leaves the issue stripped of the labels it had and carrying only some of
	// the requested ones, while the handler still answers 200 — a partial write
	// the caller records as a success.
	//
	// The comment is in here for the same reason and one more. It used to be a
	// bare Exec whose error was logged under that same 200 — the twin of the
	// label bug, ten lines below it — and this PR makes the comment the agent's
	// primary way of reporting progress to a human. "Moved to IN_PROGRESS" with
	// the explanation silently dropped is worse than the whole call failing.
	if !ub.Empty() || req.Labels != nil || hasComment {
		tx, err := h.db.BeginTx(r.Context(), nil)
		if err != nil {
			internalError(w, r, h.logger, "begin issue update tx", err)
			return
		}
		defer tx.Rollback() //nolint:errcheck

		if !ub.Empty() {
			query, args := ub.Build("missions", "id = ?", missionID)
			if _, err := tx.ExecContext(r.Context(), query, args...); err != nil {
				internalError(w, r, h.logger, "update issue", err)
				return
			}
		}

		// Comment goes in before the labels, not after. Ordering inside a
		// transaction is normally irrelevant — but the comment is the row that
		// describes WHY the other changes happened, so if a later statement
		// fails it must be the rollback that removes it, not the fact that it
		// was never reached. Writing it last makes "no comment on failure" true
		// by accident, which is unprovable and stops being true the moment
		// another statement is appended below it.
		//
		// agent_id presence was validated above, so the author is always a real
		// agent here.
		if hasComment {
			now := time.Now().UTC().Format(time.RFC3339)
			// The id is hoisted out of the transaction because the @mention
			// trigger runs AFTER the commit and needs to name the row it is
			// recording mentions for. A PATCH's inline comment is a third
			// comment door (#1768 item 3) — it carries agent-authored prose
			// exactly like CreateComment does, so it must parse too.
			inlineCommentID = generateCUID()
			if _, err := tx.ExecContext(r.Context(), `
				INSERT INTO mission_comments (id, mission_id, author_type, author_id, body, created_at, updated_at)
				VALUES (?, ?, 'agent', ?, ?, ?, ?)`,
				inlineCommentID, missionID, req.AgentID, *req.Comment, now, now); err != nil {
				internalError(w, r, h.logger, "insert internal comment", err)
				return
			}
		}

		// Labels are a full REPLACEMENT (matching the public handler), so an
		// empty array clears them. The insert is workspace-scoped by
		// construction: mission_labels carries no workspace column, so a label
		// id from another tenant would otherwise attach and the read path's
		// join would render that tenant's label name and colour inside this
		// one. A foreign / unknown id therefore matches no row and attaches
		// nothing — that silence is the intended behaviour, and distinct from
		// a driver or constraint error, which now fails the whole request
		// instead of being logged under a 200.
		if req.Labels != nil {
			if _, err := tx.ExecContext(r.Context(),
				`DELETE FROM mission_labels WHERE mission_id = ?`, missionID); err != nil {
				internalError(w, r, h.logger, "clear issue labels", err)
				return
			}
			for _, labelID := range *req.Labels {
				if _, err := tx.ExecContext(r.Context(),
					`INSERT OR IGNORE INTO mission_labels (mission_id, label_id)
					 SELECT ?, id FROM labels WHERE id = ? AND workspace_id = ?`,
					missionID, labelID, req.WorkspaceID); err != nil {
					internalError(w, r, h.logger, "insert mission label", err)
					return
				}
			}
		}

		if err := tx.Commit(); err != nil {
			internalError(w, r, h.logger, "commit issue update", err)
			return
		}
	}

	// Audit trail: the same rows, the same journal entries and the same
	// broadcast the human handlers produce. Best-effort and after the commit —
	// the mutation has already landed, and a failed activity row must not roll
	// it back.
	//
	// Collected first and handed to record() in one call so the batch produces
	// exactly ONE issue.updated broadcast, not one per changed field. A
	// label-only or comment-only PATCH collects no events and still
	// broadcasts — the board changed even when no field did.
	var evs []issueEvent
	ev := func(action issueAction, details string) {
		evs = append(evs, issueEvent{
			MissionID: missionID, ActorType: actorType, ActorID: actorID,
			RunID:  req.AuthorRunID,
			Action: action, Details: details,
		})
	}
	if !ub.Empty() {
		if statusChanged {
			evs = append(evs, issueEvent{
				MissionID: missionID, ActorType: actorType, ActorID: actorID,
				// The run that caused it. This is the pointer the composition
				// depth cap resolves; without it every automation hop opens a
				// fresh budget and the cap is unreachable.
				RunID:   req.AuthorRunID,
				Action:  actionStatusChanged,
				Details: currentStatus + " → " + req.Status,
				From:    currentStatus, To: req.Status,
			})
		}
		if req.Priority != "" {
			ev(actionPriorityChanged, req.Priority)
		}
		if assigneeChanged {
			ev(actionAssigneeChanged, "assignee_id: "+*req.AssigneeID)
		}
	}
	h.events().record(r.Context(), req.WorkspaceID,
		map[string]string{"id": missionID, "identifier": ident}, evs...)

	// @mentions in the inline comment. After the commit and after the audit
	// batch, for the same reason both of those are after the mutation: the
	// PATCH has already landed and been answered, and a mention that could not
	// be recorded must not undo it.
	if inlineCommentID != "" {
		var issueTitle, authorName string
		_ = h.db.QueryRowContext(r.Context(), `SELECT title FROM missions WHERE id = ?`, missionID).Scan(&issueTitle)
		authorName = h.resolveCommentAuthorName(r.Context(), req.WorkspaceID, req.AgentID)
		h.mentionRecorder().record(r.Context(), mentionContext{
			WorkspaceID: req.WorkspaceID,
			MissionID:   missionID,
			Identifier:  ident,
			IssueTitle:  issueTitle,
			IssueCrewID: crewID,
			CommentID:   inlineCommentID,
			CommentBody: *req.Comment,
			AuthorType:  "agent",
			AuthorID:    req.AgentID,
			AuthorName:  authorName,
		})
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// CreateComment handles POST /api/v1/internal/issues/{identifier}/comments
// Allows agents to comment on issues.
func (h *InternalIssueHandler) CreateComment(w http.ResponseWriter, r *http.Request) {
	ident := r.PathValue("identifier")

	var req struct {
		WorkspaceID string `json:"workspace_id"`
		AgentID     string `json:"agent_id"`
		Body        string `json:"body"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Body == "" || req.WorkspaceID == "" {
		writeProblem(w, r, http.StatusBadRequest, "body and workspace_id are required")
		return
	}
	// Comments must carry a real author: mission_comments' CHECK only
	// allows ('user','agent'). Pre-fix, an empty agent_id was misfiled as
	// an agent literally named "system" — reject instead.
	if req.AgentID == "" {
		writeProblem(w, r, http.StatusBadRequest, "agent_id is required")
		return
	}
	// PR-F24 F-4: bound token may only comment on its own workspace's issues.
	if !assertInternalTokenWorkspace(w, r, req.WorkspaceID) {
		return
	}
	// The AUTHOR needs the same binding the issue has. Without it the id is
	// accepted on nothing but being non-empty.
	if !h.assertAuthorAgentInWorkspace(w, r, req.WorkspaceID, req.AgentID) {
		return
	}

	var missionID, crewID, issueTitle string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id, crew_id, title FROM missions WHERE identifier = ? AND workspace_id = ?`,
		ident, req.WorkspaceID).Scan(&missionID, &crewID, &issueTitle)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		internalError(w, r, h.logger, "find issue for comment", err)
		return
	}
	// #1365: a crew-bound (crwv1) token may only comment on its OWN crew's
	// issues — mirror the guard on UpdateStatus and the issue CREATE path.
	if !assertBoundCrewWorkspaceDB(w, r, h.db, h.logger, &crewID) {
		return
	}

	commentID := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	// agent_id presence was validated above — the author is always a real agent.
	authorType := "agent"
	authorID := req.AgentID

	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO mission_comments (id, mission_id, author_type, author_id, body, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		commentID, missionID, authorType, authorID, req.Body, now, now)
	if err != nil {
		internalError(w, r, h.logger, "insert comment", err)
		return
	}

	// @mentions (#1768 item 3) — the AGENT door. Same wire format, same
	// resolution, same caps as the human one; the difference is that an agent
	// author carries a position in the delegation tree, so a mention chain
	// (A mentions B, B's reply mentions C) is bounded by the depth cap read
	// off the assignment row A is executing. See issue_mentions.go.
	var authorName string
	authorName = h.resolveCommentAuthorName(r.Context(), req.WorkspaceID, authorID)
	h.mentionRecorder().record(r.Context(), mentionContext{
		WorkspaceID: req.WorkspaceID,
		MissionID:   missionID,
		Identifier:  ident,
		IssueTitle:  issueTitle,
		IssueCrewID: crewID,
		CommentID:   commentID,
		CommentBody: req.Body,
		AuthorType:  authorType,
		AuthorID:    authorID,
		AuthorName:  authorName,
	})

	broadcastWorkspaceEvent(h.hub, req.WorkspaceID, "issue.updated", map[string]string{"id": missionID, "identifier": ident})

	writeJSON(w, http.StatusCreated, commentResponse{
		ID:         commentID,
		MissionID:  missionID,
		AuthorType: authorType,
		AuthorID:   authorID,
		Body:       req.Body,
		CreatedAt:  now,
		UpdatedAt:  now,
	})
}
