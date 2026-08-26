package main

import (
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/crewship-ai/crewship/internal/cli"
)

// Unit coverage for the pieces of `crewship oauth` that are decidable without a
// server. The wire contract is asserted against the built binary in
// acceptance_oauth_connect_test.go; what is pinned here is the flag algebra
// that runs before any request is built, because those are the branches that
// must never reach the network.
//
// A local FlagSet is used rather than the shared credCreateCmd: executing or
// enumerating a shared cobra command from a test is a documented data race in
// this package (see cobra_sort_parallel_guard_test.go).

// oauthAppFlagSet mirrors the --oauth-* flags registered on `credential create`.
func oauthAppFlagSet(t *testing.T, set map[string]string) *pflag.FlagSet {
	t.Helper()
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	for _, name := range []string{
		"oauth-provider", "oauth-client-id", "oauth-client-secret",
		"oauth-auth-url", "oauth-token-url", "oauth-scopes",
	} {
		fs.String(name, "", "")
	}
	for name, value := range set {
		if err := fs.Set(name, value); err != nil {
			t.Fatalf("set %s: %v", name, err)
		}
	}
	return fs
}

func TestReadOAuthAppFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		credType string
		flags    map[string]string
		wantNil  bool
		wantErr  string
		wantExit int
		want     *oauthAppSpec
	}{
		{
			name:     "no oauth flags on a plain API key is not an oauth credential",
			credType: "API_KEY",
			flags:    map[string]string{},
			wantNil:  true,
		},
		{
			// The server drops oauth_* for a non-OAUTH2 row without a word, so
			// accepting them here would create a credential that silently
			// ignored half its arguments.
			name:     "oauth flags on a non-OAUTH2 type are refused",
			credType: "API_KEY",
			flags:    map[string]string{"oauth-client-id": "cid"},
			wantErr:  "only valid with --type OAUTH2",
			wantExit: cli.ExitValidation,
		},
		{
			name:     "type is matched case-insensitively",
			credType: "oauth2",
			flags: map[string]string{
				"oauth-client-id":  "cid",
				"oauth-auth-url":   "https://idp.example/authorize",
				"oauth-token-url":  "https://idp.example/token",
				"oauth-scopes":     "read",
				"oauth-client-sec": "",
			},
			want: &oauthAppSpec{
				ClientID: "cid",
				AuthURL:  "https://idp.example/authorize",
				TokenURL: "https://idp.example/token",
				Scopes:   "read",
			},
		},
		{
			name:     "OAUTH2 with no client id has nothing to authorize as",
			credType: "OAUTH2",
			flags: map[string]string{
				"oauth-auth-url":  "https://idp.example/authorize",
				"oauth-token-url": "https://idp.example/token",
			},
			wantErr:  "--oauth-client-id is required",
			wantExit: cli.ExitValidation,
		},
		{
			// No provider slug means no catalogue lookup, so the endpoints have
			// to come from the operator — and half of them is not enough.
			name:     "OAUTH2 with a client id but only one endpoint",
			credType: "OAUTH2",
			flags: map[string]string{
				"oauth-client-id": "cid",
				"oauth-auth-url":  "https://idp.example/authorize",
			},
			wantErr:  "--oauth-auth-url and --oauth-token-url are required",
			wantExit: cli.ExitValidation,
		},
		{
			name:     "explicit endpoints and secret",
			credType: "OAUTH2",
			flags: map[string]string{
				"oauth-client-id":     "cid",
				"oauth-client-secret": "shh",
				"oauth-auth-url":      "https://gitlab.acme.internal/oauth/authorize",
				"oauth-token-url":     "https://gitlab.acme.internal/oauth/token",
				"oauth-scopes":        "api read_user",
			},
			want: &oauthAppSpec{
				ClientID:     "cid",
				ClientSecret: "shh",
				AuthURL:      "https://gitlab.acme.internal/oauth/authorize",
				TokenURL:     "https://gitlab.acme.internal/oauth/token",
				Scopes:       "api read_user",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			flags := oauthAppFlagSet(t, nil)
			for name, value := range tc.flags {
				if flags.Lookup(name) == nil {
					continue // guards against a typo'd key silently passing
				}
				if err := flags.Set(name, value); err != nil {
					t.Fatalf("set %s: %v", name, err)
				}
			}

			got, err := readOAuthAppFlags(flags, tc.credType)

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error, got spec %+v", got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
				}
				if code := cli.ExitCodeFor(err); code != tc.wantExit {
					t.Errorf("exit code = %d, want %d", code, tc.wantExit)
				}
				if got != nil {
					t.Errorf("a rejected flag set still produced a spec: %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantNil {
				if got != nil {
					t.Errorf("expected no oauth spec, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected an oauth spec, got nil")
			}
			if *got != *tc.want {
				t.Errorf("spec = %+v, want %+v", *got, *tc.want)
			}
		})
	}
}

// apply must never write an empty secret or an empty scope string: the server
// treats a present-but-empty oauth_client_secret as a secret, and a public
// PKCE-only client legitimately has none.
func TestOAuthAppSpecApplyOmitsEmptyOptionalFields(t *testing.T) {
	t.Parallel()

	spec := &oauthAppSpec{
		ClientID: "cid",
		AuthURL:  "https://idp.example/authorize",
		TokenURL: "https://idp.example/token",
	}
	body := map[string]interface{}{}
	spec.apply(body)

	if body["oauth_client_id"] != "cid" {
		t.Errorf("oauth_client_id = %v", body["oauth_client_id"])
	}
	if _, ok := body["oauth_client_secret"]; ok {
		t.Errorf("an empty client secret was sent: %v", body["oauth_client_secret"])
	}
	if _, ok := body["oauth_scopes"]; ok {
		t.Errorf("an empty scope string was sent: %v", body["oauth_scopes"])
	}
	// The endpoints are not optional — a row without them cannot be authorized,
	// and readOAuthAppFlags has already refused that case.
	if body["oauth_auth_url"] == "" || body["oauth_token_url"] == "" {
		t.Errorf("endpoints missing from body: %v", body)
	}
}

// proposedPath is what turns a proposal id into a review URL. A path-traversing
// id must not escape the route.
func TestProposedPathEscapesTheID(t *testing.T) {
	t.Parallel()

	if got := proposedPath("mp_abc123", "explain"); got != "/api/v1/consolidate/proposed/mp_abc123/explain" {
		t.Errorf("proposedPath = %q", got)
	}
	got := proposedPath("../../admin", "approve")
	if strings.Contains(got, "../") {
		t.Errorf("proposedPath did not escape a traversing id: %q", got)
	}
}
