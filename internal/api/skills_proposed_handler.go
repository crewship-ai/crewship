package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/skills"
)

// SkillProposedHandler serves the HITL surface for the memory→Skills
// bridge (PRD §8.2). The handler is stateless against the database —
// truth lives on disk under {crewMemoryRoot}/{crew-slug}/topics/.proposed/
// skill-*.md, the same directory the consolidator writes to in
// proposal mode. There is no skill_proposals table because the
// lifecycle is short: a staged skill is either approved (the canonical
// importer ingests it into the skills table and the staging file is
// removed) or rejected (file deleted). Audit is via journal entries
// EntryMemorySkillApproved / EntryMemorySkillRejected.
//
// Why stateless: a skill_proposals table would buy us "who staged this
// when" history, but the bridge already emits journal entries on
// staging and approve/reject emit their own. The DB row would be a
// 1:1 mirror of those journal entries with no extra information. The
// trade-off cost — a migration, a sync hazard between disk and DB
// state, recovery code for "file missing but row exists" — wasn't
// worth it for MVP. Future iterations can add the row if pagination
// or per-proposal annotations become needed.
//
// Auth: OWNER, ADMIN, and MANAGER may list, approve, and reject. The
// MANAGER threshold matches the canonical skill-import permission
// (canRole("create") in the CASL config); auto-promoted skills are
// just one more import surface.
type SkillProposedHandler struct {
	db             *sql.DB
	logger         *slog.Logger
	journal        journal.Emitter
	crewMemoryRoot string
	importer       *skills.Importer
}

// NewSkillProposedHandler constructs the handler with stub journal.
// Call SetJournal, SetCrewMemoryRoot, and SetImporter at router-mount
// time to wire production dependencies.
func NewSkillProposedHandler(db *sql.DB, logger *slog.Logger) *SkillProposedHandler {
	return &SkillProposedHandler{
		db:       db,
		logger:   logger,
		journal:  noopEmitter{},
		importer: skills.NewImporter(db, logger),
	}
}

// SetJournal wires the production emitter.
func (h *SkillProposedHandler) SetJournal(j journal.Emitter) {
	if j == nil {
		h.journal = noopEmitter{}
		return
	}
	h.journal = j
}

// SetCrewMemoryRoot pins the parent directory for crew memory files.
// Path layout: {root}/{crew-slug}/topics/.proposed/skill-*.md, which
// matches the consolidator's OutputDir convention.
func (h *SkillProposedHandler) SetCrewMemoryRoot(root string) {
	h.crewMemoryRoot = root
}

// SetImporter overrides the default importer. Used by tests; production
// uses the importer attached at construction.
func (h *SkillProposedHandler) SetImporter(imp *skills.Importer) {
	if imp != nil {
		h.importer = imp
	}
}

// ProposedSkillSummary is one entry in the List response. It carries
// only the fields the inbox UI needs to render the row — name,
// description, the staging-file name (operators reference this when
// they hit approve/reject), and the parser's quality verdict on the
// description so the UI can render a warning chip on weak triggers.
type ProposedSkillSummary struct {
	FileName           string `json:"file_name"`
	Name               string `json:"name"`
	Description        string `json:"description"`
	DescriptionQuality string `json:"description_quality"`
	Category           string `json:"category"`
}

// List serves GET /api/v1/skills/proposed?crew_id=X. Returns a JSON
// array of ProposedSkillSummary. Empty array (200) when the crew has
// no staged skills.
func (h *SkillProposedHandler) List(w http.ResponseWriter, r *http.Request) {
	if !h.requireSkillManagerRole(w, r) {
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	crewID := r.URL.Query().Get("crew_id")
	if crewID == "" {
		replyError(w, http.StatusBadRequest, "crew_id query param required")
		return
	}

	dir, err := h.proposedDirForCrew(r.Context(), wsID, crewID)
	if err != nil {
		h.mapDirError(w, err)
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusOK, []ProposedSkillSummary{})
			return
		}
		replyError(w, http.StatusInternalServerError, "list staged skills: "+err.Error())
		return
	}

	out := make([]ProposedSkillSummary, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "skill-") || !strings.HasSuffix(name, ".md") {
			continue
		}
		full := filepath.Join(dir, name)
		raw, err := os.ReadFile(full)
		if err != nil {
			h.logger.Warn("skills proposed: read entry", "file", full, "err", err)
			continue
		}
		parsed, err := skills.ParseSKILLMD(string(raw))
		if err != nil {
			// Don't drop on parse error — surface the file so an
			// operator can manually reject it. Empty Meta fields tell
			// the UI to render a "malformed" badge.
			out = append(out, ProposedSkillSummary{
				FileName:           name,
				DescriptionQuality: "parse error: " + err.Error(),
			})
			continue
		}
		out = append(out, ProposedSkillSummary{
			FileName:           name,
			Name:               parsed.Meta.Name,
			Description:        parsed.Meta.Description,
			DescriptionQuality: parsed.DescriptionQuality,
			Category:           parsed.Meta.Category,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FileName < out[j].FileName })
	writeJSON(w, http.StatusOK, out)
}

