package main

// crewship routine iterate — a scored improvement loop over a saved routine.
//
// Each round: run the routine → a grader agent scores the output against a
// rubric (0–100 JSON verdict) → if below target, an optimizer agent proposes
// an improved definition → local Parse/Validate → operator confirms → the
// definition saves as a NEW immutable version (change_summary records the
// round + score, so `routine versions` and the versions UI tell the story).
// Rollback is the escape hatch: every pre-iterate version stays addressable
// via `crewship routine rollback`.
//
// Design constraints, deliberately:
//   - CLI-orchestrated over EXISTING APIs (run, export, test_run→save,
//     versions). No new server surface; the server-side test-gate and
//     governance maker-checker apply to every save exactly as they do for a
//     human author. A risky definition still lands as 'proposed'.
//   - Grading is client-driven via a grader agent because the step-level
//     outcomes gate is pass/fail-and-discard (internal/pipeline/outcomes.go)
//     — no numeric per-run score is persisted synchronously anywhere.
//   - Human-in-the-loop by default: each save asks for confirmation.
//     --yes exists for unattended runs; the versions trail + rollback keep
//     even that auditable and reversible.

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/spf13/cobra"
)

// iterateScore is a grader verdict for one round.
type iterateScore struct {
	Score    int    `json:"score"`
	Feedback string `json:"feedback"`
}

// iterateRound is one row of the final summary.
type iterateRound struct {
	Round        int     `json:"round"`
	RunID        string  `json:"run_id"`
	RunStatus    string  `json:"run_status"`
	Score        int     `json:"score"`
	Feedback     string  `json:"feedback,omitempty"`
	CostUSD      float64 `json:"cost_usd"`
	SavedVersion bool    `json:"saved_new_version"`
}

var routineIterateCmd = &cobra.Command{
	Use:   "iterate <slug>",
	Short: "Run a scored improvement loop: run → grade → optimize → save new version",
	Long: `Runs a routine repeatedly, grading each run's output against a rubric
with a grader agent, and letting an optimizer agent rewrite the routine's
definition between rounds. Every accepted improvement saves as a new
immutable version (see 'crewship routine versions' — the change_summary
records the round and score), so the whole loop is auditable and any
version can be rolled back.

The loop stops when the score reaches --target, or after --rounds rounds.
Each save asks for confirmation unless --yes is passed. The server's
test-run gate and governance rules apply to every save: a definition that
gains risky steps (http, code, egress) lands as 'proposed' and needs
approval before it can run — the loop stops there and tells you.

The rubric is the contract: write it the way you'd brief a colleague
("must contain a TL;DR section", "tone: plain factual Czech", "no more
than 200 words"). Grader and optimizer are ordinary crew agents.

Example:
  crewship routine iterate summarize-text \
    --rubric ./rubric.md --inputs '{"text": "..."}' \
    --grader reviewer --optimizer lead --author-crew eng \
    --rounds 3 --target 90`,
	Args: cobra.ExactArgs(1),
	RunE: runRoutineIterate,
}

