package api

import (
	"context"
	"database/sql"
	"errors"
	"net/url"

	"github.com/crewship-ai/crewship/internal/credhostgate"
	"github.com/crewship-ai/crewship/internal/providerlogin"
)

// Refreshing a token sends a credential's refresh token (and, for OAuth2, its
// client secret) to a token endpoint. For a host-bound credential (#5045) that
// endpoint must be one of the approved hosts; otherwise the refresh is skipped
// and the stored token stays as it is until it expires.

// errRefreshHostNotApproved: the login is bound to hosts that do not include
// its provider's token endpoint.
var errRefreshHostNotApproved = errors.New("this login is bound to approved hosts that do not include its token endpoint, so it is not refreshed")

// oauthRefreshPermitted reports whether credID's approved hosts cover the host
// of tokenURL. Any lookup or parse failure denies.
func oauthRefreshPermitted(ctx context.Context, db *sql.DB, credID, tokenURL string) bool {
	var allowed string
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(allowed_hosts, '[]') FROM credentials WHERE id = ?`, credID).Scan(&allowed); err != nil {
		return false
	}
	return tokenEndpointPermitted(allowed, tokenURL)
}

func tokenEndpointPermitted(allowed, endpoint string) bool {
	if _, bound, _ := credhostgate.Bound(allowed); !bound {
		return true
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" {
		return false
	}
	return credhostgate.PermitsHost(allowed, u.Hostname())
}

// loginRefreshPermitted applies the same rule to a provider login. Only a
// refresher whose endpoint is known can serve a host-bound login.
func loginRefreshPermitted(allowed string, tr providerlogin.TokenRefresher) bool {
	if _, bound, _ := credhostgate.Bound(allowed); !bound {
		return true
	}
	if or, ok := tr.(*providerlogin.OpenAIRefresher); ok && or != nil {
		return tokenEndpointPermitted(allowed, or.TokenURL)
	}
	return false
}
