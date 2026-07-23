// Package leader provides a minimal, DB-backed leader-election lease so a
// single scheduler instance fires each due job when several crewshipd replicas
// run against one database.
//
// # Why
//
// The three scheduling loops (the agent cron scheduler in internal/scheduler,
// the pipeline cron scheduler in internal/pipeline, and the recurring-issue
// dispatcher in internal/api) were written single-instance: two replicas would
// both tick and both fire. Exactly-once until now rested only on the
// per-occurrence idempotency table (pipeline_run_idempotency). Leader election
// is the belt to that suspenders — it stops the redundant work (and the
// racing) before the idempotency chokepoint, and is the pre-condition the P3
// issue #1376 asks for before any multi-replica deploy.
//
// # Mechanism
//
// A single row in scheduler_leader (keyed by scope) records the current
// holder_id and an expires_at. A replica renews the row on an interval; it is
// "leader" only while it holds a non-expired lease. Acquisition is one atomic
// upsert whose ON CONFLICT ... WHERE clause only lets a contender take the row
// when the current lease has expired (or the contender already owns it), so two
// replicas racing a free/expired row cannot both win — SQLite serialises the
// write and the loser's WHERE fails.
//
// # Clocks
//
// Arbitration between replicas uses ONE clock — the database's — read via
// nowFn (SELECT strftime('%s','now')). That is deliberate: comparing each
// replica's local wall clock would let clock skew between hosts double-elect.
// The IsLeader() freshness gate instead uses the local monotonic clock: if
// renewals stop (DB unreachable, process wedged) the gate falls to false after
// one TTL even though the stale DB row lingers until its own expires_at passes
// — both bound the stale window to at most one TTL.
//
// # Intervals
//
// Defaults: TTL 60s, renew every 20s (renew ≈ TTL/3 leaves two missed
// renewals of headroom before the lease lapses). A lone replica always wins
// immediately: Start performs one synchronous acquire before returning, so the
// scheduler's startup tick already sees IsLeader()==true.
package leader

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"log/slog"
	"sync"
	"time"
)

// Default lease scope and timings. See the package doc for the rationale.
const (
	// DefaultScope keys the single lease row shared by all scheduling loops in
	// one process — they rise and fall together, so one lease gates all three.
	DefaultScope = "scheduler"
	// DefaultTTL is how long an acquired lease stays valid without a renewal.
	DefaultTTL = 60 * time.Second
	// DefaultRenewInterval is how often the holder renews. Kept well below the
	// TTL so a couple of missed renewals don't drop leadership.
	DefaultRenewInterval = 20 * time.Second
)

// Gate is the read side the schedulers depend on: "should I fire?". A nil Gate
// means "no election" — always fire — which keeps single-instance behaviour
// unchanged.
type Gate interface {
	IsLeader() bool
}

// Store is the persistence layer for the scheduler_leader lease row. It is
// backed by the same *sql.DB the schedulers already hold.
type Store struct {
	db *sql.DB
	// nowFn returns the authoritative (database) clock as unix seconds.
	// Overridable in tests to fast-forward expiry without sleeping.
	nowFn func(ctx context.Context) (int64, error)
}

// NewStore returns a lease store over db. The scheduler_leader table is created
// by migration v148.
func NewStore(db *sql.DB) *Store {
	s := &Store{db: db}
	s.nowFn = s.dbNow
	return s
}

// dbNow reads the database clock (unix seconds). Using the DB clock — not the
// replica's wall clock — is what makes arbitration skew-proof across hosts.
func (s *Store) dbNow(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT CAST(strftime('%s','now') AS INTEGER)`).Scan(&n)
	return n, err
}

// upsertSQL takes the lease when it is free/expired or already ours, and
// renews it when we hold it. The trailing WHERE guards the DO UPDATE so a
// contender cannot steal a still-valid lease held by someone else. acquired_at
// is preserved across our own renewals (so it reflects when leadership began)
// and reset only when the holder changes.
const upsertSQL = `
INSERT INTO scheduler_leader (scope, holder_id, acquired_at, expires_at, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(scope) DO UPDATE SET
    holder_id   = excluded.holder_id,
    acquired_at = CASE WHEN scheduler_leader.holder_id = excluded.holder_id
                       THEN scheduler_leader.acquired_at ELSE excluded.acquired_at END,
    expires_at  = excluded.expires_at,
    updated_at  = excluded.updated_at
WHERE scheduler_leader.expires_at <= ?
   OR scheduler_leader.holder_id = excluded.holder_id`

// TryAcquire attempts to take or renew the lease for holder. It returns true
// when, after the attempt, holder owns a non-expired lease. Errors surface the
// DB problem; on error the caller must treat itself as NOT leader (fail-closed).
func (s *Store) TryAcquire(ctx context.Context, scope, holder string, ttl time.Duration) (bool, error) {
	now, err := s.nowFn(ctx)
	if err != nil {
		return false, err
	}
	ttlSec := int64(ttl / time.Second)
	if ttlSec <= 0 {
		ttlSec = 1
	}
	expires := now + ttlSec
	if _, err := s.db.ExecContext(ctx, upsertSQL,
		scope, holder, now, expires, now, // INSERT values
		now, // WHERE expires_at <= now
	); err != nil {
		return false, err
	}
	// Read back the current owner under the same authoritative clock.
	var gotHolder string
	var gotExpires int64
	err = s.db.QueryRowContext(ctx,
		`SELECT holder_id, expires_at FROM scheduler_leader WHERE scope = ?`, scope,
	).Scan(&gotHolder, &gotExpires)
	if err != nil {
		return false, err
	}
	return gotHolder == holder && gotExpires > now, nil
}

// Release drops the lease if (and only if) holder still owns it, letting a
// standby take over on the next tick instead of waiting a full TTL. Best-effort.
func (s *Store) Release(ctx context.Context, scope, holder string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM scheduler_leader WHERE scope = ? AND holder_id = ?`, scope, holder)
	return err
}

