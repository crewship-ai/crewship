package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// pageUnshareCmd drives POST /api/v1/pages/{slug}/return-private (PX-11).
var pageUnshareCmd = &cobra.Command{
	Use:   "unshare <page>",
	Args:  cobra.ExactArgs(1),
	Short: "Return a shared artifact to your personal space after a reviewed dry run; same Page ID",
	Long: `Return a shared artifact to your personal space after a reviewed dry run.

The Page keeps its ID. Confirming sets its private audience, takes it out of
its folder, deletes every page grant, revokes every public link and every live
agent mandate on it, and records the change in the audit log. Former readers
are refused at once; workspace OWNER and ADMIN keep the audited read. Grants
are deleted, so sharing again starts from nothing. Copies someone already
downloaded, exported or read cannot be recalled.

Only the page's owner may do this. Crew-owned pages and types without a
private audience (Design, Slides, dashboards) are refused with 409. Supported:
Docs documents, workbooks, native Word and Sheet documents, HTML artifact
projects.

Run --dry-run with the exact pages_version first; it lists the accesses that
end and returns a review_token. Then confirm with --yes, the same
--pages-version and that --review-token. Any change in between returns 409 and
needs a new dry run.`,
	Example: "  crewship page unshare q3-report --dry-run --pages-version 4\n  crewship page unshare q3-report --yes --pages-version 4 --review-token <review-token>",
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := pageCatalogCLIReviewBody(cmd)
		if err != nil {
			return err
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		resp, err := client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/return-private", body)
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
		case "quiet":
			return nil
		}
		text, err := pageUnshareHuman(raw)
		if err != nil {
			return err
		}
		fmt.Print(text)
		return nil
	},
}

type pageUnsharePermissions struct {
	Read    bool `json:"read"`
	Comment bool `json:"comment"`
	Edit    bool `json:"edit"`
	Produce bool `json:"produce"`
	Propose bool `json:"propose"`
}

func (p pageUnsharePermissions) String() string {
	var out []string
	for _, c := range []struct {
		on   bool
		name string
	}{{p.Read, "read"}, {p.Comment, "comment"}, {p.Edit, "edit"}, {p.Produce, "produce"}, {p.Propose, "propose"}} {
		if c.on {
			out = append(out, c.name)
		}
	}
	if len(out) == 0 {
		return "none"
	}
	return strings.Join(out, ",")
}

// pageUnshareHuman renders the review so a person sees every access that ends
// before they confirm.
func pageUnshareHuman(raw []byte) (string, error) {
	var r struct {
		DryRun       bool   `json:"dry_run"`
		Page         string `json:"page"`
		Kind         string `json:"kind"`
		PagesVersion int64  `json:"pages_version"`
		FromFolder   *struct {
			Slug string `json:"slug"`
		} `json:"from_folder"`
		Changes []struct {
			SubjectType string                 `json:"subject_type"`
			SubjectID   string                 `json:"subject_id"`
			Label       string                 `json:"label"`
			Before      pageUnsharePermissions `json:"before"`
			After       pageUnsharePermissions `json:"after"`
		} `json:"changes"`
		RemovedGrants []struct {
			SubjectType string `json:"subject_type"`
			SubjectID   string `json:"subject_id"`
			Label       string `json:"label"`
			Level       string `json:"level"`
		} `json:"removed_grants"`
		RevokedPublicLinks []struct {
			ID string `json:"id"`
		} `json:"revoked_public_links"`
		RevokedMandates int64  `json:"revoked_mandates"`
		Notice          string `json:"notice"`
		Changed         bool   `json:"changed"`
		ReviewToken     string `json:"review_token"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	var b strings.Builder
	if r.DryRun {
		fmt.Fprintf(&b, "Dry run: returning %s (%s) to your personal space would change:\n", r.Page, r.Kind)
	} else if r.Changed {
		fmt.Fprintf(&b, "Returned %s (%s) to your personal space. Removed:\n", r.Page, r.Kind)
	} else {
		fmt.Fprintf(&b, "%s: nothing changed.\n", r.Page)
	}
	if r.FromFolder != nil {
		fmt.Fprintf(&b, "  folder: leaves %s\n", r.FromFolder.Slug)
	}
	if len(r.Changes) == 0 {
		b.WriteString("  access: no subject loses access\n")
	}
	for _, c := range r.Changes {
		who := c.Label
		if who == "" {
			who = c.SubjectID
		}
		fmt.Fprintf(&b, "  access: %s %s %s -> %s\n", c.SubjectType, who, c.Before, c.After)
	}
	for _, g := range r.RemovedGrants {
		who := g.Label
		if who == "" {
			who = g.SubjectID
		}
		fmt.Fprintf(&b, "  grant: %s %s %s\n", g.SubjectType, who, g.Level)
	}
	for _, l := range r.RevokedPublicLinks {
		fmt.Fprintf(&b, "  public link: %s\n", l.ID)
	}
	fmt.Fprintf(&b, "  agent mandates: %d\n", r.RevokedMandates)
	if r.Notice != "" {
		fmt.Fprintf(&b, "Note: %s.\n", r.Notice)
	}
	if r.DryRun && r.ReviewToken != "" {
		fmt.Fprintf(&b, "Confirm with: crewship page unshare %s --yes --pages-version %d --review-token %s\n", r.Page, r.PagesVersion, r.ReviewToken)
	}
	return b.String(), nil
}

func init() {
	addPageCatalogReviewFlags(pageUnshareCmd)
	pageCmd.AddCommand(pageUnshareCmd)
}
