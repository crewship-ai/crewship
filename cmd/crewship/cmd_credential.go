package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var credentialCmd = &cobra.Command{
	Use:     "credential",
	Aliases: []string{"cred"},
	Short:   "Manage credentials",
}

var credListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all credentials in the workspace",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/credentials")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var creds []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Type       string `json:"type"`
			Provider   string `json:"provider"`
			Status     string `json:"status"`
			AgentCount int    `json:"_count_agent_credentials"`
		}
		if err := cli.ReadJSON(resp, &creds); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "NAME", "TYPE", "PROVIDER", "STATUS", "AGENTS"}
		var rows [][]string
		for _, c := range creds {
			rows = append(rows, []string{c.ID, c.Name, c.Type, c.Provider, c.Status, fmt.Sprintf("%d", c.AgentCount)})
		}
		return f.Auto(creds, headers, rows)
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
		credID, err := resolveCredentialID(client, args[0])
		if err != nil {
			return err
		}
		resp, err := client.Get("/api/v1/credentials/" + credID)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
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
		return nameOrID, nil
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
	return "", fmt.Errorf("credential %q not found", nameOrID)
}

// testCredentialValue validates a credential value against the provider API.
// Returns (valid, errorMessage). Skips test for SECRET type, NONE provider,
// and OAuth tokens (sk-ant-oat*) which cannot be validated via API.
func testCredentialValue(client *cli.Client, provider, credType, value string) (bool, string) {
	if credType == "SECRET" || provider == "" || provider == "NONE" {
		return true, ""
	}
	if strings.HasPrefix(value, "sk-ant-oat") {
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
		path := fmt.Sprintf("/api/v1/credentials/%s/audit?limit=%d", credID, limit)
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
		}
		if err := cli.ReadJSON(resp, &events); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"TIME", "EVENT", "AGENT", "IP"}
		var rows [][]string
		for _, e := range events {
			ts := e.OccurredAt
			if t, err := time.Parse(time.RFC3339Nano, e.OccurredAt); err == nil {
				ts = t.Format("2006-01-02 15:04:05")
			} else if t, err := time.Parse(time.RFC3339, e.OccurredAt); err == nil {
				ts = t.Format("2006-01-02 15:04:05")
			}
			agent := "-"
			if e.AgentID != nil && *e.AgentID != "" {
				agent = *e.AgentID
			}
			ip := "-"
			if e.IPAddress != nil && *e.IPAddress != "" {
				ip = *e.IPAddress
			}
			rows = append(rows, []string{ts, e.EventType, agent, ip})
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
			Valid  bool   `json:"valid"`
			Status int    `json:"status"`
			Error  string `json:"error"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
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
		provider, _ := cmd.Flags().GetString("provider")
		if provider == "" {
			return fmt.Errorf("--provider is required")
		}

		client := newAPIClient()
		// Endpoint is workspace-agnostic — clear ws to match the
		// existing pre-save `credential test` invocation.
		client.WorkspaceID = ""
		resp, err := client.Get("/api/v1/credentials/default-env-var?provider=" + provider)
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
	credCreateCmd.Flags().String("name", "", "Credential name (required)")
	credCreateCmd.Flags().String("type", "", "Type: SECRET|API_KEY|AI_CLI_TOKEN|CLI_TOKEN (required)")
	credCreateCmd.Flags().String("provider", "", "Provider: ANTHROPIC|OPENAI|GOOGLE|GITHUB|GITLAB|VERCEL|AWS|CUSTOM_CLI|NONE")
	credCreateCmd.Flags().String("value", "", "Credential value (visible in process list, prefer --value-stdin)")
	credCreateCmd.Flags().Bool("value-stdin", false, "Read value from stdin (secure)")
	credCreateCmd.Flags().String("env-var-name", "", "Environment variable name")
	credCreateCmd.Flags().Int("security-level", 0, "Keeper security level: 0 (none), 1 (low), 2 (medium), 3 (sensitive)")

	credUpdateCmd.Flags().String("name", "", "Credential name")
	credUpdateCmd.Flags().String("value", "", "New value")
	credUpdateCmd.Flags().Bool("value-stdin", false, "Read value from stdin")
	credUpdateCmd.Flags().Int("security-level", 0, "Keeper security level: 0 (none), 1 (low), 2 (medium), 3 (sensitive)")

	credAssignCmd.Flags().String("env-var-name", "", "Environment variable name override")
	credAssignCmd.Flags().Int("priority", 0, "Priority (1-10)")

	credDeleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	credTestCmd.Flags().String("provider", "", "Provider: ANTHROPIC|OPENAI|GOOGLE|GITHUB|GITLAB|VERCEL|AWS|CUSTOM_CLI (required)")
	credTestCmd.Flags().String("type", "", "Type: API_KEY|AI_CLI_TOKEN|SECRET|CLI_TOKEN")
	credTestCmd.Flags().String("value", "", "Credential value to test")
	credTestCmd.Flags().Bool("value-stdin", false, "Read value from stdin")

	credAuditCmd.Flags().Int("limit", 50, "Max audit events to return (1-500)")
	credDefaultEnvVarCmd.Flags().String("provider", "", "Provider: GITHUB|GITLAB|VERCEL|AWS|KUBERNETES (required)")

	credRotateCmd.Flags().String("value", "", "New credential value (visible in process list, prefer --value-stdin)")
	credRotateCmd.Flags().Bool("value-stdin", false, "Read new value from stdin (secure)")
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
