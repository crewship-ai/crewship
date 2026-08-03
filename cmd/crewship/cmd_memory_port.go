package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/memport"
)

// Memory portability: getting memory out of Crewship in a form a human
// can read, and getting memory in from the markdown-shaped harnesses
// people arrive from.
//
// The format work lives on THIS side of the wire. A source directory
// from another product is the least trustworthy input in the feature
// and it never reaches the server: the CLI reads it locally, maps it to
// canonical documents, shows the operator what would land where, and
// only then sends documents the server knows how to validate.

var memoryExportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export agent or crew memory to a portable OKF bundle",
	Long: `Write memory out as a directory of markdown files with YAML
frontmatter — the Open Knowledge Format. The bundle is readable,
diffable and git-friendly, which a backup archive is not.

Omit --agent to export the crew-shared tier.

Examples:
  crewship memory export --crew engineering --agent alex --out ./alex-memory
  crewship memory export --crew engineering --out ./crew-memory`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		out, _ := cmd.Flags().GetString("out")
		if out == "" {
			return fmt.Errorf("--out is required (destination directory for the bundle)")
		}
		crewRef, _ := cmd.Flags().GetString("crew")
		if crewRef == "" {
			return fmt.Errorf("--crew is required")
		}
		agentSlug, _ := cmd.Flags().GetString("agent")

		client := newAPIClient()
		crewID, err := resolveCrewID(client, crewRef)
		if err != nil {
			return err
		}
		q := url.Values{}
		q.Set("crew_id", crewID)
		if agentSlug != "" {
			q.Set("agent_slug", agentSlug)
		}
		resp, err := client.Get("/api/v1/memory/export?" + q.Encode())
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var payload struct {
			Format    string `json:"format"`
			Documents []struct {
				Path  string   `json:"path"`
				Tier  string   `json:"tier"`
				Scope string   `json:"scope"`
				Title string   `json:"title"`
				Tags  []string `json:"tags"`
				Body  string   `json:"body"`
			} `json:"documents"`
			Skipped []struct {
				Source string `json:"source"`
				Reason string `json:"reason"`
			} `json:"skipped"`
		}
		if err := cli.ReadJSON(resp, &payload); err != nil {
			return err
		}
		if len(payload.Documents) == 0 {
			return fmt.Errorf("this scope holds no memory yet — nothing to export")
		}

		docs := make([]memport.Doc, 0, len(payload.Documents))
		for _, d := range payload.Documents {
			docs = append(docs, memport.Doc{
				Tier:    memory.Tier(d.Tier),
				Scope:   memport.Scope(d.Scope),
				RelPath: d.Path,
				Title:   d.Title,
				Tags:    d.Tags,
				Body:    []byte(d.Body),
			})
		}
		if err := memport.ExportOKF(out, docs); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "exported %d documents to %s\n", len(docs), out)
		for _, d := range docs {
			fmt.Fprintf(os.Stdout, "  %s\n", d.RelPath)
		}
		// Anything the server could not read is named. A bundle that is
		// quietly missing a file is worse than one that fails: the
		// operator keeps it believing it is complete.
		if len(payload.Skipped) > 0 {
			fmt.Fprintf(os.Stdout, "\nNOT exported (%d):\n", len(payload.Skipped))
			for _, sk := range payload.Skipped {
				fmt.Fprintf(os.Stdout, "  %-32s %s\n", sk.Source, sk.Reason)
			}
		}
		return nil
	},
}

