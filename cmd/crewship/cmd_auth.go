package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/crewship-ai/crewship/internal/cli"
)

// authCmd groups self-service account commands. Login/logout/whoami stay
// top-level for muscle memory; this parent hosts the newer account
// mutations like password change (#867.1).
var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage your own account (password, ...)",
}

var authPasswdCmd = &cobra.Command{
	Use:   "passwd",
	Short: "Change your account password",
	Long: `Change your own account password.

Interactively, prompts (without echo) for the current password, then the
new password twice. For scripting, pipe two lines on stdin — the current
password on the first line and the new password on the second:

    printf '%s\n%s\n' "$OLD" "$NEW" | crewship auth passwd

Passwords are never passed as flags, so they don't leak into shell
history or process listings. The new password must be at least 8
characters. Changing your password revokes your OTHER active sessions;
the session you run this from stays signed in.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		var current, newPw string
		if term.IsTerminal(int(syscall.Stdin)) {
			// Interactive: read each secret without echo.
			pw, err := promptPassword("Current password: ")
			if err != nil {
				return err
			}
			current = pw
			np, err := promptPassword("New password: ")
			if err != nil {
				return err
			}
			confirm, err := promptPassword("Confirm new password: ")
			if err != nil {
				return err
			}
			if np != confirm {
				return fmt.Errorf("passwords do not match")
			}
			newPw = np
		} else {
			// Scripted: current on line 1, new on line 2 of stdin.
			reader := bufio.NewReader(os.Stdin)
			cur, err := readSecretLine(reader)
			if err != nil {
				return fmt.Errorf("read current password from stdin: %w", err)
			}
			np, err := readSecretLine(reader)
			if err != nil {
				return fmt.Errorf("read new password from stdin: %w", err)
			}
			current, newPw = cur, np
		}

		if len(newPw) < 8 {
			return fmt.Errorf("new password must be at least 8 characters")
		}

		client := newAPIClient()
		// User-scoped endpoint — no workspace context.
		client.WorkspaceID = ""
		resp, err := client.Post("/api/v1/users/me/password", map[string]string{
			"current_password": current,
			"new_password":     newPw,
		})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Password changed. Your other sessions have been signed out.")
		return nil
	},
}

// promptPassword reads a password from the TTY without echo.
func promptPassword(label string) (string, error) {
	fmt.Print(label)
	b, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Println()
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(b), nil
}

// readSecretLine reads one line and strips only the trailing newline
// (preserving any other whitespace that may be part of the password).
func readSecretLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

var authAvatarClear bool

var authAvatarCmd = &cobra.Command{
	Use:   "avatar [image-path]",
	Short: "Upload or clear your profile picture",
	Long: `Upload your own avatar (PNG, JPEG, or WebP; max 2MB) or clear it back
to initials.

    crewship auth avatar ./me.png     # upload/replace
    crewship auth avatar --clear      # remove, back to initials

The image is served from an authenticated endpoint; other members see it
on the roster and in chat.`,
	Args: func(cmd *cobra.Command, args []string) error {
		if authAvatarClear {
			return cobra.NoArgs(cmd, args)
		}
		return cobra.ExactArgs(1)(cmd, args)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		client := newAPIClient()
		// User-scoped endpoint — no workspace context.
		client.WorkspaceID = ""

		if authAvatarClear {
			resp, err := client.Delete("/api/v1/users/me/avatar")
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if err := cli.CheckError(resp); err != nil {
				return err
			}
			cli.PrintSuccess("Avatar cleared — back to initials.")
			return nil
		}

		localPath := args[0]
		fh, err := os.Open(localPath)
		if err != nil {
			return fmt.Errorf("open %s: %w", localPath, err)
		}
		defer fh.Close()

		// 2MB server cap → assemble in memory (streaming buys nothing here).
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, err := mw.CreateFormFile("file", filepath.Base(localPath))
		if err != nil {
			return fmt.Errorf("multipart form: %w", err)
		}
		if _, err := io.Copy(fw, fh); err != nil {
			return fmt.Errorf("multipart copy: %w", err)
		}
		if err := mw.Close(); err != nil {
			return fmt.Errorf("multipart close: %w", err)
		}

		resp, err := postMultipart(cmd.Context(), client, "/api/v1/users/me/avatar", mw.FormDataContentType(), &buf)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		cli.PrintSuccess("Avatar updated.")
		return nil
	},
}

// authPairResult is the --format json/yaml/ndjson payload for `auth pair`.
// Status is reported verbatim (pending/consumed/expired) rather than a bare
// bool so a script that times out can tell "still pending" from "the code
// died" without re-parsing the human error text.
type authPairResult struct {
	Code        string `json:"code"`
	ExpiresAt   string `json:"expires_at"`
	Status      string `json:"status"`
	AdapterHint string `json:"adapter_hint,omitempty"`
	Paired      bool   `json:"paired"`
	Waited      bool   `json:"waited"`
}

var authPairCmd = &cobra.Command{
	Use:   "pair",
	Short: "Issue a device-code so another crewship CLI can log in without a browser",
	Long: `Issue a device-code pairing code and, by default, wait for it to be redeemed.

This is the other half of ` + "`crewship login --pair --code=…`" + `: that command
REDEEMS a code, this one ISSUES it. Run this on an already-authenticated CLI,
paste the printed snippet on the second machine, and — unless --no-wait —
the command blocks until that machine redeems the code or the 10-minute
code TTL runs out.

    crewship auth pair
    crewship auth pair --no-wait
    crewship auth pair --adapter CLAUDE_CODE
    crewship auth pair --timeout 5m --poll-interval 3s

The code IS the credential for the redeeming side — POST
/api/v1/auth/pair/redeem is intentionally unauthenticated, so anyone who has
the code can log in as you. Treat it like a password: read it over a channel
you trust, and let an unused code expire rather than posting it anywhere
public.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		timeout, _ := cmd.Flags().GetDuration("timeout")
		interval, _ := cmd.Flags().GetDuration("poll-interval")
		if interval <= 0 {
			return cli.WithExitCode(fmt.Errorf("--poll-interval must be positive"), cli.ExitValidation)
		}
		if timeout <= 0 {
			return cli.WithExitCode(fmt.Errorf("--timeout must be positive"), cli.ExitValidation)
		}
		noWait, _ := cmd.Flags().GetBool("no-wait")
		adapterHint, _ := cmd.Flags().GetString("adapter")

		client := newAPIClient()
		// Self-scoped account action — no workspace context, same tier as
		// `auth passwd` / `auth avatar`.
		client.WorkspaceID = ""

		payload := map[string]string{}
		if adapterHint != "" {
			payload["adapter_hint"] = adapterHint
		}
		resp, err := client.Post("/api/v1/auth/pair/start", payload)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var started struct {
			Code      string `json:"code"`
			ExpiresAt string `json:"expires_at"`
		}
		if err := cli.ReadJSON(resp, &started); err != nil {
			return err
		}

		result := authPairResult{
			Code:        started.Code,
			ExpiresAt:   started.ExpiresAt,
			Status:      "pending",
			AdapterHint: adapterHint,
		}

		f := newFormatter()
		// Ask the formatter rather than restating its rule (see `oauth
		// connect`, cmd_oauth.go): classifying quiet as machine-readable
		// would suppress the code, without which the flow cannot be
		// completed on the other machine at all.
		human := f.RoutesToHuman()

		if human {
			fmt.Println("Pairing code:")
			fmt.Println()
			fmt.Printf("  %s%s%s\n", cli.Bold, result.Code, cli.Reset)
			fmt.Println()
			fmt.Println("On the OTHER machine, run:")
			fmt.Println()
			snippet := "  crewship login --pair --code=" + result.Code
			if flagServer != "" {
				snippet += " --server " + flagServer
			}
			fmt.Println(snippet)
			fmt.Println()
			fmt.Printf("Expires: %s\n", result.ExpiresAt)
		}

		if noWait {
			return f.AutoHuman(result, func() {
				fmt.Println()
				fmt.Println("Not waiting for redemption (--no-wait).")
			})
		}

		result.Waited = true
		if human {
			fmt.Println()
			fmt.Printf("Waiting up to %s for the code to be redeemed…\n", timeout)
		}

		status, hint, err := waitForPairRedemption(client, result.Code, timeout, interval)
		result.Status = status
		result.Paired = status == "consumed"
		if hint != "" {
			result.AdapterHint = hint
		}
		if err != nil {
			// Emit the machine envelope before failing so a --format json
			// consumer still gets the status the code was stuck in.
			if !human {
				_ = f.Machine(result)
			}
			return err
		}

		return f.AutoHuman(result, func() {
			cli.PrintSuccess(fmt.Sprintf("Paired — code %s was redeemed.", result.Code))
		})
	},
}

