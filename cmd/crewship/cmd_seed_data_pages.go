package main

// Seeding Pages (docs/prd/pages.md).
//
// A seeded workspace used to open its Pages section on nothing at all, which
// for this feature is worse than it sounds: the whole of what a page does is
// show something, so an empty section reads as "not built" rather than as "not
// used yet". Every other noun the seed creates — crews, agents, routines,
// issues — arrives with content.
//
// The seed creates the page AND pushes one payload per panel, in that order,
// because they are two different permissions and both have to work:
//
//   - creating needs `page.create`, which the seeding OWNER has;
//   - pushing needs producer authority (§7.1 rule 4), and the page's owner may
//     push to a panel whose producer is a `script` (pages_authz.go mayProduce).
//     That is why every demo panel declares one.
//
// A push that fails is reported and does not stop the seed. A page with four
// panels of data and one reading "never produced" is still a useful demo, and
// it is also an honest one: that is exactly what the reader would see if a real
// producer had stopped.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
)

func seedPages(ctx context.Context, client *cli.Client) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	wsID := client.GetWorkspaceID()
	if wsID == "" {
		return fmt.Errorf("seedPages: workspace_id not set on client")
	}

	fmt.Fprintln(os.Stderr, "Creating pages...")
	created, pushed, failed := 0, 0, 0

	for _, page := range seeddata.Pages {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := seedOnePage(client, wsID, page); err != nil {
			// One bad page must not cost the others. The seed is a demo, and a
			// partial demo beats an aborted one.
			fmt.Fprintf(os.Stderr, "  page %s: %v\n", page.Slug, err)
			failed++
			continue
		}
		created++
		for _, panel := range page.Panels {
			if panel.Demo == nil {
				continue
			}
			if err := seedPagePanelData(client, wsID, page.Slug, panel); err != nil {
				fmt.Fprintf(os.Stderr, "  %s/%s: %v\n", page.Slug, panel.ID, err)
				failed++
				continue
			}
			pushed++
		}
	}

	fmt.Fprintf(os.Stderr, "  %d page(s), %d panel payload(s)", created, pushed)
	if failed > 0 {
		fmt.Fprintf(os.Stderr, ", %d failed", failed)
	}
	fmt.Fprintln(os.Stderr)
	return nil
}

// seedOnePage POSTs the spec. The body is the PARSED spec (§11b.2) — the same
// shape `crewship page create --file` sends after parsing the YAML — with `sla`
// as an integer, because nothing on the wire carries the duration string.
func seedOnePage(client *cli.Client, wsID string, page seeddata.PageDef) error {
	panels := make([]map[string]any, 0, len(page.Panels))
	for _, p := range page.Panels {
		sla, err := seedPageSLASeconds(p.SLA)
		if err != nil {
			return fmt.Errorf("panel %s: %w", p.ID, err)
		}
		panel := map[string]any{
			"id":          p.ID,
			"schema":      p.Schema,
			"owner":       p.Owner,
			"producer":    p.Producer,
			"sla_seconds": sla,
		}
		if p.Title != "" {
			panel["title"] = p.Title
		}
		if p.Icon != "" {
			panel["icon"] = p.Icon
		}
		if p.Tab != "" {
			panel["tab"] = p.Tab
		}
		if p.Span != 0 {
			panel["span"] = p.Span
		}
		panels = append(panels, panel)
	}

	body := map[string]any{
		"slug":        page.Slug,
		"name":        page.Name,
		"description": page.Description,
		"panels":      panels,
	}
	resp, err := client.Post("/api/v1/pages?workspace_id="+wsID, body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	// 409 means the page is already there — re-running the seed is normal and
	// is not a failure.
	if resp.StatusCode == http.StatusConflict {
		return nil
	}
	if resp.StatusCode >= 400 {
		return seedPageError(resp)
	}
	return nil
}

// seedPagePanelData pushes one panel's demo payload through the same route
// every producer uses. Nothing about the seed is privileged here: it is the
// page's owner pushing to a script-produced panel.
func seedPagePanelData(client *cli.Client, wsID, slug string, panel seeddata.PagePanelDef) error {
	resp, err := client.Put(
		fmt.Sprintf("/api/v1/pages/%s/panels/%s/data?workspace_id=%s", slug, panel.ID, wsID),
		panel.Demo)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return seedPageError(resp)
	}
	return nil
}

// seedPageError returns the server's own sentence. A payload refused by a panel
// schema is named field by field, and repeating that beats inventing a shorter
// message that loses which field was wrong.
func seedPageError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &body)
	switch {
	case body.Error != "":
		return fmt.Errorf("%s: %s", resp.Status, body.Error)
	case body.Message != "":
		return fmt.Errorf("%s: %s", resp.Status, body.Message)
	default:
		return fmt.Errorf("%s: %s", resp.Status, string(raw))
	}
}

// seedPageSLASeconds converts the authored `sla: 60s` to the integer the wire
// carries. Written here rather than importing internal/pages so the seed data
// stays a document the CLI posts, not a spec it compiles.
func seedPageSLASeconds(sla string) (int, error) {
	d, err := time.ParseDuration(sla)
	if err != nil {
		return 0, fmt.Errorf("sla %q: %w", sla, err)
	}
	secs := int(d.Seconds())
	if secs <= 0 {
		return 0, fmt.Errorf("sla %q is not a positive duration", sla)
	}
	return secs, nil
}