func runRoutineIterate(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	slug := args[0]

	rounds, _ := cmd.Flags().GetInt("rounds")
	target, _ := cmd.Flags().GetInt("target")
	inputsRaw, _ := cmd.Flags().GetString("inputs")
	rubricArg, _ := cmd.Flags().GetString("rubric")
	graderSlug, _ := cmd.Flags().GetString("grader")
	optimizerSlug, _ := cmd.Flags().GetString("optimizer")
	authorCrew, _ := cmd.Flags().GetString("author-crew")
	autoYes, _ := cmd.Flags().GetBool("yes")
	runTimeout, _ := cmd.Flags().GetDuration("run-timeout")
	maxTurns, _ := cmd.Flags().GetInt("max-turns")

	if rubricArg == "" {
		return fmt.Errorf("--rubric required (file path or literal criteria — it is the grading contract)")
	}
	if graderSlug == "" {
		return fmt.Errorf("--grader required (agent slug that scores each run)")
	}
	if authorCrew == "" {
		return fmt.Errorf("--author-crew required (the crew that owns the routine; agent slugs resolve against it)")
	}
	if optimizerSlug == "" {
		optimizerSlug = graderSlug
	}
	if rounds < 1 || rounds > 10 {
		return fmt.Errorf("--rounds must be 1-10 (each round is a real run + two agent calls)")
	}
	if target < 1 || target > 100 {
		return fmt.Errorf("--target must be 1-100")
	}

	rubric := rubricArg
	if raw, err := os.ReadFile(rubricArg); err == nil {
		rubric = string(raw)
	}

	var inputs map[string]any
	if inputsRaw != "" {
		if err := json.Unmarshal([]byte(inputsRaw), &inputs); err != nil {
			return fmt.Errorf("parse --inputs JSON: %w", err)
		}
	}

	client := newAPIClient()
	ws := client.GetWorkspaceID()
	graderID, err := resolveAgentID(client, graderSlug)
	if err != nil {
		return fmt.Errorf("resolve --grader: %w", err)
	}
	optimizerID := graderID
	if optimizerSlug != graderSlug {
		if optimizerID, err = resolveAgentID(client, optimizerSlug); err != nil {
			return fmt.Errorf("resolve --optimizer: %w", err)
		}
	}
	crewSlugs, err := fetchCrewAgentSlugs(authorCrew)
	if err != nil {
		return fmt.Errorf("fetch author-crew roster: %w", err)
	}

	history := make([]iterateRound, 0, rounds)
	for round := 1; round <= rounds; round++ {
		fmt.Fprintf(os.Stderr, "── round %d/%d ──────────────────────────────\n", round, rounds)

		// 1. Run the routine synchronously.
		fmt.Fprintf(os.Stderr, "▶ running %s...\n", slug)
		run, err := iterateRunRoutine(client, ws, slug, inputs, runTimeout)
		if err != nil {
			return fmt.Errorf("round %d run: %w", round, err)
		}
		switch run.Status {
		case "COMPLETED", "FAILED":
			// both are gradable — a FAILED run grades with its error context
		case "WAITING":
			return fmt.Errorf("round %d: run %s parked on an approval waitpoint — iterate does not drive approval loops; approve it and re-run, or remove the wait step for iteration", round, run.RunID)
		default:
			return fmt.Errorf("round %d: run returned status %s (deferred/deduped runs cannot be iterated synchronously)", round, run.Status)
		}

		// 2. Grade.
		fmt.Fprintf(os.Stderr, "⚖ grading with %s...\n", graderSlug)
		gradeText, err := askAgentText(client, graderID, buildGradePrompt(rubric, inputsRaw, run.Output, run.ErrorMessage), maxTurns)
		if err != nil {
			return fmt.Errorf("round %d grade: %w", round, err)
		}
		score, err := parseGraderScore(gradeText)
		if err != nil {
			return fmt.Errorf("round %d: grader %s returned an unparseable verdict (%v) — its output must contain {\"score\": 0-100, \"feedback\": \"...\"}", round, graderSlug, err)
		}
		fmt.Fprintf(os.Stderr, "  score %d/100 — %s\n", score.Score, truncateLine(score.Feedback, 120))

		row := iterateRound{Round: round, RunID: run.RunID, RunStatus: run.Status, Score: score.Score, Feedback: score.Feedback, CostUSD: run.CostUSD}

		// 3. Target reached → done.
		if score.Score >= target {
			history = append(history, row)
			fmt.Fprintf(os.Stderr, "✓ target %d reached\n", target)
			break
		}
		if round == rounds {
			history = append(history, row)
			break // out of rounds; report without another optimization
		}

		// 4. Fetch current definition + row metadata via export.
		bundle, err := iterateFetchBundle(client, ws, slug)
		if err != nil {
			return fmt.Errorf("round %d export: %w", round, err)
		}

		// 5. Optimize.
		fmt.Fprintf(os.Stderr, "🛠 optimizing with %s...\n", optimizerSlug)
		optText, err := askAgentText(client, optimizerID, buildOptimizePrompt(bundle.Pipeline.Definition, rubric, score, run.Output, run.ErrorMessage), maxTurns)
		if err != nil {
			return fmt.Errorf("round %d optimize: %w", round, err)
		}
		newDef, err := extractDefinitionJSON(optText)
		if err != nil {
			return fmt.Errorf("round %d: optimizer %s returned no valid definition JSON: %w", round, optimizerSlug, err)
		}

		// 6. Local validation before touching the server.
		dsl, err := pipeline.Parse(newDef)
		if err != nil {
			return fmt.Errorf("round %d: optimizer produced an unparseable DSL: %w", round, err)
		}
		if err := pipeline.Validate(dsl, crewSlugs, nil); err != nil {
			return fmt.Errorf("round %d: optimizer produced an invalid DSL: %w", round, err)
		}

		// 7. Confirm.
		summary := iterateChangeSummary(round, score)
		if !autoYes {
			fmt.Fprintf(os.Stderr, "proposed: %s (%d bytes)\n", summary, len(newDef))
			if !confirmInteractive("Save as new version?") {
				history = append(history, row)
				fmt.Fprintln(os.Stderr, "aborted by operator — no version saved")
				break
			}
		}

		// 8. Save (two-step test_run → save; server gates apply).
		saved, err := iterateSaveDefinition(client, ws, bundle.Pipeline.Name, bundle.Pipeline.Description, authorCrew, summary, newDef)
		if err != nil {
			return fmt.Errorf("round %d save: %w", round, err)
		}
		row.SavedVersion = true
		history = append(history, row)
		if saved.Status == "proposed" {
			fmt.Fprintf(os.Stderr, "⚠ new version saved as PROPOSED (risky steps) — approve it before the next round can run it\n")
			break
		}
		fmt.Fprintf(os.Stderr, "✓ saved new version (%s)\n", summary)
	}

	return printIterateSummary(cmd, slug, target, history)
}

