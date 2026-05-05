package api

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/skills"
)

// SkillHandler provides endpoints for listing, importing, and managing agent skills.
type SkillHandler struct {
	db     *sql.DB
	logger *slog.Logger
	// SkipURLValidation disables SSRF checks on import URLs (testing only).
	SkipURLValidation bool
}

// NewSkillHandler creates a SkillHandler with the given database and logger.
func NewSkillHandler(db *sql.DB, logger *slog.Logger) *SkillHandler {
	return &SkillHandler{db: db, logger: logger}
}

type skillResponse struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	Slug               string   `json:"slug"`
	DisplayName        string   `json:"display_name"`
	Description        *string  `json:"description"`
	Version            string   `json:"version"`
	Author             *string  `json:"author"`
	Category           string   `json:"category"`
	Source             string   `json:"source"`
	Icon               *string  `json:"icon"`
	Verification       string   `json:"verification"`
	Downloads          int      `json:"downloads"`
	RatingAvg          *float64 `json:"rating_avg"`
	RatingCount        int      `json:"rating_count"`
	Tags               *string  `json:"tags"`
	Featured           bool     `json:"featured"`
	PricingTier        string   `json:"pricing_tier"`
	ToolCount          *int     `json:"tool_count"`
	Vendor             *string  `json:"vendor"`
	Homepage           *string  `json:"homepage"`
	SPDXLicense        *string  `json:"spdx_license"`
	Runtime            string   `json:"runtime"`
	Maturity           string   `json:"maturity"`
	ScanStatus         string   `json:"scan_status"`
	DescriptionQuality *string  `json:"description_quality"`
	CreatedAt          string   `json:"created_at"`
	UpdatedAt          string   `json:"updated_at"`
	// InstalledOn is populated only on the Installed list (?installed=1)
	// — the Browse list omits it because the join would balloon the
	// payload. Each entry is the agent + crew metadata the SkillCard
	// needs to render stacked avatars.
	InstalledOn []skillInstalledAgent `json:"installed_on,omitempty"`
}

type skillInstalledAgent struct {
	AgentID         string  `json:"agent_id"`
	AgentSlug       string  `json:"agent_slug"`
	AgentName       string  `json:"agent_name"`
	AvatarSeed      *string `json:"avatar_seed"`
	AvatarStyle     *string `json:"avatar_style"`
	CrewID          *string `json:"crew_id"`
	CrewSlug        *string `json:"crew_slug"`
	CrewName        *string `json:"crew_name"`
	CrewColor       *string `json:"crew_color"`
	CrewIcon        *string `json:"crew_icon"`
	CrewAvatarStyle *string `json:"crew_avatar_style"`
}

