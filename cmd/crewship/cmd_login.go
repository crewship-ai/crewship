package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/crewship-ai/crewship/internal/cli"
)

var (
	loginTokenFlag   string
	loginGoogleFlag  bool
	loginPairFlag    bool
	loginCodeFlag    string
	loginAdapterHint string
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate CLI with the Crewship server",
	Long: `Authenticate the CLI with a Crewship server.

Interactive mode (email + password):
  crewship login

Token mode (API token):
  crewship login --token <api-token>

Google OAuth (browser-based; finishes the flow in the web UI, then
paste the CLI token printed at the end):
  crewship login --google

Device-code pairing (paste the code shown in the browser onboarding
or Settings → Pair CLI):
  crewship login --pair --code=XXXX-XXXX`,
	RunE: func(cmd *cobra.Command, args []string) error {
		server := cli.ResolveServer(flagServer, cliCfg)

		// Pre-flight transport-security check. Fail loud on a malformed
		// URL or empty host BEFORE the bearer / password hits the wire
		// against the wrong destination. Warn (don't block) on plaintext
		// HTTP to a non-loopback host so the operator notices the
		// cleartext exposure but isn't blocked from intentional choices
		// (dev VM, internal LAN behind a VPN, etc.).
		if err := preflightServerURL(cmd.ErrOrStderr(), server); err != nil {
			return err
		}

		if loginPairFlag {
			return loginWithPairing(server, loginCodeFlag, loginAdapterHint)
		}
		if loginTokenFlag != "" {
			return loginWithToken(server, loginTokenFlag)
		}
		if loginGoogleFlag {
			return loginWithGoogle(server)
		}
		return loginInteractive(server)
	},
}

// preflightServerURL validates the --server URL right before login
// dispatch. The check is intentionally CLI-local (not in
// cli.NewClient) because NewClient is also used for non-credential
// requests where the warning would be noise (e.g. whoami, list, get).
// At login time the bearer / password is about to ride the
// connection, so the audit pays for itself.
//
// Returns:
//   - nil + no output         → URL is fine (https, loopback http)
//   - nil + stderr warning    → plaintext HTTP to a non-loopback host
//   - error                   → URL is structurally broken (block login)
//
// The classification mirrors checkCLIConfigServerScheme in
// cmd_doctor_security.go; the two intentionally don't share a helper
// because that file uses //go:build !clionly and cmd_login.go is
// build-tag-free (CLI-only and full builds both need login). Ten
// duplicated lines beats the build-tag reshuffle.
func preflightServerURL(out io.Writer, serverURL string) error {
	raw := strings.TrimSpace(serverURL)
	if raw == "" {
		return fmt.Errorf("--server is empty (set the URL flag or run 'crewship login --server https://…')")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid --server URL %q: %w", raw, err)
	}
	if strings.TrimSpace(u.Hostname()) == "" {
		return fmt.Errorf("--server %q is missing a host (use https://host[:port])", raw)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("--server scheme %q is unsupported (use http or https)", u.Scheme)
	}
	if scheme == "https" {
		return nil
	}
	// HTTP: loopback is fine (dev workflow), non-loopback gets the warning.
	if isLoopbackHost(u.Hostname()) {
		return nil
	}
	fmt.Fprintf(out, "%s⚠ %s is reached over plaintext HTTP%s — credentials are about to be sent in the clear.\n",
		cli.Yellow, u.Host, cli.Reset)
	fmt.Fprintln(out, "  Recommended: move the server behind TLS (Caddy / nginx + Let's Encrypt),")
	fmt.Fprintln(out, "               or SSH-tunnel so the connection stays loopback.")
	fmt.Fprintln(out, "  Proceeding anyway in 1 second — Ctrl-C to abort.")
	// Brief sleep so the warning is visible even when subsequent prompts
	// (Email:, Password:) immediately overwrite the terminal. 1 s is short
	// enough not to annoy the scripted path; the user can still abort.
	time.Sleep(1 * time.Second)
	return nil
}

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove stored authentication token",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := cli.LoadConfig()
		if err != nil {
			return err
		}
		cfg.Token = ""
		if err := cli.SaveConfig(cfg); err != nil {
			return err
		}
		cli.PrintSuccess("Logged out successfully.")
		return nil
	},
}

var whoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Display current user and workspace info",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/workspaces")
		if err != nil {
			return fmt.Errorf("failed to connect to server: %w", err)
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var workspaces []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Slug string `json:"slug"`
			Role string `json:"currentUserRole"`
		}
		if err := cli.ReadJSON(resp, &workspaces); err != nil {
			return err
		}

		// Get user info from the validate endpoint (if CLI token) or infer from workspace
		server := cli.ResolveServer(flagServer, cliCfg)
		activeWS := cli.ResolveWorkspace(flagWorkspace, cliCfg)

		// Try to get user info from CLI token validation
		validateResp, err := client.Get("/api/v1/auth/cli-token/validate")
		if err == nil && validateResp.StatusCode == 200 {
			var userInfo struct {
				UserEmail string `json:"user_email"`
				UserID    string `json:"user_id"`
			}
			if err := cli.ReadJSON(validateResp, &userInfo); err == nil {
				fmt.Printf("%sUser:%s      %s\n", cli.Bold, cli.Reset, userInfo.UserEmail)
			}
		} else if validateResp != nil {
			validateResp.Body.Close()
		}

		fmt.Printf("%sServer:%s    %s\n", cli.Bold, cli.Reset, server)

		if activeWS != "" {
			for _, ws := range workspaces {
				if ws.Slug == activeWS || ws.ID == activeWS {
					fmt.Printf("%sWorkspace:%s %s (%s)\n", cli.Bold, cli.Reset, ws.Name, ws.Slug)
					fmt.Printf("%sRole:%s      %s\n", cli.Bold, cli.Reset, ws.Role)
					break
				}
			}
		} else if len(workspaces) > 0 {
			fmt.Printf("%sWorkspaces:%s %d available (none selected, use 'crewship workspace use <slug>')\n",
				cli.Bold, cli.Reset, len(workspaces))
		}

		return nil
	},
}

func init() {
	loginCmd.Flags().StringVar(&loginTokenFlag, "token", "", "API token for non-interactive login")
	loginCmd.Flags().BoolVar(&loginGoogleFlag, "google", false, "Sign in via Google OAuth (browser flow, finishes in the web UI)")
	loginCmd.Flags().BoolVar(&loginPairFlag, "pair", false, "Redeem a device-code shown in the browser (use with --code)")
	loginCmd.Flags().StringVar(&loginCodeFlag, "code", "", "Pairing code from the browser (with --pair)")
	loginCmd.Flags().StringVar(&loginAdapterHint, "adapter", "", "Optional adapter hint (telemetry): CLAUDE_CODE | GEMINI_CLI | CODEX_CLI | OPENCODE | CURSOR_CLI | FACTORY_DROID")
}

// loginWithPairing redeems a device-code shown in the browser
// onboarding. Adapter-blind: the --adapter flag is telemetry only;
// the server never routes on it, so a future 7th CLI adapter needs
// only a frontend registry entry, no backend change.
func loginWithPairing(serverURL, code, adapterHint string) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return fmt.Errorf("--code is required (paste the code shown in the browser, e.g. K3F9-X2NM)")
	}

	client := cli.NewClient(serverURL, "", "")
	payload := map[string]string{"code": code}
	if adapterHint != "" {
		payload["adapter_hint"] = adapterHint
	}

	resp, err := client.Post("/api/v1/auth/pair/redeem", payload)
	if err != nil {
		// Wrap %w so the original transport error (DNS NXDOMAIN,
		// connection refused, TLS handshake failure, etc.) survives
		// — otherwise the user sees the same generic hint regardless
		// of whether their DNS is broken or the server is just down.
		return fmt.Errorf("could not reach %s — is the server running and the address correct? %w", serverURL, err)
	}
	defer resp.Body.Close()

	if err := cli.CheckError(resp); err != nil {
		// The redeem endpoint folds "wrong code" / "expired code" /
		// "already consumed" into the same generic message to avoid
		// being an enumeration oracle. The most likely cause from a
		// user's perspective is the 10-minute TTL — call that out
		// directly so they know to grab a fresh code.
		msg := err.Error()
		if strings.Contains(msg, "Invalid or expired code") {
			return fmt.Errorf("the pair code didn't work — codes expire after 10 minutes; go back to the browser and click \"get a new one\"")
		}
		return fmt.Errorf("pair: %w", err)
	}

	var redeem struct {
		CliToken string `json:"cli_token"`
		Email    string `json:"email"`
	}
	if err := cli.ReadJSON(resp, &redeem); err != nil {
		return fmt.Errorf("parse redeem response: %w", err)
	}
	if redeem.CliToken == "" {
		return fmt.Errorf("server returned empty cli_token — please report this to ops")
	}

	// LoadConfig returns (&CLIConfig{}, nil) when the file is missing
	// — see internal/cli/config.go. A non-nil error therefore means
	// the file *exists* but is unreadable / malformed YAML, and
	// silently overwriting it would mask the real cause (typo, bad
	// edit, permissions) and lose the user's other config. Surface it.
	cfg, err := cli.LoadConfig()
	if err != nil {
		return fmt.Errorf("load CLI config (refusing to overwrite a malformed file — fix or remove ~/.crewship/cli-config.yaml and retry): %w", err)
	}
	cfg.Server = serverURL
	cfg.Token = redeem.CliToken
	if err := cli.SaveConfig(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	cli.PrintSuccess(fmt.Sprintf("Paired as %s. Token saved to ~/.crewship/cli-config.yaml", redeem.Email))

	// Next-step guidance — without this the user is left holding a
	// valid token and no idea what to do. Two paths from here:
	//   1. Go back to the browser; the wizard's poll loop sees the
	//      pair consumed and unlocks Launch.
	//   2. Skip the browser entirely; run `crewship setup` here.
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  • Back in the browser the onboarding wizard will unlock automatically.")
	fmt.Println("  • Or finish from the terminal:")
	fmt.Printf("      %screwship setup%s\n", cli.Bold, cli.Reset)
	fmt.Println("    (interactive prompts for crew template, adapter, API key, language)")
	return nil
}

