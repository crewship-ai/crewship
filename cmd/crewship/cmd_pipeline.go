package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// pipelineCmd groups all routine commands. Routines are workspace-
// scoped declarative DSL workflows authored once (preferably by a
// smart model like Opus) and executed many times by the cheaper
// runtime tier. See ROUTINES.md for the design.
//
// CLI shape mirrors `approvals`, `mission`, and similar workspace
// resources: list / get / run / dry-run / delete + a save subcommand
// that round-trips the test_run gate before the row hits the DB.
var pipelineCmd = &cobra.Command{
	Use:     "routine",
	Aliases: []string{"pipeline"},
	Short:   "Manage workspace routines (declarative DSL workflows; alias: pipeline)",
	Long: `Routines are AI-authored, workspace-scoped recipes that any crew can
invoke. Each routine has a slug, a JSON DSL definition, and a record
of who authored it (which crew, which agent). When you invoke a
routine, the executor runs it in the AUTHOR crew's context — you
reuse the author's persona + credentials without seeing them.

The "pipeline" alias is preserved for back-compat: every "crewship
routine X" invocation also works as "crewship pipeline X". Internal
identifiers (table, package, route paths) remain "pipeline"; only
the user-facing label is Routine.

Examples:
  crewship routine list
  crewship routine get email-fetch-summarize
  crewship routine save --name "email-fetch" --description "..." --definition routine.json --author-crew crew_a
  crewship routine run email-fetch-summarize --inputs '{"since":"yesterday"}'
  crewship routine dry-run email-fetch-summarize --inputs '{"since":"yesterday"}'
  crewship routine delete email-fetch-summarize
  crewship routine runs email-fetch-summarize --limit 20
  crewship routine versions email-fetch-summarize
  crewship routine rollback email-fetch-summarize --to 3
  crewship routine export email-fetch-summarize > bundle.json
  crewship routine import < bundle.json
  crewship routine cancel <run_id>

Subcommand status:
  list       GET    /api/v1/workspaces/{ws}/pipelines
  get        GET    /api/v1/workspaces/{ws}/pipelines/{slug}
  run        POST   /api/v1/workspaces/{ws}/pipelines/{slug}/run
  dry-run    POST   /api/v1/workspaces/{ws}/pipelines/{slug}/dry_run
  save       POST   /api/v1/workspaces/{ws}/pipelines/test_run + .../internal/pipelines/save
  delete     DELETE /api/v1/workspaces/{ws}/pipelines/{slug}
  runs       GET    /api/v1/workspaces/{ws}/pipelines/{slug}/runs (journal-backed)
  versions   GET    /api/v1/workspaces/{ws}/pipelines/{slug}/versions
  rollback   POST   /api/v1/workspaces/{ws}/pipelines/{slug}/rollback
  export     GET    /api/v1/workspaces/{ws}/pipelines/{slug}/export
  import     POST   /api/v1/workspaces/{ws}/pipelines/import
  cancel     POST   /api/v1/workspaces/{ws}/pipelines/runs/{run_id}/cancel
`,
}

// pipelineRowJSON mirrors the routine response shape from the API
// handler. We don't share the type to keep the CLI build light;
// adding fields server-side requires updating both sites, which is
// the price of stable wire shape contracts.
type pipelineRowJSON struct {
	ID                   string          `json:"id"`
	Slug                 string          `json:"slug"`
	Name                 string          `json:"name"`
	Description          string          `json:"description"`
	DSLVersion           string          `json:"dsl_version"`
	DefinitionHash       string          `json:"definition_hash"`
	Ephemeral            bool            `json:"ephemeral"`
	WorkspaceVisible     bool            `json:"workspace_visible"`
	InvocationCount      int             `json:"invocation_count"`
	LastInvokedAt        *string         `json:"last_invoked_at"`
	LastInvocationStatus string          `json:"last_invocation_status"`
	AuthorCrewID         string          `json:"author_crew_id"`
	AuthorAgentID        string          `json:"author_agent_id"`
	AuthorUserID         string          `json:"author_user_id"`
	AuthoredVia          string          `json:"authored_via"`
	CreatedAt            string          `json:"created_at"`
	UpdatedAt            string          `json:"updated_at"`
	Definition           json.RawMessage `json:"definition,omitempty"`
}

