package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"
)

// routineDoctorCmd is the preflight-check command. The "blind alley"
// problem with routines is that a /run can fail many distinct ways
// (auth, provisioning, missing agent slug, missing credential, gate
// unsatisfiable, cost cap, egress allowlist) and each surfaces as
// a different terse error. Doctor walks every check ahead of time,
// emits a ✓/⚠/✗ checklist, and tells the operator what to fix.
//
// Designed for two audiences:
//
//  1. Operator about to /run a new routine for the first time —
//     catches "I forgot to provision the crew" / "agent slug typo"
//     before the LLM call is wasted.
//  2. CI step before running the eval suite — fail-fast if the
//     target workspace isn't healthy, with a structured report
//     that's grep-able for what to fix.
var routineDoctorCmd = &cobra.Command{
	Use:   "doctor <slug>",
	Short: "Preflight diagnostics for a routine — catches blind alleys before /run",
	Long: `Walk every checkable precondition for a routine and report ✓/⚠/✗.

Checks performed:
  - Routine exists in the workspace
  - Author crew exists and is provisioned (image cached / agent containers ready)
  - Every step's agent_slug resolves to an agent in the author crew
  - Every outcomes.grader_agent_slug resolves
  - Every credentials_required type has an active workspace credential
  - egress_targets sanity (no wildcards, no localhost)
  - max_cost_usd vs estimated_cost_usd sanity (cap > estimate)
  - Validation gates aren't structurally impossible (min > max, empty must_contain)

Examples:
  crewship routine doctor eval-extract-emails
  crewship routine doctor eval-judge-cross-family -f json   # CI-friendly`,
	Args: cobra.ExactArgs(1),
	RunE: runRoutineDoctor,
}

// doctorLevel is the severity tier surfaced in the checklist line
// and in the JSON output. Only FAIL counts for the non-zero exit
// code so flag-level warnings (cost cap might be tight) don't break
// CI.
type doctorLevel string

const (
	doctorOK   doctorLevel = "OK"
	doctorWarn doctorLevel = "WARN"
	doctorFail doctorLevel = "FAIL"
)

// doctorCheck is one row in the diagnostic report. Hint is shown
// inline when non-empty so the operator gets the fix on the same
// line as the symptom.
type doctorCheck struct {
	Name    string      `json:"name"`
	Level   doctorLevel `json:"level"`
	Message string      `json:"message"`
	Hint    string      `json:"hint,omitempty"`
}

// doctorReport is the top-level structure the JSON output emits;
// table mode flattens to per-row lines.
type doctorReport struct {
	Slug        string        `json:"slug"`
	WorkspaceID string        `json:"workspace_id"`
	Checks      []doctorCheck `json:"checks"`
	Failed      int           `json:"failed"`
	Warned      int           `json:"warned"`
	Passed      int           `json:"passed"`
}

// doctorHTTPGetter is the minimal client surface every doctor check
// needs. Narrowed from *cli.Client so check functions stay testable
// with a hand-rolled stub.
type doctorHTTPGetter interface {
	Get(string) (*http.Response, error)
}

// doctorChecker bundles a check name with the closure that produces
// its report rows. The runner iterates the slice so adding a new
// check is one entry, not a new branch in runRoutineDoctor.
type doctorChecker struct {
	name string
	run  func() []doctorCheck
}

func runRoutineDoctor(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	slug := args[0]
	client := newAPIClient()
	ws := client.GetWorkspaceID()

	report := doctorReport{Slug: slug, WorkspaceID: ws}

	// Fetch routine; everything else cascades from this. If we can't
	// find it, suggest similar slugs and short-circuit.
	pipeline, ok := fetchRoutineForDoctor(client, ws, slug)
	if !ok {
		hint := suggestSimilarRoutineSlugs(client, ws, slug)
		report.Checks = append(report.Checks, doctorCheck{
			Name:    "routine_exists",
			Level:   doctorFail,
			Message: fmt.Sprintf("routine %q not found in workspace", slug),
			Hint:    hint,
		})
		return finishDoctorReport(cmd, report)
	}
	report.Checks = append(report.Checks, doctorCheck{
		Name:    "routine_exists",
		Level:   doctorOK,
		Message: fmt.Sprintf("routine found (author_crew_id=%s)", truncCrewID(pipeline.AuthorCrewID)),
	})

	checks := []doctorChecker{
		{"author_crew", func() []doctorCheck { return []doctorCheck{checkAuthorCrew(client, pipeline.AuthorCrewID)} }},
		{"agent_slugs", func() []doctorCheck { return checkAgentSlugs(client, pipeline.AuthorCrewID, pipeline.Definition) }},
		{"credentials_required", func() []doctorCheck { return checkCredentialsRequired(client, ws, pipeline.Definition) }},
		{"egress_allowlist", func() []doctorCheck { return checkEgressTargets(pipeline.Definition) }},
		{"cost_cap", func() []doctorCheck { return []doctorCheck{checkCostCap(pipeline.Definition)} }},
		{"validation_gates", func() []doctorCheck { return checkValidationGates(pipeline.Definition) }},
	}
	for _, check := range checks {
		report.Checks = append(report.Checks, check.run()...)
	}

	return finishDoctorReport(cmd, report)
}

