package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var labelCmd = &cobra.Command{
	Use:     "label",
	Aliases: []string{"labels"},
	Short:   "Manage workspace labels",
}

var labelListCmd = &cobra.Command{
	Use:   "list",
	Short: "List workspace labels",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/labels")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var labels []labelItem
		if err := cli.ReadJSON(resp, &labels); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "NAME", "COLOR", "GROUP"}
		var rows [][]string
		for _, l := range labels {
			group := derefStr(l.Group, "-")
			rows = append(rows, []string{truncateID(l.ID, 12), l.Name, l.Color, group})
		}
		return f.Auto(labels, headers, rows)
	},
}

var labelCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new label",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		name, _ := cmd.Flags().GetString("name")
		color, _ := cmd.Flags().GetString("color")
		if name == "" {
			return fmt.Errorf("--name is required")
		}
		if color == "" {
			return fmt.Errorf("--color is required")
		}

		body := map[string]interface{}{"name": name, "color": color}
		if v, _ := cmd.Flags().GetString("group"); v != "" {
			body["label_group"] = v
		}

		client := newAPIClient()
		resp, err := client.Post("/api/v1/labels", body)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var created labelItem
		if err := cli.ReadJSON(resp, &created); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Label created: %s (%s)", created.Name, created.ID))
		return nil
	},
}

var labelUpdateCmd = &cobra.Command{
	Use:   "update <label-id>",
	Short: "Update a label",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		flags := cmd.Flags()
		body := map[string]interface{}{}
		if flags.Changed("name") {
			v, _ := flags.GetString("name")
			body["name"] = v
		}
		if flags.Changed("color") {
			v, _ := flags.GetString("color")
			body["color"] = v
		}
		if flags.Changed("group") {
			v, _ := flags.GetString("group")
			body["label_group"] = v
		}

		if len(body) == 0 {
			return fmt.Errorf("no fields to update")
		}

		client := newAPIClient()
		resp, err := client.Patch("/api/v1/labels/"+args[0], body)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		cli.PrintSuccess("Label updated.")
		return nil
	},
}

var labelDeleteCmd = &cobra.Command{
	Use:     "delete <label-id>",
	Aliases: []string{"remove", "rm"},
	Short:   "Delete a label",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		if err := confirmAction(cmd, fmt.Sprintf("Delete label %q?", args[0])); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Delete("/api/v1/labels/" + args[0])
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		cli.PrintSuccess("Label deleted.")
		return nil
	},
}

func init() {
	labelCreateCmd.Flags().String("name", "", "Label name (required)")
	labelCreateCmd.Flags().String("color", "", "Hex color (e.g. #3B82F6) (required)")
	labelCreateCmd.Flags().String("group", "", "Label group")

	labelUpdateCmd.Flags().String("name", "", "New label name")
	labelUpdateCmd.Flags().String("color", "", "New hex color")
	labelUpdateCmd.Flags().String("group", "", "New label group")

	labelDeleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	labelCmd.AddCommand(labelListCmd)
	labelCmd.AddCommand(labelCreateCmd)
	labelCmd.AddCommand(labelUpdateCmd)
	labelCmd.AddCommand(labelDeleteCmd)
}
