package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/crewship-ai/crewship/internal/episodic"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/memory"
)

// WorkspaceMemoryProvider is the read-side hook the hybrid search
// handler uses to resolve a per-workspace memory.Engine for the FTS
// half of the query. Production wires
// *memory.WorkspaceMemoryRegistry, which already implements the
// "lazy-instantiate per workspace" semantics this needs. Tests can
// stub with an in-memory map.
type WorkspaceMemoryProvider interface {
	For(workspaceID string) WorkspaceEngineHolder
}

// WorkspaceEngineHolder narrows the WorkspaceMemory surface to just
// the Engine accessor. Keeps the handler decoupled from the full
// *memory.WorkspaceMemory type.
type WorkspaceEngineHolder interface {
	Engine() *memory.Engine
}

// MemoryHybridSearchHandler serves POST /api/v1/memory/search/hybrid.
// Body: {"query": "...", "limit": N, "scope": "own|crew_shared|workspace"}
//
// Combines the workspace-tier FTS engine + episodic vec+BM25 recall
// via Reciprocal Rank Fusion (k=60). Each hit carries a Source tag
// so the caller can render or weight per-engine. Empty hits +
// nil error when neither engine has matches — same shape the
// sidecar /memory/search uses.
//
// Auth: workspace context required (MEMBER+). The handler anchors
// every query on the caller's workspace, so a cross-workspace
// probe via a foreign workspace_id in the body is impossible.
type MemoryHybridSearchHandler struct {
	db       *sql.DB
	logger   *slog.Logger
	embedder episodic.Embedder
	provider WorkspaceMemoryProvider
	journal  journal.Emitter
}

func NewMemoryHybridSearchHandler(db *sql.DB, logger *slog.Logger) *MemoryHybridSearchHandler {
	return &MemoryHybridSearchHandler{db: db, logger: logger, journal: noopEmitter{}}
}

// SetJournal wires the journal emitter used to record memory.searched
// events on every non-empty search. The consolidator scoring path
// reads these back to populate RecallCount + UniqueQueries
// CandidateMetrics (PRD §8.1 closing). nil maps to the no-op so
// tests stay simple.
func (h *MemoryHybridSearchHandler) SetJournal(j journal.Emitter) {
	if j == nil {
		h.journal = noopEmitter{}
		return
	}
	h.journal = j
}

// SetEmbedder wires the dense-vector half. Optional — handler
// degrades to FTS-only when nil (the same behaviour memory
// .HybridSearch ships).
func (h *MemoryHybridSearchHandler) SetEmbedder(e episodic.Embedder) {
	h.embedder = e
}

// SetWorkspaceProvider wires the FTS half. Optional — handler
// degrades to episodic-only when nil.
func (h *MemoryHybridSearchHandler) SetWorkspaceProvider(p WorkspaceMemoryProvider) {
	h.provider = p
}

// episodicScopeForRequest translates the request's scope STRING to the
// episodic Scope enum, returning ok=false for an unknown value. This must NOT
// go through episodic.ScopeForRole — that maps a ROLE ("LEAD" → crew_shared,
// else own), so feeding it a scope string silently collapsed "crew_shared" to
// ScopeOwn and crew_shared search never hit crew memory (#1049). "" and "own"
// both mean the caller's own memory (episodic recall has no workspace-wide
// scope); "crew_shared" is the crew_id-bound scope.
func episodicScopeForRequest(s string) (episodic.Scope, bool) {
	switch s {
	case "", "own":
		return episodic.ScopeOwn, true
	case "crew_shared":
		return episodic.ScopeCrewShared, true
	default:
		return "", false
	}
}

// Search is the HTTP entry point. Returns 200 with a possibly-empty
// hits array; 400 on malformed body or missing query; 401 on
// missing workspace context. Other failures land 500 with logger
// surfacing the detail (no error string echoed back).
func (h *MemoryHybridSearchHandler) Search(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	user := UserFromContext(r.Context())
	agentID := ""
	if user != nil {
		agentID = user.ID
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
	// Translate the request scope STRING to the episodic Scope enum. This must
	// NOT go through episodic.ScopeForRole — that maps a ROLE ("LEAD" → crew_
	// shared, else own), so feeding it a scope string silently collapsed
	// "crew_shared" to ScopeOwn and the crew_shared feature never actually
	// searched crew memory (#1049). "" and "own" both mean the caller's own
	// memory (episodic recall has no workspace-wide scope); "crew_shared" is the
	// crew_id-bound scope. Any other value is rejected.
	scope, ok := episodicScopeForRequest(body.Scope)
	if !ok {
		replyError(w, http.StatusBadRequest, "invalid scope (allowed: '', own, crew_shared)")
		return
	}

	// crew_shared reads a crew's shared surface, so the caller MUST be a member
	// of the named crew — and that crew must live in the caller's workspace.
	// Without this, any workspace member could pass a SIBLING crew's id and read
	// its shared episodic memory (HybridSearch trusted the body-supplied crew_id
	// after only workspace-scoping), breaking the cross-crew isolation invariant
	// (#1049). Empty crew_id under this scope is meaningless — reject it too.
	if body.Scope == "crew_shared" {
		if body.CrewID == "" {
			replyError(w, http.StatusBadRequest, "crew_id required for scope=crew_shared")
			return
		}
		if user == nil {
			replyError(w, http.StatusForbidden, "not a member of the requested crew")
			return
		}
		var one int
		mErr := h.db.QueryRowContext(r.Context(),
			`SELECT 1 FROM crew_members cm
			   JOIN crews c ON c.id = cm.crew_id
			  WHERE cm.crew_id = ? AND cm.user_id = ? AND c.workspace_id = ? AND c.deleted_at IS NULL`,
			body.CrewID, user.ID, wsID).Scan(&one)
		if errors.Is(mErr, sql.ErrNoRows) {
			// Definitively not a member (or the crew isn't in this workspace).
			replyError(w, http.StatusForbidden, "not a member of the requested crew")
			return
		}
		if mErr != nil {
			// A transient DB error (BUSY, cancelled ctx, pool exhaustion) is NOT
			// an authorization denial — surface it as 500 so retry/backoff keyed
			// on 5xx works and a real 403 stays distinguishable in monitoring.
			h.logger.Error("crew membership check failed", "error", mErr, "workspace_id", wsID, "crew_id", body.CrewID)
			replyError(w, http.StatusInternalServerError, "membership check failed")
			return
		}
	}

	// Resolve workspace's FTS engine. nil is fine — handler downgrades
	// to episodic-only.
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
		h.logger.Error("hybrid search failed", "error", err, "workspace_id", wsID)
		replyError(w, http.StatusInternalServerError, "search failed")
		return
	}

	// Emit memory.searched on every non-empty result. The
	// consolidator scoring path reads these events back to populate
	// RecallCount + UniqueQueries, which gates Skill promotion.
	// hit_chunk_ids carries the journal entry ids from episodic hits
	// — those are the same ids LearnedRule.Evidence references, so
	// the scoring JOIN is direct.
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
			ActorType:   journal.ActorUser,
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
