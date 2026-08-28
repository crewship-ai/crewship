package main

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// `crewship oauth` — the CLI half of /api/v1/oauth/*, the flow that turns an
// OAUTH2 credential row into one that actually holds tokens.
//
// Shape of the surface, because it decides the shape of the commands. There
// are two legs, and they are alternatives, not steps:
//
//	connect    POST /oauth/loopback  — the server binds 127.0.0.1:0, hands
//	                                   back an authorize URL pointed at it, and
//	                                   a goroutine does the code exchange when
//	                                   the browser lands. Needs the browser on
//	                                   the same host as the API.
//	authorize  POST /oauth/initiate  — the server stores state + PKCE verifier
//	  + exchange POST /oauth/exchange  and hands back an authorize URL pointed
//	                                   at its own /oauth/callback. When the
//	                                   browser cannot reach the API host, the
//	                                   operator pastes the code back with
//	                                   `oauth exchange --code … --state …`.
//
// The `--state` on exchange is load-bearing and easy to lose: the server only
// recovers the PKCE code_verifier it stored during initiate when a state token
// comes back with the code (internal/api/oauth_flow.go:247). Omit it and the
// exchange goes out with no verifier, which a PKCE-enforcing provider (Google,
// Linear) rejects with invalid_grant. `oauth authorize` therefore prints the
// state next to the URL rather than burying it in --format json.
//
// There is no OAuth-specific "is it done yet" endpoint. The only truthful
// completion signal is the credential flipping to ACTIVE, which
// storeOAuthTokens does as it lands the tokens
// (internal/api/oauth_creds.go:85), so that is what `connect` waits on — and a
// wait that runs out is a non-zero exit, never a tick.

var oauthCmd = &cobra.Command{
	Use:     "oauth",
	Aliases: []string{"oauth2"},
	Short:   "Connect OAuth credentials and discover provider endpoints",
	Long: `Drive the OAuth connect flow for an OAUTH2 credential.

Two legs, pick the one your network allows:

  Browser on the same host as the API (laptop, dev box):
    crewship oauth connect my-linear-cred

  Browser elsewhere (headless server, remote API):
    crewship oauth authorize my-linear-cred      # prints URL + state
    # …complete consent in a browser, copy the ?code= from the redirect…
    crewship oauth exchange my-linear-cred --code <code> --state <state>

Before either, the credential must already exist with its OAuth app details:

  crewship credential create --name my-linear-cred --type OAUTH2 \
    --provider LINEAR --oauth-client-id <id> --oauth-client-secret <secret>

For an MCP server that supports Dynamic Client Registration, skip all of that:

  crewship oauth auto-connect https://mcp.example/sse --name example-mcp

Examples:
  crewship oauth providers
  crewship oauth discover https://mcp.example/sse
  crewship oauth connect my-linear-cred --timeout 3m
  crewship oauth connect my-linear-cred --no-wait

Note: the credential argument accepts a name or an ID.`,
}

// oauthProviderRow is the flattened catalogue row. The API returns a map keyed
// by provider slug; a map is awkward for a table and worse for a script that
// wants to iterate, so the slug is folded into the row and the result is
// emitted as a sorted array.
type oauthProviderRow struct {
	Provider      string `json:"provider"`
	AuthURL       string `json:"auth_url"`
	TokenURL      string `json:"token_url"`
	DefaultScopes string `json:"default_scopes"`
}

