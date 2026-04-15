package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

var crewProvisionCmd = &cobra.Command{
	Use:   "provision <slug-or-id>",
	Short: "Trigger devcontainer provisioning for a crew",
	Args:  cobra.ExactArgs(1),
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

		resp, err := client.Post("/api/v1/crews/"+crewID+"/provision", nil)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess(fmt.Sprintf("Provisioning started for crew %q.", args[0]))
		return nil
	},
}

var crewProvisionStatusCmd = &cobra.Command{
	Use:   "status <slug-or-id>",
	Short: "Check provisioning status for a crew",
	Args:  cobra.ExactArgs(1),
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

		resp, err := client.Get("/api/v1/crews/" + crewID + "/provision")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result struct {
			Status             string  `json:"status"`
			CachedImage        *string `json:"cached_image"`
			ConfigHash         *string `json:"config_hash"`
			DevcontainerConfig *string `json:"devcontainer_config"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}

		f := newFormatter()
		cachedImage := "-"
		if result.CachedImage != nil {
			cachedImage = *result.CachedImage
		}
		configHash := "-"
		if result.ConfigHash != nil {
			configHash = *result.ConfigHash
		}
		hasConfig := "no"
		if result.DevcontainerConfig != nil {
			hasConfig = "yes"
		}
		pairs := [][]string{
			{"Status", result.Status},
			{"Has Config", hasConfig},
			{"Cached Image", cachedImage},
			{"Config Hash", configHash},
		}
		return f.AutoDetail(result, pairs)
	},
}

var crewRebuildCmd = &cobra.Command{
	Use:   "rebuild <slug-or-id>",
	Short: "Invalidate cache and re-provision a crew container",
	Args:  cobra.ExactArgs(1),
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

		resp, err := client.Post("/api/v1/crews/"+crewID+"/rebuild", nil)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess(fmt.Sprintf("Cache invalidated and provisioning started for crew %q.", args[0]))
		return nil
	},
}

func init() {
	crewProvisionCmd.AddCommand(crewProvisionStatusCmd)
	crewCmd.AddCommand(crewProvisionCmd)
	crewCmd.AddCommand(crewRebuildCmd)
}
