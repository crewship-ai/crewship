package mooring

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"
)

var (
	ErrEditLeaseDisabled = errors.New("single-editor leases disabled")
	ErrEditLeaseBusy     = errors.New("document already has an editor")
	ErrEditLeaseStale    = errors.New("stale edit lease")
	ErrEditLeaseDenied   = errors.New("edit lease access denied")
)

type EditLeaseScope struct{ WorkspaceID, ArtifactID string }

// EditLeasePrincipal is bound by the trusted host to an authenticated connection.
// SessionID distinguishes tabs; neither field is accepted as request authority.
type EditLeasePrincipal struct{ ActorID, SessionID string }
type EditLease struct {
	Scope      EditLeaseScope
	Holder     EditLeasePrincipal
	Generation int64
	// ExpiresAt is presentation only. The server uses its persisted boot clock.
	ExpiresAt time.Time
}
type EditLeaseAccess struct {
	Scope     EditLeaseScope
	Principal EditLeasePrincipal
	Intent    string
}
type EditLeaseAuthority func(context.Context, *sql.Tx, EditLeaseAccess) error
type EditLeaseConfig struct {
	Enabled   bool
	Clock     OwnerClock
	TTL       time.Duration
	Authority EditLeaseAuthority
}

// EditLeaseStore is default-off. It does not replace the host's authorization,
// version CAS, validation or delivery checks. The authority callback must check
// the current credential/session and Page rights in the supplied transaction.
// Bind the original immutable credential/mandate epoch as well as the principal:
// restoring permissions must not revive a credential that was revoked. Renew
// requires the same live authenticated editor connection, not a claimed actor.
// Revoke intent is a trusted rights-management operation, never a reader grant.
type EditLeaseStore struct {
	db     *sql.DB
	config EditLeaseConfig
}

