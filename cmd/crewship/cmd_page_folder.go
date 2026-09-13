package main

// `crewship page folder list|create|show|update|delete|add|remove|acl|share|unshare`
// and `crewship page move` — Pages folders from the command line (#2527,
// #2533; docs/prd/pages-collections-access-analysis-2026-09-12.md §6 and
// docs/prd/pages-folder-permissions-linux-model-2026-09-13.md §6).
//
// One command per endpoint, which is the repo rule, plus `page move`, which is
// the operator's verb for the membership endpoints:
//
//	GET    /api/v1/page-folders                     page folder list
//	POST   /api/v1/page-folders                     page folder create <slug> --owner crew/<slug>
//	GET    /api/v1/page-folders/{slug}              page folder show   <slug>
//	PATCH  /api/v1/page-folders/{slug}              page folder update <slug> --name|--icon|--color
//	DELETE /api/v1/page-folders/{slug}              page folder delete <slug> --yes
//	POST   /api/v1/page-folders/{slug}/pages        page folder add    <folder> <page>   ≡ page move <page> --folder <folder>
//	POST   /api/v1/page-folders/{slug}/pages:batch  page move <page> <page>... --folder <folder>
//	DELETE /api/v1/page-folders/{slug}/pages/{page} page folder remove <folder> <page>   ≡ page move <page> --unfiled
//	GET    /api/v1/page-folders/{slug}/acl          page folder acl     <slug>
//	PUT    /api/v1/page-folders/{slug}/acl          page folder share   <slug> <subject> --view|--edit
//	DELETE /api/v1/page-folders/{slug}/acl/{t}/{id} page folder unshare <slug> <subject>
//
// A subject is `user:<email or id>`, `crew:<slug>` or `workspace` (everyone
// in the workspace). Never an agent: an agent reaches a page only by a grant
// on that page.
//
// Two properties this file holds to:
//
//  1. IT READS THE FENCES ITSELF, RIGHT BEFORE IT WRITES. A move is confirmed
//     against the page's `pages_version` and the folder's `acl_version`
//     (§5/8, §3/8). The command fetches both and sends what it read, so a 409
//     from the server is a real race — somebody else moved the page or
//     changed the folder's permissions between the two calls — and never the
//     CLI guessing zero. There is deliberately no --pages-version flag: a
//     fence typed by hand is a fence that says nothing.
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
	Shared        string `json:"shared"`
	ACLVersion    int64  `json:"acl_version"`
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
	Page         string `json:"page,omitempty"`
	PagesVersion int64  `json:"pages_version"`
	ACLVersion   *int64 `json:"acl_version,omitempty"`
}

type pageFolderBatchMoveBody struct {
	Pages []struct {
		Page         string `json:"page"`
		PagesVersion int64  `json:"pages_version"`
	} `json:"pages"`
	ACLVersion int64 `json:"acl_version"`
}

// pageFolderACLEntryJSON mirrors internal/api/pages_folder_acl.go.
type pageFolderACLEntryJSON struct {
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id"`
	Label       string `json:"label"`
	CanRead     bool   `json:"can_read"`
	CanWrite    bool   `json:"can_write"`
	SetBy       string `json:"set_by"`
	SetAt       string `json:"set_at"`
}

type pageFolderACLJSON struct {
	Folder     string                   `json:"folder"`
	ACL        []pageFolderACLEntryJSON `json:"acl"`
	ACLVersion int64                    `json:"acl_version"`
}

