package journal

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Emitter is the minimal write surface exposed to the rest of the codebase.
// Call sites depend on this interface so tests can substitute an in-memory
// recorder without touching the DB. The production implementation is
// *Writer below.
type Emitter interface {
	Emit(ctx context.Context, e Entry) (string, error)
	Flush(ctx context.Context) error
}

// Writer is the production Emitter. It buffers entries in a channel and a
// background goroutine drains the channel, batching writes up to flushSize
// rows or flushInterval, whichever comes first. The buffered+batched
// pattern keeps the hot path (agent request, tool call, LLM return) off
// the DB write lock — a single entry enqueue is a channel send plus a
// JSON marshal, which are both microsecond-scale.
//
// If the queue is full (slow DB or bursty caller) the Emit call blocks
// briefly, then falls back to a synchronous write. That fallback preserves
// durability at the cost of tail latency; it's the right trade because
// dropping journal entries silently would undermine the entire
// audit-source-of-truth contract.
// flushReq is a synchronous flush request. The writer goroutine closes ack
// after it has drained the current batch so Flush callers know every Emit
// that happened before their call has hit the DB.
type flushReq struct {
	ack chan struct{}
}

type Writer struct {
	db       *sql.DB
	logger   *slog.Logger
	queue    chan Entry
	done     chan flushReq
	wg       sync.WaitGroup
	flushN   int
	flushDur time.Duration
	closed   chan struct{}
	closeMu  sync.Mutex
}

// WriterOptions tunes the batcher. Zero values pick sensible defaults.
type WriterOptions struct {
	QueueSize     int           // buffered channel capacity (default 1024)
	FlushSize     int           // write when this many pending (default 64)
	FlushInterval time.Duration // write at least this often (default 100ms)
}

// NewWriter builds a Writer bound to db. Callers MUST call Close before
// process shutdown so buffered entries flush; Close is idempotent.
func NewWriter(db *sql.DB, logger *slog.Logger, opts WriterOptions) *Writer {
	if opts.QueueSize <= 0 {
		opts.QueueSize = 1024
	}
	if opts.FlushSize <= 0 {
		opts.FlushSize = 64
	}
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = 100 * time.Millisecond
	}
	if logger == nil {
		logger = slog.Default()
	}
	w := &Writer{
		db:       db,
		logger:   logger,
		queue:    make(chan Entry, opts.QueueSize),
		done:     make(chan flushReq),
		flushN:   opts.FlushSize,
		flushDur: opts.FlushInterval,
		closed:   make(chan struct{}),
	}
	w.wg.Add(1)
	go w.run()
	return w
}

// Emit queues the entry for asynchronous write and returns the generated
// ID. Validation errors come back synchronously. If the queue is saturated
// the write degrades to synchronous so callers still get back-pressure
// rather than silently losing events.
func (w *Writer) Emit(ctx context.Context, e Entry) (string, error) {
	if err := e.Validate(); err != nil {
		return "", err
	}
	if e.ID == "" {
		e.ID = newID()
	}
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}
	// Extract trace context if caller didn't set it explicitly.
	if e.TraceID == "" {
		if t, s, ok := traceFromContext(ctx); ok {
			e.TraceID, e.SpanID = t, s
		}
	}

	select {
	case <-w.closed:
		// Writer is shutting down; persist inline so we don't drop the entry.
		return e.ID, w.persistOne(ctx, e)
	default:
	}

	select {
	case w.queue <- e:
		return e.ID, nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(25 * time.Millisecond):
		// Fall back to a synchronous write so durability trumps latency.
		w.logger.Warn("journal queue saturated, writing synchronously",
			"entry_type", e.Type, "workspace_id", e.WorkspaceID)
		return e.ID, w.persistOne(ctx, e)
	}
}

