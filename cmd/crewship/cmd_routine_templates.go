package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// `crewship routine templates …` mirrors /workspaces/{ws}/pipeline-templates
// (ODCH-RT-22). Using a template saves a DRAFT; it never publishes.

type routineTemplateRow struct {
	ID          string          `json:"id" yaml:"id"`
	Name        string          `json:"name" yaml:"name"`
	Description string          `json:"description,omitempty" yaml:"description,omitempty"`
	Category    string          `json:"category,omitempty" yaml:"category,omitempty"`
	StepCount   int             `json:"step_count" yaml:"step_count"`
	Definition  json.RawMessage `json:"definition,omitempty" yaml:"definition,omitempty"`
	CreatedAt   string          `json:"created_at" yaml:"created_at"`
}

var routineTemplatesCmd = &cobra.Command{
	Use:   "templates",
	Short: "Keep routine definitions as templates and start new draft routines from them",
}

func templatesPath(client interface{ GetWorkspaceID() string }, rest string) string {
	return fmt.Sprintf("/api/v1/workspaces/%s/pipeline-templates%s", client.GetWorkspaceID(), rest)
}

var routineTemplatesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the workspace's routine templates",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var rows []routineTemplateRow
		if err := getJSON(client, templatesPath(client, ""), &rows); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(rows, func() {
			if len(rows) == 0 {
				fmt.Println("No templates yet. Make one with: crewship routine templates create <name> --from-routine <slug>")
				return
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tCATEGORY\tSTEPS")
			for _, r := range rows {
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\n", r.ID, r.Name, r.Category, r.StepCount)
			}
			_ = w.Flush()
		})
	},
}

var routineTemplatesGetCmd = &cobra.Command{
	Use:   "get <template_id>",
	Short: "Show a template with its definition",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var t routineTemplateRow
		if err := getJSON(client, templatesPath(client, "/"+url.PathEscape(args[0])), &t); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(t, func() {
			fmt.Printf("%s  %s\n  %d steps  %s\n%s\n", t.ID, t.Name, t.StepCount, t.Description, string(t.Definition))
		})
	},
}

var routineTemplatesCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Make a template from an existing routine or from a definition file",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		from, _ := cmd.Flags().GetString("from-routine")
		file, _ := cmd.Flags().GetString("file")
		desc, _ := cmd.Flags().GetString("description")
		category, _ := cmd.Flags().GetString("category")
		if (from == "") == (file == "") {
			return fmt.Errorf("pass --from-routine <slug> or --file <definition.json>, not both")
		}
		body := map[string]any{"name": args[0], "description": desc, "category": category}
		if from != "" {
			body["from_routine"] = from
		} else {
			raw, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			if !json.Valid(raw) {
				return fmt.Errorf("%s is not JSON (the template body is the routine definition as JSON)", file)
			}
			body["definition"] = json.RawMessage(raw)
		}
		var out routineTemplateRow
		if err := postJSON(client, templatesPath(client, ""), body, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() { fmt.Printf("Created template %q (%s).\n", out.Name, out.ID) })
	},
}

var routineTemplatesUseCmd = &cobra.Command{
	Use:   "use <template_id> --slug <new-slug>",
	Short: "Save a new DRAFT routine from a template (nothing is published)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		slug, _ := cmd.Flags().GetString("slug")
		name, _ := cmd.Flags().GetString("name")
		crew, _ := cmd.Flags().GetString("crew-id")
		if slug == "" {
			return fmt.Errorf("--slug is required")
		}
		var draft struct {
			Slug     string `json:"slug" yaml:"slug"`
			Revision int    `json:"revision" yaml:"revision"`
		}
		if err := postJSON(client, templatesPath(client, "/"+url.PathEscape(args[0])+"/use"),
			map[string]any{"slug": slug, "name": name, "author_crew_id": crew}, &draft); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(draft, func() {
			fmt.Printf("Saved draft %q. Test it and publish it from the routine page, or: crewship routine draft get %s\n", draft.Slug, draft.Slug)
		})
	},
}

var routineTemplatesDeleteCmd = &cobra.Command{
	Use:   "delete <template_id>",
	Short: "Delete a template (routines made from it are not affected)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		if err := deleteJSON(client, templatesPath(client, "/"+url.PathEscape(args[0]))); err != nil {
			return err
		}
		fmt.Printf("Deleted template %s.\n", args[0])
		return nil
	},
}

func init() {
	routineTemplatesCreateCmd.Flags().String("from-routine", "", "slug of the routine whose definition becomes the template")
	routineTemplatesCreateCmd.Flags().String("file", "", "a routine definition as a JSON file")
	routineTemplatesCreateCmd.Flags().String("description", "", "one line about what it is for")
	routineTemplatesCreateCmd.Flags().String("category", "", "a short grouping word")
	routineTemplatesUseCmd.Flags().String("slug", "", "slug of the new routine (required)")
	routineTemplatesUseCmd.Flags().String("name", "", "display name (default: the template's name)")
	routineTemplatesUseCmd.Flags().String("crew-id", "", "id of the crew that will own the routine")
	routineTemplatesCmd.AddCommand(routineTemplatesListCmd, routineTemplatesGetCmd, routineTemplatesCreateCmd, routineTemplatesUseCmd, routineTemplatesDeleteCmd)
	pipelineCmd.AddCommand(routineTemplatesCmd)
}