// List returns all skills, optionally filtered by category, source, or search text.
// GET /api/v1/skills
func (h *SkillHandler) List(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")
	source := r.URL.Query().Get("source")
	search := r.URL.Query().Get("search")

	vendor := r.URL.Query().Get("vendor")
	maturity := r.URL.Query().Get("maturity")
	runtime := r.URL.Query().Get("runtime")
	// installed_for_agent_id narrows the list to skills assigned to a
	// specific agent (per-agent installed view); installed=1 alone
	// returns any skill with at least one agent_skills row in the
	// workspace (workspace-wide installed view).
	installedForAgent := r.URL.Query().Get("installed_for_agent_id")
	installedFlag := r.URL.Query().Get("installed") == "1"

	query := `SELECT id, name, slug, display_name, description, version, author,
		category, source, icon, verification, downloads, rating_avg, rating_count,
		tags, featured, pricing_tier, tool_count, vendor, homepage, spdx_license,
		runtime, maturity, scan_status, description_quality, created_at, updated_at
		FROM skills WHERE 1=1`
	var args []interface{}

	switch {
	case installedForAgent != "":
		query += " AND id IN (SELECT skill_id FROM agent_skills WHERE agent_id = ? AND enabled = 1)"
		args = append(args, installedForAgent)
	case installedFlag:
		query += " AND id IN (SELECT DISTINCT skill_id FROM agent_skills WHERE enabled = 1)"
	}

	if category != "" {
		query += " AND category = ?"
		args = append(args, category)
	}
	if source != "" {
		query += " AND source = ?"
		args = append(args, source)
	}
	if vendor != "" {
		query += " AND vendor = ?"
		args = append(args, vendor)
	}
	if maturity != "" {
		query += " AND maturity = ?"
		args = append(args, maturity)
	}
	if runtime != "" {
		query += " AND runtime = ?"
		args = append(args, runtime)
	}
	if search != "" {
		query += " AND (name LIKE ? OR display_name LIKE ? OR description LIKE ?)"
		like := "%" + search + "%"
		args = append(args, like, like, like)
	}

	// Bundled-skill maturity (OFFICIAL > CURATED > COMMUNITY > EXPERIMENTAL)
	// surfaces highest-trust skills first; ties broken alphabetically so the
	// listing remains deterministic across reboots.
	query += " ORDER BY CASE maturity WHEN 'OFFICIAL' THEN 0 WHEN 'CURATED' THEN 1 WHEN 'COMMUNITY' THEN 2 ELSE 3 END, name ASC"

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		h.logger.Error("list skills", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var result []skillResponse
	for rows.Next() {
		var s skillResponse
		var featured int
		if err := rows.Scan(&s.ID, &s.Name, &s.Slug, &s.DisplayName, &s.Description,
			&s.Version, &s.Author, &s.Category, &s.Source, &s.Icon,
			&s.Verification, &s.Downloads, &s.RatingAvg, &s.RatingCount,
			&s.Tags, &featured, &s.PricingTier, &s.ToolCount,
			&s.Vendor, &s.Homepage, &s.SPDXLicense,
			&s.Runtime, &s.Maturity, &s.ScanStatus, &s.DescriptionQuality,
			&s.CreatedAt, &s.UpdatedAt); err != nil {
			h.logger.Error("scan skill", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		s.Featured = featured == 1
		// Normalize tags from JSON string
		if s.Tags != nil && strings.TrimSpace(*s.Tags) == "" {
			s.Tags = nil
		}
		result = append(result, s)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (skills)", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if result == nil {
		result = []skillResponse{}
	}

	// Always populate installed_on so the Browse tab can also surface
	// 'already installed somewhere' badges per the user request — a
	// skill the user has on three agents reads very differently from
	// one that's pristine and the card needs to show that. The query
	// is a single IN-clause keyed on the result IDs (one round-trip
	// regardless of result-set size), so the cost is bounded and
	// acceptable on Browse too.
	if err := h.populateInstalledOn(r, result); err != nil {
		h.logger.Warn("populate installed_on", "error", err)
		// Non-fatal — the cards will render without avatars.
	}

	writeJSON(w, http.StatusOK, result)
}

// populateInstalledOn fans out the agent_skills join into each row's
// InstalledOn array. Single query keyed on the result IDs so the cost
// is one round-trip regardless of result-set size. Order within each
// skill follows agent.name for stable rendering — without an explicit
// sort, the per-row order would shift between requests.
func (h *SkillHandler) populateInstalledOn(r *http.Request, rows []skillResponse) error {
	if len(rows) == 0 {
		return nil
	}
	ids := make([]string, 0, len(rows))
	idx := make(map[string]int, len(rows))
	for i, sr := range rows {
		ids = append(ids, sr.ID)
		idx[sr.ID] = i
	}
	placeholders := strings.Repeat("?,", len(ids)-1) + "?"
	args := make([]interface{}, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	q := `
		SELECT as2.skill_id, a.id, a.slug, a.name, a.avatar_seed, a.avatar_style,
		       c.id, c.slug, c.name, c.color, c.icon, c.avatar_style
		FROM agent_skills as2
		JOIN agents a ON a.id = as2.agent_id
		LEFT JOIN crews c ON c.id = a.crew_id
		WHERE as2.enabled = 1 AND as2.skill_id IN (` + placeholders + `)
		ORDER BY a.name`
	queryRows, err := h.db.QueryContext(r.Context(), q, args...)
	if err != nil {
		return err
	}
	defer queryRows.Close()
	for queryRows.Next() {
		var skillID string
		var ag skillInstalledAgent
		if err := queryRows.Scan(
			&skillID, &ag.AgentID, &ag.AgentSlug, &ag.AgentName,
			&ag.AvatarSeed, &ag.AvatarStyle,
			&ag.CrewID, &ag.CrewSlug, &ag.CrewName, &ag.CrewColor, &ag.CrewIcon, &ag.CrewAvatarStyle,
		); err != nil {
			return err
		}
		i, ok := idx[skillID]
		if !ok {
			continue
		}
		rows[i].InstalledOn = append(rows[i].InstalledOn, ag)
	}
	return queryRows.Err()
}

// Get handles GET /api/v1/skills/{skillId}
func (h *SkillHandler) Get(w http.ResponseWriter, r *http.Request) {
	skillID := r.PathValue("skillId")

	type skillDetailResponse struct {
		skillResponse
		Content                *string `json:"content"`
		CredentialRequirements *string `json:"credential_requirements"`
		McpServerCommand       *string `json:"mcp_server_command"`
		McpServerImage         *string `json:"mcp_server_image"`
		McpTransport           *string `json:"mcp_transport"`
		Dependencies           *string `json:"dependencies"`
		License                *string `json:"license"`
		AgentCount             int     `json:"agent_count"`
		SecurityScore          *int    `json:"security_score"`
		AllowedDomains         *string `json:"allowed_domains"`
		Changelog              *string `json:"changelog"`
	}

	var s skillDetailResponse
	var featured int
	err := h.db.QueryRowContext(r.Context(), `
		SELECT s.id, s.name, s.slug, s.display_name, s.description, s.version, s.author,
		       s.category, s.source, s.icon, s.verification, s.downloads, s.rating_avg, s.rating_count,
		       s.tags, s.featured, s.pricing_tier, s.tool_count,
		       s.vendor, s.homepage, s.spdx_license,
		       s.runtime, s.maturity, s.scan_status, s.description_quality,
		       s.created_at, s.updated_at,
		       s.content, s.credential_requirements, s.mcp_server_command, s.mcp_server_image,
		       s.mcp_transport, s.dependencies, s.license,
		       (SELECT COUNT(*) FROM agent_skills WHERE skill_id = s.id) as agent_count,
		       s.security_score, s.allowed_domains, s.changelog
		FROM skills s WHERE s.id = ?`, skillID).Scan(
		&s.ID, &s.Name, &s.Slug, &s.DisplayName, &s.Description, &s.Version, &s.Author,
		&s.Category, &s.Source, &s.Icon, &s.Verification, &s.Downloads, &s.RatingAvg, &s.RatingCount,
		&s.Tags, &featured, &s.PricingTier, &s.ToolCount,
		&s.Vendor, &s.Homepage, &s.SPDXLicense,
		&s.Runtime, &s.Maturity, &s.ScanStatus, &s.DescriptionQuality,
		&s.CreatedAt, &s.UpdatedAt,
		&s.Content, &s.CredentialRequirements, &s.McpServerCommand, &s.McpServerImage,
		&s.McpTransport, &s.Dependencies, &s.License, &s.AgentCount,
		&s.SecurityScore, &s.AllowedDomains, &s.Changelog,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]interface{}{
				"type":   "about:blank",
				"title":  "Not Found",
				"status": 404,
				"detail": "Skill not found",
			})
			return
		}
		h.logger.Error("get skill", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	s.Featured = featured == 1

	writeJSON(w, http.StatusOK, s)
}

// Import handles POST /api/v1/workspaces/{workspaceId}/skills/import.
// Accepts either a URL or raw SKILL.md content. Requires MANAGER role or above.
func (h *SkillHandler) Import(w http.ResponseWriter, r *http.Request) {
	// RFC 7807 Problem Details error helper
	writeProblem := func(status int, detail string) {
		writeJSON(w, status, map[string]interface{}{
			"type":     "about:blank",
			"title":    http.StatusText(status),
			"status":   status,
			"detail":   detail,
			"instance": r.URL.Path,
		})
	}

	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(http.StatusForbidden, "Forbidden")
		return
	}

	user := UserFromContext(r.Context())
	wsID := WorkspaceIDFromContext(r.Context())

	var req struct {
		URL                string `json:"url"`
		Content            string `json:"content"`
		AllowUnsafeLicense bool   `json:"allow_unsafe_license"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.URL == "" && req.Content == "" {
		writeProblem(http.StatusBadRequest, "url or content is required")
		return
	}
	if req.URL != "" && req.Content != "" {
		writeProblem(http.StatusBadRequest, "provide either url or content, not both")
		return
	}

	// SSRF protection: validate URL before fetching
	if req.URL != "" && !h.SkipURLValidation {
		if err := skills.ValidateImportURL(r.Context(), req.URL); err != nil {
			writeProblem(http.StatusBadRequest, err.Error())
			return
		}
	}

	imp := skills.NewImporter(h.db, h.logger)
	imp.SkipURLValidation = h.SkipURLValidation
	result, err := imp.Import(r.Context(), wsID, user.ID, skills.ImportRequest{
		URL:                req.URL,
		Content:            req.Content,
		AllowUnsafeLicense: req.AllowUnsafeLicense,
	})
	if err != nil {
		h.logger.Info("skill import failed", "error", err)
		writeProblem(http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, result)
}
