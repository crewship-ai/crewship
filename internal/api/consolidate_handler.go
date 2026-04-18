package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/journal"
)

// ConsolidateHandler triggers manual memory consolidation runs. The
// scheduled runner keeps ticking every 6h; this handler exists so
// operators can force an immediate pass after curating a crew or
// auditing new rules.
//
// A per-workspace in-flight guard prevents the same workspace from
// queueing a dogpile of runs: the handler rejects the second call with
// 409 until the first goroutine releases the lock. The map lives on
// the handler struct so every workspace has independent concurrency.
type ConsolidateHandler struct {
	db           *sql.DB
	logger       *slog.Logger
	journal      journal.Emitter
	consolidator *consolidate.Consolidator
	memoryRoot   string

	mu      sync.Mutex
	running map[string]struct{} // workspace_id → in-flight
}

func NewConsolidateHandler(db *sql.DB, logger *slog.Logger) *ConsolidateHandler {
	return &ConsolidateHandler{
		db:      db,
		logger:  logger,
		journal: noopEmitter{},
		running: map[string]struct{}{},
	}
}

// SetJournal swaps in the real journal emitter once the Router has one.
func (h *ConsolidateHandler) SetJournal(j journal.Emitter) {
	if j == nil {
		h.journal = noopEmitter{}
		return
	}
	h.journal = j
}

// SetConsolidator wires the shared Consolidator built at server
// startup. The handler doesn't own the consolidator — same instance the
// background runner uses — so both paths share the same summarizer +
// logger configuration.
func (h *ConsolidateHandler) SetConsolidator(c *consolidate.Consolidator) {
	h.consolidator = c
}

// SetMemoryRoot sets the parent directory for learned-*.md files. Must
// match what the background runner uses so manual + scheduled writes
// land in the same place.
func (h *ConsolidateHandler) SetMemoryRoot(root string) {
	h.memoryRoot = root
}

// Run serves POST /api/v1/consolidate/run. Body is optional:
//
//	{"crew_id": "...", "since": "24h"}
//
// Returns 202 immediately and performs the run in the background. The
// caller gets a worker_id so they can correlate the triggering call
// with the journal entry the worker emits.
func (h *ConsolidateHandler) Run(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "workspace required"})
		return
	}
	role := RoleFromContext(r.Context())
	if role != "OWNER" && role != "ADMIN" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "consolidation requires OWNER or ADMIN role"})
		return
	}
	user := UserFromContext(r.Context())
	actorID := ""
	if user != nil {
		actorID = user.ID
	}

	var body struct {
		CrewID string `json:"crew_id"`
		Since  string `json:"since"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
	}

	// Parse since; defaults to 24h if unset / unparseable. Zero or
	// negative values fall back to the default too — a 0h window would
	// skip every crew because MinEntries is unreachable.
	sinceDur := 24 * time.Hour
	if body.Since != "" {
		if d, err := parseSinceDuration(body.Since); err == nil && d > 0 {
			sinceDur = d
		}
	}

	// Validate crew_id if supplied. Cross-workspace reads are
	// blocked by returning a 404 that looks identical to "no such crew"
	// so existence isn't leaked. Soft-deleted crews are treated as
	// "not found" — the workspace-wide path below filters them, so
	// the single-crew path must match or a deleted crew could still
	// get fresh memory artifacts via an explicit ID.
	if body.CrewID != "" && !crewLiveInWorkspace(r.Context(), h.db, body.CrewID, workspaceID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "crew not found"})
		return
	}

	// Fail-fast path: handler was wired without a consolidator instance
	// (dev build, tests, or misconfigured server). We return 503 so
	// operators can tell the feature is off, not "accepted but nothing
	// ever happens".
	if h.consolidator == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "consolidator not configured",
		})
		return
	}

	// If the consolidator has no summarizer the runner would skip
	// every crew silently. Surface that as a 202 with an advisory note
	// so the CLI prints something useful rather than a mystery no-op.
	// We still emit the triggered/completed journal entries so the
	// audit record exists.
	if h.consolidator.Summarizer == nil {
		h.emitTriggered(r.Context(), workspaceID, body.CrewID, actorID, "", "no-summarizer-skip")
		// Emit the matching completed row so operators aren't left with a
		// dangling "triggered" entry whenever summarization is disabled.
		// The journal comment on emitTriggered promises a completed
		// follow-up; honour that on every code path, not just the
		// successful full-run one.
		h.emitCompleted(r.Context(), workspaceID, body.CrewID, "", "skipped-no-summarizer", 0, 0)
		writeJSON(w, http.StatusAccepted, map[string]any{
			"accepted": true,
			"note":     "no summarizer configured, skipping",
		})
		return
	}

	// In-flight guard. Second call for the same workspace while one
	// is running → 409.
	h.mu.Lock()
	if _, ok := h.running[workspaceID]; ok {
		h.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "already running"})
		return
	}
	h.running[workspaceID] = struct{}{}
	h.mu.Unlock()

	workerID := "csd_" + generateCUID()[:12]
	h.emitTriggered(r.Context(), workspaceID, body.CrewID, actorID, workerID, "manual")

	// Kick the run in the background. We don't cancel on request
	// cancel — the caller has already received 202 and the worker
	// should run to completion against its own ledger. Downstream
	// handler tests pass a sync.WaitGroup via t.Cleanup when they
	// need to observe the emit.
	go func() {
		defer func() {
			h.mu.Lock()
			delete(h.running, workspaceID)
			h.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		h.runOnce(ctx, workspaceID, body.CrewID, sinceDur, workerID)
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"triggered": true,
		"worker_id": workerID,
	})
}

// runOnce walks the consolidator across one crew (if crew_id was
// supplied) or every crew in the workspace, then emits one completed
// journal entry summarising the run. Per-crew errors are logged but
// don't abort the loop — matches the background runner's behaviour so
// one crew's LLM outage doesn't stop siblings.
func (h *ConsolidateHandler) runOnce(ctx context.Context, workspaceID, crewID string, since time.Duration, workerID string) {
	type crewInfo struct {
		ID   string
		Slug string
	}

	// Enumerate scope. A single crew_id pins to one row; empty means
	// every crew in the workspace, matching the runner's fan-out but
	// kept local to the workspace caller.
	var crews []crewInfo
	if crewID != "" {
		var slug string
		err := h.db.QueryRowContext(ctx, `SELECT slug FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
			crewID, workspaceID).Scan(&slug)
		if err != nil {
			h.logger.Warn("consolidate run: crew lookup failed", "err", err, "crew_id", crewID)
			h.emitCompleted(ctx, workspaceID, crewID, workerID, "crew-not-found", 0, 0)
			return
		}
		crews = []crewInfo{{ID: crewID, Slug: slug}}
	} else {
		rows, err := h.db.QueryContext(ctx,
			`SELECT id, slug FROM crews WHERE workspace_id = ? AND deleted_at IS NULL`,
			workspaceID)
		if err != nil {
			h.logger.Warn("consolidate run: crew list failed", "err", err)
			h.emitCompleted(ctx, workspaceID, "", workerID, "enumerate-failed", 0, 0)
			return
		}
		for rows.Next() {
			var c crewInfo
			if err := rows.Scan(&c.ID, &c.Slug); err != nil {
				continue
			}
			crews = append(crews, c)
		}
		// Check iteration err BEFORE closing — a driver/context abort
		// mid-scan leaves a partial `crews` slice, and reporting "ok"
		// on an incomplete set would silently under-consolidate.
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			h.logger.Warn("consolidate run: crew list partial", "err", err)
			h.emitCompleted(ctx, workspaceID, "", workerID, "enumerate-partial", 0, 0)
			return
		}
		_ = rows.Close()
	}

	memoryRoot := h.memoryRoot
	if memoryRoot == "" {
		memoryRoot = "/crew/shared/.memory"
	}

	var crewsRun, rulesAppended int
	for _, c := range crews {
		cfg := consolidate.Config{
			WorkspaceID: workspaceID,
			CrewID:      c.ID,
			Since:       since,
			OutputDir:   filepath.Join(memoryRoot, c.Slug, "topics"),
		}
		res, err := h.consolidator.Run(ctx, cfg)
		if err != nil {
			h.logger.Warn("consolidate run: crew failed",
				"err", err, "workspace_id", workspaceID, "crew_id", c.ID)
			continue
		}
		if !res.Skipped {
			crewsRun++
			rulesAppended += res.RulesAppended
		}
	}

	h.emitCompleted(ctx, workspaceID, crewID, workerID, "ok", crewsRun, rulesAppended)
}

