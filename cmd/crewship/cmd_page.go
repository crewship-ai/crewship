package main

// `crewship page` — the Pages CLI (docs/prd/pages.md §11).
//
// One command per endpoint, which is the repo rule and the contract agents
// actually read:
//
//	GET    /api/v1/pages                          page list
//	GET    /api/v1/pages/{slug}                   page get <slug>
//	POST   /api/v1/pages                          page create --file <yaml>
//	PATCH  /api/v1/pages/{slug}                   page update <slug> --file <yaml>
//	DELETE /api/v1/pages/{slug}                   page delete <slug> --yes
//	PUT    /api/v1/pages/{slug}/panels/{id}/data  page set <slug>/<panel> --data -
//
// Two things this file deliberately does NOT do:
//
//  1. It never sends provenance. There is no flag, no field and no branch here
//     through which a producer could claim a run id, a timestamp or an
//     identity — §4 rule 5 makes those server-attached and §7.1b makes agent
//     identity a property of the token. `page set` sends exactly the JSON it
//     read, and nothing else.
//
//  2. It never invents freshness. `fresh` / `stale` / `failed` /
//     `never_produced` are the server's verdict (§4 rule 2, §11b decision 8);
//     the CLI repeats the word it was given and shows the ABSOLUTE timestamp
//     next to it (§4 rule 3 — "age shown in absolute terms, not 'a while
//     ago'"). A stale panel that reads like a fresh one is the exact failure
//     the freshness contract exists to prevent.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/spf13/cobra"
)

var pageCmd = &cobra.Command{
	Use:   "page",
	Short: "Author and feed Pages — panel dashboards fed by producers, not queries",
	Long: `A Page is a slug-addressable board of panels. It holds no query, no
datasource and no credentials: it renders the last payload a producer
pushed, plus the provenance the server attached to that push.

Authoring is a YAML document (kind: Page); feeding it is one command:

  crewship page create --file fleet.page.yaml
  crewship page list
  crewship page get fleet-201
  crewship page get fleet-201 --format json
  echo '{"items":[{"name":"api","state":"ok"}]}' | crewship page set fleet-201/sluzby --data -
  crewship page update fleet-201 --file fleet.page.yaml
  crewship page delete fleet-201 --yes

Provenance — producer, run id, timestamp — is attached server-side and
appears in every panel footer. Freshness (fresh / stale / failed /
never_produced) is computed server-side from the panel's declared SLA.`,
}

// ── The wire (mirrors internal/api/pages_handler.go) ───────────────────────

type pageProvenanceJSON struct {
	Producer   string `json:"producer"`
	RunID      string `json:"run_id"`
	ProducedAt string `json:"produced_at"`
}

type pagePanelJSON struct {
	ID string `json:"id"`
	// §11b decision 14: a panel the caller may not see arrives as exactly
	// {panel_id, span, sealed, owner_crew_name}. The renderer keys on `sealed`
	// being PRESENT rather than on a field being absent, so a serialisation
	// bug can never be mistaken for a permission decision.
	PanelID       string `json:"panel_id"`
	Sealed        bool   `json:"sealed"`
	OwnerCrewName string `json:"owner_crew_name"`

	Schema     string              `json:"schema"`
	Title      string              `json:"title"`
	Owner      string              `json:"owner"`
	Producer   string              `json:"producer"`
	SLASeconds int                 `json:"sla_seconds"`
	SLA        json.RawMessage     `json:"sla"`
	Span       int                 `json:"span"`
	State      string              `json:"state"`
	Reason     string              `json:"reason"`
	Data       json.RawMessage     `json:"data"`
	Provenance *pageProvenanceJSON `json:"provenance"`
}

