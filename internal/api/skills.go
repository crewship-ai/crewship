package api

import (
	"database/sql"
	"log/slog"
	"net/http"
	"strings"
)

type SkillHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewSkillHandler(db *sql.DB, logger *slog.Logger) *SkillHandler {
	return &SkillHandler{db: db, logger: logger}
}

type skillResponse struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Slug         string  `json:"slug"`
	DisplayName  string  `json:"display_name"`
	Description  *string `json:"description"`
	Version      string  `json:"version"`
	Author       *string `json:"author"`
	Category     string  `json:"category"`
	Source       string  `json:"source"`
	Icon         *string `json:"icon"`
	Verification string  `json:"verification"`
	Downloads    int     `json:"downloads"`
	RatingAvg    *float64 `json:"rating_avg"`
	RatingCount  int     `json:"rating_count"`
	Tags         *string `json:"tags"`
	Featured     bool    `json:"featured"`
	PricingTier  string  `json:"pricing_tier"`
	ToolCount    *int    `json:"tool_count"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}

func (h *SkillHandler) List(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")
	source := r.URL.Query().Get("source")
	search := r.URL.Query().Get("search")

	query := `SELECT id, name, slug, display_name, description, version, author,
		category, source, icon, verification, downloads, rating_avg, rating_count,
		tags, featured, pricing_tier, tool_count, created_at, updated_at
		FROM skills WHERE 1=1`
	var args []interface{}

	if category != "" {
		query += " AND category = ?"
		args = append(args, category)
	}
	if source != "" {
		query += " AND source = ?"
		args = append(args, source)
	}
	if search != "" {
		query += " AND (name LIKE ? OR display_name LIKE ? OR description LIKE ?)"
		like := "%" + search + "%"
		args = append(args, like, like, like)
	}

	query += " ORDER BY name ASC"

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
			&s.CreatedAt, &s.UpdatedAt); err != nil {
			h.logger.Error("scan skill", "error", err)
			continue
		}
		s.Featured = featured == 1
		// Normalize tags from JSON string
		if s.Tags != nil && strings.TrimSpace(*s.Tags) == "" {
			s.Tags = nil
		}
		result = append(result, s)
	}

	if result == nil {
		result = []skillResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}
