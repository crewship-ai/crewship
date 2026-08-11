package api

// Workspace write paths — Create + Update. Each enforces tenant
// uniqueness and validates the language preference. Extracted from
// workspaces.go for readability.

import (
	"database/sql"
	"net/http"
	"time"
)

type createWorkspaceRequest struct {
	Name              string  `json:"name"`
	Slug              string  `json:"slug"`
	PreferredLanguage *string `json:"preferred_language"`
}

// Create provisions a new workspace and adds the current user as OWNER.
// POST /api/v1/workspaces

func (h *WorkspaceHandler) Create(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var req createWorkspaceRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	if req.Name == "" || len(req.Name) < 2 || len(req.Name) > 100 {
		replyError(w, http.StatusBadRequest, "name must be 2-100 characters")
		return
	}
	if req.Slug == "" || len(req.Slug) < 2 || len(req.Slug) > 50 {
		replyError(w, http.StatusBadRequest, "slug must be 2-50 characters")
		return
	}
	if req.PreferredLanguage != nil && *req.PreferredLanguage != "" {
		resolved, err := resolveLanguage(*req.PreferredLanguage)
		if err != nil {
			replyError(w, http.StatusBadRequest, err.Error())
			return
		}
		req.PreferredLanguage = &resolved
	}

	var existingID string
	err := h.db.QueryRowContext(r.Context(), "SELECT id FROM workspaces WHERE slug = ?", req.Slug).Scan(&existingID)
	if err == nil {
		replyError(w, http.StatusConflict, "Workspace slug already taken")
		return
	}
	if err != sql.ErrNoRows {
		replyInternalError(w, h.logger, "check workspace slug", err)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	wsID := generateCUID()
	memberID := generateCUID()

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin tx", err)
		return
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO workspaces (id, name, slug, preferred_language, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		wsID, req.Name, req.Slug, req.PreferredLanguage, now, now)
	if err != nil {
		replyInternalError(w, h.logger, "insert workspace", err)
		return
	}

	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at, updated_at) VALUES (?, ?, ?, 'OWNER', ?, ?)",
		memberID, wsID, user.ID, now, now)
	if err != nil {
		replyInternalError(w, h.logger, "insert workspace member", err)
		return
	}

	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit tx", err)
		return
	}

	writeJSON(w, http.StatusCreated, workspaceResponse{
		ID:                wsID,
		Name:              req.Name,
		Slug:              req.Slug,
		PreferredLanguage: req.PreferredLanguage,
		CreatedAt:         now,
		UpdatedAt:         now,
	})
}

// Get returns a single workspace by ID with crew, agent, and member counts.
// GET /api/v1/workspaces/{workspaceId}

type updateWorkspaceRequest struct {
	Name              *string `json:"name"`
	Slug              *string `json:"slug"`
	PreferredLanguage *string `json:"preferred_language"`
	// AllowPrivilegedCredentials (#1032) — see workspaceResponse doc comment.
	AllowPrivilegedCredentials *bool `json:"allow_privileged_credentials"`
	// RunRetentionDays (#1407) — override for the pipeline_runs retention
	// sweep window. nil leaves the column untouched; 0 is rejected (use
	// the sweep's own NULL-means-default behavior by not setting this
	// field, rather than a magic 0 that could be mistaken for "keep
	// forever" — see pipeline.DefaultRunRetentionDays).
	RunRetentionDays *int `json:"run_retention_days"`
	// CredentialAuditRetentionDays / AuditLogRetentionDays (#1887) — windows
	// for the two audit sweeps. nil leaves the column untouched.
	//
	// Unlike RunRetentionDays above, 0 IS accepted here and means an explicit
	// "keep forever". These are audit tables: `credential_audit` prunes at 90
	// days by default and an operator must be able to switch that off, and
	// `audit_logs` is unlimited by default precisely because the retention
	// obligation is theirs. Rejecting 0 would leave both intents
	// inexpressible through the supported surface. See
	// internal/api/audit_retention.go.
	CredentialAuditRetentionDays *int `json:"credential_audit_retention_days"`
	AuditLogRetentionDays        *int `json:"audit_log_retention_days"`
}

// Update modifies workspace settings such as name, slug, logo, and preferred language.
// PATCH /api/v1/workspaces/{workspaceId}

