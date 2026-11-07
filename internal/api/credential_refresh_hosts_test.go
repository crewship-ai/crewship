package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/providerlogin"
)

// #5045: refreshing a token sends the refresh token and client secret to a
// token endpoint. A host-bound credential whose approved hosts do not cover
// that endpoint is never refreshed.

func oauthTokenServer(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(srv.Close)
	// The production client refuses loopback; this test owns the endpoint.
	prev := oauthTokenClient
	oauthTokenClient = srv.Client()
	t.Cleanup(func() { oauthTokenClient = prev })
	return srv, &hits
}

func TestOAuthRefresh_HostBoundTokenEndpointIsNotCalled(t *testing.T) {
	tests := []struct {
		name     string
		hosts    string
		wantHits int32
	}{
		{"unbound refreshes", `[]`, 1},
		{"bound to another host", `["idp.example.com"]`, 0},
		{"unreadable list", `not json`, 0},
	}
	for _, path := range []string{"sweep", "mcp"} {
		for _, tc := range tests {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				ensureEncryptionKey(t)
				db := setupTestDB(t)
				userID := seedTestUser(t, db)
				wsID := seedTestWorkspace(t, db, userID)
				srv, hits := oauthTokenServer(t)
				encRefresh, _ := encryption.Encrypt("rt-valid")
				encSecret, _ := encryption.Encrypt("client-secret")
				covOTSeedExpiring(t, db, wsID, userID, "cred-o", encSecret, encRefresh, srv.URL+"/token")
				execOrFatal(t, db, `UPDATE credentials SET allowed_hosts = ? WHERE id = 'cred-o'`, tc.hosts)
				if path == "sweep" {
					refreshExpiringTokens(context.Background(), db, nil, covOTLogger())
				} else {
					var enc, exp string
					if err := db.QueryRow(`SELECT encrypted_value, oauth_token_expires_at FROM credentials WHERE id = 'cred-o'`).Scan(&enc, &exp); err != nil {
						t.Fatal(err)
					}
					ensureFreshOAuthToken(context.Background(), db, covOTLogger(), "cred-o", enc,
						"client-id", encSecret, srv.URL+"/token", encRefresh, exp, "")
				}
				if got := atomic.LoadInt32(hits); got != tc.wantHits {
					t.Fatalf("token endpoint hits = %d, want %d", got, tc.wantHits)
				}
				if tc.wantHits == 0 {
					if got := covOTCredentialStatus(t, db, "cred-o"); got != "ACTIVE" {
						t.Errorf("status = %q, want ACTIVE (a skipped refresh does not expire the credential)", got)
					}
				}
			})
		}
	}
}

func TestProviderLoginRefresh_HostBoundLoginIsNotRefreshed(t *testing.T) {
	r := newPLRig(t)
	id := r.seedCodexLogin(t, "bound-login", 2*time.Minute)
	execOrFatal(t, r.db, `UPDATE credentials SET allowed_hosts = '["llm-proxy.example.com"]' WHERE id = ?`, id)
	if _, err := r.rf.Refresh(context.Background(), id, true); !errors.Is(err, errRefreshHostNotApproved) {
		t.Fatalf("Refresh err = %v, want errRefreshHostNotApproved", err)
	}
	if r.tokens.calls != 0 {
		t.Fatalf("token refresher called %d times, want 0", r.tokens.calls)
	}
}

func TestLoginRefreshPermitted(t *testing.T) {
	openai := providerlogin.NewOpenAIRefresher(nil)
	tests := []struct {
		name    string
		allowed string
		tr      providerlogin.TokenRefresher
		want    bool
	}{
		{"unbound", `[]`, openai, true},
		{"approves the token host", `["auth.openai.com"]`, openai, true},
		{"approves a wildcard covering it", `["*.openai.com"]`, openai, true},
		{"bound elsewhere", `["api.openai.com"]`, openai, false},
		{"unknown refresher, bound", `["auth.openai.com"]`, &fakeTokens{}, false},
		{"unknown refresher, unbound", `[]`, &fakeTokens{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := loginRefreshPermitted(tc.allowed, tc.tr); got != tc.want {
				t.Fatalf("loginRefreshPermitted = %v, want %v", got, tc.want)
			}
		})
	}
}
