package main

// CLI counterpart to /api/v1/credential-folders (Vault folders, #3479).
//
//	credential folder list | create | update | delete
//
// plus --folder on `credential create`, `credential update` and
// `credential list`. A folder groups credentials and carries a reveal policy
// (inherit | admins | nobody — it only ever tightens the workspace gate), a
// Keeper tier floor/default and a replace-reminder. Folders nest two levels.

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

type credFolderOut struct {
	ID                   string  `json:"id" yaml:"id"`
	ParentID             *string `json:"parent_id" yaml:"parent_id"`
	Name                 string  `json:"name" yaml:"name"`
	Description          string  `json:"description" yaml:"description"`
	RevealPolicy         string  `json:"reveal_policy" yaml:"reveal_policy"`
	RemindAfterDays      int     `json:"remind_after_days" yaml:"remind_after_days"`
	DefaultSecurityLevel *int    `json:"default_security_level" yaml:"default_security_level"`
	MinSecurityLevel     int     `json:"min_security_level" yaml:"min_security_level"`
	CredentialCount      int     `json:"credential_count" yaml:"credential_count"`
	CreatedAt            string  `json:"created_at" yaml:"created_at"`
	UpdatedAt            string  `json:"updated_at" yaml:"updated_at"`
}

func listCredentialFolders(client *cli.Client) ([]credFolderOut, error) {
	resp, err := client.Get("/api/v1/credential-folders")
	if err != nil {
		return nil, err
	}
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var out []credFolderOut
	if err := cli.ReadJSON(resp, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// folderPath renders "Parent/Child" for a folder, the spelling --folder accepts.
func folderPath(all []credFolderOut, f credFolderOut) string {
	if f.ParentID == nil {
		return f.Name
	}
	for _, p := range all {
		if p.ID == *f.ParentID {
			return p.Name + "/" + f.Name
		}
	}
	return f.Name
}

// resolveCredentialFolderID accepts an id, a root folder name or a
// "Parent/Child" path (case-insensitive). A bare name that matches folders
// under two parents is refused rather than guessed.
func resolveCredentialFolderID(client *cli.Client, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("folder name or id is required")
	}
	all, err := listCredentialFolders(client)
	if err != nil {
		return "", fmt.Errorf("resolve folder: %w", err)
	}
	for _, f := range all {
		if f.ID == ref {
			return f.ID, nil
		}
	}
	var hits []credFolderOut
	for _, f := range all {
		if strings.EqualFold(folderPath(all, f), ref) || (f.ParentID == nil && strings.EqualFold(f.Name, ref)) {
			hits = append(hits, f)
		}
	}
	if len(hits) == 0 {
		// A unique child name is accepted without its parent.
		for _, f := range all {
			if strings.EqualFold(f.Name, ref) {
				hits = append(hits, f)
			}
		}
	}
	switch len(hits) {
	case 0:
		return "", fmt.Errorf("no folder %q (see `crewship credential folder list`)", ref)
	case 1:
		return hits[0].ID, nil
	default:
		return "", fmt.Errorf("%d folders are called %q; use the Parent/Child path or the id", len(hits), ref)
	}
}

var credFolderCmd = &cobra.Command{
	Use:     "folder",
	Aliases: []string{"folders"},
	Short:   "Manage Vault folders for credentials",
	Long: `Manage Vault folders.

A folder groups credentials and carries policy: who may reveal the values in it
(reveal policy: inherit, admins or nobody), the lowest Keeper tier a secret in it
may hold, a default tier for new ones, and a replace-reminder in days. Folders
nest two levels. A reveal policy only ever tightens the workspace reveal gate.`,
}

func folderFlagsBody(cmd *cobra.Command) (map[string]interface{}, error) {
	flags := cmd.Flags()
	body := map[string]interface{}{}
	if flags.Changed("name") {
		v, _ := flags.GetString("name")
		body["name"] = v
	}
	if flags.Changed("description") {
		v, _ := flags.GetString("description")
		body["description"] = v
	}
	if flags.Changed("reveal") {
		v, _ := flags.GetString("reveal")
		if v != "inherit" && v != "admins" && v != "nobody" {
			return nil, cli.WithExitCode(fmt.Errorf("--reveal must be inherit, admins or nobody"), cli.ExitValidation)
		}
		body["reveal_policy"] = v
	}
	if flags.Changed("remind-after-days") {
		v, _ := flags.GetInt("remind-after-days")
		body["remind_after_days"] = v
	}
	if flags.Changed("min-security-level") {
		v, _ := flags.GetInt("min-security-level")
		body["min_security_level"] = v
	}
	if flags.Changed("default-security-level") {
		v, _ := flags.GetInt("default-security-level")
		body["default_security_level"] = v
	}
	return body, nil
}

func addFolderPolicyFlags(c *cobra.Command) {
	c.Flags().String("description", "", "What the folder is for")
	c.Flags().String("reveal", "", "Reveal policy: inherit (workspace rules), admins (owners and admins only) or nobody (sealed)")
	c.Flags().Int("remind-after-days", 0, "Remind to replace a secret this many days after it was created or last rotated (0 = off)")
	c.Flags().Int("min-security-level", 0, "Lowest Keeper tier (1-4) a secret in this folder may hold")
	c.Flags().Int("default-security-level", 0, "Keeper tier (1-4) a new secret gets when none is given")
}

var credFolderListCmd = &cobra.Command{
	Use:   "list",
	Short: "List Vault folders",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		all, err := listCredentialFolders(newAPIClient())
		if err != nil {
			return err
		}
		f := newFormatter()
		return f.AutoHuman(all, func() {
			if len(all) == 0 {
				fmt.Println("No folders. Create one with `crewship credential folder create <name>`.")
				return
			}
			rows := make([][]string, 0, len(all))
			for _, fo := range all {
				remind := "-"
				if fo.RemindAfterDays > 0 {
					remind = fmt.Sprintf("%dd", fo.RemindAfterDays)
				}
				rows = append(rows, []string{folderPath(all, fo), fo.RevealPolicy, fmt.Sprintf("L%d", fo.MinSecurityLevel), remind, fmt.Sprint(fo.CredentialCount), fo.ID})
			}
			f.Table([]string{"FOLDER", "REVEAL", "MIN TIER", "REMIND", "SECRETS", "ID"}, rows)
		})
	},
}

var credFolderCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a Vault folder",
	Example: `  crewship credential folder create Production --reveal admins --min-security-level 2
  crewship credential folder create Payments --parent Production --remind-after-days 90`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		body, err := folderFlagsBody(cmd)
		if err != nil {
			return err
		}
		body["name"] = args[0]
		client := newAPIClient()
		if p, _ := cmd.Flags().GetString("parent"); p != "" {
			id, err := resolveCredentialFolderID(client, p)
			if err != nil {
				return err
			}
			body["parent_id"] = id
		}
		resp, err := client.Post("/api/v1/credential-folders", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out credFolderOut
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		return newFormatter().AutoHuman(out, func() {
			fmt.Printf("Created folder %s (%s).\n", out.Name, out.ID)
		})
	},
}

var credFolderUpdateCmd = &cobra.Command{
	Use:   "update <folder>",
	Short: "Rename a folder or change its policy",
	Long: `Update a folder by name, Parent/Child path or id. Changing the reveal policy
or the tier floor/default needs OWNER or ADMIN. --parent "" moves the folder to
the top level.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		body, err := folderFlagsBody(cmd)
		if err != nil {
			return err
		}
		client := newAPIClient()
		id, err := resolveCredentialFolderID(client, args[0])
		if err != nil {
			return err
		}
		if cmd.Flags().Changed("parent") {
			p, _ := cmd.Flags().GetString("parent")
			if p == "" {
				body["parent_id"] = nil
			} else {
				pid, err := resolveCredentialFolderID(client, p)
				if err != nil {
					return err
				}
				body["parent_id"] = pid
			}
		}
		if len(body) == 0 {
			return cli.WithExitCode(fmt.Errorf("nothing to change: pass --name, --parent, --description, --reveal, ..."), cli.ExitValidation)
		}
		resp, err := client.Patch("/api/v1/credential-folders/"+url.PathEscape(id), body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out credFolderOut
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		return newFormatter().AutoHuman(out, func() {
			fmt.Printf("Updated folder %s.\n", out.Name)
		})
	},
}

var credFolderDeleteCmd = &cobra.Command{
	Use:   "delete <folder>",
	Short: "Delete an empty Vault folder",
	Long:  "Delete a folder (OWNER or ADMIN). Only an empty folder is deleted: move its secrets and subfolders out first.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		id, err := resolveCredentialFolderID(client, args[0])
		if err != nil {
			return err
		}
		resp, err := client.Delete("/api/v1/credential-folders/" + url.PathEscape(id))
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Folder %s deleted.", args[0]))
		return nil
	},
}

func init() {
	credFolderCreateCmd.Flags().String("parent", "", "Parent folder (name, path or id); folders nest two levels")
	addFolderPolicyFlags(credFolderCreateCmd)
	credFolderUpdateCmd.Flags().String("name", "", "New name")
	credFolderUpdateCmd.Flags().String("parent", "", "New parent folder; pass an empty value to move to the top level")
	addFolderPolicyFlags(credFolderUpdateCmd)
	credFolderCmd.AddCommand(credFolderListCmd, credFolderCreateCmd, credFolderUpdateCmd, credFolderDeleteCmd)
	credentialCmd.AddCommand(credFolderCmd)
}
