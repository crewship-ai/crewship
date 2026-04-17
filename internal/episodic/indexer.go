package episodic

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// Indexer watches the journal for high-value entries and writes their
// embeddings into journal_embeddings. It is deliberately selective; see
// types.shouldEmbed. Run as a background goroutine per workspace or
// globally — Start returns a cancel func, no external scheduler needed.
type Indexer struct {
	db       *sql.DB
	embedder Embedder
	logger   *slog.Logger
	poll     time.Duration
}

// NewIndexer constructs an indexer. poll is the sleep between sweeps
// looking for new embeddable entries; 30s is a reasonable default — too
// short hammers Ollama, too long leaves a recall blind window during
// active missions.
func NewIndexer(db *sql.DB, embedder Embedder, logger *slog.Logger, poll time.Duration) *Indexer {
	if poll <= 0 {
		poll = 30 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Indexer{db: db, embedder: embedder, logger: logger, poll: poll}
}

// Start runs the sweeper loop until ctx is cancelled. Each tick processes
// up to 64 unindexed embeddable entries and sleeps. The sweep is keyset-
// paginated via indexed_at exists check so the query stays efficient
// regardless of journal_entries size.
func (x *Indexer) Start(ctx context.Context) {
	ticker := time.NewTicker(x.poll)
	defer ticker.Stop()
	// Kick off once immediately so tests and short-lived processes don't
	// have to wait a full interval.
	x.sweepOnce(ctx, 64)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			x.sweepOnce(ctx, 64)
		}
	}
}

// IndexOne embeds a single journal entry right now. Used by hot path
// callers that want the embedding ready before the next recall —
// typically right after writing a summary.generated entry. Swallows
// shouldEmbed-false silently so callers can call it unconditionally.
func (x *Indexer) IndexOne(ctx context.Context, entry journal.Entry) error {
	if !x.qualifies(entry) {
		return nil
	}
	return x.index(ctx, entry)
}

// qualifies decides whether a specific Entry (not just a type) passes
// the selective-embedding filter. Types get a blanket yes/no from
// shouldEmbed; the conversation type is special-cased because only the
// ones that escalated should be indexed.
func (x *Indexer) qualifies(e journal.Entry) bool {
	if shouldEmbed(string(e.Type), string(e.Severity)) {
		return true
	}
	if e.Type == journal.EntryPeerConversation {
		// Hand-tagged by the refs flag, or the state ended in escalation.
		if v, ok := e.Refs["episodic"].(bool); ok && v {
			return true
		}
		if state, ok := e.Payload["state"].(string); ok && state == "escalated" {
			return true
		}
	}
	// Explicit opt-in via refs.episodic=true overrides the default filter
	// so operators can force-index anything.
	if v, ok := e.Refs["episodic"].(bool); ok && v {
		return true
	}
	return false
}

// sweepOnce selects up to batch entries that (a) pass the type filter
// and (b) don't yet have an embeddings row, embeds them, and writes the
// vectors. Ollama failures are logged and the entry is skipped — we'll
// retry on the next sweep.
func (x *Indexer) sweepOnce(ctx context.Context, batch int) {
	rows, err := x.db.QueryContext(ctx, `
		SELECT e.id, e.workspace_id, e.crew_id, e.agent_id, e.entry_type, e.severity,
		       e.summary, e.payload, e.refs, e.ts
		  FROM journal_entries e
		  LEFT JOIN journal_embeddings em ON em.entry_id = e.id
		 WHERE em.entry_id IS NULL
		   AND e.entry_type IN (
		     'peer.escalation', 'peer.conversation', 'summary.generated',
		     'memory.consolidated', 'approval.denied', 'eval.regression_detected',
		     'keeper.decision', 'mission.status_change'
		   )
		 ORDER BY e.ts DESC
		 LIMIT ?`, batch)
	if err != nil {
		x.logger.Warn("episodic: sweep query failed", "err", err)
		return
	}
	defer rows.Close()

	type row struct {
		entry journal.Entry
	}
	var pending []journal.Entry
	for rows.Next() {
		var e journal.Entry
		var payloadStr, refsStr, tsStr, kind, sev string
		var crewID, agentID sql.NullString
		if err := rows.Scan(&e.ID, &e.WorkspaceID, &crewID, &agentID, &kind, &sev,
			&e.Summary, &payloadStr, &refsStr, &tsStr); err != nil {
			x.logger.Warn("episodic: scan row failed", "err", err)
			continue
		}
		e.CrewID = crewID.String
		e.AgentID = agentID.String
		e.Type = journal.EntryType(kind)
		e.Severity = journal.Severity(sev)
		// Parse payload + refs lazily — only the conversation path uses them.
		e.Payload = decodeJSONMap(payloadStr)
		e.Refs = decodeJSONMap(refsStr)
		pending = append(pending, e)
	}
	if err := rows.Err(); err != nil {
		x.logger.Warn("episodic: sweep iteration failed", "err", err)
	}

	for _, e := range pending {
		if !x.qualifies(e) {
			// Insert a tombstone embedding row so we don't reconsider this
			// entry on every sweep — but only if the type is in the coarse
			// filter. An empty vector with dim=0 serves as the marker.
			if _, err := x.db.ExecContext(ctx, `INSERT OR IGNORE INTO journal_embeddings
				(entry_id, workspace_id, crew_id, agent_id, model, dim, vector, indexed_at)
				VALUES (?, ?, ?, ?, '', 0, X'', datetime('now'))`,
				e.ID, e.WorkspaceID, nullable(e.CrewID), nullable(e.AgentID)); err != nil {
				x.logger.Warn("episodic: tombstone insert failed", "err", err, "entry", e.ID)
			}
			continue
		}
		if err := x.index(ctx, e); err != nil {
			x.logger.Warn("episodic: index failed", "err", err, "entry", e.ID)
		}
	}
}

// index runs the embedder and writes the row. Caller must have already
// confirmed qualifies(e) is true.
func (x *Indexer) index(ctx context.Context, e journal.Entry) error {
	text := e.Summary
	// Enrich with payload.question or payload.reason so the embedding
	// actually represents what the event was about, not just the
	// auto-generated summary line.
	if q, ok := e.Payload["question"].(string); ok && q != "" {
		text = text + " :: " + q
	}
	if r, ok := e.Payload["reason"].(string); ok && r != "" {
		text = text + " :: " + r
	}
	vec, err := x.embedder.Embed(ctx, text)
	if err != nil {
		return fmt.Errorf("embed: %w", err)
	}
	_, err = x.db.ExecContext(ctx, `INSERT OR REPLACE INTO journal_embeddings
		(entry_id, workspace_id, crew_id, agent_id, model, dim, vector, indexed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'))`,
		e.ID, e.WorkspaceID, nullable(e.CrewID), nullable(e.AgentID),
		x.embedder.Model(), x.embedder.Dim(), EncodeVector(vec))
	return err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
