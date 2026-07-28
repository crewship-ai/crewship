package main

// CLI parity for the credential reveal surface (PRD-CREDENTIALS-V2-2026 §2.6,
// project rule #3: every /api/v1 route gets a matching command).
//
// `credential reveal` is the only command in the CLI that cannot use the
// stored CLI token, and that is deliberate rather than an oversight. §2.6 L9
// makes the reveal endpoint unreachable from an API token, an internal or
// sidecar token, and any non-interactive path — a long-lived bearer sitting
// in ~/.crewship/cli-config.yaml is exactly the credential a compromised
// agent or a leaked CI secret would present, so the server refuses it.
//
// A command that always 403s would be worse than no command, so this one
// performs the ceremony the server is asking for: it requires a TTY, prompts
// for the account password, exchanges it for a real user session (the same
// flow `crewship login` runs), and calls the endpoint with that. An agent in
// a container has neither a terminal nor the operator's password, so the
// property L9 protects holds — while a human at a keyboard gets a command
// that actually works.
//
// The TTY requirement is enforced client-side too, before any network call.
// The server is the authority; this is the fast, honest error, and it keeps a
// piped password out of a CI job that would otherwise have talked itself into
// automating a reveal.
//
// The whole point of §2.6 L8 is that most reveals should not happen at all,
// so every failure path below points at `credential rotate` instead.

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// revealResult is the server's 200 body.
type revealResult struct {
	CredentialID   string `json:"credential_id"`
	Name           string `json:"name"`
	Type           string `json:"type"`
	Sensitivity    string `json:"sensitivity"`
	Value          string `json:"value"`
	RevealedAt     string `json:"revealed_at"`
	JournalEntryID string `json:"journal_entry_id"`
}

// revealTTYCheck is indirected so tests can drive both branches without a
// pseudo-terminal. Production always reads the real stdin.
var revealTTYCheck = func() bool { return term.IsTerminal(int(syscall.Stdin)) }

// revealPasswordPrompt is indirected for the same reason. It deliberately
// does NOT honour CREWSHIP_PASSWORD or --password-stdin the way `login`
// does: those exist so a systemd timer can mint a token unattended, which is
// precisely the caller shape that must never reach a reveal.
var revealPasswordPrompt = func() (string, error) {
	fmt.Fprint(os.Stderr, "Password: ")
	pw, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(pw), nil
}

var credRevealCmd = &cobra.Command{
	Use:   "reveal <name-or-id>",
	Short: "Show a stored credential's value (interactive sign-in required)",
	Long: `Disclose a credential's plaintext value.

Prefer rotating. Most reasons to reveal a secret are really reasons to put a
NEW secret somewhere, and 'crewship credential rotate' shows the new value
once while the old one drains through its grace window — no existing secret
is exposed. Reveal is the fallback for when you genuinely need the value that
is already in use.

Every reveal is refused unless ALL of these hold:

  • an OWNER has enabled reveal for the workspace (Settings → Access & Secrets,
    or 'crewship credential reveal-policy --enable')
  • your membership carries the 'credentials:reveal' capability — being an
    OWNER or ADMIN is necessary but NOT sufficient
  • the credential is not SEALED (SEALED can never be revealed, by anyone)
  • you pass a reason of at least 20 characters, which is recorded
  • the reveal can be written to the tamper-evident audit chain first

You will be asked for your password. The stored CLI token cannot reveal a
credential — agents, sidecars and CI jobs are locked out of this endpoint by
design, and a token in a config file is indistinguishable from one of those.

The value is written to stdout and nothing else is, so it can be redirected
or piped cleanly; the credential name, its classification and the id of the
journal entry that now records the disclosure go to stderr.

Examples:
  crewship credential reveal gh-token --reason "Migrating the deploy key to Vault, ticket OPS-812"
  crewship credential reveal deploy-key --reason "..." > deploy_key.pem`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		flags := cmd.Flags()
		reason, _ := flags.GetString("reason")
		reason = strings.TrimSpace(reason)
		if reason == "" {
			return fmt.Errorf("--reason is required and is recorded in the audit log; describe what you need the value for")
		}
		// Mirror the server's floor so the operator finds out before typing
		// their password, not after. The server remains the authority — it
		// also rejects generic reasons this check cannot judge.
		if len([]rune(reason)) < 20 {
			return fmt.Errorf("--reason must be at least 20 characters; it is what an auditor reads six months from now")
		}

		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		if !revealTTYCheck() {
			return fmt.Errorf("reveal requires an interactive terminal — the server refuses API tokens, agents and " +
				"non-interactive callers for this endpoint. Run 'crewship credential rotate' instead, or reveal from the dashboard")
		}

		// Resolve the id with the ordinary CLI token: listing credentials is
		// not the privileged part, and doing it first means a typo'd name
		// fails before the password prompt.
		client := newAPIClient()
		credID, err := resolveCredentialID(client, args[0])
		if err != nil {
			return err
		}

		sessionClient, err := revealInteractiveClient(cmd)
		if err != nil {
			return err
		}

		resp, err := sessionClient.Post("/api/v1/credentials/"+credID+"/reveal",
			map[string]string{"reason": reason})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			// The server's refusal messages name the specific layer that
			// denied (workspace switch, capability, SEALED, …), so pass them
			// through rather than flattening to "forbidden".
			return err
		}
		var out revealResult
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}

		// The value goes to stdout and NOTHING else does, so
		// `crewship credential reveal ... > key.pem` captures the secret
		// clean. Name, classification and the journal entry that now anchors
		// this disclosure go to stderr, where a human sees them and a
		// redirect does not.
		fmt.Fprintf(cmd.ErrOrStderr(),
			"%s%s%s (%s, %s) — recorded as journal entry %s\n",
			cli.Bold, out.Name, cli.Reset, out.Type, out.Sensitivity, out.JournalEntryID)
		fmt.Fprintln(cmd.OutOrStdout(), out.Value)
		return nil
	},
}

