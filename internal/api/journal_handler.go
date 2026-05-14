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
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}

	q, err := parseJournalQuery(r, workspaceID)
	if err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}

	entries, next, err := journal.List(r.Context(), h.db, q)
	if err != nil {
		h.logger.Error("journal list failed", "err", err)
		replyError(w, http.StatusInternalServerError, "journal list failed")
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
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	q, err := parseJournalQuery(r, workspaceID)
	if err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}
	// `q.Limit` is left at the default; the seed loop and poll loop
	// each pick their own batch size below (seedPageSize for the
	// resume gap walk, 100 for live polls).

	flusher, ok := w.(http.Flusher)
	if !ok {
		replyError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // nginx: don't buffer

	// Last-Event-ID resume: SSE clients (browsers and our CLI) send the
	// last successfully-seen entry id back when they reconnect after a
	// drop. Honour it by looking the entry up so we know its ts, then
	// seed every row strictly newer than that watermark — paging if a
	// long disconnect produced more than one batch's worth of gap. If
	// lookup fails (id from another tenant, expired row, malformed)
	// fall through to the regular full seed so the stream still
	// produces useful output rather than silently truncating.
	resumeID := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	// Watermark uses time.Time + id pair throughout. A prior version
	// kept the timestamp as an RFC3339Nano string and compared
	// lexically; that breaks when one side strips trailing-zero
	// fractional seconds (`Z` instead of `.000Z`) because the formats
	// then sort against each other inconsistently. time.Before/After
	// compares on the underlying instant regardless of how it was
	// originally serialised.
	var (
		lastSeenTime time.Time
		lastSeenID   string
		hasWatermark bool
	)
	if resumeID != "" {
		if resumed, err := journal.Get(r.Context(), h.db, workspaceID, resumeID); err == nil && resumed != nil {
			lastSeenTime = resumed.TS.UTC()
			lastSeenID = resumed.ID
			hasWatermark = true
		} else if err != nil {
			h.logger.Warn("journal stream resume lookup failed", "err", err, "resume_id", resumeID)
		}
	}

	// Seed with a replay of the most recent N events so a fresh client
	// paints the full current view before switching to live updates.
	// On a Last-Event-ID resume the client already has the older
	// history, so the seed is the gap between resumeID and now —
	// paged so a disconnect that missed >seedPageSize entries doesn't
	// drop the older end of the gap on the floor.
	//
	// Paging walks newest-first via the keyset Cursor (advancing the
	// watermark via Since would land us back on the newest page every
	// iteration because List is ordered DESC). Pages are collected
	// then emitted in chronological order so the client timeline
	// appends in the right direction across page boundaries.
	const seedPageSize = 50
	const maxSeedPages = 10 // 500-entry replay ceiling per resume
	seedQuery := q
	if hasWatermark {
		seedQuery.Since = lastSeenTime
	}
	seedQuery.Limit = seedPageSize
	seedQuery.Cursor = ""
	// Fresh clients (no Last-Event-ID) get a single seedPageSize
	// batch — same as the pre-paging contract, so a brand-new tab
	// doesn't flood with up-to-500 historical rows. Resume clients
	// page through the gap up to maxSeedPages.
	pageBudget := 1
	if hasWatermark {
		pageBudget = maxSeedPages
	}
	collected := make([]journal.Entry, 0, seedPageSize)
	hitCeiling := false
	for page := 0; page < pageBudget; page++ {
		entries, nextCursor, err := journal.List(r.Context(), h.db, seedQuery)
		if err != nil {
			// Don't abort the stream on a transient seed failure —
			// the poll loop below can still carry live traffic once
			// the DB recovers.
			h.logger.Warn("journal stream seed failed", "err", err, "page", page)
			break
		}
		collected = append(collected, entries...)
		// nextCursor is empty when journal.List returned fewer than
		// the limit (last page). No more pages to walk.
		if nextCursor == "" {
			break
		}
		seedQuery.Cursor = nextCursor
		// Last iteration that didn't break early — there are still
		// older entries beyond the cap. Only treated as a ceiling
		// hit when the caller asked for the full resume budget;
		// fresh clients deliberately stop after one page.
		if hasWatermark && page == pageBudget-1 {
			hitCeiling = true
		}
	}
	if hitCeiling {
		// 500 rows is a deliberate ceiling on resume replay so a
		// week-long disconnect doesn't synchronously stream a million
		// rows on reconnect. Log at warn so operators can spot
		// chronic over-the-cap clients (likely indicates a stuck poll
		// loop or a UI bug holding a stale Last-Event-ID).
		h.logger.Warn("journal stream seed hit replay ceiling — older gap entries truncated",
			"workspace_id", workspaceID,
			"resume_id", resumeID,
			"max_rows", seedPageSize*maxSeedPages)
	}
	// `collected` is newest-first across all pages. Emit reversed so
	// the client sees them in chronological order. Filter out the
	// resume id itself plus anything strictly older — the >= bound in
	// `Since` is inclusive of the watermark instant, so the resume
	// row would otherwise echo back to the client.
	for i := len(collected) - 1; i >= 0; i-- {
		e := collected[i]
		ts := e.TS.UTC()
		if hasWatermark {
			if ts.Before(lastSeenTime) {
				continue
			}
			if ts.Equal(lastSeenTime) && e.ID <= lastSeenID {
				continue
			}
		}
		writeSSEEvent(w, "entry", e)
		lastSeenTime = ts
		lastSeenID = e.ID
		hasWatermark = true
	}
	flusher.Flush()

	// Watermark is the compound (ts, id) of the last emitted entry so a
	// burst of entries sharing a millisecond timestamp isn't partially
	// skipped on the next poll — a timestamp-only watermark would drop
	// every entry with the same ts as the last one we saw. The DB
	// ORDER BY (ts DESC, id DESC) in journal.List guarantees the tie-
	// breaker is deterministic.
	if !hasWatermark {
		// Brand-new client (no Last-Event-ID) and the seed slice was
		// empty — start the live tail from "now" so the next poll
		// produces fresh entries rather than re-emitting history.
		lastSeenTime = time.Now().UTC()
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
			poll.Since = lastSeenTime
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
				ts := e.TS.UTC()
				// Skip entries the client has already seen, tied by
				// id when ts matches so burst-within-ms doesn't drop
				// rows. id comparison is stable because the journal
				// IDs are time-ordered hex tokens. Comparing on
				// time.Time directly avoids the variable-width
				// RFC3339Nano string sort (no fractional vs. .000
				// quirks).
				if ts.Before(lastSeenTime) || (ts.Equal(lastSeenTime) && e.ID <= lastSeenID) {
					continue
				}
				writeSSEEvent(w, "entry", e)
				lastSeenTime = ts
				lastSeenID = e.ID
			}
			flusher.Flush()
		}
	}
}

