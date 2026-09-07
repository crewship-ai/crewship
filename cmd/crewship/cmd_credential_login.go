package main

// `crewship credential login` — sign in to a model provider with a device
// code (docs/prd/provider-logins.md §10.3). The server drives the provider's
// device flow; this command only shows the code, waits, and prints the
// credential the server created.

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// deviceLoginStart is the server's answer to starting a sign-in.
type deviceLoginStart struct {
	DeviceID        string `json:"device_id"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresAt       string `json:"expires_at"`
	IntervalS       int    `json:"interval_s"`
}

// deviceLoginStatus is one status read.
type deviceLoginStatus struct {
	Status       string  `json:"status"`
	CredentialID *string `json:"credential_id"`
	Error        *string `json:"error"`
}

// deviceLoginPollFloor is the least often the CLI asks the server. The
// server's own poll of the provider runs at the provider's interval; this
// only reads a row, so it can be a little quicker than that without cost.
const deviceLoginPollFloor = 2 * time.Second

var credLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Sign in to a model provider with a device code",
	Long: `Sign in to a model provider with a device code and store the login as a
credential in the workspace.

The command prints a one-time code and a URL. Open the URL in any browser,
sign in to the provider account that pays for the model, enter the code, and
come back: the server finishes the sign-in, stores the login as a PROVIDER_LOGIN
credential owned by you, and the command prints it.

Only OpenAI (ChatGPT plans, for the Codex adapter) offers this today:

  crewship credential login --provider OPENAI

The stored login is delivered to Codex agents as a rendered auth.json whose
refresh token never leaves the server — see the Codex CLI guide.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		provider := strings.ToUpper(strings.TrimSpace(mustFlagString(cmd, "provider")))
		if provider == "" {
			return cli.WithExitCode(fmt.Errorf("--provider is required (OPENAI)"), cli.ExitValidation)
		}
		mode := strings.ToLower(strings.TrimSpace(mustFlagString(cmd, "mode")))
		noWait, _ := cmd.Flags().GetBool("no-wait")

		client := newAPIClient()
		body := map[string]string{"provider": provider}
		if mode != "" {
			body["mode"] = mode
		}
		resp, err := client.Post("/api/v1/provider-logins/device", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var started deviceLoginStart
		if err := cli.ReadJSON(resp, &started); err != nil {
			return err
		}

		f := newFormatter()
		if !f.RoutesToHuman() && noWait {
			return f.Machine(started)
		}
		fmt.Fprintf(os.Stderr, "\nSign in to %s with a device code:\n\n", provider)
		fmt.Fprintf(os.Stderr, "  1. Open   %s\n", started.VerificationURL)
		fmt.Fprintf(os.Stderr, "  2. Enter  %s\n\n", started.UserCode)
		if expires, perr := time.Parse(time.RFC3339, started.ExpiresAt); perr == nil {
			fmt.Fprintf(os.Stderr, "The code expires in %d minutes. Continue only if you started this sign-in yourself.\n\n",
				int(time.Until(expires).Minutes()))
		}
		if noWait {
			fmt.Fprintf(os.Stderr, "Not waiting (--no-wait). Check later with:\n  crewship credential login-status %s\n", started.DeviceID)
			return f.AutoDetail(started, [][]string{
				{"Device ID", started.DeviceID},
				{"Code", started.UserCode},
				{"URL", started.VerificationURL},
				{"Expires", started.ExpiresAt},
			})
		}

		interval := time.Duration(started.IntervalS) * time.Second
		if interval < deviceLoginPollFloor {
			interval = deviceLoginPollFloor
		}
		fmt.Fprint(os.Stderr, "Waiting for the sign-in to finish")
		expires, err := time.Parse(time.RFC3339, started.ExpiresAt)
		if err != nil {
			return fmt.Errorf("invalid device sign-in expiry: %w", err)
		}
		final, err := waitForDeviceLogin(client, started.DeviceID, interval, expires)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return err
		}
		switch final.Status {
		case "complete":
			cli.PrintSuccess("Signed in. Credential " + *final.CredentialID + " created.")
			return printCredentialDetail(client, f, *final.CredentialID)
		case "expired":
			return cli.WithExitCode(fmt.Errorf("the code expired before it was entered; run the command again for a new one"), cli.ExitNotFound)
		default:
			msg := "the sign-in was not completed"
			if final.Error != nil && *final.Error != "" {
				msg = *final.Error
			}
			return cli.WithExitCode(fmt.Errorf("sign-in %s: %s", final.Status, msg), cli.ExitValidation)
		}
	},
}