// revealInteractiveClient runs the password sign-in and returns a client
// bound to the resulting user session — the only credential shape the reveal
// endpoint accepts.
//
// The session it mints is a real one and shows up in Settings → Sessions,
// which is a feature: an operator reviewing sessions sees the sign-in that
// accompanied each reveal, next to the reveal itself in the journal.
func revealInteractiveClient(cmd *cobra.Command) (*cli.Client, error) {
	server := cli.EffectiveServer(flagServer, flagProfile, cliCfg)
	workspace := cli.ResolveWorkspace(flagWorkspace, cliCfg)

	email := revealCallerEmail(newAPIClient())
	reader := bufio.NewReader(os.Stdin)
	if email == "" {
		fmt.Fprint(os.Stderr, "Email: ")
		line, _ := reader.ReadString('\n')
		email = strings.TrimSpace(line)
	} else {
		fmt.Fprintf(os.Stderr, "Confirming your identity as %s to reveal a credential.\n", email)
	}
	if email == "" {
		return nil, fmt.Errorf("email is required")
	}

	password, err := revealPasswordPrompt()
	if err != nil {
		return nil, err
	}
	if password == "" {
		return nil, fmt.Errorf("password is required")
	}

	sessionToken, err := exchangeCredentialsForSession(cli.NewClient(server, "", ""), server, email, password)
	if err != nil {
		return nil, err
	}
	sc := cli.NewClient(server, sessionToken, workspace)
	sc.Verbose = flagVerbose
	return sc, nil
}

// revealCallerEmail asks the server who the stored token belongs to, so the
// operator confirms an identity rather than retyping one. Best-effort: an
// empty result just means we prompt for the email too.
func revealCallerEmail(client *cli.Client) string {
	var info struct {
		UserEmail string `json:"user_email"`
	}
	if err := getJSON(client, "/api/v1/auth/cli-token/validate", &info); err != nil {
		return ""
	}
	return info.UserEmail
}

// ---------------------------------------------------------------------------
// Workspace reveal policy (L1)
// ---------------------------------------------------------------------------

