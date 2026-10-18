package api

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
)

func earlierExpiry(a, b time.Time) time.Time {
	if a.IsZero() || (!b.IsZero() && b.Before(a)) {
		return b
	}
	return a
}

// authenticateStream validates the row identified by a signed WS ticket. Capture
// the epoch before reading: a revoke between the read and Track cannot admit an
// already-invalid credential. No database work is repeated per frame.
func (m *AuthMiddleware) authenticateStream(ctx context.Context, c *auth.Claims) (context.Context, func(), error) {
	epoch := m.streams.Epoch()
	if (c.Sid == "") == (c.CLITokenID == "") {
		return nil, nil, errors.New("missing stream credential")
	}
	var expiry time.Time
	if c.Sid != "" {
		s, err := m.sessions.Get(ctx, c.Sid)
		if err != nil {
			return nil, nil, err
		}
		if s.UserID != c.ID || !s.Active(time.Now()) {
			return nil, nil, errors.New("inactive stream session")
		}
		expiry = s.ExpiresAt
	} else {
		var expires, revoked sql.NullString
		var suspended sql.NullString
		err := m.db.QueryRowContext(ctx, `SELECT ct.expires_at,ct.revoked_at,u.suspended_at FROM cli_tokens ct JOIN users u ON u.id=ct.user_id WHERE ct.id=? AND ct.user_id=?`, c.CLITokenID, c.ID).Scan(&expires, &revoked, &suspended)
		if err != nil {
			return nil, nil, err
		}
		if revoked.Valid || suspended.Valid {
			return nil, nil, errors.New("inactive stream token")
		}
		if expires.Valid {
			expiry, err = time.Parse(time.RFC3339, expires.String)
			if err != nil {
				return nil, nil, err
			}
		}
	}
	if c.CredentialExpiresAt > 0 {
		expiry = earlierExpiry(expiry, time.Unix(c.CredentialExpiresAt, 0))
	}
	tracked, release := m.streams.Track(ctx, epoch, c.ID, c.Sid, c.CLITokenID, expiry)
	if !auth.StreamCredentialCurrent(tracked) {
		release()
		return nil, nil, errors.New("stream credential changed")
	}
	return tracked, release, nil
}