func loginWithToken(serverURL, token string) error {
	client := cli.NewClient(serverURL, token, "")
	resp, err := client.Get("/api/v1/auth/cli-token/validate")
	if err != nil {
		return fmt.Errorf("failed to connect to server: %w", err)
	}

	// Historical fallback to /workspaces lived here for servers that
	// predate the cli-token/validate endpoint; per audit, validate now
	// ships on every supported server, so the fallback is dead code
	// that only obscured real auth failures (a 404 on validate would
	// silently switch to a check that succeeded on any logged-in user).

	if err := cli.CheckError(resp); err != nil {
		return fmt.Errorf("token validation failed: %w", err)
	}
	resp.Body.Close()

	// Same rationale as the pairing path above: LoadConfig returns an
	// empty config + nil err when the file is missing. Non-nil err
	// means the existing file is unreadable / malformed YAML — don't
	// silently overwrite the user's other config.
	cfg, err := cli.LoadConfig()
	if err != nil {
		return fmt.Errorf("load CLI config (refusing to overwrite a malformed file — fix or remove ~/.crewship/cli-config.yaml and retry): %w", err)
	}
	cfg.Server = serverURL
	cfg.Token = token
	if err := cli.SaveConfig(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	cli.PrintSuccess("Login successful! Token saved to ~/.crewship/cli-config.yaml")
	return nil
}

// loginWithGoogle drives the browser-based Google OAuth flow. The
// server-side flow lands cookies on the browser session (see
// internal/api/auth_google.go) — there is no CLI-poll endpoint that
// hands back a token after the OAuth round-trip. So the CLI:
//
//  1. Checks /api/v1/auth/google/status to confirm OAuth is configured.
//  2. Opens the user's browser at /api/v1/auth/google/redirect.
//  3. After the user completes sign-in in the browser, asks them to
//     mint a CLI token from Settings → CLI tokens and paste it here.
//  4. Validates and stores the token via the same code path as
//     `crewship login --token`.
//
// Why not a fully headless flow: a loopback redirect would require a
// new server endpoint that hands a one-shot CLI token to the redirect
// URI. That endpoint doesn't exist today; building a polling shim
// against the existing /google/status (which only reports enabled=bool)
// would be theatre. This hybrid keeps Google sign-in usable from the
// terminal without inventing server endpoints that don't ship.
func loginWithGoogle(serverURL string) error {
	client := cli.NewClient(serverURL, "", "")
	statusResp, err := client.Get("/api/v1/auth/google/status")
	if err != nil {
		return fmt.Errorf("contact server: %w", err)
	}
	if err := cli.CheckError(statusResp); err != nil {
		return fmt.Errorf("google status: %w", err)
	}
	var status struct {
		Enabled bool `json:"enabled"`
	}
	if err := cli.ReadJSON(statusResp, &status); err != nil {
		return fmt.Errorf("parse google status: %w", err)
	}
	if !status.Enabled {
		return fmt.Errorf("Google sign-in is not configured on %s (server returned enabled=false)", serverURL) //nolint:staticcheck // ST1005: user-facing CLI message ("Google" is a proper noun)
	}

	authURL := strings.TrimRight(serverURL, "/") + "/api/v1/auth/google/redirect"
	fmt.Printf("Opening browser to complete Google sign-in:\n  %s\n\n", authURL)
	if err := browserOpen(authURL); err != nil {
		// Non-fatal: just print the URL and let the user click manually.
		fmt.Printf("%s(Could not auto-open browser: %v — copy the URL above instead.)%s\n",
			cli.Dim, err, cli.Reset)
	}
	fmt.Printf("After sign-in completes, mint a CLI token at:\n  %s/settings#cli-tokens\n\n",
		strings.TrimRight(serverURL, "/"))

	// Read the pasted token. Use bufio so we keep working when stdin is
	// piped — same UX as `login` interactive prompts.
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Paste CLI token (or Ctrl-C to abort): ")
	tok, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read token: %w", err)
	}
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return fmt.Errorf("no token entered")
	}
	return loginWithToken(serverURL, tok)
}