var oauthProvidersCmd = &cobra.Command{
	Use:     "providers",
	Aliases: []string{"provider-list"},
	Short:   "List the OAuth providers this build knows endpoints for",
	Long: `List the built-in OAuth provider catalogue: authorize URL, token URL and
default scopes for each provider Crewship ships endpoints for.

This is a static catalogue compiled into the server, not a list of providers
you have configured — it tells you which providers you can point a credential
at without looking up their endpoints yourself.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/oauth/providers")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var catalogue map[string]struct {
			AuthURL       string `json:"auth_url"`
			TokenURL      string `json:"token_url"`
			DefaultScopes string `json:"default_scopes"`
		}
		if err := cli.ReadJSON(resp, &catalogue); err != nil {
			return err
		}

		names := make([]string, 0, len(catalogue))
		for name := range catalogue {
			names = append(names, name)
		}
		sort.Strings(names)

		rows := make([]oauthProviderRow, 0, len(names))
		tableRows := make([][]string, 0, len(names))
		for _, name := range names {
			p := catalogue[name]
			rows = append(rows, oauthProviderRow{
				Provider:      name,
				AuthURL:       p.AuthURL,
				TokenURL:      p.TokenURL,
				DefaultScopes: p.DefaultScopes,
			})
			tableRows = append(tableRows, []string{
				name, p.AuthURL, p.TokenURL, derefStr(&p.DefaultScopes, "-"),
			})
		}

		f := newFormatter()
		return f.Auto(rows, []string{"PROVIDER", "AUTH URL", "TOKEN URL", "DEFAULT SCOPES"}, tableRows)
	},
}

var oauthAuthorizeCmd = &cobra.Command{
	Use:     "authorize <credential>",
	Aliases: []string{"initiate"},
	Short:   "Start an OAuth flow and print the authorize URL and state token",
	Long: `Start an OAuth flow for a credential and print the URL to open plus the
state token that goes with it.

Use this when the browser cannot reach the API host — the redirect lands on the
server's own /api/v1/oauth/callback, and if that is unreachable too, copy the
?code= out of the failed redirect and finish with:

  crewship oauth exchange <credential> --code <code> --state <state>

Keep the state token. The server stored the PKCE verifier against it, and the
exchange cannot recover that verifier without it.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		credID, err := resolveCredentialID(client, args[0])
		if err != nil {
			return err
		}

		body := map[string]string{"credential_id": credID}
		if redirect, _ := cmd.Flags().GetString("redirect-uri"); redirect != "" {
			body["redirect_uri"] = redirect
		}

		resp, err := client.Post("/api/v1/oauth/initiate", body)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var out struct {
			AuthURL string `json:"auth_url"`
			State   string `json:"state"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}

		f := newFormatter()
		return f.AutoHuman(out, func() {
			fmt.Println("Open this URL to authorize:")
			fmt.Println()
			fmt.Println("  " + out.AuthURL)
			fmt.Println()
			fmt.Printf("State token: %s\n", out.State)
			fmt.Printf("Finish with: crewship oauth exchange %s --code <code> --state %s\n",
				args[0], out.State)
		})
	},
}

var oauthExchangeCmd = &cobra.Command{
	Use:   "exchange <credential>",
	Short: "Exchange an authorization code for tokens",
	Long: `Exchange an authorization code for access and refresh tokens, storing them
encrypted against the credential.

--state is what lets the server recover the PKCE code_verifier it stored during
` + "`oauth authorize`" + `. Pass it whenever you have it; a provider that
enforces PKCE rejects an exchange without a verifier.

--code-verifier is the escape hatch for a flow this CLI did not start, where no
server-side state row exists to recover it from.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		code, _ := cmd.Flags().GetString("code")
		if strings.TrimSpace(code) == "" {
			return cli.WithExitCode(
				fmt.Errorf("--code is required (the ?code= parameter from the OAuth redirect)"),
				cli.ExitValidation)
		}

		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		credID, err := resolveCredentialID(client, args[0])
		if err != nil {
			return err
		}

		state, _ := cmd.Flags().GetString("state")
		verifier, _ := cmd.Flags().GetString("code-verifier")
		redirect, _ := cmd.Flags().GetString("redirect-uri")
		if state == "" && verifier == "" {
			// Not fatal — a provider that does not enforce PKCE will accept it,
			// and the loopback leg never had a state to give out. But an
			// invalid_grant from here is otherwise very hard to read.
			cli.PrintWarning("No --state and no --code-verifier: the exchange will run without a PKCE verifier, " +
				"which a provider that enforces PKCE rejects with invalid_grant.")
		}

		body := map[string]string{"credential_id": credID, "code": code}
		if state != "" {
			body["state"] = state
		}
		if verifier != "" {
			body["code_verifier"] = verifier
		}
		if redirect != "" {
			body["redirect_uri"] = redirect
		}

		resp, err := client.Post("/api/v1/oauth/exchange", body)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var out struct {
			Status       string `json:"status"`
			CredentialID string `json:"credential_id"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}

		f := newFormatter()
		return f.AutoHuman(out, func() {
			cli.PrintSuccess(fmt.Sprintf("Tokens stored for credential %s", out.CredentialID))
		})
	},
}

// oauthConnectResult is what `oauth connect` emits under --format json. It
// reports the terminal status verbatim rather than a bare boolean, so a script
// that times out can tell PENDING from ERROR without re-fetching.
type oauthConnectResult struct {
	CredentialID string `json:"credential_id"`
	AuthURL      string `json:"auth_url"`
	LoopbackPort int    `json:"loopback_port"`
	State        string `json:"state"`
	Status       string `json:"status"`
	Connected    bool   `json:"connected"`
	Waited       bool   `json:"waited"`
}

var oauthConnectCmd = &cobra.Command{
	Use:   "connect <credential>",
	Short: "Run the loopback OAuth flow and wait for the credential to connect",
	Long: `Run the loopback OAuth flow: the server opens a temporary listener on its
own 127.0.0.1, hands back an authorize URL pointed at it, and completes the
token exchange itself when the browser lands on the redirect.

The browser must therefore be on the same host as the API. If it is not, use
` + "`crewship oauth authorize`" + ` + ` + "`crewship oauth exchange`" + ` instead.

By default the command waits until the credential reports ACTIVE, because that
is the only signal that the tokens actually landed — there is no OAuth-specific
completion endpoint. A wait that runs out exits non-zero and names the status
the credential is stuck in; it does not report success.

Examples:
  crewship oauth connect my-linear-cred
  crewship oauth connect my-linear-cred --open
  crewship oauth connect my-linear-cred --no-wait
  crewship oauth connect my-linear-cred --timeout 3m --poll-interval 5s`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		timeout, _ := cmd.Flags().GetDuration("timeout")
		interval, _ := cmd.Flags().GetDuration("poll-interval")
		if interval <= 0 {
			return cli.WithExitCode(fmt.Errorf("--poll-interval must be positive"), cli.ExitValidation)
		}
		if timeout <= 0 {
			return cli.WithExitCode(fmt.Errorf("--timeout must be positive"), cli.ExitValidation)
		}

		client := newAPIClient()
		credID, err := resolveCredentialID(client, args[0])
		if err != nil {
			return err
		}

		resp, err := client.Post("/api/v1/oauth/loopback", map[string]string{"credential_id": credID})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var started struct {
			AuthURL      string `json:"auth_url"`
			LoopbackPort int    `json:"loopback_port"`
			State        string `json:"state"`
		}
		if err := cli.ReadJSON(resp, &started); err != nil {
			return err
		}

		result := oauthConnectResult{
			CredentialID: credID,
			AuthURL:      started.AuthURL,
			LoopbackPort: started.LoopbackPort,
			State:        started.State,
		}

		f := newFormatter()
		// Ask the formatter rather than restating its rule: classifying quiet
		// as machine-readable suppressed the authorize URL, without which the
		// flow cannot be completed at all, while still printing the success
		// line afterwards.
		human := f.RoutesToHuman()

		if human {
			fmt.Println("Open this URL to authorize (must be a browser on the API host):")
			fmt.Println()
			fmt.Println("  " + started.AuthURL)
			fmt.Println()
			fmt.Printf("Loopback listener: 127.0.0.1:%d\n", started.LoopbackPort)
		}
		if open, _ := cmd.Flags().GetBool("open"); open {
			if err := browserOpen(started.AuthURL); err != nil && human {
				cli.PrintWarning(fmt.Sprintf("Could not open a browser (%v) — use the URL above.", err))
			}
		}

		if noWait, _ := cmd.Flags().GetBool("no-wait"); noWait {
			// Deliberately no status fetch: the flow was started microseconds
			// ago, so the answer is PENDING by construction and the round trip
			// buys nothing. `connected` stays false because nothing has been
			// observed to be true.
			return f.AutoHuman(result, func() {
				fmt.Println()
				fmt.Printf("Not waiting. Check with: crewship credential get %s\n", args[0])
			})
		}

		result.Waited = true
		if human {
			fmt.Printf("Waiting up to %s for the credential to connect…\n", timeout)
		}

		status, err := waitForCredentialActive(client, credID, timeout, interval)
		result.Status = status
		result.Connected = status == "ACTIVE"
		if err != nil {
			// Emit the machine envelope before failing so a --format json
			// consumer still gets the status it was stuck in.
			if !human {
				_ = f.Machine(result)
			}
			return err
		}

		return f.AutoHuman(result, func() {
			cli.PrintSuccess(fmt.Sprintf("Credential %s connected (status ACTIVE)", args[0]))
		})
	},
}

// fetchCredentialStatus reads one credential's status column.
func fetchCredentialStatus(client *cli.Client, credID string) (string, error) {
	resp, err := client.Get("/api/v1/credentials/" + url.PathEscape(credID))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return "", err
	}
	var cred struct {
		Status string `json:"status"`
	}
	if err := cli.ReadJSON(resp, &cred); err != nil {
		return "", err
	}
	return cred.Status, nil
}

// nextPollDelay decides how long to wait before the next status poll, and
// whether the budget is spent.
//
// Split out of the loop so the arithmetic is testable without a clock. It used
// to be inline as `if !now.Add(interval).Before(deadline) { give up }`, which
// abandons up to a full interval of the operator's budget: with the default 2s
// interval that is a connect declaring failure ~2s before the server's loopback
// window actually closes, on a flow that may be one redirect away from landing.
func nextPollDelay(now, deadline time.Time, interval time.Duration) (time.Duration, bool) {
	if !now.Before(deadline) {
		return 0, true
	}
	if remaining := deadline.Sub(now); remaining < interval {
		return remaining, false
	}
	return interval, false
}

// waitForCredentialActive polls until the credential reports ACTIVE, the
// deadline passes, or the credential lands in a terminal non-ACTIVE state.
//
// It returns the last status it saw alongside the error, because "we gave up"
// and "the provider refused" are different operator problems and the status is
// the only thing that tells them apart.
//
// A read that fails is not automatically fatal. The loopback listener is
// one-shot and its PKCE verifier lives only in the server goroutine's closure,
// so abandoning the wait costs the operator the entire flow — including consent
// they may already have granted. A dropped keep-alive or a proxy 502 is
// therefore retried until the deadline, and only reported if nothing better
// ever arrives. A 401/403/404 is a different thing entirely: the credential is
// gone or we may not read it, and no amount of waiting changes that.
func waitForCredentialActive(client *cli.Client, credID string, timeout, interval time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	last := ""
	var lastErr error
	for {
		status, err := fetchCredentialStatus(client, credID)
		switch {
		case err == nil:
			lastErr = nil
			last = status
			switch status {
			case "ACTIVE":
				return status, nil
			case "REVOKED", "ERROR":
				return status, cli.WithExitCode(
					fmt.Errorf("credential %s is %s — the OAuth flow did not complete", credID, status),
					cli.ExitGeneric)
			}
		case isFatalPollError(err):
			return last, err
		default:
			lastErr = err
		}

		delay, done := nextPollDelay(time.Now(), deadline, interval)
		if done {
			if lastErr != nil {
				return last, fmt.Errorf(
					"gave up after %s: the credential's status could not be read: %w", timeout, lastErr)
			}
			return last, cli.WithExitCode(
				fmt.Errorf("timed out after %s waiting for credential %s to connect; it is still %s. "+
					"The authorization was never completed in a browser, or the browser could not reach "+
					"the loopback listener on the API host",
					timeout, credID, last),
				cli.ExitGeneric)
		}
		time.Sleep(delay)
	}
}

// isFatalPollError reports whether a failed status read is worth retrying.
// Auth and not-found are answers, not hiccups.
func isFatalPollError(err error) bool {
	switch cli.ExitCodeFor(err) {
	case cli.ExitAuth, cli.ExitNotFound, cli.ExitValidation:
		return true
	default:
		return false
	}
}

var oauthDiscoverCmd = &cobra.Command{
	Use:   "discover <mcp-url>",
	Short: "Discover a server's OAuth endpoints from its well-known documents",
	Long: `Fetch an MCP server's OAuth metadata (RFC 9728 protected-resource, then
RFC 8414 authorization-server) and report the endpoints it advertises.

Read-only, and the answer decides what to do next: supports_dcr true means
` + "`crewship oauth auto-connect`" + ` can register a client for you; false
means you have to create an OAuth app in the provider's own settings and pass
its client id to ` + "`crewship credential create`" + `.

A source of "known_provider" means discovery failed and the URL was matched
against the built-in catalogue instead — see ` + "`crewship oauth providers`" + `.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Post("/api/v1/oauth/discover", map[string]string{"mcp_url": args[0]})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var out struct {
			AuthURL              string `json:"auth_url"`
			TokenURL             string `json:"token_url"`
			RegistrationEndpoint string `json:"registration_endpoint"`
			Scopes               string `json:"scopes"`
			SupportsDCR          bool   `json:"supports_dcr"`
			SupportsPKCE         bool   `json:"supports_pkce"`
			Source               string `json:"source"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}

		f := newFormatter()
		return f.AutoDetail(out, [][]string{
			{"Authorize URL", out.AuthURL},
			{"Token URL", out.TokenURL},
			{"Registration endpoint", derefStr(&out.RegistrationEndpoint, "-")},
			{"Scopes", derefStr(&out.Scopes, "-")},
			{"Supports DCR", fmt.Sprintf("%v", out.SupportsDCR)},
			{"Supports PKCE", fmt.Sprintf("%v", out.SupportsPKCE)},
			{"Source", out.Source},
		})
	},
}

var oauthAutoConnectCmd = &cobra.Command{
	Use:   "auto-connect <mcp-url>",
	Short: "Discover, register a client, and create a PENDING OAuth credential",
	Long: `Discover an MCP server's OAuth endpoints, register a client with it via
Dynamic Client Registration (RFC 7591), create an OAUTH2 credential in PENDING
state, and print the URL to authorize.

This only works where the provider offers a registration endpoint. When it does
not, register an OAuth app with the provider and pass --oauth-client-id. The
server repeats discovery before creating the credential, so the protected MCP
resource and authorization-server issuer remain bound to the flow. Public PKCE
clients need no secret; confidential clients can read one from stdin with
--oauth-client-secret-stdin.

Finish the flow afterwards with:

  crewship oauth connect <credential-id>

Examples:
  crewship oauth auto-connect https://mcp.example/sse --name example-mcp
  crewship oauth auto-connect https://mcp.linear.app/sse --name linear`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		body := map[string]string{"mcp_url": args[0]}
		if name, _ := cmd.Flags().GetString("name"); name != "" {
			body["server_name"] = name
		}
		clientID, _ := cmd.Flags().GetString("oauth-client-id")
		if clientID == "" && (cmd.Flags().Changed("oauth-client-secret") || cmd.Flags().Changed("oauth-client-secret-stdin")) {
			return cli.WithExitCode(fmt.Errorf("--oauth-client-id is required when a client secret is provided"), cli.ExitValidation)
		}
		if clientID != "" {
			clientSecret, err := readOAuthClientSecret(cmd.Flags())
			if err != nil {
				return err
			}
			body["oauth_client_id"] = clientID
			if clientSecret != "" {
				body["oauth_client_secret"] = clientSecret
			}
		}
		// provider_hint is never sent — see the note in init().

		client := newAPIClient()
		resp, err := client.Post("/api/v1/oauth/auto-connect", body)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var out struct {
			Status       string `json:"status"`
			AuthURL      string `json:"auth_url"`
			TokenURL     string `json:"token_url"`
			Scopes       string `json:"scopes"`
			RedirectURI  string `json:"redirect_uri"`
			CredentialID string `json:"credential_id"`
			Message      string `json:"message"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}

		// needs_client_id is a 200 that created nothing. Reporting it as a
		// success would leave an operator looking for a credential that does
		// not exist.
		if out.Status != "authorize" {
			f := newFormatter()
			// Same predicate as the authorize path above. `f.Format != "table"`
			// counted quiet as machine here and as human there, so one flag
			// value produced an envelope in one command and not the other.
			if !f.RoutesToHuman() {
				_ = f.Machine(out)
			}
			msg := out.Message
			if msg == "" {
				msg = "the provider does not support Dynamic Client Registration"
			}
			redirectHint := ""
			if out.RedirectURI != "" {
				redirectHint = "\nRegister this redirect URI on the OAuth app: " + out.RedirectURI
			}
			return cli.WithExitCode(fmt.Errorf(
				"no credential was created: %s%s\n"+
					"Create an OAuth app in the provider's settings, then retry this command with "+
					"--oauth-client-id <id> (and --oauth-client-secret-stdin when required).",
				msg, redirectHint), cli.ExitValidation)
		}

		f := newFormatter()
		return f.AutoHuman(out, func() {
			cli.PrintSuccess("Credential " + out.CredentialID + " created (PENDING)")
			fmt.Println()
			fmt.Println("Open this URL to authorize:")
			fmt.Println()
			fmt.Println("  " + out.AuthURL)
			fmt.Println()
			fmt.Printf("Or run: crewship oauth connect %s\n", out.CredentialID)
		})
	},
}

// oauthAppSpec is the OAuth *application* half of an OAUTH2 credential: the
// client registered with the provider, and the endpoints its flow runs against.
// It is not a secret the agent ever sees — the tokens the flow fetches are.
type oauthAppSpec struct {
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	Scopes       string
}

// apply writes the spec onto a POST /api/v1/credentials body.
func (s *oauthAppSpec) apply(body map[string]interface{}) {
	body["oauth_client_id"] = s.ClientID
	if s.ClientSecret != "" {
		body["oauth_client_secret"] = s.ClientSecret
	}
	body["oauth_auth_url"] = s.AuthURL
	body["oauth_token_url"] = s.TokenURL
	if s.Scopes != "" {
		body["oauth_scopes"] = s.Scopes
	}
}

// readOAuthAppFlags resolves the --oauth-* flags on `credential create` into a
// spec, or returns (nil, nil) when this is not an OAuth credential.
//
// It exists because POST /api/v1/credentials has accepted oauth_client_id and
// its endpoints since the OAuth flow was written, and the CLI exposed none of
// them — so an OAUTH2 row could only be minted through the web UI, and every
// `crewship oauth` command below had nothing to operate on (#2086).
//
// --oauth-provider fills the endpoints from the same catalogue
// `crewship oauth providers` prints, which is the whole point of shipping a
// catalogue; explicit --oauth-auth-url/--oauth-token-url override it and are
// the path for a provider the catalogue does not carry.
// readOAuthClientSecret resolves the OAuth app client secret from
// --oauth-client-secret or --oauth-client-secret-stdin.
//
// Every other secret-bearing flag on `credential create` already had this pair
// — --value/--value-stdin and --auth-token/--auth-token-stdin — because an
// argument is readable by anything that can see the process table for as long
// as the command runs, and lands in shell history besides. The client secret
// shipped without one, despite being the longest-lived secret of the three: an
// access token expires, an app's client secret does not until it is rotated.
//
// Unlike --auth-token-stdin there is no merge to protect, so empty input is
// simply "no secret" — a public PKCE-only client is a legitimate configuration.
func readOAuthClientSecret(flags *pflag.FlagSet) (string, error) {
	secret, _ := flags.GetString("oauth-client-secret")
	fromStdin, _ := flags.GetBool("oauth-client-secret-stdin")
	if !fromStdin {
		return secret, nil
	}
	if flags.Changed("oauth-client-secret") {
		return "", cli.WithExitCode(
			fmt.Errorf("--oauth-client-secret and --oauth-client-secret-stdin are mutually exclusive"),
			cli.ExitValidation)
	}
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		secret = scanner.Text()
	}
	if err := scanner.Err(); err != nil {
		return "", cli.WithExitCode(
			fmt.Errorf("read --oauth-client-secret-stdin: %w", err), cli.ExitValidation)
	}
	return secret, nil
}

// oauthAppFlagNames is every flag readOAuthAppFlags consults, listed once so
// the "did the operator reach for one of these?" question below cannot drift
// from the set of flags that actually feed the spec.
var oauthAppFlagNames = []string{
	"oauth-provider",
	"oauth-client-id",
	"oauth-client-secret",
	"oauth-client-secret-stdin",
	"oauth-auth-url",
	"oauth-token-url",
	"oauth-scopes",
}

// anyOAuthAppFlagSet reports whether the operator reached for an --oauth-*
// flag — which is not the same question as whether one carried a value.
//
// Reading it off the resolved values instead let an explicitly empty source
// disappear: `--oauth-client-secret-stdin < /dev/null` and
// `--oauth-client-secret ""` both resolve to "", so
// `credential create --type API_KEY --value v --oauth-client-secret-stdin`
// slipped past the --type guard and created an API key with the OAuth option
// silently dropped — exactly the silent-drop the guard exists to prevent.
func anyOAuthAppFlagSet(flags *pflag.FlagSet) bool {
	for _, name := range oauthAppFlagNames {
		if flags.Changed(name) {
			return true
		}
	}
	return false
}

func readOAuthAppFlags(flags *pflag.FlagSet, credType string) (*oauthAppSpec, error) {
	anySet := anyOAuthAppFlagSet(flags)

	if !strings.EqualFold(credType, "OAUTH2") {
		if anySet {
			// The server drops these fields for a non-OAUTH2 row without
			// complaining, so accepting them here would create a credential
			// that quietly ignored half its arguments.
			return nil, cli.WithExitCode(fmt.Errorf(
				"--oauth-* flags are only valid with --type OAUTH2; --type %s stores a value, not an OAuth app",
				credType), cli.ExitValidation)
		}
		return nil, nil
	}

	// An OAUTH2 credential created with a value and no app flags is the older,
	// still-legal form — a token obtained elsewhere, filed by hand. Only an
	// operator who actually reached for an --oauth-* flag is configuring an
	// app, so only they are held to the app's requirements.
	if !anySet {
		return nil, nil
	}

	providerSlug, _ := flags.GetString("oauth-provider")
	clientID, _ := flags.GetString("oauth-client-id")
	authURL, _ := flags.GetString("oauth-auth-url")
	tokenURL, _ := flags.GetString("oauth-token-url")
	scopes, _ := flags.GetString("oauth-scopes")

	if clientID == "" {
		return nil, cli.WithExitCode(fmt.Errorf(
			"--oauth-client-id is required for --type OAUTH2: without it there is no client to authorize as. "+
				"Register an OAuth app with the provider first, then pass its client id here"),
			cli.ExitValidation)
	}

	// Read last, deliberately. Resolving the secret consumes stdin, and a run
	// that is about to be refused for its --type or for a missing client id
	// must not first swallow a stream another flag was going to read.
	clientSecret, err := readOAuthClientSecret(flags)
	if err != nil {
		return nil, err
	}

	spec := &oauthAppSpec{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		AuthURL:      authURL,
		TokenURL:     tokenURL,
		Scopes:       scopes,
	}

	if providerSlug != "" {
		catalogue, err := fetchOAuthProviders()
		if err != nil {
			return nil, err
		}
		p, ok := catalogue[strings.ToLower(providerSlug)]
		if !ok {
			known := make([]string, 0, len(catalogue))
			for name := range catalogue {
				known = append(known, name)
			}
			sort.Strings(known)
			return nil, cli.WithExitCode(fmt.Errorf(
				"unknown --oauth-provider %q; the catalogue carries: %s.\n"+
					"For a provider that is not listed, pass --oauth-auth-url and --oauth-token-url instead",
				providerSlug, strings.Join(known, ", ")), cli.ExitValidation)
		}
		// Explicit flags win: a self-hosted GitLab is still "gitlab" for scopes
		// but its endpoints are not gitlab.com's.
		if spec.AuthURL == "" {
			spec.AuthURL = p.AuthURL
		}
		if spec.TokenURL == "" {
			spec.TokenURL = p.TokenURL
		}
		if spec.Scopes == "" {
			spec.Scopes = p.DefaultScopes
		}
	}

	if spec.AuthURL == "" || spec.TokenURL == "" {
		return nil, cli.WithExitCode(fmt.Errorf(
			"--oauth-auth-url and --oauth-token-url are required (or use --oauth-provider to take them "+
				"from the catalogue — see `crewship oauth providers`)"), cli.ExitValidation)
	}

	return spec, nil
}

// oauthCatalogueEntry is one row of GET /api/v1/oauth/providers.
type oauthCatalogueEntry struct {
	AuthURL       string `json:"auth_url"`
	TokenURL      string `json:"token_url"`
	DefaultScopes string `json:"default_scopes"`
}

// fetchOAuthProviders reads the server's built-in provider catalogue.
func fetchOAuthProviders() (map[string]oauthCatalogueEntry, error) {
	client := newAPIClient()
	resp, err := client.Get("/api/v1/oauth/providers")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var catalogue map[string]oauthCatalogueEntry
	if err := cli.ReadJSON(resp, &catalogue); err != nil {
		return nil, err
	}
	return catalogue, nil
}

func init() {
	oauthAuthorizeCmd.Flags().String("redirect-uri", "",
		"Override the redirect URI (default: the server's own /api/v1/oauth/callback)")

	oauthExchangeCmd.Flags().String("code", "", "Authorization code from the OAuth redirect (required)")
	oauthExchangeCmd.Flags().String("state", "",
		"State token from `oauth authorize` — lets the server recover the stored PKCE verifier")
	oauthExchangeCmd.Flags().String("code-verifier", "",
		"PKCE code verifier, for a flow this CLI did not start")
	oauthExchangeCmd.Flags().String("redirect-uri", "",
		"Redirect URI the code was issued for, when it differs from the stored one")

	// 3m against the server's 120s loopback window. The listener is torn down
	// at 120s (internal/api/oauth_flow.go:461), so the wait has to outlast it
	// to report "the window closed" rather than race it — 2m is exactly equal
	// and loses that race on any scheduling jitter.
	oauthConnectCmd.Flags().Duration("timeout", 3*time.Minute,
		"How long to wait for the credential to reach ACTIVE")
	oauthConnectCmd.Flags().Duration("poll-interval", 2*time.Second,
		"How often to re-check the credential status while waiting")
	oauthConnectCmd.Flags().Bool("no-wait", false,
		"Print the authorize URL and exit without waiting for the flow to complete")
	oauthConnectCmd.Flags().Bool("open", false, "Open the authorize URL in a browser")

	oauthAutoConnectCmd.Flags().String("name", "", "Name for the MCP server (default: mcp-server)")
	oauthAutoConnectCmd.Flags().String("oauth-client-id", "", "Existing OAuth app client ID when the provider has no dynamic registration")
	oauthAutoConnectCmd.Flags().String("oauth-client-secret", "", "OAuth app client secret (prefer --oauth-client-secret-stdin)")
	oauthAutoConnectCmd.Flags().Bool("oauth-client-secret-stdin", false, "Read the OAuth app client secret from stdin")
	// Deliberately no --provider. The endpoint takes a provider_hint, but
	// sending one fills authURL from the catalogue, which makes AutoConnect
	// skip discovery (internal/api/oauth_creds.go:185-192), which leaves
	// registrationEndpoint empty, which makes DCR impossible — so every
	// hinted call returns needs_client_id and no credential is ever created.
	// A flag that can only fail is worse than no flag.

	oauthCmd.AddCommand(oauthProvidersCmd)
	oauthCmd.AddCommand(oauthAuthorizeCmd)
	oauthCmd.AddCommand(oauthExchangeCmd)
	oauthCmd.AddCommand(oauthConnectCmd)
	oauthCmd.AddCommand(oauthDiscoverCmd)
	oauthCmd.AddCommand(oauthAutoConnectCmd)
}
