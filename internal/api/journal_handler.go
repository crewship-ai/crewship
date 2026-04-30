package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// JournalHandler serves the Crew Journal read API: paginated list and
// a Server-Sent Events stream for live timeline updates. Write path is
// exclusively internal via journal.Writer — nothing here accepts POST;
// entries come from backend code emitting scoped events.
type JournalHandler struct {
	db      *sql.DB
	logger  *slog.Logger
	journal journal.Emitter
}

// NewJournalHandler wires a handler around the database. The emitter is
// only used by priority-change audit writes; reads hit the table
// directly. Passing nil is allowed (noop) so existing call sites don't
// break.
func NewJournalHandler(db *sql.DB, logger *slog.Logger, j journal.Emitter) *JournalHandler {
	if j == nil {
		j = noopEmitter{}
	}
	return &JournalHandler{db: db, logger: logger, journal: j}
}

// List serves GET /api/v1/journal. Filters come from query params:
//
//	crew_id, agent_id, mission_id — scope narrows
//	entry_type=a,b,c — CSV of EntryType values
//	severity=a,b — CSV of Severity values
//	since=<RFC3339>, until=<RFC3339>
//	cursor=<opaque> — from a prior page's Next
//	limit=<N> — 1..500, default 100
func (h *JournalHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "workspace required"})
		return
	}

	q, err := parseJournalQuery(r, workspaceID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	entries, next, err := journal.List(r.Context(), h.db, q)
	if err != nil {
		h.logger.Error("journal list failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "journal list failed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"entries":     serializeEntries(entries),
		"next_cursor": next,
		"count":       len(entries),
	})
}

// Stream serves GET /api/v1/journal/stream. Implements Server-Sent Events:
// the client subscribes once and receives new journal entries as they are
// written. Under the hood this polls the journal every 1s for entries
// newer than the last-sent ID. Using the journal table as the source
// rather than tapping the Writer directly keeps the stream recoverable
// across server restarts — if the connection drops, reconnecting with
// Last-Event-ID replays the missed window.
func (h *JournalHandler) Stream(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "workspace required"})
		return
	}
	q, err := parseJournalQuery(r, workspaceID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	q.Limit = 50 // bound the initial batch so a long-idle reconnect doesn't flood

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming not supported"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // nginx: don't buffer

	// Seed with a replay of the most recent N events so a fresh client
	// paints the full current view before switching to live updates.
	entries, _, err := journal.List(r.Context(), h.db, q)
	if err != nil {
		// Don't abort the stream on a transient seed failure — the
		// poll loop below can still carry live traffic once the DB
		// recovers. Log so oncall can see why the client's initial
		// view was empty.
		h.logger.Warn("journal stream seed failed", "err", err)
	} else {
		for _, e := range entries {
			writeSSEEvent(w, "entry", e)
		}
		flusher.Flush()
	}

	// Watermark is the compound (ts, id) of the last emitted entry so a
	// burst of entries sharing a millisecond timestamp isn't partially
	// skipped on the next poll — a timestamp-only watermark would drop
	// every entry with the same ts as the last one we saw. The DB
	// ORDER BY (ts DESC, id DESC) in journal.List guarantees the tie-
	// breaker is deterministic.
	var lastSeenTS, lastSeenID string
	if len(entries) > 0 {
		lastSeenTS = entries[0].TS.UTC().Format(time.RFC3339Nano)
		lastSeenID = entries[0].ID
	} else {
		lastSeenTS = time.Now().UTC().Format(time.RFC3339Nano)
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	// Heartbeat every 15s to keep proxies from closing the connection
	// as idle. SSE comments (": heartbeat\n\n") don't surface to the
	// client handler but keep the TCP connection warm.
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			_, _ = fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		case <-ticker.C:
			poll := q
			poll.Since, _ = time.Parse(time.RFC3339Nano, lastSeenTS)
			poll.Cursor = ""
			poll.Limit = 100
			rows, _, err := journal.List(r.Context(), h.db, poll)
			if err != nil {
				h.logger.Warn("journal stream poll failed", "err", err)
				continue
			}
			// Emit oldest first so the client timeline appends in order.
			for i := len(rows) - 1; i >= 0; i-- {
				e := rows[i]
				ts := e.TS.Format(time.RFC3339Nano)
				// Skip entries the client has already seen, tied by
				// id when ts matches so burst-within-ms doesn't drop
				// rows. id comparison is stable because the journal
				// IDs are time-ordered hex tokens.
				if ts < lastSeenTS || (ts == lastSeenTS && e.ID <= lastSeenID) {
					continue
				}
				writeSSEEvent(w, "entry", e)
				lastSeenTS = e.TS.UTC().Format(time.RFC3339Nano)
				lastSeenID = e.ID
			}
			flusher.Flush()
		}
	}
}

