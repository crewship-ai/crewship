package main

// `crewship page export|import|versions|rollback` — portability and history
// (docs/prd/pages.md §10b.1, §10b.2).
//
// One command per endpoint, the repo rule:
//
//	GET  /api/v1/pages/{slug}/export     page export <slug>
//	POST /api/v1/pages/import            page import <file> --slug --bind
//	GET  /api/v1/pages/{slug}/versions   page versions <slug>
//	POST /api/v1/pages/{slug}/rollback   page rollback <slug> --to <seq>
//
// Two shapes here are decisions rather than taste:
//
//  1. `--bind` IS REPEATABLE AND NOT COMMA-SEPARATED (§11b.13). The same
//     reasoning as `page grant --panels`, one step stronger: a binding's two
//     halves are slugs, a slug may contain a comma far more plausibly than a
//     flag may be repeated, and a mis-split binding does not fail — it binds
//     the wrong thing and the page comes up pointing at a name nobody
//     answers to.
//
//  2. `page export` PRINTS YAML BY DEFAULT. The bundle is a document people
//     keep in a repository and read in a diff, §10b.2's own example is
//     `crewship page export <slug> > weekly-close.page.yaml`, and YAML is the
//     authoring format the rest of Pages uses. `--format json` gives the wire
//     document byte for byte.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// ── The bundle, as the CLI reads and writes it ─────────────────────────────
//
// Dual-tagged: JSON is the wire, YAML is the file on disk. Both names are the
// same string on purpose — a bundle that renamed its fields on the way to a
// file would not be the same document, and `page export | page import` has to
// be a round trip.

const pageBundleFormatCLI = "crewship-page-bundle/v1"

type pageBundlePanelJSON struct {
	ID     string `json:"id" yaml:"id"`
	Schema string `json:"schema" yaml:"schema"`
	Title  string `json:"title,omitempty" yaml:"title,omitempty"`
	// The panel's glyph, from the closed set the server validates against
	// (internal/pages/icons.go). Carried so `export | import` is a round trip:
	// a field the bundle drops is a field the install silently loses.
	Icon       string `json:"icon,omitempty" yaml:"icon,omitempty"`
	Owner      string `json:"owner" yaml:"owner"`
	Producer   string `json:"producer" yaml:"producer"`
	SLASeconds int    `json:"sla_seconds" yaml:"sla_seconds"`
	Span       int    `json:"span,omitempty" yaml:"span,omitempty"`
}

type pageBundlePageJSON struct {
	Name        string                `json:"name" yaml:"name"`
	Slug        string                `json:"slug" yaml:"slug"`
	Description string                `json:"description,omitempty" yaml:"description,omitempty"`
	Owner       string                `json:"owner,omitempty" yaml:"owner,omitempty"`
	Panels      []pageBundlePanelJSON `json:"panels" yaml:"panels"`
}

type pageBundleRefJSON struct {
	Ref      string   `json:"ref" yaml:"ref"`
	Kind     string   `json:"kind" yaml:"kind"`
	Bindable bool     `json:"bindable" yaml:"bindable"`
	UsedBy   []string `json:"used_by,omitempty" yaml:"used_by,omitempty"`
}

type pageBundleJSON struct {
	Format string              `json:"format" yaml:"format"`
	Page   pageBundlePageJSON  `json:"page" yaml:"page"`
	Refs   []pageBundleRefJSON `json:"references,omitempty" yaml:"references,omitempty"`
}

// pageImportBodyJSON is the bundle plus the two things only the importer
// knows.
type pageImportBodyJSON struct {
	Format string              `json:"format"`
	Page   pageBundlePageJSON  `json:"page"`
	Refs   []pageBundleRefJSON `json:"references,omitempty"`
	Slug   string              `json:"slug,omitempty"`
	Bind   map[string]string   `json:"bind,omitempty"`
}

// ── export ─────────────────────────────────────────────────────────────────

