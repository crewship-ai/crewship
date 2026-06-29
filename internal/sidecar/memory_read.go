package sidecar

import (
	"errors"
	"net/http"
	"os"
	"strconv"
)

// MemoryReadResponse is the success envelope for GET /memory/read.
// Returning the content as JSON (vs. application/octet-stream) keeps
// the surface symmetric with /memory/search and /memory/status — every
// agent-callable endpoint returns JSON, agents parse JSON. The byte
// length lands in a sibling field so callers don't recompute from the
// string.
type MemoryReadResponse struct {
	Path  string `json:"path"`
	Scope string `json:"scope"`
	Bytes int    `json:"bytes"`
	// Content is the UTF-8 string the file's bytes decode to. Memory
	// files are always markdown so a string field is appropriate;
	// binary memory is not a supported tier. Empty file → empty
	// string + Bytes=0, NOT a 404.
	Content string `json:"content"`
}

// handleMemoryRead serves GET /memory/read?file=...&scope=...
// Foundation for the memory_read tool the agent will call mid-session
// (PR #3 step 3). Path-traversal guard mirrors handleMemoryWrite so
// the read + write surfaces stay symmetric: relative paths only,
// absolute and "../" rejected with 403, no escape outside the
// configured tier base.
//
// Returns:
//
//	200 OK            { path, scope, bytes, content }
//	400 Bad Request   missing file param OR unknown scope
//	403 Forbidden     illegal file path (traversal / absolute)
//	404 Not Found     file does not exist
//	503 Service Unavailable  memory engine for scope not configured
func (s *Server) handleMemoryRead(w http.ResponseWriter, r *http.Request) {
	file := r.URL.Query().Get("file")
	if file == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "file query param required"})
		return
	}
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "agent"
	}

	_, basePath, valid := s.resolveMemoryEngineWithPath(scope)
	if !valid {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid scope: use agent or crew"})
		return
	}
	if basePath == "" {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": scope + " memory engine not available"})
		return
	}

	target, err := safeJoinUnder(basePath, file)
	if err != nil {
		// Same security stance as the write handler: don't echo the
		// resolved path back, so a probe attempt doesn't get free
		// feedback on what's under the base.
		writeJSONResponse(w, http.StatusForbidden, map[string]string{"error": "illegal file path"})
		return
	}

	// Apply the write-side filename whitelist to the read path too (finding
	// MEM, 2026-06 audit): the read surface must be no more permissive than
	// the write surface, so an agent can only read the same AGENT.md / CREW.md
	// / pins.md / daily/<name>.md files it is allowed to write.
	if _, known := memoryFileCap(file); !known {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": "unsupported file path; allowed: AGENT.md, CREW.md, pins.md, daily/<name>.md",
		})
		return
	}

	content, err := os.ReadFile(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSONResponse(w, http.StatusNotFound, map[string]string{"error": "file not found"})
			return
		}
		s.logger.Error("memory read failed", "error", err, "scope", scope, "file", file)
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "read failed"})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Memory-Bytes", strconv.Itoa(len(content)))
	writeJSONResponse(w, http.StatusOK, MemoryReadResponse{
		Path:    target,
		Scope:   scope,
		Bytes:   len(content),
		Content: string(content),
	})
}
