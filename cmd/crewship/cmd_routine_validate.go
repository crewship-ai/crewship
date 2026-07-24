package main

// Routine validate subcommand. Validates a routine DSL file LOCALLY
// without contacting the server — useful for editor-loop iteration,
// CI gates, and pre-commit hooks. Catches everything Parse + Validate
// catches at save-time except cross-routine cycle detection (which
// needs a workspace-wide call graph the local CLI can't see).

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/spf13/cobra"
)

var routineValidateCmd = &cobra.Command{
	Use:   "validate [file.json|file.yaml]",
	Short: "Validate a routine DSL file offline (no server call)",
	Long: `Parses + validates a routine DSL file locally — JSON or YAML, sniffed from
the content, not the extension. Reports the same errors the server would on
save, except cross-routine cycle detection (which needs the workspace's
full call graph). Exit code 0 = valid, 1 = invalid.

YAML input gets comments and real multiline strings instead of JSON's
"\n"-escaped ones — useful for a long agent_run prompt. It's converted to
canonical JSON before validation; the JSON-pointer paths in error messages
and the definition saved to the server are unaffected either way.

Reads from the given file argument, or from stdin if no argument:
  crewship routine validate routine.json
  crewship routine validate routine.yaml
  cat routine.json | crewship routine validate

Use in CI:
  - name: Validate routine DSL
    run: crewship routine validate routines/email-fetch.json

The local check runs:
  1. JSON parse
  2. Schema validation (required fields, step types, slug format)
  3. Template reference checks (inputs.X / steps.Y.output)
  4. Step ID uniqueness, needs[] references valid
  5. JSON Schema subset checks on validation blocks
  6. concurrency_key input bindings (referenced inputs are required or defaulted)
  7. Unsatisfiable output gates (min_length>max_length, must_contain∩must_not_contain)
  8. Dead egress_targets entries — '*'/'*.*'/empty host match no host at run time

Resolve agent_slug references offline so typos fail here, not at save:
  crewship routine validate r.json --agents triage,writer
  crewship routine validate r.json --author-crew growth   # one server call

Server-side checks not run locally:
  - Cross-routine call_pipeline cycle detection (needs workspace)
  - Slug uniqueness against existing routines (needs DB)
  - Agent slug existence, unless --agents/--author-crew is given
`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var raw []byte
		var src string
		if len(args) == 1 {
			b, err := os.ReadFile(args[0])
			if err != nil {
				return fmt.Errorf("read file: %w", err)
			}
			raw = b
			src = args[0]
		} else {
			b, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("read stdin: %w", err)
			}
			if len(b) == 0 {
				return fmt.Errorf("empty input (no file argument and stdin is empty)")
			}
			raw = b
			src = "<stdin>"
		}

		// #1423 item 2: accept YAML as well as JSON — comments and real
		// multiline block-scalar strings (`prompt: |`) instead of
		// JSON-escaped "\n". Pass-through for JSON input; converted to
		// canonical JSON here so everything downstream (Parse, Validate,
		// the JSON-pointer paths in validation errors) is unaffected.
		raw, err := pipeline.ToCanonicalJSON(raw)
		if err != nil {
			return printValidationError(src, "parse", err)
		}

		// Resolve the agent-slug set the validator checks agent_slug
		// references against. Default (no flags) is nil → the existence
		// check is skipped and validate stays fully offline, as before.
		// --agents enumerates the crew roster locally (airgapped/CI);
		// --author-crew fetches it with one server call so typos fail
		// pre-save instead of at save. A flag/auth error is returned
		// as a normal cobra error (not an exit-1 validation failure).
		agentSlugs, err := resolveValidateAgentSlugs(cmd)
		if err != nil {
			return err
		}

		dsl, err := pipeline.Parse(raw)
		if err != nil {
			return printValidationError(src, "parse", err)
		}
		if err := pipeline.Validate(dsl, agentSlugs, nil); err != nil {
			return printValidationError(src, "validate", err)
		}

		// Local cycle pre-check: catches direct self-references and
		// in-DSL loops (call_pipeline a → a, or a → b → a embedded in
		// a single bundle). Workspace-wide cycle detection still runs
		// at save.
		visited := make(map[string]bool, len(dsl.Steps))
		for _, s := range dsl.Steps {
			if visited[s.ID] {
				return printValidationError(src, "validate", fmt.Errorf("step ID %q used twice", s.ID))
			}
			visited[s.ID] = true
		}

		f := resolvedFormatter(cmd)
		if f.Format == "json" || f.Format == "yaml" || f.Format == "ndjson" {
			payload := map[string]interface{}{
				"source":      src,
				"valid":       true,
				"name":        dsl.Name,
				"step_count":  len(dsl.Steps),
				"input_count": len(dsl.Inputs),
				"step_types":  collectStepTypes(dsl),
			}
			switch f.Format {
			case "yaml":
				return f.YAML(payload)
			case "ndjson":
				return f.NDJSON(payload)
			default:
				return f.JSON(payload)
			}
		}
		fmt.Printf("✓ %s is a valid routine DSL.\n", src)
		fmt.Printf("  Name:      %s\n", dsl.Name)
		fmt.Printf("  Steps:     %d (%s)\n", len(dsl.Steps), strings.Join(collectStepTypes(dsl), ", "))
		fmt.Printf("  Inputs:    %d\n", len(dsl.Inputs))
		if len(dsl.Outputs) > 0 {
			fmt.Printf("  Outputs:   %d\n", len(dsl.Outputs))
		}
		if len(dsl.EgressTargets) > 0 {
			fmt.Printf("  Egress:    %s\n", strings.Join(dsl.EgressTargets, ", "))
		}
		if ir := dsl.NormalizedIntegrationsRequired(); len(ir) > 0 {
			fmt.Printf("  Integr.:   %s\n", strings.Join(ir, ", "))
		}
		fmt.Println("Save with: crewship routine save --definition <file> --author-crew <crew-slug-or-id>")
		return nil
	},
}

