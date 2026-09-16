package main

import (
	"fmt"
	"net/url"
	"strings"

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
	Key   string `json:"key" yaml:"key"`
	Value string `json:"value" yaml:"value"`
	// Provenance is where the fact came from (#1693): absent for a fact
	// recorded before the server kept evidence.
	Provenance *userModelProvenance `json:"provenance,omitempty" yaml:"provenance,omitempty"`
}

type userModelProvenance struct {
	Quote      string `json:"quote" yaml:"quote"`
	MessageID  string `json:"message_id" yaml:"message_id"`
	SourceType string `json:"source_type" yaml:"source_type"`
	At         string `json:"at" yaml:"at"`
}

type userModelResponse struct {
	UserID    string          `json:"user_id" yaml:"user_id"`
	Exists    bool            `json:"exists" yaml:"exists"`
	UserSlug  string          `json:"user_slug" yaml:"user_slug"`
	Bytes     int             `json:"bytes" yaml:"bytes"`
	UpdatedAt string          `json:"updated_at" yaml:"updated_at"`
	Content   string          `json:"content" yaml:"content"`
	Facts     []userModelFact `json:"facts" yaml:"facts"`
	Purged    int             `json:"purged" yaml:"purged"`
	Forgot    string          `json:"forgot" yaml:"forgot"`
	Remaining []userModelFact `json:"remaining" yaml:"remaining"`
}

var privacyUserModelListCmd = &cobra.Command{
	Use:     "list [field]",
	Aliases: []string{"ls", "show", "get"},
	Short:   "Show every fact stored about you in this workspace",
	Long: `Show the operator model stored about you in this workspace, one row per
field. Name a field to show just that one ('show timezone').

--provenance adds where each entry came from: the exact words of yours it
was verified against, when it was recorded, and the message it was found
in — the answer to "when did I say that?" before deciding whether to
forget it. An entry recorded before provenance was kept shows '-'.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		withProvenance, _ := cmd.Flags().GetBool("provenance")
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
		if len(args) == 1 {
			// A field that is not stored is a miss, the same answer
			// `forget` gives — reporting an empty table for a field the
			// person named would read as "stored, but blank".
			field := strings.ToLower(strings.TrimSpace(args[0]))
			kept := out.Facts[:0:0]
			for _, f := range out.Facts {
				if f.Key == field {
					kept = append(kept, f)
				}
			}
			if len(kept) == 0 {
				return fmt.Errorf("no field %q is stored about you in this workspace", args[0])
			}
			out.Facts = kept
		}
		rows := make([][]string, 0, len(out.Facts))
		for _, f := range out.Facts {
			if !withProvenance {
				rows = append(rows, []string{f.Key, f.Value})
				continue
			}
			rows = append(rows, userModelProvenanceRow(f))
		}
		headers := []string{"FIELD", "VALUE"}
		if withProvenance {
			headers = []string{"FIELD", "VALUE", "SOURCE", "QUOTE", "RECORDED", "MESSAGE"}
		}
		// An empty table is a real answer here — nothing has been recorded
		// yet — and `exists` in the JSON body distinguishes it from a
		// model that exists with no parseable bullets. Nothing is printed
		// alongside the table on purpose: a stray line before Auto would
		// corrupt `-f json`, and this command's whole job is to be a
		// faithful readout.
		return newFormatter().Auto(out, headers, rows)
	},
}

// userModelProvenanceRow renders one fact with its origin. A fact the
// server has no evidence row for — recorded before provenance was kept —
// gets '-' in every provenance column rather than an invented origin.
func userModelProvenanceRow(f userModelFact) []string {
	if f.Provenance == nil {
		return []string{f.Key, f.Value, "-", "-", "-", "-"}
	}
	p := f.Provenance
	msg := p.MessageID
	if msg == "" {
		msg = "-"
	}
	return []string{f.Key, f.Value, p.SourceType, p.Quote, p.At, msg}
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
	privacyUserModelListCmd.Flags().Bool("provenance", false, "Show where each entry came from: the quote, when it was recorded and the message it was found in")
	privacyUserModelDeleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")
	privacyUserModelCmd.AddCommand(
		privacyUserModelListCmd,
		privacyUserModelForgetCmd,
		privacyUserModelDeleteCmd,
	)
	privacyCmd.AddCommand(privacyUserModelCmd)
}