// parseJournalQuery turns URL query params into a journal.Query.
// Returns an error describing the first bad input so the handler can
// respond 400 with a useful message.
func parseJournalQuery(r *http.Request, workspaceID string) (journal.Query, error) {
	qs := r.URL.Query()
	q := journal.Query{
		WorkspaceID: workspaceID,
		CrewID:      qs.Get("crew_id"),
		AgentID:     qs.Get("agent_id"),
		MissionID:   qs.Get("mission_id"),
		Cursor:      qs.Get("cursor"),
	}
	if v := qs.Get("entry_type"); v != "" {
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				q.Types = append(q.Types, journal.EntryType(s))
			}
		}
	}
	if v := qs.Get("severity"); v != "" {
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				q.Severities = append(q.Severities, journal.Severity(s))
			}
		}
	}
	if v := qs.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return q, fmt.Errorf("bad since: %w", err)
		}
		q.Since = t
	}
	if v := qs.Get("until"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return q, fmt.Errorf("bad until: %w", err)
		}
		q.Until = t
	}
	if v := qs.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			return q, fmt.Errorf("limit must be 1..500")
		}
		q.Limit = n
	}
	// Free-text search via FTS5 (?q=…). Trimmed and capped at 200 chars
	// so we don't shovel arbitrary blobs into FTS5; the phrase-quoting
	// in journal.fts5Phrase neutralises operators inside the input.
	if v := qs.Get("q"); v != "" {
		v = strings.TrimSpace(v)
		const maxFTSQuery = 200
		if len(v) > maxFTSQuery {
			v = v[:maxFTSQuery]
		}
		q.FTSQuery = v
	}
	return q, nil
}

