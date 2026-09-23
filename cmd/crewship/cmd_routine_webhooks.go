package main

// Routine webhook subcommands. Webhooks are token-addressed event
// triggers: external services POST to /api/v1/webhooks/{token} and
// the matching routine fires with the request body delivered as the
// `event` input. HMAC verification is supported when the webhook has
// a signing_secret configured.
//
// Stripe-style secret reveal: the signing secret is shown ONCE on
// create response and once more on an explicit `update --rotate-secret`
// (F21, B9 #2362). Everything else about a webhook — name, rate limit,
// inputs template, enabled state — is editable in place via `update`
// without touching the token, so existing senders never see their URL
// change.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

type WebhookRow struct {
	IngressProfile        string                 `json:"ingress_profile" yaml:"ingress_profile"`
	ID                    string                 `json:"id" yaml:"id"`
	WorkspaceID           string                 `json:"workspace_id" yaml:"workspace_id"`
	Name                  string                 `json:"name" yaml:"name"`
	TargetPipelineID      string                 `json:"target_pipeline_id" yaml:"target_pipeline_id"`
	TargetPipelineSlug    string                 `json:"target_pipeline_slug,omitempty" yaml:"target_pipeline_slug,omitempty"`
	TargetPipelineVersion *int                   `json:"target_pipeline_version,omitempty" yaml:"target_pipeline_version,omitempty"`
	Token                 string                 `json:"token" yaml:"token"`
	SigningSecretSet      bool                   `json:"signing_secret_set" yaml:"signing_secret_set"`
	SigningSecret         string                 `json:"signing_secret,omitempty" yaml:"signing_secret,omitempty"`
	InputsTemplate        map[string]interface{} `json:"inputs_template" yaml:"inputs_template"`
	Enabled               bool                   `json:"enabled" yaml:"enabled"`
	RateLimitPerMin       int                    `json:"rate_limit_per_min" yaml:"rate_limit_per_min"`
	LastFiredAt           *string                `json:"last_fired_at,omitempty" yaml:"last_fired_at,omitempty"`
	LastStatus            *string                `json:"last_status,omitempty" yaml:"last_status,omitempty"`
	LastRunID             *string                `json:"last_run_id,omitempty" yaml:"last_run_id,omitempty"`
	FireCount             int64                  `json:"fire_count" yaml:"fire_count"`
	CreatedAt             string                 `json:"created_at" yaml:"created_at"`
	UpdatedAt             string                 `json:"updated_at" yaml:"updated_at"`
}

// webhookCreateResult is `routine webhooks create`'s machine payload: the
// whole row (this is the one moment the signing secret exists in plaintext)
// plus the URL a sender is configured with.
//
// A NAMED type rather than an anonymous struct so cli_yaml_key_parity_test.go
// can hold it to the json/yaml contract — a payload the guard cannot name is a
// payload the guard cannot check.
type webhookCreateResult struct {
	WebhookRow `json:",inline" yaml:",inline"`
	PublicURL  string `json:"public_url" yaml:"public_url"`
}

// webhookURLResult is `routine webhooks url`'s machine payload. The human form
// is the bare URL, because this command exists so you can paste it into a
// sender's config; a machine caller gets a document.
type webhookURLResult struct {
	WebhookID string `json:"webhook_id" yaml:"webhook_id"`
	URL       string `json:"url" yaml:"url"`
}

