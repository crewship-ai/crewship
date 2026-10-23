package mooring

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"time"
)

var (
	ErrOwnerDisabled = errors.New("Mooring session ownership disabled")
	ErrOwnerBusy     = errors.New("Mooring session already owned")
	ErrOwnerStale    = errors.New("stale_owner")
)

// OwnerClock must identify the OS boot and measure elapsed time including suspend.
// An enabled owner store requires this trusted clock; wall time is not a fallback.
type OwnerClock interface {
	Now() (bootID string, elapsed time.Duration, err error)
}
type SessionKey struct{ WorkspaceID, ArtifactID, ApplicationID string }

// OwnerLease is a host-runtime capability. Never expose Token to a participant.
type OwnerLease struct {
	Key        SessionKey
	OwnerID    string
	Generation int64
	Token      string
}

// OwnerStore is an unused, default-off ownership boundary. Callers still must
// authenticate the runtime and authorize every participant in the commit tx.
type OwnerStore struct {
	db      *sql.DB
	enabled bool
	clock   OwnerClock
	ttl     time.Duration
}

func NewOwnerStore(db *sql.DB, enabled bool, clock OwnerClock, ttl time.Duration) *OwnerStore {
	return &OwnerStore{db: db, enabled: enabled, clock: clock, ttl: ttl}
}
func ownerIDValid(s string) bool { return len(s) > 0 && len(s) <= 200 && !strings.ContainsRune(s, 0) }
func (s *OwnerStore) now() (string, int64, error) {
	if !s.enabled {
		return "", 0, ErrOwnerDisabled
	}
	if s.db == nil || s.clock == nil || s.ttl <= 0 || s.ttl > time.Minute {
		return "", 0, ErrInvalid
	}
	boot, tick, e := s.clock.Now()
	if e != nil {
		return "", 0, e
	}
	if !ownerIDValid(boot) || tick < 0 || int64(tick) > math.MaxInt64-int64(s.ttl) {
		return "", 0, ErrInvalid
	}
	return boot, int64(tick), nil
}
func ownerFence(ctx context.Context, tx *sql.Tx) error {
	r, e := tx.ExecContext(ctx, `UPDATE mooring_session_writer_fence SET seq=seq WHERE id=1`)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return ErrOwnerStale
	}
	return nil
}
func (s *OwnerStore) transaction(ctx context.Context, fn func(*sql.Tx, string, int64) error) error {
	if !s.enabled {
		return ErrOwnerDisabled
	}
	if s.db == nil {
		return ErrInvalid
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = ownerFence(ctx, tx); e != nil {
		return e
	}
	var synchronous int
	if e = tx.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); e != nil {
		return e
	}
	if synchronous < 2 {
		return ErrInvalid
	}
	boot, now, e := s.now()
	if e != nil {
		return e
	}
	if e = fn(tx, boot, now); e != nil {
		return e
	}
	return tx.Commit()
}
func ownerScopeExists(ctx context.Context, tx *sql.Tx, k SessionKey) error {
	if !ownerIDValid(k.WorkspaceID) || !ownerIDValid(k.ArtifactID) || !ownerIDValid(k.ApplicationID) {
		return ErrOwnerStale
	}
	var exists int
	e := tx.QueryRowContext(ctx, `SELECT 1 FROM pages WHERE id=? AND workspace_id=?`, k.ArtifactID, k.WorkspaceID).Scan(&exists)
	if errors.Is(e, sql.ErrNoRows) {
		return ErrOwnerStale
	}
	return e
}
func ownerTokenHash(token string) []byte { h := sha256.Sum256([]byte(token)); return h[:] }

