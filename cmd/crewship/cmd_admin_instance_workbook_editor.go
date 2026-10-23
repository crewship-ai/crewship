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
	LearnedFrom       string    `json:"learned_from,omitempty" yaml:"learned_from,omitempty"`
	AdminSession      bool      `json:"admin_session,omitempty" yaml:"admin_session,omitempty"`
	ProxyCheck        string    `json:"proxy_check" yaml:"proxy_check"`
	Disabled          bool      `json:"disabled" yaml:"disabled"`
	Cause             string    `json:"cause,omitempty" yaml:"cause,omitempty"`
	Problems          []string  `json:"problems,omitempty" yaml:"problems,omitempty"`
	Warnings          []string  `json:"warnings,omitempty" yaml:"warnings,omitempty"`
	ConsecutivePasses int       `json:"consecutive_passes,omitempty" yaml:"consecutive_passes,omitempty"`
	CheckedAt         time.Time `json:"checked_at,omitzero,omitempty" yaml:"checked_at,omitempty"`
	LastSeen          time.Time `json:"last_seen,omitzero,omitempty" yaml:"last_seen,omitempty"`
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

A learned origin names the user whose signed-in request taught it, and
whether an instance administrator's session used it. Only the configured
origin and origins an administrator used count towards /healthz; any
signed-in user can make the server learn an origin, so check that the others
are yours. A learned origin not used for 24 hours is forgotten.

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
				source := o.Source
				if o.LearnedFrom != "" {
					source += " from " + o.LearnedFrom
				}
				if o.AdminSession {
					source += " (admin)"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", o.Origin, source, o.ProxyCheck, editor, cause)
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
