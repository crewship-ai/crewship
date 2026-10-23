//go:build !clionly

package main

// `crewship admin instance workbook-editor` — GET /api/v1/admin/instance/workbook-editor.

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

type workbookEditorOrigin struct {
	Origin            string    `json:"origin" yaml:"origin"`
	Source            string    `json:"source" yaml:"source"`
	ProxyCheck        string    `json:"proxy_check" yaml:"proxy_check"`
	Disabled          bool      `json:"disabled" yaml:"disabled"`
	Cause             string    `json:"cause,omitempty" yaml:"cause,omitempty"`
	Problems          []string  `json:"problems,omitempty" yaml:"problems,omitempty"`
	Warnings          []string  `json:"warnings,omitempty" yaml:"warnings,omitempty"`
	ConsecutivePasses int       `json:"consecutive_passes,omitempty" yaml:"consecutive_passes,omitempty"`
	CheckedAt         time.Time `json:"checked_at,omitzero" yaml:"checked_at,omitempty"`
}

type workbookEditorState struct {
	Mode    string                 `json:"mode" yaml:"mode"`
	Status  string                 `json:"status,omitempty" yaml:"status,omitempty"`
	Origin  string                 `json:"origin,omitempty" yaml:"origin,omitempty"`
	Origins []workbookEditorOrigin `json:"origins,omitempty" yaml:"origins,omitempty"`
}

var adminInstanceWorkbookEditorCmd = &cobra.Command{
	Use:   "workbook-editor",
	Short: "Show the embedded workbook editor's mode and proxy self-check",
	Long: `GET /api/v1/admin/instance/workbook-editor. The embedded workbook editor's
mode (off, same-host or dedicated) and, in same-host mode, the server's
self-check of every public Studio origin it knows: the configured one
(CREWSHIP_PAGE_STUDIO_ORIGIN), or those signed-in browsers used. For each:
the result, whether the editor is disabled there, the cause and the
problems found. The public /healthz shows only the coarse state.

A disabled origin comes back by itself after three consecutive passing
checks, five minutes apart.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireAuthInstance()
		if err != nil {
			return err
		}
		var out workbookEditorState
		if err := getJSON(client, "/api/v1/admin/instance/workbook-editor", &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			w := cmd.OutOrStdout()
			switch out.Mode {
			case "same-host":
				fmt.Fprintf(w, "Workbook editor: same host (/_editor/ on the Studio origin), %s\n", out.Status)
			case "dedicated":
				fmt.Fprintf(w, "Workbook editor: dedicated origin %s\n", out.Origin)
				return
			default:
				fmt.Fprintln(w, "Workbook editor: off")
				return
			}
			if len(out.Origins) == 0 {
				fmt.Fprintln(w, "No public Studio origin known yet: the self-check starts once a signed-in browser has used Studio.")
				return
			}
			tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ORIGIN\tSOURCE\tCHECK\tEDITOR\tCAUSE")
			for _, o := range out.Origins {
				editor := "enabled"
				if o.Disabled {
					editor = fmt.Sprintf("disabled (%d/3 passes)", o.ConsecutivePasses)
				}
				cause := o.Cause
				if cause == "" {
					cause = "-"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", o.Origin, o.Source, o.ProxyCheck, editor, cause)
			}
			_ = tw.Flush()
			for _, o := range out.Origins {
				for _, p := range append(append([]string{}, o.Problems...), o.Warnings...) {
					fmt.Fprintf(w, "  %s: %s\n", o.Origin, strings.TrimSpace(p))
				}
			}
		})
	},
}

func init() {
	adminInstanceCmd.AddCommand(adminInstanceWorkbookEditorCmd)
}
