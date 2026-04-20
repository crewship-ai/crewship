package main

import (
	"fmt"
	"net/url"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// hooksCmd is the CLI surface for the lifecycle-hook system: registered
// callbacks that fire on journal events, assignment transitions, and
// similar platform lifecycle moments. Live against:
//
//	GET  /api/v1/hooks[?crew_id=…]
//	POST /api/v1/hooks/{id}/enable
//	POST /api/v1/hooks/{id}/disable
//
// `register` is deliberately absent — hook registration stays a
// config-time operation (loaded from workspace config); there is no
// runtime create/delete endpoint by design.
var hooksCmd = &cobra.Command{
	Use:   "hooks",
	Short: "Lifecycle hooks registry (list/enable/disable)",
	Long: `Manage the lifecycle-hook registry — scripts or webhooks that fire on
platform events (assignment.completed, journal.entry, keeper.decision, …).

Examples:
  crewship hooks list
  crewship hooks list --crew backend-team
  crewship hooks enable hk_abc
  crewship hooks disable hk_abc

Subcommand status:
  list     — live (GET /api/v1/hooks)
  enable   — live (POST /api/v1/hooks/{id}/enable)
  disable  — live (POST /api/v1/hooks/{id}/disable)`,
}

var hooksListCmd = &cobra.Command{
	Use:   "list",
	Short: "List registered lifecycle hooks",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()

		crew, _ := cmd.Flags().GetString("crew")
		q := url.Values{}
		if crew != "" {
			q.Set("crew_id", crew)
		}
		path := "/api/v1/hooks"
		if enc := q.Encode(); enc != "" {
			path += "?" + enc
		}

		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var body struct {
			Rows []struct {
				ID           string `json:"id"`
				CrewID       string `json:"crew_id"`
				Event        string `json:"event"`
				HandlerKind  string `json:"handler_kind"`
				Target       string `json:"target"`
				Enabled      bool   `json:"enabled"`
				AllowedShell bool   `json:"allowed_shell"`
				CreatedAt    string `json:"created_at"`
			} `json:"rows"`
			Count int `json:"count"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}

		f := newFormatter()
		if f.Format == "json" {
			return f.JSON(body.Rows)
		}
		if f.Format == "yaml" {
			return f.YAML(body.Rows)
		}

		if len(body.Rows) == 0 {
			fmt.Println("(no hooks registered)")
			return nil
		}
		header := []string{"ID", "EVENT", "HANDLER", "TARGET", "ENABLED", "CREATED"}
		rows := make([][]string, 0, len(body.Rows))
		for _, r := range body.Rows {
			enabled := "no"
			if r.Enabled {
				enabled = "yes"
			}
			target := r.Target
			if len(target) > 40 {
				target = target[:37] + "…"
			}
			rows = append(rows, []string{r.ID, r.Event, r.HandlerKind, target, enabled, r.CreatedAt})
		}
		f.Table(header, rows)
		return nil
	},
}

var hooksEnableCmd = &cobra.Command{
	Use:   "enable <id>",
	Short: "Enable a registered hook",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return hooksToggle(args[0], true)
	},
}

var hooksDisableCmd = &cobra.Command{
	Use:   "disable <id>",
	Short: "Disable a registered hook",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return hooksToggle(args[0], false)
	},
}

// hooksToggle drives the enable/disable subcommands through a single
// code path — the two endpoints differ only in URL suffix, the body
// is empty and the response shape is the same on both. Keeping the
// RunE bodies thin makes the test plan simpler (one path, two URLs).
func hooksToggle(id string, enable bool) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	client := newAPIClient()

	verb := "disable"
	if enable {
		verb = "enable"
	}
	path := fmt.Sprintf("/api/v1/hooks/%s/%s", url.PathEscape(id), verb)
	resp, err := client.Post(path, nil)
	if err != nil {
		return err
	}
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	fmt.Printf("Hook %s: %sd\n", id, verb)
	return nil
}

func init() {
	hooksListCmd.Flags().String("crew", "", "Filter by crew slug or ID")
	hooksCmd.AddCommand(hooksListCmd)
	hooksCmd.AddCommand(hooksEnableCmd)
	hooksCmd.AddCommand(hooksDisableCmd)
}