func (h *WorkspaceHandler) Update(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	var req updateWorkspaceRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	if req.Name != nil && (len(*req.Name) < 2 || len(*req.Name) > 100) {
		replyError(w, http.StatusBadRequest, "name must be 2-100 characters")
		return
	}
	if req.Slug != nil && (len(*req.Slug) < 2 || len(*req.Slug) > 50) {
		replyError(w, http.StatusBadRequest, "slug must be 2-50 characters")
		return
	}
	if req.RunRetentionDays != nil && *req.RunRetentionDays <= 0 {
		replyError(w, http.StatusBadRequest, "run_retention_days must be a positive number of days")
		return
	}
	// 0 is legal for both audit windows — it is the explicit "keep forever".
	// Negative is not: it is not a shorter window or a longer one, it is a
	// typo, and silently coercing it would leave the operator believing they
	// had set something.
	if req.CredentialAuditRetentionDays != nil && *req.CredentialAuditRetentionDays < 0 {
		replyError(w, http.StatusBadRequest, "credential_audit_retention_days must be 0 (keep forever) or a positive number of days")
		return
	}
	if req.AuditLogRetentionDays != nil && *req.AuditLogRetentionDays < 0 {
		replyError(w, http.StatusBadRequest, "audit_log_retention_days must be 0 (keep forever) or a positive number of days")
		return
	}

	if req.PreferredLanguage != nil && *req.PreferredLanguage != "" {
		resolved, err := resolveLanguage(*req.PreferredLanguage)
		if err != nil {
			replyError(w, http.StatusBadRequest, err.Error())
			return
		}
		req.PreferredLanguage = &resolved
	}

	if req.Slug != nil {
		var existingID string
		err := h.db.QueryRowContext(r.Context(),
			"SELECT id FROM workspaces WHERE slug = ? AND id != ?", *req.Slug, workspaceID).Scan(&existingID)
		if err == nil {
			replyError(w, http.StatusConflict, "Workspace slug already taken")
			return
		}
		if err != sql.ErrNoRows {
			replyInternalError(w, h.logger, "check workspace slug", err)
			return
		}
	}

	ub := newUpdate()
	if req.Name != nil {
		ub.Set("name", *req.Name)
	}
	if req.Slug != nil {
		ub.Set("slug", *req.Slug)
	}
	if req.PreferredLanguage != nil {
		if *req.PreferredLanguage == "" {
			ub.SetNull("preferred_language")
		} else {
			ub.Set("preferred_language", *req.PreferredLanguage)
		}
	}
	if req.AllowPrivilegedCredentials != nil {
		val := 0
		if *req.AllowPrivilegedCredentials {
			val = 1
			// #1032: an operator just accepted that a privileged crew's
			// sidecar CredStore is reachable from any agent in that
			// container (the UID 1001/1002 boundary is gone under
			// --privileged) — worth a durable trail, not just a 200 OK.
			h.logger.Warn("workspace opted in to loading credentials into privileged crews' sidecars (#1032)",
				"workspace_id", workspaceID)
		}
		ub.Set("allow_privileged_credentials", val)
	}
	if req.RunRetentionDays != nil {
		ub.Set("run_retention_days", *req.RunRetentionDays)
	}
	if req.CredentialAuditRetentionDays != nil {
		ub.Set("credential_audit_retention_days", *req.CredentialAuditRetentionDays)
	}
	if req.AuditLogRetentionDays != nil {
		// Worth a durable line: switching audit_logs from "keep everything"
		// to a finite window is a decision about compliance data, and the
		// operator who has to answer for it later should be able to find when
		// it was made.
		if *req.AuditLogRetentionDays > 0 {
			h.logger.Warn("workspace set a finite retention window on audit_logs (#1887)",
				"workspace_id", workspaceID, "days", *req.AuditLogRetentionDays)
		}
		ub.Set("audit_log_retention_days", *req.AuditLogRetentionDays)
	}
	persisted := !ub.Empty()
	if persisted {
		query, args := ub.Build("workspaces", "id = ?", workspaceID)
		if _, err := h.db.ExecContext(r.Context(), query, args...); err != nil {
			replyInternalError(w, h.logger, "update workspace", err)
			return
		}
	}

	var ws workspaceResponse
	err := h.db.QueryRowContext(r.Context(), `
		SELECT w.id, w.name, w.slug, w.logo_url, w.preferred_language, w.created_at, w.updated_at,
			w.allow_privileged_credentials, w.run_retention_days,
			w.credential_audit_retention_days, w.audit_log_retention_days,
			(SELECT COUNT(*) FROM crews WHERE workspace_id = w.id AND deleted_at IS NULL) AS crew_count,
			(SELECT COUNT(*) FROM agents WHERE workspace_id = w.id AND deleted_at IS NULL) AS agent_count,
			(SELECT COUNT(*) FROM workspace_members WHERE workspace_id = w.id) AS member_count
		FROM workspaces w
		WHERE w.id = ? AND w.deleted_at IS NULL
	`, workspaceID).Scan(&ws.ID, &ws.Name, &ws.Slug, &ws.LogoURL, &ws.PreferredLanguage,
		&ws.CreatedAt, &ws.UpdatedAt, &ws.AllowPrivilegedCredentials, &ws.RunRetentionDays,
		&ws.CredentialAuditRetentionDays, &ws.AuditLogRetentionDays,
		&ws.CrewCount, &ws.AgentCount, &ws.MemberCount)
	if err != nil {
		replyInternalError(w, h.logger, "get workspace after update", err)
		return
	}
	ws.fillNestedCount()

	// Which settings moved. allow_privileged_credentials gets called out by
	// name because it is the one that removes the fail-closed boundary between
	// privileged crews and stored secrets — if a single row in this log ever
	// matters, it is that one.
	changed := make([]string, 0, 4)
	if req.Name != nil {
		changed = append(changed, "name")
	}
	if req.Slug != nil {
		changed = append(changed, "slug")
	}
	if req.PreferredLanguage != nil {
		changed = append(changed, "preferred_language")
	}
	if req.RunRetentionDays != nil {
		changed = append(changed, "run_retention_days")
	}
	meta := map[string]interface{}{"fields": changed}
	if req.AllowPrivilegedCredentials != nil {
		changed = append(changed, "allow_privileged_credentials")
		meta["fields"] = changed
		meta["allow_privileged_credentials"] = *req.AllowPrivilegedCredentials
	}
	// Only a PATCH that actually persisted something is an event. A `{}`
	// body (or one carrying nothing but ignored fields) skips the update
	// above, and recording it anyway fills the trail with settings changes
	// that never happened — which is exactly the trail an operator later
	// reads to find out when a setting moved.
	if persisted {
		auditFromRequest(r, h.db, "workspace.update", "WORKSPACE", workspaceID, meta)
	}

	writeJSON(w, http.StatusOK, ws)
}
