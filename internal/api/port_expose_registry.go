package api

import (
	"context"
	"database/sql"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// ExposeEntry is the denormalised subset of a port_exposures row that the
// reverse proxy needs on every request. Stored in-memory so the proxy can
// forward without a SQL round-trip per request.
//
// ContainerID is kept alongside ContainerIP because crew containers can
// restart (Docker auto-restart, manual rm+recreate) and pick up a different
// bridge IP. The proxy re-resolves IP from ContainerID on every request so
// stale cached IPs surface as 502 against the right container id rather than
// silently forwarding to whoever got the old IP.
type ExposeEntry struct {
	ID string
	// Token is the CLEARTEXT capability token. It is set by the create
	// path, which knows it because it just minted it, and is empty on any
	// entry rehydrated from the database — post-#1888 the cleartext is not
	// stored, so it cannot come back. Nothing reads it after Add; the map
	// is keyed by TokenHash.
	Token string
	// TokenHash is the at-rest digest and the registry's lookup key.
	TokenHash     string
	ContainerID   string
	ContainerIP   string
	ContainerPort int
	ExpiresAt     time.Time
}

// Target returns the URL of the destination container this entry points at.
func (e *ExposeEntry) Target() *url.URL {
	return &url.URL{
		Scheme: "http",
		Host:   e.ContainerIP + ":" + strconv.Itoa(e.ContainerPort),
	}
}

// Expired reports whether the entry's ExpiresAt is in the past relative to now.
// Useful for the proxy fast-path so we reject with 410 before proxying.
func (e *ExposeEntry) Expired(now time.Time) bool {
	return now.After(e.ExpiresAt)
}

// PortExposeRegistry holds the live set of token → container endpoint mappings
// the reverse proxy serves. The registry is the authoritative runtime lookup;
// the port_exposures table is the durable backing store. On crewshipd startup
// the registry is rehydrated from any ACTIVE, non-expired rows via LoadFromDB
// so in-flight exposures survive a daemon restart.
//
// Thread safety: all public methods take the mutex. Lookup is the hot path
// (called once per inbound HTTP request on /exposed/) and uses RLock.
type PortExposeRegistry struct {
	mu      sync.RWMutex
	entries map[string]*ExposeEntry // keyed by token

	db     *sql.DB
	logger *slog.Logger

	// stop is closed when Shutdown is called. The purge goroutine exits when
	// it drains. Shutdown is idempotent via the once guard.
	stop chan struct{}
	once sync.Once

	// ensureColumn guards the one-time token_hash schema check (#1888).
	ensureColumn sync.Once

	// purgeBatch / purgeMaxIters bound one purgeOnce tick. Set from the
	// constants below by NewPortExposeRegistry; tests override them to make
	// the batching observable without a driver mock. Not mutex-guarded:
	// written once at construction (or by a test before StartPurger) and
	// read-only thereafter.
	purgeBatch    int
	purgeMaxIters int
}

// Bounds on one purgeOnce tick. See the long note on purgeOnce for why this
// particular sweeper is chunked and why that is NOT a pattern to copy.
const (
	// portExposePurgeBatchSize is the maximum number of rows a single purge
	// UPDATE may touch. Measured against the real schema (the composite
	// index idx_port_exposures_expires makes the row selection cheap, so the
	// cost is the write itself): 5,000 rows held SQLite's single writer lock
	// for 41ms, 50,000 rows for 486ms — call it ~9µs of exclusive lock per
	// row. 500 rows therefore lands at ~4-5ms, single-digit milliseconds
	// regardless of how deep the backlog is. The same measurement showed the
	// worst concurrent live write blocked for 25ms at a 5k backlog and 449ms
	// at 50k; that stall is what took down logins on 2026-05-25 (see the
	// busy_timeout note in internal/database/database.go).
	portExposePurgeBatchSize = 500

	// portExposePurgeMaxIterations caps how many batches one tick may run,
	// so a pathological backlog can never monopolise the writer lock
	// indefinitely. 200 × 500 = 100,000 rows per tick, double the worst
	// backlog ever measured (50k), so in practice the cap never fires and
	// the backlog always drains within a single tick. Even at the cap the
	// cumulative exclusive-lock time is ~1s spread across a 30s tick with
	// the lock fully released between every batch — roughly a 3% duty cycle,
	// versus the 486ms unbroken stall the unbounded statement produced.
	// When the cap does fire we log the remaining backlog: no silent caps.
	portExposePurgeMaxIterations = 200
)

// NewPortExposeRegistry builds an empty registry. Call LoadFromDB after
// construction to populate it from durable state, then StartPurger to enable
// automatic expiry.
func NewPortExposeRegistry(db *sql.DB, logger *slog.Logger) *PortExposeRegistry {
	if logger == nil {
		logger = slog.Default()
	}
	return &PortExposeRegistry{
		entries:       make(map[string]*ExposeEntry),
		db:            db,
		logger:        logger,
		stop:          make(chan struct{}),
		purgeBatch:    portExposePurgeBatchSize,
		purgeMaxIters: portExposePurgeMaxIterations,
	}
}

// Add inserts or overwrites an entry for the given token. Called from the
// request handler once a new port_exposures row has been committed to the DB.
//
// Add is also the choke point that takes the cleartext token out of the
// database (#1888). The create path INSERTs the row with the cleartext it
// minted; Add immediately replaces that column with a dead marker and writes
// the digest, so the plaintext exists on disk only for the remainder of the
// same request rather than for the exposure's whole TTL.
func (r *PortExposeRegistry) Add(entry *ExposeEntry) {
	if entry == nil {
		return
	}
	if entry.TokenHash == "" {
		if entry.Token == "" {
			return
		}
		entry.TokenHash = pipeline.HashCapabilityToken(entry.Token)
	}
	r.persistTokenHash(entry.ID, entry.TokenHash)
	r.mu.Lock()
	r.entries[entry.TokenHash] = entry
	r.mu.Unlock()
}

// persistTokenHash records the digest on the row and overwrites the cleartext.
// Best-effort and logged: the in-memory registry is the authoritative runtime
// lookup, so a failure here degrades to "the row gets hashed by the backfill
// at next boot" rather than to a broken exposure.
//
// The column is NOT NULL UNIQUE, so an empty string is not available for more
// than one row; the marker is derived from the primary key, which keeps it
// unique, obviously dead, and traceable to the row it belonged to.
func (r *PortExposeRegistry) persistTokenHash(id, digest string) {
	if digest == "" || r.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Runs before the id check on purpose: the revoke path reads token_hash
	// back, so the column has to exist even when this particular entry has
	// no row to update.
	r.ensureTokenHashColumn(ctx)
	if id == "" {
		return
	}
	if _, err := r.db.ExecContext(ctx,
		`UPDATE port_exposures SET token_hash = ?, token = ? WHERE id = ?`,
		digest, redactedExposeToken(id), id,
	); err != nil {
		r.logger.Warn("port expose registry: persist token hash", "id", id, "error", err)
	}
}

// redactedExposeToken is what replaces the cleartext in port_exposures.token.
func redactedExposeToken(id string) string { return "redacted:" + id }

// Lookup returns the entry for the presented cleartext token and whether it
// was found. The caller is responsible for the expiry check (registry can't
// enforce it without a clock dependency and the proxy already has one anyway).
//
// The token is hashed before the map is consulted, so the at-rest digest is
// not itself a usable capability: pipeline.CapabilityTokenDigests refuses a
// value that already carries a scheme prefix, and hashing it again would land
// on a key nothing holds.
func (r *PortExposeRegistry) Lookup(token string) (*ExposeEntry, bool) {
	key := exposeLookupKey(token)
	if key == "" {
		return nil, false
	}
	r.mu.RLock()
	e, ok := r.entries[key]
	r.mu.RUnlock()
	return e, ok
}

// exposeLookupKey maps a presented cleartext token to the registry key, or ""
// when the value cannot be a capability token at all.
func exposeLookupKey(token string) string {
	if token == "" || pipeline.IsCapabilityTokenDigest(token) {
		return ""
	}
	return pipeline.HashCapabilityToken(token)
}

// Remove deletes the entry for the presented cleartext token. Called after the
// purger transitions a row to EXPIRED in the DB.
func (r *PortExposeRegistry) Remove(token string) {
	if key := exposeLookupKey(token); key != "" {
		r.RemoveByHash(key)
	}
}

// RemoveByHash deletes the entry keyed by an at-rest digest. This is what the
// revoke path uses: the row's cleartext is no longer readable, so the only
// handle a caller holding an exposure id can get is the digest.
func (r *PortExposeRegistry) RemoveByHash(tokenHash string) {
	if tokenHash == "" {
		return
	}
	r.mu.Lock()
	delete(r.entries, tokenHash)
	r.mu.Unlock()
}

// UpdateIP swaps the cached container IP for the entry at token. Called by
// the proxy when it re-resolves the container's address and finds it has
// moved (container restart / recreate). A no-op if the token is unknown so
// the proxy can call this without racing the purger.
func (r *PortExposeRegistry) UpdateIP(token, newIP string) {
	key := exposeLookupKey(token)
	if key == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[key]; ok {
		e.ContainerIP = newIP
	}
}

// HasContainer reports whether any unexpired entry points at the container.
// The idle-crew reaper (#1662) calls it before stopping a crew container: a
// stop kills the process the agent exposed the port from, so the capability
// URL would 502 for the rest of its TTL with nothing to restart it.
//
// This is a map scan rather than a hold taken at expose time on purpose. An
// exposure outlives the run that created it and, because the registry
// rehydrates from port_exposures at boot (LoadFromDB), it outlives the
// process too — a hold would be dropped by the restart the exposure survives.
func (r *PortExposeRegistry) HasContainer(containerID string) bool {
	if containerID == "" {
		return false
	}
	now := time.Now().UTC()
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, e := range r.entries {
		if e.ContainerID == containerID && !e.Expired(now) {
			return true
		}
	}
	return false
}

// Len returns the current number of live entries. Only used in tests + status
// dumps; acquires RLock.
func (r *PortExposeRegistry) Len() int {
	r.mu.RLock()
	n := len(r.entries)
	r.mu.RUnlock()
	return n
}

// LoadFromDB rehydrates the registry from durable state. Called once at
// crewshipd startup. Rows that are already past their expiry are skipped and
// flipped to EXPIRED in the DB so the invariant "ACTIVE ⇒ in-memory" holds.
func (r *PortExposeRegistry) LoadFromDB(ctx context.Context) error {
	now := time.Now().UTC()

	// Complete the 20260810171000 migration before reading anything. The
	// .sql file adds token_hash and its index; the digest itself has to be
	// computed in Go (SQLite has no HMAC), and it has to happen before the
	// proxy serves a request — this is the one boot-time hook the registry
	// has. Idempotent, and normally touches zero rows.
	r.backfillTokenHashes(ctx)

	rows, err := r.db.QueryContext(ctx, `
		SELECT id, COALESCE(token_hash, ''), container_id, container_ip, container_port, expires_at
		FROM port_exposures
		WHERE status = 'ACTIVE'
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var staleIDs []string
	loaded := 0
	for rows.Next() {
		var id, tokenHash, containerID, ip, expiresStr string
		var port int
		if err := rows.Scan(&id, &tokenHash, &containerID, &ip, &port, &expiresStr); err != nil {
			r.logger.Warn("port expose registry: scan row", "error", err)
			continue
		}
		expires, perr := time.Parse(time.RFC3339, expiresStr)
		if perr != nil {
			r.logger.Warn("port expose registry: parse expires_at", "id", id, "value", expiresStr, "error", perr)
			continue
		}
		if !now.Before(expires) {
			staleIDs = append(staleIDs, id)
			continue
		}
		if tokenHash == "" {
			// The backfill above could not hash this row (its cleartext
			// was empty, or the UPDATE failed and was logged). Loading it
			// under an empty key would make every unknown token match it.
			r.logger.Warn("port expose registry: active row has no token hash; skipping", "id", id)
			continue
		}
		r.entries[tokenHash] = &ExposeEntry{
			ID:        id,
			TokenHash: tokenHash,
			// Token stays empty: the cleartext is not in the table any
			// more, which is the entire point.
			ContainerID:   containerID,
			ContainerIP:   ip,
			ContainerPort: port,
			ExpiresAt:     expires,
		}
		loaded++
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// Expire by id rather than by token: the token column no longer holds
	// anything to match on.
	for _, id := range staleIDs {
		if _, err := r.db.ExecContext(ctx, `
			UPDATE port_exposures SET status = 'EXPIRED'
			WHERE id = ? AND status = 'ACTIVE'
		`, id); err != nil {
			r.logger.Warn("port expose registry: expire stale on load", "id", id, "error", err)
		}
	}

	r.logger.Info("port expose registry loaded", "active", loaded, "stale_expired", len(staleIDs))
	return nil
}

// ensureTokenHashColumn adds token_hash when it is missing.
//
// 20260810171000 is what adds it in production. The guard exists because the
// registry is also constructed against databases the migration runner never
// touched (this package's tests hand-roll port_exposures, and so does anything
// restoring a partial dump), and a registry that cannot find its lookup column
// would fail every capability check closed rather than degrade. One PRAGMA per
// registry lifetime.
func (r *PortExposeRegistry) ensureTokenHashColumn(ctx context.Context) {
	r.ensureColumn.Do(func() {
		rows, err := r.db.QueryContext(ctx, `SELECT name FROM pragma_table_info('port_exposures')`)
		if err != nil {
			r.logger.Warn("port expose registry: inspect schema", "error", err)
			return
		}
		defer rows.Close()
		found, present := false, false
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				r.logger.Warn("port expose registry: inspect schema", "error", err)
				return
			}
			found = true
			if name == "token_hash" {
				present = true
			}
		}
		if !found || present {
			return
		}
		if _, err := r.db.ExecContext(ctx, `ALTER TABLE port_exposures ADD COLUMN token_hash TEXT`); err != nil {
			r.logger.Warn("port expose registry: add token_hash column", "error", err)
			return
		}
		if _, err := r.db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_port_exposures_token_hash
			ON port_exposures (token_hash) WHERE token_hash IS NOT NULL`); err != nil {
			r.logger.Warn("port expose registry: index token_hash", "error", err)
		}
	})
}

