package main

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

// `crewship routine checkup <slug>` mirrors GET .../pipelines/{slug}/checkup
// (ODCH-RT-06): the same checklist the routine page's Checkup card shows,
// computed by the server (agents, credentials, cost cap, failures in a row,
// the schedule circuit breaker, untested changes). `routine doctor` stays the
// preflight that also looks at the crew's container and the egress list.

type routineCheckupCheck struct {
	Key    string `json:"key" yaml:"key"`
	Level  string `json:"level" yaml:"level"`
	Title  string `json:"title" yaml:"title"`
	Detail string `json:"detail" yaml:"detail"`
	Tab    string `json:"tab,omitempty" yaml:"tab,omitempty"`
}

type routineCheckupReport struct {
	Slug      string                `json:"slug" yaml:"slug"`
	CheckedAt string                `json:"checked_at" yaml:"checked_at"`
	Passed    int                   `json:"passed" yaml:"passed"`
	Warned    int                   `json:"warned" yaml:"warned"`
	Failed    int                   `json:"failed" yaml:"failed"`
	Total     int                   `json:"total" yaml:"total"`
	Checks    []routineCheckupCheck `json:"checks" yaml:"checks"`
}

var routineCheckupCmd = &cobra.Command{
	Use:   "checkup <slug>",
	Short: "The server's checklist for a routine: agents, credentials, cost cap, failures, breaker, untested changes",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var rep routineCheckupReport
		if err := getJSON(client, fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/checkup", client.GetWorkspaceID(), url.PathEscape(args[0])), &rep); err != nil {
			return err
		}
		if err := resolvedFormatter(cmd).AutoHuman(rep, func() {
			sym := map[string]string{"ok": "ok  ", "warn": "warn", "bad": "FIX "}
			fmt.Printf("Checkup: %s  (%d of %d passed)\n\n", rep.Slug, rep.Passed, rep.Total)
			for _, c := range rep.Checks {
				fmt.Printf("  %s  %s\n        %s\n", sym[c.Level], c.Title, c.Detail)
			}
		}); err != nil {
			return err
		}
		if rep.Failed > 0 {
			return fmt.Errorf("%d check(s) need fixing", rep.Failed)
		}
		return nil
	},
}

func init() { pipelineCmd.AddCommand(routineCheckupCmd) }
