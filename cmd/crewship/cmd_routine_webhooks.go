package main

// Routine webhook subcommands. Webhooks are token-addressed event
// triggers: external services POST to /api/v1/webhooks/{token} and
// the matching routine fires with the request body delivered as the
// `event` input. HMAC verification is supported when the webhook has
// a signing_secret configured.
//
// Stripe-style secret reveal: the signing secret is shown ONCE on
// create response. To rotate, delete + recreate.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

type webhookRow struct {
	ID                    string                 `json:"id"`
	WorkspaceID           string                 `json:"workspace_id"`
	Name                  string                 `json:"name"`
	TargetPipelineID      string                 `json:"target_pipeline_id"`
	TargetPipelineSlug    string                 `json:"target_pipeline_slug,omitempty"`
	TargetPipelineVersion *int                   `json:"target_pipeline_version,omitempty"`
	Token                 string                 `json:"token"`
	SigningSecretSet      bool                   `json:"signing_secret_set"`
	SigningSecret         string                 `json:"signing_secret,omitempty"`
	InputsTemplate        map[string]interface{} `json:"inputs_template"`
	Enabled               bool                   `json:"enabled"`
	RateLimitPerMin       int                    `json:"rate_limit_per_min"`
	LastFiredAt           *string                `json:"last_fired_at,omitempty"`
	LastStatus            *string                `json:"last_status,omitempty"`
	LastRunID             *string                `json:"last_run_id,omitempty"`
	FireCount             int64                  `json:"fire_count"`
	CreatedAt             string                 `json:"created_at"`
	UpdatedAt             string                 `json:"updated_at"`
}

var routineWebhooksCmd = &cobra.Command{
	Use:   "webhooks",
	Short: "Manage event-driven webhook triggers",
	Long: `Webhooks fire saved routines when external services POST to
/api/v1/webhooks/{token}. Each webhook is named, targets one routine,
optionally HMAC-signed for delivery integrity, and rate-limited per
token. The signing secret is revealed only once on create — to rotate,
delete + recreate.

Examples:
  crewship routine webhooks list
  crewship routine webhooks list --slug summarize-text
  crewship routine webhooks create --slug pr-review-structured \
      --name "github-pr-reviews" --hmac-secret "$(openssl rand -hex 32)" \
      --rate-limit 30
  crewship routine webhooks create --slug summarize-text  # no HMAC
  crewship routine webhooks delete <webhook_id>
  crewship routine webhooks url <webhook_id>     # print public URL
`,
}

var routineWebhooksListCmd = &cobra.Command{
	Use:   "list",
	Short: "List webhooks in this workspace",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		slugFilter, _ := cmd.Flags().GetString("slug")
		resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipeline-webhooks", ws))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var rows []webhookRow
		if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if slugFilter != "" {
			out := rows[:0]
			for _, r := range rows {
				if r.TargetPipelineSlug == slugFilter {
					out = append(out, r)
				}
			}
			rows = out
		}
		jsonOut, _ := cmd.Flags().GetBool("json")
		if jsonOut {
			// Redact tokens + secrets from --json output. The list
			// endpoint returns webhook tokens (the public URL
			// segment) and signing_secret_set flags; piping --json
			// to a log/share could leak the public URL or
			// inadvertently confirm secret-set state for sensitive
			// webhooks. The user can fetch the full record via the
			// `url` subcommand when they explicitly need it.
			redacted := make([]webhookRow, len(rows))
			for i, r := range rows {
				redacted[i] = r
				if redacted[i].Token != "" {
					redacted[i].Token = redactedShort(redacted[i].Token)
				}
				redacted[i].SigningSecret = ""
			}
			b, _ := json.MarshalIndent(redacted, "", "  ")
			fmt.Println(string(b))
			return nil
		}
		if len(rows) == 0 {
			fmt.Println("No webhooks in this workspace.")
			fmt.Println("Create one: crewship routine webhooks create --slug <routine>")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tROUTINE\tHMAC\tFIRES\tLAST STATUS\tRATE/MIN\tENABLED")
		for _, h := range rows {
			hmac := "no"
			if h.SigningSecretSet {
				hmac = "yes"
			}
			lastStatus := "—"
			if h.LastStatus != nil && *h.LastStatus != "" {
				lastStatus = *h.LastStatus
			}
			enabled := "no"
			if h.Enabled {
				enabled = "yes"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\t%d\t%s\n",
				shortID(h.ID), h.Name, h.TargetPipelineSlug, hmac, h.FireCount, lastStatus, h.RateLimitPerMin, enabled)
		}
		return w.Flush()
	},
}

var routineWebhooksCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new webhook (signing secret revealed once on success)",
	RunE: func(cmd *cobra.Command, _ []string) error {
		slug, _ := cmd.Flags().GetString("slug")
		name, _ := cmd.Flags().GetString("name")
		hmac, _ := cmd.Flags().GetString("hmac-secret")
		rateLimit, _ := cmd.Flags().GetInt("rate-limit")
		inputsTpl, _ := cmd.Flags().GetString("inputs-template")
		if slug == "" {
			return fmt.Errorf("--slug is required")
		}
		if name == "" {
			name = fmt.Sprintf("%s webhook", slug)
		}
		if rateLimit <= 0 {
			rateLimit = 60
		}
		body := map[string]interface{}{
			"name":                 name,
			"target_pipeline_slug": slug,
			"rate_limit_per_min":   rateLimit,
			"enabled":              true,
		}
		if hmac != "" {
			body["signing_secret"] = hmac
		}
		if inputsTpl != "" {
			var tpl map[string]interface{}
			if err := json.Unmarshal([]byte(inputsTpl), &tpl); err != nil {
				return fmt.Errorf("--inputs-template must be valid JSON: %w", err)
			}
			body["inputs_template"] = tpl
		}
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Post(fmt.Sprintf("/api/v1/workspaces/%s/pipeline-webhooks", ws), body)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var w webhookRow
		if err := json.NewDecoder(resp.Body).Decode(&w); err != nil {
			// Server returned 2xx but we can't parse the body —
			// don't claim "Webhook created" because the user has
			// no token to copy. Fail loudly so the operator knows
			// to check the workspace via /webhooks list.
			return fmt.Errorf("webhook may have been created but response decode failed: %w (run 'crewship routine webhooks list --slug %s' to verify)", err, slug)
		}
		if w.Token == "" {
			// Decode succeeded structurally but the token field is
			// empty — same risk profile, same surface to user.
			return fmt.Errorf("webhook server response missing token; run 'crewship routine webhooks list --slug %s' to verify creation", slug)
		}
		baseURL, _ := cmd.Flags().GetString("base-url")
		if baseURL == "" {
			baseURL = clientBaseURL(client)
		}
		publicURL := strings.TrimRight(baseURL, "/") + "/api/v1/webhooks/" + url.PathEscape(w.Token)
		fmt.Println("Webhook created.")
		fmt.Printf("  ID:        %s\n", w.ID)
		fmt.Printf("  Name:      %s\n", w.Name)
		fmt.Printf("  Routine:   %s\n", w.TargetPipelineSlug)
		fmt.Printf("  Public URL: %s\n", publicURL)
		fmt.Printf("  Rate limit: %d / minute\n", w.RateLimitPerMin)
		if w.SigningSecret != "" {
			fmt.Println()
			fmt.Println("== HMAC signing secret (shown once, copy now) ==")
			fmt.Println(w.SigningSecret)
			fmt.Println("== Senders MUST include header: X-Crewship-Signature: sha256=<hex_hmac_of_body>")
		}
		return nil
	},
}

