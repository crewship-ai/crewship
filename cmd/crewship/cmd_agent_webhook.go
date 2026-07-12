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
Deliveries signed with the previous secret are rejected immediately.

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
		resp, err := client.Post(fmt.Sprintf("/api/v1/agents/%s/webhook-secret/rotate", agentID), nil)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			WebhookSecret string `json:"webhook_secret"`
			RotatedAt     string `json:"rotated_at"`
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
		fmt.Fprintln(os.Stderr, "Shown ONCE — store it now. The previous secret no longer validates.")
		return nil
	},
}