// iterateRunResult is the subset of the synchronous run response iterate needs.
type iterateRunResult struct {
	RunID        string  `json:"run_id"`
	Status       string  `json:"status"`
	Output       string  `json:"output"`
	ErrorMessage string  `json:"error_message"`
	CostUSD      float64 `json:"cost_usd"`
}

func iterateRunRoutine(client *cli.Client, ws, slug string, inputs map[string]any, timeout time.Duration) (*iterateRunResult, error) {
	runClient := client.WithTimeout(timeout)
	resp, err := runClient.Post(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/run", ws, slug), map[string]any{"inputs": inputs})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var out iterateRunResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode run response: %w", err)
	}
	return &out, nil
}

// iterateBundle is the subset of the export bundle iterate consumes.
type iterateBundle struct {
	Pipeline struct {
		Slug        string          `json:"slug"`
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Definition  json.RawMessage `json:"definition"`
	} `json:"pipeline"`
}

func iterateFetchBundle(client *cli.Client, ws, slug string) (*iterateBundle, error) {
	resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/export", ws, slug))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var b iterateBundle
	if err := json.NewDecoder(resp.Body).Decode(&b); err != nil {
		return nil, fmt.Errorf("decode export bundle: %w", err)
	}
	if len(b.Pipeline.Definition) == 0 {
		return nil, fmt.Errorf("export bundle has no definition")
	}
	return &b, nil
}

// iterateSaveResult is the save response subset iterate needs (status tells
// us whether governance parked the version as 'proposed').
type iterateSaveResult struct {
	Slug           string `json:"slug"`
	ID             string `json:"id"`
	DefinitionHash string `json:"definition_hash"`
	Status         string `json:"status"`
}

