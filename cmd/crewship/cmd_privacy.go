package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// GDPR self-service for the peer-memory feature. Every route here acts
// on the caller's OWN data (the server reads user_id from the auth
// context, the path uses the literal "me"), so any authenticated
// workspace member can use it — no OWNER/ADMIN gate. They do need a
// workspace context because peer cards are workspace-scoped, hence
// requireAuthAndWorkspace rather than plain requireAuth.
var privacyCmd = &cobra.Command{
	Use:   "privacy",
	Short: "Manage your peer-memory privacy (consent + your peer cards)",
	Long: `Self-service controls for the peer-memory feature: decide whether crew
agents may keep "peer cards" about you, see every card stored about you
across the workspace, and delete them.

Every action is scoped to YOUR own data — no workspace-admin role is
required. Opting out purges existing cards immediately.`,
}

// ── peer-consent ─────────────────────────────────────────────────────

var privacyConsentCmd = &cobra.Command{
	Use:     "peer-consent",
	Aliases: []string{"consent"},
	Short:   "View or set your peer-card opt-out state",
}

type peerConsentResponse struct {
	UserID      string `json:"user_id"`
	WorkspaceID string `json:"workspace_id"`
	OptedOut    bool   `json:"opted_out"`
	OptedOutAt  string `json:"opted_out_at"`
	Purged      int    `json:"purged"`
}

var privacyConsentGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Show whether you've opted out of peer cards",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		resp, err := client.Get("/api/v1/users/me/peer-consent")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var c peerConsentResponse
		if err := cli.ReadJSON(resp, &c); err != nil {
			return err
		}
		state := "no — agents may keep peer cards about you"
		if c.OptedOut {
			state = "yes — agents may not keep peer cards about you"
		}
		pairs := [][]string{{"Opted out", state}}
		if c.OptedOutAt != "" {
			pairs = append(pairs, []string{"Opted out at", c.OptedOutAt})
		}
		return newFormatter().AutoDetail(c, pairs)
	},
}

var privacyConsentSetCmd = &cobra.Command{
	Use:   "set <on|off>",
	Short: "Opt out of (on) or back into (off) peer cards",
	Long: `'set on' opts you OUT of peer cards — every existing card about you in
this workspace is purged immediately. 'set off' opts back in; it does not
recreate anything, agents may simply extract new cards going forward.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		optedOut, err := parseOnOff(args[0])
		if err != nil {
			return err
		}
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		// Opting out is destructive (immediate purge) — gate it behind a
		// confirmation unless --yes. Opting back in creates nothing, so it
		// needs no confirmation.
		if optedOut {
			if err := confirmAction(cmd, "Opt out of peer cards? This immediately deletes every peer card about you in this workspace."); err != nil {
				return err
			}
		}
		resp, err := client.Put("/api/v1/users/me/peer-consent", map[string]bool{"opted_out": optedOut})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var c peerConsentResponse
		if err := cli.ReadJSON(resp, &c); err != nil {
			return err
		}
		if optedOut {
			cli.PrintSuccess(fmt.Sprintf("Opted out of peer cards. Purged %d existing card(s).", c.Purged))
		} else {
			cli.PrintSuccess("Opted back into peer cards.")
		}
		return nil
	},
}

// ── peer-cards ───────────────────────────────────────────────────────

var privacyCardsCmd = &cobra.Command{
	Use:     "peer-cards",
	Aliases: []string{"cards"},
	Short:   "List or delete peer cards stored about you",
}

type peerCard struct {
	ID        string `json:"id"`
	AgentID   string `json:"agent_id"`
	AgentSlug string `json:"agent_slug"`
	UserSlug  string `json:"user_slug"`
	Bytes     int    `json:"bytes"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	Content   string `json:"content,omitempty"`
}

type peerCardsResponse struct {
	UserID string     `json:"user_id"`
	Peers  []peerCard `json:"peers"`
	Purged int        `json:"purged"`
}

var privacyCardsListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List every peer card stored about you across the workspace",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		resp, err := client.Get("/api/v1/users/me/peer-cards")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out peerCardsResponse
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		rows := make([][]string, 0, len(out.Peers))
		for _, p := range out.Peers {
			rows = append(rows, []string{
				truncateID(p.ID, 12),
				p.AgentSlug,
				fmt.Sprintf("%d", p.Bytes),
				p.UpdatedAt,
			})
		}
		// Full card content is returned by the API but omitted from the
		// table to keep it scannable; `-f json` surfaces it for a SAR.
		return newFormatter().Auto(out, []string{"ID", "AGENT", "BYTES", "UPDATED"}, rows)
	},
}

var privacyCardsDeleteCmd = &cobra.Command{
	Use:     "delete",
	Aliases: []string{"purge", "rm"},
	Short:   "Delete every peer card stored about you (does not opt you out)",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		if err := confirmAction(cmd, "Delete every peer card about you in this workspace? Agents may re-extract new cards later unless you also opt out."); err != nil {
			return err
		}
		resp, err := client.Delete("/api/v1/users/me/peer-cards")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out peerCardsResponse
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Deleted %d peer card(s).", out.Purged))
		return nil
	},
}

// parseOnOff maps a permissive set of truthy/falsey words to the
// opted_out boolean, so `peer-consent set on|off|true|false|yes|no` all
// work. Anything else is a clear error rather than a silent default.
func parseOnOff(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "true", "yes", "y", "1", "out":
		return true, nil
	case "off", "false", "no", "n", "0", "in":
		return false, nil
	}
	if b, err := strconv.ParseBool(s); err == nil {
		return b, nil
	}
	return false, fmt.Errorf("expected 'on' or 'off' (also accepts true/false, yes/no); got %q", s)
}

func init() {
	privacyConsentSetCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")
	privacyCardsDeleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	privacyConsentCmd.AddCommand(privacyConsentGetCmd, privacyConsentSetCmd)
	privacyCardsCmd.AddCommand(privacyCardsListCmd, privacyCardsDeleteCmd)
	privacyCmd.AddCommand(privacyConsentCmd, privacyCardsCmd)
}
