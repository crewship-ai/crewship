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
// A panel is more than id/schema/owner/producer/sla now: seedPagePanelBody also
// forwards `wake:`, `on_failure:`, `actions:` and `public:` verbatim whenever
// the catalogue declares them. That is what lets the seeded page ship with a
// real gate, a routed failure, a working button and a publishable panel rather
// than five panels of static data — and it is why the catalogue holds those
// fields as `any`: the server owns their grammar and judges them.
//
// A push that fails is reported and does not stop the seed. A page with four
// panels of data and one reading "never produced" is still a useful demo, and
// it is also an honest one: that is exactly what the reader would see if a real
// producer had stopped.
//
// The catalogue also carries panels the seeder CANNOT fill this way. A panel
// declaring `routine/<slug>` is refused an owner push (pages_authz.go
// mayProduce admits one only for `script` and `webhook`), so those panels carry
// no demo payload and are filled instead by firing their producer — see
// seedPageProducerRoutines. That is the second half of the same demo: one page
// showing what a producer pushed, one showing the loop that pushes it.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/pages"
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

	return seedPageProducerRoutines(ctx, client, wsID)
}

// seedPageProducerRoutines fires one run of every routine a seeded panel names
// as its producer.
//
// This is not a convenience. A panel declaring `routine/<slug>` is one the
// seeding owner may not write — mayProduce (pages_authz.go) admits an owner
// push for `script` and `webhook` and refuses every other kind — so a routine-
// produced panel cannot carry a `demo` payload the way the rest of the
// catalogue does. Its only path to holding data is its own producer running,
// and at seed time nothing else makes that happen: the seed deliberately
// creates no schedules, so without this the page would open on the
// never-produced em dash, which is the state that means "nobody wired this up".
//
// The slugs are derived from the pages rather than listed here, so a panel that
// changes producer is followed automatically and a routine that is renamed
// fails loudly at run time instead of being silently skipped.
//
// The run is asynchronous — Run spawns it and answers — so the panels fill a
// moment after the seed prints its last line. Nothing here waits for that: the
// seeder's job is to make the run happen, and a run that fails has a run record
// saying so, which is a better artefact than a seed that blocks on a poll.
func seedPageProducerRoutines(ctx context.Context, client *cli.Client, wsID string) error {
	slugs := pageProducerRoutineSlugs(seeddata.Pages)
	if len(slugs) == 0 {
		return nil
	}

	fmt.Fprintln(os.Stderr, "Running page producer routines...")
	started, failed := 0, 0
	for _, slug := range slugs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := seedRunRoutine(client, wsID, slug); err != nil {
			// Same rule as a failed push: report and carry on. A demo missing
			// one page beats a seed that aborted after creating half a
			// workspace.
			fmt.Fprintf(os.Stderr, "  routine %s: %v\n", slug, err)
			failed++
			continue
		}
		started++
	}

	fmt.Fprintf(os.Stderr, "  %d run(s) started", started)
	if failed > 0 {
		fmt.Fprintf(os.Stderr, ", %d failed", failed)
	}
	fmt.Fprintln(os.Stderr)
	return nil
}

// pageProducerRoutineSlugs collects the distinct routine slugs named by the
// panels' `producer:` fields, in first-seen order.
//
// Deduplicated because one routine writing several panels of a page is the
// normal shape — page-watch writes both of Hlídka's — and firing its run once
// per panel would bill the same work twice and race two runs at one page.
// Order is preserved rather than sorted so the seed's output reads in the order
// the catalogue is authored.
func pageProducerRoutineSlugs(catalogue []seeddata.PageDef) []string {
	var slugs []string
	seen := map[string]bool{}
	for _, page := range catalogue {
		for _, panel := range page.Panels {
			kind, slug := seedPanelProducer(panel)
			if kind != pages.ProducerRoutine || slug == "" || seen[slug] {
				continue
			}
			seen[slug] = true
			slugs = append(slugs, slug)
		}
	}
	return slugs
}

// seedPanelProducer splits a seeded panel's `producer:` into its kind and ref,
// returning the zero kind for anything malformed.
//
// It does not borrow pages.PanelSpec.ProducerParts, for the same reason
// seedPageSLASeconds does not import the duration parser: the seed catalogue is
// a document the CLI posts, not a spec it compiles, and the server is what
// judges it. What this needs is the one question the seeder has to answer
// before it posts anything — is this a panel I can fill, or one I have to run
// something to fill — and a malformed producer is not that question's problem.
// The server refuses it by name, and TestSeedPages_EverySpecFieldIsWellFormed
// refuses it before anyone gets that far.
func seedPanelProducer(panel seeddata.PagePanelDef) (pages.ProducerKind, string) {
	kind, ref, ok := strings.Cut(strings.TrimSpace(panel.Producer), "/")
	if !ok {
		return "", ""
	}
	return pages.ProducerKind(kind), strings.TrimSpace(ref)
}

