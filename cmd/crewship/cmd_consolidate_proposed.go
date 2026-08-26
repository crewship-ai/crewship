package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// `crewship consolidate proposed` — the human-in-the-loop half of memory
// consolidation. `consolidate run` triggers the extraction; these four verbs
// are what an operator does with the result before it becomes canonical crew
// memory.
//
// Two things about this surface that the commands have to state out loud,
// because neither is discoverable from the API:
//
//  1. Proposals only exist when the consolidator ran in ProposalMode, which is
//     `CREWSHIP_CONSOLIDATE_HITL=1` and off by default
//     (internal/consolidate/runner.go:459). With it unset the table is empty
//     and every one of these commands can only ever 404. That is a
//     configuration answer, not a bug, and the help says so rather than
//     letting an operator conclude the feature is broken.
//
//  2. There is no list endpoint. `memory_proposals` rows are addressed by id
//     and nothing enumerates them — the one place an id surfaces is the inbox
//     item written alongside the proposal (internal/consolidate/proposed.go:138),
//     kind `memory_consolidation`, with the id in `payload.proposal_id`. So the
//     entry point is `crewship inbox list --kind memory_consolidation`, and the
//     help says that too.
//
// `explain` and `diff` had no consumer at all before this — not the CLI, and
// not the web UI, which only wires the approve/reject buttons.

var consolidateProposedCmd = &cobra.Command{
	Use:     "proposed",
	Aliases: []string{"proposal", "proposals"},
	Short:   "Review, approve or reject staged memory-consolidation proposals",
	Long: `Review the memory-consolidation proposals staged for human approval.

When the consolidator runs with CREWSHIP_CONSOLIDATE_HITL=1 it does not write
extracted rules straight into a crew's canonical learned-*.md. It stages them
in .proposed/, records a pending row, and raises an inbox item — and these
commands are the decision side of that.

Finding a proposal id. There is no list endpoint; the id travels on the inbox
item the proposal raises:

  crewship inbox list --kind memory_consolidation

Reviewing one, in the order that keeps you honest:

  crewship consolidate proposed explain <id>   # why: rule count, evidence, scores
  crewship consolidate proposed diff <id>      # what: the exact bytes it would append
  crewship consolidate proposed approve <id>   # merge into the canonical file
  crewship consolidate proposed reject <id> --reason "over-fitted"

Approving merges the staged rules into the crew's canonical learned-*.md and
records a new memory version. It is a write to what every future run of that
crew reads, so it asks first; --yes skips the prompt.

If nothing is ever proposed, check CREWSHIP_CONSOLIDATE_HITL — unset (the
default) means the consolidator writes directly and stages nothing.`,
}

// proposalExplanation mirrors consolidate.ProposalExplanation. Evidence and
// scores stay RawMessage: they are opaque per-run JSON blobs and flattening
// them to a string would lose the shape a script wants to index.
type proposalExplanation struct {
	ProposalID      string          `json:"proposal_id"`
	WorkspaceID     string          `json:"workspace_id"`
	CrewID          string          `json:"crew_id"`
	Status          string          `json:"status"`
	ProposalPath    string          `json:"proposal_path"`
	RulesCount      int             `json:"rules_count"`
	EntriesScanned  int             `json:"entries_scanned"`
	CreatedAt       string          `json:"created_at"`
	DecidedAt       string          `json:"decided_at,omitempty"`
	DecidedByUserID string          `json:"decided_by_user_id,omitempty"`
	Evidence        json.RawMessage `json:"evidence,omitempty"`
	Scores          json.RawMessage `json:"scores,omitempty"`
}

type proposalDiff struct {
	ProposalID      string `json:"proposal_id"`
	WorkspaceID     string `json:"workspace_id"`
	CrewID          string `json:"crew_id"`
	Status          string `json:"status"`
	CanonicalPath   string `json:"canonical_path"`
	CanonicalExists bool   `json:"canonical_exists"`
	ProposalPath    string `json:"proposal_path"`
	RulesCount      int    `json:"rules_count"`
	Diff            string `json:"diff"`
	Stats           struct {
		Additions     int `json:"additions"`
		Deletions     int `json:"deletions"`
		RulesAppended int `json:"rules_appended"`
	} `json:"stats"`
}

