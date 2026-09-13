package main

// `crewship page folder list|create|show|update|delete|add|remove` and
// `crewship page move` — Pages folders from the command line (#2527;
// docs/prd/pages-collections-access-analysis-2026-09-12.md §6).
//
// One command per endpoint, which is the repo rule, plus `page move`, which is
// the operator's verb for the same two endpoints:
//
//	GET    /api/v1/page-folders                     page folder list
//	POST   /api/v1/page-folders                     page folder create <slug> --owner crew/<slug>
//	GET    /api/v1/page-folders/{slug}              page folder show   <slug>
//	PATCH  /api/v1/page-folders/{slug}              page folder update <slug> --name|--icon|--color
//	DELETE /api/v1/page-folders/{slug}              page folder delete <slug> --yes
//	POST   /api/v1/page-folders/{slug}/pages        page folder add    <folder> <page>   ≡ page move <page> --folder <folder>
//	DELETE /api/v1/page-folders/{slug}/pages/{page} page folder remove <folder> <page>   ≡ page move <page> --unfiled
//
// Two properties this file holds to:
//
//  1. IT READS THE FENCES ITSELF, RIGHT BEFORE IT WRITES. A move is confirmed
//     against the page's `pages_version` and the folder's `grants_version`
//     (§5/8). The command fetches both and sends what it read, so a 409 from
//     the server is a real race — somebody else moved the page or changed
//     the folder between the two calls — and never the CLI guessing zero.
//     There is deliberately no --pages-version flag: a fence typed by hand is
//     a fence that says nothing.
//
//  2. IT NEVER DECIDES ANYTHING. Who may create a folder, who may file a page
//     in it and who may take one out are server-side questions (§4), and the
//     refusal names who can. The CLI carries it through unedited.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// ── The wire (mirrors internal/api/pages_folders.go) ───────────────────────

type pageFolderJSON struct {
	ID            string `json:"id"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	Icon          string `json:"icon"`
	Color         string `json:"color"`
	Owner         string `json:"owner"`
	OwnerCrewName string `json:"owner_crew_name"`
	PageCount     int    `json:"page_count"`
	GrantsVersion int64  `json:"grants_version"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type pageFoldersJSON struct {
	Folders []pageFolderJSON `json:"folders"`
}

type pageFolderShowJSON struct {
	pageFolderJSON
	Pages []pageListRowJSON `json:"pages"`
}

// pageFolderWriteBody is create's body; update sends the subset that changed
// (pointers, so an empty string clears an icon and an omitted one keeps it).
type pageFolderWriteBody struct {
	Slug  string  `json:"slug,omitempty"`
	Name  *string `json:"name,omitempty"`
	Icon  *string `json:"icon,omitempty"`
	Color *string `json:"color,omitempty"`
	Owner string  `json:"owner,omitempty"`
}

type pageFolderMoveBody struct {
	Page          string `json:"page,omitempty"`
	PagesVersion  int64  `json:"pages_version"`
	GrantsVersion *int64 `json:"grants_version,omitempty"`
}

// pageMoveTargetJSON is what a move reads back from `GET /pages/{slug}`: the
// fence and where the page is now.
type pageMoveTargetJSON struct {
	Slug         string `json:"slug"`
	PagesVersion int64  `json:"pages_version"`
	Folder       *struct {
		Slug string `json:"slug"`
	} `json:"folder"`
}

type pageFolderMovedJSON struct {
	Page struct {
		Slug         string `json:"slug"`
		PagesVersion int64  `json:"pages_version"`
		Folder       *struct {
			Slug string `json:"slug"`
		} `json:"folder"`
	} `json:"page"`
}

// ── folder (group) ─────────────────────────────────────────────────────────

var pageFolderCmd = &cobra.Command{
	Use:   "folder",
	Short: "Group pages into crew-owned folders",
	Long: `A folder is a named group of pages owned by one crew, with an icon and a
colour from the crew's own picker. One level, a page in at most one folder;
pages outside any folder are unfiled.

Folders arrange, they do not share: filing a page in a folder changes nobody's
access to it. Who may see a page is still the page's own business (its owner,
its panels' crews, its grants), and a folder shows each viewer only the pages
in it they already reach.`,
}

// ── list ───────────────────────────────────────────────────────────────────

var pageFolderListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the folders you can see, with how many of their pages you reach",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := pageClient()
		if err != nil {
			return err
		}
		raw, err := pageFolderGet(client, "/api/v1/page-folders")
		if err != nil {
			return err
		}
		f := newFormatter()
		switch f.Format {
		case "json", "yaml", "ndjson":
			return pageEmitMachine(f, raw, `{"folders":[]}`)
		}
		var doc pageFoldersJSON
		if err := json.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if f.Format == "quiet" {
			for _, folder := range doc.Folders {
				fmt.Println(folder.Slug)
			}
			return nil
		}
		if len(doc.Folders) == 0 {
			fmt.Println("No folders yet. Create one: crewship page folder create <slug> --name <name> --owner crew/<slug>")
			return nil
		}
		rows := make([][]string, 0, len(doc.Folders))
		for _, folder := range doc.Folders {
			rows = append(rows, []string{folder.Slug, folder.Name, folder.Owner,
				fmt.Sprint(folder.PageCount), pageDash(folder.Icon), pageDash(folder.Color)})
		}
		f.Table([]string{"SLUG", "NAME", "OWNER", "PAGES", "ICON", "COLOR"}, rows)
		return nil
	},
}