func NewEditLeaseStore(db *sql.DB, config EditLeaseConfig) *EditLeaseStore {
	return &EditLeaseStore{db: db, config: config}
}
func (s *EditLeaseStore) ready() error {
	if s == nil || !s.config.Enabled {
		return ErrEditLeaseDisabled
	}
	if s.db == nil || s.config.Clock == nil || s.config.Authority == nil || s.config.TTL <= 0 || s.config.TTL > time.Minute {
		return ErrInvalid
	}
	return nil
}
func (s *EditLeaseStore) clock() (string, int64, error) {
	if err := s.ready(); err != nil {
		return "", 0, err
	}
	boot, tick, err := s.config.Clock.Now()
	if err != nil {
		return "", 0, err
	}
	if !ownerIDValid(boot) || tick < 0 || int64(tick) > math.MaxInt64-int64(s.config.TTL) {
		return "", 0, ErrInvalid
	}
	return boot, int64(tick), nil
}
func (s *EditLeaseStore) authorize(ctx context.Context, tx *sql.Tx, scope EditLeaseScope, p EditLeasePrincipal, intent string) error {
	if !ownerIDValid(scope.WorkspaceID) || !ownerIDValid(scope.ArtifactID) || !ownerIDValid(p.ActorID) || !ownerIDValid(p.SessionID) {
		return ErrEditLeaseDenied
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM pages p JOIN workspaces w ON w.id=p.workspace_id WHERE p.id=? AND p.workspace_id=? AND w.deleted_at IS NULL`, scope.ArtifactID, scope.WorkspaceID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrEditLeaseDenied
		}
		return err
	}
	return s.config.Authority(ctx, tx, EditLeaseAccess{Scope: scope, Principal: p, Intent: intent})
}
func (s *EditLeaseStore) transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	if err := s.ready(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = ownerFence(ctx, tx); err != nil {
		return err
	}
	var synchronous int
	if err = tx.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		return err
	}
	if synchronous < 2 {
		return ErrInvalid
	}
	if _, _, err = s.clock(); err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
func editLeaseRow(ctx context.Context, tx *sql.Tx, scope EditLeaseScope) (l EditLease, boot string, expires int64, err error) {
	l.Scope = scope
	var workspace string
	err = tx.QueryRowContext(ctx, `SELECT workspace_id,actor_id,session_id,generation,boot_id,expires_ns FROM mooring_edit_leases WHERE page_id=?`, scope.ArtifactID).Scan(&workspace, &l.Holder.ActorID, &l.Holder.SessionID, &l.Generation, &boot, &expires)
	if err == nil && workspace != scope.WorkspaceID {
		err = ErrEditLeaseStale
	}
	return
}
func editLeaseLive(l EditLease, boot string, expires int64, currentBoot string, now int64) bool {
	return l.Holder.ActorID != "" && l.Holder.SessionID != "" && boot == currentBoot && now < expires
}
func presentEditLease(l EditLease, expires, now int64) EditLease {
	l.ExpiresAt = time.Now().UTC().Add(time.Duration(expires - now))
	return l
}

// check returns the exact clock snapshot validated against the stored lease.
// Renewal/handoff must use it, rather than resampling a later time as if valid.
func (s *EditLeaseStore) check(ctx context.Context, tx *sql.Tx, l EditLease, intent string) (string, int64, error) {
	if err := s.authorize(ctx, tx, l.Scope, l.Holder, intent); err != nil {
		return "", 0, err
	}
	stored, storedBoot, expires, err := editLeaseRow(ctx, tx, l.Scope)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, ErrEditLeaseStale
	}
	if err != nil {
		return "", 0, err
	}
	boot, now, err := s.clock()
	if err != nil {
		return "", 0, err
	}
	if l.Generation < 1 || stored.Generation != l.Generation || stored.Holder != l.Holder || !editLeaseLive(stored, storedBoot, expires, boot, now) {
		return "", 0, ErrEditLeaseStale
	}
	return boot, now, nil
}

// CheckWrite MUST run in the SAME authoritative transaction as native version
// CAS and publication, after engine validation. Caller must roll back on error.
// No preflight response, holder label or client-provided deadline substitutes.
func (s *EditLeaseStore) CheckWrite(ctx context.Context, tx *sql.Tx, l EditLease) error {
	if err := s.ready(); err != nil {
		return err
	}
	if tx == nil {
		return ErrInvalid
	}
	if err := ownerFence(ctx, tx); err != nil {
		return err
	}
	_, _, err := s.check(ctx, tx, l, "write")
	return err
}
func (s *EditLeaseStore) install(ctx context.Context, tx *sql.Tx, scope EditLeaseScope, p EditLeasePrincipal, generation int64, boot string, now int64) (EditLease, error) {
	if generation == math.MaxInt64 {
		return EditLease{}, ErrEditLeaseStale
	}
	l := EditLease{Scope: scope, Holder: p, Generation: generation + 1}
	expires := now + int64(s.config.TTL)
	_, err := tx.ExecContext(ctx, `INSERT INTO mooring_edit_leases(page_id,workspace_id,actor_id,session_id,generation,boot_id,expires_ns) VALUES(?,?,?,?,?,?,?) ON CONFLICT(page_id) DO UPDATE SET actor_id=excluded.actor_id,session_id=excluded.session_id,generation=excluded.generation,boot_id=excluded.boot_id,expires_ns=excluded.expires_ns`, scope.ArtifactID, scope.WorkspaceID, p.ActorID, p.SessionID, l.Generation, boot, expires)
	if err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM mooring_edit_lease_requests WHERE page_id=?`, scope.ArtifactID)
	}
	return presentEditLease(l, expires, now), err
}
func (s *EditLeaseStore) Acquire(ctx context.Context, scope EditLeaseScope, p EditLeasePrincipal) (result EditLease, err error) {
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		if e := s.authorize(ctx, tx, scope, p, "acquire"); e != nil {
			return e
		}
		old, oldBoot, expires, e := editLeaseRow(ctx, tx, scope)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		boot, now, err := s.clock()
		if err != nil {
			return err
		}
		if e == nil && editLeaseLive(old, oldBoot, expires, boot, now) {
			if old.Holder != p {
				return ErrEditLeaseBusy
			}
			result = presentEditLease(old, expires, now)
			return nil
		}
		result, e = s.install(ctx, tx, scope, p, old.Generation, boot, now)
		return e
	})
	if err != nil {
		return EditLease{}, err
	}
	return
}