type pageJSON struct {
	ID          string          `json:"id"`
	Slug        string          `json:"slug"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Owner       string          `json:"owner"`
	Panels      []pagePanelJSON `json:"panels"`
	UpdatedAt   string          `json:"updated_at"`
}

// pageListRowJSON is one index row. `panel_count` is what the server sends;
// `panels` is read as a number too, because a list that carries a count under
// the plural noun is the other reasonable shape and reading both costs a line.
type pageListRowJSON struct {
	Slug           string         `json:"slug"`
	Name           string         `json:"name"`
	Owner          string         `json:"owner"`
	PanelCount     int            `json:"panel_count"`
	Panels         json.Number    `json:"panels"`
	PanelStates    map[string]int `json:"panel_states"`
	State          string         `json:"state"`
	LastProducedAt string         `json:"last_produced_at"`
	UpdatedAt      string         `json:"updated_at"`
}

// slaLabel renders the panel's SLA. `sla_seconds` is canonical (§11b decision
// 3); the `sla` fallback exists because §4 rule 1 makes the SLA mandatory, so a
// panel that carries it in the older sugar form must still print it rather than
// print nothing and read as a panel that cannot go stale.
func (p pagePanelJSON) slaLabel() string {
	if p.SLASeconds > 0 {
		return fmt.Sprintf("%ds", p.SLASeconds)
	}
	raw := strings.TrimSpace(string(p.SLA))
	if raw == "" || raw == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(p.SLA, &s) == nil {
		return s
	}
	return raw
}

func (r pageListRowJSON) panelCount() int {
	if r.PanelCount > 0 {
		return r.PanelCount
	}
	if n, err := r.Panels.Int64(); err == nil {
		return int(n)
	}
	return 0
}

// pageWriteJSON is what create and update send: the PARSED spec as JSON
// (§11b decision 2). The CLI parses the YAML; the server validates it. Sending
// the document verbatim as a string would leave the server holding an opaque
// blob it is required by §10b.1 to validate.
type pageWriteJSON struct {
	Slug        string               `json:"slug"`
	Name        string               `json:"name"`
	Description string               `json:"description,omitempty"`
	Panels      []pageWritePanelJSON `json:"panels"`
}

type pageWritePanelJSON struct {
	ID     string `json:"id"`
	Schema string `json:"schema"`
	Title  string `json:"title,omitempty"`
	// The panel's glyph (internal/pages/icons.go). ParseDocument has already
	// refused anything outside the closed set, and the server refuses it again.
	Icon       string `json:"icon,omitempty"`
	Owner      string `json:"owner"`
	Producer   string `json:"producer"`
	SLASeconds int    `json:"sla_seconds"`
	Span       int    `json:"span"`
	Public     bool   `json:"public,omitempty"`
	// Actions ride through verbatim (§8b.1). The CLI does not interpret them:
	// ParseDocument already refused anything the vocabulary does not admit, and
	// the server validates again, which is the gate. Re-encoding them field by
	// field here would be a place for the two representations to drift.
	Actions []pages.PanelAction `json:"actions,omitempty"`
	// Wake gates and on_failure ride through verbatim for the same reason
	// (§5, §4 rule 4). The predicate in `when:` is parsed by the SERVER, which
	// is where the panel's schema is known and where the refusal has to
	// happen; a CLI-side parse would be a second grammar to keep in step.
	Wake      []pages.PanelWake     `json:"wake,omitempty"`
	OnFailure *pages.PanelOnFailure `json:"on_failure,omitempty"`
}

// ── list ───────────────────────────────────────────────────────────────────

var pageListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the pages in this workspace",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := pageClient()
		if err != nil {
			return err
		}
		resp, err := client.Get("/api/v1/pages")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := pageCheckError(resp); err != nil {
			return err
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("read response: %w", err)
		}

		f := newFormatter()
		switch f.Format {
		case "json", "yaml", "ndjson":
			return pageEmitMachine(f, body, "[]")
		}

		var rows []pageListRowJSON
		if err := json.Unmarshal(body, &rows); err != nil {
			// A wrapped envelope ({"pages": [...]}) is the other convention in
			// this repo; read it rather than printing nothing.
			var wrapped struct {
				Pages []pageListRowJSON `json:"pages"`
				Rows  []pageListRowJSON `json:"rows"`
			}
			if err2 := json.Unmarshal(body, &wrapped); err2 != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			rows = wrapped.Pages
			if rows == nil {
				rows = wrapped.Rows
			}
		}
		if len(rows) == 0 {
			fmt.Println("No pages yet.")
			fmt.Println("Author one: crewship page create --file <page.yaml>")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "SLUG\tNAME\tPANELS\tSTATE\tOWNER\tLAST DATA")
		for _, row := range rows {
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\n",
				row.Slug, row.Name, row.panelCount(),
				pageDash(row.State), pageDash(row.Owner), pageDash(row.LastProducedAt))
		}
		return w.Flush()
	},
}

// ── get ────────────────────────────────────────────────────────────────────

var pageGetCmd = &cobra.Command{
	Use:   "get <slug>",
	Short: "Show one page: its panels, their data, freshness and provenance",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := pageClient()
		if err != nil {
			return err
		}
		resp, err := client.Get("/api/v1/pages/" + pagePathEscape(args[0]))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := pageCheckError(resp); err != nil {
			return err
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("read response: %w", err)
		}

		f := newFormatter()
		switch f.Format {
		case "json", "yaml", "ndjson":
			// The server's document, passed through. Re-encoding it from a
			// typed struct would silently drop any field this build does not
			// know about, and a machine format that quietly loses fields is
			// worse than one that never had them.
			return pageEmitMachine(f, body, "{}")
		}

		var page pageJSON
		if err := json.Unmarshal(body, &page); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		printPageHuman(page)
		return nil
	},
}

// printPageHuman renders the page for a person.
//
// Every panel prints its state IN WORDS and, when it has data, the absolute
// timestamp and the provenance triple. §4 rule 3 forbids the relative form:
// "a while ago" is precisely the phrase the PRD names as the thing not to
// print, because it reads as "recently" whatever the number behind it is.
func printPageHuman(page pageJSON) {
	fmt.Printf("%s%s%s  (%s)\n", cli.Bold, page.Name, cli.Reset, page.Slug)
	if page.Description != "" {
		fmt.Printf("%s\n", page.Description)
	}
	if page.Owner != "" {
		fmt.Printf("owner: %s\n", page.Owner)
	}
	fmt.Println()

	if len(page.Panels) == 0 {
		fmt.Println("This page has no panels you can see.")
		return
	}
	for _, p := range page.Panels {
		if p.Sealed {
			// The slot is on the page and its width is real; everything else
			// about it belongs to a crew this caller is not in.
			fmt.Printf("%s[%s]%s  sealed — owned by %s, and not visible to you\n\n",
				cli.Dim, pageDash(p.PanelID), cli.Reset, pageDash(p.OwnerCrewName))
			continue
		}
		title := p.Title
		if title == "" {
			title = p.ID
		}
		fmt.Printf("%s%s%s  [%s]  %s\n", cli.Bold, title, cli.Reset, p.ID, p.Schema)
		fmt.Printf("  state:    %s%s\n", p.State, pageReasonSuffix(p))
		fmt.Printf("  owner:    %s\n", pageDash(p.Owner))
		fmt.Printf("  producer: %s\n", pageDash(p.Producer))
		if sla := p.slaLabel(); sla != "" {
			fmt.Printf("  sla:      %s\n", sla)
		}
		if prov := p.Provenance; prov != nil {
			// The footer §4 rule 5 requires: producer, run id, timestamp —
			// every one of them written by the server.
			fmt.Printf("  produced: %s by %s (run %s)\n",
				pageDash(prov.ProducedAt), pageDash(prov.Producer), pageDash(prov.RunID))
		} else {
			fmt.Printf("  produced: — nothing has been pushed to this panel yet\n")
		}
		if len(p.Data) > 0 && string(p.Data) != "null" {
			var pretty bytes.Buffer
			if err := json.Indent(&pretty, p.Data, "  ", "  "); err == nil {
				fmt.Printf("  data:     %s\n", pretty.String())
			} else {
				fmt.Printf("  data:     %s\n", string(p.Data))
			}
		}
		fmt.Println()
	}
}

func pageReasonSuffix(p pagePanelJSON) string {
	if strings.TrimSpace(p.Reason) == "" {
		return ""
	}
	return " — " + p.Reason
}

// ── create / update ────────────────────────────────────────────────────────

var pageCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a page from a YAML (or JSON) page document",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		doc, err := pageDocumentFromFlag(cmd)
		if err != nil {
			return err
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		resp, err := client.Post("/api/v1/pages", pageWriteFrom(doc))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := pageCheckError(resp); err != nil {
			return err
		}
		return pagePrintWriteResult(resp, "created")
	},
}

var pageUpdateCmd = &cobra.Command{
	Use:   "update <slug>",
	Short: "Replace a page's spec from a YAML (or JSON) page document",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		doc, err := pageDocumentFromFlag(cmd)
		if err != nil {
			return err
		}
		if doc.Metadata.Slug != "" && doc.Metadata.Slug != args[0] {
			return cli.WithExitCode(fmt.Errorf(
				"the document declares slug %q but you asked to update %q; a page's slug is its address",
				doc.Metadata.Slug, args[0]), cli.ExitValidation)
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		body := pageWriteFrom(doc)
		body.Slug = ""
		resp, err := client.Patch("/api/v1/pages/"+pagePathEscape(args[0]), body)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := pageCheckError(resp); err != nil {
			return err
		}
		return pagePrintWriteResult(resp, "updated")
	},
}

// pageDocumentFromFlag reads and validates the authored document.
//
// Validation happens here as well as on the server, and that is not
// duplication for its own sake: a typo in a panel's schema should cost a local
// error message naming the line, not a round trip. The server's validation is
// the gate (§10b.1); this one is the fast fail.
func pageDocumentFromFlag(cmd *cobra.Command) (*pages.Document, error) {
	path, _ := cmd.Flags().GetString("file")
	if strings.TrimSpace(path) == "" {
		return nil, cli.WithExitCode(errors.New("--file is required: a page is authored as a YAML document (kind: Page)"), cli.ExitValidation)
	}
	var raw []byte
	var err error
	if path == "-" {
		raw, err = io.ReadAll(os.Stdin)
	} else {
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, cli.WithExitCode(fmt.Errorf("read %s: %w", path, err), cli.ExitValidation)
	}
	doc, err := pages.ParseDocument(raw)
	if err != nil {
		return nil, cli.WithExitCode(err, cli.ExitValidation)
	}
	return doc, nil
}

// pageWriteFrom flattens the parsed document into the request body.
//
// `sla: 30s` becomes `sla_seconds: 30` here — §11b decision 3: one
// representation in the database, one on the wire, one for humans.
func pageWriteFrom(doc *pages.Document) *pageWriteJSON {
	out := &pageWriteJSON{
		Slug:        doc.Metadata.Slug,
		Name:        doc.Metadata.Name,
		Description: doc.Metadata.Description,
		Panels:      make([]pageWritePanelJSON, 0, len(doc.Spec.Panels)),
	}
	for i := range doc.Spec.Panels {
		p := &doc.Spec.Panels[i]
		sla, _ := p.SLADuration() // already validated by ParseDocument
		out.Panels = append(out.Panels, pageWritePanelJSON{
			ID:         p.ID,
			Schema:     string(p.Schema),
			Title:      p.Title,
			Icon:       string(p.Icon),
			Owner:      p.Owner,
			Producer:   p.Producer,
			SLASeconds: int(sla.Seconds()),
			Span:       p.Span,
			Public:     p.Public,
			Actions:    p.Actions,
			Wake:       p.Wake,
			OnFailure:  p.OnFailure,
		})
	}
	return out
}

func pagePrintWriteResult(resp *http.Response, verb string) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	f := newFormatter()
	switch f.Format {
	case "json", "yaml", "ndjson":
		return pageEmitMachine(f, body, "{}")
	}
	var page pageJSON
	if err := json.Unmarshal(body, &page); err != nil {
		fmt.Printf("Page %s.\n", verb)
		return nil
	}
	fmt.Printf("Page %s: %s (%s), %d panel(s).\n", verb, page.Name, page.Slug, len(page.Panels))
	return nil
}

// ── delete ─────────────────────────────────────────────────────────────────

var pageDeleteCmd = &cobra.Command{
	Use:   "delete <slug>",
	Short: "Delete a page and its panels",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// §11b decision 5: every destructive command in this CLI gates on
		// confirmAction, and --yes is what makes it scriptable. A delete an
		// agent cannot run non-interactively is not usable by an agent.
		if err := confirmAction(cmd, fmt.Sprintf("Delete page %q and every panel on it?", args[0])); err != nil {
			return err
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		resp, err := client.Delete("/api/v1/pages/" + pagePathEscape(args[0]))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := pageCheckError(resp); err != nil {
			return err
		}
		fmt.Printf("Page %s deleted.\n", args[0])
		return nil
	},
}

// ── set ────────────────────────────────────────────────────────────────────

var pageSetCmd = &cobra.Command{
	Use:   "set <slug>/<panel>",
	Short: "Push a panel's payload (JSON on stdin with --data -)",
	Long: `Push one panel's payload. This is the single write path, and it is
what appears in every producer script:

  watch-services --json | crewship page set fleet-201/sluzby --data -

--data accepts "-" for stdin, "@path" for a file, or a literal JSON
document. The body is the payload and nothing else: the producer, the
run id and the timestamp are attached by the SERVER, from the token and
its own clock, and cannot be supplied by the caller.

A producer reporting its own failure passes --state failed; the payload
is still stored, and the panel renders as failed rather than as current.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		slug, panel, ok := strings.Cut(args[0], "/")
		if !ok || strings.TrimSpace(slug) == "" || strings.TrimSpace(panel) == "" {
			return cli.WithExitCode(fmt.Errorf(
				"expected <page>/<panel>, got %q", args[0]), cli.ExitValidation)
		}
		data, _ := cmd.Flags().GetString("data")
		payload, err := pageReadPayload(data)
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(payload)) == 0 {
			return cli.WithExitCode(errors.New("the payload is empty; a push carries the panel's JSON"), cli.ExitValidation)
		}
		// Parsed only to fail early on malformed JSON — the bytes that go on
		// the wire are the bytes that were read, unmodified. Re-encoding them
		// would be a chance to add a field, and this command adds none.
		var probe any
		if err := json.Unmarshal(payload, &probe); err != nil {
			return cli.WithExitCode(fmt.Errorf("the payload is not JSON: %w", err), cli.ExitValidation)
		}

		client, err := pageClient()
		if err != nil {
			return err
		}
		path := fmt.Sprintf("/api/v1/pages/%s/panels/%s/data", pagePathEscape(slug), pagePathEscape(panel))
		// The producer's own verdict rides on the QUERY STRING, never in the
		// body: a `state` key next to the payload's keys would read as part of
		// the payload, and §4 rule 2 keeps the two apart.
		if state, _ := cmd.Flags().GetString("state"); strings.TrimSpace(state) != "" {
			path += "?state=" + pagePathEscape(state)
		}
		resp, err := client.Put(path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := pageCheckError(resp); err != nil {
			return err
		}
		body, _ := io.ReadAll(resp.Body)

		f := newFormatter()
		switch f.Format {
		case "json", "yaml", "ndjson":
			return pageEmitMachine(f, body, "{}")
		}
		var ack struct {
			State      string              `json:"state"`
			Provenance *pageProvenanceJSON `json:"provenance"`
		}
		_ = json.Unmarshal(body, &ack)
		if ack.Provenance != nil {
			fmt.Printf("Pushed %d bytes to %s/%s — %s, produced %s by %s (run %s).\n",
				len(payload), slug, panel, pageDash(ack.State),
				pageDash(ack.Provenance.ProducedAt), pageDash(ack.Provenance.Producer), pageDash(ack.Provenance.RunID))
			return nil
		}
		fmt.Printf("Pushed %d bytes to %s/%s.\n", len(payload), slug, panel)
		return nil
	},
}