// parseJournalQuery turns URL query params into a journal.Query.
// Returns an error describing the first bad input so the handler can
// respond 400 with a useful message.
//
// Supported params (all optional, all CSV where multi-valued):
//
//	crew_id           single crew filter
//	agent_id          single agent filter
//	crew_ids          multi-value crew filter — CSV, takes precedence over crew_id
//	agent_ids         multi-value agent filter — CSV, takes precedence over agent_id
//	mission_id        narrow to a single mission
//	trace_id          narrow to a single run (trace_id == run_id)
//	entry_type        CSV of EntryType values to include
//	exclude_entry_type CSV of EntryType values to exclude (NOT IN)
//	severity          CSV of Severity values
//	actor_type        CSV of ActorType values (agent|user|system|keeper|sidecar|orchestrator)
//	priority          CSV of Priority values (normal|high|pin|permanent)
//	since, until      RFC3339 bounds
//	cursor, limit     pagination
//	q                 free-text search (FTS5)
func parseJournalQuery(r *http.Request, workspaceID string) (journal.Query, error) {
	qs := r.URL.Query()
	q := journal.Query{
		WorkspaceID: workspaceID,
		CrewID:      qs.Get("crew_id"),
		AgentID:     qs.Get("agent_id"),
		MissionID:   qs.Get("mission_id"),
		TraceID:     qs.Get("trace_id"),
		Cursor:      qs.Get("cursor"),
	}
	if v := qs.Get("crew_ids"); v != "" {
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				q.CrewIDs = append(q.CrewIDs, s)
			}
		}
	}
	if v := qs.Get("agent_ids"); v != "" {
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				q.AgentIDs = append(q.AgentIDs, s)
			}
		}
	}
	if v := qs.Get("entry_type"); v != "" {
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				q.Types = append(q.Types, journal.EntryType(s))
			}
		}
	}
	if v := qs.Get("exclude_entry_type"); v != "" {
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				q.ExcludeTypes = append(q.ExcludeTypes, journal.EntryType(s))
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
	if v := qs.Get("actor_type"); v != "" {
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				q.ActorTypes = append(q.ActorTypes, journal.ActorType(s))
			}
		}
	}
	if v := qs.Get("priority"); v != "" {
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				p := journal.Priority(s)
				if !journal.ValidPriority(p) {
					return q, fmt.Errorf("priority must be one of normal|high|pin|permanent (got %q)", s)
				}
				q.Priorities = append(q.Priorities, p)
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
	// Free-text search via FTS5 (?q=…). Trimmed and rejected with 400
	// when over 200 chars rather than silently truncated — silent
	// truncation can drop the meaningful tail of the query and surprise
	// the caller with seemingly unrelated matches. The phrase-quoting
	// in journal.fts5Phrase neutralises operators inside the input.
	if v := qs.Get("q"); v != "" {
		v = strings.TrimSpace(v)
		const maxFTSQuery = 200
		if len(v) > maxFTSQuery {
			return q, fmt.Errorf("q parameter exceeds %d characters", maxFTSQuery)
		}
		q.FTSQuery = v
	}
	return q, nil
}

