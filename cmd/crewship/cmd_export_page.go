package main

// `crewship export page [slug]` — the kinds path PRD §13 obstacle 6 said
// `crewship export` was missing (docs/prd/pages.md):
//
//	"Manifest export is dead code for SPEC-2 kinds. ExportSavedViews,
//	 ExportRoutines, ExportProjects have no non-test callers; crewship
//	 export knows only Crew and Workspace. ExportPages would ship tested
//	 and unreachable unless the CLI grows a kinds path."
//
// Three decisions are worth their lines here.
//
//  1. THIS IS NOT `crewship page export`, AND THE TWO ARE NOT THE SAME
//     DOCUMENT. `page export <slug>` produces a page BUNDLE
//     (internal/api/pages_transfer.go): `format: crewship-page-bundle/v1`,
//     workspace ids stripped, every external reference DECLARED in a
//     `references:` block so an importer can see what it must bind, and it
//     is consumed by exactly one thing — `crewship page import --bind`.
//     What this command produces is a MANIFEST: `apiVersion` + `kind: Page`
//     + `metadata` + `spec`, consumed by `crewship apply -f`, diffed by
//     `apply --dry-run`, and living in a git repo next to the kind: Crew
//     documents that declare the crews its panels are owned by. The bundle
//     is for carrying a page to a workspace that has never heard of it; the
//     manifest is for declaring the page you already run. One kind of file
//     cannot be the other: the bundle has no `kind:` for apply to dispatch
//     on, and the manifest has no references block for an importer to bind
//     against. Building the second door is therefore not a second way to do
//     one thing — it is the only way to do the thing apply does.
//
//  2. `export workspace` DOES NOT LEARN ABOUT PAGES. It renders a single
//     kind: Workspace document whose spec is crews, skills and credentials
//     (internal/manifest/schema.go WorkspaceSpec), and there is no `pages:`
//     under it. Adding one would change what an EXISTING bundle means: an
//     operator who applies their workspace backup today creates crews, and
//     would afterwards also create and replace pages — including replacing
//     panels whose buttons and gates this exporter cannot read back (see
//     the caveat below). A page is a top-level kind and travels as its own
//     document; `crewship export page >> full.workspace.yaml` appends it to
//     the same multi-document file, which is why every document this
//     command emits is prefixed with its own `---`.
//
//  3. WHOSE ACCOUNT EXPORTS DECIDES WHAT THE FILE CONTAINS. The read path
//     DOES echo the authored half — `public` rides on the panel wire, and
//     `actions`, `wake`, `on_failure` and `refresh` are attached by
//     attachAuthoredHalf (internal/api/pages_handler.go) — but only to a
//     caller who may edit the page's spec. Export as someone holding read
//     or produce and the same command emits the grid alone, silently,
//     because an absent field is not evidence of anything: a panel that
//     declares no actions and a panel whose actions were not echoed look
//     identical on the wire.
//
//     So the caveat names the CONDITION rather than claiming a loss that
//     usually is not there. It goes to stderr, where redirecting stdout
//     into a file leaves it visible.

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/crewship-ai/crewship/internal/manifest"
	"github.com/crewship-ai/crewship/internal/manifest/kinds"
)

// pageManifestSchemaHint is the yaml-language-server directive every
// exported manifest carries, so an editor validates the file as it is
// edited. Restated here rather than reused from manifest.MarshalDocument
// because that function takes a manifest.Document — the crew-shaped one —
// and a kind: Page document is not one.
const pageManifestSchemaHint = "# yaml-language-server: $schema=https://schemas.crewship.ai/v1/manifest.json\n"

// pageExportCaveat is decision 3 above, in the words an operator needs at
// the moment they have just created the file.
const pageExportCaveat = "note: a panel's authored half — `public`, `actions`, `wake`, `on_failure`, `refresh` —\n" +
	"      is echoed only to an account that may EDIT the page. Exported by anyone else, this\n" +
	"      file is the grid alone and re-applying it removes those panels' buttons and gates.\n" +
	"      The absent field looks the same either way, so check the file before you apply it.\n"

