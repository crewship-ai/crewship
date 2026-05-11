package main

import (
	"bufio"
	"fmt"
	"os"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var credCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a credential",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		flags := cmd.Flags()
		name, _ := flags.GetString("name")
		credType, _ := flags.GetString("type")
		provider, _ := flags.GetString("provider")
		value, _ := flags.GetString("value")
		valueStdin, _ := flags.GetBool("value-stdin")
		envVarName, _ := flags.GetString("env-var-name")

		if name == "" {
			return fmt.Errorf("--name is required")
		}
		if credType == "" {
			return fmt.Errorf("--type is required (SECRET, API_KEY, AI_CLI_TOKEN, or CLI_TOKEN)")
		}

		if valueStdin {
			scanner := bufio.NewScanner(os.Stdin)
			if scanner.Scan() {
				value = scanner.Text()
			}
		}

		if value == "" {
			return fmt.Errorf("--value or --value-stdin is required")
		}

		secLevel, _ := flags.GetInt("security-level")
		if secLevel < 0 || secLevel > 3 {
			return fmt.Errorf("--security-level must be between 0 and 3")
		}

		body := map[string]interface{}{
			"name":  name,
			"type":  credType,
			"value": value,
		}
		if provider != "" {
			body["provider"] = provider
		}
		if envVarName != "" {
			body["env_var_name"] = envVarName
		}
		if secLevel >= 1 {
			body["security_level"] = secLevel
		}

		client := newAPIClient()

		valid, errMsg := testCredentialValue(client, provider, credType, value)
		if valid {
			cli.PrintSuccess("Key validated successfully")
		} else {
			msg := errMsg
			if msg == "" {
				msg = "key validation failed"
			}
			if !term.IsTerminal(int(os.Stdin.Fd())) {
				cli.PrintWarning(fmt.Sprintf("Key validation failed: %s (non-interactive, skipping confirmation)", msg))
			} else if !confirmInvalidKey(msg) {
				return fmt.Errorf("aborted")
			}
		}

		resp, err := client.Post("/api/v1/credentials", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var created struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := cli.ReadJSON(resp, &created); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf("Credential created: %s (%s)", created.Name, created.ID))
		return nil
	},
}

var credUpdateCmd = &cobra.Command{
	Use:   "update <name-or-id>",
	Short: "Update a credential",
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

		body := map[string]interface{}{}
		flags := cmd.Flags()

		if flags.Changed("value") {
			v, _ := flags.GetString("value")
			if v == "" {
				return fmt.Errorf("--value cannot be empty")
			}
			body["value"] = v
		}
		if flags.Changed("name") {
			v, _ := flags.GetString("name")
			body["name"] = v
		}
		if flags.Changed("value-stdin") {
			scanner := bufio.NewScanner(os.Stdin)
			if scanner.Scan() {
				v := scanner.Text()
				if v == "" {
					return fmt.Errorf("stdin value cannot be empty")
				}
				body["value"] = v
			}
		}
		if flags.Changed("security-level") {
			v, _ := flags.GetInt("security-level")
			if v < 0 || v > 3 {
				return fmt.Errorf("--security-level must be between 0 and 3")
			}
			body["security_level"] = v
		}

		if len(body) == 0 {
			return fmt.Errorf("no fields to update")
		}

		if val, ok := body["value"]; ok {
			if valStr, ok := val.(string); ok && valStr != "" {
				metaResp, metaErr := client.Get("/api/v1/credentials/" + credID)
				if metaErr != nil {
					cli.PrintWarning("Could not fetch credential metadata for validation: " + metaErr.Error())
				} else if err := cli.CheckError(metaResp); err != nil {
					cli.PrintWarning("Could not fetch credential metadata for validation: " + err.Error())
				} else {
					var cred struct {
						Type     string `json:"type"`
						Provider string `json:"provider"`
					}
					if err := cli.ReadJSON(metaResp, &cred); err != nil {
						cli.PrintWarning("Could not parse credential metadata, skipping validation: " + err.Error())
					} else {
						valid, errMsg := testCredentialValue(client, cred.Provider, cred.Type, valStr)
						if valid {
							cli.PrintSuccess("Key validated successfully")
						} else {
							msg := errMsg
							if msg == "" {
								msg = "key validation failed"
							}
							if !term.IsTerminal(int(os.Stdin.Fd())) {
								cli.PrintWarning(fmt.Sprintf("Key validation failed: %s (non-interactive, skipping confirmation)", msg))
							} else if !confirmInvalidKey(msg) {
								return fmt.Errorf("aborted")
							}
						}
					}
				}
			}
		}

		resp, err := client.Patch("/api/v1/credentials/"+credID, body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Credential updated.")
		return nil
	},
}