// Flush forces pending entries to disk and waits for the drain to complete.
// Used by tests and by graceful shutdown paths where "all emits so far are
// durable" is required.
func (w *Writer) Flush(ctx context.Context) error {
	req := flushReq{ack: make(chan struct{})}
	select {
	case w.done <- req:
	case <-ctx.Done():
		return ctx.Err()
	case <-w.closed:
		// Writer already stopped; whatever was queued has either been
		// drained or written inline via persistOne, so nothing to do.
		return nil
	}
	select {
	case <-req.ack:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close stops the writer goroutine and flushes remaining entries. Safe to
// call multiple times.
func (w *Writer) Close() error {
	w.closeMu.Lock()
	select {
	case <-w.closed:
		w.closeMu.Unlock()
		return nil
	default:
		close(w.closed)
	}
	w.closeMu.Unlock()
	close(w.queue)
	w.wg.Wait()
	return nil
}

func (w *Writer) run() {
	defer w.wg.Done()
	batch := make([]Entry, 0, w.flushN)
	ticker := time.NewTicker(w.flushDur)
	defer ticker.Stop()

	drain := func() {
		if len(batch) == 0 {
			return
		}
		if err := w.persistBatch(context.Background(), batch); err != nil {
			w.logger.Error("journal batch write failed", "err", err, "n", len(batch))
		}
		batch = batch[:0]
	}

	for {
		select {
		case e, ok := <-w.queue:
			if !ok {
				drain()
				return
			}
			batch = append(batch, e)
			if len(batch) >= w.flushN {
				drain()
			}
		case <-ticker.C:
			drain()
		case req := <-w.done:
			drain()
			close(req.ack)
		}
	}
}

// persistOne writes a single entry without going through the batch path.
// Used as the fallback for saturated queues and for synchronous callers
// during shutdown.
func (w *Writer) persistOne(ctx context.Context, e Entry) error {
	return w.persistBatch(ctx, []Entry{e})
}

const insertSQL = `INSERT INTO journal_entries
	(id, workspace_id, crew_id, agent_id, mission_id, ts, entry_type, severity,
	 actor_type, actor_id, summary, payload, refs, trace_id, span_id, expires_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

func (w *Writer) persistBatch(ctx context.Context, batch []Entry) error {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("journal: begin tx: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, insertSQL)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("journal: prepare: %w", err)
	}
	defer stmt.Close()

	for _, e := range batch {
		payload, err := e.payloadJSON()
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("journal: marshal payload: %w", err)
		}
		refs, err := e.refsJSON()
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("journal: marshal refs: %w", err)
		}
		var expires sql.NullString
		if e.ExpiresAt != nil {
			expires = sql.NullString{String: e.ExpiresAt.UTC().Format(time.RFC3339Nano), Valid: true}
		}
		_, err = stmt.ExecContext(ctx,
			e.ID,
			e.WorkspaceID,
			nullable(e.CrewID),
			nullable(e.AgentID),
			nullable(e.MissionID),
			e.TS.UTC().Format("2006-01-02T15:04:05.000Z"),
			string(e.Type),
			string(e.Severity),
			string(e.ActorType),
			nullable(e.ActorID),
			e.Summary,
			payload,
			refs,
			nullable(e.TraceID),
			nullable(e.SpanID),
			expires,
		)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("journal: insert %s: %w", e.Type, err)
		}
	}
	return tx.Commit()
}

// nullable turns an empty string into sql.NullString{Valid:false} so the
// row stores NULL instead of the empty string, keeping the indexed
// "agent_id IS NULL" queries cheap.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// newID generates a short random identifier for journal entries. Not a
// UUID v7: we just need collision-free within a workspace and small enough
// to not bloat indexes. 16 random hex chars (64 bits) gives 2^32 entries
// before birthday collision probability hits 1e-9.
func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "j_" + hex.EncodeToString(b[:])
}

// traceFromContext is a thin shim so the journal package doesn't import
// OpenTelemetry directly. The telemetry package registers a resolver at
// startup via SetTraceResolver. If nothing is registered the function
// returns ok=false and the entry records empty trace/span.
var (
	traceResolver   func(ctx context.Context) (traceID, spanID string, ok bool)
	traceResolverMu sync.RWMutex
)

func SetTraceResolver(fn func(ctx context.Context) (string, string, bool)) {
	traceResolverMu.Lock()
	traceResolver = fn
	traceResolverMu.Unlock()
}

func traceFromContext(ctx context.Context) (string, string, bool) {
	traceResolverMu.RLock()
	fn := traceResolver
	traceResolverMu.RUnlock()
	if fn == nil {
		return "", "", false
	}
	return fn(ctx)
}
