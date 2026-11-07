package api

import (
	"context"
	"database/sql"
	"errors"
	"net/url"

	"github.com/crewship-ai/crewship/internal/credhostgate"
	"github.com/crewship-ai/crewship/internal/providerlogin"
)

// Refreshing a token, or exchanging an authorization code, sends a
// credential's secrets (refresh token, client secret, code and PKCE verifier)
// to a token endpoint. For a host-bound credential (#5045) that endpoint must
// be one of the approved hosts; otherwise the refresh is skipped and the
// stored token stays as it is until it expires, and an exchange is refused.

// errRefreshHostNotApproved: the login is bound to hosts that do not include
// its provider's token endpoint.
var errRefreshHostNotApproved = errors.New("this login is bound to approved hosts that do not include its token endpoint, so it is not refreshed")

// errTokenEndpointNotApproved: an OAuth2 credential is bound to approved hosts
// that do not include its token URL.
var errTokenEndpointNotApproved = errors.New("this credential is bound to approved hosts that do not include its token URL")

// exchangeOAuthCodeGated is the only way the OAuth flow exchanges a code: it
// refuses a host-bound credential whose approved hosts do not cover tokenURL.
func exchangeOAuthCodeGated(ctx context.Context, db *sql.DB, credID, tokenURL, clientID, clientSecret, code, redirectURI, codeVerifier, resource string) (*tokenResponse, error) {
	if !oauthTokenEndpointPermitted(ctx, db, credID, tokenURL) {
		return nil, errTokenEndpointNotApproved
	}
	return exchangeOAuthCode(ctx, tokenURL, clientID, clientSecret, code, redirectURI, codeVerifier, resource)
}

// oauthTokenEndpointPermitted reports whether credID's approved hosts cover
// the host of tokenURL. Any lookup or parse failure denies.
func oauthTokenEndpointPermitted(ctx context.Context, db *sql.DB, credID, tokenURL string) bool {
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
