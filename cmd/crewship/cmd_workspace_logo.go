package main

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// `crewship workspace logo set|remove` (#3005): the current workspace's logo,
// the CLI side of Settings › General › Workspace logo. Same file rules as the
// profile picture: PNG, JPEG or WebP, at most 2MB and 4096px a side.
var workspaceLogoCmd = &cobra.Command{
	Use:   "logo",
	Short: "Set or remove the current workspace's logo",
	Long: `Set or remove the logo the current workspace is shown with: the
workspace switcher, Admin lists and the Backups and Security scope. Without a
logo the workspace is drawn with its initials. Needs ADMIN or OWNER.

    crewship workspace logo set ./logo.png
    crewship workspace logo remove`,
}

var workspaceLogoSetCmd = &cobra.Command{
	Use:   "set <file>",
	Short: "Upload or replace the workspace logo (PNG, JPEG or WebP, max 2MB)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, wsID, err := workspaceLogoClient()
		if err != nil {
			return err
		}
		fh, err := os.Open(args[0])
		if err != nil {
			return fmt.Errorf("open %s: %w", args[0], err)
		}
		defer fh.Close()
		// 2MB server cap: assemble in memory, as `auth avatar` does.
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, err := mw.CreateFormFile("file", filepath.Base(args[0]))
		if err != nil {
			return fmt.Errorf("multipart form: %w", err)
		}
		if _, err := io.Copy(fw, fh); err != nil {
			return fmt.Errorf("multipart copy: %w", err)
		}
		if err := mw.Close(); err != nil {
			return fmt.Errorf("multipart close: %w", err)
		}
		resp, err := postMultipart(cmd.Context(), client, "/api/v1/workspaces/"+wsID+"/logo", mw.FormDataContentType(), &buf)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		cli.PrintSuccess("Workspace logo updated.")
		return nil
	},
}

var workspaceLogoRemoveCmd = &cobra.Command{
	Use:   "remove",
	Short: "Remove the workspace logo (back to initials)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, wsID, err := workspaceLogoClient()
		if err != nil {
			return err
		}
		resp, err := client.Delete("/api/v1/workspaces/" + wsID + "/logo")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		cli.PrintSuccess("Workspace logo removed — back to initials.")
		return nil
	},
}

func workspaceLogoClient() (*cli.Client, string, error) {
	if err := requireAuth(); err != nil {
		return nil, "", err
	}
	if err := requireWorkspace(); err != nil {
		return nil, "", err
	}
	client := newAPIClient()
	wsID := client.GetWorkspaceID()
	if wsID == "" {
		return nil, "", fmt.Errorf("no workspace selected")
	}
	return client, wsID, nil
}

func init() {
	workspaceLogoCmd.AddCommand(workspaceLogoSetCmd)
	workspaceLogoCmd.AddCommand(workspaceLogoRemoveCmd)
	workspaceCmd.AddCommand(workspaceLogoCmd)
}