// Get serves GET /api/v1/journal/{id}. Returns a single entry,
// scoped to the caller's workspace. Cross-tenant IDs return 404 with
// the same shape as "not found" so existence is not leaked across
// workspace boundaries — same contract every other read handler in
// this package follows.
func (h *JournalHandler) Get(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	entryID := r.PathValue("id")
	if entryID == "" {
		replyError(w, http.StatusBadRequest, "entry id required")
		return
	}

	entry, err := journal.Get(r.Context(), h.db, workspaceID, entryID)
	if err != nil {
		h.logger.Error("journal get failed", "err", err, "entry_id", entryID)
		replyError(w, http.StatusInternalServerError, "journal get failed")
		return
	}
	if entry == nil {
		replyError(w, http.StatusNotFound, "entry not found")
		return
	}
	// Reuse the list serializer so single-entry shape stays identical to
	// the array form a client just paged through. The slice form is the
	// only one that survived as the canonical serialiser.
	writeJSON(w, http.StatusOK, serializeEntries([]journal.Entry{*entry})[0])
}

// Count serves GET /api/v1/journal/count. Returns the total number of
// entries matching the same query parameters as List, ignoring cursor
// and limit. The UI uses this to render result-set badges that stay
// honest under filter changes — without it the only way to know the
// total was to page through every entry.
func (h *JournalHandler) Count(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	// Strip pagination params from the request URL before parsing so a
	// stray `?limit=999` or malformed `?cursor=…` doesn't 400 a count
	// request that has no use for either. parseJournalQuery validates
	// `limit` against 1..500 and decodes `cursor` strictly — neither
	// bound is meaningful when the answer is a single integer over the
	// full result set.
	stripped := r.Clone(r.Context())
	if rawQ := stripped.URL.Query(); rawQ.Has("limit") || rawQ.Has("cursor") {
		rawQ.Del("limit")
		rawQ.Del("cursor")
		// URL.Query() returns a copy; re-encode onto a clone of the URL
		// so the rest of the handler stack still sees the unmodified
		// request if anything were to read it later.
		newURL := *stripped.URL
		newURL.RawQuery = rawQ.Encode()
		stripped.URL = &newURL
	}

	q, err := parseJournalQuery(stripped, workspaceID)
	if err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}

	n, err := journal.Count(r.Context(), h.db, q)
	if err != nil {
		h.logger.Error("journal count failed", "err", err)
		replyError(w, http.StatusInternalServerError, "journal count failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": n})
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
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	role := RoleFromContext(r.Context())
	if role != "OWNER" && role != "ADMIN" {
		replyError(w, http.StatusForbidden, "priority edit requires OWNER or ADMIN")
		return
	}
	entryID := r.PathValue("id")
	if entryID == "" {
		replyError(w, http.StatusBadRequest, "entry id required")
		return
	}

	var body struct {
		Priority string `json:"priority"`
		Reason   string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
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
		replyError(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if existing == nil {
		replyError(w, http.StatusNotFound, "entry not found")
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
		replyError(w, http.StatusInternalServerError, "update failed")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		replyError(w, http.StatusNotFound, "entry not found")
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
