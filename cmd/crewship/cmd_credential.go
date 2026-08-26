package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/credprovider"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var credentialCmd = &cobra.Command{
	Use:     "credential",
	Aliases: []string{"cred"},
	Short:   "Manage credentials",
}

// credRow is a single credential row as rendered by `credential list`.
type credRow struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Provider   string `json:"provider"`
	Status     string `json:"status"`
	AgentCount int    `json:"_count_agent_credentials"`
	// SecurityLevel is the Keeper tier. Listed because it is the property that
	// decides what happens when an agent asks for the credential — at L4 every
	// read becomes a human approval — and it was invisible in every listing, so an
	// operator had no way to notice that a production credential was filed as L1
	// (which, until the create path was fixed, is exactly what happened to
	// anything marked 4).
	SecurityLevel         int     `json:"security_level"`
	SecurityLevelLabel    *string `json:"security_level_label"`
	CreatedByActorType    *string `json:"created_by_actor_type"`
	ProvisionedForService *string `json:"provisioned_for_service"`
}

// decodeCredentialListPage tolerates BOTH the legacy bare-array response and
// the paginated {credentials, next_cursor} envelope (#1033), so the command
// works against any server version.
func decodeCredentialListPage(raw []byte) ([]credRow, *string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var rows []credRow
		if err := json.Unmarshal(raw, &rows); err != nil {
			return nil, nil, err
		}
		return rows, nil, nil
	}
	var env struct {
		Credentials []credRow `json:"credentials"`
		NextCursor  *string   `json:"next_cursor"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, nil, err
	}
	return env.Credentials, env.NextCursor, nil
}

var credListCmd = &cobra.Command{
	Use:   "list",
	Short: "List credentials in the workspace",
	Long: `List credentials in the workspace.

By default one page is returned; pass --all to follow the cursor and fetch
every page. --search and --tag filter server-side.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		flags := cmd.Flags()
		search, _ := flags.GetString("search")
		tag, _ := flags.GetString("tag")
		limit, _ := flags.GetInt("limit")
		limitSet := flags.Changed("limit")
		cursor, _ := flags.GetString("cursor")
		all, _ := flags.GetBool("all")

		if limitSet && limit <= 0 {
			return fmt.Errorf("--limit must be a positive integer, got %d", limit)
		}

		client := newAPIClient()
		buildURL := func(cur string) string {
			q := url.Values{}
			// paginate=true opts into the cursor envelope; the server still
			// returns a bare array to callers that don't ask, so this is safe.
			q.Set("paginate", "true")
			// Always send an explicit limit. The paginated envelope defaults
			// to 50 server-side, vs. the long-standing bare-array endpoint's
			// 100 — without this, opting into pagination would silently
			// halve the page size for every caller that didn't pass --limit.
			if limitSet {
				q.Set("limit", strconv.Itoa(limit))
			} else {
				q.Set("limit", "100")
			}
			if search != "" {
				q.Set("search", search)
			}
			if tag != "" {
				q.Set("tag", tag)
			}
			if cur != "" {
				q.Set("cursor", cur)
			}
			return "/api/v1/credentials?" + q.Encode()
		}

		// maxAllPages bounds --all's cursor-follow loop. Combined with the
		// non-advancing-cursor check below, this guarantees the loop always
		// terminates even against a buggy or malicious server.
		const maxAllPages = 200

		var creds []credRow
		var lastNext *string
		cur := cursor
		for pageNum := 0; ; pageNum++ {
			if pageNum >= maxAllPages {
				return fmt.Errorf("--all stopped after %d pages without reaching the end — this looks like a server bug; re-run without --all or with an explicit --cursor", maxAllPages)
			}
			resp, err := client.Get(buildURL(cur))
			if err != nil {
				return err
			}
			if err := cli.CheckError(resp); err != nil {
				return err
			}
			var raw json.RawMessage
			if err := cli.ReadJSON(resp, &raw); err != nil {
				return err
			}
			rows, next, err := decodeCredentialListPage(raw)
			if err != nil {
				return err
			}
			creds = append(creds, rows...)
			lastNext = next
			// Stop after one page unless --all; also stop when there is no
			// next page or the server returned a bare array (next == nil).
			if !all || next == nil || *next == "" {
				break
			}
			if *next == cur {
				return fmt.Errorf("--all aborted: the server returned the same cursor twice (%q) — no progress", *next)
			}
			cur = *next
		}

		// SOURCE column surfaces who/what owns the row: a literal
		// "user" for the default (operator created via UI / CLI),
		// "system" for v98 AUTO_MANAGED rows minted by the manifest
		// dispatch, "agent" for future per-agent-attributed rows.
		// When a row is tagged with provisioned_for_service we suffix
		// the service slug so operators see *what* the auto-managed
		// row belongs to without a second `crewship credential get`.
		f := newFormatter()
		headers := []string{"ID", "NAME", "TYPE", "TIER", "STATUS", "AGENTS", "SOURCE"}
		var rows [][]string
		for _, c := range creds {
			actor := "user"
			if c.CreatedByActorType != nil && *c.CreatedByActorType != "" {
				actor = *c.CreatedByActorType
			}
			source := actor
			if c.ProvisionedForService != nil && *c.ProvisionedForService != "" {
				source = actor + " (" + *c.ProvisionedForService + ")"
			}
			// TIER replaces PROVIDER in the table: the provider of a credential is
			// on `credential get` and rarely the question, while the tier changes
			// whether an agent can read it at all. Falls back to the bare level for
			// a server that does not send the label.
			tier := fmt.Sprintf("L%d", c.SecurityLevel)
			if c.SecurityLevelLabel != nil && *c.SecurityLevelLabel != "" {
				tier = *c.SecurityLevelLabel
			}
			if c.SecurityLevel == 0 {
				tier = "—"
			}
			rows = append(rows, []string{c.ID, c.Name, c.Type, tier, c.Status, fmt.Sprintf("%d", c.AgentCount), source})
		}
		if err := f.Auto(creds, headers, rows); err != nil {
			return err
		}
		// Hint when more pages exist and the caller didn't ask for --all.
		// To stderr so stdout stays a clean, parseable table.
		if !all && lastNext != nil && *lastNext != "" {
			fmt.Fprintf(os.Stderr, "More results available — re-run with --all, or --cursor %s for the next page.\n", *lastNext)
		}
		return nil
	},
}

