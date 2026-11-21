package main

import (
	"fmt"
	"io"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// maxImageDownload matches the 2 MiB upload ceiling for avatars and logos.
const maxImageDownload = 2 << 20

// downloadImage writes a GET response body to output atomically: a refused or
// oversized download leaves an existing destination untouched.
func downloadImage(client *cli.Client, path, output, what string) error {
	if output == "" || output == "-" {
		return fmt.Errorf("--output must name a local file")
	}
	dest, err := cli.NewAtomicFile(output)
	if err != nil {
		return err
	}
	defer dest.Close()
	resp, err := client.Get(path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	n, err := io.Copy(dest, io.LimitReader(resp.Body, maxImageDownload+1))
	if err != nil {
		return err
	}
	if n > maxImageDownload {
		return fmt.Errorf("%s exceeds 2 MiB", what)
	}
	return dest.Commit()
}

func newUserAvatarGetCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "avatar-get <user-id>", Short: "Download a user's stored avatar", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		output, _ := cmd.Flags().GetString("output")
		id, err := guestCLIPathSegment(args[0])
		if err != nil {
			return err
		}
		if err := requireAuth(); err != nil {
			return err
		}
		return downloadImage(newAPIClient(), "/api/v1/users/"+id+"/avatar", output, "avatar")
	}}
	cmd.Flags().String("output", "", "Destination file (atomic, owner-only)")
	return cmd
}

func newWorkspaceLogoGetCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "get", Short: "Download the current workspace logo", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		output, _ := cmd.Flags().GetString("output")
		client, workspace, err := workspaceLogoClient()
		if err != nil {
			return err
		}
		id, err := guestCLIPathSegment(workspace)
		if err != nil {
			return err
		}
		return downloadImage(client, "/api/v1/workspaces/"+id+"/logo", output, "logo")
	}}
	cmd.Flags().String("output", "", "Destination file (atomic, owner-only)")
	return cmd
}

func init() {
	authCmd.AddCommand(newUserAvatarGetCommand())
	workspaceLogoCmd.AddCommand(newWorkspaceLogoGetCommand())
}
