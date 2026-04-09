package api

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/license"
)

// validLanguages maps language name → true for validation.
// Must stay in sync with lib/languages.ts on the frontend.
var validLanguages = map[string]bool{
	"Afrikaans": true, "Arabic": true, "Bulgarian": true, "Bengali": true,
	"Catalan": true, "Czech": true, "Danish": true, "German": true,
	"Greek": true, "English": true, "Spanish": true, "Estonian": true,
	"Persian": true, "Finnish": true, "French": true, "Hebrew": true,
	"Hindi": true, "Croatian": true, "Hungarian": true, "Indonesian": true,
	"Italian": true, "Japanese": true, "Korean": true, "Lithuanian": true,
	"Latvian": true, "Malay": true, "Norwegian": true, "Dutch": true,
	"Polish": true, "Portuguese": true, "Portuguese (Brazil)": true,
	"Romanian": true, "Russian": true, "Slovak": true, "Slovenian": true,
	"Serbian": true, "Swedish": true, "Swahili": true, "Tamil": true,
	"Thai": true, "Turkish": true, "Ukrainian": true, "Urdu": true,
	"Vietnamese": true, "Chinese": true, "Chinese (Traditional)": true,
}

// languageCodeToName maps ISO codes to language names for CLI convenience.
var languageCodeToName = map[string]string{
	"af": "Afrikaans", "ar": "Arabic", "bg": "Bulgarian", "bn": "Bengali",
	"ca": "Catalan", "cs": "Czech", "da": "Danish", "de": "German",
	"el": "Greek", "en": "English", "es": "Spanish", "et": "Estonian",
	"fa": "Persian", "fi": "Finnish", "fr": "French", "he": "Hebrew",
	"hi": "Hindi", "hr": "Croatian", "hu": "Hungarian", "id": "Indonesian",
	"it": "Italian", "ja": "Japanese", "ko": "Korean", "lt": "Lithuanian",
	"lv": "Latvian", "ms": "Malay", "nb": "Norwegian", "nl": "Dutch",
	"pl": "Polish", "pt": "Portuguese", "pt-BR": "Portuguese (Brazil)",
	"ro": "Romanian", "ru": "Russian", "sk": "Slovak", "sl": "Slovenian",
	"sr": "Serbian", "sv": "Swedish", "sw": "Swahili", "ta": "Tamil",
	"th": "Thai", "tr": "Turkish", "uk": "Ukrainian", "ur": "Urdu",
	"vi": "Vietnamese", "zh": "Chinese", "zh-TW": "Chinese (Traditional)",
}

// resolveLanguage validates a language value. Accepts either a name ("Czech")
// or an ISO code ("cs") and returns the canonical name, or an error.
func resolveLanguage(val string) (string, error) {
	if validLanguages[val] {
		return val, nil
	}
	if name, ok := languageCodeToName[val]; ok {
		return name, nil
	}
	return "", fmt.Errorf("invalid language %q — use a name (e.g. Czech) or ISO code (e.g. cs)", val)
}

type WorkspaceHandler struct {
	db      *sql.DB
	logger  *slog.Logger
	license *license.License
}

func NewWorkspaceHandler(db *sql.DB, logger *slog.Logger) *WorkspaceHandler {
	return &WorkspaceHandler{db: db, logger: logger}
}

func (h *WorkspaceHandler) SetLicense(lic *license.License) { h.license = lic }