var credGetCmd = &cobra.Command{
	Use:   "get <name-or-id>",
	Short: "Show credential details (value is never displayed)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		// #1177: getByRef issues ONE request when args[0] is a real CUID (the
		// existence check IS this fetch) instead of verifying then re-GETting
		// the same URL.
		resp, _, err := getByRef(client, "/api/v1/credentials/", args[0], resolveCredentialID)
		if err != nil {
			return err
		}

		var cred struct {
			ID        string  `json:"id"`
			Name      string  `json:"name"`
			Type      string  `json:"type"`
			Provider  string  `json:"provider"`
			Status    string  `json:"status"`
			Scope     string  `json:"scope"`
			CreatedAt string  `json:"created_at"`
			CrewID    *string `json:"crew_id"`
		}
		if err := cli.ReadJSON(resp, &cred); err != nil {
			return err
		}

		f := newFormatter()
		pairs := [][]string{
			{"ID", cred.ID},
			{"Name", cred.Name},
			{"Type", cred.Type},
			{"Provider", cred.Provider},
			{"Status", cred.Status},
			{"Scope", cred.Scope},
			{"Created", cred.CreatedAt},
		}
		return f.AutoDetail(cred, pairs)
	},
}

func resolveCredentialID(client *cli.Client, nameOrID string) (string, error) {
	if looksLikeCUID(nameOrID) {
		ok, err := cuidExists(client, "/api/v1/credentials/"+nameOrID)
		if err != nil {
			return "", fmt.Errorf("resolve credential: %w", err)
		}
		if ok {
			return nameOrID, nil
		}
		// Miss: nameOrID only looks like a CUID — fall through to the
		// name scan below instead of forwarding a doomed id (#1075).
	}

	resp, err := client.Get("/api/v1/credentials")
	if err != nil {
		return "", fmt.Errorf("resolve credential: %w", err)
	}
	if err := cli.CheckError(resp); err != nil {
		return "", err
	}

	var creds []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := cli.ReadJSON(resp, &creds); err != nil {
		return "", err
	}

	for _, c := range creds {
		if c.Name == nameOrID {
			return c.ID, nil
		}
	}
	return "", cli.NotFoundf("credential %q not found", nameOrID)
}