// backfillTokenHashes hashes every row that still carries a cleartext token
// and overwrites the cleartext in place. Idempotent — the predicate is
// "token_hash IS NULL" — so a re-run after a restore is a no-op.
//
// It deliberately covers REVOKED and EXPIRED rows too, not just the ACTIVE set
// the registry loads: a revoked exposure's token is dead as a capability but is
// still a live secret sitting in a table, and nothing else would ever come back
// for it.
func (r *PortExposeRegistry) backfillTokenHashes(ctx context.Context) {
	r.ensureTokenHashColumn(ctx)
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, token FROM port_exposures WHERE token_hash IS NULL OR token_hash = ''`)
	if err != nil {
		r.logger.Warn("port expose registry: scan rows needing a token hash", "error", err)
		return
	}
	type pending struct{ id, token string }
	var todo []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.token); err != nil {
			rows.Close()
			r.logger.Warn("port expose registry: scan row needing a token hash", "error", err)
			return
		}
		todo = append(todo, p)
	}
	scanErr := rows.Err()
	rows.Close()
	if scanErr != nil {
		r.logger.Warn("port expose registry: scan rows needing a token hash", "error", scanErr)
		return
	}

	hashed := 0
	for _, p := range todo {
		if p.token == "" || strings.HasPrefix(p.token, "redacted:") {
			continue
		}
		if _, err := r.db.ExecContext(ctx,
			`UPDATE port_exposures SET token_hash = ?, token = ? WHERE id = ?`,
			pipeline.HashCapabilityToken(p.token), redactedExposeToken(p.id), p.id,
		); err != nil {
			r.logger.Error("port expose registry: hash token at rest failed — the row still holds cleartext",
				"id", p.id, "error", err)
			continue
		}
		hashed++
	}
	if hashed > 0 {
		r.logger.Info("port exposure tokens hashed at rest", "rows", hashed)
	}
}

// StartPurger runs a background goroutine that every interval flips ACTIVE
// rows with expires_at < now to EXPIRED in the DB and drops the matching
// tokens from the in-memory registry. Call Shutdown to stop it.
func (r *PortExposeRegistry) StartPurger(interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	go r.purgeLoop(interval)
}

func (r *PortExposeRegistry) purgeLoop(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
			r.purgeOnce(context.Background())
		}
	}
}

// purgeOnce is exported for tests — production paths use the ticker. It
// returns the number of UPDATE statements it issued, which is what the batch
// tests assert on (a driver mock would prove nothing about the SQL).
//
// Why this sweeper is chunked, and why that is not a general rule:
// SQLite allows exactly one writer database-wide, so however long this UPDATE
// holds the write lock is dead time for every other writer in the process. A
// single unbounded statement scales that stall with the backlog — measured at
// 41ms for 5,000 rows and 486ms for 50,000, with a concurrent live write
// blocked for 25ms and 449ms respectively. That is not hypothetical: it took
// logins down on 2026-05-25 (a lockout-counter write hit SQLITE_BUSY and
// surfaced as "Invalid email or password"), which is why busy_timeout was
// raised from 5s to 30s. See internal/database/database.go.
//
// Chunking is the right answer HERE because this job runs every 30 seconds
// and its backlog grows with traffic, so the unbounded statement's cost has
// no ceiling. It is the WRONG answer for a large, infrequent job: chunking a
// daily 20,000-row sweep was measured to make things worse (150ms total →
// 405ms, and a live writer's p95 went 4.1ms → 21.5ms) because re-acquiring
// the write lock costs more than the shorter holds save. Do not copy this
// pattern into other sweepers without re-measuring — frequency and backlog
// growth, not row count, are what make it pay off.
func (r *PortExposeRegistry) purgeOnce(ctx context.Context) int {
	now := time.Now().UTC()
	// One timestamp for the whole tick: recomputing it per batch would move
	// the boundary underneath the loop, so rows could slide into or out of
	// the predicate mid-drain and the "short batch means done" exit would no
	// longer be sound.
	cutoff := now.Format(time.RFC3339)

	batch := r.purgeBatch
	if batch <= 0 {
		batch = portExposePurgeBatchSize
	}
	maxIters := r.purgeMaxIters
	if maxIters <= 0 {
		maxIters = portExposePurgeMaxIterations
	}

	// 1. Flip DB rows, a bounded batch at a time. We rely on the DB as the
	//    source of truth for which tokens are expiring: a concurrent revoke
	//    on another goroutine already moved the row to REVOKED and we must
	//    not clobber it — hence status = 'ACTIVE' stays in the predicate on
	//    every iteration, re-evaluated against whatever the row looks like
	//    now rather than against a snapshot taken at the top of the tick.
	var expired int64
	statements := 0
	hitCap := false
	for i := 0; ; i++ {
		// Between batches the write lock is released, which is the whole
		// point: live writers interleave here. It is also where a shutdown
		// gets to stop us rather than grinding through the rest of the
		// backlog.
		if ctx.Err() != nil {
			r.logger.Debug("port expose registry: purge cancelled",
				"statements", statements, "expired", expired)
			break
		}
		if i >= maxIters {
			hitCap = true
			break
		}

		// ORDER BY expires_at drains oldest-first, so a tick that stops at
		// the cap still makes deterministic forward progress instead of
		// re-picking an arbitrary slice. It is free in production: the
		// composite index (status, expires_at) already yields rows in that
		// order for a fixed status.
		res, err := r.db.ExecContext(ctx, `
			UPDATE port_exposures SET status = 'EXPIRED'
			WHERE id IN (
				SELECT id FROM port_exposures
				WHERE status = 'ACTIVE' AND expires_at < ?
				ORDER BY expires_at
				LIMIT ?
			)
		`, cutoff, batch)
		if err != nil {
			r.logger.Warn("port expose registry: purge DB update", "error", err)
			return statements
		}
		statements++
		n, _ := res.RowsAffected()
		expired += n
		if n < int64(batch) {
			// A short batch means the backlog is drained. (An exact multiple
			// of batch costs one extra empty statement, which is cheap.)
			break
		}
	}

	if hitCap {
		// No silent caps: say how much is left so an operator can see a
		// backlog that keeps outrunning the sweeper.
		remaining := -1
		if err := r.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM port_exposures
			WHERE status = 'ACTIVE' AND expires_at < ?
		`, cutoff).Scan(&remaining); err != nil {
			r.logger.Warn("port expose registry: count remaining purge backlog", "error", err)
		}
		r.logger.Warn("port expose registry: purge stopped at iteration cap",
			"batch", batch, "max_iterations", maxIters,
			"expired", expired, "remaining", remaining,
			"note", "backlog outran one tick; it resumes on the next tick")
	}

	// 2. Re-sync the in-memory set by dropping any entry that passed its
	//    ExpiresAt. Cheaper and simpler than reading back the changed rows.
	//    Runs on every non-error path, including the no-op tick, in case a
	//    locally-tracked entry lagged the DB.
	r.sweepInMemory(now)
	return statements
}

func (r *PortExposeRegistry) sweepInMemory(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for tok, e := range r.entries {
		if e.Expired(now) {
			delete(r.entries, tok)
		}
	}
}

// Shutdown stops the purge goroutine. Safe to call multiple times.
func (r *PortExposeRegistry) Shutdown() {
	r.once.Do(func() { close(r.stop) })
}
