package main

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

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
	for _, name := range oauthAppFlagNames {
		// The one bool in the set. Registering it with the same type the real
		// command uses is what lets a test drive the stdin path at all — a
		// FlagSet missing it makes GetBool/Changed answer "false" forever and
		// every stdin assertion below would pass vacuously.
		if name == "oauth-client-secret-stdin" {
			fs.Bool(name, false, "")
			continue
		}
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
			// THE REGRESSION. "Did the operator reach for an --oauth-* flag?"
			// used to be answered by looking at the resolved VALUES, so an
			// explicitly empty one answered "no" and the guard above never
			// fired: the flag was accepted and then dropped in silence, which
			// is the exact outcome the guard exists to prevent.
			name:     "an explicitly empty oauth flag on a non-OAUTH2 type is still a flag",
			credType: "API_KEY",
			flags:    map[string]string{"oauth-client-secret": ""},
			wantErr:  "only valid with --type OAUTH2",
			wantExit: cli.ExitValidation,
		},
		{
			// The same defect reached through the stdin source rather than
			// argv: an empty stream also resolves to "".
			name:     "an empty --oauth-client-secret-stdin on a non-OAUTH2 type is still a flag",
			credType: "API_KEY",
			flags:    map[string]string{"oauth-client-secret-stdin": "true"},
			wantErr:  "only valid with --type OAUTH2",
			wantExit: cli.ExitValidation,
		},
		{
			// Nothing but the stdin switch is still an app configuration, and
			// an app configuration without a client id cannot be authorized.
			// Before the fix this returned (nil, nil) and the row was created
			// as a bare OAUTH2 value credential.
			name:     "OAUTH2 with only the stdin switch is an app config, and an incomplete one",
			credType: "OAUTH2",
			flags:    map[string]string{"oauth-client-secret-stdin": "true"},
			wantErr:  "--oauth-client-id is required",
			wantExit: cli.ExitValidation,
		},
		{
			name:     "type is matched case-insensitively",
			credType: "oauth2",
			flags: map[string]string{
				"oauth-client-id": "cid",
				"oauth-auth-url":  "https://idp.example/authorize",
				"oauth-token-url": "https://idp.example/token",
				"oauth-scopes":    "read",
			},
			want: &oauthAppSpec{
				ClientID: "cid",
				AuthURL:  "https://idp.example/authorize",
				TokenURL: "https://idp.example/token",
				Scopes:   "read",
			},
		},
		{
			// The older, still-legal form: a token obtained elsewhere, filed by
			// hand as OAUTH2. Nobody reached for an --oauth-* flag, so nobody is
			// configuring an app, and the value-required path downstream owns it.
			name:     "OAUTH2 with no oauth flags at all is not an app config",
			credType: "OAUTH2",
			flags:    map[string]string{},
			wantNil:  true,
		},
		{
			name:     "OAUTH2 with endpoints but no client id has nothing to authorize as",
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
				// Fail, don't skip. The previous version `continue`d on an
				// unknown name and called that a guard against "a typo'd key
				// silently passing" — but skipping IS passing silently, and a
				// typo (`oauth-client-sec`) was already sitting in the table
				// below, asserting nothing while the case reported green.
				if flags.Lookup(name) == nil {
					t.Fatalf("no such flag %q — a typo here would otherwise assert nothing", name)
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

// The --type guard has to fire BEFORE the secret is resolved. Resolving it
// consumes os.Stdin, so a run that is about to exit 2 for its --type must not
// first swallow the stream — the operator would get the refusal and an empty
// pipe, with nothing left to pipe into the corrected command.
//
// Not parallel: it swaps os.Stdin.
func TestReadOAuthAppFlagsRefusesBeforeItConsumesStdin(t *testing.T) {
	feedStdin(t, "the-secret\n")

	flags := oauthAppFlagSet(t, nil)
	if err := flags.Set("oauth-client-secret-stdin", "true"); err != nil {
		t.Fatalf("set oauth-client-secret-stdin: %v", err)
	}

	got, err := readOAuthAppFlags(flags, "API_KEY")
	if err == nil {
		t.Fatalf("an --oauth-* flag on --type API_KEY was accepted: %+v", got)
	}
	if !strings.Contains(err.Error(), "only valid with --type OAUTH2") {
		t.Fatalf("error = %q, want the --type refusal", err)
	}

	rest, readErr := io.ReadAll(os.Stdin)
	if readErr != nil {
		t.Fatalf("read stdin: %v", readErr)
	}
	if strings.TrimSpace(string(rest)) != "the-secret" {
		t.Errorf("the refused run consumed stdin; what is left = %q", rest)
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

// nextPollDelay is the deadline arithmetic behind `oauth connect --timeout`,
// split out precisely so it can be checked without a clock: the original
// inline form gave up when now+interval reached the deadline, which throws
// away up to a full interval of the budget the operator asked for. With the
// default 2s interval that is a connect abandoning a flow ~2s before the
// server's loopback window actually closes.
func TestNextPollDelay(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		now       time.Time
		deadline  time.Time
		interval  time.Duration
		wantDelay time.Duration
		wantDone  bool
	}{
		{
			name:      "plenty of budget left",
			now:       base,
			deadline:  base.Add(10 * time.Second),
			interval:  2 * time.Second,
			wantDelay: 2 * time.Second,
		},
		{
			// THE REGRESSION. Less than one interval remains, but it is not
			// zero — the budget must be spent, not abandoned.
			name:      "less than one interval left is still time",
			now:       base.Add(9 * time.Second),
			deadline:  base.Add(10 * time.Second),
			interval:  2 * time.Second,
			wantDelay: 1 * time.Second,
		},
		{
			name:     "exactly at the deadline is done",
			now:      base.Add(10 * time.Second),
			deadline: base.Add(10 * time.Second),
			interval: 2 * time.Second,
			wantDone: true,
		},
		{
			name:     "past the deadline is done",
			now:      base.Add(11 * time.Second),
			deadline: base.Add(10 * time.Second),
			interval: 2 * time.Second,
			wantDone: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			delay, done := nextPollDelay(tc.now, tc.deadline, tc.interval)
			if done != tc.wantDone {
				t.Fatalf("done = %v, want %v", done, tc.wantDone)
			}
			if !done && delay != tc.wantDelay {
				t.Errorf("delay = %v, want %v", delay, tc.wantDelay)
			}
		})
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