func loginInteractive(serverURL string) error {
	reader := bufio.NewReader(os.Stdin)

	fmt.Printf("Crewship server: %s\n\n", serverURL)
	fmt.Print("Email: ")
	email, _ := reader.ReadString('\n')
	email = strings.TrimSpace(email)

	fmt.Print("Password: ")
	passwordBytes, err := term.ReadPassword(int(syscall.Stdin))
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}
	password := string(passwordBytes)
	fmt.Println()

	if email == "" || password == "" {
		return fmt.Errorf("email and password are required")
	}

	client := cli.NewClient(serverURL, "", "")

	// Step 1: Get CSRF token
	csrfResp, err := client.Get("/api/auth/csrf")
	if err != nil {
		return fmt.Errorf("CSRF request failed: %w", err)
	}

	var csrfBody struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := cli.ReadJSON(csrfResp, &csrfBody); err != nil {
		return fmt.Errorf("parse CSRF: %w", err)
	}

	// Extract CSRF cookie
	var csrfCookie string
	for _, c := range csrfResp.Cookies() {
		if strings.Contains(c.Name, "csrf-token") {
			csrfCookie = c.Value
			break
		}
	}

	// Step 2: Login with credentials
	loginBody := map[string]interface{}{
		"email":     email,
		"password":  password,
		"csrfToken": csrfBody.CSRFToken,
		"redirect":  "false",
		"json":      "true",
	}

	req, err := json.Marshal(loginBody)
	if err != nil {
		return fmt.Errorf("marshal login body: %w", err)
	}
	httpReq, err := http.NewRequest("POST", serverURL+"/api/auth/callback/credentials", strings.NewReader(string(req)))
	if err != nil {
		return fmt.Errorf("create login request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if csrfCookie != "" {
		// This is a CLIENT-side AddCookie on an outgoing http.Request, not a
		// server-side Set-Cookie response. Secure/HttpOnly attributes are
		// meaningful only on response cookies (browser enforces at storage
		// time), so the missing flags are correct here.
		httpReq.AddCookie(&http.Cookie{Name: "authjs.csrf-token", Value: csrfCookie}) // nosemgrep: cookie-missing-secure,cookie-missing-httponly
	}

	httpResp, err := client.HTTPClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("login request failed: %w", err)
	}
	defer httpResp.Body.Close()

	var loginResult struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := cli.ReadJSON(httpResp, &loginResult); err != nil {
		return fmt.Errorf("parse login response: %w", err)
	}

	if !loginResult.OK {
		return fmt.Errorf("login failed: invalid credentials")
	}

	// Extract session token from cookies
	var sessionToken string
	for _, c := range httpResp.Cookies() {
		if strings.Contains(c.Name, "session-token") {
			sessionToken = c.Value
			break
		}
	}

	if sessionToken == "" {
		return fmt.Errorf("login succeeded but no session token received")
	}

	// Step 3: Try to generate a CLI token via the dedicated endpoint
	tokenClient := cli.NewClient(serverURL, sessionToken, "")
	cliTokenResp, err := tokenClient.Post("/api/v1/auth/cli-token", map[string]string{
		"name": "CLI login",
	})

	var finalToken string
	if err == nil {
		if cliTokenResp.StatusCode == http.StatusOK {
			var tokenResult struct {
				Token string `json:"token"`
			}
			if err := cli.ReadJSON(cliTokenResp, &tokenResult); err == nil && tokenResult.Token != "" {
				finalToken = tokenResult.Token
			}
		} else {
			cliTokenResp.Body.Close()
		}
	}

	// Fall back to session token if CLI token endpoint not available
	if finalToken == "" {
		finalToken = sessionToken
	}

	cfg, err := cli.LoadConfig()
	if err != nil {
		return fmt.Errorf("load CLI config (refusing to overwrite a malformed file — fix or remove ~/.crewship/cli-config.yaml and retry): %w", err)
	}
	cfg.Server = serverURL
	cfg.Token = finalToken
	if err := cli.SaveConfig(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	cli.PrintSuccess("Login successful! Token saved to ~/.crewship/cli-config.yaml")
	return nil
}