// iterateSaveDefinition mirrors `routine save`'s two-step test_run→save
// protocol (see pipelineSaveCmd) and carries the iterate change_summary.
func iterateSaveDefinition(client *cli.Client, ws, name, description, authorCrew, changeSummary string, definition []byte) (*iterateSaveResult, error) {
	testResp, err := client.Post(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/test_run", ws), map[string]any{
		"definition":     json.RawMessage(definition),
		"author_crew_id": authorCrew,
	})
	if err != nil {
		return nil, err
	}
	defer testResp.Body.Close()
	if err := cli.CheckError(testResp); err != nil {
		return nil, err
	}
	var testResult struct {
		Status    string `json:"status"`
		SaveToken string `json:"save_token"`
		Error     string `json:"error_message"`
	}
	if err := json.NewDecoder(testResp.Body).Decode(&testResult); err != nil {
		return nil, fmt.Errorf("decode test_run response: %w", err)
	}
	if testResult.Status != "DRY_RUN_OK" && testResult.Status != "COMPLETED" {
		return nil, fmt.Errorf("server-side validation failed (status=%s): %s", testResult.Status, testResult.Error)
	}

	saveBody := map[string]any{
		"slug":           slugifyName(name),
		"name":           name,
		"description":    description,
		"definition":     json.RawMessage(definition),
		"author_crew_id": authorCrew,
		"change_summary": changeSummary,
	}
	if testResult.SaveToken != "" {
		saveBody["save_token"] = testResult.SaveToken
	}
	saveResp, err := client.Post(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/save", ws), saveBody)
	if err != nil {
		return nil, err
	}
	defer saveResp.Body.Close()
	if err := cli.CheckError(saveResp); err != nil {
		return nil, err
	}
	var saved iterateSaveResult
	if err := json.NewDecoder(saveResp.Body).Decode(&saved); err != nil {
		return nil, fmt.Errorf("decode save response: %w", err)
	}
	return &saved, nil
}

// askAgentText runs one prompt against an agent and returns the accumulated
// final text. Same WS flow as `crewship ask --no-stream` (cmd_run.go
// runNoStream), minus the printing/saving concerns — iterate consumes the
// text programmatically.
func askAgentText(client *cli.Client, agentID, prompt string, maxTurns int) (string, error) {
	chatResp, err := client.Post("/api/v1/agents/"+agentID+"/chats", ChatCreationBody())
	if err != nil {
		return "", err
	}
	defer chatResp.Body.Close()
	if err := cli.CheckError(chatResp); err != nil {
		return "", err
	}
	var chat struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(chatResp.Body).Decode(&chat); err != nil {
		return "", fmt.Errorf("decode chat: %w", err)
	}

	wsToken, err := cli.WSTokenFromServer(client)
	if err != nil {
		return "", err
	}
	wsc, err := cli.NewWSClient(streamServerURL(), wsToken)
	if err != nil {
		return "", err
	}
	defer wsc.Close()
	if err := wsc.Subscribe("session:" + chat.ID); err != nil {
		return "", fmt.Errorf("subscribe: %w", err)
	}
	if err := wsc.SendMessage("agent:"+agentID, chat.ID, prompt, maxTurns); err != nil {
		return "", fmt.Errorf("send message: %w", err)
	}

	var fullText strings.Builder
	for {
		msg, err := wsc.ReadMessage()
		if err != nil {
			return "", fmt.Errorf("agent stream closed early: %w", err)
		}
		event, err := cli.ParseChatEvent(msg)
		if err != nil || event == nil {
			continue
		}
		switch event.Type {
		case "text":
			fullText.WriteString(event.Content)
		case "error":
			return "", fmt.Errorf("agent error: %s", sanitizeTerminal(event.Content))
		case "done":
			return fullText.String(), nil
		}
	}
}

// ---- pure helpers (unit-tested in cmd_routine_iterate_test.go) ----