var pageExportCmd = &cobra.Command{
	Use:   "export <slug>",
	Short: "Export a page as a portable bundle (no workspace ids, references declared)",
	Long: `Export a page as a bundle another workspace can install.

  crewship page export weekly-close > weekly-close.page.yaml
  crewship page import weekly-close.page.yaml --slug uzaverka \
      --bind crew/ucetni=crew/finance

The bundle carries no workspace ids. Every external thing the page needs —
the crews that own its panels, the routines and agents that produce them —
is DECLARED in a references block, so you can see what you will have to
bind before you install it.

Exporting carries the whole page, including panels that are sealed to an
ordinary reader, so it needs the same authority as editing the page.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := pageClient()
		if err != nil {
			return err
		}
		resp, err := client.Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/export")
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
		if f.Format == "json" || f.Format == "ndjson" {
			return pageEmitMachine(f, body, "{}")
		}
		// YAML for everything else, `table` included: a bundle has no table
		// form, and printing one would produce a file that cannot be imported.
		var doc any
		if err := json.Unmarshal(body, &doc); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		return f.YAML(doc)
	},
}

// ── import ─────────────────────────────────────────────────────────────────

var pageImportCmd = &cobra.Command{
	Use:   "import <file>",
	Short: "Install a page bundle, binding its declared references to local ones",
	Long: `Install a bundle produced by "crewship page export" (- reads stdin).

  crewship page import weekly-close.page.yaml --slug uzaverka \
      --bind crew/ucetni=crew/finance \
      --bind routine/nightly-close=routine/nocni-uzaverka

--bind is repeatable and is NOT comma-separated: both halves are slugs, and
a slug may contain a comma far more plausibly than a flag may be repeated.

The import is one transaction. Either every reference binds to something
that exists in this workspace, or nothing is created and the refusal names
the references it could not resolve — a page full of dead panels is the
failure this is designed against.

Panels arrive unpublished regardless of what the source page published:
publishing is per panel, human-only, and never a bulk action.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		bundle, err := pageReadBundle(args[0])
		if err != nil {
			return err
		}
		bindFlags, _ := cmd.Flags().GetStringArray("bind")
		bind, err := parsePageBinds(bindFlags)
		if err != nil {
			return err
		}
		slug, _ := cmd.Flags().GetString("slug")
		slug = strings.TrimSpace(slug)

		body := pageImportBodyJSON{
			Format: bundle.Format,
			Page:   bundle.Page,
			Refs:   bundle.Refs,
			Slug:   slug,
			Bind:   bind,
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		resp, err := client.Post("/api/v1/pages/import", body)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := pageImportCheckError(resp); err != nil {
			return err
		}

		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("read response: %w", err)
		}
		f := newFormatter()
		switch f.Format {
		case "json", "yaml", "ndjson":
			return pageEmitMachine(f, raw, "{}")
		}
		var page pageJSON
		if err := json.Unmarshal(raw, &page); err != nil {
			fmt.Println("Bundle imported.")
			return nil
		}
		fmt.Printf("Imported %s (%s), %d panel(s).\n", page.Name, page.Slug, len(page.Panels))
		if len(bind) > 0 {
			froms := make([]string, 0, len(bind))
			for from := range bind {
				froms = append(froms, from)
			}
			sort.Strings(froms)
			for _, from := range froms {
				fmt.Printf("  bound %s -> %s\n", from, bind[from])
			}
		}
		fmt.Println("No panel has data yet — each one waits for its producer's first push.")
		return nil
	},
}

