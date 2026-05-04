package api

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/skills"
)

// SkillBulkImportHandler exposes POST /workspaces/{workspaceId}/skills/bulk-import
// which walks a git repo or local path for SKILL.md files and upserts each
// through the v65-aware path with license gating and built-in scanning.
type SkillBulkImportHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewSkillBulkImportHandler wires the handler against an open *sql.DB.
func NewSkillBulkImportHandler(db *sql.DB, logger *slog.Logger) *SkillBulkImportHandler {
	return &SkillBulkImportHandler{db: db, logger: logger}
}

type skillBulkImportRequest struct {
	GitURL             string   `json:"git_url"`
	GitRef             string   `json:"git_ref,omitempty"`
	LocalPath          string   `json:"local_path,omitempty"`
	Paths              []string `json:"paths,omitempty"`
	Vendor             string   `json:"vendor,omitempty"`
	AllowUnsafeLicense bool     `json:"allow_unsafe_license,omitempty"`
	DryRun             bool     `json:"dry_run,omitempty"`
}

type skillBulkImportResponse struct {
	Source        string                  `json:"source"`
	TotalFound    int                     `json:"total_found"`
	TotalImported int                     `json:"total_imported"`
	Imported      []skillBulkImportedItem `json:"imported"`
	Skipped       []skillBulkSkippedItem  `json:"skipped"`
}

type skillBulkImportedItem struct {
	SkillID string `json:"skill_id"`
	Slug    string `json:"slug"`
	Created bool   `json:"created"`
}

type skillBulkSkippedItem struct {
	Path   string `json:"path"`
	Slug   string `json:"slug,omitempty"`
	Reason string `json:"reason"`
}

// Import handles the request, delegating to skills.Importer.BulkImport.
// Errors at the walker level (e.g. git clone failure) come back as 502;
// per-skill rejections go in the response body's Skipped list rather
// than failing the whole batch.
func (h *SkillBulkImportHandler) Import(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("workspaceId") == "" {
		writeProblem(w, r, http.StatusBadRequest, "workspace_id is required")
		return
	}

	var body skillBulkImportRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	body.GitURL = strings.TrimSpace(body.GitURL)
	body.LocalPath = strings.TrimSpace(body.LocalPath)
	if body.GitURL == "" && body.LocalPath == "" {
		writeProblem(w, r, http.StatusBadRequest, "git_url or local_path is required")
		return
	}

	imp := skills.NewImporter(h.db, h.logger)
	res, err := imp.BulkImport(r.Context(), skills.BulkImportRequest{
		GitURL:             body.GitURL,
		GitRef:             body.GitRef,
		LocalPath:          body.LocalPath,
		Paths:              body.Paths,
		Vendor:             body.Vendor,
		AllowUnsafeLicense: body.AllowUnsafeLicense,
		DryRun:             body.DryRun,
	})
	if err != nil {
		writeProblem(w, r, http.StatusBadGateway, "bulk import failed: "+err.Error())
		return
	}

	imported := make([]skillBulkImportedItem, 0, len(res.Skills))
	for _, s := range res.Skills {
		imported = append(imported, skillBulkImportedItem{
			SkillID: s.SkillID,
			Slug:    s.Slug,
			Created: s.Created,
		})
	}
	skipped := make([]skillBulkSkippedItem, 0, len(res.Skipped))
	for _, s := range res.Skipped {
		skipped = append(skipped, skillBulkSkippedItem{
			Path:   s.Path,
			Slug:   s.Slug,
			Reason: s.Reason,
		})
	}
	writeJSON(w, http.StatusOK, skillBulkImportResponse{
		Source:        res.Source,
		TotalFound:    res.TotalFound,
		TotalImported: res.TotalImported,
		Imported:      imported,
		Skipped:       skipped,
	})
}
