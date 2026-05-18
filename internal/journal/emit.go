package journal

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
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
// Writer fields. Flush uses an in-queue barrier sentinel (an Entry
// with flushBarrierAck set) rather than a separate channel, so the
// worker naturally processes every prior queue element before
// signalling — no race where Flush could ack while earlier entries
// were still buffered.
type Writer struct {
	db       *sql.DB
	logger   *slog.Logger
	queue    chan Entry
	wg       sync.WaitGroup
	flushN   int
	flushDur time.Duration
	closed   chan struct{}
	closeMu  sync.Mutex
}

// DB exposes the underlying *sql.DB for callers that need to run a
// scoped lookup before emitting (e.g. resolving a workspace from a
// crew_id when the hot path doesn't carry it). Read-only intent —
// callers must not run schema changes through this handle.
func (w *Writer) DB() *sql.DB {
	if w == nil {
		return nil
	}
	return w.db
}

// LookupWorkspaceForCrew returns the workspace_id that owns the given
// crew. Used by emit-side helpers (file watcher, port scanner) that
// only know the crew on the hot path. Empty crewID or missing row
// returns "" and a nil error so callers can treat it as a soft-skip.
func LookupWorkspaceForCrew(ctx context.Context, db *sql.DB, crewID string) (string, error) {
	if db == nil || crewID == "" {
		return "", nil
	}
	var ws sql.NullString
	err := db.QueryRowContext(ctx, `SELECT workspace_id FROM crews WHERE id = ?`, crewID).Scan(&ws)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("lookup workspace for crew %q: %w", crewID, err)
	}
	return ws.String, nil
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
	// Extract trace context if caller didn't set it explicitly. Run-id
	// (set via WithRunID) wins over OTel trace context: a journal entry
	// belonging to an agent run must share trace_id == run.id so the
	// Runs aggregation view (GROUP BY trace_id) finds it. We still let
	// OTel populate the SpanID so span hierarchy survives.
	if e.TraceID == "" {
		if rid := RunIDFromContext(ctx); rid != "" {
			e.TraceID = rid
		}
	}
	if e.TraceID == "" || e.SpanID == "" {
		if t, s, ok := traceFromContext(ctx); ok {
			if e.TraceID == "" {
				e.TraceID = t
			}
			if e.SpanID == "" {
				e.SpanID = s
			}
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

// Flush forces pending entries to disk and waits for the drain to
// complete. The barrier travels through the same entry queue (wrapped
// in an Entry with a special sentinel marker on the run loop), so the
// worker naturally processes every prior entry before it reaches the
// flush marker and closes req.ack. Without the in-queue barrier an
// earlier implementation could close ack after only the current batch
// was drained — entries still buffered in w.queue would still be
// pending and "all emits so far are durable" was a lie.
func (w *Writer) Flush(ctx context.Context) error {
	ack := make(chan struct{})
	barrier := Entry{flushBarrierAck: ack}

	select {
	case w.queue <- barrier:
	case <-ctx.Done():
		return ctx.Err()
	case <-w.closed:
		// Writer already stopped; whatever was queued has either been
		// drained or written inline via persistOne, so nothing to do.
		return nil
	}
	select {
	case <-ack:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close stops the writer goroutine and flushes remaining entries. Safe to
// call multiple times.
// Close signals the worker goroutine to drain and exit. Does NOT close
// w.queue — a concurrent Emit that already passed the `<-w.closed` check
// but hasn't reached `w.queue <- e` would panic with send-on-closed-
// channel. The worker treats `<-w.closed` as "drain and return", and
// anything queued after the close signal is still drainable because
// the channel stays open; any Emit arriving after the signal takes the
// inline persistOne path via the `case <-w.closed:` branch in Emit.
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
	w.wg.Wait()
	return nil
}

func (w *Writer) run() {
	defer w.wg.Done()
	batch := make([]Entry, 0, w.flushN)
	ticker := time.NewTicker(w.flushDur)
	defer ticker.Stop()

	// Exponential backoff for failed persist attempts. The journal is
	// the canonical audit stream — dropping entries on a transient DB
	// error would violate the core contract. Instead we keep the
	// batch and retry next tick, with an upper bound on the batch
	// size so a long DB outage doesn't grow the buffer unboundedly
	// (at that point we start logging and dropping oldest).
	const maxBatchRetryBytes = 64 * 1024 * 1024 // ~64 MB of buffered entries
	var persistFailures int
	drain := func() {
		if len(batch) == 0 {
			return
		}
		if err := w.persistBatch(context.Background(), batch); err != nil {
			persistFailures++
			w.logger.Error("journal batch write failed — retaining batch for retry",
				"err", err, "n", len(batch), "consecutive_failures", persistFailures)
			// Hard cap so a permanently broken DB doesn't OOM the
			// process. 64 MB worth of Entry structs is ~30k rows;
			// far beyond that we start dropping oldest with a loud
			// error so the failure is still observable.
			if estimateBatchBytes(batch) > maxBatchRetryBytes {
				w.logger.Error("journal batch exceeded retry cap — dropping oldest half",
					"n", len(batch))
				batch = batch[len(batch)/2:]
			}
			return
		}
		persistFailures = 0
		batch = batch[:0]
	}

	for {
		select {
		case e, ok := <-w.queue:
			if !ok {
				drain()
				return
			}
			// Flush barrier: drain everything before the barrier
			// (guaranteed durable because the barrier couldn't have
			// landed here until every earlier queue element was
			// consumed), then close the ack so the Flush caller
			// returns. The barrier entry itself is not persisted.
			if e.flushBarrierAck != nil {
				drain()
				close(e.flushBarrierAck)
				continue
			}
			batch = append(batch, e)
			if len(batch) >= w.flushN {
				drain()
			}
		case <-ticker.C:
			drain()
		case <-w.closed:
			// Shutdown signal from Close(). Drain anything already
			// buffered in the channel so Close->Wait doesn't race
			// with in-flight writers, then exit. Inline persistOne
			// handles any new Emit that lands after this point.
			for {
				select {
				case e := <-w.queue:
					batch = append(batch, e)
					if len(batch) >= w.flushN {
						drain()
					}
				default:
					drain()
					return
				}
			}
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
	(id, workspace_id, crew_id, agent_id, mission_id, ts, entry_type, severity, priority,
	 actor_type, actor_id, summary, payload, refs, trace_id, span_id, expires_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

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
			priorityOrNormal(e.Priority),
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

// estimateBatchBytes is a rough size estimate used to cap the retry
// buffer when the DB is unreachable. Not load-bearing for correctness —
// 256 B/entry is a reasonable average for summary + payload — so a
// fixed multiplier is cheaper than marshalling every entry twice.
func estimateBatchBytes(batch []Entry) int {
	total := 0
	for _, e := range batch {
		total += 256 + len(e.Summary)
		for k, v := range e.Payload {
			total += len(k)
			if s, ok := v.(string); ok {
				total += len(s)
			} else {
				total += 32
			}
		}
	}
	return total
}

// nullable turns an empty string into sql.NullString{Valid:false} so the
// row stores NULL instead of the empty string, keeping the indexed
// "agent_id IS NULL" queries cheap.
// priorityOrNormal returns the string form of p, falling back to
// "normal" when the zero value slips through. Keeping the coercion
// centralised means the DB CHECK constraint never sees an empty
// string even if a caller builds an Entry without setting Priority.
func priorityOrNormal(p Priority) string {
	if p == "" {
		return string(PriorityNormal)
	}
	return string(p)
}

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
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return "j_" + hex.EncodeToString(b[:])
}

// runIDCtxKey is a private type used as the ctx.WithValue key so callers
// can't accidentally collide with us. Using an unexported zero-size type
// is the standard Go pattern for context keys.
type runIDCtxKey struct{}

// WithRunID stamps runID on ctx so any subsequent journal.Emit call that
// inherits the same context inherits trace_id == runID. Used to thread
// the run identity through goroutines that emit run-scoped entries (LLM
// calls, exec commands, network egress, etc.) without every Emit call
// site having to set Entry.TraceID manually.
//
// Empty runID is a no-op (returns ctx unchanged) so callers can pass
// through optionally-set values without branching.
//
// Run-id beats OTel trace context in Emit — see the comment there.
func WithRunID(ctx context.Context, runID string) context.Context {
	if runID == "" {
		return ctx
	}
	return context.WithValue(ctx, runIDCtxKey{}, runID)
}

// RunIDFromContext returns the runID stamped on ctx via WithRunID, or
// "" if none was set.
func RunIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v := ctx.Value(runIDCtxKey{})
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// traceFromContext is a thin shim so the journal package doesn't import
// OpenTelemetry directly. The telemetry package registers a resolver at
// startup via SetTraceResolver. If nothing is registered the function
// returns ok=false and the entry records empty trace/span.
//
// Stored as an atomic.Pointer (not a mutex-guarded var) because the
// resolver is read on every Emit but written only once at process
// startup — paying RLock/RUnlock per emit just to read a pointer that
// effectively never changes is wasted work, and a hypothetical late
// SetTraceResolver caller would briefly starve concurrent emits under
// the RWMutex's writer-preference. atomic.Pointer.Load is a single
// atomic read with no blocking.
type traceResolverFunc func(ctx context.Context) (traceID, spanID string, ok bool)

var traceResolver atomic.Pointer[traceResolverFunc]

func SetTraceResolver(fn func(ctx context.Context) (string, string, bool)) {
	if fn == nil {
		traceResolver.Store(nil)
		return
	}
	typed := traceResolverFunc(fn)
	traceResolver.Store(&typed)
}

func traceFromContext(ctx context.Context) (string, string, bool) {
	fn := traceResolver.Load()
	if fn == nil {
		return "", "", false
	}
	return (*fn)(ctx)
}