// proposedPath builds the review path for one proposal id.
func proposedPath(id, verb string) string {
	return "/api/v1/consolidate/proposed/" + url.PathEscape(id) + "/" + verb
}

var consolidateProposedExplainCmd = &cobra.Command{
	Use:   "explain <proposal-id>",
	Short: "Show why a proposal was raised: rule count, evidence and scores",
	Long: `Show the provenance of a staged proposal — which crew it came from, how many
journal entries were scanned, how many rules were extracted, the evidence they
rest on and the scores the consolidator assigned.

Read-only, MEMBER and above. Pair it with ` + "`diff`" + `: explain says why,
diff says what.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		var out proposalExplanation
		if err := getProposalJSON(proposedPath(args[0], "explain"), &out); err != nil {
			return err
		}

		f := newFormatter()
		pairs := [][]string{
			{"Proposal", out.ProposalID},
			{"Status", out.Status},
			{"Crew", out.CrewID},
			{"Rules", fmt.Sprintf("%d", out.RulesCount)},
			{"Entries scanned", fmt.Sprintf("%d", out.EntriesScanned)},
			{"Staged at", out.ProposalPath},
			{"Created", out.CreatedAt},
		}
		if out.DecidedAt != "" {
			pairs = append(pairs,
				[]string{"Decided", out.DecidedAt},
				[]string{"Decided by", out.DecidedByUserID})
		}
		if len(out.Scores) > 0 && string(out.Scores) != "{}" {
			pairs = append(pairs, []string{"Scores", compactJSON(out.Scores)})
		}
		if len(out.Evidence) > 0 && string(out.Evidence) != "{}" {
			pairs = append(pairs, []string{"Evidence", compactJSON(out.Evidence)})
		}
		return f.AutoDetail(out, pairs)
	},
}

var consolidateProposedDiffCmd = &cobra.Command{
	Use:   "diff <proposal-id>",
	Short: "Preview the exact bytes approving this proposal would append",
	Long: `Show the unified diff between a crew's canonical learned-*.md and what it
would become if this proposal were approved.

The server generates this preview from the same code path approve writes
through, so what you see here is byte-for-byte what lands. Read-only,
MEMBER and above.

A proposal whose staged markdown has been deleted answers 410; one larger than
8 MiB answers 413.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		var out proposalDiff
		if err := getProposalJSON(proposedPath(args[0], "diff"), &out); err != nil {
			return err
		}

		f := newFormatter()
		return f.AutoHuman(out, func() { printProposalDiff(out) })
	},
}

// printProposalDiff renders the human view of a diff response. Shared with
// `approve --diff`, where it is the preview shown before the prompt.
func printProposalDiff(d proposalDiff) {
	target := d.CanonicalPath
	if !d.CanonicalExists {
		target += " (new file)"
	}
	fmt.Printf("%s → %s\n", d.ProposalPath, target)
	fmt.Printf("%d rules, +%d/-%d lines\n\n", d.Stats.RulesAppended, d.Stats.Additions, d.Stats.Deletions)
	if strings.TrimSpace(d.Diff) == "" {
		fmt.Println("(no textual change)")
		return
	}
	fmt.Println(strings.TrimRight(d.Diff, "\n"))
}