// Acquire changes ownership only after expiry or an OS reboot. Generation is
// preserved on disk and advanced atomically; no force-steal path exists.
func (s *OwnerStore) Acquire(ctx context.Context, k SessionKey, owner string) (lease OwnerLease, err error) {
	err = s.transaction(ctx, func(tx *sql.Tx, boot string, now int64) error {
		if !ownerIDValid(owner) {
			return ErrInvalid
		}
		if e := ownerScopeExists(ctx, tx, k); e != nil {
			return e
		}
		var generation, expiry int64
		var oldBoot, oldApp string
		e := tx.QueryRowContext(ctx, `SELECT generation,expires_ns,boot_id,application_id FROM mooring_session_owners WHERE page_id=?`, k.ArtifactID).Scan(&generation, &expiry, &oldBoot, &oldApp)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if e == nil {
			if oldApp != k.ApplicationID {
				return ErrOwnerStale
			}
			if oldBoot == boot && now < expiry {
				return ErrOwnerBusy
			}
		}
		if generation == math.MaxInt64 {
			return ErrOwnerStale
		}
		raw := make([]byte, 32)
		if _, e = rand.Read(raw); e != nil {
			return e
		}
		token := hex.EncodeToString(raw)
		generation++
		_, e = tx.ExecContext(ctx, `INSERT INTO mooring_session_owners(page_id,workspace_id,application_id,owner_id,generation,token_hash,boot_id,expires_ns) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(page_id) DO UPDATE SET owner_id=excluded.owner_id,generation=excluded.generation,token_hash=excluded.token_hash,boot_id=excluded.boot_id,expires_ns=excluded.expires_ns`, k.ArtifactID, k.WorkspaceID, k.ApplicationID, owner, generation, ownerTokenHash(token), boot, now+int64(s.ttl))
		if e == nil {
			lease = OwnerLease{Key: k, OwnerID: owner, Generation: generation, Token: token}
		}
		return e
	})
	if err != nil {
		return OwnerLease{}, err
	}
	return lease, nil
}
func (s *OwnerStore) check(ctx context.Context, tx *sql.Tx, l OwnerLease, boot string, now int64) error {
	if e := ownerScopeExists(ctx, tx, l.Key); e != nil {
		return e
	}
	if l.Generation < 1 || len(l.Token) != 64 || !ownerIDValid(l.OwnerID) {
		return ErrOwnerStale
	}
	var app, owner, storedBoot, workspace string
	var generation, expires int64
	var hash []byte
	e := tx.QueryRowContext(ctx, `SELECT workspace_id,application_id,owner_id,generation,token_hash,boot_id,expires_ns FROM mooring_session_owners WHERE page_id=?`, l.Key.ArtifactID).Scan(&workspace, &app, &owner, &generation, &hash, &storedBoot, &expires)
	if errors.Is(e, sql.ErrNoRows) {
		return ErrOwnerStale
	}
	if e != nil {
		return e
	}
	if workspace != l.Key.WorkspaceID || app != l.Key.ApplicationID || owner != l.OwnerID || generation != l.Generation || storedBoot != boot || now >= expires || subtle.ConstantTimeCompare(hash, ownerTokenHash(l.Token)) != 1 {
		return ErrOwnerStale
	}
	return nil
}

// Check MUST be called in the SAME transaction as authorization and persistence.
// Call after expensive validation and immediately before writing. It acquires
// the SQLite writer fence before checking; a preflight read is not a
// substitute. The capability alone grants no document/participant permissions.
func (s *OwnerStore) Check(ctx context.Context, tx *sql.Tx, l OwnerLease) error {
	if !s.enabled {
		return ErrOwnerDisabled
	}
	if tx == nil {
		return ErrInvalid
	}
	if e := ownerFence(ctx, tx); e != nil {
		return e
	}
	boot, now, e := s.now()
	if e != nil {
		return e
	}
	return s.check(ctx, tx, l, boot, now)
}
func (s *OwnerStore) Renew(ctx context.Context, l OwnerLease) (OwnerLease, error) {
	e := s.transaction(ctx, func(tx *sql.Tx, boot string, now int64) error {
		if e := s.check(ctx, tx, l, boot, now); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, `UPDATE mooring_session_owners SET expires_ns=? WHERE page_id=? AND generation=?`, now+int64(s.ttl), l.Key.ArtifactID, l.Generation)
		return e
	})
	if e != nil {
		return OwnerLease{}, e
	}
	return l, nil
}
