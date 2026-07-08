package api

// Run→files association (#839). A read-time, non-invasive mapping of a
// pipeline run to the files it produced: resolve the run's crew, list the
// crew's host-side files (agent /output + /crew/shared) over the same IPC
// proxy the agent/crew file endpoints use, and keep the ones whose
// modification time falls inside the run's [started_at, ended_at] window.
//
// This deliberately does NOT touch the pipeline executor — it reuses the
// existing file plumbing and infers "produced by this run" from the mtime
// window. Limitation: two runs on the same crew whose windows overlap can
// cross-attribute a file; acceptable for the presentation use-case (the
// files ARE the deliverable for document-processing routines).

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// ipcFileInfo mirrors the relevant fields of provider.FileInfo as returned
// by the crewshipd /crews/{id}/files IPC endpoint.
type ipcFileInfo struct {
	Path    string    `json:"path"`
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	IsDir   bool      `json:"is_dir"`
	ModTime time.Time `json:"mod_time"`
}

type runProducedFile struct {
	Path    string    `json:"path"`
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

type runFilesResponse struct {
	CrewID string            `json:"crew_id"`
	Files  []runProducedFile `json:"files"`
}

// RunFiles lists the files a pipeline run produced.
// GET /api/v1/workspaces/{workspaceId}/pipeline-runs/{runId}/files
//
// Crew resolution mirrors RunGitDiff: the run's invoking_crew_id, falling
// back to the pipeline's author_crew_id. A run with no resolvable crew
// (e.g. a workspace-level pipeline) returns an empty file list rather than
// erroring.
func (h *ProxyHandler) RunFiles(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	if !canRole(RoleFromContext(r.Context()), "read") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	var crewID sql.NullString
	var startedAt string
	var endedAt sql.NullString
	err := h.db.QueryRowContext(r.Context(), `
		SELECT COALESCE(NULLIF(pr.invoking_crew_id, ''), p.author_crew_id), pr.started_at, pr.ended_at
		FROM pipeline_runs pr
		LEFT JOIN pipelines p ON pr.pipeline_id = p.id AND p.workspace_id = pr.workspace_id
		WHERE pr.id = ? AND pr.workspace_id = ?`, runID, workspaceID).Scan(&crewID, &startedAt, &endedAt)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "Run not found")
		return
	}
	if err != nil {
		h.logger.Error("resolve run for files", "error", err, "run_id", runID)
		replyError(w, http.StatusInternalServerError, "Failed to resolve run")
		return
	}
	if !crewID.Valid || crewID.String == "" {
		writeJSON(w, http.StatusOK, runFilesResponse{CrewID: "", Files: []runProducedFile{}})
		return
	}

	// SECURITY: re-validate the resolved crew belongs to THIS workspace
	// before reaching into its files — author_crew_id is an unchecked
	// column value (same tenant guard as RunGitDiff).
	var ck int
	if cerr := h.db.QueryRowContext(r.Context(),
		"SELECT 1 FROM crews WHERE id = ? AND workspace_id = ?", crewID.String, workspaceID).Scan(&ck); cerr != nil {
		writeJSON(w, http.StatusOK, runFilesResponse{CrewID: "", Files: []runProducedFile{}})
		return
	}

	// Run window. If the run is still running (ended_at empty) use "now" as
	// the upper bound so in-flight artefacts still surface.
	start, ok := parseRunTime(startedAt)
	if !ok {
		// Without a parseable start we can't window — return empty rather
		// than dumping every pre-existing file as "produced".
		writeJSON(w, http.StatusOK, runFilesResponse{CrewID: crewID.String, Files: []runProducedFile{}})
		return
	}
	end := time.Now()
	if endedAt.Valid && endedAt.String != "" {
		if e, eok := parseRunTime(endedAt.String); eok {
			end = e
		}
	}

	// List both trees the crew writes to: the /output namespace (crew root,
	// recursive — covers /output/<agentSlug>) and /crew/shared.
	esc := url.PathEscape(crewID.String)
	ipcPaths := []string{
		fmt.Sprintf("/crews/%s/files?recursive=true", esc),
		fmt.Sprintf("/crews/%s/files?recursive=true&subdir=shared", esc),
	}

	seen := make(map[string]struct{})
	produced := []runProducedFile{}
	for _, p := range ipcPaths {
		for _, f := range h.listCrewFiles(r, p) {
			if f.IsDir {
				continue
			}
			if f.ModTime.Before(start) || f.ModTime.After(end) {
				continue
			}
			if _, dup := seen[f.Path]; dup {
				continue
			}
			seen[f.Path] = struct{}{}
			produced = append(produced, runProducedFile{
				Path: f.Path, Name: f.Name, Size: f.Size, ModTime: f.ModTime,
			})
		}
	}

	writeJSON(w, http.StatusOK, runFilesResponse{CrewID: crewID.String, Files: produced})
}

// (run-timestamp parsing lives in parseRunTime, issue_handler_runs.go)

// listCrewFiles fetches one crew-files IPC listing and returns its files.
// A failed call or malformed body yields no files (best-effort — a missing
// shared tree shouldn't 502 the whole endpoint).
func (h *ProxyHandler) listCrewFiles(r *http.Request, ipcPath string) []ipcFileInfo {
	resp, err := h.ipcGet(r.Context(), ipcPath)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var data struct {
		Files []ipcFileInfo `json:"files"`
	}
	if json.NewDecoder(resp.Body).Decode(&data) != nil {
		return nil
	}
	return data.Files
}
