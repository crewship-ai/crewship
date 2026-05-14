package api

import (
	"database/sql"
	"log/slog"
	"net/http"
)

// JournalLookupHandler serves a tiny denormalised reference table the
// frontend uses to enrich journal entry cards (crew chips, agent chips,
// mission breadcrumbs) without needing per-entry JOINs on the timeline
// or stream queries. The dataset is small and bounded by workspace, so
// the frontend can fetch once on mount and cache in a React context.
//
// Read-only; no auth-elevated path. Standard workspace + auth middleware
// gates access via WorkspaceIDFromContext.
type JournalLookupHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewJournalLookupHandler builds the handler. Logger is used only for
// internal errors; transport-level failures still return JSON.
func NewJournalLookupHandler(db *sql.DB, logger *slog.Logger) *JournalLookupHandler {
	return &JournalLookupHandler{db: db, logger: logger}
}

// lookupCap caps each list in the response. Workspaces with more rows
// still get a useful subset; the frontend's enrichment cache degrades
// gracefully (entries with unknown ids fall back to id-only display).
const lookupCap = 1000

type lookupCrewEntry struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Slug  string  `json:"slug"`
	Icon  *string `json:"icon"`
	Color *string `json:"color"`
}

type lookupAgentEntry struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Slug        string  `json:"slug"`
	CrewID      *string `json:"crew_id"`
	AvatarSeed  *string `json:"avatar_seed"`
	AvatarStyle *string `json:"avatar_style"`
}

// lookupMissionEntry matches the missions table shape — there's no
// soft-delete column on missions, so all rows are returned regardless
// of status.
type lookupMissionEntry struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

type journalLookupResponse struct {
	Crews    []lookupCrewEntry    `json:"crews"`
	Agents   []lookupAgentEntry   `json:"agents"`
	Missions []lookupMissionEntry `json:"missions"`
}

// Get serves GET /api/v1/journal/lookup. Returns a workspace-scoped
// snapshot of reference rows the frontend needs to render journal
// entries with human-readable names + lucide icons + palette colors.
//
// All three lists are guaranteed non-nil even when empty, so the
// frontend can safely access `.crews.find(...)` etc. without nil
// guards.
func (h *JournalLookupHandler) Get(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}

	resp := journalLookupResponse{
		Crews:    []lookupCrewEntry{},
		Agents:   []lookupAgentEntry{},
		Missions: []lookupMissionEntry{},
	}

	// Crews — icon (lucide name) and color (palette ID) per CLAUDE.md.
	// deleted_at IS NULL skips soft-deleted rows.
	crewRows, err := h.db.QueryContext(r.Context(), `
		SELECT id, name, slug, icon, color
		FROM crews
		WHERE workspace_id = ? AND deleted_at IS NULL
		ORDER BY name
		LIMIT ?`, workspaceID, lookupCap)
	if err != nil {
		h.logger.Error("journal lookup: crews", "err", err)
		replyError(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	for crewRows.Next() {
		var c lookupCrewEntry
		var icon, color sql.NullString
		if err := crewRows.Scan(&c.ID, &c.Name, &c.Slug, &icon, &color); err != nil {
			_ = crewRows.Close()
			h.logger.Error("journal lookup: scan crew", "err", err)
			replyError(w, http.StatusInternalServerError, "lookup failed")
			return
		}
		if icon.Valid {
			s := icon.String
			c.Icon = &s
		}
		if color.Valid {
			s := color.String
			c.Color = &s
		}
		resp.Crews = append(resp.Crews, c)
	}
	if err := crewRows.Err(); err != nil {
		_ = crewRows.Close()
		h.logger.Error("journal lookup: crew rows", "err", err)
		replyError(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	_ = crewRows.Close()

	// Agents — schema has avatar_seed + avatar_style instead of an icon
	// column; the frontend deterministically renders one from the seed.
	// Soft-delete via deleted_at IS NULL.
	agentRows, err := h.db.QueryContext(r.Context(), `
		SELECT id, name, slug, crew_id, avatar_seed, avatar_style
		FROM agents
		WHERE workspace_id = ? AND deleted_at IS NULL
		ORDER BY name
		LIMIT ?`, workspaceID, lookupCap)
	if err != nil {
		h.logger.Error("journal lookup: agents", "err", err)
		replyError(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	for agentRows.Next() {
		var a lookupAgentEntry
		var crewID, seed, style sql.NullString
		if err := agentRows.Scan(&a.ID, &a.Name, &a.Slug, &crewID, &seed, &style); err != nil {
			_ = agentRows.Close()
			h.logger.Error("journal lookup: scan agent", "err", err)
			replyError(w, http.StatusInternalServerError, "lookup failed")
			return
		}
		if crewID.Valid {
			s := crewID.String
			a.CrewID = &s
		}
		if seed.Valid {
			s := seed.String
			a.AvatarSeed = &s
		}
		if style.Valid {
			s := style.String
			a.AvatarStyle = &s
		}
		resp.Agents = append(resp.Agents, a)
	}
	if err := agentRows.Err(); err != nil {
		_ = agentRows.Close()
		h.logger.Error("journal lookup: agent rows", "err", err)
		replyError(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	_ = agentRows.Close()

	// Missions — no deleted_at column; filter by workspace only. Order
	// by created_at DESC so the most recent missions land first within
	// the cap (older missions still reachable via the missions page).
	missionRows, err := h.db.QueryContext(r.Context(), `
		SELECT id, title, status
		FROM missions
		WHERE workspace_id = ?
		ORDER BY created_at DESC
		LIMIT ?`, workspaceID, lookupCap)
	if err != nil {
		h.logger.Error("journal lookup: missions", "err", err)
		replyError(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	for missionRows.Next() {
		var m lookupMissionEntry
		if err := missionRows.Scan(&m.ID, &m.Title, &m.Status); err != nil {
			_ = missionRows.Close()
			h.logger.Error("journal lookup: scan mission", "err", err)
			replyError(w, http.StatusInternalServerError, "lookup failed")
			return
		}
		resp.Missions = append(resp.Missions, m)
	}
	if err := missionRows.Err(); err != nil {
		_ = missionRows.Close()
		h.logger.Error("journal lookup: mission rows", "err", err)
		replyError(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	_ = missionRows.Close()

	writeJSON(w, http.StatusOK, resp)
}
