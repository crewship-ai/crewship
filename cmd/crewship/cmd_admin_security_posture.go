//go:build !clionly

package main

// `crewship admin security-posture` — read-only view of the instance's
// env-driven security flags (#1379).
//
// These flags are deliberately NOT settable from the app: they are deploy
// decisions. But an admin still has to be able to SEE them, and until now the
// only way was shell access to read the process environment — which is exactly
// what the person triaging an incident often doesn't have.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

type postureWarningRow struct {
	Key      string `json:"key"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// securityPostureRow mirrors internal/api.securityPostureResponse.
type securityPostureRow struct {
	Environment                  string              `json:"environment"`
	EncryptionKeyConfigured      bool                `json:"encryption_key_configured"`
	PlaintextSecretsAllowed      bool                `json:"plaintext_secrets_allowed"`
	PrivateEndpointsCeiling      bool                `json:"private_endpoints_ceiling"`
	SignupOpen                   bool                `json:"signup_open"`
	OAuthConfigured              bool                `json:"oauth_configured"`
	EmailConfigured              bool                `json:"email_configured"`
	RateLimitDisabled            bool                `json:"rate_limit_disabled"`
	RateLimitEffectivelyDisabled bool                `json:"rate_limit_effectively_disabled"`
	Warnings                     []postureWarningRow `json:"warnings"`
}

// postureState renders a boolean as the words that match what it MEANS, not
// as true/false. "plaintext secrets: true" reads as fine at a glance; "ALLOWED
// (insecure)" does not, and the whole point of this command is the glance.
func postureState(v bool, whenTrue, whenFalse string) string {
	if v {
		return whenTrue
	}
	return whenFalse
}

var adminSecurityPostureCmd = &cobra.Command{
	Use:     "security-posture",
	Aliases: []string{"posture"},
	Short:   "Show the instance's env-driven security posture (admin; read-only)",
	Long: `Reports how this instance is postured: encryption at rest, the private-egress
ceiling, signup policy, rate limiting, and whether email/OAuth are configured.

Read-only by design. These are env-driven deploy decisions and are NOT settable
from the app or this command — the gap this closes is that their STATE was
invisible without shell access to the box.

No secret value is ever returned — only whether something is configured.

Examples:
  crewship admin security-posture
  crewship admin security-posture --format json | jq '.warnings[] | select(.severity=="high")'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Get("/api/v1/admin/security-posture")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var row securityPostureRow
		if err := json.NewDecoder(resp.Body).Decode(&row); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}

		return resolvedFormatter(cmd).AutoHuman(row, func() {
			env := row.Environment
			if env == "" {
				env = "(unset)"
			}
			fmt.Printf("Instance security posture — environment: %s\n\n", env)

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "SETTING\tSTATE")
			fmt.Fprintf(w, "Encryption key\t%s\n",
				postureState(row.EncryptionKeyConfigured, "configured", "NOT configured"))
			fmt.Fprintf(w, "Plaintext secrets\t%s\n",
				postureState(row.PlaintextSecretsAllowed, "ALLOWED (insecure)", "refused"))
			fmt.Fprintf(w, "Private-egress ceiling\t%s\n",
				postureState(row.PrivateEndpointsCeiling, "open", "closed"))
			fmt.Fprintf(w, "Signup\t%s\n",
				postureState(row.SignupOpen, "OPEN", "invite-only"))
			// Show intent and effect separately: in prod the limiter runs even
			// when the flag says otherwise, and collapsing that into one line
			// would either hide an exposure or invent one.
			rl := postureState(row.RateLimitDisabled, "disable flag set", "enabled")
			if row.RateLimitDisabled && !row.RateLimitEffectivelyDisabled {
				rl = "disable flag set, but IGNORED (production) — limiter running"
			} else if row.RateLimitEffectivelyDisabled {
				rl = "DISABLED"
			}
			fmt.Fprintf(w, "Rate limiter\t%s\n", rl)
			fmt.Fprintf(w, "Email (Resend)\t%s\n",
				postureState(row.EmailConfigured, "configured", "not configured"))
			fmt.Fprintf(w, "OAuth (Google)\t%s\n",
				postureState(row.OAuthConfigured, "configured", "not configured"))
			if err := w.Flush(); err != nil {
				fmt.Fprintf(os.Stderr, "warning: %v\n", err)
			}

			if len(row.Warnings) == 0 {
				fmt.Println("\nNo warnings — nothing in this instance's posture stands out.")
				return
			}
			fmt.Printf("\n%d warning(s):\n", len(row.Warnings))
			for _, wn := range row.Warnings {
				fmt.Printf("  [%s] %s\n", strings.ToUpper(wn.Severity), wn.Message)
			}
		})
	},
}

func init() {
	adminCmd.AddCommand(adminSecurityPostureCmd)
}
