package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

func newPageArchiveCommands() []*cobra.Command {
	commands := make([]*cobra.Command, 0, 2)
	for _, operation := range []string{"archive", "unarchive"} {
		operation := operation
		cmd := &cobra.Command{Use: operation + " <page>", Args: cobra.ExactArgs(1), Short: "Review and confirm a catalog change; content and access are retained", RunE: func(cmd *cobra.Command, args []string) error {
			body, err := pageCatalogCLIReviewBody(cmd)
			if err != nil {
				return err
			}
			client, err := pageClient()
			if err != nil {
				return err
			}
			path := "/api/v1/pages/" + pagePathEscape(args[0]) + "/archive"
			if operation == "unarchive" {
				path = "/api/v1/pages/" + pagePathEscape(args[0]) + "/unarchive"
			}
			resp, err := client.Post(path, body)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			raw, err := pageFolderRead(resp)
			if err != nil {
				return err
			}
			return pageCatalogCLIPrint(raw)
		}}
		addPageCatalogReviewFlags(cmd)
		commands = append(commands, cmd)
	}
	return commands
}

func addPageCatalogReviewFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("dry-run", false, "Preview only; returns a review token and makes no change")
	cmd.Flags().Bool("yes", false, "Confirm exactly the previously reviewed change")
	cmd.Flags().Int64("pages-version", 0, "Exact pages_version from the reviewed Page (required)")
	cmd.Flags().String("review-token", "", "Server token returned by the matching dry-run")
}

func pageCatalogCLIReviewBody(cmd *cobra.Command) (map[string]any, error) {
	dry, _ := cmd.Flags().GetBool("dry-run")
	yes, _ := cmd.Flags().GetBool("yes")
	token, _ := cmd.Flags().GetString("review-token")
	version, _ := cmd.Flags().GetInt64("pages-version")
	if !cmd.Flags().Changed("pages-version") || version < 0 || version >= 9007199254740991 || (dry && (yes || token != "")) || (!dry && (!yes || token == "")) {
		return nil, cli.WithExitCode(fmt.Errorf("use --dry-run --pages-version N first; then --yes --pages-version N --review-token TOKEN to confirm the same review"), cli.ExitValidation)
	}
	body := map[string]any{"pages_version": version}
	if dry {
		body["dry_run"] = true
	} else {
		body["confirm"] = true
		body["review_token"] = token
	}
	return body, nil
}

func pageCatalogCLIPrint(raw []byte) error {
	f := newFormatter()
	switch f.Format {
	case "json", "yaml", "ndjson":
		return pageEmitMachine(f, raw, "{}")
	case "quiet":
		return nil
	default:
		fmt.Println(string(raw))
		return nil
	}
}

func init() {
	pageCmd.AddCommand(newPageArchiveCommands()...)
	// Preserve the existing fenced CLI mode. Review flags opt into the new
	// exact preview/confirmation contract; they never trigger a blind retry.
	previous := pageFolderAddCmd.RunE
	addPageCatalogReviewFlags(pageFolderAddCmd)
	pageFolderAddCmd.Flags().Int64("acl-version", 0, "Exact target acl_version from the review (required in review mode)")
	pageFolderAddCmd.RunE = func(cmd *cobra.Command, args []string) error {
		review := false
		for _, flag := range []string{"dry-run", "yes", "review-token", "pages-version", "acl-version"} {
			review = review || cmd.Flags().Changed(flag)
		}
		if !review {
			return previous(cmd, args)
		}
		body, err := pageCatalogCLIReviewBody(cmd)
		if err != nil {
			return err
		}
		acl, _ := cmd.Flags().GetInt64("acl-version")
		if !cmd.Flags().Changed("acl-version") || acl < 0 || acl > 9007199254740991 {
			return cli.WithExitCode(fmt.Errorf("--acl-version is required for a reviewed folder move"), cli.ExitValidation)
		}
		body["page"] = args[1]
		body["acl_version"] = acl
		client, err := pageClient()
		if err != nil {
			return err
		}
		resp, err := client.Post("/api/v1/page-folders/"+pagePathEscape(args[0])+"/pages", body)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		raw, err := pageFolderRead(resp)
		if err != nil {
			return err
		}
		return pageCatalogCLIPrint(raw)
	}
}