// finishDoctorReport tallies levels, renders, and returns a non-zero
// error when any check FAILed (CI-friendly). WARN does not propagate
// to exit code — it's an operator hint, not a blocker.
func finishDoctorReport(cmd *cobra.Command, report doctorReport) error {
	for _, c := range report.Checks {
		switch c.Level {
		case doctorOK:
			report.Passed++
		case doctorWarn:
			report.Warned++
		case doctorFail:
			report.Failed++
		}
	}

	f := newFormatter()
	if f.Format == "json" {
		_ = f.JSON(report)
	} else {
		printDoctorTable(cmd, report)
	}

	if report.Failed > 0 {
		return fmt.Errorf("%d check(s) failed", report.Failed)
	}
	return nil
}

// printDoctorTable renders the report as a compact ✓/⚠/✗ list.
// Hint text is on a continuation line at slight indent so the
// eye tracks the row→hint pairing.
func printDoctorTable(cmd *cobra.Command, report doctorReport) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "\nDoctor: %s\n\n", report.Slug)
	for _, c := range report.Checks {
		var sym string
		switch c.Level {
		case doctorOK:
			sym = "✓"
		case doctorWarn:
			sym = "⚠"
		case doctorFail:
			sym = "✗"
		}
		fmt.Fprintf(out, "  %s %-30s %s\n", sym, c.Name, c.Message)
		if c.Hint != "" {
			fmt.Fprintf(out, "    → %s\n", c.Hint)
		}
	}
	fmt.Fprintf(out, "\n  Summary: %d passed, %d warning(s), %d failed\n",
		report.Passed, report.Warned, report.Failed)
}

// fetchedRoutine is the minimal shape doctor needs from
// /api/v1/workspaces/{ws}/pipelines/{slug}. We re-decode rather
// than reuse a server-side struct to avoid coupling the CLI to
// internal types.
type fetchedRoutine struct {
	Slug          string                 `json:"slug"`
	AuthorCrewID  string                 `json:"author_crew_id"`
	Definition    map[string]interface{} `json:"definition_parsed"`
	DefinitionRaw json.RawMessage        `json:"definition_json"`
}

func fetchRoutineForDoctor(client doctorHTTPGetter, ws, slug string) (fetchedRoutine, bool) {
	// Both ws and slug are escaped as path segments. Mirrors the
	// fix in slug_suggest.go (CR2): a workspace id or routine slug
	// containing reserved characters (slash, hash, %) would
	// otherwise produce a malformed request that 404s under a
	// confusing path instead of failing on the actual lookup.
	resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s", url.PathEscape(ws), url.PathEscape(slug)))
	if err != nil || resp == nil {
		return fetchedRoutine{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fetchedRoutine{}, false
	}
	// Server returns the routine row + parsed definition map.
	// Different deployments may shape this slightly differently;
	// we accept both flat and nested forms by trying a couple.
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return fetchedRoutine{}, false
	}
	out := fetchedRoutine{}
	if v, ok := raw["slug"]; ok {
		_ = json.Unmarshal(v, &out.Slug)
	}
	if v, ok := raw["author_crew_id"]; ok {
		_ = json.Unmarshal(v, &out.AuthorCrewID)
	}
	// Production endpoint returns a parsed `definition` object.
	// Older / alternate shapes used `definition_parsed` / a raw
	// `definition_json` string; we accept all three so doctor
	// stays useful across server versions.
	switch {
	case raw["definition"] != nil:
		_ = json.Unmarshal(raw["definition"], &out.Definition)
	case raw["definition_parsed"] != nil:
		_ = json.Unmarshal(raw["definition_parsed"], &out.Definition)
	case raw["definition_json"] != nil:
		var s string
		if json.Unmarshal(raw["definition_json"], &s) == nil {
			_ = json.Unmarshal([]byte(s), &out.Definition)
		}
	}
	return out, true
}

// optionalInt extracts an int from a JSON-decoded map; numbers come
// in as float64 so we coerce. Returns nil when the key is missing
// or the value is the wrong shape.
func optionalInt(m map[string]interface{}, key string) *int {
	v, ok := m[key]
	if !ok {
		return nil
	}
	switch x := v.(type) {
	case float64:
		i := int(x)
		return &i
	case int:
		return &x
	default:
		return nil
	}
}

// truncCrewID shortens a long ID to its first 12 RUNES (not bytes)
// for display. Keeps doctor output readable on terminals that wrap;
// the full ID is in the JSON output for tooling that needs it.
//
// Slicing by runes avoids splitting multi-byte UTF-8 mid-sequence
// if a future ID scheme stops being ASCII-only. Mirrors the
// rune-safe truncate() in internal/pipeline/executor.go.
func truncCrewID(id string) string {
	runes := []rune(id)
	if len(runes) <= 12 {
		return id
	}
	return string(runes[:12]) + "…"
}

func init() {
	pipelineCmd.AddCommand(routineDoctorCmd)
}