// exportPageCmd is `crewship export page [slug]`.
//
// The arity carries the meaning, matching the two commands beside it: with
// a slug it is `export crew <slug>` (this one thing), without it is
// `export workspace` (everything of this kind in the workspace, as a
// multi-document stream apply reads in one pass).
var exportPageCmd = &cobra.Command{
	Use:   "page [slug]",
	Short: "Export pages as kind=Page manifests (YAML)",
	Long: `Pull a page's current state and render it as a kind: Page manifest that
can be re-applied with 'crewship apply -f'. With no slug, every page in
the workspace is emitted as one multi-document YAML stream, sorted by
slug so the file is diff-stable.

This is NOT 'crewship page export', which produces a portable BUNDLE for
'crewship page import' to install in another workspace. This produces the
declarative document 'crewship apply' reads.

Panels sealed to you (owned by a crew you are not in) refuse the export
rather than being dropped, because a document missing a panel DELETES
that panel on the next apply.

Examples:
  crewship export page weekly-close > weekly-close.page.yaml
  crewship export page                        # every page, one stream
  crewship export page -o ./manifests/pages.yaml`,
	Args: cobra.MaximumNArgs(1),
	RunE: runExportPage,
}

func init() {
	exportPageCmd.Flags().StringP("output", "o", "", "Write to file instead of stdout")
	exportCmd.AddCommand(exportPageCmd)
}

func runExportPage(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}

	output, _ := cmd.Flags().GetString("output")
	client := manifest.NewKindsClient(manifest.NewClientFromCLI(newAPIClient()))

	var (
		docs []*kinds.PageDocument
		err  error
	)
	if len(args) == 1 {
		var doc *kinds.PageDocument
		doc, err = kinds.ExportPage(cmd.Context(), client, args[0])
		if doc != nil {
			docs = []*kinds.PageDocument{doc}
		}
	} else {
		docs, err = kinds.ExportPages(cmd.Context(), client)
	}
	if err != nil {
		// Deliberately unwrapped. Every error out of the kinds exporter
		// already opens with `export page "<slug>":` and the sealed-panel
		// refusal is a full sentence telling the operator what to do about
		// it; prefixing it again would print the words "export page" twice
		// in one line and push the instruction off the end of it.
		return err
	}

	// Nothing to write is not a failure — a workspace may simply have no
	// pages — but it must not produce an empty file either. `-o` pointed at
	// last week's export would otherwise truncate it and report success.
	if len(docs) == 0 {
		fmt.Fprintln(os.Stderr, "no pages in this workspace — nothing to export")
		return nil
	}

	rendered, err := renderPageManifests(docs)
	if err != nil {
		return err
	}
	if output == "" {
		fmt.Print(rendered)
	} else {
		if err := os.WriteFile(output, []byte(rendered), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", output, err)
		}
		fmt.Fprintf(os.Stderr, "wrote %s\n", output)
	}
	fmt.Fprint(os.Stderr, pageExportCaveat)
	return nil
}

// renderPageManifests serialises the documents as one YAML stream.
//
// Every document is prefixed with `---`, including the first. A stream that
// opens with a separator is legal YAML, apply's loader skips the empty
// document it would imply, and it means the output can be appended to an
// existing manifest (`crewship export page >> full.workspace.yaml`) without
// the first page silently merging into the document above it.
func renderPageManifests(docs []*kinds.PageDocument) (string, error) {
	var sb strings.Builder
	sb.WriteString(pageManifestSchemaHint)
	for _, doc := range docs {
		out, err := yaml.Marshal(doc)
		if err != nil {
			return "", fmt.Errorf("marshal page %q: %w", doc.Metadata.Slug, err)
		}
		sb.WriteString("---\n")
		sb.Write(out)
	}
	return sb.String(), nil
}