// pageReadBundle reads a bundle from a path (or stdin) as YAML or JSON.
//
// YAML is a superset of JSON, so one decoder reads both and there is no
// sniffing to get wrong. KnownFields is deliberately NOT set: a bundle from a
// newer build may carry fields this one has never heard of, and the server
// checks the format version — refusing locally on an unknown key would turn a
// forwards-compatible document into an error the operator cannot act on.
func pageReadBundle(path string) (*pageBundleJSON, error) {
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
	var bundle pageBundleJSON
	if err := yaml.Unmarshal(raw, &bundle); err != nil {
		return nil, cli.WithExitCode(fmt.Errorf("%s is not a readable bundle: %w", path, err), cli.ExitValidation)
	}
	if strings.TrimSpace(bundle.Format) == "" {
		return nil, cli.WithExitCode(fmt.Errorf(
			"%s carries no `format` — a bundle is what `crewship page export` writes, not a page document; "+
				"to author a page use `crewship page create --file`", path), cli.ExitValidation)
	}
	if bundle.Format != pageBundleFormatCLI {
		return nil, cli.WithExitCode(fmt.Errorf(
			"%s declares format %q; this build reads %s", path, bundle.Format, pageBundleFormatCLI), cli.ExitValidation)
	}
	if len(bundle.Page.Panels) == 0 {
		return nil, cli.WithExitCode(fmt.Errorf(
			"%s declares no panels; there would be nothing to render and nothing to push to", path), cli.ExitValidation)
	}
	return &bundle, nil
}

// parsePageBinds turns the repeated --bind flag into the wire map.
//
// A repeated LEFT-hand side is refused rather than resolved last-wins: two
// bindings for one reference is an operator who has lost track of which one is
// in force, and silently picking one of them is how the page ends up bound to
// the crew they did not mean.
func parsePageBinds(flags []string) (map[string]string, error) {
	if len(flags) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(flags))
	for _, raw := range flags {
		from, to, ok := strings.Cut(raw, "=")
		from, to = strings.TrimSpace(from), strings.TrimSpace(to)
		if !ok || from == "" || to == "" {
			return nil, cli.WithExitCode(fmt.Errorf(
				"--bind %q is not a binding; the form is --bind <bundle-ref>=<local-ref>, "+
					"for example --bind crew/ucetni=crew/finance", raw), cli.ExitValidation)
		}
		if prev, dup := out[from]; dup {
			return nil, cli.WithExitCode(fmt.Errorf(
				"%s is bound twice, to %s and to %s; one reference binds to one thing", from, prev, to), cli.ExitValidation)
		}
		out[from] = to
	}
	return out, nil
}

// pageImportCheckError renders the server's refusal in full.
//
// pageCheckError would surface the `error` sentence, which already names the
// references — but the 422 also carries the structured list, and an operator
// fixing bindings wants one line per reference with the panels that need it.
// Everything else falls through to the shared handling.
func pageImportCheckError(resp *http.Response) error {
	if resp.StatusCode != http.StatusUnprocessableEntity {
		return pageCheckError(resp)
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var refused struct {
		Error      string `json:"error"`
		Hint       string `json:"hint"`
		Unresolved []struct {
			Ref    string   `json:"ref"`
			Kind   string   `json:"kind"`
			UsedBy []string `json:"used_by"`
			Reason string   `json:"reason"`
		} `json:"unresolved"`
	}
	if json.Unmarshal(raw, &refused) == nil && len(refused.Unresolved) > 0 {
		var b strings.Builder
		b.WriteString("import refused — nothing was created.\n")
		for _, u := range refused.Unresolved {
			fmt.Fprintf(&b, "  %s: %s", u.Ref, u.Reason)
			if len(u.UsedBy) > 0 {
				fmt.Fprintf(&b, " (needed by %s)", strings.Join(u.UsedBy, ", "))
			}
			b.WriteString("\n")
		}
		if strings.TrimSpace(refused.Hint) != "" {
			b.WriteString("  " + refused.Hint)
		}
		return cli.WithExitCode(errors.New(strings.TrimRight(b.String(), "\n")), cli.ExitValidation)
	}
	resp.Body = io.NopCloser(strings.NewReader(string(raw)))
	return pageCheckError(resp)
}

// ── versions ───────────────────────────────────────────────────────────────

type pageVersionRowJSON struct {
	Seq         int64  `json:"seq"`
	CreatedAt   string `json:"created_at"`
	Author      string `json:"author"`
	AuthorLabel string `json:"author_label"`
	Name        string `json:"name"`
	PanelCount  int    `json:"panel_count"`
	Current     bool   `json:"current"`
}

var pageVersionsCmd = &cobra.Command{
	Use:   "versions <slug>",
	Short: "Show the retained version history of a page",
	Long: `Every save of a page is a version, and the last 50 are retained.
This is what "page rollback --to" chooses from.

Panel DATA is not versioned: a rollback restores the page's structure and
never its numbers.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := pageClient()
		if err != nil {
			return err
		}
		resp, err := client.Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/versions")
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
			return pageEmitMachine(f, body, "{}")
		}
		var out struct {
			Page     string               `json:"page"`
			Retained int                  `json:"retained"`
			Versions []pageVersionRowJSON `json:"versions"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if len(out.Versions) == 0 {
			fmt.Printf("No versions retained for %s.\n", args[0])
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "SEQ\tWHEN\tAUTHOR\tPANELS\tNAME")
		for _, v := range out.Versions {
			seq := fmt.Sprintf("%d", v.Seq)
			if v.Current {
				seq += " *"
			}
			author := v.AuthorLabel
			if strings.TrimSpace(author) == "" {
				author = v.Author
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n",
				seq, pageDash(v.CreatedAt), pageDash(author), v.PanelCount, pageDash(v.Name))
		}
		if err := w.Flush(); err != nil {
			return err
		}
		fmt.Printf("\n* current. The last %d versions are retained; roll back with: crewship page rollback %s --to <seq>\n",
			out.Retained, args[0])
		return nil
	},
}

