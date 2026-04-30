// Package sessions backs the auth lifecycle with a row per active
// refresh-token chain in user_sessions (migration v60). Every API and
// WebSocket request flows through Get(sid) — if the row is missing or
// has revoked_at != NULL, auth fails with session_revoked. signOut,
// password-change, admin force-logout, and refresh rotation all flip
// revoked_at; that's the single chokepoint for invalidating any access
// or WS ticket already minted under the same session.
//
// last_used_at is updated lazily: an in-memory cache throttles writes
// to at-most-once-per-60-seconds per session so hot-path requests don't
// take a SQLite write each. The persisted timestamp is therefore
// approximate (within ~60s) — fine for the "Active sessions" UI but
// not appropriate for security-critical accounting.
package sessions

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Reasons emitted into user_sessions.revoked_reason. Pinned strings so
// the Active-sessions UI and audit queries don't have to guess.
const (
	ReasonLogout         = "logout"
	ReasonAdminForce     = "admin_force"
	ReasonRotation       = "rotation"
	ReasonPasswordChange = "password_change"
)

// LastUsedThrottle is the minimum interval between two persisted
// last_used_at updates for the same session. Smaller = more accurate
// "Last seen" UI; larger = fewer SQLite writes on hot endpoints. 60s
// is the documented contract.
const LastUsedThrottle = 60 * time.Second

// ErrNotFound is returned when a session id does not exist (or has
// been GC'd). Distinct from "exists but revoked" — that returns the
// row with RevokedAt set.
var ErrNotFound = errors.New("session not found")

// Session mirrors a single user_sessions row.
type Session struct {
	ID                string
	UserID            string
	CreatedAt         time.Time
	ExpiresAt         time.Time
	LastUsedAt        time.Time
	RevokedAt         *time.Time
	RevokedReason     string
	UserAgent         string
	IP                string
	CurrentRefreshJti string // empty until first refresh rotation
}

// Active is true iff the session has not been revoked AND has not
// passed its absolute expires_at. The middleware should treat both
// conditions as "session_revoked" for the response code (the user
// can't tell them apart and shouldn't need to).
func (s *Session) Active(now time.Time) bool {
	if s.RevokedAt != nil {
		return false
	}
	return now.Before(s.ExpiresAt)
}

// Store is the persistence interface. Implemented by *DBStore against
// the migration v60 schema; tests can plug in MemStore for isolation.
type Store interface {
	Create(ctx context.Context, userID, userAgent, ip string, ttl time.Duration) (*Session, error)
	Get(ctx context.Context, id string) (*Session, error)
	ListActiveForUser(ctx context.Context, userID string) ([]*Session, error)
	Revoke(ctx context.Context, id, reason string) error
	RevokeAllForUser(ctx context.Context, userID, reason string) (int64, error)
	TouchLastUsed(ctx context.Context, id string) error

	// RotateRefreshJti is the CAS step at the heart of the refresh-
	// token-reuse detection (OWASP ASVS 3.7.4). The caller passes the
	// JTI it received in the inbound refresh token; we update only if
	// it matches the row's current_refresh_jti (or the row has none
	// yet, allowing the very first rotation). Returns ErrJTIMismatch
	// if the inbound JTI doesn't match — that's the theft signal and
	// the caller MUST revoke the entire session.
	RotateRefreshJti(ctx context.Context, sessionID, expectedJti, newJti string) error

	// SetClock overrides the time source. Production code never calls
	// this; tests use it to control created_at / expires_at /
	// last_used_at without sleeping. On the interface so handler-
	// level tests don't need to type-assert to *DBStore just to
	// reach the override.
	SetClock(fn func() time.Time)
}

// ErrJTIMismatch fires when a refresh request carries a JTI that does
// not match the session's current_refresh_jti. Either:
//   (a) the rightful client already rotated and the attacker is using
//       the old token (theft), or
//   (b) the attacker rotated first and the rightful client is now
//       presenting a since-superseded token (still theft, just from
//       the other side).
// In both cases the response is to revoke the whole session.
var ErrJTIMismatch = errors.New("refresh jti mismatch")

// DBStore is the production implementation backed by SQLite via
// database/sql. Pass the same *sql.DB the rest of the API uses — the
// migration creates user_sessions in that database.
type DBStore struct {
	db    *sql.DB
	clock func() time.Time

	// touchCache throttles last_used_at writes per session id. Map
	// access is guarded by touchMu. Entries are never aged out
	// explicitly; revoked sessions stop being queried so their entry
	// just stops getting hit. Worst-case memory is one timestamp per
	// session that has ever been touched in the process lifetime,
	// which is bounded by the size of user_sessions itself.
	touchMu    sync.Mutex
	touchCache map[string]time.Time
}