// resolveValidateAgentSlugs turns the --agents / --author-crew flags into the
// agent-slug set pipeline.Validate resolves agent_slug references against.
//
//	neither flag     → nil  (skip the existence check; validate stays offline)
//	--agents a,b,c   → {a,b,c}  (airgapped: caller enumerates the roster)
//	--author-crew C  → the crew's live roster (one GET /api/v1/agents call)
//
// The two flags are mutually exclusive — --agents is the offline override,
// --author-crew is the server-backed convenience; combining them is ambiguous.
func resolveValidateAgentSlugs(cmd *cobra.Command) (map[string]struct{}, error) {
	crew, _ := cmd.Flags().GetString("author-crew")
	agents, _ := cmd.Flags().GetStringSlice("agents")
	if crew != "" && len(agents) > 0 {
		return nil, fmt.Errorf("--author-crew and --agents are mutually exclusive (pick one)")
	}
	if len(agents) > 0 {
		return slugSet(agents), nil
	}
	if crew != "" {
		return fetchCrewAgentSlugs(crew)
	}
	return nil, nil
}

// slugSet builds a lookup set from a --agents list, trimming whitespace and
// dropping empties (so "a, b, ,c" → {a,b,c}).
func slugSet(list []string) map[string]struct{} {
	out := make(map[string]struct{}, len(list))
	for _, s := range list {
		if s = strings.TrimSpace(s); s != "" {
			out[s] = struct{}{}
		}
	}
	return out
}

// fetchCrewAgentSlugs returns the live agent-slug roster for a crew. This is
// the ONE place `routine validate` contacts the server — gated behind the
// --author-crew flag — so the default validate stays offline.
func fetchCrewAgentSlugs(crew string) (map[string]struct{}, error) {
	if err := requireAuth(); err != nil {
		return nil, err
	}
	if err := requireWorkspace(); err != nil {
		return nil, err
	}
	client := newAPIClient()
	crewID, err := resolveCrewID(client, crew)
	if err != nil {
		return nil, err
	}
	resp, err := client.Get("/api/v1/agents?crew_id=" + crewID)
	if err != nil {
		return nil, err
	}
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var agents []agentListItem
	if err := cli.ReadJSON(resp, &agents); err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(agents))
	for _, a := range agents {
		out[a.Slug] = struct{}{}
	}
	return out, nil
}

func printValidationError(src, phase string, err error) error {
	fmt.Fprintf(os.Stderr, "✗ %s failed at %s phase:\n  %v\n", src, phase, err)
	os.Exit(1)
	return nil
}

func collectStepTypes(dsl *pipeline.DSL) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range dsl.Steps {
		t := string(s.Type)
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

func init() {
	routineValidateCmd.Flags().Bool("json", false, "Deprecated alias for --format json")
	routineValidateCmd.Flags().StringSlice("agents", nil, "Agent slugs to resolve agent_slug references against, offline (e.g. --agents triage,writer). Typos then fail validate, not save.")
	routineValidateCmd.Flags().String("author-crew", "", "Resolve agent_slug references against this crew's live roster (one server call). Mutually exclusive with --agents.")
	pipelineCmd.AddCommand(routineValidateCmd)
}
