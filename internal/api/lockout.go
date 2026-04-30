package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Per-account brute-force lockout. Layered on TOP of the per-IP rate
// limiter — that limiter slows down a single IP, but a botnet rotating
// IPs can still hammer one email at full speed. The lockout watches the
// account itself and freezes signin attempts after N consecutive
// failures regardless of where they come from.
//
// Counter-only-on-fail with reset-on-success is the simplest pattern
// that doesn't accidentally lock out legitimate users juggling
// password-managers; it costs O(1) writes per failed attempt and zero
// per success (we just zero the counter when it was non-zero).
const (
	// LockoutThreshold is the number of consecutive failed signin
	// attempts before the account is locked. NIST SP 800-63B suggests
	// "no fewer than 100" for online attacks at 1-per-min throughput,
	// but our per-IP limit is 10/min so an attacker can already manage
	// at most ~14k/day per IP. 10 is a tight balance: enough room for
	// honest fat-fingering, low enough to materially impede single-IP
	// dictionary attacks. Tunable via env in production.
	LockoutThreshold = 10

	// LockoutDuration is how long an account stays locked after
	// hitting the threshold. Short enough that a legitimate user
	// who fat-fingered themselves into a lock can recover by
	// waiting; long enough that an attacker hitting the lock 10×
	// in a row has spent an hour, not an afternoon.
	LockoutDuration = 5 * time.Minute
)

// ErrAccountLocked is the typed error returned by signin when the
// account in question has hit the lockout threshold and is in its
// cooldown window. Differs from ErrInvalidCredentials so the handler
// can return a clearer 423 Locked response, but the response body
// deliberately doesn't leak which it is — both surface as the same
// "CredentialsSignin" string to the client. We log the lock distinctly.
var ErrAccountLocked = errors.New("account locked")

// ErrInvalidCredentials wraps wrong password / unknown email so the
// signin handler can react without leaking which one it was.
var ErrInvalidCredentials = errors.New("invalid credentials")

// checkAndLockoutOnFail consolidates the "look up user, verify password,
// update lockout counters" flow into one function so signin can call
// it without spreading those concerns across the handler.
//
// Returns the userID and full_name on success; on failure returns one
// of ErrAccountLocked / ErrInvalidCredentials. The DB row's lockout
// state is checked BEFORE password verification — that means a locked
// account fails fast without burning a bcrypt cycle on every attacker
// guess (which is the whole point of locking).
func checkAndLockoutOnFail(ctx context.Context, db *sql.DB, email, password string, now time.Time) (userID, fullName string, err error) {
	var (
		hashedPw      string
		failedCount   int
		lockedUntilNS sql.NullString
	)
	row := db.QueryRowContext(ctx,
		`SELECT id, full_name, hashed_password, failed_login_count, locked_until
		   FROM users
		  WHERE email = ?`, email,
	)
	if err := row.Scan(&userID, &fullName, &hashedPw, &failedCount, &lockedUntilNS); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Unknown email path. We deliberately do NOT advance
			// any counter (no row to advance, and storing per-
			// email-string failure counts for non-existing emails
			// would let an attacker learn email enumeration data
			// just by attempting the same string twice). Just
			// return the generic invalid-credentials error and
			// let the per-IP rate limiter slow them down.
			return "", "", ErrInvalidCredentials
		}
		return "", "", fmt.Errorf("lockout: query user: %w", err)
	}

	if lockedUntilNS.Valid && lockedUntilNS.String != "" {
		lockedUntil, perr := time.Parse(time.RFC3339, lockedUntilNS.String)
		if perr == nil && now.Before(lockedUntil) {
			return "", "", ErrAccountLocked
		}
		// Lock window has expired; clear the lock and the counter so
		// the legitimate user gets a fresh slate. We don't advance
		// the counter here — the password check below will take it
		// to 1 if they get it wrong again.
		if _, ce := db.ExecContext(ctx,
			`UPDATE users SET locked_until = NULL, failed_login_count = 0 WHERE id = ?`,
			userID,
		); ce != nil {
			return "", "", fmt.Errorf("lockout: clear expired: %w", ce)
		}
		failedCount = 0
	}

	if compareErr := bcryptCompareHashAndPassword(hashedPw, password); compareErr != nil {
		// Distinguish "wrong password" from "the bcrypt comparison
		// itself failed" (corrupt hash, malformed prefix, hash too
		// short to be valid bcrypt). Only the first kind should
		// advance the lockout counter — locking a user out because
		// their stored hash got truncated by a botched DB migration
		// is a denial-of-service against the legitimate owner.
		if !errors.Is(compareErr, bcrypt.ErrMismatchedHashAndPassword) {
			return "", "", fmt.Errorf("lockout: bcrypt compare: %w", compareErr)
		}

		// Wrong password. Update the counter atomically — the previous
		// read-modify-write was racy: two concurrent bad-password
		// attempts could both read failedCount=N, both write N+1, and
		// the lockout would advance by 1 instead of 2. Under the exact
		// parallel attack pattern this code is meant to stop, that
		// keeps the threshold artificially out of reach.
		//
		// SQLite supports `UPDATE ... RETURNING` since 3.35 (modernc
		// driver carries it). One round-trip + one row written.
		const q = `
			UPDATE users
			   SET failed_login_count = failed_login_count + 1,
			       last_failed_login_at = ?,
			       locked_until = CASE
			           WHEN failed_login_count + 1 >= ? THEN ?
			           ELSE locked_until
			       END
			 WHERE id = ?
		 RETURNING failed_login_count, locked_until`
		lockedUntilStr := now.Add(LockoutDuration).UTC().Format(time.RFC3339)
		var newCount int
		var newLockedUntil sql.NullString
		if scanErr := db.QueryRowContext(ctx, q,
			now.UTC().Format(time.RFC3339), LockoutThreshold, lockedUntilStr, userID,
		).Scan(&newCount, &newLockedUntil); scanErr != nil {
			return "", "", fmt.Errorf("lockout: bump: %w", scanErr)
		}
		if newCount >= LockoutThreshold {
			return "", "", ErrAccountLocked
		}
		return "", "", ErrInvalidCredentials
	}

	// Successful login: zero the counter if it was non-zero. Skip the
	// write when it was already zero (most logins) to keep the happy
	// path write-free.
	if failedCount > 0 {
		if _, re := db.ExecContext(ctx,
			`UPDATE users
			    SET failed_login_count = 0,
			        locked_until = NULL
			  WHERE id = ?`, userID,
		); re != nil {
			return "", "", fmt.Errorf("lockout: reset: %w", re)
		}
	}
	return userID, fullName, nil
}

// bcryptCompareHashAndPassword is a thin wrapper over bcrypt with the
// concrete dependency hidden so tests can inject a stub. (We do not
// stub it currently; the indirection costs nothing and keeps the door
// open for a deterministic test bcrypt later.)
var bcryptCompareHashAndPassword = func(hash, password string) error {
	return bcryptCompareImpl(hash, password)
}
