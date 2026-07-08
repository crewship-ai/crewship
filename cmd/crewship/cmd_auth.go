package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"syscall"

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

func init() {
	authCmd.AddCommand(authPasswdCmd)
	authAvatarCmd.Flags().BoolVar(&authAvatarClear, "clear", false, "remove your avatar (back to initials)")
	authCmd.AddCommand(authAvatarCmd)
}