// NewDBStore wraps a *sql.DB. The store is safe for concurrent use.
func NewDBStore(db *sql.DB) *DBStore {
	return &DBStore{db: db, clock: time.Now, touchCache: make(map[string]time.Time)}
}

// SetClock overrides the time source for tests. Production code should
// never call this.
func (s *DBStore) SetClock(fn func() time.Time) { s.clock = fn }

// Create inserts a new active session row and returns it. ttl sets
// expires_at; pass auth.RefreshTokenTTL for normal logins. user_agent
// and ip are persisted verbatim — caller is responsible for stripping
// any sensitive data and bounding length (250 char cap is enforced
// here as a defensive trim).
func (s *DBStore) Create(ctx context.Context, userID, userAgent, ip string, ttl time.Duration) (*Session, error) {
	if userID == "" {
		return nil, errors.New("user id required")
	}
	if ttl <= 0 {
		return nil, errors.New("ttl must be positive")
	}
	now := s.clock().UTC()
	id, err := newSessionID()
	if err != nil {
		return nil, fmt.Errorf("gen id: %w", err)
	}
	expires := now.Add(ttl)

	const q = `INSERT INTO user_sessions
		(id, user_id, created_at, expires_at, last_used_at, user_agent, ip)
		VALUES (?, ?, ?, ?, ?, ?, ?)`
	_, err = s.db.ExecContext(ctx, q,
		id, userID,
		now.Format(time.RFC3339), expires.Format(time.RFC3339), now.Format(time.RFC3339),
		trimStr(userAgent, 250), trimStr(ip, 64),
	)
	if err != nil {
		return nil, fmt.Errorf("insert session: %w", err)
	}
	return &Session{
		ID: id, UserID: userID,
		CreatedAt: now, ExpiresAt: expires, LastUsedAt: now,
		UserAgent: trimStr(userAgent, 250), IP: trimStr(ip, 64),
	}, nil
}

// Get returns the session row by id, including the revoked_at field
// (nil when active). Callers must check Active(now) — Get on its own
// does NOT filter out revoked rows because the middleware needs to
// distinguish "expired" from "revoked" from "never existed" in its
// 401 response code.
func (s *DBStore) Get(ctx context.Context, id string) (*Session, error) {
	const q = `SELECT id, user_id, created_at, expires_at, last_used_at,
		revoked_at, revoked_reason, user_agent, ip, current_refresh_jti
		FROM user_sessions WHERE id = ?`
	row := s.db.QueryRowContext(ctx, q, id)
	return scanSession(row)
}