// seedRunRoutine triggers one run through the same endpoint the UI's Run button
// uses. An empty body is deliberate: every input on a seeded routine carries a
// default, and passing none is what a demoer pressing Run would send.
func seedRunRoutine(client *cli.Client, wsID, slug string) error {
	resp, err := client.Post(
		fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/run", wsID, slug),
		map[string]any{})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return seedPageError(resp)
	}
	return nil
}

// seedOnePage POSTs the spec. The body is the PARSED spec (§11b.2) — the same
// shape `crewship page create --file` sends after parsing the YAML — with `sla`
// as an integer, because nothing on the wire carries the duration string.
func seedOnePage(client *cli.Client, wsID string, page seeddata.PageDef) error {
	panels := make([]map[string]any, 0, len(page.Panels))
	for _, p := range page.Panels {
		panel, err := seedPagePanelBody(p)
		if err != nil {
			return err
		}
		panels = append(panels, panel)
	}

	body := map[string]any{
		"slug":        page.Slug,
		"name":        page.Name,
		"description": page.Description,
		"panels":      panels,
	}
	// Omitted rather than sent empty: the server reads `owner` as "hand this to
	// somebody", and an empty string is not a somebody. Absent is what selects
	// the default, which is that the creator keeps it.
	if page.Owner != "" {
		body["owner"] = page.Owner
	}
	resp, err := client.Post("/api/v1/pages?workspace_id="+wsID, body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	// 409 means the page is already there, which is the normal state of any
	// workspace that has been seeded before. It used to end here, and that was
	// a quiet hole: a panel added to the catalogue, a tab declared, a producer
	// changed — none of it ever reached a workspace that already had the page,
	// and the seed reported success either way. Re-apply the spec instead, so
	// the catalogue in this repo is what a re-seeded workspace actually holds.
	//
	// `owner` is deliberately NOT re-sent on this path. Ownership is decided at
	// creation and moving it afterwards is a transfer with its own rules; a
	// re-seed that silently reassigned a page somebody had since handed to
	// another crew would be a permission change nobody asked for.
	if resp.StatusCode == http.StatusConflict {
		return seedUpdateOnePage(client, wsID, page.Slug, body)
	}
	if resp.StatusCode >= 400 {
		return seedPageError(resp)
	}
	return nil
}

// seedUpdateOnePage re-applies a spec onto a page that already exists.
//
// PATCH rather than delete-and-recreate: a page carries grants, webhooks and a
// payload ring that a delete would take with it, and re-seeding a demo is not a
// reason to destroy any of them.
func seedUpdateOnePage(client *cli.Client, wsID, slug string, body map[string]any) error {
	delete(body, "owner")
	resp, err := client.Patch(
		fmt.Sprintf("/api/v1/pages/%s?workspace_id=%s", url.PathEscape(slug), wsID), body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return seedPageError(resp)
	}
	return nil
}

// seedPagePanelBody converts one authored panel into the body the create route
// takes.
//
// Extracted from seedOnePage so a test can assert on it directly. That is not
// tidiness: the conversion is field by field rather than a marshal — the wire
// carries `sla_seconds` where the document carries `sla` — so every field added
// to the catalogue has to be added here too, and the failure when it is not is
// silent. The page saves, the panels render, and whatever was dropped simply
// does not exist in the workspace.
func seedPagePanelBody(p seeddata.PagePanelDef) (map[string]any, error) {
	sla, err := seedPageSLASeconds(p.SLA)
	if err != nil {
		return nil, fmt.Errorf("panel %s: %w", p.ID, err)
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
	// The authored half. Sent verbatim: the server parses `when` against the
	// panel's schema, resolves the action's routine and applies the publication
	// rules, and a seeder that pre-judged any of it would be a second grammar to
	// keep in step. Omitted when absent so a panel that declares no gate does
	// not post an empty one — `wake: []` and no wake at all are different
	// documents.
	if p.Wake != nil {
		panel["wake"] = p.Wake
	}
	if p.OnFailure != nil {
		panel["on_failure"] = p.OnFailure
	}
	if p.Actions != nil {
		panel["actions"] = p.Actions
	}
	if p.Public {
		panel["public"] = true
	}
	return panel, nil
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
