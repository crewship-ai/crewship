package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"syscall"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh/terminal"
)

var loginTokenFlag string

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate CLI with the Crewship server",
	Long: `Authenticate the CLI with a Crewship server.

Interactive mode (email + password):
  crewship login

Token mode (API token):
  crewship login --token <api-token>`,
	RunE: func(cmd *cobra.Command, args []string) error {
		server := cli.ResolveServer(flagServer, cliCfg)

		if loginTokenFlag != "" {
			return loginWithToken(server, loginTokenFlag)
		}
		return loginInteractive(server)
	},
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
}

func loginWithToken(serverURL, token string) error {
	client := cli.NewClient(serverURL, token, "")
	resp, err := client.Get("/api/v1/auth/cli-token/validate")
	if err != nil {
		return fmt.Errorf("failed to connect to server: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		// Server doesn't have cli-token endpoint yet — fall back to workspace check
		resp.Body.Close()
		resp, err = client.Get("/api/v1/workspaces")
		if err != nil {
			return fmt.Errorf("failed to connect to server: %w", err)
		}
	}

	if err := cli.CheckError(resp); err != nil {
		return fmt.Errorf("token validation failed: %w", err)
	}
	resp.Body.Close()

	cfg, err := cli.LoadConfig()
	if err != nil {
		cfg = &cli.CLIConfig{}
	}
	cfg.Server = serverURL
	cfg.Token = token
	if err := cli.SaveConfig(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	cli.PrintSuccess("Login successful! Token saved to ~/.crewship/cli-config.yaml")
	return nil
}

func loginInteractive(serverURL string) error {
	reader := bufio.NewReader(os.Stdin)

	fmt.Printf("Crewship server: %s\n\n", serverURL)
	fmt.Print("Email: ")
	email, _ := reader.ReadString('\n')
	email = strings.TrimSpace(email)

	fmt.Print("Password: ")
	passwordBytes, err := terminal.ReadPassword(int(syscall.Stdin))
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

	req, _ := json.Marshal(loginBody)
	httpReq, _ := http.NewRequest("POST", serverURL+"/api/auth/callback/credentials", strings.NewReader(string(req)))
	httpReq.Header.Set("Content-Type", "application/json")
	if csrfCookie != "" {
		httpReq.AddCookie(&http.Cookie{Name: "authjs.csrf-token", Value: csrfCookie})
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
	if err == nil && cliTokenResp.StatusCode == http.StatusOK {
		var tokenResult struct {
			Token string `json:"token"`
		}
		if err := cli.ReadJSON(cliTokenResp, &tokenResult); err == nil && tokenResult.Token != "" {
			finalToken = tokenResult.Token
		}
	}

	// Fall back to session token if CLI token endpoint not available
	if finalToken == "" {
		finalToken = sessionToken
	}

	cfg, err := cli.LoadConfig()
	if err != nil {
		cfg = &cli.CLIConfig{}
	}
	cfg.Server = serverURL
	cfg.Token = finalToken
	if err := cli.SaveConfig(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	cli.PrintSuccess("Login successful! Token saved to ~/.crewship/cli-config.yaml")
	return nil
}