// ListActiveForUser returns rows where revoked_at IS NULL AND
// expires_at > now, ordered by last_used_at DESC. Used by the
// "Active sessions" UI.
func (s *DBStore) ListActiveForUser(ctx context.Context, userID string) ([]*Session, error) {
	now := s.clock().UTC().Format(time.RFC3339)
	const q = `SELECT id, user_id, created_at, expires_at, last_used_at,
		revoked_at, revoked_reason, user_agent, ip, current_refresh_jti
		FROM user_sessions
		WHERE user_id = ? AND revoked_at IS NULL AND expires_at > ?
		ORDER BY last_used_at DESC`
	rows, err := s.db.QueryContext(ctx, q, userID, now)
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	defer rows.Close()

	var out []*Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// Revoke flips revoked_at to now and stamps the reason. Idempotent —
// a repeat call updates the timestamp/reason but stays revoked.
func (s *DBStore) Revoke(ctx context.Context, id, reason string) error {
	if id == "" {
		return errors.New("session id required")
	}
	now := s.clock().UTC().Format(time.RFC3339)
	const q = `UPDATE user_sessions
		SET revoked_at = ?, revoked_reason = ?
		WHERE id = ?`
	res, err := s.db.ExecContext(ctx, q, now, reason, id)
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	// Drop the touch-cache entry so a future re-Create at the same
	// id (impossible in practice, but cheap defense) doesn't carry
	// over stale throttling state.
	s.touchMu.Lock()
	delete(s.touchCache, id)
	s.touchMu.Unlock()
	return nil
}

// RevokeAllForUser flips every active session for the given user.
// Used by password-change and admin force-logout.
func (s *DBStore) RevokeAllForUser(ctx context.Context, userID, reason string) (int64, error) {
	if userID == "" {
		return 0, errors.New("user id required")
	}
	now := s.clock().UTC().Format(time.RFC3339)
	const q = `UPDATE user_sessions
		SET revoked_at = ?, revoked_reason = ?
		WHERE user_id = ? AND revoked_at IS NULL`
	res, err := s.db.ExecContext(ctx, q, now, reason, userID)
	if err != nil {
		return 0, fmt.Errorf("revoke all: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// TouchLastUsed updates last_used_at, throttled to LastUsedThrottle
// per session. Returns nil silently when the cached timestamp says
// we already wrote within the window — that's the throttle contract,
// not a hidden failure. Any actual SQL error propagates.
//
// The middleware calls this on every authed request; making it cheap
// and non-fatal keeps the auth path tight.
func (s *DBStore) TouchLastUsed(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	now := s.clock().UTC()

	s.touchMu.Lock()
	prev, ok := s.touchCache[id]
	if ok && now.Sub(prev) < LastUsedThrottle {
		s.touchMu.Unlock()
		return nil
	}
	s.touchCache[id] = now
	s.touchMu.Unlock()

	const q = `UPDATE user_sessions SET last_used_at = ? WHERE id = ? AND revoked_at IS NULL`
	_, err := s.db.ExecContext(ctx, q, now.Format(time.RFC3339), id)
	if err != nil {
		// Roll back the cache entry so the next call retries
		// rather than hiding the previous failure for a minute.
		s.touchMu.Lock()
		if cur, ok := s.touchCache[id]; ok && cur.Equal(now) {
			delete(s.touchCache, id)
		}
		s.touchMu.Unlock()
		return fmt.Errorf("touch last_used: %w", err)
	}
	return nil
}

// RotateRefreshJti enforces single-use refresh tokens by atomically
// swapping current_refresh_jti from expectedJti to newJti. The query
// requires expectedJti to match the stored value OR the stored value
// to be NULL (which only happens on the very first rotation after
// signIn — Create() leaves current_refresh_jti=NULL). On mismatch we
// return ErrJTIMismatch and the caller revokes the whole session.
func (s *DBStore) RotateRefreshJti(ctx context.Context, sessionID, expectedJti, newJti string) error {
	if sessionID == "" || newJti == "" {
		return errors.New("session id and newJti required")
	}
	const q = `UPDATE user_sessions
		SET current_refresh_jti = ?
		WHERE id = ?
		  AND revoked_at IS NULL
		  AND (current_refresh_jti IS NULL OR current_refresh_jti = ?)`
	res, err := s.db.ExecContext(ctx, q, newJti, sessionID, expectedJti)
	if err != nil {
		return fmt.Errorf("rotate jti: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Disambiguate: maybe the row exists but jti didn't match
		// (theft), maybe the row is gone or revoked (legitimate
		// expiry / explicit signOut). Theft is the more dangerous
		// signal so we raise ErrJTIMismatch — the caller decides
		// whether to revoke (we don't auto-revoke from inside the
		// store so its responsibilities stay narrow).
		var revokedAt sql.NullString
		err := s.db.QueryRowContext(ctx,
			`SELECT revoked_at FROM user_sessions WHERE id = ?`, sessionID,
		).Scan(&revokedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("rotate jti diagnose: %w", err)
		}
		if revokedAt.Valid {
			return ErrNotFound
		}
		return ErrJTIMismatch
	}
	return nil
}

// rowScanner abstracts *sql.Row and *sql.Rows so scanSession can be
// shared between Get (single row) and ListActiveForUser (many rows).
type rowScanner interface {
	Scan(dest ...any) error
}

func scanSession(r rowScanner) (*Session, error) {
	var sess Session
	var createdAt, expiresAt, lastUsedAt string
	var revokedAt sql.NullString
	var revokedReason, userAgent, ip, currentRefreshJti sql.NullString

	err := r.Scan(
		&sess.ID, &sess.UserID,
		&createdAt, &expiresAt, &lastUsedAt,
		&revokedAt, &revokedReason, &userAgent, &ip, &currentRefreshJti,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan session: %w", err)
	}

	sess.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	sess.ExpiresAt, _ = time.Parse(time.RFC3339, expiresAt)
	sess.LastUsedAt, _ = time.Parse(time.RFC3339, lastUsedAt)
	if revokedAt.Valid {
		t, perr := time.Parse(time.RFC3339, revokedAt.String)
		if perr == nil {
			sess.RevokedAt = &t
		}
	}
	if revokedReason.Valid {
		sess.RevokedReason = revokedReason.String
	}
	if userAgent.Valid {
		sess.UserAgent = userAgent.String
	}
	if ip.Valid {
		sess.IP = ip.String
	}
	if currentRefreshJti.Valid {
		sess.CurrentRefreshJti = currentRefreshJti.String
	}
	return &sess, nil
}

func newSessionID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "s_" + hex.EncodeToString(b[:]), nil
}

func trimStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