// fetchPairStatus polls one pairing code's status.
func fetchPairStatus(client *cli.Client, code string) (status, adapterHint string, err error) {
	resp, err := client.Get("/api/v1/auth/pair/poll?code=" + url.QueryEscape(code))
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return "", "", err
	}
	var poll struct {
		Status      string `json:"status"`
		AdapterHint string `json:"adapter_hint"`
	}
	if err := cli.ReadJSON(resp, &poll); err != nil {
		return "", "", err
	}
	return poll.Status, poll.AdapterHint, nil
}

// waitForPairRedemption polls until the code is consumed, expires, or the
// deadline passes. Shaped after waitForCredentialActive (cmd_oauth.go),
// reusing its nextPollDelay/isFatalPollError helpers: the last-seen status
// travels alongside any error, because "we gave up" and "the code expired"
// are different operator problems and the status is the only thing that
// tells them apart.
func waitForPairRedemption(client *cli.Client, code string, timeout, interval time.Duration) (status, adapterHint string, err error) {
	deadline := time.Now().Add(timeout)
	last := "pending"
	var lastErr error
	for {
		st, hint, ferr := fetchPairStatus(client, code)
		switch {
		case ferr == nil:
			lastErr = nil
			last = st
			if hint != "" {
				adapterHint = hint
			}
			switch st {
			case "consumed":
				return st, adapterHint, nil
			case "expired":
				// Terminal, not transient: a code that flipped to expired
				// will never flip back, so grinding through the rest of the
				// timeout budget polling it helps nobody.
				return st, adapterHint, cli.WithExitCode(
					fmt.Errorf("pairing code %s expired before it was redeemed — codes are valid for 10 minutes; "+
						"run `crewship auth pair` again for a fresh one", code),
					cli.ExitGeneric)
			}
		case isFatalPollError(ferr):
			return last, adapterHint, ferr
		default:
			lastErr = ferr
		}

		delay, done := nextPollDelay(time.Now(), deadline, interval)
		if done {
			if lastErr != nil {
				return last, adapterHint, fmt.Errorf(
					"gave up after %s: the pairing code's status could not be read: %w", timeout, lastErr)
			}
			return last, adapterHint, cli.WithExitCode(
				fmt.Errorf("timed out after %s waiting for pairing code %s to be redeemed; it is still %s",
					timeout, code, last),
				cli.ExitGeneric)
		}
		time.Sleep(delay)
	}
}

