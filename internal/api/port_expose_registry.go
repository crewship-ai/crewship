package api

import (
	"context"
	"database/sql"
	"log/slog"
	"net/url"
	"strconv"
	"sync"
	"time"
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
	ID            string
	Token         string
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
}

// NewPortExposeRegistry builds an empty registry. Call LoadFromDB after
// construction to populate it from durable state, then StartPurger to enable
// automatic expiry.
func NewPortExposeRegistry(db *sql.DB, logger *slog.Logger) *PortExposeRegistry {
	if logger == nil {
		logger = slog.Default()
	}
	return &PortExposeRegistry{
		entries: make(map[string]*ExposeEntry),
		db:      db,
		logger:  logger,
		stop:    make(chan struct{}),
	}
}

// Add inserts or overwrites an entry for the given token. Called from the
// request handler once a new port_exposures row has been committed to the DB.
func (r *PortExposeRegistry) Add(entry *ExposeEntry) {
	if entry == nil || entry.Token == "" {
		return
	}
	r.mu.Lock()
	r.entries[entry.Token] = entry
	r.mu.Unlock()
}

// Lookup returns the entry for the token and whether it was found. The caller
// is responsible for the expiry check (registry can't enforce it without a
// clock dependency and the proxy already has one anyway).
func (r *PortExposeRegistry) Lookup(token string) (*ExposeEntry, bool) {
	r.mu.RLock()
	e, ok := r.entries[token]
	r.mu.RUnlock()
	return e, ok
}

// Remove deletes the entry for the token. Called on explicit revoke or after
// the purger transitions a row to EXPIRED in the DB.
func (r *PortExposeRegistry) Remove(token string) {
	r.mu.Lock()
	delete(r.entries, token)
	r.mu.Unlock()
}

// UpdateIP swaps the cached container IP for the entry at token. Called by
// the proxy when it re-resolves the container's address and finds it has
// moved (container restart / recreate). A no-op if the token is unknown so
// the proxy can call this without racing the purger.
func (r *PortExposeRegistry) UpdateIP(token, newIP string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[token]; ok {
		e.ContainerIP = newIP
	}
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
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, token, container_id, container_ip, container_port, expires_at
		FROM port_exposures
		WHERE status = 'ACTIVE'
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var staleTokens []string
	loaded := 0
	for rows.Next() {
		var id, token, containerID, ip, expiresStr string
		var port int
		if err := rows.Scan(&id, &token, &containerID, &ip, &port, &expiresStr); err != nil {
			r.logger.Warn("port expose registry: scan row", "error", err)
			continue
		}
		expires, perr := time.Parse(time.RFC3339, expiresStr)
		if perr != nil {
			r.logger.Warn("port expose registry: parse expires_at", "id", id, "value", expiresStr, "error", perr)
			continue
		}
		if !now.Before(expires) {
			staleTokens = append(staleTokens, token)
			continue
		}
		r.entries[token] = &ExposeEntry{
			ID:            id,
			Token:         token,
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

	for _, tok := range staleTokens {
		if _, err := r.db.ExecContext(ctx, `
			UPDATE port_exposures SET status = 'EXPIRED'
			WHERE token = ? AND status = 'ACTIVE'
		`, tok); err != nil {
			r.logger.Warn("port expose registry: expire stale on load", "token", tok, "error", err)
		}
	}

	r.logger.Info("port expose registry loaded", "active", loaded, "stale_expired", len(staleTokens))
	return nil
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

// purgeOnce is exported for tests — production paths use the ticker.
func (r *PortExposeRegistry) purgeOnce(ctx context.Context) {
	now := time.Now().UTC()

	// 1. Flip DB rows. We rely on the DB as the source of truth for which
	//    tokens are expiring: a concurrent revoke on another goroutine
	//    already moved the row to REVOKED and we must not clobber it.
	res, err := r.db.ExecContext(ctx, `
		UPDATE port_exposures SET status = 'EXPIRED'
		WHERE status = 'ACTIVE' AND expires_at < ?
	`, now.Format(time.RFC3339))
	if err != nil {
		r.logger.Warn("port expose registry: purge DB update", "error", err)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Nothing rolled over this tick. Still sweep the in-memory map in case
		// a locally-tracked entry lagged the DB.
		r.sweepInMemory(now)
		return
	}

	// 2. Re-sync the in-memory set by dropping any entry that passed its
	//    ExpiresAt. Cheaper and simpler than reading back the changed rows.
	r.sweepInMemory(now)
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