var memoryImportCmd = &cobra.Command{
	Use:   "import",
	Short: "Import memory from an OKF, NanoClaw or OpenClaw source",
	Long: `Read a memory directory produced by another harness, map it onto
Crewship's tiers, and write it into one agent's (or the crew's) memory.

The format is detected from the directory's shape; --format overrides
the guess. Recognised: crewship, okf, nanoclaw, openclaw.

NOTHING IS WRITTEN WITHOUT --apply. The default run prints the plan —
which source file becomes which memory file, and what is being left
behind — because an import lands in the context an agent reasons from,
and reviewing that after the fact is too late.

Derived data is never imported: embeddings, session transcripts and
task logs are listed as skipped rather than silently dropped.

Examples:
  crewship memory import --from ~/.openclaw/workspace-main --crew engineering --agent alex
  crewship memory import --from ./nanoclaw/ --group telegram_dev-team --crew eng --agent alex --apply`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Auth is checked in the --apply branch, not here: reading a
		// directory on your own disk and being shown what it would map
		// to needs no server, and making the preview require a login
		// would push people to skip straight to the write.
		from, _ := cmd.Flags().GetString("from")
		if from == "" {
			return fmt.Errorf("--from is required (the source memory directory)")
		}
		crewRef, _ := cmd.Flags().GetString("crew")
		if crewRef == "" {
			return fmt.Errorf("--crew is required")
		}
		agentSlug, _ := cmd.Flags().GetString("agent")
		formatFlag, _ := cmd.Flags().GetString("format")
		operator, _ := cmd.Flags().GetString("operator")
		group, _ := cmd.Flags().GetString("group")
		apply, _ := cmd.Flags().GetBool("apply")
		withCrew, _ := cmd.Flags().GetBool("with-crew")

		st, err := os.Stat(from)
		if err != nil {
			return fmt.Errorf("reading %s: %w", from, err)
		}
		if !st.IsDir() {
			return fmt.Errorf("%s is not a directory", from)
		}
		// SecureDirFS, not os.DirFS. os.DirFS refuses ".." inside a name
		// and still follows symlinks in the tree — and the source here is
		// by this feature's own description the least trustworthy input
		// it has: a bundle somebody else produced. A link named
		// MEMORY.md pointing at ~/.ssh/id_rsa would be read, printed in
		// the plan, and POSTed to the server. The server-side read is
		// already guarded this way; the local one has the same exposure
		// and now the same guard.
		fsys := memport.SecureDirFS(from)

		format := memport.Format(formatFlag)
		if formatFlag == "" || formatFlag == "auto" {
			format, err = memport.Detect(fsys)
			if err != nil {
				return fmt.Errorf("%w — pass --format to say which layout this is", err)
			}
		}

		plan, err := memport.ReadSource(fsys, format, memport.Options{
			OperatorSlug: operator,
			Group:        group,
		})
		if err != nil {
			return err
		}
		if len(plan.Docs) == 0 {
			return fmt.Errorf("nothing to import from %s (detected format: %s)", from, format)
		}

		agentDocs, crewDocs, blocked := routeByTarget(plan, agentSlug, withCrew)
		printImportPlan(plan, agentSlug, blocked)
		if len(agentDocs)+len(crewDocs) == 0 {
			return fmt.Errorf("nothing left to import after routing — see the reasons above")
		}
		if !apply {
			fmt.Fprintf(os.Stdout, "\nDry run — nothing written. Re-run with --apply to write.\n")
			return nil
		}

		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		crewID, err := resolveCrewID(client, crewRef)
		if err != nil {
			return err
		}

		// Two scopes, two requests: the crew-shared tree and the
		// agent's own are different directories on the server.
		var rejected int
		if len(agentDocs) > 0 {
			n, err := postImportBatch(client, crewID, agentSlug, agentDocs)
			if err != nil {
				return err
			}
			rejected += n
		}
		if len(crewDocs) > 0 {
			n, err := postImportBatch(client, crewID, "", crewDocs)
			if err != nil {
				return err
			}
			rejected += n
		}
		if rejected > 0 {
			return fmt.Errorf("import incomplete: %d document(s) rejected", rejected)
		}
		// A source file the reader could not open is not a clean import.
		// It is listed in the plan, but a plan scrolls past and an exit
		// code does not: without this, "some of your memory could not be
		// read" ends in a zero exit and reads as success.
		if n := unreadableCount(plan); n > 0 {
			return fmt.Errorf("import incomplete: %d source file(s) could not be read — see the list above", n)
		}
		return nil
	},
}

// postImportBatch sends one scope's documents and reports how many the
// server's write policy refused.
//
// A rejection is the memory writer doing its job, not a transport
// failure — but an operator who does not see it believes the import was
// whole, so it is printed and counted rather than returned as a bare
// error.
func postImportBatch(client *cli.Client, crewID, agentSlug string, docs []memport.Doc) (int, error) {
	scope := "crew-shared memory"
	if agentSlug != "" {
		scope = "agent " + agentSlug
	}
	payload := make([]map[string]any, 0, len(docs))
	for _, d := range docs {
		payload = append(payload, map[string]any{
			"path":    d.RelPath,
			"tier":    string(d.Tier),
			"scope":   string(d.Scope),
			"title":   d.Title,
			"tags":    d.Tags,
			"sources": d.Sources,
			"body":    string(d.Body),
		})
	}
	resp, err := client.Post("/api/v1/memory/import", map[string]any{
		"crew_id":    crewID,
		"agent_slug": agentSlug,
		"documents":  payload,
	})
	if err != nil {
		return 0, err
	}
	if err := cli.CheckError(resp); err != nil {
		return 0, err
	}
	var out struct {
		Written  []string `json:"written"`
		Rejected []struct {
			Path string `json:"path"`
			Kind string `json:"kind"`
		} `json:"rejected"`
		Failed []struct {
			Path   string `json:"path"`
			Reason string `json:"reason"`
		} `json:"failed"`
	}
	if err := cli.ReadJSON(resp, &out); err != nil {
		return 0, err
	}
	fmt.Fprintf(os.Stdout, "\n%s — wrote %d document(s)\n", scope, len(out.Written))
	for _, w := range out.Written {
		fmt.Fprintf(os.Stdout, "  %s\n", w)
	}
	if len(out.Rejected) > 0 {
		fmt.Fprintf(os.Stdout, "  %d REJECTED by memory write policy:\n", len(out.Rejected))
		for _, rj := range out.Rejected {
			fmt.Fprintf(os.Stdout, "    %s (%s)\n", rj.Path, rj.Kind)
		}
	}
	if len(out.Failed) > 0 {
		fmt.Fprintf(os.Stdout, "  %d REFUSED:\n", len(out.Failed))
		for _, f := range out.Failed {
			fmt.Fprintf(os.Stdout, "    %s — %s\n", f.Path, f.Reason)
		}
	}
	return len(out.Rejected) + len(out.Failed), nil
}