// ── create ─────────────────────────────────────────────────────────────────

var pageFolderCreateCmd = &cobra.Command{
	Use:   "create <slug>",
	Short: "Create a folder owned by a crew",
	Long: `Create a folder. The owner is a crew, always, and the folder is the crew's from
birth: a workspace admin, or a MANAGER (or higher) who belongs to that crew,
may create it.

  crewship page folder create ops-board --name "Ops board" --owner crew/ops
  crewship page folder create ops-board --name "Ops board" --owner crew/ops --icon rocket --color amber

The icon is a crew icon name and the colour a crew palette key — the same
picker a crew uses — and the server refuses anything outside either set.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		owner := strings.TrimSpace(mustFlagString(cmd, "owner"))
		if owner == "" {
			return cli.WithExitCode(errors.New("--owner crew/<slug> is required: a folder is owned by a crew"), cli.ExitValidation)
		}
		if !strings.HasPrefix(owner, "crew/") {
			owner = "crew/" + owner
		}
		name := strings.TrimSpace(mustFlagString(cmd, "name"))
		if name == "" {
			name = args[0]
		}
		body := pageFolderWriteBody{Slug: args[0], Name: &name, Owner: owner}
		if cmd.Flags().Changed("icon") {
			icon := mustFlagString(cmd, "icon")
			body.Icon = &icon
		}
		if cmd.Flags().Changed("color") {
			color := mustFlagString(cmd, "color")
			body.Color = &color
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		resp, err := client.Post("/api/v1/page-folders", body)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		raw, err := pageFolderRead(resp)
		if err != nil {
			return err
		}
		f := newFormatter()
		switch f.Format {
		case "json", "yaml", "ndjson":
			return pageEmitMachine(f, raw, "{}")
		}
		var folder pageFolderJSON
		if err := json.Unmarshal(raw, &folder); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if f.Format == "quiet" {
			fmt.Println(folder.Slug)
			return nil
		}
		fmt.Printf("Created folder %s (%q, owned by %s).\n", folder.Slug, folder.Name, folder.Owner)
		fmt.Printf("File a page in it: crewship page move <page> --folder %s\n", folder.Slug)
		return nil
	},
}

// ── show ───────────────────────────────────────────────────────────────────

var pageFolderShowCmd = &cobra.Command{
	Use:   "show <slug>",
	Short: "Show a folder and the pages in it you reach",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := pageClient()
		if err != nil {
			return err
		}
		raw, err := pageFolderGet(client, "/api/v1/page-folders/"+pagePathEscape(args[0]))
		if err != nil {
			return err
		}
		f := newFormatter()
		switch f.Format {
		case "json", "yaml", "ndjson":
			return pageEmitMachine(f, raw, "{}")
		}
		var doc pageFolderShowJSON
		if err := json.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if f.Format == "quiet" {
			for _, p := range doc.Pages {
				fmt.Println(p.Slug)
			}
			return nil
		}
		fmt.Printf("%s  %s\n", doc.Slug, doc.Name)
		fmt.Printf("Owner: %s (%s)   Icon: %s   Color: %s   Pages you reach: %d\n",
			doc.Owner, pageDash(doc.OwnerCrewName), pageDash(doc.Icon), pageDash(doc.Color), doc.PageCount)
		if len(doc.Pages) == 0 {
			fmt.Println("No pages in this folder that you reach.")
			return nil
		}
		fmt.Println()
		rows := make([][]string, 0, len(doc.Pages))
		for _, p := range doc.Pages {
			rows = append(rows, []string{p.Slug, p.Name, p.Owner, pageDash(p.State), strings.Join(p.Reach, ",")})
		}
		f.Table([]string{"SLUG", "NAME", "OWNER", "STATE", "REACH"}, rows)
		return nil
	},
}

// ── update ─────────────────────────────────────────────────────────────────

var pageFolderUpdateCmd = &cobra.Command{
	Use:   "update <slug>",
	Short: "Rename a folder or change its icon or colour",
	Long: `Change a folder's name, icon or colour. Only the flags you pass are sent; an
icon or colour passed as "" is cleared. The slug and the owning crew are fixed
at creation.

  crewship page folder update ops-board --name "Operations"
  crewship page folder update ops-board --icon chart --color cyan
  crewship page folder update ops-board --icon ""`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var body pageFolderWriteBody
		if cmd.Flags().Changed("name") {
			name := mustFlagString(cmd, "name")
			body.Name = &name
		}
		if cmd.Flags().Changed("icon") {
			icon := mustFlagString(cmd, "icon")
			body.Icon = &icon
		}
		if cmd.Flags().Changed("color") {
			color := mustFlagString(cmd, "color")
			body.Color = &color
		}
		if body.Name == nil && body.Icon == nil && body.Color == nil {
			return cli.WithExitCode(errors.New("nothing to change: pass --name, --icon or --color"), cli.ExitValidation)
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		resp, err := client.Patch("/api/v1/page-folders/"+pagePathEscape(args[0]), body)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		raw, err := pageFolderRead(resp)
		if err != nil {
			return err
		}
		f := newFormatter()
		switch f.Format {
		case "json", "yaml", "ndjson":
			return pageEmitMachine(f, raw, "{}")
		}
		var folder pageFolderJSON
		if err := json.Unmarshal(raw, &folder); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if f.Format == "quiet" {
			fmt.Println(folder.Slug)
			return nil
		}
		fmt.Printf("Updated folder %s: %q, icon %s, color %s.\n", folder.Slug, folder.Name, pageDash(folder.Icon), pageDash(folder.Color))
		return nil
	},
}

// ── delete ─────────────────────────────────────────────────────────────────

var pageFolderDeleteCmd = &cobra.Command{
	Use:   "delete <slug>",
	Short: "Delete an empty folder",
	Long: `Delete a folder. Only an empty one: a folder still holding a page — including
one you cannot see — is refused with 409. Move the pages out first
("crewship page move <page> --unfiled"), then delete.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := confirmAction(cmd, fmt.Sprintf("Delete folder %q?", args[0])); err != nil {
			return err
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		resp, err := client.Delete("/api/v1/page-folders/" + pagePathEscape(args[0]))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := pageCheckError(resp); err != nil {
			return err
		}
		f := newFormatter()
		switch f.Format {
		case "json", "yaml", "ndjson":
			return pageEmitMachine(f, []byte(fmt.Sprintf(`{"deleted":%q}`, args[0])), "{}")
		case "quiet":
			return nil
		}
		fmt.Printf("Deleted folder %s.\n", args[0])
		return nil
	},
}