var consolidateProposedApproveCmd = &cobra.Command{
	Use:   "approve <proposal-id>",
	Short: "Merge a staged proposal into the crew's canonical memory",
	Long: `Approve a staged proposal: merge its rules into the crew's canonical
learned-*.md, record a new memory version, and resolve the inbox item.

This is a write to what every future run of that crew reads, so it prompts
before acting. --yes skips the prompt; --diff prints the exact change first,
which is the pairing worth using interactively.

OWNER or ADMIN only. A proposal that was already approved or rejected answers
409 — decisions are not reversible from here.

Examples:
  crewship consolidate proposed approve mp_20260826T101500-abcdef0123456789 --diff
  crewship consolidate proposed approve mp_20260826T101500-abcdef0123456789 --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		id := args[0]

		// The preview has to happen before the prompt, or it previews nothing.
		if showDiff, _ := cmd.Flags().GetBool("diff"); showDiff {
			var d proposalDiff
			if err := getProposalJSON(proposedPath(id, "diff"), &d); err != nil {
				return err
			}
			printProposalDiff(d)
			fmt.Println()
		}

		if err := confirmAction(cmd,
			fmt.Sprintf("Merge proposal %s into the crew's canonical memory?", id)); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Post(proposedPath(id, "approve"), map[string]string{})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var out struct {
			ProposalID    string `json:"proposal_id"`
			CanonicalPath string `json:"canonical_path"`
			RulesMerged   int    `json:"rules_merged"`
			WorkspaceID   string `json:"workspace_id"`
			CrewID        string `json:"crew_id"`
			DecidedBy     string `json:"decided_by"`
			VersionSHA    string `json:"version_sha"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}

		f := newFormatter()
		return f.AutoHuman(out, func() {
			cli.PrintSuccess(fmt.Sprintf("Merged %d rule(s) into %s", out.RulesMerged, out.CanonicalPath))
			if out.VersionSHA != "" {
				fmt.Printf("Memory version: %s\n", out.VersionSHA)
			}
		})
	},
}

var consolidateProposedRejectCmd = &cobra.Command{
	Use:   "reject <proposal-id>",
	Short: "Reject a staged proposal without touching canonical memory",
	Long: `Reject a staged proposal. The canonical learned-*.md is left untouched and
the inbox item resolves as rejected.

--reason is sent and echoed back, but the server does not persist it yet —
memory_proposals has no reason column (internal/api/consolidate_proposed_handler.go).
Put anything you need to keep in the crew's journal instead.

OWNER or ADMIN only. Already-decided proposals answer 409.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		id := args[0]
		if err := confirmAction(cmd, fmt.Sprintf("Reject proposal %s?", id)); err != nil {
			return err
		}

		body := map[string]string{}
		reason, _ := cmd.Flags().GetString("reason")
		if reason != "" {
			body["reason"] = reason
		}

		client := newAPIClient()
		resp, err := client.Post(proposedPath(id, "reject"), body)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var out struct {
			ProposalID string `json:"proposal_id"`
			Status     string `json:"status"`
			DecidedBy  string `json:"decided_by"`
			Reason     string `json:"reason"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}

		f := newFormatter()
		return f.AutoHuman(out, func() {
			cli.PrintSuccess(fmt.Sprintf("Proposal %s rejected; canonical memory unchanged", out.ProposalID))
			if reason != "" {
				fmt.Println("The reason was sent but is not stored server-side — " +
					"memory_proposals has no column for it yet.")
			}
		})
	},
}

// getProposalJSON runs a GET against a proposal review path and decodes it.
// The three read verbs share the same error mapping, and cli.CheckError is
// what turns the server's 404 into ExitNotFound rather than ExitGeneric.
func getProposalJSON(path string, out any) error {
	client := newAPIClient()
	resp, err := client.Get(path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	return cli.ReadJSON(resp, out)
}

// compactJSON renders an opaque JSON blob on one line for the detail view.
// Invalid JSON is passed through rather than swallowed — a malformed blob is
// something the operator should see, not something to hide behind "-".
func compactJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

func init() {
	consolidateProposedApproveCmd.Flags().Bool("diff", false,
		"Print the exact change this would make before asking to confirm")
	consolidateProposedApproveCmd.Flags().Bool("yes", false, "Skip the confirmation prompt")
	consolidateProposedRejectCmd.Flags().String("reason", "",
		"Why it is being rejected (sent to the server, not persisted there yet)")
	consolidateProposedRejectCmd.Flags().Bool("yes", false, "Skip the confirmation prompt")

	consolidateProposedCmd.AddCommand(consolidateProposedExplainCmd)
	consolidateProposedCmd.AddCommand(consolidateProposedDiffCmd)
	consolidateProposedCmd.AddCommand(consolidateProposedApproveCmd)
	consolidateProposedCmd.AddCommand(consolidateProposedRejectCmd)

	consolidateCmd.AddCommand(consolidateProposedCmd)
}