// approveBody is the request shape for POST /api/v1/skills/proposed/approve.
// FileName is the bare basename returned by List; the handler resolves
// the full path under the crew's .proposed directory.
type approveBody struct {
	CrewID   string `json:"crew_id"`
	FileName string `json:"file_name"`
}

// Approve serves POST /api/v1/skills/proposed/approve. On success the
// SKILL.md content is imported through the canonical skills importer
// (same path used by the URL/paste import surface), the staging file
// is removed, and an EntryMemorySkillApproved journal entry fires.
// Returns the imported skill id so the UI can deep-link to it.
func (h *SkillProposedHandler) Approve(w http.ResponseWriter, r *http.Request) {
	if !h.requireSkillManagerRole(w, r) {
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())
	actorID := ""
	if user != nil {
		actorID = user.ID
	}

	var body approveBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if body.CrewID == "" || body.FileName == "" {
		replyError(w, http.StatusBadRequest, "crew_id and file_name required")
		return
	}
	if !safeStagingFileName(body.FileName) {
		// A file name with .. or / would let a caller escape the
		// .proposed dir; reject before any filesystem call.
		replyError(w, http.StatusBadRequest, "invalid file_name")
		return
	}

	dir, err := h.proposedDirForCrew(r.Context(), wsID, body.CrewID)
	if err != nil {
		h.mapDirError(w, err)
		return
	}
	full := filepath.Join(dir, body.FileName)
	raw, err := os.ReadFile(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			replyError(w, http.StatusNotFound, "staged skill not found")
			return
		}
		replyError(w, http.StatusInternalServerError, "read staged skill: "+err.Error())
		return
	}

	result, err := h.importer.Import(r.Context(), wsID, actorID, skills.ImportRequest{
		Content: string(raw),
	})
	if err != nil {
		// Surface the importer's own error — typically a parser
		// validation failure (frontmatter malformed) or a license
		// rejection. 422 because the request was well-formed but the
		// staged content failed validation; the operator's option is
		// to fix the file or reject it.
		replyError(w, http.StatusUnprocessableEntity, "import staged skill: "+err.Error())
		return
	}

	// Delete only on successful import — if import failed we leave the
	// file on disk so the operator can retry after a fix. A best-effort
	// remove failure is logged but not surfaced; the imported row is
	// already authoritative, and the staging file will get cleaned up
	// on the next consolidator pass.
	if err := os.Remove(full); err != nil {
		h.logger.Warn("skills proposed: stage remove after approve",
			"file", full, "err", err, "skill_id", result.SkillID)
	}

	if _, emitErr := h.journal.Emit(r.Context(), journal.Entry{
		WorkspaceID: wsID,
		CrewID:      body.CrewID,
		Type:        journal.EntryMemorySkillApproved,
		ActorType:   journal.ActorUser,
		ActorID:     actorID,
		Severity:    journal.SeverityNotice,
		Summary:     "skill approved from memory: " + result.SkillID,
		Payload: map[string]any{
			"skill_id":   result.SkillID,
			"skill_path": full,
			"slug":       result.Slug,
			"created":    result.Created,
		},
	}); emitErr != nil {
		h.logger.Warn("skill approve emit", "err", emitErr)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"skill_id":  result.SkillID,
		"slug":      result.Slug,
		"created":   result.Created,
		"file_name": body.FileName,
	})
}