// parseGraderScore extracts {"score": N, "feedback": "..."} from grader
// output, tolerating fenced code blocks and surrounding prose. Score is
// clamped to [0,100]; a missing score field is an error (a grader that
// doesn't score is misconfigured, not a zero).
func parseGraderScore(text string) (iterateScore, error) {
	raw, err := extractJSONObject(text)
	if err != nil {
		return iterateScore{}, err
	}
	var verdict struct {
		Score    *float64 `json:"score"`
		Feedback string   `json:"feedback"`
	}
	if err := json.Unmarshal(raw, &verdict); err != nil {
		return iterateScore{}, fmt.Errorf("parse verdict JSON: %w", err)
	}
	if verdict.Score == nil {
		return iterateScore{}, fmt.Errorf("verdict has no score field")
	}
	// Floor (not round) a fractional score: a grader that says 66.7 has not
	// awarded 67 — deterministic truncation keeps target comparisons stable.
	s := int(math.Max(0, math.Min(100, math.Floor(*verdict.Score))))
	return iterateScore{Score: s, Feedback: strings.TrimSpace(verdict.Feedback)}, nil
}

// extractDefinitionJSON pulls the improved routine definition out of
// optimizer output: a ```json fenced block wins, else the first balanced
// top-level JSON object. The result must parse as JSON.
func extractDefinitionJSON(text string) ([]byte, error) {
	raw, err := extractJSONObject(text)
	if err != nil {
		return nil, fmt.Errorf("no definition JSON found: %w", err)
	}
	return raw, nil
}

// extractJSONObject finds a JSON object in free-form agent text: fenced
// ```json blocks take priority, then the first balanced {...} span
// (string-and-escape aware). Validates with json.Valid before returning.
func extractJSONObject(text string) (json.RawMessage, error) {
	if fenceStart := strings.Index(text, "```json"); fenceStart != -1 {
		rest := text[fenceStart+len("```json"):]
		if fenceEnd := strings.Index(rest, "```"); fenceEnd != -1 {
			candidate := strings.TrimSpace(rest[:fenceEnd])
			if json.Valid([]byte(candidate)) {
				return json.RawMessage(candidate), nil
			}
			return nil, fmt.Errorf("fenced json block is not valid JSON")
		}
	}
	start := strings.Index(text, "{")
	if start == -1 {
		return nil, fmt.Errorf("no JSON object in text")
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(text); i++ {
		c := text[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				candidate := text[start : i+1]
				if json.Valid([]byte(candidate)) {
					return json.RawMessage(candidate), nil
				}
				return nil, fmt.Errorf("balanced span is not valid JSON")
			}
		}
	}
	return nil, fmt.Errorf("unbalanced JSON object in text")
}

const iterateDelim = "-----"

func buildGradePrompt(rubric, inputsJSON, output, errMsg string) string {
	var b strings.Builder
	b.WriteString("You are grading one run of an automated routine against a rubric.\n")
	b.WriteString("Respond with ONLY a JSON object: {\"score\": <0-100>, \"feedback\": \"<one short paragraph: what loses points and why>\"}\n\n")
	b.WriteString("RUBRIC (the contract — grade against this, nothing else):\n")
	b.WriteString(iterateDelim + "\n" + rubric + "\n" + iterateDelim + "\n\n")
	if inputsJSON != "" {
		b.WriteString("RUN INPUTS:\n" + iterateDelim + "\n" + inputsJSON + "\n" + iterateDelim + "\n\n")
	}
	if errMsg != "" {
		b.WriteString("THE RUN FAILED. Error:\n" + iterateDelim + "\n" + errMsg + "\n" + iterateDelim + "\n")
		b.WriteString("A failed run scores low, but read the error — feedback should say what in the routine's design likely caused it.\n\n")
	}
	b.WriteString("RUN OUTPUT:\n" + iterateDelim + "\n" + output + "\n" + iterateDelim + "\n")
	return b.String()
}

