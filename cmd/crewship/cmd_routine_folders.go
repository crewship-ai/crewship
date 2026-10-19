package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// Routine folders (ODCH-RT-01): `crewship routine folders …` mirrors
// /workspaces/{ws}/pipeline-folders. A folder only organises the Routines
// panel; it grants no access. A folder may wear an icon (--icon, the crew icon
// registry) and sit inside another (--parent), at most three levels deep.

type routineFolderRow struct {
	ID           string `json:"id" yaml:"id"`
	Name         string `json:"name" yaml:"name"`
	Color        string `json:"color,omitempty" yaml:"color,omitempty"`
	Icon         string `json:"icon,omitempty" yaml:"icon,omitempty"`
	ParentID     string `json:"parent_id,omitempty" yaml:"parent_id,omitempty"`
	RoutineCount int    `json:"routine_count" yaml:"routine_count"`
	CreatedAt    string `json:"created_at" yaml:"created_at"`
	UpdatedAt    string `json:"updated_at" yaml:"updated_at"`
}

var routineFoldersCmd = &cobra.Command{
	Use:   "folders",
	Short: "Organise routines into folders (the Routines panel's Group by Folder)",
}

var routineFoldersListCmd = &cobra.Command{
	Use:   "list",
	Short: "List routine folders with how many routines each holds",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var rows []routineFolderRow
		if err := getJSON(client, fmt.Sprintf("/api/v1/workspaces/%s/pipeline-folders", client.GetWorkspaceID()), &rows); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(rows, func() {
			if len(rows) == 0 {
				fmt.Println("No folders yet. Create one with: crewship routine folders create <name>")
				return
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tFOLDER\tICON\tCOLOR\tROUTINES")
			for _, r := range rows {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\n", r.ID, routineFolderPath(rows, r), r.Icon, r.Color, r.RoutineCount)
			}
			_ = w.Flush()
		})
	},
}

// routineFolderPath is "Ops / Daily": the folder's name after its parents'.
func routineFolderPath(rows []routineFolderRow, f routineFolderRow) string {
	byID := make(map[string]routineFolderRow, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	parts := []string{f.Name}
	for p, ok := byID[f.ParentID]; ok && len(parts) < 8; p, ok = byID[p.ParentID] {
		parts = append([]string{p.Name}, parts...)
	}
	return strings.Join(parts, " / ")
}

var routineFoldersCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a routine folder",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		color, _ := cmd.Flags().GetString("color")
		icon, _ := cmd.Flags().GetString("icon")
		parent, _ := cmd.Flags().GetString("parent")
		var out routineFolderRow
		if err := postJSON(client, fmt.Sprintf("/api/v1/workspaces/%s/pipeline-folders", client.GetWorkspaceID()),
			map[string]any{"name": args[0], "color": color, "icon": icon, "parent_id": parent}, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() { fmt.Printf("Created folder %q (%s).\n", out.Name, out.ID) })
	},
}

var routineFoldersRenameCmd = &cobra.Command{
	Use:   "update <folder_id>",
	Short: "Rename, recolour, change the icon of or move a routine folder",
	Long: `Change a routine folder. Only the flags you pass change.

  --parent <id> moves the folder inside another; --parent "" moves it to the
  top level. A folder cannot go inside itself or one of its subfolders, and
  folders nest at most three levels deep.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		body := map[string]any{}
		if cmd.Flags().Changed("name") {
			v, _ := cmd.Flags().GetString("name")
			body["name"] = v
		}
		if cmd.Flags().Changed("color") {
			v, _ := cmd.Flags().GetString("color")
			body["color"] = v
		}
		if cmd.Flags().Changed("icon") {
			v, _ := cmd.Flags().GetString("icon")
			body["icon"] = v
		}
		if cmd.Flags().Changed("parent") {
			v, _ := cmd.Flags().GetString("parent")
			if v == "" {
				body["parent_id"] = nil
			} else {
				body["parent_id"] = v
			}
		}
		if len(body) == 0 {
			return fmt.Errorf("pass at least one of --name, --color, --icon, --parent")
		}
		var out routineFolderRow
		if err := patchJSON(client, fmt.Sprintf("/api/v1/workspaces/%s/pipeline-folders/%s", client.GetWorkspaceID(), url.PathEscape(args[0])), body, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() { fmt.Printf("Folder %s is now %q.\n", out.ID, out.Name) })
	},
}

var routineFoldersDeleteCmd = &cobra.Command{
	Use:   "delete <folder_id>",
	Short: "Delete a routine folder; its routines are kept and become unfiled, its subfolders move up",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		if err := deleteJSON(client, fmt.Sprintf("/api/v1/workspaces/%s/pipeline-folders/%s", client.GetWorkspaceID(), url.PathEscape(args[0]))); err != nil {
			return err
		}
		// A receipt goes to stderr so `-f json | jq` stays clean (#2086).
		cli.PrintSuccess(fmt.Sprintf("Deleted folder %s. Its routines are unfiled.", args[0]))
		return nil
	},
}

var routineFoldersMoveCmd = &cobra.Command{
	Use:   "move <routine_slug>...",
	Short: "File routines in a folder, or take them out with --none",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		folder, _ := cmd.Flags().GetString("folder")
		none, _ := cmd.Flags().GetBool("none")
		if (folder == "") == !none {
			return fmt.Errorf("pass --folder <id> or --none, not both")
		}
		var target any
		if folder != "" {
			target = folder
		}
		var out struct {
			Moved    int     `json:"moved" yaml:"moved"`
			FolderID *string `json:"folder_id" yaml:"folder_id"`
		}
		if err := postJSON(client, fmt.Sprintf("/api/v1/workspaces/%s/pipeline-folders/move", client.GetWorkspaceID()),
			map[string]any{"slugs": args, "folder_id": target}, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() { fmt.Printf("Moved %d routine(s).\n", out.Moved) })
	},
}

func init() {
	routineFoldersCreateCmd.Flags().String("color", "", "palette colour for the folder (optional)")
	routineFoldersCreateCmd.Flags().String("icon", "", "icon name from the crew icon registry (optional)")
	routineFoldersCreateCmd.Flags().String("parent", "", "create inside this folder id (a subfolder)")
	routineFoldersRenameCmd.Flags().String("name", "", "new name")
	routineFoldersRenameCmd.Flags().String("color", "", "new colour; empty clears it")
	routineFoldersRenameCmd.Flags().String("icon", "", "new icon from the crew icon registry; empty clears it")
	routineFoldersRenameCmd.Flags().String("parent", "", "move inside this folder id; empty moves it to the top level")
	routineFoldersMoveCmd.Flags().String("folder", "", "folder id to file the routines in")
	routineFoldersMoveCmd.Flags().Bool("none", false, "take the routines out of their folder")
	routineFoldersCmd.AddCommand(routineFoldersListCmd, routineFoldersCreateCmd, routineFoldersRenameCmd, routineFoldersDeleteCmd, routineFoldersMoveCmd)
	pipelineCmd.AddCommand(routineFoldersCmd)
}