// SetPriority serves POST /api/v1/journal/{id}/priority. Body:
//
//	{"priority": "permanent|high|pin|normal", "reason": "..."}
//
// Requires OWNER or ADMIN role. The priority marker feeds the
// consolidator (permanent → immediate rule extraction, pin → snapshot
// to pins.md) and the compactor (permanent → never deleted). Agents
// themselves cannot call this endpoint — it's strictly operator
// curation, so the role gate is load-bearing, not cosmetic.
func (h *JournalHandler) SetPriority(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "workspace required"})
		return
	}
	role := RoleFromContext(r.Context())
	if role != "OWNER" && role != "ADMIN" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "priority edit requires OWNER or ADMIN"})
		return
	}
	entryID := r.PathValue("id")
	if entryID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "entry id required"})
		return
	}

	var body struct {
		Priority string `json:"priority"`
		Reason   string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	prio := journal.Priority(body.Priority)
	if !journal.ValidPriority(prio) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "priority must be one of: normal, high, pin, permanent",
		})
		return
	}

	// Entry must exist in the caller's workspace. Using journal.Get
	// here matches how every other read handler does the cross-tenant
	// check — same 404 shape as "no such entry".
	existing, err := journal.Get(r.Context(), h.db, workspaceID, entryID)
	if err != nil {
		h.logger.Error("journal priority: get", "err", err, "entry_id", entryID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lookup failed"})
		return
	}
	if existing == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "entry not found"})
		return
	}

	// Scoped UPDATE — workspace_id in the WHERE clause is the tenant
	// isolation boundary for writes. Dropping it would let a caller
	// flip a foreign workspace's priority via a stolen ID.
	res, err := h.db.ExecContext(r.Context(),
		`UPDATE journal_entries SET priority = ? WHERE id = ? AND workspace_id = ?`,
		string(prio), entryID, workspaceID)
	if err != nil {
		h.logger.Error("journal priority: update", "err", err, "entry_id", entryID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "update failed"})
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "entry not found"})
		return
	}

	// Durable audit trail. Priority is a load-bearing marker (permanent
	// entries are never compacted; pins land in curated pins.md) so a
	// silent UPDATE would hide who upgraded or downgraded what, and why.
	// The reason body field is captured here — otherwise it was purely
	// echoed back in the response and evaporated.
	actorID := ""
	if u := UserFromContext(r.Context()); u != nil {
		actorID = u.ID
	}
	if _, emitErr := h.journal.Emit(r.Context(), journal.Entry{
		WorkspaceID: workspaceID,
		CrewID:      existing.CrewID,
		AgentID:     existing.AgentID,
		MissionID:   existing.MissionID,
		Type:        "memory.priority_changed",
		ActorType:   journal.ActorUser,
		ActorID:     actorID,
		Severity:    journal.SeverityNotice,
		Summary: fmt.Sprintf("priority: %s → %s on %s",
			existing.Priority, prio, entryID),
		Payload: map[string]any{
			"target_entry_id":   entryID,
			"target_entry_type": string(existing.Type),
			"previous_priority": string(existing.Priority),
			"new_priority":      string(prio),
			"reason":            body.Reason,
		},
		Refs: map[string]any{
			"parent_entry_id": entryID,
		},
	}); emitErr != nil {
		// The UPDATE already happened; audit-emit failure is best-effort.
		h.logger.Warn("journal priority: audit emit failed", "err", emitErr, "entry_id", entryID)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":       entryID,
		"priority": string(prio),
		"reason":   body.Reason,
		"previous": string(existing.Priority),
	})
}

// serializeEntries turns the journal.Entry slice into a JSON-friendly
// shape. The TS field becomes an RFC3339Nano string so the UI can
// parse with the built-in Date constructor.
func serializeEntries(entries []journal.Entry) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		row := map[string]any{
			"id":           e.ID,
			"workspace_id": e.WorkspaceID,
			"ts":           e.TS.UTC().Format(time.RFC3339Nano),
			"entry_type":   string(e.Type),
			"severity":     string(e.Severity),
			"priority":     string(e.Priority),
			"actor_type":   string(e.ActorType),
			"summary":      e.Summary,
		}
		if e.CrewID != "" {
			row["crew_id"] = e.CrewID
		}
		if e.AgentID != "" {
			row["agent_id"] = e.AgentID
		}
		if e.MissionID != "" {
			row["mission_id"] = e.MissionID
		}
		if e.ActorID != "" {
			row["actor_id"] = e.ActorID
		}
		if e.TraceID != "" {
			row["trace_id"] = e.TraceID
		}
		if len(e.Payload) > 0 {
			row["payload"] = e.Payload
		}
		if len(e.Refs) > 0 {
			row["refs"] = e.Refs
		}
		out = append(out, row)
	}
	return out
}

// writeSSEEvent frames one journal entry as an SSE message. Uses the
// entry's ID as the SSE event ID so the client's automatic Last-Event-ID
// handling lets reconnects skip already-seen rows.
func writeSSEEvent(w http.ResponseWriter, eventType string, e journal.Entry) {
	payload := serializeEntries([]journal.Entry{e})[0]
	data, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", e.ID, eventType, data)
}
