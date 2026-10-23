package api

// Workspace write paths — Create + Update. Each enforces tenant
// uniqueness and validates the language preference. Extracted from
// workspaces.go for readability.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
	"github.com/crewship-ai/crewship/internal/retention"
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

	// Creating one's own workspace keeps its existing route contract, but
	// stale authenticated context cannot bypass current account restrictions.
	var activeTrusted bool
	if err := tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM users u WHERE u.id=? AND u.suspended_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM workspace_members m WHERE m.user_id=u.id AND m.access_mode='restricted'))`, user.ID).Scan(&activeTrusted); err != nil {
		replyInternalError(w, h.logger, "check workspace creator", err)
		return
	}
	if !activeTrusted {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO workspaces (id, name, slug, preferred_language, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		wsID, req.Name, req.Slug, req.PreferredLanguage, now, now)
	if err != nil {
		replyInternalError(w, h.logger, "insert workspace", err)
		return
	}
	// The new workspace's own copy of the Keeper defaults (a template).
	if err := governance.SeedWorkspace(r.Context(), tx, wsID); err != nil {
		replyInternalError(w, h.logger, "keeper template", err)
		return
	}
	// The retention windows an instance admin last saved for all workspaces.
	if err := retention.ApplyDefaults(r.Context(), tx, wsID); err != nil {
		replyInternalError(w, h.logger, "retention defaults", err)
		return
	}

	memberResult, err := tx.ExecContext(r.Context(),
		"INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at, updated_at) VALUES (?, ?, ?, 'OWNER', ?, ?)",
		memberID, wsID, user.ID, now, now)
	if err != nil {
		replyInternalError(w, h.logger, "insert workspace member", err)
		return
	}

	notify, err := appendWorkspaceOwnerAudit(r.Context(), tx, h.policyJournal, memberResult, wsID, memberID, user.ID, "workspace_create")
	if err != nil {
		replyInternalError(w, h.logger, "audit initial workspace owner", err)
		return
	}

	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit tx", err)
		return
	}

	if notify != nil {
		notify()
	}

	writeJSON(w, http.StatusCreated, workspaceResponse{
		ID:                wsID,
		PagesTheme:        json.RawMessage(`{}`),
		AppearanceDefault: json.RawMessage(`{}`),
		Name:              req.Name,
		Slug:              req.Slug,
		Kind:              "team",
		PreferredLanguage: req.PreferredLanguage,
		CreatedAt:         now,
		UpdatedAt:         now,
	})
}

// Get returns a single workspace by ID with crew, agent, and member counts.
// GET /api/v1/workspaces/{workspaceId}

type updateWorkspaceRequest struct {
	PagesTheme json.RawMessage `json:"pages_theme"`
	// AppearanceDefault (WSD-01) is the workspace default look; `{}` resets it.
	AppearanceDefault json.RawMessage `json:"appearance_default"`
	Name              *string         `json:"name"`
	Slug              *string         `json:"slug"`
	PreferredLanguage *string         `json:"preferred_language"`
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
	// ApprovalsRetentionDays (#2233) — window for the approvals_queue
	// retention sweep. nil leaves the column untouched.
	//
	// Like the audit pair above (and unlike RunRetentionDays), 0 IS
	// accepted and means an explicit "keep forever": this flag sits on the
	// same `workspace update` command as the two audit ones, where 0
	// already means keep-forever, so giving it different semantics here
	// would silently delete what an operator typing 0 out of habit with
	// its neighbours expected to keep. See internal/harbormaster/retention.go.
	ApprovalsRetentionDays *int `json:"approvals_retention_days"`
	// GuestRoutineMaxRunMinutes bounds future Guest-published runs (1-240).
	GuestRoutineMaxRunMinutes *int `json:"guest_routine_max_run_minutes"`
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

	// This flag also authorizes privileged container configurations, whose
	// authority reaches beyond a workspace. A workspace administrator may
	// disable the opt-in, but only an instance administrator may enable it.
	if req.AllowPrivilegedCredentials != nil && *req.AllowPrivilegedCredentials &&
		(!isInstanceAdmin(r, h.db) || !canScope(r.Context(), scopeInstanceAdmin)) {
		replyError(w, http.StatusForbidden, "Only an instance administrator can enable privileged runtime access")
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
	if req.ApprovalsRetentionDays != nil && *req.ApprovalsRetentionDays < 0 {
		replyError(w, http.StatusBadRequest, "approvals_retention_days must be 0 (keep forever) or a positive number of days")
		return
	}
	// Reject before persisting rather than let it reach the sweeper:
	// time.Duration is int64 nanoseconds, so *days*24h overflows past
	// harbormaster.MaxApprovalsRetentionDays (~106751 days, ~292 years)
	// and wraps NEGATIVE — a cutoff in the future that would match every
	// terminal row regardless of age. See retention.go.
	if req.GuestRoutineMaxRunMinutes != nil && (*req.GuestRoutineMaxRunMinutes < 1 || *req.GuestRoutineMaxRunMinutes > 240) {
		replyError(w, http.StatusBadRequest, "guest_routine_max_run_minutes must be between 1 and 240")
		return
	}
	if req.ApprovalsRetentionDays != nil && *req.ApprovalsRetentionDays > harbormaster.MaxApprovalsRetentionDays {
		replyError(w, http.StatusBadRequest, fmt.Sprintf(
			"approvals_retention_days is %d; the maximum is %d", *req.ApprovalsRetentionDays, harbormaster.MaxApprovalsRetentionDays))
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

	if req.PagesTheme != nil {
		if err := validateWorkspacePagesTheme(req.PagesTheme); err != nil {
			replyError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if req.AppearanceDefault != nil {
		if err := validateWorkspaceAppearanceDefault(req.AppearanceDefault); err != nil {
			replyError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	ub := newUpdate()
	if req.AppearanceDefault != nil {
		ub.Set("appearance_default", string(req.AppearanceDefault))
	}
	if req.PagesTheme != nil {
		ub.Set("pages_theme", string(req.PagesTheme))
	}
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
	if req.ApprovalsRetentionDays != nil {
		// Opposite direction from audit_logs above but the same rule: the
		// transition worth a durable line is the one that changes what gets
		// pruned. approvals_queue defaults to a 90-day window, so setting it
		// to 0 is what turns pruning OFF.
		if *req.ApprovalsRetentionDays == 0 {
			h.logger.Warn("workspace set approvals_queue to keep forever (#2233)",
				"workspace_id", workspaceID)
		}
		ub.Set("approvals_retention_days", *req.ApprovalsRetentionDays)
	}
	if req.GuestRoutineMaxRunMinutes != nil {
		ub.Set("guest_routine_max_run_minutes", *req.GuestRoutineMaxRunMinutes)
	}
	persisted := !ub.Empty()
	var tx *sql.Tx
	var expected []any
	var previousPrivileged bool
	var reader interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	} = h.db
	if persisted {
		var err error
		tx, err = h.db.BeginTx(r.Context(), nil)
		if err != nil {
			replyInternalError(w, h.logger, "begin workspace settings", err)
			return
		}
		defer tx.Rollback()
		if !checkWorkspaceSettingsActor(w, r, tx, h.logger, req.AllowPrivilegedCredentials) {
			return
		}
		if req.AllowPrivilegedCredentials != nil {
			if err := tx.QueryRowContext(r.Context(), `SELECT allow_privileged_credentials FROM workspaces WHERE id=? AND deleted_at IS NULL`, workspaceID).Scan(&previousPrivileged); err != nil {
				replyInternalError(w, h.logger, "read previous workspace privilege", err)
				return
			}
		}
		query, args := ub.Build("workspaces", "id = ? AND deleted_at IS NULL", workspaceID)
		result, err := tx.ExecContext(r.Context(), query, args...)
		if err != nil {
			replyInternalError(w, h.logger, "update workspace", err)
			return
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			replyInternalError(w, h.logger, "update workspace", fmt.Errorf("workspace update not applied: %w", err))
			return
		}
		if err := settingsUpdatePersisted(r.Context(), tx, "workspaces", workspaceID, ub); err != nil {
			replyInternalError(w, h.logger, "acknowledge workspace settings", err)
			return
		}
		expected, err = settingsNativeRow(r.Context(), tx, "workspaces", workspaceID)
		if err != nil {
			replyInternalError(w, h.logger, "read workspace settings", err)
			return
		}
		reader = tx
	}

	ws, err := workspaceSettingsResponse(r.Context(), reader, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "get workspace after update", err)
		return
	}

	// Which settings moved. allow_privileged_credentials gets called out by
	// name because it is the one that removes the fail-closed boundary between
	// privileged crews and stored secrets — if a single row in this log ever
	// matters, it is that one.
	changed := make([]string, 0, 4)
	if req.PagesTheme != nil {
		changed = append(changed, "pages_theme")
	}
	if req.AppearanceDefault != nil {
		changed = append(changed, "appearance_default")
	}
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
	// Both retention windows belong in the trail for the same reason the
	// others do, and one of them more than any: turning on deletion of
	// audit_logs is a decision about the compliance record itself. An entry
	// that says "workspace.update" with no field names is what an operator
	// would find months later while asking why the trail has a hole in it.
	if req.CredentialAuditRetentionDays != nil {
		changed = append(changed, "credential_audit_retention_days")
	}
	if req.AuditLogRetentionDays != nil {
		changed = append(changed, "audit_log_retention_days")
	}
	if req.ApprovalsRetentionDays != nil {
		changed = append(changed, "approvals_retention_days")
	}
	if req.GuestRoutineMaxRunMinutes != nil {
		changed = append(changed, "guest_routine_max_run_minutes")
	}
	meta := map[string]interface{}{"fields": changed}
	if req.AllowPrivilegedCredentials != nil {
		changed = append(changed, "allow_privileged_credentials")
		meta["fields"] = changed
		meta["allow_privileged_credentials"] = *req.AllowPrivilegedCredentials
		meta["previous"] = map[string]any{"allow_privileged_credentials": previousPrivileged}
		meta["current"] = map[string]any{"allow_privileged_credentials": *req.AllowPrivilegedCredentials}
	}
	// Only a PATCH that actually persisted something is an event. A `{}`
	// body (or one carrying nothing but ignored fields) skips the update
	// above, and recording it anyway fills the trail with settings changes
	// that never happened — which is exactly the trail an operator later
	// reads to find out when a setting moved.
	if persisted {
		notify, err := appendSettingsAudit(r, tx, h.policyJournal, "workspace.update", "WORKSPACE", workspaceID, meta)
		if err != nil {
			replyInternalError(w, h.logger, "audit workspace settings", err)
			return
		}
		if err := acknowledgeSettingsRow(r.Context(), tx, "workspaces", workspaceID, expected); err != nil {
			replyInternalError(w, h.logger, "acknowledge workspace settings", err)
			return
		}
		if !checkWorkspaceSettingsActor(w, r, tx, h.logger, req.AllowPrivilegedCredentials) {
			return
		}
		if err := tx.Commit(); err != nil {
			replyInternalError(w, h.logger, "commit workspace settings", err)
			return
		}
		if notify != nil {
			notify()
		}
		broadcastWorkspaceEvent(h.hub, workspaceID, "workspace.updated", map[string]string{"workspace_id": workspaceID})
	}

	writeJSON(w, http.StatusOK, ws)
}

// workspaceByID reads the workspace as the detail and update responses show
// it. Update and the logo endpoints (#3005) answer with it.
func (h *WorkspaceHandler) workspaceByID(ctx context.Context, workspaceID string) (workspaceResponse, error) {
	return workspaceSettingsResponse(ctx, h.db, workspaceID)
}

func workspaceSettingsResponse(ctx context.Context, reader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, workspaceID string) (workspaceResponse, error) {
	var ws workspaceResponse
	err := reader.QueryRowContext(ctx, `
		SELECT w.id, w.name, w.slug, w.kind, `+personalOwnerSQL+`, w.logo_url, w.preferred_language, w.created_at, w.updated_at,
			w.allow_privileged_credentials, w.run_retention_days,
			w.credential_audit_retention_days, w.audit_log_retention_days,
			w.approvals_retention_days, w.guest_routine_max_run_minutes, w.pages_theme, w.appearance_default,
			(SELECT COUNT(*) FROM crews WHERE workspace_id = w.id AND deleted_at IS NULL) AS crew_count,
			(SELECT COUNT(*) FROM agents WHERE workspace_id = w.id AND deleted_at IS NULL) AS agent_count,
			(SELECT COUNT(*) FROM workspace_members WHERE workspace_id = w.id) AS member_count
		FROM workspaces w
		WHERE w.id = ? AND w.deleted_at IS NULL
	`, workspaceID).Scan(&ws.ID, &ws.Name, &ws.Slug, &ws.Kind, &ws.PersonalOwnerID, &ws.LogoURL, &ws.PreferredLanguage,
		&ws.CreatedAt, &ws.UpdatedAt, &ws.AllowPrivilegedCredentials, &ws.RunRetentionDays,
		&ws.CredentialAuditRetentionDays, &ws.AuditLogRetentionDays,
		&ws.ApprovalsRetentionDays, &ws.GuestRoutineMaxRunMinutes, &ws.pagesThemeJSON, &ws.appearanceDefaultJSON,
		&ws.CrewCount, &ws.AgentCount, &ws.MemberCount)
	if err != nil {
		return ws, err
	}
	ws.fillNestedCount()
	return ws, nil
}
