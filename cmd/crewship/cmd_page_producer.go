package main

// `crewship page authorize-producer <page> <panel>` — the page owner's explicit
// authorization of a panel's agent producer (#5260, R71 §0.14/1).
//
//	POST /api/v1/pages/{slug}/panels/{panelId}/producer/authorize

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

type pageProducerAuthorizationJSON struct {
	Page       string `json:"page" yaml:"page"`
	Panel      string `json:"panel" yaml:"panel"`
	Producer   string `json:"producer" yaml:"producer"`
	Authorized bool   `json:"authorized" yaml:"authorized"`
	Changed    bool   `json:"changed" yaml:"changed"`
}

var pageAuthorizeProducerCmd = &cobra.Command{
	Use:   "authorize-producer <page> <panel>",
	Short: "As the page's owner, authorize the agent a panel names as its producer",
	Long: `Authorize the agent a panel names as its producer, so it may push that
panel's data. Only the page's owner can do this (the owning user, or a member
of the owning crew while the crew is not deleted), and only a person: a
workspace admin, an Edit grantee or an agent is refused with exit 4. A
private artifact delegates nothing to agents and answers exit 5.

A panel whose agent producer the owner did not name reads "Producer not
authorized by the owner" and takes no pushes from that agent. That happens
when someone else named the agent, when the page changed owner, or when the
upgrade could not tell who named it. This command is the owner's fix. It
authorizes the producer the spec already declares and nothing else, it is
audited, and it lasts only while you own the page.

  crewship page authorize-producer fleet-201 sluzby`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := pageClient()
		if err != nil {
			return err
		}
		resp, err := client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/panels/"+pagePathEscape(args[1])+"/producer/authorize", struct{}{})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := pageCheckError(resp); err != nil {
			return err
		}
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("read response: %w", err)
		}
		f := newFormatter()
		switch f.Format {
		case "json", "yaml", "ndjson":
			return pageEmitMachine(f, raw, "{}")
		}
		var out pageProducerAuthorizationJSON
		if err := json.Unmarshal(raw, &out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if !out.Changed {
			fmt.Printf("%s already pushes %s on %s with your authorization.\n", out.Producer, out.Panel, out.Page)
			return nil
		}
		fmt.Printf("Authorized %s as the producer of %s on %s.\n", out.Producer, out.Panel, out.Page)
		return nil
	},
}

func init() {
	pageCmd.AddCommand(pageAuthorizeProducerCmd)
}