var pipelineListCmd = &cobra.Command{
	Use:   "list",
	Short: "List workspace routines (sorted by usage)",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		path := fmt.Sprintf("/api/v1/workspaces/%s/pipelines", ws)
		if order, _ := cmd.Flags().GetString("order"); order != "" {
			path += "?order=" + order
		}
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var rows []pipelineRowJSON
		if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if len(rows) == 0 {
			fmt.Println("No routines registered yet.")
			fmt.Println("Save one via: crewship routine save --name … --definition file.json --author-crew <crew_id>")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "SLUG\tINVOC\tLAST STATUS\tAUTHOR CREW\tDESCRIPTION")
		for _, p := range rows {
			desc := p.Description
			if len(desc) > 60 {
				desc = desc[:57] + "..."
			}
			lastStatus := p.LastInvocationStatus
			if lastStatus == "" {
				lastStatus = "—"
			}
			authorCrew := p.AuthorCrewID
			if authorCrew == "" {
				authorCrew = "—"
			}
			fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\n", p.Slug, p.InvocationCount, lastStatus, authorCrew, desc)
		}
		return w.Flush()
	},
}

var pipelineGetCmd = &cobra.Command{
	Use:   "get <slug>",
	Short: "Show full routine detail including DSL definition",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		format, _ := cmd.Flags().GetString("format")
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s", ws, args[0]))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var p pipelineRowJSON
		if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}

		switch strings.ToLower(format) {
		case "json":
			// Machine-readable mode: emit the whole row as
			// pretty-printed JSON. Used by `crewship export` callers
			// and any operator scripting against the CLI.
			out, err := json.MarshalIndent(p, "", "  ")
			if err != nil {
				return fmt.Errorf("marshal routine to JSON: %w", err)
			}
			fmt.Println(string(out))
			return nil
		case "", "human", "table":
			// fall through to human-readable rendering below
		default:
			return fmt.Errorf("unknown --format %q (want human | json)", format)
		}

		// Pretty-print: human header on top, full DSL JSON below.
		// Tabwriter for the header keeps the layout aligned even when
		// fields wrap.
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "Slug:\t%s\n", p.Slug)
		fmt.Fprintf(w, "Name:\t%s\n", p.Name)
		fmt.Fprintf(w, "Description:\t%s\n", p.Description)
		fmt.Fprintf(w, "DSL version:\t%s\n", p.DSLVersion)
		fmt.Fprintf(w, "Author crew:\t%s\n", p.AuthorCrewID)
		fmt.Fprintf(w, "Author agent:\t%s\n", p.AuthorAgentID)
		fmt.Fprintf(w, "Authored via:\t%s\n", p.AuthoredVia)
		fmt.Fprintf(w, "Invocations:\t%d\n", p.InvocationCount)
		if p.LastInvokedAt != nil && *p.LastInvokedAt != "" {
			fmt.Fprintf(w, "Last invoked:\t%s (status=%s)\n", *p.LastInvokedAt, p.LastInvocationStatus)
		}
		fmt.Fprintf(w, "Created:\t%s\n", p.CreatedAt)
		fmt.Fprintf(w, "Updated:\t%s\n", p.UpdatedAt)
		_ = w.Flush()
		fmt.Println("\nDefinition:")
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, p.Definition, "  ", "  "); err != nil {
			fmt.Println(string(p.Definition))
		} else {
			fmt.Println("  " + pretty.String())
		}
		return nil
	},
}

