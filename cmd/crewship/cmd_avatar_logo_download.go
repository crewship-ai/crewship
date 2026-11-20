package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

func newUserAvatarGetCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "avatar-get <user-id>", Short: "Download a user's stored avatar", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		userID, err := guestCLIPathSegment(args[0])
		if err != nil {
			return err
		}
		output, _ := cmd.Flags().GetString("output")
		if output == "" || output == "-" {
			return fmt.Errorf("--output must name a local file")
		}
		if err := requireAuth(); err != nil {
			return err
		}
		dest, err := cli.NewAtomicFile(output)
		if err != nil {
			return err
		}
		defer dest.Close()
		resp, err := newAPIClient().Get("/api/v1/users/" + userID + "/avatar")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		n, err := io.Copy(dest, io.LimitReader(resp.Body, (2<<20)+1))
		if err != nil {
			return err
		}
		if n > 2<<20 {
			return fmt.Errorf("avatar exceeds 2 MiB")
		}
		return dest.Commit()
	}}
	cmd.Flags().String("output", "", "Destination file (atomic, owner-only)")
	return cmd
}

func newWorkspaceLogoGetCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "get", Short: "Download the current workspace logo", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		output, _ := cmd.Flags().GetString("output")
		if output == "" || output == "-" {
			return fmt.Errorf("--output must name a local file")
		}
		client, workspace, err := workspaceLogoClient()
		if err != nil {
			return err
		}
		dest, err := cli.NewAtomicFile(output)
		if err != nil {
			return err
		}
		defer dest.Close()
		workspace, err = guestCLIPathSegment(workspace)
		if err != nil {
			return err
		}
		resp, err := client.Get("/api/v1/workspaces/" + workspace + "/logo")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		n, err := io.Copy(dest, io.LimitReader(resp.Body, (2<<20)+1))
		if err != nil {
			return err
		}
		if n > 2<<20 {
			return fmt.Errorf("logo exceeds 2 MiB")
		}
		return dest.Commit()
	}}
	cmd.Flags().String("output", "", "Destination file (atomic, owner-only)")
	return cmd
}

func init() {
	authCmd.AddCommand(newUserAvatarGetCommand())
	workspaceLogoCmd.AddCommand(newWorkspaceLogoGetCommand())
}
