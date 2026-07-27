//go:build !clionly

package main

// `crewship admin ratelimits ...` — inspect and tune the instance's rate
// limiters at runtime (#1505 follow-up). Backs the same
// /api/v1/admin/rate-limits surface as the admin console's Rate Limiters tab,
// so an operator can raise a too-tight bucket (or reset it) from the CLI.

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// rateLimiterRow mirrors internal/ratelimitcfg.State.
type rateLimiterRow struct {
	Key         string `json:"key"`
	Group       string `json:"group"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Unit        string `json:"unit"`
	Default     int    `json:"default"`
	Value       int    `json:"value"`
	Overridden  bool   `json:"overridden"`
}

type rateLimiterListResponse struct {
	Limiters []rateLimiterRow `json:"limiters"`
}

var adminRateLimitsCmd = &cobra.Command{
	Use:     "ratelimits",
	Aliases: []string{"rate-limits", "ratelimit"},
	Short:   "Inspect and tune runtime rate limiters (admin)",
	Long: `List every tunable rate limiter, override one, or reset it to the shipped default.

Overrides apply instance-wide and take effect immediately — the per-IP HTTP
buckets retune live; the other limiters pick up the new value on next use.

Examples:
  crewship admin ratelimits list
  crewship admin ratelimits set http.auth_per_min 30
  crewship admin ratelimits reset http.auth_per_min`,
}

var adminRateLimitsListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List all rate limiters with their current values",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Get("/api/v1/admin/rate-limits")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var list rateLimiterListResponse
		if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}

		return resolvedFormatter(cmd).AutoHuman(list, func() {
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "GROUP\tKEY\tVALUE\tDEFAULT\tUNIT\tSOURCE")
			for _, r := range list.Limiters {
				source := "default"
				if r.Overridden {
					source = "override"
				}
				fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\t%s\n",
					r.Group, r.Key, r.Value, r.Default, r.Unit, source)
			}
			if err := w.Flush(); err != nil {
				fmt.Fprintf(os.Stderr, "warning: %v\n", err)
			}
		})
	},
}

var adminRateLimitsSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Override a rate limiter's value (applies instance-wide, live)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := args[0]
		value, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("value must be a whole number, got %q", args[1])
		}
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Put("/api/v1/admin/rate-limits/"+key, map[string]int{"value": value})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var row rateLimiterRow
		if err := json.NewDecoder(resp.Body).Decode(&row); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		return resolvedFormatter(cmd).AutoHuman(row, func() {
			fmt.Printf("Set %s = %d %s (default %d). Applied instance-wide.\n",
				row.Key, row.Value, row.Unit, row.Default)
		})
	},
}

var adminRateLimitsResetCmd = &cobra.Command{
	Use:   "reset <key>",
	Short: "Reset a rate limiter to its shipped default",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := args[0]
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Delete("/api/v1/admin/rate-limits/" + key)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var row rateLimiterRow
		if err := json.NewDecoder(resp.Body).Decode(&row); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		return resolvedFormatter(cmd).AutoHuman(row, func() {
			fmt.Printf("Reset %s to its default of %d %s.\n", row.Key, row.Value, row.Unit)
		})
	},
}

func init() {
	adminRateLimitsCmd.AddCommand(adminRateLimitsListCmd)
	adminRateLimitsCmd.AddCommand(adminRateLimitsSetCmd)
	adminRateLimitsCmd.AddCommand(adminRateLimitsResetCmd)
	adminCmd.AddCommand(adminRateLimitsCmd)
}