type pageFolderACLWriteBody struct {
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id,omitempty"`
	CanWrite    bool   `json:"can_write"`
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

A folder can be shared — with a person, a crew, or everyone in the workspace
("page folder share") — and what it is shared with applies to every page in
it right now: filing a page in a shared folder shares it, taking it out
unshares it. "Can view" opens the pages; "can edit" also renames the folder,
edits the pages inside and takes pages out of it. A panel owned by another
crew stays sealed whatever the folder says. Only the owning crew's managers
and workspace admins see or change a folder's sharing; everyone else sees a
label (none, crew, workspace) and their own access ("page access --me").`,
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
				fmt.Sprint(folder.PageCount), pageDash(folder.Shared), pageDash(folder.Icon), pageDash(folder.Color)})
		}
		f.Table([]string{"SLUG", "NAME", "OWNER", "PAGES", "SHARED", "ICON", "COLOR"}, rows)
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
		fmt.Printf("Owner: %s (%s)   Shared: %s   Icon: %s   Color: %s   Pages you reach: %d\n",
			doc.Owner, pageDash(doc.OwnerCrewName), pageDash(doc.Shared), pageDash(doc.Icon), pageDash(doc.Color), doc.PageCount)
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
page's owner or a workspace admin, and the folder accepts you as an admin, a
MANAGER (or higher) of its owning crew, or somebody its sharing lets edit it.
Lacking either, the refusal names who can. Filing a page in a shared folder
shares it with whoever the folder is shared with.`,
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
--unfiled". The page's owner, a workspace admin, or somebody the folder's
sharing lets edit it may — the last on purpose: taking a page out withdraws
the access everyone else inherited from the folder.`,
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
	Use:   "move <page>... --folder <slug> | --unfiled",
	Short: "Move pages into a folder, or out of the ones they are in",
	Long: `Move one page, or several.

  crewship page move fleet-201 --folder ops-board
  crewship page move fleet-201 fleet-202 fleet-203 --folder ops-board
  crewship page move fleet-201 --unfiled

--folder files the pages there (moving them out of any other folder);
--unfiled takes each out of whichever folder it is in. Moving a page into a
shared folder shares it with whoever the folder is shared with; moving it out
unshares it. The dialog in the app shows who will see the page afterwards;
here, read "page folder show <slug>" first.

Several pages with --folder are one request and one transaction: every page is
checked on its own, and one refusal moves nothing and names the page.

The move is confirmed against each page and the folder's permissions as this
command just read them. A 409 means somebody else moved a page, or changed the
folder's sharing, between that read and the write: look again and re-run.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		folder := strings.TrimSpace(mustFlagString(cmd, "folder"))
		unfiled, _ := cmd.Flags().GetBool("unfiled")
		switch {
		case folder == "" && !unfiled:
			return cli.WithExitCode(errors.New("say where: --folder <slug> files the pages there, --unfiled takes them out of their folders"), cli.ExitValidation)
		case folder != "" && unfiled:
			return cli.WithExitCode(errors.New("--folder and --unfiled were both given; a page ends up in one place"), cli.ExitValidation)
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		if unfiled {
			for _, page := range args {
				target, err := pageFolderReadPage(client, page)
				if err != nil {
					return err
				}
				if target.Folder == nil {
					f := newFormatter()
					switch f.Format {
					case "json", "yaml", "ndjson":
						if err := pageEmitMachine(f, []byte(fmt.Sprintf(`{"page":%q,"folder":null,"changed":false}`, page)), "{}"); err != nil {
							return err
						}
					case "quiet":
					default:
						fmt.Printf("Page %s is already unfiled.\n", page)
					}
					continue
				}
				if err := pageFolderUnfile(client, target.Folder.Slug, page); err != nil {
					return err
				}
			}
			return nil
		}
		if len(args) == 1 {
			return pageFolderFile(client, folder, args[0])
		}
		return pageFolderFileMany(client, folder, args)
	},
}

// ── acl / share / unshare ──────────────────────────────────────────────────

var pageFolderACLCmd = &cobra.Command{
	Use:   "acl <slug>",
	Short: "Show who a folder is shared with (owning crew's managers and admins only)",
	Long: `Show a folder's permissions: each subject, whether it can view or edit, who
set it and when. Only a workspace admin or a MANAGER (or higher) of the owning
crew may read this; everyone else sees the folder's sharing label in
"page folder show" and their own access in "page access <slug> --me".

The owning crew and workspace admins are not listed: they hold the folder
without an entry, and an entry is never needed for them.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := pageClient()
		if err != nil {
			return err
		}
		raw, err := pageFolderGet(client, "/api/v1/page-folders/"+pagePathEscape(args[0])+"/acl")
		if err != nil {
			return err
		}
		return pageFolderPrintACL(raw, "")
	},
}

var pageFolderShareCmd = &cobra.Command{
	Use:   "share <slug> <subject> --view | --edit",
	Short: "Share a folder with a user, a crew or everyone in the workspace",
	Long: `Share a folder, and with it every page that is in it now or later.

  crewship page folder share ops-board user:ada@example.com --view
  crewship page folder share ops-board crew:support --edit
  crewship page folder share ops-board workspace --view

--view: can open the folder and the pages in it. --edit: can also rename the
folder, edit the pages inside, and take pages out of it (which withdraws the
access everyone else inherited). Edit always includes view. Neither ever
opens a panel owned by another crew, moves a page elsewhere, deletes the
folder or changes its sharing.

A subject is user:<email or id>, crew:<slug>, or the word workspace for every
current member. Never an agent: an agent reaches a page only by
"crewship page grant --agent" on that page. Only a workspace admin or a
MANAGER (or higher) of the owning crew may run this. The entry belongs to the
folder and stays when whoever set it leaves.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		view, _ := cmd.Flags().GetBool("view")
		edit, _ := cmd.Flags().GetBool("edit")
		switch {
		case !view && !edit:
			return cli.WithExitCode(errors.New("say which: --view (can open the pages) or --edit (can also rename the folder, edit the pages and take pages out)"), cli.ExitValidation)
		case view && edit:
			return cli.WithExitCode(errors.New("--view and --edit were both given; --edit includes --view, so pass one"), cli.ExitValidation)
		}
		subjectType, subjectID, err := pageFolderSubject(args[1])
		if err != nil {
			return err
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		resp, err := client.Put("/api/v1/page-folders/"+pagePathEscape(args[0])+"/acl",
			pageFolderACLWriteBody{SubjectType: subjectType, SubjectID: subjectID, CanWrite: edit})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		raw, err := pageFolderRead(resp)
		if err != nil {
			return err
		}
		right := "can view"
		if edit {
			right = "can view and edit"
		}
		return pageFolderPrintACL(raw, fmt.Sprintf("Shared folder %s with %s: %s.", args[0], pageFolderSubjectLabel(subjectType, subjectID), right))
	},
}