// pageReadPayload resolves --data into bytes: "-" is stdin, "@path" is a file,
// anything else is the literal document.
func pageReadPayload(data string) ([]byte, error) {
	switch {
	case strings.TrimSpace(data) == "" || data == "-":
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, cli.WithExitCode(fmt.Errorf("read stdin: %w", err), cli.ExitValidation)
		}
		return raw, nil
	case strings.HasPrefix(data, "@"):
		raw, err := os.ReadFile(strings.TrimPrefix(data, "@"))
		if err != nil {
			return nil, cli.WithExitCode(fmt.Errorf("read %s: %w", data[1:], err), cli.ExitValidation)
		}
		return raw, nil
	default:
		return []byte(data), nil
	}
}

// ── Shared plumbing ────────────────────────────────────────────────────────

func pageClient() (*cli.Client, error) {
	if err := requireAuth(); err != nil {
		return nil, err
	}
	if err := requireWorkspace(); err != nil {
		return nil, err
	}
	return newAPIClient(), nil
}

// pageCheckError is cli.CheckError plus the 422 rejection envelope.
//
// The envelope (internal/sidecar/memory_write.go's shape) carries neither
// "error" nor "detail", so CheckError falls through to dumping the raw body:
// `API error (422): {"rejected":true,…}`. That is not a message, it is a JSON
// blob with a number in front — and the whole point of the richer envelope is
// that it says what was refused and what the limit is. So it is read here.
func pageCheckError(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode == http.StatusUnprocessableEntity {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		var rej struct {
			Rejected bool           `json:"rejected"`
			Kind     string         `json:"kind"`
			Message  string         `json:"message"`
			Detail   map[string]any `json:"detail"`
		}
		if json.Unmarshal(raw, &rej) == nil && rej.Rejected && strings.TrimSpace(rej.Message) != "" {
			return cli.WithExitCode(errors.New(rej.Message), cli.ExitValidation)
		}
		// Not the rejection shape after all — hand the body back so CheckError
		// renders it the way it renders every other error.
		resp.Body = io.NopCloser(bytes.NewReader(raw))
	}
	return cli.CheckError(resp)
}

