package api

import (
	"database/sql"
	"encoding/json"
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
	// Validate raw scope before translation. ScopeForRole would silently
	// coerce unknown values to "" (= no scope filter), which leaks
	// cross-agent results to a caller that just typo'd. Accept only the
	// documented surface — empty (no filter, MEMBER+ implied),
	// "own" (agent-only), "crew_shared" (crew_id-bound).
	switch body.Scope {
	case "", "own", "crew_shared":
		// accepted
	default:
		replyError(w, http.StatusBadRequest, "invalid scope (allowed: '', own, crew_shared)")
		return
	}
	scope := episodic.ScopeForRole(body.Scope)

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