var pipelineSaveCmd = &cobra.Command{
	Use:   "save",
	Short: "Save a new routine from a JSON DSL file",
	Long: `Save a routine by uploading a DSL JSON file. The save flow runs the
test_run gate first — your DSL is parsed, validated, and executed
once against the workspace's execution tier. Only on success does
the row land in the registry.

The DSL file should be a JSON document matching the format described
in ROUTINES.md (top-level: name, description, inputs, steps).

You also need to supply --author-crew so the runtime knows which
crew owns the routine. The agent_slug references inside the DSL
are resolved against THIS crew, not the caller's crew (cross-crew
reuse contract).`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		definitionPath, _ := cmd.Flags().GetString("definition")
		name, _ := cmd.Flags().GetString("name")
		description, _ := cmd.Flags().GetString("description")
		authorCrew, _ := cmd.Flags().GetString("author-crew")
		authorAgent, _ := cmd.Flags().GetString("author-agent")
		sampleInputsRaw, _ := cmd.Flags().GetString("sample-inputs")

		if definitionPath == "" {
			return fmt.Errorf("--definition <path> required")
		}
		if name == "" {
			return fmt.Errorf("--name required")
		}
		if authorCrew == "" {
			return fmt.Errorf("--author-crew required (the crew that owns this routine)")
		}

		definitionRaw, err := os.ReadFile(definitionPath)
		if err != nil {
			return fmt.Errorf("read definition file: %w", err)
		}

		var sampleInputs map[string]any
		if sampleInputsRaw != "" {
			if err := json.Unmarshal([]byte(sampleInputsRaw), &sampleInputs); err != nil {
				return fmt.Errorf("parse --sample-inputs JSON: %w", err)
			}
		}

		client := newAPIClient()
		ws := client.GetWorkspaceID()

		// Step 1: test_run. The save endpoint requires a fresh
		// passing test_run within 5 min, so we run one inline. The
		// CLI surfaces the same gate the sidecar enforces for
		// in-container agents.
		fmt.Println("Running test_run gate against the execution tier...")
		testBody := map[string]any{
			"definition":     json.RawMessage(definitionRaw),
			"author_crew_id": authorCrew,
			"sample_inputs":  sampleInputs,
		}
		testResp, err := client.Post(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/test_run", ws), testBody)
		if err != nil {
			return err
		}
		defer testResp.Body.Close()
		if err := cli.CheckError(testResp); err != nil {
			return fmt.Errorf("test_run failed: %w", err)
		}
		var testResult struct {
			Status       string  `json:"status"`
			Output       string  `json:"output"`
			ErrorMessage string  `json:"error_message"`
			DurationMs   int64   `json:"duration_ms"`
			CostUSD      float64 `json:"cost_usd"`
		}
		if err := json.NewDecoder(testResp.Body).Decode(&testResult); err != nil {
			return fmt.Errorf("decode test_run response: %w", err)
		}
		if testResult.Status != "COMPLETED" {
			return fmt.Errorf("test_run did not complete cleanly: status=%s err=%q", testResult.Status, testResult.ErrorMessage)
		}
		fmt.Printf("test_run passed (%dms, $%.4f). Saving...\n", testResult.DurationMs, testResult.CostUSD)

		// Step 2: internal save. We hit the public path indirectly
		// via the sidecar IPC route — but from the CLI we need the
		// internal endpoint directly with our user JWT. The
		// PipelineHandler.InternalSave route is mounted under
		// authed() (not under the X-Internal-Token chain) precisely
		// because user-driven save flows from the CLI need to land
		// here too.
		nowRFC := time.Now().UTC().Format(time.RFC3339Nano)
		saveBody := map[string]any{
			"workspace_id":         ws,
			"slug":                 slugifyName(name),
			"name":                 name,
			"description":          description,
			"definition":           json.RawMessage(definitionRaw),
			"author_crew_id":       authorCrew,
			"author_agent_id":      authorAgent,
			"last_test_run_at":     nowRFC,
			"last_test_run_passed": true,
		}
		saveResp, err := client.Post("/api/v1/internal/pipelines/save", saveBody)
		if err != nil {
			return err
		}
		defer saveResp.Body.Close()
		if err := cli.CheckError(saveResp); err != nil {
			return err
		}
		var saved pipelineRowJSON
		if err := json.NewDecoder(saveResp.Body).Decode(&saved); err != nil {
			return fmt.Errorf("decode save response: %w", err)
		}
		// Guard against unexpectedly short hash so a server change
		// (or a future migration that stores hashes differently)
		// doesn't crash the CLI with a slice-out-of-range panic.
		shortHash := saved.DefinitionHash
		if len(shortHash) > 12 {
			shortHash = shortHash[:12]
		}
		fmt.Printf("Saved routine %s (id=%s, hash=%s)\n", saved.Slug, saved.ID, shortHash)
		fmt.Printf("Invoke with: crewship routine run %s\n", saved.Slug)
		return nil
	},
}

