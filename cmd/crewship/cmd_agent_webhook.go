package main

// crewship agent rotate-webhook-secret — CLI parity for
// POST /api/v1/agents/{agentId}/webhook-secret/rotate (#999).
//
// The webhook signing secret is show-once: no endpoint returns a stored
// secret back, so rotation is the only way to obtain one. The command
// prints the new value exactly once; the previous secret stops
// validating immediately.

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

var agentRotateWebhookSecretCmd = &cobra.Command{
	Use:   "rotate-webhook-secret <slug-or-id>",
	Short: "Mint a new webhook signing secret (shown ONCE; the old secret stops validating)",
	Long: `Rotates the agent's webhook signing secret. The new secret is printed
exactly once — it is never readable back from any API or CLI surface, so
store it in the external system (GitHub/Stripe/... webhook config) now.
Deliveries signed with the previous secret are rejected immediately, unless
--previous-secret-ttl (60s to 7d, e.g. 24h) keeps it valid for that long so the
sender can migrate. Each delivery verified with it is audited, and
"revoke-previous-webhook-secret" ends the overlap early. Rotating again during
an overlap replaces the previous secret: at most two are ever valid.

Signature scheme: X-Signature: <hex HMAC-SHA256 of the raw body, no
prefix> (see the webhooks API docs for the timestamped variant).`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeAgentSlug,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		agentID, err := resolveAgentID(client, args[0])
		if err != nil {
			return err
		}
		var reqBody any
		if cmd.Flags().Changed("previous-secret-ttl") {
			raw, _ := cmd.Flags().GetString("previous-secret-ttl")
			seconds, perr := parseSecretOverlapTTL(raw)
			if perr != nil {
				return cli.WithExitCode(fmt.Errorf("--previous-secret-ttl: %w", perr), cli.ExitValidation)
			}
			reqBody = map[string]int{"previous_secret_ttl_seconds": seconds}
		}
		resp, err := client.Post(fmt.Sprintf("/api/v1/agents/%s/webhook-secret/rotate", agentID), reqBody)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			WebhookSecret string `json:"webhook_secret" yaml:"webhook_secret"`
			RotatedAt     string `json:"rotated_at" yaml:"rotated_at"`
			PreviousUntil string `json:"previous_secret_expires_at,omitempty" yaml:"previous_secret_expires_at,omitempty"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		f := newFormatter()
		switch f.Format {
		case "json":
			return f.JSON(out)
		case "yaml":
			return f.YAML(out)
		}
		fmt.Printf("New webhook secret for %s:\n\n  %s\n\n", args[0], out.WebhookSecret)
		if out.PreviousUntil != "" {
			fmt.Fprintf(os.Stderr, "Shown ONCE — store it now. The previous secret keeps validating until %s.\n", out.PreviousUntil)
		} else {
			fmt.Fprintln(os.Stderr, "Shown ONCE — store it now. The previous secret no longer validates.")
		}
		return nil
	},
}

var agentRevokePreviousWebhookSecretCmd = &cobra.Command{
	Use:   "revoke-previous-webhook-secret <slug-or-id>",
	Short: "End a webhook secret rotation overlap now (only the current secret validates)",
	Long: `Revokes the previous webhook signing secret that a rotation with
--previous-secret-ttl left valid. Idempotent: with no overlap open it succeeds
and changes nothing.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeAgentSlug,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		agentID, err := resolveAgentID(client, args[0])
		if err != nil {
			return err
		}
		resp, err := client.Delete(fmt.Sprintf("/api/v1/agents/%s/webhook-secret/previous", agentID))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		out := struct {
			Revoked bool `json:"revoked" yaml:"revoked"`
		}{true}
		f := newFormatter()
		switch f.Format {
		case "json":
			return f.JSON(out)
		case "yaml":
			return f.YAML(out)
		}
		fmt.Printf("Previous webhook secret for %s revoked; only the current secret validates.\n", args[0])
		return nil
	},
}

func init() {
	agentRotateWebhookSecretCmd.Flags().String("previous-secret-ttl", "", "keep the replaced secret valid for this long (60s to 7d, e.g. 24h; default none) so senders can migrate")
}