// routeByTarget splits a plan by where each document actually lives on
// the server.
//
// A source that produces both tiers is the normal case, not the edge
// one: NanoClaw's groups/global/CLAUDE.md is crew-shared while the
// group's own file is the agent's, and OpenClaw's AGENTS.md is shared
// while MEMORY.md is not. The two tiers are different directories, so
// one import request cannot carry both.
//
// Crew-shared memory is read by every agent in the crew. Writing it
// because somebody asked to import into ONE agent is a blast radius
// they did not ask for, so it takes an explicit opt-in; without it the
// documents are reported, never silently dropped.
func routeByTarget(plan memport.Plan, agentSlug string, withCrew bool) (agentDocs, crewDocs []memport.Doc, blocked []memport.Skip) {
	for _, d := range plan.Docs {
		// Scope, not tier. A crew's pinned notes carry tier "pins" and
		// belong to the crew tree; an agent's own pins.md carries the
		// same tier and belongs to the agent's. Routing on tier put crew
		// content into one agent's private directory, where the prompt
		// builder that reads the crew tree never sees it again.
		isCrewScoped := d.Scope == memport.ScopeCrew
		switch {
		case isCrewScoped && agentSlug == "":
			crewDocs = append(crewDocs, d)
		case isCrewScoped && withCrew:
			crewDocs = append(crewDocs, d)
		case isCrewScoped:
			blocked = append(blocked, memport.Skip{
				Source: d.RelPath,
				Reason: "crew-shared — every agent in the crew reads it; re-run with --with-crew to include it",
			})
		case agentSlug == "":
			blocked = append(blocked, memport.Skip{
				Source: d.RelPath,
				Reason: "agent-private — name a target with --agent to import it",
			})
		default:
			agentDocs = append(agentDocs, d)
		}
	}
	return agentDocs, crewDocs, blocked
}

// unreadableCount counts source files the reader could not open, as
// opposed to ones it deliberately left behind.
func unreadableCount(plan memport.Plan) int {
	n := 0
	for _, s := range plan.Skipped {
		if strings.HasPrefix(s.Reason, "unreadable:") {
			n++
		}
	}
	return n
}

// printImportPlan renders the mapping an operator is being asked to
// approve. Sources are shown per target because the interesting
// question is never "what did you read" but "what is about to become
// my agent's long-term memory, and where did it come from".
func printImportPlan(plan memport.Plan, agentSlug string, blocked []memport.Skip) {
	target := "crew-shared memory"
	if agentSlug != "" {
		target = "agent " + agentSlug
	}
	fmt.Fprintf(os.Stdout, "Detected format: %s\nTarget: %s\n\n", plan.Format, target)
	for _, d := range plan.Docs {
		fmt.Fprintf(os.Stdout, "  %-28s  %6d bytes  [%s]\n", d.RelPath, len(d.Body), d.Tier)
		for _, s := range d.Sources {
			fmt.Fprintf(os.Stdout, "      <- %s\n", s)
		}
	}
	notImported := append(append([]memport.Skip{}, blocked...), plan.Skipped...)
	if len(notImported) > 0 {
		fmt.Fprintf(os.Stdout, "\nNot imported (%d):\n", len(notImported))
		for _, s := range notImported {
			fmt.Fprintf(os.Stdout, "  %-40s %s\n", s.Source, s.Reason)
		}
	}
}

func init() {
	memoryExportCmd.Flags().String("out", "", "destination directory for the bundle (required)")
	memoryExportCmd.Flags().String("crew", "", "crew slug or id (required)")
	memoryExportCmd.Flags().String("agent", "", "agent slug; omit for the crew-shared tier")

	memoryImportCmd.Flags().String("from", "", "source memory directory (required)")
	memoryImportCmd.Flags().String("crew", "", "target crew slug or id (required)")
	memoryImportCmd.Flags().String("agent", "", "target agent slug; omit for the crew-shared tier")
	memoryImportCmd.Flags().String("format", "auto", "source layout: auto|crewship|okf|nanoclaw|openclaw")
	memoryImportCmd.Flags().String("operator", "", "slug of the person an operator card (OpenClaw USER.md) belongs to")
	memoryImportCmd.Flags().String("group", "", "which NanoClaw group to import when the source holds several")
	memoryImportCmd.Flags().Bool("with-crew", false, "also write crew-shared documents (every agent in the crew reads them)")
	memoryImportCmd.Flags().Bool("apply", false, "actually write; without it the command only prints the plan")

	memoryCmd.AddCommand(memoryExportCmd)
	memoryCmd.AddCommand(memoryImportCmd)
}