// authProfileFullName backs --full-name on authProfileCmd.
var authProfileFullName string

// authProfileCmd is the CLI for PATCH /api/v1/users/me — the one
// self-service profile mutation that shipped with no command (#2147).
// Password (authPasswdCmd) and avatar (authAvatarCmd) already had theirs;
// this is the last of the three.
var authProfileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Update your display name",
	Long: `Update your own profile.

    crewship auth profile --full-name "Jane Doe"

Only your full name is editable this way — the API accepts no other
field on this endpoint. Email changes require a re-verification flow
that does not exist yet, so there is no --email flag here to promise
something the server would silently ignore.

Prints the updated profile and honors the global --format flag (table
by default, or json/yaml/ndjson for scripts).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		name := strings.TrimSpace(authProfileFullName)
		if name == "" {
			return cli.WithExitCode(fmt.Errorf("--full-name is required"), cli.ExitValidation)
		}

		client := newAPIClient()
		// User-scoped endpoint — no workspace context.
		client.WorkspaceID = ""
		resp, err := client.Patch("/api/v1/users/me", map[string]string{"full_name": name})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var profile struct {
			ID        string  `json:"id"`
			Email     string  `json:"email"`
			FullName  *string `json:"full_name"`
			AvatarURL *string `json:"avatar_url"`
		}
		if err := cli.ReadJSON(resp, &profile); err != nil {
			return err
		}

		displayName := ""
		if profile.FullName != nil {
			displayName = *profile.FullName
		}
		pairs := [][]string{
			{"ID", profile.ID},
			{"Email", profile.Email},
			{"Full name", displayName},
		}
		if profile.AvatarURL != nil && *profile.AvatarURL != "" {
			pairs = append(pairs, []string{"Avatar URL", *profile.AvatarURL})
		}
		return resolvedFormatter(cmd).AutoDetail(profile, pairs)
	},
}

func init() {
	authCmd.AddCommand(authPasswdCmd)
	authAvatarCmd.Flags().BoolVar(&authAvatarClear, "clear", false, "remove your avatar (back to initials)")
	authCmd.AddCommand(authAvatarCmd)

	authProfileCmd.Flags().StringVar(&authProfileFullName, "full-name", "", "your new display name (1-100 characters)")
	authCmd.AddCommand(authProfileCmd)

	authPairCmd.Flags().String("adapter", "", "Optional adapter hint (telemetry): CLAUDE_CODE | GEMINI_CLI | CODEX_CLI | OPENCODE | CURSOR_CLI | FACTORY_DROID")
	authPairCmd.Flags().Bool("no-wait", false, "Print the code and exit immediately, without waiting for it to be redeemed")
	authPairCmd.Flags().Duration("timeout", 9*time.Minute, "How long to wait for the code to be redeemed (codes expire after 10 minutes server-side)")
	authPairCmd.Flags().Duration("poll-interval", 2*time.Second, "How often to poll for redemption while waiting")
	authCmd.AddCommand(authPairCmd)
}
