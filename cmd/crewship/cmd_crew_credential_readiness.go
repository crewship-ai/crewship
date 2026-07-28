package main

// crewCredentialReadinessCmd is the CLI counterpart to
// GET /api/v1/crews/{crewId}/credential-readiness — the read-only report
// of which credentials a crew can use are for a CLI its container does
// not have.
//
// It exists because the two halves of that failure were never connected
// anywhere the user could see: the vault shows a healthy GitHub PAT, the
// crew's devcontainer never declared github-cli, and the only signal is
// `gh: command not found` inside an agent transcript. The command names
// both the credential and the feature ref that fixes it.
//
// Report only. It does not edit the devcontainer config and does not
// trigger a rebuild — adding a feature changes what runs inside the
// container, which is the user's call, so the output ends in the
// `crewship crew config` invocation rather than performing it.

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

type crewCredentialGapOut struct {
	CredentialID   string `json:"credential_id"`
	CredentialName string `json:"credential_name"`
	Provider       string `json:"provider"`
	Tool           string `json:"tool"`
	Feature        string `json:"feature"`
	FeatureID      string `json:"feature_id"`
}

type crewCredentialReadinessOut struct {
	CrewID   string                 `json:"crew_id"`
	CrewSlug string                 `json:"crew_slug"`
	Tools    []string               `json:"tools"`
	Checked  int                    `json:"checked"`
	Gaps     []crewCredentialGapOut `json:"gaps"`
}

var crewCredentialReadinessCmd = &cobra.Command{
	Use:   "credential-readiness <crew-slug-or-id>",
	Short: "Report credentials whose CLI is missing from the crew's container",
	Long: `Report which of a crew's credentials need a CLI the crew's container does not have.

The sandbox runtime image ships git, curl and jq. Tools like gh, aws, kubectl,
gcloud, docker, terraform and ansible only exist in the container when the
crew's devcontainer config declares the matching feature — so a valid
credential and a working agent are two different things.

This command only reports. Adding the feature is a separate step:

  crewship crew config <crew> --devcontainer ./devcontainer.json`,
	Example: `  crewship crew credential-readiness engineering
  crewship crew credential-readiness engineering --format json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}

		resp, err := client.Get("/api/v1/crews/" + crewID + "/credential-readiness")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out crewCredentialReadinessOut
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}

		f := newFormatter()
		return f.AutoHuman(out, func() {
			label := out.CrewSlug
			if label == "" {
				label = args[0]
			}
			if len(out.Gaps) == 0 {
				fmt.Printf("Crew %s: every credential's CLI is present in the container (%d checked).\n",
					label, out.Checked)
				return
			}

			fmt.Printf("Crew %s: %d of %d credential(s) need a tool the container doesn't have.\n\n",
				label, len(out.Gaps), out.Checked)
			rows := make([][]string, 0, len(out.Gaps))
			for _, g := range out.Gaps {
				rows = append(rows, []string{g.CredentialName, g.Provider, g.Tool, g.Feature})
			}
			f.Table([]string{"CREDENTIAL", "PROVIDER", "TOOL", "FEATURE"}, rows)

			if len(out.Tools) > 0 {
				fmt.Printf("\nAlready in the container: %s\n", strings.Join(out.Tools, ", "))
			}
			// Point at the fix without performing it — adding a feature
			// rebuilds the image, which the user opts into.
			fmt.Printf("\nAdd the missing feature(s) to the crew's devcontainer config, then re-provision:\n"+
				"  crewship crew config %s --show\n"+
				"  crewship crew config %s --devcontainer ./devcontainer.json\n", label, label)
		})
	},
}