var credRevealPolicyCmd = &cobra.Command{
	Use:   "reveal-policy",
	Short: "Show or set the workspace credential-reveal switch",
	Long: `Reveal is OFF for every workspace until an OWNER turns it on. A newly
created workspace therefore has no reveal surface at all — that is a decision
someone makes, not a state they inherit.

Without flags this prints the current setting (OWNER, ADMIN and MANAGER can
read it; MEMBER and VIEWER cannot). --enable / --disable require OWNER, and
the change is written to the tamper-evident audit chain before it takes
effect.

Examples:
  crewship credential reveal-policy
  crewship credential reveal-policy --enable
  crewship credential reveal-policy --disable`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		flags := cmd.Flags()
		enable, _ := flags.GetBool("enable")
		disable, _ := flags.GetBool("disable")
		if enable && disable {
			return fmt.Errorf("--enable and --disable are mutually exclusive")
		}

		var out struct {
			WorkspaceID string `json:"workspace_id"`
			Enabled     bool   `json:"enabled"`
		}

		if !enable && !disable {
			if err := getJSON(client, "/api/v1/credentials/reveal-policy", &out); err != nil {
				return err
			}
			state := "disabled"
			if out.Enabled {
				state = "enabled"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Credential reveal is %s for this workspace.\n", state)
			if !out.Enabled {
				fmt.Fprintln(cmd.OutOrStdout(),
					"An OWNER can turn it on with: crewship credential reveal-policy --enable")
			}
			return nil
		}

		resp, err := client.Put("/api/v1/credentials/reveal-policy", map[string]bool{"enabled": enable})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		if out.Enabled {
			cli.PrintSuccess("Credential reveal ENABLED for this workspace. " +
				"Individual people still need the 'credentials:reveal' capability — role alone does not grant it.")
		} else {
			cli.PrintSuccess("Credential reveal DISABLED for this workspace.")
		}
		return nil
	},
}

// ---------------------------------------------------------------------------
// Classification (L0)
// ---------------------------------------------------------------------------

var credSensitivityCmd = &cobra.Command{
	Use:   "sensitivity <name-or-id> <" + sensitivityChoices + ">",
	Short: "Set a credential's classification",
	Long: `Classify a credential.

  STANDARD    dev tokens, read-only keys — revealable with the full ceremony
  RESTRICTED  production API keys, deploy keys
  SEALED      production databases, root credentials, anything an agent made —
              NEVER revealable, by any role, with no escape hatch

Raising a classification is a MANAGER+ action and takes effect immediately:
it only ever removes reach, so it needs no ceremony. Lowering one requires
OWNER or ADMIN and is written to the tamper-evident audit chain first — it
hands out a key that did not exist a moment earlier.

To get at a SEALED credential's value, rotate it: 'crewship credential rotate'
mints a new value and shows it once.

Examples:
  crewship credential sensitivity prod-db SEALED
  crewship credential sensitivity gh-token RESTRICTED`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		want := strings.ToUpper(strings.TrimSpace(args[1]))
		if !validSensitivityChoice(want) {
			return fmt.Errorf("sensitivity must be one of %s (got %q)", sensitivityChoices, args[1])
		}
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		credID, err := resolveCredentialID(client, args[0])
		if err != nil {
			return err
		}
		resp, err := client.Put("/api/v1/credentials/"+credID+"/sensitivity",
			map[string]string{"sensitivity": want})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			CredentialID string `json:"credential_id"`
			Sensitivity  string `json:"sensitivity"`
			Previous     string `json:"previous"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		if out.Sensitivity == out.Previous {
			fmt.Fprintf(cmd.OutOrStdout(), "Already %s — nothing changed.\n", out.Sensitivity)
			return nil
		}
		cli.PrintSuccess(fmt.Sprintf("Classification %s → %s", out.Previous, out.Sensitivity))
		if out.Sensitivity == api.SensitivitySealed {
			fmt.Fprintln(cmd.OutOrStdout(),
				"This credential can no longer be revealed by anyone. Rotate it if you need a usable value.")
		}
		return nil
	},
}

// sensitivityChoices renders the closed set for help text and error
// messages. Built from the server's vocabulary so the CLI cannot drift into
// offering a class the column would reject.
var sensitivityChoices = strings.Join(api.AllSensitivities(), "|")

func validSensitivityChoice(s string) bool {
	for _, c := range api.AllSensitivities() {
		if c == s {
			return true
		}
	}
	return false
}

func init() {
	credRevealCmd.Flags().String("reason", "",
		"Why you need the value (required, min 20 chars, recorded in the audit log)")

	credRevealPolicyCmd.Flags().Bool("enable", false, "Turn reveal ON for this workspace (OWNER only)")
	credRevealPolicyCmd.Flags().Bool("disable", false, "Turn reveal OFF for this workspace (OWNER only)")

	credentialCmd.AddCommand(credRevealCmd)
	credentialCmd.AddCommand(credRevealPolicyCmd)
	credentialCmd.AddCommand(credSensitivityCmd)
}