var credLoginStatusCmd = &cobra.Command{
	Use:   "login-status <device-id>",
	Short: "Show the state of a device-code sign-in started with --no-wait",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		client := newAPIClient()
		st, raw, err := readDeviceLoginStatus(client, args[0])
		if err != nil {
			return err
		}
		pairs := [][]string{{"Status", st.Status}}
		if st.CredentialID != nil {
			pairs = append(pairs, []string{"Credential", *st.CredentialID})
		}
		if st.Error != nil {
			pairs = append(pairs, []string{"Error", *st.Error})
		}
		return newFormatter().AutoDetail(raw, pairs)
	},
}

// waitForDeviceLogin polls the status until it leaves pending. A transport
// error is retried a few times before it is reported — a server restart in
// the middle of a sign-in resumes the flow, and the CLI should outlive it.
func waitForDeviceLogin(client *cli.Client, deviceID string, interval time.Duration, expires time.Time) (deviceLoginStatus, error) {
	failures := 0
	for {
		st, _, err := readDeviceLoginStatus(client, deviceID)
		if err != nil {
			failures++
			if failures > 5 {
				return deviceLoginStatus{}, err
			}
		} else {
			failures = 0
			if st.Status != "pending" {
				return st, nil
			}
		}
		remaining := time.Until(expires)
		if remaining <= 0 {
			return deviceLoginStatus{Status: "expired"}, nil
		}
		fmt.Fprint(os.Stderr, ".")
		time.Sleep(min(interval, remaining))
	}
}

func readDeviceLoginStatus(client *cli.Client, deviceID string) (deviceLoginStatus, map[string]any, error) {
	resp, err := client.Get("/api/v1/provider-logins/device/" + deviceID)
	if err != nil {
		return deviceLoginStatus{}, nil, err
	}
	if err := cli.CheckError(resp); err != nil {
		return deviceLoginStatus{}, nil, err
	}
	var raw map[string]any
	if err := cli.ReadJSON(resp, &raw); err != nil {
		return deviceLoginStatus{}, nil, err
	}
	var st deviceLoginStatus
	if s, ok := raw["status"].(string); ok {
		st.Status = s
	}
	if s, ok := raw["credential_id"].(string); ok && s != "" {
		st.CredentialID = &s
	}
	if s, ok := raw["error"].(string); ok && s != "" {
		st.Error = &s
	}
	return st, raw, nil
}

// printCredentialDetail fetches the created credential and prints it the way
// `credential get` does, so the two commands agree on what a credential
// looks like.
func printCredentialDetail(client *cli.Client, f *cli.Formatter, credentialID string) error {
	resp, err := client.Get("/api/v1/credentials/" + credentialID)
	if err != nil {
		return err
	}
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	var cred struct {
		ID           string  `json:"id"`
		Name         string  `json:"name"`
		Type         string  `json:"type"`
		Provider     string  `json:"provider"`
		Status       string  `json:"status"`
		Scope        string  `json:"scope"`
		AccountEmail *string `json:"account_email"`
		CreatedAt    string  `json:"created_at"`
	}
	if err := cli.ReadJSON(resp, &cred); err != nil {
		return err
	}
	pairs := [][]string{
		{"ID", cred.ID},
		{"Name", cred.Name},
		{"Type", cred.Type},
		{"Provider", cred.Provider},
		{"Status", cred.Status},
		{"Scope", cred.Scope},
	}
	if cred.AccountEmail != nil && *cred.AccountEmail != "" {
		pairs = append(pairs, []string{"Account", *cred.AccountEmail})
	}
	pairs = append(pairs, []string{"Created", cred.CreatedAt})
	return f.AutoDetail(cred, pairs)
}

func init() {
	credLoginCmd.Flags().String("provider", "", "Provider to sign in to (OPENAI)")
	credLoginCmd.Flags().String("mode", "", "Login mode; only `subscription` (the default) is offered by device code")
	credLoginCmd.Flags().Bool("no-wait", false, "Print the code and device id and return; check later with `credential login-status`")
	credentialCmd.AddCommand(credLoginCmd)
	credentialCmd.AddCommand(credLoginStatusCmd)
}