// ── rollback ───────────────────────────────────────────────────────────────

var pageRollbackCmd = &cobra.Command{
	Use:   "rollback <slug>",
	Short: "Restore a retained version of a page's spec",
	Long: `Restore the page's structure as it was at a given version.

  crewship page versions weekly-close
  crewship page rollback weekly-close --to 7

A rollback restores STRUCTURE, never numbers. A panel the rollback brings
back — or redefines, by changing its schema, its producer or its owning
crew — renders dimmed and "waiting for first data" until its producer
pushes again, even if older rows for it survive. Old payloads are never
resurrected and shown as current: a rollback is exactly when someone is
most likely to believe what they see.

The rollback is itself a save and appends a new version, so a rollback can
itself be rolled back.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		to, _ := cmd.Flags().GetInt64("to")
		if to <= 0 {
			return cli.WithExitCode(errors.New(
				"--to <seq> is required: rollback names the version to restore — see crewship page versions "+args[0]),
				cli.ExitValidation)
		}
		if err := confirmAction(cmd, fmt.Sprintf(
			"Restore page %q to version %d? Panels it brings back or redefines will show no data until their producer pushes again.",
			args[0], to)); err != nil {
			return err
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		resp, err := client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/rollback",
			map[string]any{"to": to})
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
			return pageEmitMachine(f, body, "{}")
		}
		var out struct {
			Page         pageJSON `json:"page"`
			RolledBackTo int64    `json:"rolled_back_to"`
			Version      int64    `json:"version"`
			AwaitingData []string `json:"awaiting_data"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			fmt.Printf("Page %s rolled back to version %d.\n", args[0], to)
			return nil
		}
		fmt.Printf("Page %s restored to version %d, saved as version %d.\n",
			args[0], out.RolledBackTo, out.Version)
		if len(out.AwaitingData) > 0 {
			fmt.Printf("Waiting for first data (no old payload is shown as current): %s\n",
				strings.Join(out.AwaitingData, ", "))
		}
		return nil
	},
}

func init() {
	pageImportCmd.Flags().String("slug", "", "Slug to install the page under here (defaults to the bundle's own)")
	pageImportCmd.Flags().StringArray("bind", nil,
		"Bind a declared reference to a local one: --bind crew/ucetni=crew/finance. Repeatable; not comma-separated")
	pageRollbackCmd.Flags().Int64("to", 0, "The version seq to restore (see: crewship page versions <slug>)")
	pageRollbackCmd.Flags().BoolP("yes", "y", false, "Skip the interactive confirmation prompt")

	pageCmd.AddCommand(pageExportCmd)
	pageCmd.AddCommand(pageImportCmd)
	pageCmd.AddCommand(pageVersionsCmd)
	pageCmd.AddCommand(pageRollbackCmd)
}
