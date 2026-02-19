package sidecar

import (
	"encoding/json"
	"net/http"
)

// handleMemorySearch handles POST /memory/search.
// Request body: {"query": "search terms", "limit": 10}
// Returns FTS5 search results ranked by BM25.
func (s *Server) handleMemorySearch(w http.ResponseWriter, r *http.Request) {
	if s.memoryEngine == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{
			"error": "memory engine not available",
		})
		return
	}

	var req struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": "invalid JSON body",
		})
		return
	}

	if req.Query == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": "query is required",
		})
		return
	}

	if req.Limit <= 0 {
		req.Limit = 10
	}

	results, err := s.memoryEngine.Search(req.Query, req.Limit)
	if err != nil {
		s.logger.Error("memory search failed", "error", err, "query", req.Query)
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{
			"error": "search failed: " + err.Error(),
		})
		return
	}

	if results == nil {
		writeJSONResponse(w, http.StatusOK, map[string]interface{}{
			"results": []struct{}{},
			"count":   0,
		})
		return
	}

	writeJSONResponse(w, http.StatusOK, map[string]interface{}{
		"results": results,
		"count":   len(results),
	})
}

// handleMemoryStatus handles GET /memory/status.
// Returns information about the memory index state.
func (s *Server) handleMemoryStatus(w http.ResponseWriter, r *http.Request) {
	if s.memoryEngine == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{
			"error": "memory engine not available",
		})
		return
	}

	status, err := s.memoryEngine.Status()
	if err != nil {
		s.logger.Error("memory status failed", "error", err)
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{
			"error": "status check failed",
		})
		return
	}

	writeJSONResponse(w, http.StatusOK, status)
}

// handleMemoryReindex handles POST /memory/reindex.
// Triggers a full reindex of all memory files.
func (s *Server) handleMemoryReindex(w http.ResponseWriter, r *http.Request) {
	if s.memoryEngine == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{
			"error": "memory engine not available",
		})
		return
	}

	if err := s.memoryEngine.Reindex(); err != nil {
		s.logger.Error("memory reindex failed", "error", err)
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{
			"error": "reindex failed: " + err.Error(),
		})
		return
	}

	status, err := s.memoryEngine.Status()
	if err != nil {
		writeJSONResponse(w, http.StatusOK, map[string]string{"status": "reindexed"})
		return
	}

	writeJSONResponse(w, http.StatusOK, status)
}
