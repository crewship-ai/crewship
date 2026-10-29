package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// versionDiffResponse is the wire shape for GET .../pipelines/{slug}/diff.
// UnifiedDiff is computed over PRETTY-PRINTED JSON of each version's
// definition (not the raw compact storage form) so the diff reads like a
// real code review — matching indentation lines up, and a single field
// change shows as a single-line hunk instead of the whole minified blob
// changing.
type versionDiffResponse struct {
	Slug        string `json:"slug"`
	FromVersion int    `json:"from_version"`
	ToVersion   int    `json:"to_version"`
	FromHash    string `json:"from_hash"`
	ToHash      string `json:"to_hash"`
	Identical   bool   `json:"identical"`
	UnifiedDiff string `json:"unified_diff"`
}

// DiffVersions returns a unified diff between two versions of a routine's
// definition (#1422 item 5). `versions show <n>` already dumps one
// version for external diffing; this is the native in-product equivalent
// — also what a post-rollback "what changed" view is built from (the CLI
// rollback command calls this with from=<new head> to=<previous head>).
//
// GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/diff?from=N&to=M
func (h *PipelineHandler) DiffVersions(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")

	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")
	if fromStr == "" || toStr == "" {
		replyError(w, http.StatusBadRequest, "from and to query params are required (version numbers)")
		return
	}
	fromN, ferr := parseSmallInt(fromStr)
	if ferr != nil {
		replyError(w, http.StatusBadRequest, "from must be a positive integer")
		return
	}
	toN, terr := parseSmallInt(toStr)
	if terr != nil {
		replyError(w, http.StatusBadRequest, "to must be a positive integer")
		return
	}

	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	if err != nil {
		replyError(w, http.StatusInternalServerError, "load pipeline")
		return
	}

	fromV, err := h.store.GetVersion(r.Context(), p.ID, fromN)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "from version not found")
		return
	}
	if err != nil {
		h.logger.Error("pipeline diff versions: load from", "pipeline_id", p.ID, "version", fromN, "error", err)
		replyError(w, http.StatusInternalServerError, "failed to load from version")
		return
	}
	toV, err := h.store.GetVersion(r.Context(), p.ID, toN)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "to version not found")
		return
	}
	if err != nil {
		h.logger.Error("pipeline diff versions: load to", "pipeline_id", p.ID, "version", toN, "error", err)
		replyError(w, http.StatusInternalServerError, "failed to load to version")
		return
	}

	resp := versionDiffResponse{
		Slug:        slug,
		FromVersion: fromN,
		ToVersion:   toN,
		FromHash:    fromV.DefinitionHash,
		ToHash:      toV.DefinitionHash,
		Identical:   fromV.DefinitionHash == toV.DefinitionHash,
	}
	if !resp.Identical {
		release, admitted := h.diffs.acquire()
		if !admitted {
			replyDiffBusy(w)
			return
		}
		defer release()
		fromPretty, toPretty, derr := h.diffs.prettyPair(fromV.DefinitionJSON, toV.DefinitionJSON)
		if errors.Is(derr, errDiffTooLarge) {
			replyDiffTooLarge(w)
			return
		}
		if derr != nil {
			replyError(w, http.StatusInternalServerError, "failed to compute diff")
			return
		}
		out, derr := h.diffs.render(fromPretty, toPretty, "v"+strconv.Itoa(fromN), "v"+strconv.Itoa(toN))
		if errors.Is(derr, errDiffTooLarge) {
			replyDiffTooLarge(w)
			return
		}
		if derr != nil {
			h.logger.Error("pipeline diff versions: compute diff", "pipeline_id", p.ID, "error", derr)
			replyError(w, http.StatusInternalServerError, "failed to compute diff")
			return
		}
		resp.UnifiedDiff = out
	}
	writeJSON(w, http.StatusOK, resp)
}