// credRotateCmd issues a new value for a credential and starts a grace
// overlap. Destructive (the old value is moved to a rotation row and
// scrubbed after the grace window expires) so it gates behind a confirm
// prompt unless --yes is passed.
//
// Flag shape mirrors `credential create`: the new value can come on the
// command line (--value, visible in `ps`) or from stdin (--value-stdin,
// preferred for scripts).
var credRotateCmd = &cobra.Command{
	Use:   "rotate <name-or-id>",
	Short: "Rotate a credential value with a grace-overlap window",
	Long: `Issue a new value for the credential. The old value is preserved
on the rotation row for --grace-seconds (default 24h, max 7d) so
in-flight agents that cached the old key can still fall back during
their run, then the old value is scrubbed.

Examples:
  crewship credential rotate gh-token --value sk_new_... --yes
  echo "$NEW" | crewship credential rotate gh-token --value-stdin
  crewship credential rotate gh-token --value-stdin --grace-seconds 0  # immediate cutover`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		flags := cmd.Flags()
		value, _ := flags.GetString("value")
		valueStdin, _ := flags.GetBool("value-stdin")
		if valueStdin {
			scanner := bufio.NewScanner(os.Stdin)
			if scanner.Scan() {
				value = scanner.Text()
			}
		}
		if value == "" {
			return fmt.Errorf("--value or --value-stdin is required")
		}

		if err := confirmAction(cmd, fmt.Sprintf("Rotate credential %q? The old value will be scrubbed after the grace window.", args[0])); err != nil {
			return err
		}

		client := newAPIClient()
		credID, err := resolveCredentialID(client, args[0])
		if err != nil {
			return err
		}

		body := map[string]interface{}{"value": value}
		if flags.Changed("grace-seconds") {
			gs, _ := flags.GetInt("grace-seconds")
			body["grace_seconds"] = gs
		}

		resp, err := client.Post("/api/v1/credentials/"+credID+"/rotate", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var out struct {
			ID           string `json:"id"`
			Status       string `json:"status"`
			GraceSeconds int    `json:"grace_seconds"`
			ExpiresAt    string `json:"expires_at"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf(
			"Rotation %s started (grace %ds, expires %s)",
			out.ID, out.GraceSeconds, out.ExpiresAt,
		))
		return nil
	},
}

// credRotationCancelCmd ends an ACTIVE grace window immediately and
// scrubs the old value. EXPIRED / CANCELLED rotations are no-ops on
// the server side (idempotent 200), so the command still succeeds.
var credRotationCancelCmd = &cobra.Command{
	Use:   "rotation-cancel <rotation-id>",
	Short: "End an ACTIVE rotation's grace window early (scrubs old value)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		if err := confirmAction(cmd, fmt.Sprintf("Cancel rotation %q? The old value will be scrubbed immediately.", args[0])); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Delete("/api/v1/credential-rotations/" + args[0])
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		if out.Message != "" {
			cli.PrintSuccess(fmt.Sprintf("Rotation %s: %s (%s)", args[0], out.Status, out.Message))
		} else {
			cli.PrintSuccess(fmt.Sprintf("Rotation %s cancelled.", args[0]))
		}
		return nil
	},
}

var credDeleteCmd = &cobra.Command{
	Use:   "delete <name-or-id>",
	Short: "Delete a credential",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		if err := confirmAction(cmd, fmt.Sprintf("Delete credential %q?", args[0])); err != nil {
			return err
		}

		client := newAPIClient()
		credID, err := resolveCredentialID(client, args[0])
		if err != nil {
			return err
		}
		resp, err := client.Delete("/api/v1/credentials/" + credID)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Credential deleted.")
		return nil
	},
}