// Renew is called by a live authenticated editor connection, independently of
// typing or tab visibility. A disconnected or revoked session cannot renew.
func (s *EditLeaseStore) Renew(ctx context.Context, l EditLease) (result EditLease, err error) {
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		_, now, e := s.check(ctx, tx, l, "renew")
		if e != nil {
			return e
		}
		expires := now + int64(s.config.TTL)
		_, e = tx.ExecContext(ctx, `UPDATE mooring_edit_leases SET expires_ns=? WHERE page_id=?`, expires, l.Scope.ArtifactID)
		result = presentEditLease(l, expires, now)
		return e
	})
	if err != nil {
		return EditLease{}, err
	}
	return
}
func (s *EditLeaseStore) Release(ctx context.Context, l EditLease) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if _, _, e := s.check(ctx, tx, l, "release"); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, `UPDATE mooring_edit_leases SET actor_id='',session_id='',expires_ns=0 WHERE page_id=?`, l.Scope.ArtifactID)
		if e == nil {
			_, e = tx.ExecContext(ctx, `DELETE FROM mooring_edit_lease_requests WHERE page_id=?`, l.Scope.ArtifactID)
		}
		return e
	})
}
func (s *EditLeaseStore) RequestHandoff(ctx context.Context, scope EditLeaseScope, p EditLeasePrincipal) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if e := s.authorize(ctx, tx, scope, p, "request"); e != nil {
			return e
		}
		l, storedBoot, expires, e := editLeaseRow(ctx, tx, scope)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrEditLeaseStale
		}
		if e != nil {
			return e
		}
		boot, now, e := s.clock()
		if e != nil {
			return e
		}
		if !editLeaseLive(l, storedBoot, expires, boot, now) || l.Holder == p {
			return ErrEditLeaseStale
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO mooring_edit_lease_requests(page_id,actor_id,session_id,generation) VALUES(?,?,?,?) ON CONFLICT(page_id,actor_id,session_id) DO UPDATE SET generation=excluded.generation`, scope.ArtifactID, p.ActorID, p.SessionID, l.Generation)
		return e
	})
}

// Handoff serializes after any publication and rechecks both participants. The
// host first settles uncertain saves and preserves its local draft, then calls.
func (s *EditLeaseStore) Handoff(ctx context.Context, l EditLease, target EditLeasePrincipal) (result EditLease, err error) {
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		if _, _, e := s.check(ctx, tx, l, "handoff"); e != nil {
			return e
		}
		if e := s.authorize(ctx, tx, l.Scope, target, "acquire"); e != nil {
			return e
		}
		var requested int
		e := tx.QueryRowContext(ctx, `SELECT 1 FROM mooring_edit_lease_requests WHERE page_id=? AND actor_id=? AND session_id=? AND generation=?`, l.Scope.ArtifactID, target.ActorID, target.SessionID, l.Generation).Scan(&requested)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrEditLeaseDenied
		}
		if e != nil {
			return e
		}
		// Target authorization may outlive the holder's lease.
		boot, now, e := s.check(ctx, tx, l, "handoff")
		if e != nil {
			return e
		}
		result, e = s.install(ctx, tx, l.Scope, target, l.Generation, boot, now)
		return e
	})
	if err != nil {
		return EditLease{}, err
	}
	return
}

// Read rechecks read authority; it neither acquires nor renews a lease. Hosts
// must likewise recheck read rights before every content delivery.
func (s *EditLeaseStore) Read(ctx context.Context, scope EditLeaseScope, p EditLeasePrincipal) (result EditLease, err error) {
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		if e := s.authorize(ctx, tx, scope, p, "read"); e != nil {
			return e
		}
		l, storedBoot, expires, e := editLeaseRow(ctx, tx, scope)
		if errors.Is(e, sql.ErrNoRows) {
			result.Scope = scope
			return nil
		}
		if e != nil {
			return e
		}
		boot, now, e := s.clock()
		if e != nil {
			return e
		}
		if !editLeaseLive(l, storedBoot, expires, boot, now) {
			l.Holder = EditLeasePrincipal{}
			l.ExpiresAt = time.Time{}
			result = l
		} else {
			result = presentEditLease(l, expires, now)
		}
		return nil
	})
	if err != nil {
		return EditLease{}, err
	}
	return
}

// Revoke invalidates the expected generation in the SAME transaction as the
// host's rights/mandate change. The trusted caller's authority callback must
// authorize the revoke intent. Preserving the generation prevents permission
// restoration from reviving the revoked connection. A stale revocation cannot
// invalidate a later holder. Readers are governed separately by read authority.
func (s *EditLeaseStore) Revoke(ctx context.Context, tx *sql.Tx, scope EditLeaseScope, by EditLeasePrincipal, expectedGeneration int64) error {
	if err := s.ready(); err != nil {
		return err
	}
	if tx == nil || expectedGeneration < 1 {
		return ErrInvalid
	}
	if err := ownerFence(ctx, tx); err != nil {
		return err
	}
	if err := s.authorize(ctx, tx, scope, by, "revoke"); err != nil {
		return err
	}
	l, _, _, err := editLeaseRow(ctx, tx, scope)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrEditLeaseStale
	}
	if err != nil {
		return err
	}
	if l.Generation != expectedGeneration {
		return ErrEditLeaseStale
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mooring_edit_leases SET actor_id='',session_id='',expires_ns=0 WHERE page_id=?`, scope.ArtifactID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM mooring_edit_lease_requests WHERE page_id=?`, scope.ArtifactID)
	return err
}
