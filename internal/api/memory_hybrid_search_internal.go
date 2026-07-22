package api

// Internal (sidecar-facing) surface of the hybrid memory search — #1348.
//
// The sidecar's hybrid forward authenticates with X-Internal-Token, which
// identifies the SIDECAR (one shared identity per crew container), not the
// agent that asked. The public handler (memory_hybrid_search_handler.go)
// derives the own-scope agent from UserFromContext, so an internal-token
// request had no per-agent identity at all and every sibling's scope="own"
// collapsed onto whatever the token resolved to.
//
// SearchInternal instead takes the acting agent from the sidecar-forwarded
// X-Acting-Agent-Slug header — but treats it strictly as a NARROWING of the
// internal token's authority:
//
//   - the internal token must be workspace- or crew-bound (a master token
//     has no binding to resolve the slug inside → 403, never a guess);
//   - the slug must resolve to a live agent in the token's bound workspace,
//     and — for a crew-bound token — in the token's bound crew (403
//     otherwise, never a fallback to a wider identity);
//   - the header is mandatory here: this route only exists for sidecars
//     that forward the acting identity, so an identity-less request is
//     refused rather than served ambiguously. Older sidecars never call
//     this route (they target the public path), so their behaviour is
//     unchanged.
//
// The sidecar derives the header exclusively from its token-resolved acting
// identity (internal/sidecar/memory.go hybridActingSlug); this side re-proves
// the binding because a header from a compromised container is still
// caller-supplied data — it may pick any sibling INSIDE the crew the token is
// already bound to (exactly the authority the shared container already has),
// but nothing beyond it.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/memory"
)

// actingAgentSlugHeader is the IPC header the sidecar's hybrid forward uses
// to carry the acting agent's slug (mirrors the constant of the same name in
// internal/sidecar/memory.go — the two sides of one contract).
const actingAgentSlugHeader = "X-Acting-Agent-Slug"

// SearchInternal serves POST /api/v1/internal/memory/search/hybrid behind
// requireInternal. Body is the same shape the public Search accepts.
func (h *MemoryHybridSearchHandler) SearchInternal(w http.ResponseWriter, r *http.Request) {
	wsID := InternalTokenWorkspaceFromContext(r.Context())
	if wsID == "" {
		// Master-token caller: no workspace binding to resolve the acting
		// agent inside. Refuse rather than widen.
		replyError(w, http.StatusForbidden, "workspace-bound internal token required")
		return
	}
	boundCrew := InternalTokenCrewFromContext(r.Context())

	slug := strings.TrimSpace(r.Header.Get(actingAgentSlugHeader))
	if slug == "" {
		replyError(w, http.StatusForbidden, "acting agent identity required")
		return
	}

	var agentID string
	var agentCrew sql.NullString
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id, crew_id FROM agents WHERE workspace_id = ? AND slug = ? AND deleted_at IS NULL`,
		wsID, slug).Scan(&agentID, &agentCrew)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusForbidden, "acting agent is not in the workspace bound to the internal token")
		return
	}
	if err != nil {
		// Transient DB failure is not an authorization decision — 500 so
		// retry/backoff works and a real 403 stays meaningful in monitoring.
		h.logger.Error("resolve acting agent for internal hybrid search", "error", err, "workspace_id", wsID, "slug", slug)
		replyError(w, http.StatusInternalServerError, "acting agent resolution failed")
		return
	}
	if boundCrew != "" && agentCrew.String != boundCrew {
		replyError(w, http.StatusForbidden, "acting agent is not in the crew bound to the internal token")
		return
	}

	var body struct {
		Query  string `json:"query"`
		Limit  int    `json:"limit"`
		Scope  string `json:"scope"`
		CrewID string `json:"crew_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Query == "" {
		replyError(w, http.StatusBadRequest, "query required")
		return
	}
	if body.Limit <= 0 {
		body.Limit = 10
	}
	scope, ok := episodicScopeForRequest(body.Scope)
	if !ok {
		replyError(w, http.StatusBadRequest, "invalid scope (allowed: '', own, crew_shared)")
		return
	}

	// Crew pinning for a crew-bound token, mirroring
	// assertBoundCrewWorkspaceDB (#1186/#1222): a body crew_id that
	// disagrees with the binding is a sibling-crew forgery (403); an
	// omitted one is filled in so the query and the journal row are
	// attributed to the bound crew instead of running crew-less.
	if boundCrew != "" {
		if body.CrewID == "" {
			body.CrewID = boundCrew
		} else if body.CrewID != boundCrew {
			replyError(w, http.StatusForbidden, "crew does not match the crew bound to the internal token")
			return
		}
	}

	if body.Scope == "crew_shared" {
		if body.CrewID == "" {
			replyError(w, http.StatusBadRequest, "crew_id required for scope=crew_shared")
			return
		}
		// The acting agent must be a member of the requested crew — same
		// invariant the public handler proves via crew_members for human
		// callers (#1049), keyed on agents.crew_id for agents.
		if !agentCrew.Valid || agentCrew.String != body.CrewID {
			replyError(w, http.StatusForbidden, "acting agent is not a member of the requested crew")
			return
		}
	}

	var engine *memory.Engine
	if h.provider != nil {
		if holder := h.provider.For(wsID); holder != nil {
			engine = holder.Engine()
		}
	}

	hits, err := memory.HybridSearch(r.Context(), engine, h.db, h.embedder, memory.HybridQuery{
		WorkspaceID: wsID,
		AgentID:     agentID,
		CrewID:      body.CrewID,
		Scope:       scope,
		Text:        body.Query,
		Limit:       body.Limit,
	})
	if err != nil {
		h.logger.Error("internal hybrid search failed", "error", err, "workspace_id", wsID)
		replyError(w, http.StatusInternalServerError, "search failed")
		return
	}

	// Same memory.searched telemetry as the public handler, attributed to
	// the acting AGENT (the recall-count scoring path keys off AgentID).
	if len(hits) > 0 {
		hitIDs := make([]string, 0, len(hits))
		for _, hit := range hits {
			if hit.Episodic != nil && hit.Episodic.EntryID != "" {
				hitIDs = append(hitIDs, hit.Episodic.EntryID)
			}
		}
		if _, emitErr := h.journal.Emit(r.Context(), journal.Entry{
			WorkspaceID: wsID,
			AgentID:     agentID,
			CrewID:      body.CrewID,
			Type:        journal.EntryMemorySearched,
			ActorType:   journal.ActorAgent,
			Severity:    journal.SeverityInfo,
			Summary:     "memory search returned " + strconv.Itoa(len(hits)) + " hit(s)",
			Payload: map[string]any{
				"query":         body.Query,
				"scope":         string(scope),
				"hit_count":     len(hits),
				"hit_chunk_ids": hitIDs,
			},
		}); emitErr != nil {
			h.logger.Debug("memory.searched emit", "err", emitErr)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"query": body.Query,
		"count": len(hits),
		"hits":  hits,
	})
}