// Reject serves POST /api/v1/skills/proposed/reject. Same body shape
// as Approve. Deletes the staging file and emits an audit entry.
// Returns 200 even on a missing file (idempotent — calling reject
// twice should not be an error).
func (h *SkillProposedHandler) Reject(w http.ResponseWriter, r *http.Request) {
	if !h.requireSkillManagerRole(w, r) {
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())
	actorID := ""
	if user != nil {
		actorID = user.ID
	}

	var body approveBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if body.CrewID == "" || body.FileName == "" {
		replyError(w, http.StatusBadRequest, "crew_id and file_name required")
		return
	}
	if !safeStagingFileName(body.FileName) {
		replyError(w, http.StatusBadRequest, "invalid file_name")
		return
	}

	dir, err := h.proposedDirForCrew(r.Context(), wsID, body.CrewID)
	if err != nil {
		h.mapDirError(w, err)
		return
	}
	full := filepath.Join(dir, body.FileName)
	removed := true
	if err := os.Remove(full); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			removed = false
		} else {
			replyError(w, http.StatusInternalServerError, "delete staged skill: "+err.Error())
			return
		}
	}

	if _, emitErr := h.journal.Emit(r.Context(), journal.Entry{
		WorkspaceID: wsID,
		CrewID:      body.CrewID,
		Type:        journal.EntryMemorySkillRejected,
		ActorType:   journal.ActorUser,
		ActorID:     actorID,
		Severity:    journal.SeverityInfo,
		Summary:     "skill rejected from memory: " + body.FileName,
		Payload: map[string]any{
			"skill_path":    full,
			"removed_on_fs": removed,
			"file_name":     body.FileName,
		},
	}); emitErr != nil {
		h.logger.Warn("skill reject emit", "err", emitErr)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"file_name": body.FileName,
		"removed":   removed,
	})
}

// requireSkillManagerRole enforces OWNER/ADMIN/MANAGER. Returns true
// when the request may proceed; writes 401/403 and returns false on
// rejection.
func (h *SkillProposedHandler) requireSkillManagerRole(w http.ResponseWriter, r *http.Request) bool {
	if WorkspaceIDFromContext(r.Context()) == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return false
	}
	role := RoleFromContext(r.Context())
	if role != "OWNER" && role != "ADMIN" && role != "MANAGER" {
		replyError(w, http.StatusForbidden, "skill review requires OWNER, ADMIN, or MANAGER role")
		return false
	}
	return true
}

// proposedDirForCrew resolves the on-disk .proposed directory for a
// crew. Looks up the crew slug from the DB and joins it with the
// crewMemoryRoot. Returns ErrNotExist semantics so callers can map to
// 404 — a crew the caller can't see or doesn't exist is the same to
// the response shape.
func (h *SkillProposedHandler) proposedDirForCrew(ctx context.Context, workspaceID, crewID string) (string, error) {
	if h.crewMemoryRoot == "" {
		return "", errors.New("crew memory root not configured")
	}
	var slug string
	err := h.db.QueryRowContext(ctx,
		`SELECT slug FROM crews WHERE id = ? AND workspace_id = ?`,
		crewID, workspaceID).Scan(&slug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", os.ErrNotExist
		}
		return "", err
	}
	// Slug comes from the DB, but the DB layer enforces only the
	// uniqueness constraint — not the filesystem-safety contract this
	// path join needs. A historical row with "../foo" or "x/y" slug
	// would let the .proposed directory escape crewMemoryRoot. Reject
	// these explicitly so the bug surfaces as "crew not found" (the
	// caller's UX wouldn't change), not as a path leak.
	if slug == "" ||
		strings.ContainsAny(slug, `/\`) ||
		strings.Contains(slug, "..") ||
		filepath.Clean(slug) != slug ||
		filepath.IsAbs(slug) {
		return "", os.ErrNotExist
	}
	return filepath.Join(h.crewMemoryRoot, slug, "topics", ".proposed"), nil
}

// mapDirError maps the proposedDirForCrew error palette onto HTTP.
// ErrNotExist (crew not in workspace, or missing) maps to 404; "not
// configured" surfaces as 503 so the operator knows it's a server-side
// setup gap rather than a missing skill.
func (h *SkillProposedHandler) mapDirError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, os.ErrNotExist):
		replyError(w, http.StatusNotFound, "crew not found")
	case err.Error() == "crew memory root not configured":
		replyError(w, http.StatusServiceUnavailable, "skills proposal listing not configured on this server")
	default:
		// Log the raw error server-side; surface a generic message to
		// the client. Raw DB / filesystem errors leak internals (table
		// names, paths, OS-level errno strings) that an attacker can
		// use to probe the deployment.
		h.logger.Error("skills proposed: resolve dir", "err", err)
		replyError(w, http.StatusInternalServerError, "internal server error")
	}
}

// safeStagingFileName rejects path-escape sequences. The bridge writes
// only "skill-{slug}.md" with -N suffix for disambiguation; anything
// else is suspicious. We hard-require the prefix + suffix and no
// slashes — that lets the file walk under filepath.Join stay inside
// the .proposed directory we resolved above.
func safeStagingFileName(name string) bool {
	if name == "" || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return false
	}
	if !strings.HasPrefix(name, "skill-") || !strings.HasSuffix(name, ".md") {
		return false
	}
	return true
}
