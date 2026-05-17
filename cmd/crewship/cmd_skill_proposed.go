package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// cmd_skill_proposed wires the operator-side CLI for the memory→Skills
// HITL surface (PRD §8.2). Three verbs mirror the HTTP endpoints:
//
//   crewship skill proposed list --crew <crew_id>
//   crewship skill proposed approve --crew <crew_id> --file skill-<slug>.md
//   crewship skill proposed reject  --crew <crew_id> --file skill-<slug>.md
//
// These hit /api/v1/skills/proposed[/approve|/reject] on the daemon
// over the same auth path as `crewship skill list` — so the surface
// feels like one continuous command family rather than a separate tool.

var skillProposedCmd = &cobra.Command{
	Use:   "proposed",
	Short: "Review skills auto-promoted from memory awaiting approval",
	Long: `Operate the HITL surface for the memory→Skills bridge.

When a learned rule in the consolidator accumulates sustained recall
(default ≥ 10 recall events, composite score ≥ 0.85), it gets staged
as an Anthropic-format SKILL.md under .proposed/skill-<slug>.md. This
subcommand lets an operator list those proposals, approve them (which
imports the SKILL.md through the canonical skill importer), or
reject them (which deletes the staging file).

Three commands:
  list     - List staged skills for a crew
  approve  - Import a staged skill into the workspace registry
  reject   - Discard a staged skill (delete the staging file)

All three respect the canonical OWNER/ADMIN/MANAGER permission gate.`,
}

var skillProposedListCmd = &cobra.Command{
	Use:   "list",
	Short: "List staged skill proposals for a crew",
	Long: `List the staged SKILL.md proposals waiting under .proposed/ for the
named crew. Output is a table by default (--format=text) or JSON
(--format=json) for piping into jq.

Example:
  crewship skill proposed list --crew crew_abc123`,
	RunE: runSkillProposedList,
}

var skillProposedApproveCmd = &cobra.Command{
	Use:   "approve",
	Short: "Approve a staged skill — import it and remove the staging file",
	Long: `Import the named staging file through the canonical skill importer
(same path as URL-based imports — SPDX license check + prompt
injection scan apply). On success the staging file is deleted and
an EntryMemorySkillApproved journal entry fires.

Example:
  crewship skill proposed approve --crew crew_abc --file skill-deploy-friday.md`,
	RunE: runSkillProposedApprove,
}

var skillProposedRejectCmd = &cobra.Command{
	Use:   "reject",
	Short: "Reject a staged skill — discard the staging file",
	Long: `Delete the staging file without importing. Idempotent: rejecting an
already-deleted file returns success (no error). An
EntryMemorySkillRejected journal entry fires either way.

Example:
  crewship skill proposed reject --crew crew_abc --file skill-noise.md`,
	RunE: runSkillProposedReject,
}

func init() {
	skillProposedListCmd.Flags().String("crew", "", "crew id whose .proposed/ dir to scan (required)")
	skillProposedListCmd.Flags().String("format", "text", "output format: text|json")
	_ = skillProposedListCmd.MarkFlagRequired("crew")

	skillProposedApproveCmd.Flags().String("crew", "", "crew id (required)")
	skillProposedApproveCmd.Flags().String("file", "", "staged file name, e.g. skill-foo.md (required)")
	_ = skillProposedApproveCmd.MarkFlagRequired("crew")
	_ = skillProposedApproveCmd.MarkFlagRequired("file")

	skillProposedRejectCmd.Flags().String("crew", "", "crew id (required)")
	skillProposedRejectCmd.Flags().String("file", "", "staged file name, e.g. skill-foo.md (required)")
	_ = skillProposedRejectCmd.MarkFlagRequired("crew")
	_ = skillProposedRejectCmd.MarkFlagRequired("file")

	skillProposedCmd.AddCommand(skillProposedListCmd)
	skillProposedCmd.AddCommand(skillProposedApproveCmd)
	skillProposedCmd.AddCommand(skillProposedRejectCmd)
	skillCmd.AddCommand(skillProposedCmd)
}