// testCredentialValue validates a credential value against the provider API.
// Returns (valid, errorMessage). Skips the test only where there is genuinely
// nothing to ask: an opaque SECRET, or no provider to ask.
//
// It used to short-circuit sk-ant-oat tokens here too, on the claim that OAuth
// tokens "cannot be validated via API". That claim was wrong on the server
// (probeAnthropicCredential authenticates exactly this shape against
// /v1/messages) and the server side has been fixed — but this branch made the
// fix invisible from the CLI, which is where `crewship credential create`
// reports to the operator. Creating a credential with a fabricated
// sk-ant-oat value printed "Key validated successfully" having contacted
// nothing.
//
// A check that cannot run must not report success. Here it can run, so it does.
func testCredentialValue(client *cli.Client, provider, credType, value string) (bool, string) {
	if credType == "SECRET" || provider == "" || provider == "NONE" {
		return true, ""
	}

	body := map[string]interface{}{
		"provider": provider,
		"type":     credType,
		"value":    value,
	}
	resp, err := client.Post("/api/v1/credentials/test", body)
	if err != nil {
		return false, "test request failed: " + err.Error()
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return false, "test request failed: " + err.Error()
	}

	var result struct {
		Valid bool   `json:"valid"`
		Error string `json:"error"`
	}
	if err := cli.ReadJSON(resp, &result); err != nil {
		return false, "failed to read test result"
	}
	return result.Valid, result.Error
}

// confirmInvalidKey prompts the user to confirm saving an invalid credential.
// Uses huh for interactive TTY sessions; falls back to plain stdin read when
// either stdin or stdout is not a TTY. We gate on BOTH: a redirected stdout
// (`crewship credential create ... > out.txt`) would otherwise cause huh to
// write ANSI escape sequences into the target file.
func confirmInvalidKey(errMsg string) bool {
	cli.PrintWarning(fmt.Sprintf("Key validation failed: %s", errMsg))

	// Non-TTY fallback (kept for safety even though caller already checks)
	stdinTTY := term.IsTerminal(int(os.Stdin.Fd()))
	stdoutTTY := term.IsTerminal(int(os.Stdout.Fd()))
	if !stdinTTY || !stdoutTTY {
		fmt.Print("Save anyway? [y/N] ")
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
			return answer == "y" || answer == "yes"
		}
		return false
	}

	var confirmed bool
	err := huh.NewConfirm().
		Title("Save anyway?").
		Description("The credential value failed provider validation — it may not work in production.").
		Affirmative("Save anyway").
		Negative("Cancel").
		Value(&confirmed).
		Run()
	if err != nil {
		return false
	}
	return confirmed
}

// credRotationsCmd lists the rotation history for a single credential. The
// "audit" tab in the detail Sheet shows the same data; this exposes it to
// scripts that want to verify a rotation actually fired (e.g. after a
// scheduled key-rotation cron).
var credRotationsCmd = &cobra.Command{
	Use:   "rotations <name-or-id>",
	Short: "List rotation history for a credential",
	Args:  cobra.ExactArgs(1),
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
		resp, err := client.Get("/api/v1/credentials/" + credID + "/rotations")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var rotations []struct {
			ID           string  `json:"id"`
			CredentialID string  `json:"credential_id"`
			GraceSeconds int     `json:"grace_seconds"`
			RotatedAt    string  `json:"rotated_at"`
			ExpiresAt    string  `json:"expires_at"`
			RotatedBy    string  `json:"rotated_by"`
			Status       string  `json:"status"`
			OldValueGone bool    `json:"old_value_gone"`
			CancelledAt  *string `json:"cancelled_at,omitempty"`
		}
		if err := cli.ReadJSON(resp, &rotations); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "STATUS", "ROTATED_AT", "EXPIRES_AT", "GRACE_S", "OLD_GONE", "ROTATED_BY"}
		var rows [][]string
		for _, r := range rotations {
			rotatedAt := r.RotatedAt
			if t, err := time.Parse(time.RFC3339, r.RotatedAt); err == nil {
				rotatedAt = t.Format("2006-01-02 15:04:05")
			}
			expiresAt := r.ExpiresAt
			if t, err := time.Parse(time.RFC3339, r.ExpiresAt); err == nil {
				expiresAt = t.Format("2006-01-02 15:04:05")
			}
			rows = append(rows, []string{
				r.ID, r.Status, rotatedAt, expiresAt,
				fmt.Sprintf("%d", r.GraceSeconds),
				yesNo(r.OldValueGone), r.RotatedBy,
			})
		}
		return f.Auto(rotations, headers, rows)
	},
}