var routineWebhooksUrlCmd = &cobra.Command{
	Use:   "url <webhook_id>",
	Short: "Print the public URL for a webhook (no secret reveal)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		// We don't have a GET-by-id endpoint exposed — read the list
		// and filter. For workspaces with hundreds of webhooks this is
		// chunky but list is cheap in practice (most workspaces have
		// <10 webhooks per routine).
		resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipeline-webhooks", ws))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var rows []webhookRow
		if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		for _, w := range rows {
			if w.ID == args[0] {
				baseURL, _ := cmd.Flags().GetString("base-url")
				if baseURL == "" {
					baseURL = clientBaseURL(client)
				}
				fmt.Println(strings.TrimRight(baseURL, "/") + "/api/v1/webhooks/" + url.PathEscape(w.Token))
				return nil
			}
		}
		return fmt.Errorf("webhook %s not found", args[0])
	},
}

var routineWebhooksDeleteCmd = &cobra.Command{
	Use:   "delete <webhook_id>",
	Short: "Delete a webhook (existing senders will start failing)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		yes, _ := cmd.Flags().GetBool("yes")
		if !yes {
			fmt.Printf("Delete webhook %s? Existing senders using this token will start getting 404s.\n", args[0])
			fmt.Print("Type 'yes' to confirm: ")
			var input string
			_, _ = fmt.Scanln(&input)
			if strings.ToLower(strings.TrimSpace(input)) != "yes" {
				return fmt.Errorf("aborted")
			}
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Delete(fmt.Sprintf("/api/v1/workspaces/%s/pipeline-webhooks/%s", ws, args[0]))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		fmt.Printf("Webhook %s deleted.\n", args[0])
		return nil
	},
}

// clientBaseURL pulls the API base URL from the cli.Client config so
// the URL we print matches whatever server the user is talking to.
// Falls back to a sensible local-dev default if config introspection
// fails — better to show http://localhost:8080 than nothing.
func clientBaseURL(c *cli.Client) string {
	type baseURLer interface {
		BaseURL() string
	}
	if b, ok := any(c).(baseURLer); ok {
		if u := b.BaseURL(); u != "" {
			return u
		}
	}
	if env := os.Getenv("CREWSHIP_SERVER"); env != "" {
		return env
	}
	return "http://localhost:8080"
}

// _ = http.NoBody // (kept for future force-fire endpoint)
var _ = http.NoBody

// redactedShort returns the last 4 chars of s prefixed with "***" for
// log-safe display of secret-bearing identifiers (webhook tokens,
// API keys, etc.). Short enough that operators recognize the value
// they previously copied; opaque enough that --json output piped
// into a shared log file doesn't expose it.
func redactedShort(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return "***" + s[len(s)-4:]
}

func init() {
	routineWebhooksListCmd.Flags().String("slug", "", "filter to webhooks targeting this routine slug")
	routineWebhooksListCmd.Flags().Bool("json", false, "output as JSON for scripting")

	routineWebhooksCreateCmd.Flags().String("slug", "", "target routine slug (REQUIRED)")
	routineWebhooksCreateCmd.Flags().String("name", "", "human-readable webhook name (default: '<slug> webhook')")
	routineWebhooksCreateCmd.Flags().String("hmac-secret", "", "HMAC signing secret — empty means no signature verification")
	routineWebhooksCreateCmd.Flags().Int("rate-limit", 60, "max fires per minute per webhook (default 60)")
	routineWebhooksCreateCmd.Flags().String("inputs-template", "", "JSON template merged with the request body to form routine inputs")
	routineWebhooksCreateCmd.Flags().String("base-url", "", "override the public base URL printed in the response (defaults to server URL)")

	routineWebhooksUrlCmd.Flags().String("base-url", "", "override the public base URL")
	routineWebhooksDeleteCmd.Flags().Bool("yes", false, "skip the interactive confirmation prompt")

	routineWebhooksCmd.AddCommand(routineWebhooksListCmd)
	routineWebhooksCmd.AddCommand(routineWebhooksCreateCmd)
	routineWebhooksCmd.AddCommand(routineWebhooksUrlCmd)
	routineWebhooksCmd.AddCommand(routineWebhooksDeleteCmd)

	pipelineCmd.AddCommand(routineWebhooksCmd)
}