func buildOptimizePrompt(definition []byte, rubric string, score iterateScore, runOutput, runErr string) string {
	var b strings.Builder
	b.WriteString("You are improving an automated routine (JSON DSL) so its runs score higher against a rubric.\n")
	b.WriteString("Respond with ONLY the complete improved JSON definition (same schema, all fields), inside a ```json fenced block. Keep the same name and inputs contract; change step prompts/structure as needed. Do NOT add http/code/egress steps that were not already present.\n\n")
	b.WriteString("CURRENT DEFINITION:\n```json\n")
	b.Write(definition)
	b.WriteString("\n```\n\n")
	b.WriteString("RUBRIC:\n" + iterateDelim + "\n" + rubric + "\n" + iterateDelim + "\n\n")
	fmt.Fprintf(&b, "LAST RUN SCORED %d/100. Grader feedback:\n%s\n%s\n%s\n\n", score.Score, iterateDelim, score.Feedback, iterateDelim)
	if runErr != "" {
		b.WriteString("LAST RUN ERROR:\n" + iterateDelim + "\n" + runErr + "\n" + iterateDelim + "\n\n")
	}
	if runOutput != "" {
		b.WriteString("LAST RUN OUTPUT (for context):\n" + iterateDelim + "\n" + truncateLine(runOutput, 4000) + "\n" + iterateDelim + "\n")
	}
	return b.String()
}

// iterateChangeSummary is the one-line provenance note stored on the version
// row: "iterate round 2: score 74/100 — <feedback…>", capped for the UI.
func iterateChangeSummary(round int, score iterateScore) string {
	s := fmt.Sprintf("iterate round %d: score %d/100", round, score.Score)
	if fb := strings.TrimSpace(score.Feedback); fb != "" {
		s += " — " + fb
	}
	return truncateLine(s, 160)
}

func truncateLine(s string, max int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return s[:max]
	}
	return s[:max-1] + "…"
}

func printIterateSummary(cmd *cobra.Command, slug string, target int, history []iterateRound) error {
	f := resolvedFormatter(cmd)
	return f.AutoHuman(map[string]any{"routine": slug, "target": target, "rounds": history}, func() {
		printIterateSummaryHuman(slug, target, history)
	})
}

func printIterateSummaryHuman(slug string, target int, history []iterateRound) {
	fmt.Printf("\nIterate summary for %s (target %d):\n", slug, target)
	fmt.Printf("%-6s %-10s %-7s %-9s %s\n", "ROUND", "STATUS", "SCORE", "SAVED", "FEEDBACK")
	for _, r := range history {
		saved := "-"
		if r.SavedVersion {
			saved = "new ver"
		}
		fmt.Printf("%-6d %-10s %-7d %-9s %s\n", r.Round, r.RunStatus, r.Score, saved, truncateLine(r.Feedback, 60))
	}
	if len(history) > 0 {
		last := history[len(history)-1]
		if last.Score >= target {
			fmt.Printf("\n✓ target reached in round %d. Inspect the trail: crewship routine versions %s\n", last.Round, slug)
		} else {
			fmt.Printf("\ntarget not reached (best effort recorded). Roll back anytime: crewship routine rollback %s --to <n>\n", slug)
		}
	}
}

func init() {
	routineIterateCmd.Flags().Int("rounds", 3, "maximum improvement rounds (1-10)")
	routineIterateCmd.Flags().Int("target", 90, "stop when the grader score reaches this (1-100)")
	routineIterateCmd.Flags().String("inputs", "", "JSON inputs passed to every run")
	routineIterateCmd.Flags().String("rubric", "", "grading rubric: file path or literal text (REQUIRED)")
	routineIterateCmd.Flags().String("grader", "", "agent slug that scores each run against the rubric (REQUIRED)")
	routineIterateCmd.Flags().String("optimizer", "", "agent slug that rewrites the definition (default: the grader)")
	routineIterateCmd.Flags().String("author-crew", "", "crew slug/id that owns the routine (REQUIRED; agent slugs resolve against it)")
	routineIterateCmd.Flags().BoolP("yes", "y", false, "skip the per-round save confirmation")
	routineIterateCmd.Flags().Duration("run-timeout", 10*time.Minute, "timeout for each synchronous routine run")
	routineIterateCmd.Flags().Int("max-turns", 0, "max turns for grader/optimizer agent calls (0 = server default)")
	pipelineCmd.AddCommand(routineIterateCmd)
}
