package main

// `crewship page access <slug> --me` — the caller's own paths to a page
// (docs/prd/pages-folder-permissions-linux-model-2026-09-13.md §3/10, §6;
// #2533).
//
//	GET /api/v1/pages/{slug}/access/me   page access <slug> --me
//
// It prints the server's paths verbatim — `owner`, `role`, `crew:<slug>`,
// `panel_crew:<slug>`, `folder:<slug>`, `grant` — the same words the page
// list's REACH column uses, because the vocabulary is the API's and a client
// that reworded it would be a second place for it to drift. Anyone who can
// open the page may ask; the answer describes them and nobody else. Who
// ELSE reaches a page is the owner's and the admin's question, answered by
// the folder's `page folder acl` and the page's `page grants`.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

type pageAccessMeJSON struct {
	Page        string   `json:"page"`
	SubjectType string   `json:"subject_type"`
	SubjectID   string   `json:"subject_id"`
	Label       string   `json:"label"`
	Paths       []string `json:"paths"`
	Folder      string   `json:"folder"`
	Shared      string   `json:"shared"`
}

var pageAccessMeCmd = &cobra.Command{
	Use:   "access <slug> --me",
	Short: "How you reach a page: your own paths, in the API's words",
	Long: `Print the paths by which YOU reach a page.

  crewship page access fleet-201 --me

A path is one of the API's own words:

  owner                 you own the page
  role                  your workspace role carries manage (ADMIN or OWNER)
  crew:<slug>           you belong to the crew that owns the page
  panel_crew:<slug>     you belong to a crew that owns a panel on it
  folder:<slug>         the folder the page is in is shared with you
  grant                 a live grant on the page names you or one of your crews

When the page is in a folder, the folder's slug and its sharing label (none,
crew, workspace) are printed too — the label, never the names: who a folder is
shared with is its managers' to read ("page folder acl").`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if me, _ := cmd.Flags().GetBool("me"); !me {
			return cli.WithExitCode(errors.New("pass --me: this command answers about you; who else reaches the page is \"page folder acl\" and \"page grants\""), cli.ExitValidation)
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		raw, err := pageFolderGet(client, "/api/v1/pages/"+pagePathEscape(args[0])+"/access/me")
		if err != nil {
			return err
		}
		f := newFormatter()
		switch f.Format {
		case "json", "yaml", "ndjson":
			return pageEmitMachine(f, raw, "{}")
		}
		var doc pageAccessMeJSON
		if err := json.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if f.Format == "quiet" {
			for _, p := range doc.Paths {
				fmt.Println(p)
			}
			return nil
		}
		fmt.Printf("%s reaches page %s by: %s\n", doc.Label, doc.Page, strings.Join(doc.Paths, ", "))
		if doc.Folder != "" {
			fmt.Printf("In folder %s (shared: %s).\n", doc.Folder, doc.Shared)
		}
		return nil
	},
}

func init() {
	pageAccessMeCmd.Flags().Bool("me", false, "Ask about yourself (required)")
	pageCmd.AddCommand(pageAccessMeCmd)
}