// credAuditCmd renders the full credential timeline. Same view the
// detail Sheet's Audit tab uses, exposed for scripts that want to grep
// for ROTATE / TEST / REVOKE events without scraping the UI.
var credAuditCmd = &cobra.Command{
	Use:   "audit <name-or-id>",
	Short: "Show audit timeline for a credential (USE, ROTATE, TEST, REVOKE)",
	Args:  cobra.ExactArgs(1),
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
		limit, _ := cmd.Flags().GetInt("limit")
		if limit < 1 || limit > 500 {
			return fmt.Errorf("--limit must be between 1 and 500")
		}
		path := fmt.Sprintf("/api/v1/credentials/%s/audit?limit=%d", url.PathEscape(credID), limit)
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var events []struct {
			ID         string         `json:"id"`
			EventType  string         `json:"event_type"`
			AgentID    *string        `json:"agent_id"`
			IPAddress  *string        `json:"ip_address"`
			Metadata   map[string]any `json:"metadata"`
			OccurredAt string         `json:"occurred_at"`
			// Who did it, resolved by the server. The AGENT column only ever
			// held the agent_id column, so a rotation or a reveal — the events
			// an incident responder cares about most — showed "-" for the
			// person who did it.
			ActorKind string `json:"actor_kind"`
			ActorID   string `json:"actor_id"`
			ActorName string `json:"actor_name"`
		}
		if err := cli.ReadJSON(resp, &events); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"TIME", "EVENT", "ACTOR", "WHO", "IP"}
		var rows [][]string
		for _, e := range events {
			ts := e.OccurredAt
			if t, err := time.Parse(time.RFC3339Nano, e.OccurredAt); err == nil {
				ts = t.Format("2006-01-02 15:04:05")
			} else if t, err := time.Parse(time.RFC3339, e.OccurredAt); err == nil {
				ts = t.Format("2006-01-02 15:04:05")
			}
			kind := e.ActorKind
			if kind == "" && e.AgentID != nil && *e.AgentID != "" {
				// An older server sends no actor block; the agent_id column is
				// the one attribution that predates it.
				kind = "agent"
			}
			if kind == "" {
				kind = "system"
			}
			// The name where we have one, the id where we do not. A deleted
			// agent still did the thing, and its id is the only handle left.
			who := e.ActorName
			if who == "" {
				who = e.ActorID
			}
			if who == "" && e.AgentID != nil {
				who = *e.AgentID
			}
			if who == "" {
				who = "-"
			}
			ip := "-"
			if e.IPAddress != nil && *e.IPAddress != "" {
				ip = *e.IPAddress
			}
			rows = append(rows, []string{ts, e.EventType, kind, who, ip})
		}
		return f.Auto(events, headers, rows)
	},
}

// credTestStoredCmd validates an already-saved credential by ID/name
// against the provider API. This is distinct from `credential test`,
// which validates a value the caller types on the command line *before*
// it is saved — the existing pre-save flow.
var credTestStoredCmd = &cobra.Command{
	Use:   "test-stored <name-or-id>",
	Short: "Test a saved credential by ID/name against the provider API",
	Args:  cobra.ExactArgs(1),
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
		resp, err := client.Post("/api/v1/credentials/"+credID+"/test", nil)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result struct {
			Valid     bool   `json:"valid"`
			Status    int    `json:"status"`
			Error     string `json:"error"`
			Supported bool   `json:"supported"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}
		// A provider with no upstream probe comes back valid:true — nothing
		// failed, because nothing was attempted. Reporting that as "is valid"
		// answers the one question this command exists to answer with a result
		// it never obtained. Say what actually happened instead; it is not an
		// error, so exit 0 and let scripts carry on.
		//
		// Gate on valid too, not on supported alone: a real failure must stay a
		// failure even when `supported` is absent — an older server predating
		// the field would otherwise have every expired key reported as merely
		// unchecked. Absent field against a passing probe degrades the other
		// way, to "not checked", which is the safe direction: never a false
		// green.
		if result.Valid && !result.Supported {
			cli.PrintWarning(fmt.Sprintf(
				"Credential %s was not checked — Crewship has no upstream probe for this provider. "+
					"It is stored and will be delivered to agents as configured.", args[0]))
			return nil
		}
		if result.Valid {
			cli.PrintSuccess(fmt.Sprintf("Credential %s is valid.", args[0]))
			return nil
		}
		msg := result.Error
		if msg == "" {
			msg = "validation failed"
		}
		return fmt.Errorf("credential invalid: %s", msg)
	},
}

// credDefaultEnvVarCmd looks up the conventional env var name for a
// CLI tool provider (GH_TOKEN, GITLAB_TOKEN, VERCEL_TOKEN, …). Useful
// when scripting `credential assign` and you don't want to memorise
// every provider's convention.
var credDefaultEnvVarCmd = &cobra.Command{
	Use:   "default-env-var",
	Short: "Print the conventional env var name for a provider (GH_TOKEN, GITLAB_TOKEN, ...)",
	Example: `  crewship credential default-env-var --provider GITHUB
  crewship credential default-env-var --provider GITLAB`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		// #1083: the route is now workspace-scoped (wsCtx) for uniformity
		// with the rest of the credentials surface, so a workspace must be
		// selected even though the response carries no tenant data.
		if err := requireWorkspace(); err != nil {
			return err
		}
		provider, _ := cmd.Flags().GetString("provider")
		if provider == "" {
			return fmt.Errorf("--provider is required")
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/credentials/default-env-var?provider=" + url.QueryEscape(provider))
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var result struct {
			EnvVar string `json:"env_var"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}
		if result.EnvVar == "" {
			return fmt.Errorf("no default env var for provider %q", provider)
		}
		fmt.Println(result.EnvVar)
		return nil
	},
}