var pipelineRunCmd = &cobra.Command{
	Use:   "run <slug>",
	Short: "Invoke a saved routine against the live execution tier",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		inputsRaw, _ := cmd.Flags().GetString("inputs")
		invokingCrew, _ := cmd.Flags().GetString("invoking-crew")
		_ = invokingCrew // header is sidecar-side; CLI flag accepted but
		// not threaded through client.Do until the client gains
		// per-call header support — Phase 1.5 polish.

		// Inputs default to {} when caller doesn't pass any.
		inputs := map[string]any{}
		if inputsRaw != "" {
			if err := json.Unmarshal([]byte(inputsRaw), &inputs); err != nil {
				return fmt.Errorf("parse --inputs JSON: %w", err)
			}
		}

		tierOverride, _ := cmd.Flags().GetString("tier-override")
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		runBody := map[string]any{"inputs": inputs}
		if tierOverride != "" {
			runBody["tier_override"] = tierOverride
		}
		resp, err := client.Do("POST", fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/run", ws, args[0]), runBody)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		// Convert "routine not found" 404s into actionable
		// suggestions. Listing the workspace's routines costs
		// one extra round-trip but only on the slow / failing
		// path, which is exactly when the user wants help.
		if resp.StatusCode == http.StatusNotFound {
			if hint := suggestSimilarRoutineSlugs(client, ws, args[0]); hint != "" {
				return fmt.Errorf("routine %q not found — %s", args[0], hint)
			}
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		// Pretty-print run result. Status colour is best done by the
		// terminal user, not us — we just label COMPLETED / FAILED
		// / DRY_RUN_OK + show output + show step outputs map.
		var result struct {
			RunID        string            `json:"run_id"`
			Status       string            `json:"status"`
			Output       string            `json:"output"`
			StepOutputs  map[string]string `json:"step_outputs"`
			DurationMs   int64             `json:"duration_ms"`
			CostUSD      float64           `json:"cost_usd"`
			FailedAtStep string            `json:"failed_at_step"`
			ErrorMessage string            `json:"error_message"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return fmt.Errorf("decode run response: %w", err)
		}
		fmt.Printf("Run %s: %s (%dms, $%.4f)\n", result.RunID, result.Status, result.DurationMs, result.CostUSD)
		if result.Status == "FAILED" {
			fmt.Printf("  failed at step: %s\n  error: %s\n", result.FailedAtStep, result.ErrorMessage)
			return fmt.Errorf("routine run failed")
		}
		if result.Output != "" {
			fmt.Println("\nFinal output:")
			fmt.Println(indent(result.Output, "  "))
		}
		if len(result.StepOutputs) > 0 {
			fmt.Println("\nStep outputs:")
			for id, out := range result.StepOutputs {
				preview := out
				if len(preview) > 200 {
					preview = preview[:200] + "..."
				}
				fmt.Printf("  [%s]\n%s\n", id, indent(preview, "    "))
			}
		}
		return nil
	},
}

var pipelineDryRunCmd = &cobra.Command{
	Use:   "dry-run <slug>",
	Short: "Preview what a routine would do without invoking agents",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		inputsRaw, _ := cmd.Flags().GetString("inputs")

		var body bytes.Buffer
		if inputsRaw != "" {
			var inputs map[string]any
			if err := json.Unmarshal([]byte(inputsRaw), &inputs); err != nil {
				return fmt.Errorf("parse --inputs JSON: %w", err)
			}
			_ = json.NewEncoder(&body).Encode(map[string]any{"inputs": inputs})
		} else {
			body.WriteString(`{"inputs":{}}`)
		}

		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Post(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/dry_run", ws, args[0]), &body)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var result struct {
			Status     string  `json:"status"`
			DurationMs int64   `json:"duration_ms"`
			CostUSD    float64 `json:"cost_usd"`
			// JSON tag MUST match the server-side wire name (`would_execute`)
			// — the server marshals types.RunResult.WouldExecute with
			// `json:"would_execute,omitempty"` (internal/pipeline/types.go).
			// Previously this struct used `json:"WouldExecute"` which never
			// matched the wire, so the CLI silently rendered "0 steps" for
			// every dry-run regardless of what the server returned.
			WouldExecute []struct {
				StepID         string  `json:"step_id"`
				StepType       string  `json:"step_type"`
				WouldCallAgent string  `json:"would_call_agent,omitempty"`
				WouldCallSlug  string  `json:"would_call_pipeline,omitempty"`
				WouldPass      string  `json:"would_pass,omitempty"`
				TierAdapter    string  `json:"tier_adapter,omitempty"`
				TierModel      string  `json:"tier_model,omitempty"`
				EstimatedCost  float64 `json:"estimated_cost_usd,omitempty"`
			} `json:"would_execute"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return fmt.Errorf("decode dry_run response: %w", err)
		}
		fmt.Printf("Dry run: %s (estimated %dms, $%.4f total)\n\n", result.Status, result.DurationMs, result.CostUSD)
		for i, s := range result.WouldExecute {
			fmt.Printf("Step %d [%s] (%s):\n", i+1, s.StepID, s.StepType)
			if s.WouldCallAgent != "" {
				fmt.Printf("  would call agent: %s\n", s.WouldCallAgent)
			}
			if s.WouldCallSlug != "" {
				fmt.Printf("  would call routine: %s\n", s.WouldCallSlug)
			}
			if s.TierAdapter != "" {
				fmt.Printf("  resolved tier: %s/%s\n", s.TierAdapter, s.TierModel)
			}
			if s.EstimatedCost > 0 {
				fmt.Printf("  estimated cost: $%.4f\n", s.EstimatedCost)
			}
			if s.WouldPass != "" {
				preview := s.WouldPass
				if len(preview) > 300 {
					preview = preview[:300] + "..."
				}
				fmt.Printf("  rendered prompt:\n%s\n", indent(preview, "    "))
			}
			fmt.Println()
		}
		return nil
	},
}

var pipelineDeleteCmd = &cobra.Command{
	Use:   "delete <slug>",
	Short: "Soft-delete a routine (hidden but row preserved for audit)",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Do("DELETE", fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s", ws, args[0]), nil)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		fmt.Printf("Deleted routine %s\n", args[0])
		return nil
	},
}

var pipelineRunsCmd = &cobra.Command{
	Use:   "runs <slug>",
	Short: "List recent invocations for a routine (from journal)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		limit, _ := cmd.Flags().GetInt("limit")
		if limit <= 0 {
			limit = 20
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		path := fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/runs?limit=%d", ws, args[0], limit)
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var rows []struct {
			ID        string `json:"id"`
			Timestamp string `json:"ts"`
			EntryType string `json:"entry_type"`
			Severity  string `json:"severity"`
			Summary   string `json:"summary"`
			RunID     string `json:"run_id"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if len(rows) == 0 {
			fmt.Println("No runs yet for this routine.")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "TS\tTYPE\tSEVERITY\tRUN_ID\tSUMMARY")
		for _, r := range rows {
			runID := r.RunID
			if len(runID) > 16 {
				runID = runID[:16] + "…"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Timestamp, r.EntryType, r.Severity, runID, r.Summary)
		}
		return w.Flush()
	},
}

// slugifyName mirrors internal/sidecar.slugifyForPipelines so a name
// passed via --name on the CLI produces the same slug the sidecar
// would mint for the same name from an in-container agent. Keeps
// the two save flows interchangeable.
func slugifyName(name string) string {
	var out []rune
	prevHyphen := true
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = append(out, r)
			prevHyphen = false
		case r == '-' || r == '_':
			out = append(out, r)
			prevHyphen = true
		case r == ' ' || r == '.' || r == '/' || r == ':':
			if !prevHyphen {
				out = append(out, '-')
				prevHyphen = true
			}
		}
	}
	for len(out) > 0 && (out[len(out)-1] == '-' || out[len(out)-1] == '_') {
		out = out[:len(out)-1]
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return string(out)
}

// indent prefixes every line in s with the given indent. Cheaper
// than pulling in a markdown library for what's basically text
// alignment in error/output blocks.
func indent(s, prefix string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func init() {
	pipelineListCmd.Flags().String("order", "popularity", "sort order: popularity | recent | name")
	pipelineGetCmd.Flags().StringP("format", "f", "human", "output format: human | json")
	pipelineRunsCmd.Flags().Int("limit", 20, "max number of run entries to return (1-500)")

	pipelineSaveCmd.Flags().String("name", "", "human-readable name (REQUIRED; slug derived from this)")
	pipelineSaveCmd.Flags().String("description", "", "one-line description shown in [AVAILABLE ROUTINES] block")
	pipelineSaveCmd.Flags().String("definition", "", "path to a JSON DSL file (REQUIRED)")
	pipelineSaveCmd.Flags().String("author-crew", "", "crew_id that owns this routine (REQUIRED)")
	pipelineSaveCmd.Flags().String("author-agent", "", "agent_id that authored this routine (optional but recommended)")
	pipelineSaveCmd.Flags().String("sample-inputs", "", "JSON inputs the test_run uses to validate the DSL")

	pipelineRunCmd.Flags().String("inputs", "", "JSON inputs for the run (e.g. '{\"since\":\"yesterday\"}')")
	pipelineRunCmd.Flags().String("invoking-crew", "", "crew_id to record as the invoker (cross-crew reuse audit)")
	pipelineRunCmd.Flags().String("tier-override", "", "force every agent_run step onto a tier (trivial|fast|moderate|smart). Step-level model_override still wins. Empty = use authored complexity.")

	pipelineDryRunCmd.Flags().String("inputs", "", "JSON inputs for the dry-run preview")

	pipelineCmd.AddCommand(pipelineListCmd)
	pipelineCmd.AddCommand(pipelineGetCmd)
	pipelineCmd.AddCommand(pipelineSaveCmd)
	pipelineCmd.AddCommand(pipelineRunCmd)
	pipelineCmd.AddCommand(pipelineDryRunCmd)
	pipelineCmd.AddCommand(pipelineDeleteCmd)
	pipelineCmd.AddCommand(pipelineRunsCmd)
}
