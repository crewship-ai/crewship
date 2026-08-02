package main

import (
	"fmt"
	"net/url"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// #1669 — CLI parity for /api/v1/users/me/user-model.
//
// The operator model is the profile agents read about you at the start of
// every session. Until this shipped, the only control over it was
// `privacy peer-consent set on`, which turns the whole feature off — a
// blunt answer to "that one entry is wrong".

var privacyUserModelCmd = &cobra.Command{
	Use:     "user-model",
	Aliases: []string{"operator-model", "about-me"},
	Short:   "See or correct the operator model agents read about you",
	Long: `The operator model is a short profile of how you work that every agent in
your crew reads at the start of a session: your role, what you own, and
the working preferences and constraints you have stated.

It records only what you actually said, never what the system concluded
about you. If an entry is wrong, forget that one field — you do not have
to turn the whole feature off to correct it.`,
}

type userModelFact struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type userModelResponse struct {
	UserID    string          `json:"user_id"`
	Exists    bool            `json:"exists"`
	UserSlug  string          `json:"user_slug"`
	Bytes     int             `json:"bytes"`
	UpdatedAt string          `json:"updated_at"`
	Content   string          `json:"content"`
	Facts     []userModelFact `json:"facts"`
	Purged    int             `json:"purged"`
	Forgot    string          `json:"forgot"`
	Remaining []userModelFact `json:"remaining"`
}

var privacyUserModelListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls", "show", "get"},
	Short:   "Show every fact stored about you in this workspace",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		resp, err := client.Get("/api/v1/users/me/user-model")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out userModelResponse
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		rows := make([][]string, 0, len(out.Facts))
		for _, f := range out.Facts {
			rows = append(rows, []string{f.Key, f.Value})
		}
		// An empty table is a real answer here — nothing has been recorded
		// yet — and `exists` in the JSON body distinguishes it from a
		// model that exists with no parseable bullets. Nothing is printed
		// alongside the table on purpose: a stray line before Auto would
		// corrupt `-f json`, and this command's whole job is to be a
		// faithful readout.
		return newFormatter().Auto(out, []string{"FIELD", "VALUE"}, rows)
	},
}

var privacyUserModelForgetCmd = &cobra.Command{
	Use:   "forget <field>",
	Short: "Forget one field (e.g. 'timezone') and keep the rest",
	Long: `Remove a single field from the operator model. Use 'privacy user-model list'
to see the field names.

This is the answer to "an agent recorded something wrong about me": the
rest of the profile stays, and the field may be recorded again if you
state it later. To stop new facts being recorded at all, use
'privacy peer-consent set on'.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		resp, err := client.Delete("/api/v1/users/me/user-model/facts/" + url.PathEscape(args[0]))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out userModelResponse
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Forgot %q. %d field(s) still stored.", out.Forgot, len(out.Remaining)))
		return nil
	},
}

var privacyUserModelDeleteCmd = &cobra.Command{
	Use:     "delete",
	Aliases: []string{"purge", "rm"},
	Short:   "Forget the whole operator model (does not opt you out)",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		if err := confirmAction(cmd, "Forget everything stored about you in this workspace? Agents may record new facts you state later unless you also opt out."); err != nil {
			return err
		}
		resp, err := client.Delete("/api/v1/users/me/user-model")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out userModelResponse
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Deleted %d operator model(s).", out.Purged))
		return nil
	},
}

func init() {
	privacyUserModelDeleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")
	privacyUserModelCmd.AddCommand(
		privacyUserModelListCmd,
		privacyUserModelForgetCmd,
		privacyUserModelDeleteCmd,
	)
	privacyCmd.AddCommand(privacyUserModelCmd)
}