func (h *ConsolidateHandler) emitTriggered(ctx context.Context, workspaceID, crewID, actorID, workerID, reason string) {
	_, _ = h.journal.Emit(ctx, journal.Entry{
		WorkspaceID: workspaceID,
		CrewID:      crewID,
		Type:        journal.EntrySystemConsolidationTriggered,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorUser,
		ActorID:     actorID,
		Summary:     "consolidation triggered (" + reason + ")",
		Payload: map[string]any{
			"worker_id": workerID,
			"reason":    reason,
		},
	})
}

// crewLiveInWorkspace is the soft-delete-aware variant of
// crewBelongsToWorkspace. Memory consolidation must not touch crews
// that have been soft-deleted — otherwise a deleted crew still grows
// memory artifacts via explicit ID, leaking stored state across the
// delete boundary. Kept local to this handler; other handlers can
// keep the permissive shared helper until they opt into the same
// stricter semantics.
func crewLiveInWorkspace(ctx context.Context, db *sql.DB, crewID, workspaceID string) bool {
	var n int
	_ = db.QueryRowContext(ctx,
		`SELECT 1 FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		crewID, workspaceID).Scan(&n)
	return n == 1
}

// parseSinceDuration wraps time.ParseDuration with a fallback for "d"
// (days) and "w" (weeks) suffixes, which Go's standard parser rejects
// on purpose. The CLI help text advertises `--since 7d` so accepting
// that verbatim keeps the contract honest. The conversion is exact
// (24h/day, 7*24h/week) — we deliberately ignore DST/calendar quirks
// because journal windows are wall-clock, not calendar-scoped.
func parseSinceDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	last := s[len(s)-1]
	if last == 'd' || last == 'w' {
		numPart := s[:len(s)-1]
		n, err := strconv.Atoi(numPart)
		if err != nil {
			return 0, err
		}
		switch last {
		case 'd':
			return time.Duration(n) * 24 * time.Hour, nil
		case 'w':
			return time.Duration(n) * 7 * 24 * time.Hour, nil
		}
	}
	return time.ParseDuration(s)
}

func (h *ConsolidateHandler) emitCompleted(ctx context.Context, workspaceID, crewID, workerID, status string, crewsRun, rulesAppended int) {
	_, _ = h.journal.Emit(ctx, journal.Entry{
		WorkspaceID: workspaceID,
		CrewID:      crewID,
		Type:        journal.EntrySystemConsolidationCompleted,
		Severity:    journal.SeverityNotice,
		ActorType:   journal.ActorSystem,
		ActorID:     "consolidator",
		Summary:     "consolidation completed (" + status + ")",
		Payload: map[string]any{
			"worker_id":      workerID,
			"status":         status,
			"crews_run":      crewsRun,
			"rules_appended": rulesAppended,
		},
	})
}
