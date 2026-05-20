package policy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"
)

// cacheTTL is the per-crew Policy snapshot lifetime. PRD §6 F2
// originally specified 60 seconds; revised down to 10 seconds during
// PR-Z critical review for tighter flip latency on security-sensitive
// flows (operator goes strict → trusted → strict; subsequent writes
// should respect the second flip within seconds, not a minute).
const cacheTTL = 10 * time.Second

// queryRower is the subset of *sql.DB the resolver needs. Lets tests
// inject a counting wrapper without dragging in the full DB interface.
type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Resolver loads a crew's Policy from the DB and caches it per crew
// for cacheTTL. Concurrent Resolve calls for the same crew share the
// fetch via the cache's RWMutex; the DB call itself isn't
// singleflighted because the worst case (two parallel SELECTs on a
// cache miss) is cheap and the lock overhead of singleflight
// outweighs the saving.
type Resolver struct {
	db    queryRower
	now   func() time.Time
	mu    sync.RWMutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	policy    Policy
	expiresAt time.Time
}

// NewResolver builds a Resolver with the real clock.
func NewResolver(db queryRower) *Resolver {
	return NewResolverWithClock(db, time.Now)
}

// NewResolverWithClock lets tests inject a fake clock for TTL
// verification.
func NewResolverWithClock(db queryRower, now func() time.Time) *Resolver {
	return &Resolver{
		db:    db,
		now:   now,
		cache: make(map[string]cacheEntry),
	}
}

// Resolve returns the current Policy for crewID. Returns the
// documented safe default (guided/warn) if the crew row is missing
// — a transient race (crew deleted mid-call) must not crash callers
// that are already mid-decision.
func (r *Resolver) Resolve(ctx context.Context, crewID string) (Policy, error) {
	now := r.now()
	r.mu.RLock()
	if e, ok := r.cache[crewID]; ok && e.expiresAt.After(now) {
		r.mu.RUnlock()
		return e.policy, nil
	}
	r.mu.RUnlock()

	p, err := r.loadFromDB(ctx, crewID)
	if err != nil {
		return Policy{}, err
	}

	r.mu.Lock()
	r.cache[crewID] = cacheEntry{policy: p, expiresAt: now.Add(cacheTTL)}
	r.mu.Unlock()
	return p, nil
}

// Invalidate drops the cached entry for crewID. Called by the API
// PATCH handler after updating policy so the next Resolve fetches
// fresh state instead of waiting for TTL.
func (r *Resolver) Invalidate(crewID string) {
	r.mu.Lock()
	delete(r.cache, crewID)
	r.mu.Unlock()
}

// loadFromDB issues the SELECT. Missing crew returns a safe default
// (guided/warn) rather than ErrNoRows so the caller's hot path
// stays linear.
func (r *Resolver) loadFromDB(ctx context.Context, crewID string) (Policy, error) {
	var (
		lvl, mode, setBy, setAt, reason sql.NullString
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT autonomy_level, behavior_mode,
		       autonomy_set_by_user_id, autonomy_set_at, autonomy_reason
		FROM crews
		WHERE id = ?`,
		crewID,
	).Scan(&lvl, &mode, &setBy, &setAt, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return Policy{
			CrewID:        crewID,
			AutonomyLevel: AutonomyGuided,
			BehaviorMode:  BehaviorWarn,
		}, nil
	}
	if err != nil {
		return Policy{}, fmt.Errorf("policy: load %s: %w", crewID, err)
	}
	p := Policy{
		CrewID:        crewID,
		AutonomyLevel: AutonomyLevel(lvl.String),
		BehaviorMode:  BehaviorMode(mode.String),
		SetByUserID:   setBy.String,
		Reason:        reason.String,
	}
	if setAt.Valid {
		if t, terr := time.Parse(time.RFC3339, setAt.String); terr == nil {
			p.SetAt = t
		}
	}
	// Validate stored data — defense in depth against schema drift
	// or manual SQL hot-patch that bypassed the v98 CHECK constraint.
	if err := p.Validate(); err != nil {
		return Policy{}, err
	}
	return p, nil
}
