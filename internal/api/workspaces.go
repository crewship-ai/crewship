package api

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"

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

// WorkspaceHandler provides CRUD endpoints for workspaces and their membership/invitation management.

type WorkspaceHandler struct {
	db      *sql.DB
	logger  *slog.Logger
	license *license.License
}

// NewWorkspaceHandler creates a WorkspaceHandler with the given database and logger.

func NewWorkspaceHandler(db *sql.DB, logger *slog.Logger) *WorkspaceHandler {
	return &WorkspaceHandler{db: db, logger: logger}
}

// SetLicense attaches the license for enforcing workspace member limits.
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

// List returns all workspaces the authenticated user belongs to.
// GET /api/v1/workspaces

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
