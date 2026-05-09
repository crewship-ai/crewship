package api

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
)

// PendingEscalationCount returns the number of unresolved escalations workspace-wide.
func (h *QueryHandler) PendingEscalationCount(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	var count int
	err := h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM escalations e
		 JOIN crews c ON c.id = e.crew_id
		 WHERE c.workspace_id = ? AND e.status = 'PENDING' AND c.deleted_at IS NULL`,
		workspaceID).Scan(&count)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"count": count})
}

// CreateEscalation handles POST /api/v1/internal/escalations.
// Auth: protected by internalAuth middleware (X-Internal-Token) in router.go.
func (h *QueryHandler) CreateEscalation(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FromSlug    string `json:"from_slug"`
		Reason      string `json:"reason"`
		Context     string `json:"context"`
		Type        string `json:"type"`
		Metadata    string `json:"metadata"`
		CrewID      string `json:"crew_id"`
		WorkspaceID string `json:"workspace_id"`
		ChatID      string `json:"chat_id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}
	if body.FromSlug == "" || body.Reason == "" || body.CrewID == "" || body.WorkspaceID == "" || body.ChatID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "from_slug, reason, crew_id, workspace_id, chat_id required",
		})
		return
	}

	// Look up the from agent
	var fromAgentID string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT id FROM agents WHERE slug = ? AND crew_id = ? AND deleted_at IS NULL
	`, body.FromSlug, body.CrewID).Scan(&fromAgentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "from agent not found"})
			return
		}
		h.logger.Error("lookup from agent for escalation", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	escalationID := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	var contextVal interface{}
	if body.Context != "" {
		contextVal = body.Context
	}

	escalationType := body.Type
	if escalationType == "" {
		escalationType = "TEXT"
	}
	if escalationType != "TEXT" && escalationType != "CREDENTIAL" && escalationType != "LINK" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "type must be TEXT, CREDENTIAL, or LINK"})
		return
	}

	if escalationType == "LINK" {
		if body.Metadata == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "metadata (https URL) required for LINK type"})
			return
		}
		u, parseErr := url.ParseRequestURI(body.Metadata)
		if parseErr != nil || u.Scheme != "https" || u.Host == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "metadata must be a valid https URL"})
			return
		}
	}

	var metadataVal interface{}
	if body.Metadata != "" {
		metadataVal = body.Metadata
	}

	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, context, type, metadata, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'PENDING', ?)
	`, escalationID, body.WorkspaceID, body.CrewID, body.ChatID, fromAgentID, body.Reason, contextVal, escalationType, metadataVal, now)
	if err != nil {
		h.logger.Error("create escalation", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Write-through to inbox_items so the escalation surfaces in the
	// unified Inbox without a fan-out query at read time. Best-effort:
	// failure here is logged + swallowed; the escalations table stays
	// the source of truth and a future inbox-rebuild job can backfill.
	inbox.Insert(r.Context(), h.db, h.logger, inbox.Item{
		WorkspaceID: body.WorkspaceID,
		Kind:        "escalation",
		SourceID:    escalationID,
		TargetRole:  "MANAGER",
		Title:       fmt.Sprintf("Agent escalation: %s", truncate(body.Reason, 80)),
		BodyMD:      body.Context,
		SenderType:  "agent",
		SenderID:    fromAgentID,
		SenderName:  body.FromSlug,
		Priority:    "high",
		Blocking:    true,
		Payload: map[string]interface{}{
			"crew_id":         body.CrewID,
			"chat_id":         body.ChatID,
			"reason":          body.Reason,
			"escalation_type": escalationType,
		},
	})

	// Dual-write the escalation into the journal. Severity=warn because
	// an unresolved escalation should surface in the default "things
	// needing attention" filter (severity IN (warn, error)).
	_, _ = h.journal.Emit(r.Context(), journal.Entry{
		WorkspaceID: body.WorkspaceID,
		CrewID:      body.CrewID,
		AgentID:     fromAgentID,
		Type:        journal.EntryPeerEscalation,
		Severity:    journal.SeverityWarn,
		ActorType:   journal.ActorAgent,
		ActorID:     fromAgentID,
		Summary:     fmt.Sprintf("escalation from %s: %s", body.FromSlug, truncate(body.Reason, 140)),
		Payload: map[string]any{
			"reason":          body.Reason,
			"context":         body.Context,
			"escalation_type": escalationType,
			"metadata":        body.Metadata,
			"from_slug":       body.FromSlug,
			"state":           "pending",
		},
		Refs: map[string]any{"escalation_id": escalationID, "chat_id": body.ChatID},
	})

	// Broadcast escalation event
	broadcastChannelEvent(h.hub, "session", body.ChatID, "escalation_created",
		map[string]string{
			"id":     escalationID,
			"from":   body.FromSlug,
			"reason": body.Reason,
		})
	broadcastWorkspaceEvent(h.hub, body.WorkspaceID, "escalation.created",
		map[string]string{
			"id":        escalationID,
			"crew_id":   body.CrewID,
			"from_slug": body.FromSlug,
			"reason":    body.Reason,
		})

	h.logger.Info("escalation created",
		"escalation_id", escalationID,
		"from", body.FromSlug,
		"crew_id", body.CrewID,
	)

	writeJSON(w, http.StatusCreated, map[string]string{
		"escalation_id": escalationID,
		"status":        "PENDING",
	})
}

// ResolveEscalation handles PATCH /api/v1/escalations/{escalationId}/resolve.
func (h *QueryHandler) ResolveEscalation(w http.ResponseWriter, r *http.Request) {
	escalationID := r.PathValue("escalationId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	// Require at least MANAGER to resolve escalations (data-modifying operation)
	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	var body struct {
		Resolution string `json:"resolution"`
		Action     string `json:"action"`
		RedirectTo string `json:"redirect_to"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}
	if body.Resolution == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "resolution required"})
		return
	}

	// Default action to "approve" for backward compatibility.
	if body.Action == "" {
		body.Action = "approve"
	}
	if body.Action != "approve" && body.Action != "reject" && body.Action != "redirect" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "action must be approve, reject, or redirect"})
		return
	}
	if body.Action == "redirect" && body.RedirectTo == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "redirect_to required when action is redirect"})
		return
	}
	if body.Action != "redirect" {
		body.RedirectTo = ""
	}

	var status, chatID, crewID, fromSlug, escalationType string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT e.status, e.chat_id, e.crew_id, a.slug, e.type
		FROM escalations e
		JOIN agents a ON a.id = e.from_agent_id
		WHERE e.id = ? AND e.workspace_id = ?
	`, escalationID, workspaceID).Scan(&status, &chatID, &crewID, &fromSlug, &escalationType)

	// Validate redirect_to agent exists in the same crew (after we know crew_id).
	if err == nil && body.Action == "redirect" && body.RedirectTo != "" {
		var exists int
		if scanErr := h.db.QueryRowContext(r.Context(), `
			SELECT COUNT(*) FROM agents WHERE slug = ? AND crew_id = ? AND deleted_at IS NULL
		`, body.RedirectTo, crewID).Scan(&exists); scanErr != nil {
			h.logger.Error("resolve escalation redirect lookup", "error", scanErr)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		if exists == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("redirect_to agent %q not found in crew", body.RedirectTo),
			})
			return
		}
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "escalation not found"})
			return
		}
		h.logger.Error("resolve escalation lookup", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if status != "PENDING" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "escalation already resolved"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// For CREDENTIAL escalations encrypt the value at rest; for others store as-is.
	storedResolution := body.Resolution
	if escalationType == "CREDENTIAL" {
		enc, encErr := encryption.Encrypt(body.Resolution)
		if encErr != nil {
			h.logger.Error("encrypt credential resolution", "error", encErr)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		storedResolution = enc
	}

	var redirectToVal interface{}
	if body.RedirectTo != "" {
		redirectToVal = body.RedirectTo
	}

	result, err := h.db.ExecContext(r.Context(), `
		UPDATE escalations SET status = 'RESOLVED', resolution = ?, action = ?, redirect_to = ?, resolved_at = ?, resolved_by = 'user'
		WHERE id = ? AND workspace_id = ? AND status = 'PENDING'
	`, storedResolution, body.Action, redirectToVal, now, escalationID, workspaceID)
	if err != nil {
		h.logger.Error("resolve escalation update", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	n, err := result.RowsAffected()
	if err != nil {
		h.logger.Error("resolve escalation rows affected", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if n == 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "escalation already resolved"})
		return
	}

	// Mirror the resolution into the unified inbox so the row drops
	// from "needs action" into the resolved feed in real time. Done
	// after the source UPDATE so we don't flip the inbox row before
	// the source actually transitions.
	if user := UserFromContext(r.Context()); user != nil {
		inbox.ResolveBySource(r.Context(), h.db, h.logger,
			"escalation", escalationID, body.Action, user.ID)
	} else {
		inbox.ResolveBySource(r.Context(), h.db, h.logger,
			"escalation", escalationID, body.Action, "")
	}

	// Resolution closes the escalation thread in the journal. Severity
	// stays at notice (not warn) because the ongoing-attention signal
	// ended — filters on "warn+ only" will drop this correctly.
	//
	// CRITICAL: CREDENTIAL escalations carry secret material in
	// body.Resolution (that's why the storage path above encrypts it
	// before writing to the escalations table). Never write the raw
	// value into the journal payload — the journal is a broadcast
	// stream visible to every workspace reader. Replace with an
	// opaque marker instead; the encrypted value in `escalations.
	// resolution` stays the canonical record.
	resolutionForJournal := body.Resolution
	if escalationType == "CREDENTIAL" {
		resolutionForJournal = "***REDACTED:credential***"
	}
	_, _ = h.journal.Emit(r.Context(), journal.Entry{
		WorkspaceID: workspaceID,
		Type:        journal.EntryPeerEscalation,
		Severity:    journal.SeverityNotice,
		ActorType:   journal.ActorUser,
		Summary:     fmt.Sprintf("escalation %s resolved (%s)", escalationID, body.Action),
		Payload: map[string]any{
			"resolution":      resolutionForJournal,
			"action":          body.Action,
			"redirect_to":     body.RedirectTo,
			"state":           "resolved",
			"escalation_type": escalationType,
		},
		Refs: map[string]any{"escalation_id": escalationID},
	})

	// Notify any waiting sidecar that the escalation has been resolved.
	h.notifyEscalationWaiter(escalationID, escalationResult{
		Resolution: body.Resolution,
		Action:     body.Action,
		RedirectTo: body.RedirectTo,
	})

	broadcastResolution := body.Resolution
	if escalationType == "CREDENTIAL" {
		broadcastResolution = "[credential submitted]"
	}
	broadcastChannelEvent(h.hub, "session", chatID, "escalation_resolved",
		map[string]string{
			"id":         escalationID,
			"resolution": broadcastResolution,
			"action":     body.Action,
		})
	broadcastWorkspaceEvent(h.hub, workspaceID, "escalation.resolved",
		map[string]string{
			"id":        escalationID,
			"crew_id":   crewID,
			"from_slug": fromSlug,
			"action":    body.Action,
		})

	h.logger.Info("escalation resolved",
		"escalation_id", escalationID,
		"crew_id", crewID,
		"action", body.Action,
	)

	writeJSON(w, http.StatusOK, map[string]string{
		"id":     escalationID,
		"status": "RESOLVED",
		"action": body.Action,
	})
}

// ListEscalations handles GET /api/v1/crews/{crewId}/escalations.
func (h *QueryHandler) ListEscalations(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	limit, offset := parsePagination(r, 50, 100)

	type escalationItem struct {
		ID                 string  `json:"id"`
		Type               string  `json:"type"`
		FromName           string  `json:"from_name"`
		FromSlug           string  `json:"from_slug"`
		Reason             string  `json:"reason"`
		Context            *string `json:"context"`
		Metadata           *string `json:"metadata"`
		PeerConversationID *string `json:"peer_conversation_id"`
		Status             string  `json:"status"`
		Resolution         *string `json:"resolution"`
		Action             *string `json:"action"`
		RedirectTo         *string `json:"redirect_to"`
		ResolvedBy         *string `json:"resolved_by"`
		ResolvedAt         *string `json:"resolved_at"`
		CreatedAt          string  `json:"created_at"`
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT e.id, e.type, e.reason, e.context, e.metadata, e.peer_conversation_id, e.status,
		       e.resolution, e.action, e.redirect_to, e.resolved_by, e.resolved_at, e.created_at,
		       from_a.name, from_a.slug
		FROM escalations e
		JOIN agents from_a ON from_a.id = e.from_agent_id
		WHERE e.crew_id = ? AND e.workspace_id = ?
		ORDER BY e.created_at DESC
		LIMIT ? OFFSET ?
	`, crewID, workspaceID, limit, offset)
	if err != nil {
		h.logger.Error("list escalations", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	items := make([]escalationItem, 0, limit)
	for rows.Next() {
		var item escalationItem
		if err := rows.Scan(
			&item.ID, &item.Type, &item.Reason, &item.Context, &item.Metadata,
			&item.PeerConversationID, &item.Status, &item.Resolution, &item.Action,
			&item.RedirectTo, &item.ResolvedBy, &item.ResolvedAt, &item.CreatedAt,
			&item.FromName, &item.FromSlug,
		); err != nil {
			h.logger.Error("scan escalation", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		// Never expose plaintext credential values to the list response
		if item.Type == "CREDENTIAL" && item.Resolution != nil {
			masked := "[credential submitted]"
			item.Resolution = &masked
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusOK, items)
}