// pageEmitMachine prints a server document in a machine format without
// re-encoding it through a typed struct.
func pageEmitMachine(f *cli.Formatter, body []byte, empty string) error {
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte(empty)
	}
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	switch f.Format {
	case "yaml":
		return f.YAML(doc)
	case "ndjson":
		return f.NDJSON(doc)
	default:
		return f.JSON(doc)
	}
}

// pagePathEscape keeps a slug that is not a slug (an operator typo, a slash, a
// query separator) from rewriting the request path.
func pagePathEscape(s string) string { return url.PathEscape(s) }

func pageDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func init() {
	pageCreateCmd.Flags().String("file", "", "Page document to author from (YAML or JSON; - for stdin)")
	pageUpdateCmd.Flags().String("file", "", "Page document to replace the spec with (YAML or JSON; - for stdin)")
	pageDeleteCmd.Flags().BoolP("yes", "y", false, "Skip the interactive confirmation prompt")
	pageSetCmd.Flags().String("data", "-", `Payload: "-" for stdin, "@path" for a file, or a literal JSON document`)
	pageSetCmd.Flags().String("state", "", `The producer's own verdict: "ok" (default) or "failed"`)

	pageCmd.AddCommand(pageListCmd)
	pageCmd.AddCommand(pageGetCmd)
	pageCmd.AddCommand(pageCreateCmd)
	pageCmd.AddCommand(pageUpdateCmd)
	pageCmd.AddCommand(pageDeleteCmd)
	pageCmd.AddCommand(pageSetCmd)

	rootCmd.AddCommand(pageCmd)
}