var routineWebhooksCmd = &cobra.Command{
	Use:   "webhooks",
	Short: "Manage event-driven webhook triggers",
	Long: `Webhooks fire saved routines when external services POST to
/api/v1/webhooks/{token}. Each webhook is named, targets one routine,
HMAC-signed for delivery integrity, and rate-limited per token. The
signing secret is revealed only once on create; rotate it in place with
"update <webhook_id> --rotate-secret" (the URL survives — only delete +
recreate mints a new token).

An endpoint's ingress profile fixes how a delivery is signed and shaped:
"crewship" (the default) verifies X-Crewship-Signature over the raw body;
"github" verifies X-Hub-Signature-256 and accepts only pull_request
events, at the public URL's /github-pull-request suffix; "unsigned"
dispatches with no HMAC at all — the 256-bit random token in the URL is
the whole credential, intended for senders who cannot sign (e.g. Coolify
notifications). The profile is chosen at create time and cannot be
changed afterwards.

Examples:
  crewship routine webhooks list
  crewship routine webhooks list --slug summarize-text
  crewship routine webhooks create --slug pr-review-structured \
      --name "github-pr-reviews" --hmac-secret "$(openssl rand -hex 32)" \
      --rate-limit 30
  crewship routine webhooks create --slug summarize-text  # generate and show HMAC secret
  crewship routine webhooks create --slug pr-review --ingress-profile github
  crewship routine webhooks fire <public_url> --secret <signing_secret> --body @event.json
  crewship routine webhooks update <webhook_id> --rotate-secret
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
		var rows []WebhookRow
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
		f := resolvedFormatter(cmd)
		if f.Format == "json" || f.Format == "yaml" || f.Format == "ndjson" {
			// Redact tokens + secrets from machine output. The list
			// endpoint returns webhook tokens (the public URL
			// segment) and signing_secret_set flags; piping --json
			// to a log/share could leak the public URL or
			// inadvertently confirm secret-set state for sensitive
			// webhooks. The user can fetch the full record via the
			// `url` subcommand when they explicitly need it. IDs stay
			// full-length so scripts can feed them to delete/update —
			// the human table below truncates them via shortID.
			redacted := make([]WebhookRow, len(rows))
			for i, r := range rows {
				redacted[i] = r
				if redacted[i].Token != "" {
					redacted[i].Token = redactedShort(redacted[i].Token)
				}
				redacted[i].SigningSecret = ""
			}
			switch {
			case f.Format == "yaml":
				return f.YAML(redacted)
			case f.Format == "ndjson":
				return f.NDJSON(redacted)
			default:
				return f.JSON(redacted)
			}
		}
		if len(rows) == 0 {
			fmt.Println("No webhooks in this workspace.")
			fmt.Println("Create one: crewship routine webhooks create --slug <routine>")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tROUTINE\tPROFILE\tHMAC\tFIRES\tLAST STATUS\tRATE/MIN\tENABLED")
		for _, h := range rows {
			// Rows written before the profile column existed carry an
			// empty value; the store treats that as the default.
			profile := h.IngressProfile
			if profile == "" {
				profile = "crewship"
			}
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
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%d\t%s\n",
				shortID(h.ID), h.Name, routineCell(h.TargetPipelineSlug, h.TargetPipelineVersion), profile, hmac, h.FireCount, lastStatus, h.RateLimitPerMin, enabled)
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
		if cmd.Flags().Changed("pin-version") {
			pin, _ := cmd.Flags().GetInt("pin-version")
			if pin < 1 {
				return fmt.Errorf("--pin-version must be a positive routine version number")
			}
			body["target_pipeline_version"] = pin
		}
		if hmac != "" {
			body["signing_secret"] = hmac
		}
		// Validated here against the same enum CreateWebhook enforces, so a
		// typo is a flag error and never a round trip. Absent means the
		// server default (crewship); the profile cannot be changed by
		// `update`, so this is the one place it is chosen.
		if profile, _ := cmd.Flags().GetString("ingress-profile"); profile != "" {
			if profile != webhookProfileCrewship && profile != webhookProfileGitHub && profile != webhookProfileUnsigned {
				return fmt.Errorf("--ingress-profile must be %s, %s or %s", webhookProfileCrewship, webhookProfileGitHub, webhookProfileUnsigned)
			}
			body["ingress_profile"] = profile
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
		var w WebhookRow
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
		publicURL := routineWebhookPublicURL(baseURL, w)

		// This used to always print the human block regardless of
		// --format. Unlike `webhooks list` (which redacts tokens/secrets
		// on machine output since it can be re-run any time), `create`
		// is the ONE moment the signing secret exists in plaintext — a
		// script capturing it via --format json needs the full,
		// unredacted WebhookRow, not a stripped-down view. AutoHuman
		// keeps the human "shown once, copy now" block byte-identical
		// for table/quiet/default (#1195).
		// The embedded row is EXPORTED and carries both inline tags. An
		// untagged embedded field is flattened by encoding/json and nested by
		// yaml.v3 — and when the embedded type is unexported, yaml.v3 cannot
		// reflect into it at all and `-f yaml` panics rather than printing
		// anything. This wrapper did exactly that until the format sweep made
		// the command honour `-f yaml` and turned it into a reachable crash.
		result := webhookCreateResult{WebhookRow: w, PublicURL: publicURL}
		return newFormatter().AutoHuman(result, func() {
			fmt.Println("Webhook created.")
			fmt.Printf("  ID:        %s\n", w.ID)
			fmt.Printf("  Name:      %s\n", w.Name)
			fmt.Printf("  Routine:   %s\n", routineCell(w.TargetPipelineSlug, w.TargetPipelineVersion))
			if w.IngressProfile == webhookProfileGitHub {
				fmt.Printf("  Profile:   %s\n", w.IngressProfile)
			}
			fmt.Printf("  Public URL: %s\n", publicURL)
			fmt.Printf("  Rate limit: %d / minute\n", w.RateLimitPerMin)
			if w.SigningSecret != "" {
				fmt.Println()
				fmt.Println("== HMAC signing secret (shown once, copy now) ==")
				fmt.Println(w.SigningSecret)
				if w.IngressProfile == webhookProfileGitHub {
					fmt.Println("== Paste it as the GitHub webhook secret; GitHub signs each delivery as X-Hub-Signature-256: sha256=<hex_hmac_of_body>")
				} else {
					fmt.Println("== Senders MUST include header: X-Crewship-Signature: sha256=<hex_hmac_of_body>")
				}
			}
		})
	},
}

// webhookUpdateResult is `routine webhooks update`'s machine payload. The
// signing secret only appears when this call actually rotated it
// (--rotate-secret) — same show-once contract as create.
type webhookUpdateResult struct {
	WebhookRow `json:",inline" yaml:",inline"`
	Rotated    bool `json:"rotated" yaml:"rotated"`
}

var routineWebhooksUpdateCmd = &cobra.Command{
	Use:   "update <webhook_id>",
	Short: "Edit a webhook in place (F21) — name, rate limit, inputs template; the URL never rotates",
	Long: `Edit an existing webhook without losing the URL a sender already has
configured. Before this command the only way to change anything about a
webhook was delete + recreate, which mints a new token and therefore a new
URL — every configured sender broke.

The signing secret is preserved unless --rotate-secret is passed explicitly;
the token (and so the public URL) never changes through this command at
all — rotating the URL still requires delete + recreate.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body := map[string]interface{}{}
		if v, _ := cmd.Flags().GetString("name"); v != "" {
			body["name"] = v
		}
		if v, _ := cmd.Flags().GetString("slug"); v != "" {
			body["target_pipeline_slug"] = v
		}
		if cmd.Flags().Changed("rate-limit") {
			v, _ := cmd.Flags().GetInt("rate-limit")
			if v <= 0 {
				return fmt.Errorf("--rate-limit must be a positive integer")
			}
			body["rate_limit_per_min"] = v
		}
		if v, _ := cmd.Flags().GetString("inputs-template"); v != "" {
			var tpl map[string]interface{}
			if err := json.Unmarshal([]byte(v), &tpl); err != nil {
				return fmt.Errorf("--inputs-template must be valid JSON: %w", err)
			}
			body["inputs_template"] = tpl
		}
		if cmd.Flags().Changed("enabled") {
			v, _ := cmd.Flags().GetBool("enabled")
			body["enabled"] = v
		}
		// Version pin — same --pin-version/--unpin pair as `schedules
		// update`: absent mentions neither and keeps the existing pin,
		// --unpin sends an explicit null to clear it.
		unpin, _ := cmd.Flags().GetBool("unpin")
		if cmd.Flags().Changed("pin-version") {
			if unpin {
				return fmt.Errorf("--pin-version and --unpin are mutually exclusive")
			}
			pin, _ := cmd.Flags().GetInt("pin-version")
			if pin < 1 {
				return fmt.Errorf("--pin-version must be a positive routine version number")
			}
			body["target_pipeline_version"] = pin
		}
		if unpin {
			body["target_pipeline_version"] = nil
		}
		rotate, _ := cmd.Flags().GetBool("rotate-secret")
		if rotate {
			body["rotate_secret"] = true
		}
		if len(body) == 0 {
			return fmt.Errorf("at least one of --name / --slug / --rate-limit / --inputs-template / --enabled / --pin-version / --unpin / --rotate-secret required")
		}
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Patch(fmt.Sprintf("/api/v1/workspaces/%s/pipeline-webhooks/%s", ws, args[0]), body)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var w WebhookRow
		if err := json.NewDecoder(resp.Body).Decode(&w); err != nil {
			return fmt.Errorf("webhook may have been updated but response decode failed: %w", err)
		}
		result := webhookUpdateResult{WebhookRow: w, Rotated: w.SigningSecret != ""}
		return resolvedFormatter(cmd).AutoHuman(result, func() {
			fmt.Printf("Webhook %s updated.\n", w.ID)
			fmt.Printf("  Name:       %s\n", w.Name)
			fmt.Printf("  Rate limit: %d / minute\n", w.RateLimitPerMin)
			if w.SigningSecret != "" {
				fmt.Println()
				fmt.Println("== New HMAC signing secret (shown once, copy now) ==")
				fmt.Println(w.SigningSecret)
			} else {
				fmt.Println("  URL/secret: unchanged")
			}
		})
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
		var rows []WebhookRow
		if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		for _, w := range rows {
			if w.ID == args[0] {
				if w.Token == "" {
					// #1888: the token is hashed at rest, so list/get
					// return an empty one and there is no URL to print.
					// This used to print "…/api/v1/webhooks/" with
					// nothing after the slash and exit 0 — the user
					// pasted a truncated URL into their sender config
					// and it 404'd with no clue why.
					return fmt.Errorf(
						"webhook %s exists but its token is not retrievable: tokens are stored hashed and shown only once, in the create response.\n"+
							"Re-create the webhook to get a new URL (senders must be updated):\n"+
							"  crewship routine webhooks delete %s --yes\n"+
							"  crewship routine webhooks create --slug %s",
						args[0], args[0], routineSlugOrPlaceholder(w))
				}
				baseURL, _ := cmd.Flags().GetString("base-url")
				if baseURL == "" {
					baseURL = clientBaseURL(client)
				}
				full := routineWebhookPublicURL(baseURL, w)
				// The bare URL stays the human output — this command exists so
				// you can paste it into a sender's config, and a JSON envelope
				// around it would be in the way. Under a machine format it
				// becomes an object, because a bare URL on stdout is not a
				// document and `-f json` promises one.
				return resolvedFormatter(cmd).AutoHuman(webhookURLResult{
					WebhookID: w.ID, URL: full,
				}, func() {
					fmt.Println(full)
				})
			}
		}
		return cli.NotFoundf("webhook %s not found", args[0])
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

// routineSlugOrPlaceholder names the routine to re-create a webhook against.
// The list response usually carries the slug; when it does not, the message
// still has to be copy-pasteable, so it says what to fill in.
func routineSlugOrPlaceholder(w WebhookRow) string {
	if w.TargetPipelineSlug != "" {
		return w.TargetPipelineSlug
	}
	return "<routine-slug>"
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
	routineWebhooksListCmd.Flags().Bool("json", false, "Deprecated alias for --format json")

	routineWebhooksCreateCmd.Flags().String("slug", "", "target routine slug (REQUIRED)")
	routineWebhooksCreateCmd.Flags().String("name", "", "human-readable webhook name (default: '<slug> webhook')")
	routineWebhooksCreateCmd.Flags().String("hmac-secret", "", "HMAC signing secret — empty generates a secret shown once")
	routineWebhooksCreateCmd.Flags().Int("rate-limit", 60, "max fires per minute per webhook (default 60)")
	routineWebhooksCreateCmd.Flags().String("inputs-template", "", "JSON template merged with the request body to form routine inputs")
	routineWebhooksCreateCmd.Flags().String("base-url", "", "override the public base URL printed in the response (defaults to server URL)")
	routineWebhooksCreateCmd.Flags().Int("pin-version", 0, "pin the webhook to a specific routine version — every fire executes that immutable version instead of head; if the version is later deleted the fire FAILS (409) rather than silently running head")
	routineWebhooksCreateCmd.Flags().String("ingress-profile", "", "signature profile: crewship (default; X-Crewship-Signature over the body), github (X-Hub-Signature-256, pull_request events only, public URL ends in /github-pull-request), or unsigned (no HMAC at all — bearer token in the URL is the credential); fixed for the webhook's lifetime")

	routineWebhooksUpdateCmd.Flags().String("name", "", "new webhook name")
	routineWebhooksUpdateCmd.Flags().String("slug", "", "retarget to a different routine slug")
	routineWebhooksUpdateCmd.Flags().Int("rate-limit", 0, "new max fires per minute")
	routineWebhooksUpdateCmd.Flags().String("inputs-template", "", "replace the JSON template merged with the request body to form routine inputs")
	routineWebhooksUpdateCmd.Flags().Bool("enabled", false, "set enabled state — explicit flag presence required (--enabled / --enabled=false)")
	routineWebhooksUpdateCmd.Flags().Int("pin-version", 0, "pin (or re-pin) the webhook to a specific routine version; fires execute that immutable version instead of head")
	routineWebhooksUpdateCmd.Flags().Bool("unpin", false, "remove the version pin (fires track head again); updates that mention neither --pin-version nor --unpin keep the existing pin")
	routineWebhooksUpdateCmd.Flags().Bool("rotate-secret", false, "mint a new HMAC signing secret (shown once); the URL/token is NEVER rotated by this command regardless")

	routineWebhooksUrlCmd.Flags().String("base-url", "", "override the public base URL")
	routineWebhooksDeleteCmd.Flags().Bool("yes", false, "skip the interactive confirmation prompt")

	routineWebhooksFireCmd.Flags().String("secret", "", "the webhook's HMAC signing secret, as revealed by create / update --rotate-secret (REQUIRED)")
	routineWebhooksFireCmd.Flags().String("body", "", "request body: inline text, @file, or - for stdin (REQUIRED)")
	routineWebhooksFireCmd.Flags().String("profile", "", "signature profile to sign with: crewship or github (default: github when the URL ends in /github-pull-request, else crewship)")
	routineWebhooksFireCmd.Flags().String("event", "pull_request", "github profile only: the X-GitHub-Event header")
	routineWebhooksFireCmd.Flags().String("delivery-id", "", "the sender's delivery id — X-GitHub-Delivery on the github profile, X-Crewship-Event-ID on crewship (default: a fresh random id)")
	routineWebhooksFireCmd.Flags().Bool("timestamp", false, "crewship profile only: send X-Crewship-Timestamp and sign \"<timestamp>.<body>\" (the replay-safe scheme)")
	routineWebhooksFireCmd.Flags().String("base-url", "", "public base URL to prepend when the argument is a bare token (defaults to the configured server URL)")

	routineWebhooksCmd.AddCommand(routineWebhooksListCmd)
	routineWebhooksCmd.AddCommand(routineWebhooksCreateCmd)
	routineWebhooksCmd.AddCommand(routineWebhooksUpdateCmd)
	routineWebhooksCmd.AddCommand(routineWebhooksUrlCmd)
	routineWebhooksCmd.AddCommand(routineWebhooksDeleteCmd)
	routineWebhooksCmd.AddCommand(routineWebhooksFireCmd)

	pipelineCmd.AddCommand(routineWebhooksCmd)
}

// The two ingress profiles CreateWebhook accepts (internal/api/pipeline_webhooks.go)
// and the public-URL suffix that selects the GitHub verifier
// (internal/api/router_pipelines.go).
const (
	webhookProfileCrewship = "crewship"
	webhookProfileGitHub   = "github"
	webhookProfileUnsigned = "unsigned"
	webhookGitHubURLSuffix = "/github-pull-request"
)

func routineWebhookPublicURL(baseURL string, w WebhookRow) string {
	suffix := ""
	if w.IngressProfile == webhookProfileGitHub {
		suffix = webhookGitHubURLSuffix
	}
	return strings.TrimRight(baseURL, "/") + "/api/v1/webhooks/" + url.PathEscape(w.Token) + suffix
}