var pageFolderUnshareCmd = &cobra.Command{
	Use:   "unshare <slug> <subject>",
	Short: "Stop sharing a folder with a user, a crew or everyone in the workspace",
	Long: `Remove one entry from a folder's permissions. The same subject spelling as
"share": user:<email or id>, crew:<slug>, or workspace. A user who has since
left is removed by the id or email the entry was set under, which
"page folder acl" prints.

  crewship page folder unshare ops-board crew:support
  crewship page folder unshare ops-board workspace`,
	Args: cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		subjectType, subjectID, err := pageFolderSubject(args[1])
		if err != nil {
			return err
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		// The workspace subject has no id; the path spells the type twice.
		pathID := subjectID
		if subjectType == "workspace" {
			pathID = "workspace"
		}
		resp, err := client.Delete("/api/v1/page-folders/" + pagePathEscape(args[0]) + "/acl/" + subjectType + "/" + pagePathEscape(pathID))
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
			return pageEmitMachine(f, []byte(fmt.Sprintf(`{"folder":%q,"removed":{"subject_type":%q,"subject_id":%q}}`, args[0], subjectType, subjectID)), "{}")
		case "quiet":
			return nil
		}
		fmt.Printf("Folder %s is no longer shared with %s.\n", args[0], pageFolderSubjectLabel(subjectType, subjectID))
		return nil
	},
}

// pageFolderSubject parses `user:<ref>`, `crew:<ref>` or `workspace`. An
// agent is refused here with the reason, before any request.
func pageFolderSubject(raw string) (subjectType, subjectID string, err error) {
	raw = strings.TrimSpace(raw)
	if strings.EqualFold(raw, "workspace") {
		return "workspace", "", nil
	}
	kind, ref, found := strings.Cut(raw, ":")
	kind = strings.ToLower(strings.TrimSpace(kind))
	ref = strings.TrimSpace(ref)
	switch kind {
	case "user", "crew":
		if !found || ref == "" {
			return "", "", cli.WithExitCode(fmt.Errorf("%q: a subject is user:<email or id>, crew:<slug> or workspace", raw), cli.ExitValidation)
		}
		return kind, ref, nil
	case "agent":
		return "", "", cli.WithExitCode(errors.New(
			"a folder is never shared with an agent; an agent reaches a page only by \"crewship page grant --agent\" on that page"), cli.ExitValidation)
	default:
		return "", "", cli.WithExitCode(fmt.Errorf("%q: a subject is user:<email or id>, crew:<slug> or workspace", raw), cli.ExitValidation)
	}
}

func pageFolderSubjectLabel(subjectType, subjectID string) string {
	if subjectType == "workspace" {
		return "everyone in the workspace"
	}
	return subjectType + ":" + subjectID
}