// Lease is a running leader-election participant: it renews the lease on an
// interval and exposes IsLeader() as the gate the schedulers check.
type Lease struct {
	store    *Store
	scope    string
	holderID string
	ttl      time.Duration
	renew    time.Duration
	logger   *slog.Logger

	// localNow is the freshness clock for the IsLeader() gate — monotonic in
	// production, overridable in tests.
	localNow func() time.Time

	mu            sync.Mutex
	held          bool
	lastConfirmed time.Time

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	stopped   chan struct{}
}

// Option configures a Lease.
type Option func(*Lease)

// WithScope sets the lease scope key (default DefaultScope).
func WithScope(scope string) Option { return func(l *Lease) { l.scope = scope } }

// WithHolderID sets this replica's identity. Empty means a random per-process
// id is generated.
func WithHolderID(id string) Option { return func(l *Lease) { l.holderID = id } }

// WithTTL sets the lease validity window (default DefaultTTL).
func WithTTL(ttl time.Duration) Option { return func(l *Lease) { l.ttl = ttl } }

// WithRenewInterval sets how often the holder renews (default DefaultRenewInterval).
func WithRenewInterval(d time.Duration) Option { return func(l *Lease) { l.renew = d } }

// WithLogger sets the logger (default slog.Default()).
func WithLogger(lg *slog.Logger) Option { return func(l *Lease) { l.logger = lg } }

// New builds a Lease ready to Start. It is safe to Start even for a single
// replica: it will simply hold the lease uncontended.
func New(db *sql.DB, opts ...Option) *Lease {
	l := &Lease{
		store:    NewStore(db),
		scope:    DefaultScope,
		ttl:      DefaultTTL,
		renew:    DefaultRenewInterval,
		logger:   slog.Default(),
		localNow: time.Now,
		stopCh:   make(chan struct{}),
		stopped:  make(chan struct{}),
	}
	for _, o := range opts {
		o(l)
	}
	if l.holderID == "" {
		l.holderID = randomHolderID()
	}
	if l.ttl <= 0 {
		l.ttl = DefaultTTL
	}
	if l.renew <= 0 {
		l.renew = DefaultRenewInterval
	}
	if l.logger == nil {
		l.logger = slog.Default()
	}
	return l
}

// HolderID returns this replica's lease identity.
func (l *Lease) HolderID() string { return l.holderID }

// Start performs one synchronous acquisition (so a lone replica is leader the
// instant Start returns) and then renews on the configured interval until Stop.
// Idempotent.
func (l *Lease) Start(ctx context.Context) {
	l.startOnce.Do(func() {
		l.attempt(ctx)
		go l.run(ctx)
	})
}

// Stop halts renewal, releases the lease if held, and waits for the loop.
// Idempotent.
func (l *Lease) Stop() {
	l.stopOnce.Do(func() {
		close(l.stopCh)
		<-l.stopped
		// Best-effort release so a standby can take over promptly.
		l.mu.Lock()
		held := l.held
		l.mu.Unlock()
		if held {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := l.store.Release(ctx, l.scope, l.holderID); err != nil {
				l.logger.Warn("leader: release on stop", "scope", l.scope, "error", err)
			}
		}
	})
}

func (l *Lease) run(ctx context.Context) {
	defer close(l.stopped)
	t := time.NewTicker(l.renew)
	defer t.Stop()
	for {
		select {
		case <-l.stopCh:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			l.attempt(ctx)
		}
	}
}

// attempt renews/acquires once and records the outcome for the gate. On error
// it does NOT clear held immediately — the local freshness gate lets held lapse
// after one TTL — so a single transient DB blip doesn't cause a leadership
// flap, while a sustained outage still drops leadership within a TTL.
func (l *Lease) attempt(ctx context.Context) {
	held, err := l.store.TryAcquire(ctx, l.scope, l.holderID, l.ttl)
	if err != nil {
		l.logger.Warn("leader: lease attempt failed", "scope", l.scope, "holder", l.holderID, "error", err)
		return
	}
	l.mu.Lock()
	was := l.held
	l.held = held
	if held {
		l.lastConfirmed = l.localNow()
	}
	l.mu.Unlock()
	if held && !was {
		l.logger.Info("leader: acquired lease", "scope", l.scope, "holder", l.holderID)
	} else if !held && was {
		l.logger.Info("leader: lost lease", "scope", l.scope, "holder", l.holderID)
	}
}

// IsLeader reports whether this replica currently holds a fresh lease. It is
// cheap (no DB call) so schedulers can call it on every tick/fire. Freshness is
// measured against the local monotonic clock: a confirmation older than the TTL
// is treated as lost even if attempt() hasn't run since.
func (l *Lease) IsLeader() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.held && l.localNow().Sub(l.lastConfirmed) < l.ttl
}

func randomHolderID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Fall back to a time-derived id; uniqueness across replicas is
		// best-effort, and a collision only costs a redundant renewal race.
		return "replica-" + hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return "replica-" + hex.EncodeToString(b)
}
