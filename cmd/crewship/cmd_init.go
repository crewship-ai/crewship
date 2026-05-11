package main

import (
	"fmt"
	"os"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/crewship-ai/crewship/internal/cli"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize Crewship with the first admin user",
	Long: `Create the first admin user on a fresh Crewship instance.

This command only works on an empty database (no existing users).
It creates an admin user with OWNER role and returns a CLI token
for immediate access.

Example:
  crewship init --server http://localhost:8080 \
    --email admin@crewship.ai --name "Pavel Srba"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		serverURL, _ := cmd.Flags().GetString("server")
		email, _ := cmd.Flags().GetString("email")
		name, _ := cmd.Flags().GetString("name")
		password, _ := cmd.Flags().GetString("password")

		if serverURL == "" {
			serverURL = "http://localhost:8080"
		}
		if email == "" {
			return fmt.Errorf("--email is required")
		}
		if name == "" {
			return fmt.Errorf("--name is required")
		}

		if password == "" {
			fmt.Print("Password (min 8 chars): ")
			passwordBytes, err := term.ReadPassword(int(syscall.Stdin))
			if err != nil {
				return fmt.Errorf("read password: %w", err)
			}
			password = string(passwordBytes)
			fmt.Println()
		}

		if len(password) < 8 {
			return fmt.Errorf("password must be at least 8 characters")
		}

		client := cli.NewClient(serverURL, "", "")
		resp, err := client.Post("/api/v1/bootstrap", map[string]string{
			"email":     email,
			"full_name": name,
			"password":  password,
		})
		if err != nil {
			return fmt.Errorf("bootstrap request failed: %w", err)
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result struct {
			UserID      string `json:"user_id"`
			Email       string `json:"email"`
			WorkspaceID string `json:"workspace_id"`
			CLIToken    string `json:"cli_token"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}

		cli.PrintSuccess("Crewship initialized!")
		fmt.Fprintf(os.Stderr, "\n")
		fmt.Fprintf(os.Stderr, "  Admin:     %s\n", result.Email)
		fmt.Fprintf(os.Stderr, "  Workspace: %s\n", result.WorkspaceID)
		fmt.Fprintf(os.Stderr, "  CLI Token: %s\n", result.CLIToken)
		fmt.Fprintf(os.Stderr, "\nTo start using the CLI:\n\n")
		fmt.Fprintf(os.Stderr, "  crewship login --server %s --token %s\n\n", serverURL, result.CLIToken)

		return nil
	},
}

func init() {
	initCmd.Flags().String("server", "", "Crewship server URL (default http://localhost:8080)")
	initCmd.Flags().String("email", "", "Admin email address (required)")
	initCmd.Flags().String("name", "", "Admin full name (required)")
	initCmd.Flags().String("password", "", "Admin password (prompted if omitted)")
}