type workspaceResponse struct {
	ID                string  `json:"id"`
	Name              string  `json:"name"`
	Slug              string  `json:"slug"`
	LogoURL           *string `json:"logo_url"`
	PreferredLanguage *string `json:"preferred_language"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
	CurrentUserRole   *string `json:"currentUserRole,omitempty"`
	CrewCount         int     `json:"_count_crews,omitempty"`
	AgentCount        int     `json:"_count_agents,omitempty"`
	MemberCount       int     `json:"_count_members,omitempty"`
}

func (h *WorkspaceHandler) List(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT w.id, w.name, w.slug, w.logo_url, w.preferred_language, w.created_at, w.updated_at,
			wm.role,
			(SELECT COUNT(*) FROM crews WHERE workspace_id = w.id AND deleted_at IS NULL) AS crew_count,
			(SELECT COUNT(*) FROM agents WHERE workspace_id = w.id AND deleted_at IS NULL) AS agent_count,
			(SELECT COUNT(*) FROM workspace_members WHERE workspace_id = w.id) AS member_count
		FROM workspaces w
		JOIN workspace_members wm ON wm.workspace_id = w.id AND wm.user_id = ?
		WHERE w.deleted_at IS NULL
		ORDER BY w.created_at DESC
	`, user.ID)
	if err != nil {
		h.logger.Error("list workspaces", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var result []workspaceResponse
	for rows.Next() {
		var ws workspaceResponse
		if err := rows.Scan(&ws.ID, &ws.Name, &ws.Slug, &ws.LogoURL, &ws.PreferredLanguage,
			&ws.CreatedAt, &ws.UpdatedAt, &ws.CurrentUserRole,
			&ws.CrewCount, &ws.AgentCount, &ws.MemberCount); err != nil {
			h.logger.Error("scan workspace", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		result = append(result, ws)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (workspaces)", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if result == nil {
		result = []workspaceResponse{}
	}

	writeJSON(w, http.StatusOK, result)
}

type createWorkspaceRequest struct {
	Name              string  `json:"name"`
	Slug              string  `json:"slug"`
	PreferredLanguage *string `json:"preferred_language"`
}

func (h *WorkspaceHandler) Create(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}

	var req createWorkspaceRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	if req.Name == "" || len(req.Name) < 2 || len(req.Name) > 100 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name must be 2-100 characters"})
		return
	}
	if req.Slug == "" || len(req.Slug) < 2 || len(req.Slug) > 50 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slug must be 2-50 characters"})
		return
	}
	if req.PreferredLanguage != nil && *req.PreferredLanguage != "" {
		resolved, err := resolveLanguage(*req.PreferredLanguage)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		req.PreferredLanguage = &resolved
	}

	var existingID string
	err := h.db.QueryRowContext(r.Context(), "SELECT id FROM workspaces WHERE slug = ?", req.Slug).Scan(&existingID)
	if err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Workspace slug already taken"})
		return
	}
	if err != sql.ErrNoRows {
		h.logger.Error("check workspace slug", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	wsID := generateCUID()
	memberID := generateCUID()

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("begin tx", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO workspaces (id, name, slug, preferred_language, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		wsID, req.Name, req.Slug, req.PreferredLanguage, now, now)
	if err != nil {
		h.logger.Error("insert workspace", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at, updated_at) VALUES (?, ?, ?, 'OWNER', ?, ?)",
		memberID, wsID, user.ID, now, now)
	if err != nil {
		h.logger.Error("insert workspace member", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("commit tx", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
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

func (h *WorkspaceHandler) Get(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	var ws workspaceResponse
	err := h.db.QueryRowContext(r.Context(), `
		SELECT w.id, w.name, w.slug, w.logo_url, w.preferred_language, w.created_at, w.updated_at,
			(SELECT COUNT(*) FROM crews WHERE workspace_id = w.id AND deleted_at IS NULL) AS crew_count,
			(SELECT COUNT(*) FROM agents WHERE workspace_id = w.id AND deleted_at IS NULL) AS agent_count,
			(SELECT COUNT(*) FROM workspace_members WHERE workspace_id = w.id) AS member_count
		FROM workspaces w
		WHERE w.id = ? AND w.deleted_at IS NULL
	`, workspaceID).Scan(&ws.ID, &ws.Name, &ws.Slug, &ws.LogoURL, &ws.PreferredLanguage,
		&ws.CreatedAt, &ws.UpdatedAt, &ws.CrewCount, &ws.AgentCount, &ws.MemberCount)
	if err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Workspace not found"})
			return
		}
		h.logger.Error("get workspace", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	ws.CurrentUserRole = &role

	writeJSON(w, http.StatusOK, ws)
}

type updateWorkspaceRequest struct {
	Name              *string `json:"name"`
	Slug              *string `json:"slug"`
	PreferredLanguage *string `json:"preferred_language"`
}

func (h *WorkspaceHandler) Update(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	var req updateWorkspaceRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	if req.Name != nil && (len(*req.Name) < 2 || len(*req.Name) > 100) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name must be 2-100 characters"})
		return
	}
	if req.Slug != nil && (len(*req.Slug) < 2 || len(*req.Slug) > 50) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slug must be 2-50 characters"})
		return
	}

	if req.PreferredLanguage != nil && *req.PreferredLanguage != "" {
		resolved, err := resolveLanguage(*req.PreferredLanguage)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		req.PreferredLanguage = &resolved
	}

	if req.Slug != nil {
		var existingID string
		err := h.db.QueryRowContext(r.Context(),
			"SELECT id FROM workspaces WHERE slug = ? AND id != ?", *req.Slug, workspaceID).Scan(&existingID)
		if err == nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "Workspace slug already taken"})
			return
		}
		if err != sql.ErrNoRows {
			h.logger.Error("check workspace slug", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
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
	if !ub.Empty() {
		query, args := ub.Build("workspaces", "id = ?", workspaceID)
		if _, err := h.db.ExecContext(r.Context(), query, args...); err != nil {
			h.logger.Error("update workspace", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
	}

	var ws workspaceResponse
	err := h.db.QueryRowContext(r.Context(), `
		SELECT w.id, w.name, w.slug, w.logo_url, w.preferred_language, w.created_at, w.updated_at,
			(SELECT COUNT(*) FROM crews WHERE workspace_id = w.id AND deleted_at IS NULL) AS crew_count,
			(SELECT COUNT(*) FROM agents WHERE workspace_id = w.id AND deleted_at IS NULL) AS agent_count,
			(SELECT COUNT(*) FROM workspace_members WHERE workspace_id = w.id) AS member_count
		FROM workspaces w
		WHERE w.id = ? AND w.deleted_at IS NULL
	`, workspaceID).Scan(&ws.ID, &ws.Name, &ws.Slug, &ws.LogoURL, &ws.PreferredLanguage,
		&ws.CreatedAt, &ws.UpdatedAt, &ws.CrewCount, &ws.AgentCount, &ws.MemberCount)
	if err != nil {
		h.logger.Error("get workspace after update", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusOK, ws)
}

type memberResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	UserID      string  `json:"user_id"`
	Role        string  `json:"role"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	User        *memberUser `json:"user,omitempty"`
}

type memberUser struct {
	ID        string  `json:"id"`
	Email     string  `json:"email"`
	FullName  *string `json:"full_name"`
	AvatarURL *string `json:"avatar_url"`
}

func (h *WorkspaceHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT wm.id, wm.workspace_id, wm.user_id, wm.role, wm.created_at, wm.updated_at,
			u.id, u.email, u.full_name, u.avatar_url
		FROM workspace_members wm
		JOIN users u ON u.id = wm.user_id
		WHERE wm.workspace_id = ?
		ORDER BY wm.created_at ASC
	`, workspaceID)
	if err != nil {
		h.logger.Error("list members", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var result []memberResponse
	for rows.Next() {
		var m memberResponse
		var u memberUser
		if err := rows.Scan(&m.ID, &m.WorkspaceID, &m.UserID, &m.Role, &m.CreatedAt, &m.UpdatedAt,
			&u.ID, &u.Email, &u.FullName, &u.AvatarURL); err != nil {
			h.logger.Error("scan member", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		m.User = &u
		result = append(result, m)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (members)", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if result == nil {
		result = []memberResponse{}
	}

	writeJSON(w, http.StatusOK, result)
}

type addMemberRequest struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

func (h *WorkspaceHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	if h.license != nil {
		if err := h.license.CheckMemberLimit(r.Context(), h.db, workspaceID); err != nil {
			if license.IsLimitError(err) {
				writeJSON(w, http.StatusPaymentRequired, map[string]string{"error": err.Error()})
				return
			}
			h.logger.Error("check member limit", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
	}

	var req addMemberRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	if req.UserID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	if req.Role == "" {
		req.Role = "MEMBER"
	}

	// V-02: Validate role against whitelist and prevent escalation
	validAssignableRoles := map[string]bool{"ADMIN": true, "MANAGER": true, "MEMBER": true, "VIEWER": true}
	if !validAssignableRoles[req.Role] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "role must be ADMIN, MANAGER, MEMBER, or VIEWER"})
		return
	}
	// Only OWNER can assign ADMIN role
	if req.Role == "ADMIN" && role != "OWNER" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Only workspace owner can assign ADMIN role"})
		return
	}

	var existingID string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM workspace_members WHERE workspace_id = ? AND user_id = ?",
		workspaceID, req.UserID).Scan(&existingID)
	if err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "User is already a member of this workspace"})
		return
	}
	if err != sql.ErrNoRows {
		h.logger.Error("check existing member", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	var userExists bool
	err = h.db.QueryRowContext(r.Context(), "SELECT 1 FROM users WHERE id = ?", req.UserID).Scan(&userExists)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "User not found"})
		return
	}
	if err != nil {
		h.logger.Error("check user exists", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	memberID := generateCUID()

	_, err = h.db.ExecContext(r.Context(),
		"INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		memberID, workspaceID, req.UserID, req.Role, now, now)
	if err != nil {
		h.logger.Error("insert member", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusCreated, memberResponse{
		ID:          memberID,
		WorkspaceID: workspaceID,
		UserID:      req.UserID,
		Role:        req.Role,
		CreatedAt:   now,
		UpdatedAt:   now,
	})
}

func (h *WorkspaceHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	memberID := r.PathValue("memberId")

	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	if memberID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "memberId is required"})
		return
	}

	var memberRole string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT role FROM workspace_members WHERE id = ? AND workspace_id = ?",
		memberID, workspaceID).Scan(&memberRole)
	if err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Member not found"})
			return
		}
		h.logger.Error("get member", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if memberRole == "OWNER" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Cannot remove workspace owner"})
		return
	}

	_, err = h.db.ExecContext(r.Context(),
		"DELETE FROM workspace_members WHERE id = ? AND workspace_id = ?",
		memberID, workspaceID)
	if err != nil {
		h.logger.Error("delete member", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

type invitationResponse struct {
	ID          string       `json:"id"`
	WorkspaceID string       `json:"workspace_id"`
	Email       string       `json:"email"`
	Role        string       `json:"role"`
	InvitedBy   string       `json:"invited_by"`
	Token       string       `json:"token"`
	ExpiresAt   string       `json:"expires_at"`
	AcceptedAt  *string      `json:"accepted_at"`
	CreatedAt   string       `json:"created_at"`
	Inviter     *inviterUser `json:"inviter,omitempty"`
}

type inviterUser struct {
	ID       string  `json:"id"`
	Email    string  `json:"email"`
	FullName *string `json:"full_name"`
}

func (h *WorkspaceHandler) ListInvitations(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT wi.id, wi.workspace_id, wi.email, wi.role, wi.invited_by, wi.token,
			wi.expires_at, wi.accepted_at, wi.created_at,
			u.id, u.email, u.full_name
		FROM workspace_invitations wi
		JOIN users u ON u.id = wi.invited_by
		WHERE wi.workspace_id = ? AND wi.accepted_at IS NULL
		ORDER BY wi.created_at DESC
	`, workspaceID)
	if err != nil {
		h.logger.Error("list invitations", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var result []invitationResponse
	for rows.Next() {
		var inv invitationResponse
		var inviter inviterUser
		if err := rows.Scan(&inv.ID, &inv.WorkspaceID, &inv.Email, &inv.Role,
			&inv.InvitedBy, &inv.Token, &inv.ExpiresAt, &inv.AcceptedAt, &inv.CreatedAt,
			&inviter.ID, &inviter.Email, &inviter.FullName); err != nil {
			h.logger.Error("scan invitation", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		inv.Inviter = &inviter
		result = append(result, inv)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (invitations)", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if result == nil {
		result = []invitationResponse{}
	}

	writeJSON(w, http.StatusOK, result)
}

type createInvitationRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generateToken: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func (h *WorkspaceHandler) CreateInvitation(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	user := UserFromContext(r.Context())

	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	if h.license != nil {
		if err := h.license.CheckMemberLimit(r.Context(), h.db, workspaceID); err != nil {
			if license.IsLimitError(err) {
				writeJSON(w, http.StatusPaymentRequired, map[string]string{"error": err.Error()})
				return
			}
			h.logger.Error("check member limit for invitation", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
	}

	var req createInvitationRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	if req.Email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email is required"})
		return
	}
	if req.Role == "" {
		req.Role = "MEMBER"
	}

	// V-03: Validate role against whitelist and prevent escalation
	validInviteRoles := map[string]bool{"ADMIN": true, "MANAGER": true, "MEMBER": true, "VIEWER": true}
	if !validInviteRoles[req.Role] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "role must be ADMIN, MANAGER, MEMBER, or VIEWER"})
		return
	}
	if req.Role == "ADMIN" && role != "OWNER" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Only workspace owner can invite with ADMIN role"})
		return
	}

	var existingMemberID string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT wm.id FROM workspace_members wm
		JOIN users u ON u.id = wm.user_id
		WHERE wm.workspace_id = ? AND u.email = ?
	`, workspaceID, req.Email).Scan(&existingMemberID)
	if err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "User is already a member of this workspace"})
		return
	}
	if err != sql.ErrNoRows {
		h.logger.Error("check existing member by email", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	var existingInviteID string
	err = h.db.QueryRowContext(r.Context(), `
		SELECT id FROM workspace_invitations
		WHERE workspace_id = ? AND email = ? AND accepted_at IS NULL AND expires_at > datetime('now')
	`, workspaceID, req.Email).Scan(&existingInviteID)
	if err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "An active invitation already exists for this email"})
		return
	}
	if err != sql.ErrNoRows {
		h.logger.Error("check existing invitation", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	now := time.Now().UTC()
	expiresAt := now.Add(7 * 24 * time.Hour)
	invID := generateCUID()
	token, err := generateToken()
	if err != nil {
		h.logger.Error("generate invitation token", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO workspace_invitations (id, workspace_id, email, role, invited_by, token, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		invID, workspaceID, req.Email, req.Role, user.ID, token,
		expiresAt.Format(time.RFC3339), now.Format(time.RFC3339))
	if err != nil {
		h.logger.Error("insert invitation", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusCreated, invitationResponse{
		ID:          invID,
		WorkspaceID: workspaceID,
		Email:       req.Email,
		Role:        req.Role,
		InvitedBy:   user.ID,
		Token:       token,
		ExpiresAt:   expiresAt.Format(time.RFC3339),
		CreatedAt:   now.Format(time.RFC3339),
		Inviter: &inviterUser{
			ID:       user.ID,
			Email:    user.Email,
			FullName: &user.Name,
		},
	})
}
