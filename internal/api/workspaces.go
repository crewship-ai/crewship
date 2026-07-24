package api

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/license"
	wshub "github.com/crewship-ai/crewship/internal/ws"
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
	hub     *wshub.Hub
}

// NewWorkspaceHandler creates a WorkspaceHandler with the given database and logger.

func NewWorkspaceHandler(db *sql.DB, logger *slog.Logger) *WorkspaceHandler {
	return &WorkspaceHandler{db: db, logger: logger}
}

// SetLicense attaches the license for enforcing workspace member limits.
func (h *WorkspaceHandler) SetLicense(lic *license.License) { h.license = lic }

// SetHub attaches the WebSocket hub so mutations (currently workspace
// deletion) broadcast realtime events to connected clients.
func (h *WorkspaceHandler) SetHub(hub *wshub.Hub) { h.hub = hub }

// workspaceCounts is the nested `_count` object the settings UI reads
// (settings-layout.tsx: org._count.{crews,agents,members}). Always
// emitted — the FE relies on it for the General-tab usage numbers.
type workspaceCounts struct {
	Crews   int `json:"crews"`
	Agents  int `json:"agents"`
	Members int `json:"members"`
}

type workspaceResponse struct {
	ID                string  `json:"id"`
	Name              string  `json:"name"`
	Slug              string  `json:"slug"`
	LogoURL           *string `json:"logo_url"`
	PreferredLanguage *string `json:"preferred_language"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
	CurrentUserRole   *string `json:"currentUserRole,omitempty"`
	// CurrentUserCapabilities (#1034) — the caller's resolved per-membership
	// capability grants (v109), sorted. The frontend ability layer reads
	// these so UI can gate on capability (e.g. show Rotate for a MANAGER
	// holding credential.rotate) instead of role alone. Resolution matches
	// the runtime gate exactly (resolveCapabilitiesFromRow), so the UI can
	// never promise an action the backend would 403.
	CurrentUserCapabilities []string `json:"currentUserCapabilities,omitempty"`
	// AllowPrivilegedCredentials (#1032) — explicit opt-in to load
	// credentials into a --privileged crew's sidecar CredStore despite the
	// collapsed UID 1001/1002 isolation boundary. false (default): the
	// agent-config resolver fails closed and omits credentials for a
	// privileged crew.
	AllowPrivilegedCredentials bool `json:"allow_privileged_credentials"`
	// RunRetentionDays (#1407) is the per-workspace override for the
	// pipeline_runs retention sweep window in days. nil means "use
	// pipeline.DefaultRunRetentionDays (90)".
	RunRetentionDays *int `json:"run_retention_days"`
	// Nested `_count` is the canonical shape the frontend consumes
	// (#866.1). The flat `_count_*` keys are retained one release for
	// back-compat with any older client and should be removed after.
	Count       *workspaceCounts `json:"_count,omitempty"`
	CrewCount   int              `json:"_count_crews,omitempty"`
	AgentCount  int              `json:"_count_agents,omitempty"`
	MemberCount int              `json:"_count_members,omitempty"`
}

// fillNestedCount mirrors the flat scan targets into the nested `_count`
// object so both shapes stay in lockstep no matter which query path
// populated the row.
func (ws *workspaceResponse) fillNestedCount() {
	ws.Count = &workspaceCounts{
		Crews:   ws.CrewCount,
		Agents:  ws.AgentCount,
		Members: ws.MemberCount,
	}
}

// List returns all workspaces the authenticated user belongs to.
// GET /api/v1/workspaces

func (h *WorkspaceHandler) List(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT w.id, w.name, w.slug, w.logo_url, w.preferred_language, w.created_at, w.updated_at,
			wm.role, wm.capabilities, w.allow_privileged_credentials,
			(SELECT COUNT(*) FROM crews WHERE workspace_id = w.id AND deleted_at IS NULL) AS crew_count,
			(SELECT COUNT(*) FROM agents WHERE workspace_id = w.id AND deleted_at IS NULL) AS agent_count,
			(SELECT COUNT(*) FROM workspace_members WHERE workspace_id = w.id) AS member_count
		FROM workspaces w
		JOIN workspace_members wm ON wm.workspace_id = w.id AND wm.user_id = ?
		WHERE w.deleted_at IS NULL
		ORDER BY w.created_at DESC
	`, user.ID)
	if err != nil {
		replyInternalError(w, h.logger, "list workspaces", err)
		return
	}
	defer rows.Close()

	var result []workspaceResponse
	for rows.Next() {
		var ws workspaceResponse
		var capsJSON sql.NullString
		if err := rows.Scan(&ws.ID, &ws.Name, &ws.Slug, &ws.LogoURL, &ws.PreferredLanguage,
			&ws.CreatedAt, &ws.UpdatedAt, &ws.CurrentUserRole, &capsJSON, &ws.AllowPrivilegedCredentials,
			&ws.CrewCount, &ws.AgentCount, &ws.MemberCount); err != nil {
			replyInternalError(w, h.logger, "scan workspace", err)
			return
		}
		role := ""
		if ws.CurrentUserRole != nil {
			role = *ws.CurrentUserRole
		}
		ws.CurrentUserCapabilities = capsAsSortedSlice(resolveCapabilitiesFromRow(capsJSON, role))
		ws.fillNestedCount()
		result = append(result, ws)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "rows iteration (workspaces)", err)
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
			w.allow_privileged_credentials,
			(SELECT COUNT(*) FROM crews WHERE workspace_id = w.id AND deleted_at IS NULL) AS crew_count,
			(SELECT COUNT(*) FROM agents WHERE workspace_id = w.id AND deleted_at IS NULL) AS agent_count,
			(SELECT COUNT(*) FROM workspace_members WHERE workspace_id = w.id) AS member_count
		FROM workspaces w
		WHERE w.id = ? AND w.deleted_at IS NULL
	`, workspaceID).Scan(&ws.ID, &ws.Name, &ws.Slug, &ws.LogoURL, &ws.PreferredLanguage,
		&ws.CreatedAt, &ws.UpdatedAt, &ws.AllowPrivilegedCredentials,
		&ws.CrewCount, &ws.AgentCount, &ws.MemberCount)
	if err != nil {
		if err == sql.ErrNoRows {
			replyError(w, http.StatusNotFound, "Workspace not found")
			return
		}
		replyInternalError(w, h.logger, "get workspace", err)
		return
	}
	ws.CurrentUserRole = &role
	// Same capability surface as List — cached lookup (30 s TTL) keeps this
	// off the hot path. A missing membership row (shouldn't happen behind
	// RequireWorkspace) simply omits the field rather than failing the read.
	if user := UserFromContext(r.Context()); user != nil {
		if caps, _, ok := CapabilitiesForMember(r.Context(), h.db, workspaceID, user.ID); ok {
			ws.CurrentUserCapabilities = capsAsSortedSlice(caps)
		}
	}
	ws.fillNestedCount()

	writeJSON(w, http.StatusOK, ws)
}