func pageFolderPrintACL(raw []byte, did string) error {
	f := newFormatter()
	switch f.Format {
	case "json", "yaml", "ndjson":
		return pageEmitMachine(f, raw, "{}")
	}
	var doc pageFolderACLJSON
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if f.Format == "quiet" {
		for _, e := range doc.ACL {
			fmt.Println(pageFolderSubjectLabel(e.SubjectType, e.Label))
		}
		return nil
	}
	if did != "" {
		fmt.Println(did)
	}
	if len(doc.ACL) == 0 {
		fmt.Printf("Folder %s is shared with nobody: its owning crew and workspace admins reach it. (acl_version %d)\n", doc.Folder, doc.ACLVersion)
		return nil
	}
	rows := make([][]string, 0, len(doc.ACL))
	for _, e := range doc.ACL {
		right := "can view"
		if e.CanWrite {
			right = "can view and edit"
		}
		rows = append(rows, []string{pageFolderSubjectLabel(e.SubjectType, e.Label), right, pageDash(e.SetBy), e.SetAt})
	}
	f.Table([]string{"SUBJECT", "ACCESS", "SET BY", "SET AT"}, rows)
	fmt.Printf("acl_version %d — the fence a move is confirmed against.\n", doc.ACLVersion)
	return nil
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
	acl := doc.ACLVersion
	resp, err := client.Post("/api/v1/page-folders/"+pagePathEscape(folder)+"/pages",
		pageFolderMoveBody{Page: page, PagesVersion: target.PagesVersion, ACLVersion: &acl})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return pageFolderPrintMoved(resp, fmt.Sprintf("Filed page %s in folder %s", page, folder))
}

// pageFolderFileMany reads every page's fence and the folder's, then posts
// the batch: one request, one transaction, all or nothing.
func pageFolderFileMany(client *cli.Client, folder string, pages []string) error {
	var body pageFolderBatchMoveBody
	for _, page := range pages {
		target, err := pageFolderReadPage(client, page)
		if err != nil {
			return err
		}
		body.Pages = append(body.Pages, struct {
			Page         string `json:"page"`
			PagesVersion int64  `json:"pages_version"`
		}{Page: page, PagesVersion: target.PagesVersion})
	}
	raw, err := pageFolderGet(client, "/api/v1/page-folders/"+pagePathEscape(folder))
	if err != nil {
		return err
	}
	var doc pageFolderJSON
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("decode folder: %w", err)
	}
	body.ACLVersion = doc.ACLVersion
	resp, err := client.Post("/api/v1/page-folders/"+pagePathEscape(folder)+"/pages:batch", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err = pageFolderRead(resp)
	if err != nil {
		return err
	}
	f := newFormatter()
	switch f.Format {
	case "json", "yaml", "ndjson":
		return pageEmitMachine(f, raw, "{}")
	}
	var moved struct {
		Pages []struct {
			Slug         string `json:"slug"`
			PagesVersion int64  `json:"pages_version"`
		} `json:"pages"`
	}
	if err := json.Unmarshal(raw, &moved); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if f.Format == "quiet" {
		for _, p := range moved.Pages {
			fmt.Println(p.Slug)
		}
		return nil
	}
	fmt.Printf("Filed %d pages in folder %s:\n", len(moved.Pages), folder)
	for _, p := range moved.Pages {
		fmt.Printf("  %s (pages_version %d)\n", p.Slug, p.PagesVersion)
	}
	return nil
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
			Error        string `json:"error"`
			Conflict     string `json:"conflict"`
			Page         string `json:"page"`
			PagesVersion int64  `json:"pages_version"`
			ACLVersion   int64  `json:"acl_version"`
		}
		if json.Unmarshal(raw, &c) == nil && c.Conflict != "" {
			page := ""
			if c.Page != "" {
				page = " for page " + c.Page
			}
			return nil, cli.WithExitCode(fmt.Errorf("%s (now pages_version %d%s, acl_version %d); look again and re-run",
				c.Error, c.PagesVersion, page, c.ACLVersion), cli.ExitConflict)
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
	pageMoveCmd.Flags().String("folder", "", "File the pages in this folder (several pages: one all-or-nothing request)")
	pageMoveCmd.Flags().Bool("unfiled", false, "Take each page out of the folder it is in")
	pageFolderShareCmd.Flags().Bool("view", false, "Can open the folder and the pages in it")
	pageFolderShareCmd.Flags().Bool("edit", false, "Can also rename the folder, edit the pages inside and take pages out (includes --view)")

	pageFolderCmd.AddCommand(pageFolderListCmd)
	pageFolderCmd.AddCommand(pageFolderCreateCmd)
	pageFolderCmd.AddCommand(pageFolderShowCmd)
	pageFolderCmd.AddCommand(pageFolderUpdateCmd)
	pageFolderCmd.AddCommand(pageFolderDeleteCmd)
	pageFolderCmd.AddCommand(pageFolderAddCmd)
	pageFolderCmd.AddCommand(pageFolderRemoveCmd)
	pageFolderCmd.AddCommand(pageFolderACLCmd)
	pageFolderCmd.AddCommand(pageFolderShareCmd)
	pageFolderCmd.AddCommand(pageFolderUnshareCmd)
	pageCmd.AddCommand(pageFolderCmd)
	pageCmd.AddCommand(pageMoveCmd)
}
