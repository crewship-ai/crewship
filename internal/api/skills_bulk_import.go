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
	GitURL string `json:"git_url"`
	GitRef string `json:"git_ref,omitempty"`
	// LocalPath is intentionally NOT part of the HTTP surface. The
	// importer accepts it so test code and the in-process bundled
	// loader can reuse the same walker, but exposing it over the
	// network turns the endpoint into an arbitrary host-FS read
	// primitive — even with the SKILL.md filename filter, a caller
	// can probe directories through symlinks. Anyone wanting a
	// local-path import must run the CLI on the host directly,
	// where authorisation is the OS user account.
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
	// Truncated signals that the walker stopped before reaching the
	// end of the source tree (maxBulkSkills cap reached). Surfaced so
	// the CLI / UI can warn rather than report a partial import as
	// complete.
	Truncated bool `json:"truncated,omitempty"`
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

// isClientFacingImportError returns true when the importer error is
// safe to put in an HTTP body — validation messages we author here in
// the package, not anything that wraps an exec.ExitError or a raw fs
// path. Keep the allowlist tight; the default is "log it, hide it".
func isClientFacingImportError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, prefix := range []string{
		"bulk import requires",
		"bulk import only supports",
		"bulk import via git_url",
		"private/internal IP",
		"localhost git URLs",
		"git URL missing host",
		"git URL must not embed",
		"parse git URL",
	} {
		if strings.Contains(msg, prefix) {
			return true
		}
	}
	return false
}

// Import handles the request, delegating to skills.Importer.BulkImport.
// Errors at the walker level (e.g. git clone failure) come back as 502;
// per-skill rejections go in the response body's Skipped list rather
// than failing the whole batch.
//
// Authorisation: requires the same MANAGER+ role as the single-import
// endpoint (canRole "create"). An earlier revision skipped the role
// check entirely, which meant any workspace member could trigger
// arbitrary git clones through the server — both a credential-cost
// vector (clones run on every retry) and a routing surface (the
// orchestrator process makes outbound HTTPS calls on the operator's
// behalf).
func (h *SkillBulkImportHandler) Import(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

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
	if body.GitURL == "" {
		writeProblem(w, r, http.StatusBadRequest, "git_url is required")
		return
	}

	imp := skills.NewImporter(h.db, h.logger)
	res, err := imp.BulkImport(r.Context(), skills.BulkImportRequest{
		GitURL:             body.GitURL,
		GitRef:             body.GitRef,
		Paths:              body.Paths,
		Vendor:             body.Vendor,
		AllowUnsafeLicense: body.AllowUnsafeLicense,
		DryRun:             body.DryRun,
	})
	if err != nil {
		// Validation errors (bad URL, blocked SSRF, missing git binary)
		// are safe to echo verbatim — they're authored by us in this
		// codebase. Anything else (clone process stderr, walker
		// surprises) can carry git's raw output, which leaks server
		// filesystem paths and any embedded URL credentials. Map those
		// to a generic 502 and keep the detail in the server log.
		if isClientFacingImportError(err) {
			writeProblem(w, r, http.StatusBadGateway, "bulk import failed: "+err.Error())
		} else {
			h.logger.Error("bulk import: server-side failure",
				"error", err, "git_url", body.GitURL)
			writeProblem(w, r, http.StatusBadGateway, "bulk import failed; check server logs for details")
		}
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
		Truncated:     res.Truncated,
	})
}