func init() {
	credListCmd.Flags().Int("limit", 0, "Page size (1-500; enables cursor pagination)")
	credListCmd.Flags().String("cursor", "", "Opaque cursor from a previous page's output")
	credListCmd.Flags().String("search", "", "Filter by a substring of the name or description (server-side)")
	credListCmd.Flags().String("tag", "", "Filter to credentials carrying this exact tag (server-side)")
	credListCmd.Flags().Bool("all", false, "Fetch every page by following the cursor")

	credCreateCmd.Flags().String("name", "", "Credential name (required)")
	credCreateCmd.Flags().String("type", "", "Type: SECRET|API_KEY|AI_CLI_TOKEN|CLI_TOKEN|ENDPOINT_URL (required)")
	credCreateCmd.Flags().String("provider", "", "Provider: "+credprovider.ProvidersHelp())
	credCreateCmd.Flags().String("value", "", "Credential value — the URL for ENDPOINT_URL (visible in process list, prefer --value-stdin)")
	credCreateCmd.Flags().Bool("value-stdin", false, "Read value from stdin (secure)")
	credCreateCmd.Flags().String("base-url", "", "Endpoint this provider is reached at — required for a provider whose upstream comes from the credential (OPENAI_COMPAT), rejected for every other. Stored with the key as one object and delivered to the sidecar, never to the agent's environment")
	credCreateCmd.Flags().String("auth-token", "", "Bearer token sent to the endpoint (Authorization: Bearer …); stored encrypted, never displayed. For --type ENDPOINT_URL, or with --base-url. Prefer --auth-token-stdin: an argument is visible to anything that can read the process table")
	credCreateCmd.Flags().Bool("auth-token-stdin", false, "Read the endpoint bearer token from stdin instead of --auth-token, so it never appears in argv")
	credCreateCmd.Flags().StringArray("header", nil, "Extra request header KEY=VALUE (repeatable; use for Basic/custom-header endpoints). For --type ENDPOINT_URL, or with --base-url")
	credCreateCmd.Flags().String("env-var-name", "", "Environment variable name")
	credCreateCmd.Flags().String("account-label", "", "Which account/instance this credential is for (e.g. \"work\", or a forge host like \"ghe.acme.internal\" — git links match a credential by host)")
	credCreateCmd.Flags().Int("security-level", 0, "Keeper credential tier — "+securityLevelHelp()+" (0 = leave at the server default). L4 requires a human to approve every read.")
	credCreateCmd.Flags().StringSlice("crews", nil, "Crew slugs or IDs to scope this credential to (repeatable/comma-separated); sets scope=CREW. Omit for a workspace-wide credential")
	credCreateCmd.Flags().String("scope", "", "Visibility scope: WORKSPACE (default) or CREW. Usually inferred from --crews; set explicitly to override")
	// OAuth app fields (--type OAUTH2). The row is created empty and PENDING;
	// `crewship oauth connect` is what puts tokens in it. See cmd_oauth.go.
	credCreateCmd.Flags().String("oauth-provider", "", "Fill the OAuth endpoints from the built-in catalogue (see `crewship oauth providers`); --type OAUTH2 only")
	credCreateCmd.Flags().String("oauth-client-id", "", "OAuth app client ID, required for --type OAUTH2")
	credCreateCmd.Flags().String("oauth-client-secret", "", "OAuth app client secret; stored encrypted. Omit for a public (PKCE-only) client")
	credCreateCmd.Flags().String("oauth-auth-url", "", "Authorization endpoint; overrides --oauth-provider, and required without it")
	credCreateCmd.Flags().String("oauth-token-url", "", "Token endpoint; overrides --oauth-provider, and required without it")
	credCreateCmd.Flags().String("oauth-scopes", "", "Space-separated scopes to request; defaults to the catalogue's for --oauth-provider")

	credUpdateCmd.Flags().String("name", "", "Credential name")
	credUpdateCmd.Flags().String("value", "", "New value")
	credUpdateCmd.Flags().Bool("value-stdin", false, "Read value from stdin")
	credUpdateCmd.Flags().String("account-label", "", "Which account/instance this credential is for; pass an empty value to clear it")
	credUpdateCmd.Flags().Int("security-level", 0, "Keeper credential tier — "+securityLevelHelp()+". L4 requires a human to approve every read.")
	credUpdateCmd.Flags().StringSlice("crews", nil, "Replace the crew scoping with these crew slugs or IDs (repeatable/comma-separated); pass an empty value to clear crews and make it workspace-wide")
	credUpdateCmd.Flags().String("scope", "", "Visibility scope: WORKSPACE or CREW. Usually inferred from --crews; set explicitly to override")

	credAssignCmd.Flags().String("env-var-name", "", "Environment variable name override")
	credAssignCmd.Flags().Int("priority", 0, "Priority (1-10)")
	credAssignCmd.Flags().String("ttl", "", "Issue a short-lived lease instead of a standing grant, e.g. 30m, 2h, 24h (max 30d). The grant is refused at injection time once it expires.")

	credDeleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	credTestCmd.Flags().String("provider", "", "Provider: "+credprovider.ProvidersHelp()+" (required)")
	credTestCmd.Flags().String("type", "", "Type: API_KEY|AI_CLI_TOKEN|SECRET|CLI_TOKEN")
	credTestCmd.Flags().String("value", "", "Credential value to test")
	credTestCmd.Flags().Bool("value-stdin", false, "Read value from stdin")

	credAuditCmd.Flags().Int("limit", 50, "Max audit events to return (1-500)")
	credDefaultEnvVarCmd.Flags().String("provider", "", "Provider: GITHUB|GITLAB|VERCEL|AWS|KUBERNETES (required)")

	credRotateCmd.Flags().String("value", "", "New credential value — the URL for ENDPOINT_URL (visible in process list, prefer --value-stdin)")
	credRotateCmd.Flags().Bool("value-stdin", false, "Read new value from stdin (secure)")
	credRotateCmd.Flags().String("auth-token", "", "Endpoint-storing credentials only (type ENDPOINT_URL, or a provider whose endpoint comes from the credential): new bearer token, merged over the stored value so the endpoint and headers survive. Prefer --auth-token-stdin: an argument is visible to anything that can read the process table")
	credRotateCmd.Flags().Bool("auth-token-stdin", false, "Read the new bearer token from stdin instead of --auth-token, so it never appears in argv")
	credRotateCmd.Flags().StringArray("header", nil, "Endpoint-storing credentials only: replace the extra request headers, KEY=VALUE (repeatable)")
	credRotateCmd.Flags().Int("grace-seconds", 0, "Grace overlap in seconds (default 24h server-side, max 7d)")
	credRotateCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	credRotationCancelCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	credentialCmd.AddCommand(credListCmd)
	credentialCmd.AddCommand(credCreateCmd)
	credentialCmd.AddCommand(credGetCmd)
	credentialCmd.AddCommand(credUpdateCmd)
	credentialCmd.AddCommand(credDeleteCmd)
	credentialCmd.AddCommand(credAssignCmd)
	credentialCmd.AddCommand(credUnassignCmd)
	credentialCmd.AddCommand(credTestCmd)
	credentialCmd.AddCommand(credRotateCmd)
	credentialCmd.AddCommand(credRotationsCmd)
	credentialCmd.AddCommand(credRotationCancelCmd)
	credentialCmd.AddCommand(credAuditCmd)
	credentialCmd.AddCommand(credTestStoredCmd)
	credentialCmd.AddCommand(credDefaultEnvVarCmd)
}