// proposedSkillRow mirrors the api.ProposedSkillSummary JSON shape.
// Duplicated here rather than imported so the CLI binary doesn't pull
// in the internal/api package's HTTP dependencies.
type proposedSkillRow struct {
	FileName           string `json:"file_name"`
	Name               string `json:"name"`
	Description        string `json:"description"`
	DescriptionQuality string `json:"description_quality"`
	Category           string `json:"category"`
}

func runSkillProposedList(cmd *cobra.Command, _ []string) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	crewID, _ := cmd.Flags().GetString("crew")
	format, _ := cmd.Flags().GetString("format")

	client := newAPIClient()
	// URL-encode the crew_id so a value with `&` or `?` doesn't
	// silently change query semantics. Crew ids are CUID-format
	// today (no special chars), but the encoding is cheap and the
	// safety case is non-hypothetical for any future id scheme.
	q := url.Values{}
	q.Set("crew_id", crewID)
	resp, err := client.Get("/api/v1/skills/proposed?" + q.Encode())
	if err != nil {
		return err
	}
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	var rows []proposedSkillRow
	if err := cli.ReadJSON(resp, &rows); err != nil {
		return err
	}

	if format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}

	if len(rows) == 0 {
		fmt.Fprintf(os.Stderr, "no staged skill proposals for crew %s\n", crewID)
		return nil
	}
	f := newFormatter()
	headers := []string{"FILE", "NAME", "CATEGORY", "DESCRIPTION", "QUALITY"}
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		desc := r.Description
		if len(desc) > 60 {
			desc = desc[:57] + "..."
		}
		quality := r.DescriptionQuality
		if quality == "" {
			quality = "ok"
		}
		out = append(out, []string{r.FileName, r.Name, r.Category, desc, quality})
	}
	f.Table(headers, out)
	return nil
}

// proposedRequest mirrors api.approveBody. Used for both approve and
// reject — the wire format is identical.
type proposedRequest struct {
	CrewID   string `json:"crew_id"`
	FileName string `json:"file_name"`
}

func runSkillProposedApprove(cmd *cobra.Command, _ []string) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	crewID, _ := cmd.Flags().GetString("crew")
	fileName, _ := cmd.Flags().GetString("file")

	client := newAPIClient()
	resp, err := client.Post("/api/v1/skills/proposed/approve", proposedRequest{CrewID: crewID, FileName: fileName})
	if err != nil {
		return err
	}
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	var out struct {
		SkillID  string `json:"skill_id"`
		Slug     string `json:"slug"`
		Created  bool   `json:"created"`
		FileName string `json:"file_name"`
	}
	if err := cli.ReadJSON(resp, &out); err != nil {
		return err
	}
	verb := "updated"
	if out.Created {
		verb = "created"
	}
	fmt.Printf("approved %s -> skill %s (%s, %s)\n", fileName, out.SkillID, out.Slug, verb)
	return nil
}

func runSkillProposedReject(cmd *cobra.Command, _ []string) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	crewID, _ := cmd.Flags().GetString("crew")
	fileName, _ := cmd.Flags().GetString("file")

	client := newAPIClient()
	resp, err := client.Post("/api/v1/skills/proposed/reject", proposedRequest{CrewID: crewID, FileName: fileName})
	if err != nil {
		return err
	}
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	var out struct {
		FileName string `json:"file_name"`
		Removed  bool   `json:"removed"`
	}
	if err := cli.ReadJSON(resp, &out); err != nil {
		return err
	}
	if out.Removed {
		fmt.Printf("rejected %s — staging file removed\n", out.FileName)
	} else {
		fmt.Printf("rejected %s — staging file was already gone (idempotent)\n", out.FileName)
	}
	return nil
}