// ── add / remove ───────────────────────────────────────────────────────────

var pageFolderAddCmd = &cobra.Command{
	Use:   "add <folder> <page>",
	Short: "File a page in a folder (moving it out of any other)",
	Long: `File a page in a folder. The same thing as "crewship page move <page>
--folder <folder>". You need both halves of the authority: you initiate as the
page's owner or a workspace admin, and the folder accepts you as an admin or a
MANAGER (or higher) of its owning crew. Lacking either, the refusal names who
can.`,
	Args: cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		client, err := pageClient()
		if err != nil {
			return err
		}
		return pageFolderFile(client, args[0], args[1])
	},
}

var pageFolderRemoveCmd = &cobra.Command{
	Use:   "remove <folder> <page>",
	Short: "Take a page out of a folder (it becomes unfiled)",
	Long: `Take a page out of a folder. The same thing as "crewship page move <page>
--unfiled". The page's owner or a workspace admin may; the folder is not
consulted — taking your page back never waits for anyone.`,
	Args: cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		client, err := pageClient()
		if err != nil {
			return err
		}
		return pageFolderUnfile(client, args[0], args[1])
	},
}

// ── move ───────────────────────────────────────────────────────────────────

var pageMoveCmd = &cobra.Command{
	Use:   "move <page> --folder <slug> | --unfiled",
	Short: "Move a page into a folder, or out of the one it is in",
	Long: `Move a page.

  crewship page move fleet-201 --folder ops-board
  crewship page move fleet-201 --unfiled

--folder files the page there (moving it out of any other folder); --unfiled
takes it out of whichever folder it is in. Moving changes nobody's access to
the page.

The move is confirmed against the page and the folder as this command just
read them. A 409 means somebody else moved the page, or changed the folder,
between that read and the write: look again and re-run.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		folder := strings.TrimSpace(mustFlagString(cmd, "folder"))
		unfiled, _ := cmd.Flags().GetBool("unfiled")
		switch {
		case folder == "" && !unfiled:
			return cli.WithExitCode(errors.New("say where: --folder <slug> files the page there, --unfiled takes it out of its folder"), cli.ExitValidation)
		case folder != "" && unfiled:
			return cli.WithExitCode(errors.New("--folder and --unfiled were both given; a page ends up in one place"), cli.ExitValidation)
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		if unfiled {
			target, err := pageFolderReadPage(client, args[0])
			if err != nil {
				return err
			}
			if target.Folder == nil {
				f := newFormatter()
				switch f.Format {
				case "json", "yaml", "ndjson":
					return pageEmitMachine(f, []byte(fmt.Sprintf(`{"page":%q,"folder":null,"changed":false}`, args[0])), "{}")
				case "quiet":
					return nil
				}
				fmt.Printf("Page %s is already unfiled.\n", args[0])
				return nil
			}
			return pageFolderUnfile(client, target.Folder.Slug, args[0])
		}
		return pageFolderFile(client, folder, args[0])
	},
}

// ── Shared ─────────────────────────────────────────────────────────────────

// pageFolderFile reads the two fences and posts the move.
func pageFolderFile(client *cli.Client, folder, page string) error {
	target, err := pageFolderReadPage(client, page)
	if err != nil {
		return err
	}
	raw, err := pageFolderGet(client, "/api/v1/page-folders/"+pagePathEscape(folder))
	if err != nil {
		return err
	}
	var doc pageFolderJSON
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("decode folder: %w", err)
	}
	grants := doc.GrantsVersion
	resp, err := client.Post("/api/v1/page-folders/"+pagePathEscape(folder)+"/pages",
		pageFolderMoveBody{Page: page, PagesVersion: target.PagesVersion, GrantsVersion: &grants})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return pageFolderPrintMoved(resp, fmt.Sprintf("Filed page %s in folder %s", page, folder))
}

// pageFolderUnfile reads the page's fence and deletes its membership.
func pageFolderUnfile(client *cli.Client, folder, page string) error {
	target, err := pageFolderReadPage(client, page)
	if err != nil {
		return err
	}
	resp, err := client.Do(http.MethodDelete,
		"/api/v1/page-folders/"+pagePathEscape(folder)+"/pages/"+pagePathEscape(page),
		pageFolderMoveBody{PagesVersion: target.PagesVersion})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return pageFolderPrintMoved(resp, fmt.Sprintf("Took page %s out of folder %s", page, folder))
}

func pageFolderPrintMoved(resp *http.Response, did string) error {
	raw, err := pageFolderRead(resp)
	if err != nil {
		return err
	}
	f := newFormatter()
	switch f.Format {
	case "json", "yaml", "ndjson":
		return pageEmitMachine(f, raw, "{}")
	}
	var doc pageFolderMovedJSON
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if f.Format == "quiet" {
		fmt.Println(doc.Page.Slug)
		return nil
	}
	fmt.Printf("%s (pages_version %d).\n", did, doc.Page.PagesVersion)
	return nil
}

func pageFolderReadPage(client *cli.Client, page string) (*pageMoveTargetJSON, error) {
	raw, err := pageFolderGet(client, "/api/v1/pages/"+pagePathEscape(page))
	if err != nil {
		return nil, err
	}
	var target pageMoveTargetJSON
	if err := json.Unmarshal(raw, &target); err != nil {
		return nil, fmt.Errorf("decode page: %w", err)
	}
	return &target, nil
}

func pageFolderGet(client *cli.Client, path string) ([]byte, error) {
	resp, err := client.Get(path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return pageFolderRead(resp)
}

// pageFolderRead is pageCheckError plus the fence conflict: a 409 carrying
// `conflict` is rendered with the current pair, because "re-read and try
// again" is the one instruction the operator needs and the bare error text
// does not carry the numbers.
func pageFolderRead(resp *http.Response) ([]byte, error) {
	if resp.StatusCode == http.StatusConflict {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		var c struct {
			Error         string `json:"error"`
			Conflict      string `json:"conflict"`
			PagesVersion  int64  `json:"pages_version"`
			GrantsVersion int64  `json:"grants_version"`
		}
		if json.Unmarshal(raw, &c) == nil && c.Conflict != "" {
			return nil, cli.WithExitCode(fmt.Errorf("%s (now pages_version %d, grants_version %d); look again and re-run",
				c.Error, c.PagesVersion, c.GrantsVersion), cli.ExitConflict)
		}
		resp.Body = io.NopCloser(strings.NewReader(string(raw)))
	}
	if err := pageCheckError(resp); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	return raw, nil
}

func init() {
	pageFolderCreateCmd.Flags().String("name", "", "The folder's display name (defaults to the slug)")
	pageFolderCreateCmd.Flags().String("owner", "", "The owning crew, crew/<slug> (required)")
	for _, c := range []*cobra.Command{pageFolderCreateCmd, pageFolderUpdateCmd} {
		c.Flags().String("icon", "", "A crew icon name (the crew icon picker's set)")
		c.Flags().String("color", "", "A crew palette key: blue, emerald, violet, amber, rose, cyan, lime or fuchsia")
	}
	pageFolderUpdateCmd.Flags().String("name", "", "A new display name")
	pageFolderDeleteCmd.Flags().BoolP("yes", "y", false, "Skip the interactive confirmation prompt")
	pageMoveCmd.Flags().String("folder", "", "File the page in this folder")
	pageMoveCmd.Flags().Bool("unfiled", false, "Take the page out of the folder it is in")

	pageFolderCmd.AddCommand(pageFolderListCmd)
	pageFolderCmd.AddCommand(pageFolderCreateCmd)
	pageFolderCmd.AddCommand(pageFolderShowCmd)
	pageFolderCmd.AddCommand(pageFolderUpdateCmd)
	pageFolderCmd.AddCommand(pageFolderDeleteCmd)
	pageFolderCmd.AddCommand(pageFolderAddCmd)
	pageFolderCmd.AddCommand(pageFolderRemoveCmd)
	pageCmd.AddCommand(pageFolderCmd)
	pageCmd.AddCommand(pageMoveCmd)
}
