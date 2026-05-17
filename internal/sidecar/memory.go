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
// Request body: {"query": "...", "limit": 10, "scope": "agent|crew|both",
//
//	"hybrid": false}
//
// scope defaults to "agent" for backward compatibility.
//
// When hybrid=true AND IPC is wired, the call is forwarded to the
// host-side /api/v1/memory/search/hybrid endpoint, which combines the
// workspace FTS engine + episodic vec+BM25 recall via RRF. Sidecar's
// own FTS engines are bypassed on the hybrid path because the
// container-local markdown corpus duplicates what's already in the
// workspace tier (the consolidator writes to both). The agent calling
// hybrid=true expects cross-corpus recall, not just markdown.
//
// When hybrid=true but IPC is not configured, falls back to the
// FTS-only path with a Warning header so the operator can tell the
// degradation happened.
func (s *Server) handleMemorySearch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query  string `json:"query"`
		Limit  int    `json:"limit"`
		Scope  string `json:"scope"`
		Hybrid bool   `json:"hybrid"`
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

	if req.Hybrid && s.ipc != nil {
		s.forwardHybridSearch(w, r, req.Query, req.Limit, req.Scope)
		return
	}
	if req.Hybrid && s.ipc == nil {
		// Surface the degradation so an agent inspecting headers sees
		// "you asked for hybrid but I gave you FTS only" rather than
		// silently masking the missing wiring.
		w.Header().Set("X-Memory-Hybrid-Fallback", "ipc_not_configured")
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

// forwardHybridSearch translates the sidecar's scope vocabulary
// ("agent" / "crew" / "both") to the host hybrid endpoint's scope
// vocabulary ("own" / "crew_shared" / "") and proxies via
// X-Internal-Token. Same 15 s timeout the rest of proxyIPCJSON
// callers use; same auth.
func (s *Server) forwardHybridSearch(w http.ResponseWriter, r *http.Request, query string, limit int, scope string) {
	hostScope := ""
	switch scope {
	case "agent":
		hostScope = "own"
	case "crew":
		hostScope = "crew_shared"
	case "both":
		// "both" has no direct host equivalent — pass empty so the
		// host's ScopeForRole picks ScopeOwn (the default) and the
		// caller's agent-id-from-token does the filtering.
		hostScope = ""
	}
	body, _ := json.Marshal(map[string]any{
		"query":   query,
		"limit":   limit,
		"scope":   hostScope,
		"crew_id": s.ipcCrewID(),
	})
	s.proxyIPCJSON(w, r, "POST", "/api/v1/memory/search/hybrid", "memory hybrid search", body)
}

// ipcCrewID returns the crew id from the IPC config so the hybrid
// query's episodic side scopes correctly. Empty when IPC is unset
// or the field is blank.
func (s *Server) ipcCrewID() string {
	if s == nil || s.ipc == nil {
		return ""
	}
	return s.ipc.CrewID
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
	for i, res := range results {
		scoped[i] = scopedResult{SearchResult: res, Source: scope}
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

	if err := engine.ReindexContext(r.Context()); err != nil {
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
