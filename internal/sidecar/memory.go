package sidecar

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/crewship-ai/crewship/internal/memory"
)

// scopedResult wraps a SearchResult with its source scope.
// This avoids modifying the shared memory.SearchResult struct (DRY).
type scopedResult struct {
	memory.SearchResult
	Source string `json:"source"` // "agent" or "crew"
}

// resolveMemoryEngine returns the engine for the given scope.
// Returns engine, true if valid scope; nil, false if scope is invalid.
func (s *Server) resolveMemoryEngine(scope string) (*memory.Engine, bool) {
	switch scope {
	case "agent", "":
		return s.memoryEngine, true
	case "crew":
		return s.crewMemoryEngine, true
	default:
		return nil, false
	}
}

// handleMemorySearch handles POST /memory/search.
// Request body: {"query": "...", "limit": 10, "scope": "agent|crew|both"}
// scope defaults to "agent" for backward compatibility.
func (s *Server) handleMemorySearch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
		Scope string `json:"scope"`
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
	if req.Scope == "" {
		req.Scope = "agent"
	}

	switch req.Scope {
	case "agent":
		s.searchSingleScope(w, r, s.memoryEngine, "agent", req.Query, req.Limit)
	case "crew":
		s.searchSingleScope(w, r, s.crewMemoryEngine, "crew", req.Query, req.Limit)
	case "both":
		s.searchBothScopes(w, r, req.Query, req.Limit)
	default:
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": "invalid scope: use agent, crew, or both",
		})
	}
}

func (s *Server) searchSingleScope(w http.ResponseWriter, r *http.Request, engine *memory.Engine, scope, query string, limit int) {
	if engine == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{
			"error": scope + " memory engine not available",
		})
		return
	}

	results, err := engine.Search(r.Context(), query, limit)
	if err != nil {
		s.logger.Error("memory search failed", "error", err, "query", query, "scope", scope)
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{
			"error": "search failed: " + err.Error(),
		})
		return
	}

	scoped := make([]scopedResult, len(results))
	for i, r := range results {
		scoped[i] = scopedResult{SearchResult: r, Source: scope}
	}

	writeJSONResponse(w, http.StatusOK, map[string]interface{}{
		"results": scoped,
		"count":   len(scoped),
	})
}

func (s *Server) searchBothScopes(w http.ResponseWriter, r *http.Request, query string, limit int) {
	// At least one engine must be available
	if s.memoryEngine == nil && s.crewMemoryEngine == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{
			"error": "no memory engines available",
		})
		return
	}

	var all []scopedResult
	var searchErrors int

	// Query agent memory
	if s.memoryEngine != nil {
		results, err := s.memoryEngine.Search(r.Context(), query, limit)
		if err != nil {
			s.logger.Warn("agent memory search failed in scope=both", "error", err)
			searchErrors++
		}
		for _, res := range results {
			all = append(all, scopedResult{SearchResult: res, Source: "agent"})
		}
	}

	// Query crew memory
	if s.crewMemoryEngine != nil {
		results, err := s.crewMemoryEngine.Search(r.Context(), query, limit)
		if err != nil {
			s.logger.Warn("crew memory search failed in scope=both", "error", err)
			searchErrors++
		}
		for _, res := range results {
			all = append(all, scopedResult{SearchResult: res, Source: "crew"})
		}
	}

	// If all attempted scopes failed, return error instead of empty 200
	engineCount := 0
	if s.memoryEngine != nil {
		engineCount++
	}
	if s.crewMemoryEngine != nil {
		engineCount++
	}
	if searchErrors > 0 && searchErrors >= engineCount {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{
			"error": "all memory scopes failed to search",
		})
		return
	}

	// Sort by BM25 score (lower = better match in FTS5 rank)
	sort.Slice(all, func(i, j int) bool {
		return all[i].Score < all[j].Score
	})

	// Apply limit
	if len(all) > limit {
		all = all[:limit]
	}

	writeJSONResponse(w, http.StatusOK, map[string]interface{}{
		"results": all,
		"count":   len(all),
	})
}

// handleMemoryStatus handles GET /memory/status[?scope=agent|crew].
func (s *Server) handleMemoryStatus(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "agent"
	}

	engine, valid := s.resolveMemoryEngine(scope)
	if !valid {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": "invalid scope: use agent or crew",
		})
		return
	}
	if engine == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{
			"error": scope + " memory engine not available",
		})
		return
	}

	status, err := engine.Status(r.Context())
	if err != nil {
		s.logger.Error("memory status failed", "error", err, "scope", scope)
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{
			"error": "status check failed",
		})
		return
	}

	writeJSONResponse(w, http.StatusOK, status)
}

// handleMemoryReindex handles POST /memory/reindex[?scope=agent|crew].
func (s *Server) handleMemoryReindex(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "agent"
	}

	engine, valid := s.resolveMemoryEngine(scope)
	if !valid {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": "invalid scope: use agent or crew",
		})
		return
	}
	if engine == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{
			"error": scope + " memory engine not available",
		})
		return
	}

	if err := engine.Reindex(); err != nil {
		s.logger.Error("memory reindex failed", "error", err, "scope", scope)
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{
			"error": "reindex failed: " + err.Error(),
		})
		return
	}

	status, err := engine.Status(r.Context())
	if err != nil {
		writeJSONResponse(w, http.StatusOK, map[string]string{"status": "reindexed"})
		return
	}

	writeJSONResponse(w, http.StatusOK, status)
}
