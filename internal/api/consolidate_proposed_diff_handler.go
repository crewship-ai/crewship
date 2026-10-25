package api

// HITL proposal diff endpoint — Iter 3 of the memory-hardening
// series. Sits next to Explain (read-only) and Approve/Reject
// (write) on the ProposedHandler so the human-in-the-loop UI can
// preview, in a single round trip, the exact byte-level change
// that approving a memory_proposals row would land in the
// canonical learned-*.md file.
//
// Without this, the only way to know what a merge will produce
// is to approve it and then read the result — which is a no-op
// for cases where the reviewer needs to abort. The diff endpoint
// closes that gap.
//
// Endpoint: GET /api/v1/consolidate/proposed/{id}/diff
// Auth:     authed + wsCtx + MEMBER+ (matches Explain — no
//           write authority required to preview).
//
// Response shape (stable; downstream UI keys are pinned by the
// table-driven test):
//
//	{
//	  "proposal_id": "...",
//	  "workspace_id": "...",
//	  "crew_id": "...",
//	  "status": "pending",                    // can preview rejected/approved too
//	  "canonical_path": "/.../learned-2026-05-18.md",
//	  "canonical_exists": true,
//	  "proposal_path": "/.../.proposed/proposal-...md",
//	  "rules_count": 3,
//	  "diff": "--- canonical (current)\n+++ canonical (post-merge)\n@@ ...",
//	  "stats": {
//	    "additions": 14,
//	    "deletions": 0,
//	    "rules_appended": 3
//	  }
//	}
//
// Reading the proposal body from disk is best-effort: a missing
// .proposed file (deleted out-of-band) surfaces as 410 Gone with
// a clear message — the proposal row exists but the source
// markdown does not. Cross-workspace probe is 404 (same as
// Explain) so existence of proposals in other workspaces stays
// unobservable.

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
)

// proposalDiffMaxBytes retains the existing absolute per-file read ceiling.
// The configured shared budget may impose a smaller input/line ceiling.
const proposalDiffMaxBytes = 8 * 1024 * 1024

type proposalDiffStats struct {
	Additions     int `json:"additions"`
	Deletions     int `json:"deletions"`
	RulesAppended int `json:"rules_appended"`
}

type proposalDiffResponse struct {
	ProposalID      string            `json:"proposal_id"`
	WorkspaceID     string            `json:"workspace_id"`
	CrewID          string            `json:"crew_id"`
	Status          string            `json:"status"`
	CanonicalPath   string            `json:"canonical_path"`
	CanonicalExists bool              `json:"canonical_exists"`
	ProposalPath    string            `json:"proposal_path"`
	RulesCount      int               `json:"rules_count"`
	Diff            string            `json:"diff"`
	Stats           proposalDiffStats `json:"stats"`
}

// Diff serves GET /api/v1/consolidate/proposed/{id}/diff.
//
// The simulation uses the same canonical-path resolution + append
// block construction that ApproveProposal does (via the exported
// helpers CanonicalPathForProposal and BuildCanonicalAppendBlock),
// so a future change to either is automatically reflected in the
// preview — drift between "what diff said" and "what approve did"
// would be a serious operator-trust bug, kept tight by the table-
// driven test that calls both and asserts byte equality.
func (h *ProposedHandler) Diff(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	proposalID := r.PathValue("id")
	if proposalID == "" {
		replyError(w, http.StatusBadRequest, "proposal id required")
		return
	}
	exp, err := consolidate.ExplainProposal(r.Context(), h.db, proposalID)
	if err != nil {
		h.mapDecisionError(w, err, "diff")
		return
	}
	if exp.WorkspaceID != wsID {
		replyError(w, http.StatusNotFound, "memory proposal not found")
		return
	}
	// Check the source artifact's existence before admission, preserving 410.
	// Reading and formatting wait until after the nonblocking shared gate.
	proposalFile, err := os.Open(exp.ProposalPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			replyError(w, http.StatusGone, "proposal markdown is missing on disk")
			return
		}
		h.logger.Error("proposal diff: open body", "proposal_id", proposalID, "error", err)
		replyError(w, http.StatusInternalServerError, "diff failed")
		return
	}
	defer proposalFile.Close()
	release, admitted := h.diffs.acquire()
	if !admitted {
		replyDiffBusy(w)
		return
	}
	defer release()
	body, oversize, err := h.diffs.readFile(r.Context(), proposalFile)
	if err != nil {
		h.logger.Error("proposal diff: read body", "proposal_id", proposalID, "error", err)
		replyError(w, http.StatusInternalServerError, "diff failed")
		return
	}
	if oversize {
		replyError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("diff is too large: proposal markdown exceeds %d bytes", h.diffs.limits.MaxInputBytes))
		return
	}
	rulesBlock := consolidate.ExtractProposalRulesBody(string(body))
	now := time.Now().UTC()
	canonicalPath := consolidate.CanonicalPathForProposal(exp.ProposalPath, now)
	var current []byte
	canonicalExists := true
	canonicalFile, err := os.Open(canonicalPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		canonicalExists = false
	case err != nil:
		h.logger.Error("proposal diff: open canonical", "path", canonicalPath, "error", err)
		replyError(w, http.StatusInternalServerError, "diff failed")
		return
	default:
		defer canonicalFile.Close()
		var over bool
		current, over, err = h.diffs.readFile(r.Context(), canonicalFile)
		if err != nil {
			h.logger.Error("proposal diff: read canonical", "path", canonicalPath, "error", err)
			replyError(w, http.StatusInternalServerError, "diff failed")
			return
		}
		if over {
			replyError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("diff is too large: canonical learned-*.md exceeds %d bytes", h.diffs.limits.MaxInputBytes))
			return
		}
	}
	appendBlock := consolidate.BuildCanonicalAppendBlock(canonicalExists, now, rulesBlock)
	// Account for both sides before allocating the merged side; subtraction
	// checks in checkLengths avoid integer overflow as well as excess memory.
	if err := h.diffs.checkLengths(len(current), len(current), len(appendBlock)); err != nil {
		replyDiffTooLarge(w)
		return
	}
	merged := append([]byte{}, current...)
	merged = append(merged, appendBlock...)
	diffText, err := h.diffs.render(string(current), string(merged), "canonical (current)", "canonical (post-merge)")
	if errors.Is(err, errDiffTooLarge) {
		replyDiffTooLarge(w)
		return
	}
	if err != nil {
		h.logger.Error("proposal diff: build diff", "proposal_id", proposalID, "error", err)
		replyError(w, http.StatusInternalServerError, "diff failed")
		return
	}
	additions, deletions := countDiffEdits(diffText)
	writeJSON(w, http.StatusOK, proposalDiffResponse{
		ProposalID: exp.ProposalID, WorkspaceID: exp.WorkspaceID, CrewID: exp.CrewID, Status: exp.Status,
		CanonicalPath: canonicalPath, CanonicalExists: canonicalExists, ProposalPath: exp.ProposalPath, RulesCount: exp.RulesCount, Diff: diffText,
		Stats: proposalDiffStats{Additions: additions, Deletions: deletions, RulesAppended: exp.RulesCount},
	})
}

// countDiffEdits walks the unified-diff output and tallies lines
// that begin with '+' / '-' but are not the diff's own header
// lines (--- and +++). Hunk markers (@@) and context lines (' ')
// are ignored. Empty diffs (identical inputs) return zeroes.
func countDiffEdits(diff string) (int, int) {
	var add, del int
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			continue
		case strings.HasPrefix(line, "+"):
			add++
		case strings.HasPrefix(line, "-"):
			del++
		}
	}
	return add, del
}
