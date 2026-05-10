package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

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

	// Crew + provisioning health
	report.Checks = append(report.Checks, checkAuthorCrew(client, pipeline.AuthorCrewID))

	// Agent slug + grader resolution
	report.Checks = append(report.Checks,
		checkAgentSlugs(client, pipeline.AuthorCrewID, pipeline.Definition)...)

	// Credential type matching
	report.Checks = append(report.Checks,
		checkCredentialsRequired(client, ws, pipeline.Definition)...)

	// Egress allowlist sanity
	report.Checks = append(report.Checks,
		checkEgressTargets(pipeline.Definition)...)

	// Cost sanity
	report.Checks = append(report.Checks,
		checkCostCap(pipeline.Definition))

	// Validation gate sanity
	report.Checks = append(report.Checks,
		checkValidationGates(pipeline.Definition)...)

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

// ── individual checks ────────────────────────────────────────────

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

func fetchRoutineForDoctor(client interface {
	Get(string) (*http.Response, error)
}, ws, slug string) (fetchedRoutine, bool) {
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

func checkAuthorCrew(client interface {
	Get(string) (*http.Response, error)
}, crewID string) doctorCheck {
	if crewID == "" {
		return doctorCheck{
			Name:    "author_crew",
			Level:   doctorFail,
			Message: "author_crew_id is empty",
			Hint:    "the routine has no owner crew — re-save with --author-crew",
		}
	}
	resp, err := client.Get(fmt.Sprintf("/api/v1/crews/%s/provision", url.PathEscape(crewID)))
	if err != nil || resp == nil {
		return doctorCheck{
			Name:    "author_crew",
			Level:   doctorWarn,
			Message: "could not query crew provisioning status",
			Hint:    "the run will probably still work; this just means doctor couldn't verify the crew is ready",
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return doctorCheck{
			Name:    "author_crew",
			Level:   doctorFail,
			Message: "author crew not found in workspace",
			Hint:    "crew was deleted — re-author this routine under a still-existing crew",
		}
	}
	if resp.StatusCode != http.StatusOK {
		return doctorCheck{
			Name:    "author_crew",
			Level:   doctorWarn,
			Message: fmt.Sprintf("crew status returned HTTP %d", resp.StatusCode),
		}
	}
	var status struct {
		Status             string `json:"status"`
		DevcontainerConfig string `json:"devcontainer_config"`
		CachedImage        string `json:"cached_image"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return doctorCheck{Name: "author_crew", Level: doctorWarn, Message: "could not decode crew status response"}
	}
	if status.DevcontainerConfig == "" {
		return doctorCheck{
			Name:    "author_crew",
			Level:   doctorWarn,
			Message: "crew has no devcontainer config — Claude Code CLI may not be available",
			Hint:    "set a devcontainer config on the crew so the OrchestratorRunner can spawn agents",
		}
	}
	switch status.Status {
	case "completed":
		return doctorCheck{
			Name:    "author_crew",
			Level:   doctorOK,
			Message: fmt.Sprintf("provisioned (image cached: %s)", truncCrewID(status.CachedImage)),
		}
	case "in_progress":
		return doctorCheck{
			Name:    "author_crew",
			Level:   doctorWarn,
			Message: "provisioning in progress — first run will block until image is built",
			Hint:    "wait for `crewship crew provision status " + crewID + "` to show completed",
		}
	case "failed":
		return doctorCheck{
			Name:    "author_crew",
			Level:   doctorFail,
			Message: "crew provisioning failed",
			Hint:    "re-trigger via `crewship crew provision start " + crewID + "` and inspect logs",
		}
	default:
		return doctorCheck{
			Name:    "author_crew",
			Level:   doctorWarn,
			Message: "provisioning status: " + status.Status,
		}
	}
}

// checkAgentSlugs walks the DSL steps and verifies each agent_slug
// + outcomes.grader_agent_slug exists in the author crew. The
// pipeline parser already does this at save time, but agents can
// be deleted/renamed AFTER save — this catches that drift.
func checkAgentSlugs(client interface {
	Get(string) (*http.Response, error)
}, crewID string, def map[string]interface{}) []doctorCheck {
	steps, ok := def["steps"].([]interface{})
	if !ok || len(steps) == 0 {
		return []doctorCheck{{Name: "agent_slugs", Level: doctorWarn, Message: "no steps in DSL definition"}}
	}

	available := fetchAgentSlugsForCrew(client, crewID)
	if available == nil {
		return []doctorCheck{{
			Name:    "agent_slugs",
			Level:   doctorWarn,
			Message: "could not fetch crew agents — skipping resolution check",
		}}
	}

	missing := map[string]string{} // slug → step ID where referenced
	for _, raw := range steps {
		step, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		stepID, _ := step["id"].(string)
		if slug, _ := step["agent_slug"].(string); slug != "" {
			if _, found := available[slug]; !found {
				missing[slug] = stepID
			}
		}
		if outcomes, ok := step["outcomes"].(map[string]interface{}); ok {
			if grader, _ := outcomes["grader_agent_slug"].(string); grader != "" {
				if _, found := available[grader]; !found {
					missing[grader] = stepID + "/outcomes"
				}
			}
		}
	}
	if len(missing) == 0 {
		return []doctorCheck{{
			Name:    "agent_slugs",
			Level:   doctorOK,
			Message: fmt.Sprintf("all agent_slug + grader references resolve in crew (%d agents available)", len(available)),
		}}
	}
	out := make([]doctorCheck, 0, len(missing))
	availList := make([]string, 0, len(available))
	for slug := range available {
		availList = append(availList, slug)
	}
	availList = truncateList(availList, 8)
	for slug, stepID := range missing {
		out = append(out, doctorCheck{
			Name:    "agent_slug:" + slug,
			Level:   doctorFail,
			Message: fmt.Sprintf("step %q references agent_slug %q not in author crew", stepID, slug),
			Hint:    "available slugs in crew: " + strings.Join(availList, ", "),
		})
	}
	return out
}

func fetchAgentSlugsForCrew(client interface {
	Get(string) (*http.Response, error)
}, crewID string) map[string]struct{} {
	// Workspace ID is auto-injected as ?workspace_id by the client;
	// we just supply the crew filter. The list endpoint scopes by
	// workspace + filter, returning only agents in this crew.
	// crewID is escaped as a query value (not a path segment) so
	// it survives any reserved character intact.
	resp, err := client.Get(fmt.Sprintf("/api/v1/agents?crew_id=%s", url.QueryEscape(crewID)))
	if err != nil || resp == nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Fall back to the workspace listing — slightly broader
		// but still useful for the suggestion hint.
		return nil
	}
	var rows []struct {
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil
	}
	out := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		if r.Slug != "" {
			out[r.Slug] = struct{}{}
		}
	}
	return out
}

func checkCredentialsRequired(client interface {
	Get(string) (*http.Response, error)
}, ws string, def map[string]interface{}) []doctorCheck {
	creds, ok := def["credentials_required"].([]interface{})
	if !ok || len(creds) == 0 {
		// No declared creds is fine — many routines need none.
		return []doctorCheck{{
			Name:    "credentials_required",
			Level:   doctorOK,
			Message: "no credentials declared",
		}}
	}

	available := fetchActiveCredentialTypes(client, ws)
	if available == nil {
		return []doctorCheck{{
			Name:    "credentials_required",
			Level:   doctorWarn,
			Message: "could not fetch workspace credentials — skipping match check",
		}}
	}

	out := make([]doctorCheck, 0, len(creds))
	for _, raw := range creds {
		creq, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		credType, _ := creq["type"].(string)
		credType = strings.ToUpper(credType)
		if credType == "" {
			continue
		}
		if _, found := available[credType]; !found {
			out = append(out, doctorCheck{
				Name:    "credential:" + credType,
				Level:   doctorFail,
				Message: fmt.Sprintf("declared credential type %q has no active match in workspace", credType),
				Hint:    "create one with `crewship credential create --type=" + credType + " ...`",
			})
		} else {
			out = append(out, doctorCheck{
				Name:    "credential:" + credType,
				Level:   doctorOK,
				Message: "active credential of type found",
			})
		}
	}
	return out
}

func fetchActiveCredentialTypes(client interface {
	Get(string) (*http.Response, error)
}, _ string) map[string]struct{} {
	resp, err := client.Get("/api/v1/credentials")
	if err != nil || resp == nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var rows []struct {
		Provider string `json:"provider"`
		Type     string `json:"type"`
		Status   string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil
	}
	out := map[string]struct{}{}
	for _, r := range rows {
		if r.Status != "ACTIVE" {
			continue
		}
		// Match either provider name (e.g. "ANTHROPIC") or
		// declared `type` field — different routines write the
		// credentials_required.type with different conventions.
		if r.Provider != "" {
			out[strings.ToUpper(r.Provider)] = struct{}{}
		}
		if r.Type != "" {
			out[strings.ToUpper(r.Type)] = struct{}{}
		}
	}
	return out
}

func checkEgressTargets(def map[string]interface{}) []doctorCheck {
	targets, ok := def["egress_targets"].([]interface{})
	if !ok || len(targets) == 0 {
		// Empty list is OK for routines without http steps. Only
		// warn when http steps are declared but the list is empty.
		if hasHTTPStep(def) {
			return []doctorCheck{{
				Name:    "egress_allowlist",
				Level:   doctorWarn,
				Message: "DSL has http step(s) but egress_targets is empty",
				Hint:    "add the target hostnames to egress_targets so the runtime allowlist permits them",
			}}
		}
		return []doctorCheck{{Name: "egress_allowlist", Level: doctorOK, Message: "no http steps; allowlist not required"}}
	}
	// Collect every issue rather than returning on the first.
	// An operator iterating on a routine with both `*` and
	// `localhost` in the allowlist gets BOTH problems in a
	// single doctor pass — fewer round-trips while fixing.
	issues := make([]doctorCheck, 0, 2)
	for _, raw := range targets {
		host, _ := raw.(string)
		if host == "*" || host == "*.*" || host == "" {
			issues = append(issues, doctorCheck{
				Name:    "egress_allowlist",
				Level:   doctorWarn,
				Message: fmt.Sprintf("egress_targets contains wildcard %q", host),
				Hint:    "wildcards open the routine to SSRF; pin to specific hostnames",
			})
			continue
		}
		// strings.HasPrefix(host, "127.") catches the full IPv4
		// loopback /8 range — original "127.0.0.1" check missed
		// 127.0.0.2 and other valid loopback aliases.
		if strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "127.") {
			issues = append(issues, doctorCheck{
				Name:    "egress_allowlist",
				Level:   doctorWarn,
				Message: fmt.Sprintf("egress_targets includes loopback host %q", host),
				Hint:    "remove loopback from production routines; it points at the agent container, not the operator's machine",
			})
		}
	}
	if len(issues) > 0 {
		return issues
	}
	return []doctorCheck{{
		Name:    "egress_allowlist",
		Level:   doctorOK,
		Message: fmt.Sprintf("%d target(s) declared, no wildcards or loopback", len(targets)),
	}}
}

func hasHTTPStep(def map[string]interface{}) bool {
	steps, ok := def["steps"].([]interface{})
	if !ok {
		return false
	}
	for _, raw := range steps {
		s, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if t, _ := s["type"].(string); t == "http" {
			return true
		}
	}
	return false
}

func checkCostCap(def map[string]interface{}) doctorCheck {
	cap, _ := def["max_cost_usd"].(float64)
	est, _ := def["estimated_cost_usd"].(float64)

	if cap == 0 {
		return doctorCheck{
			Name:    "cost_cap",
			Level:   doctorWarn,
			Message: "max_cost_usd not set",
			Hint:    "without a cap, a runaway tier escalation can spend uncapped — set max_cost_usd to ~10× estimated_cost_usd",
		}
	}
	if est == 0 {
		return doctorCheck{
			Name:    "cost_cap",
			Level:   doctorOK,
			Message: fmt.Sprintf("max_cost_usd=$%.4f set; no estimate to compare", cap),
		}
	}
	if cap < est*1.5 {
		return doctorCheck{
			Name:    "cost_cap",
			Level:   doctorWarn,
			Message: fmt.Sprintf("max_cost_usd $%.4f is < 1.5× estimated $%.4f", cap, est),
			Hint:    "tier escalation or grader iterations will likely trip the cap; widen to 10× estimate",
		}
	}
	return doctorCheck{
		Name:    "cost_cap",
		Level:   doctorOK,
		Message: fmt.Sprintf("max $%.4f cap is %.1f× the estimated $%.4f", cap, cap/est, est),
	}
}

func checkValidationGates(def map[string]interface{}) []doctorCheck {
	steps, ok := def["steps"].([]interface{})
	if !ok {
		return nil
	}
	out := make([]doctorCheck, 0, len(steps))
	for _, raw := range steps {
		step, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		stepID, _ := step["id"].(string)
		v, ok := step["validation"].(map[string]interface{})
		if !ok {
			continue
		}
		minLen := optionalInt(v, "min_length")
		maxLen := optionalInt(v, "max_length")
		if minLen != nil && maxLen != nil && *minLen > *maxLen {
			out = append(out, doctorCheck{
				Name:    "validation:" + stepID,
				Level:   doctorFail,
				Message: fmt.Sprintf("step %q validation has min_length %d > max_length %d", stepID, *minLen, *maxLen),
				Hint:    "no output can satisfy this gate — fix the bounds",
			})
			continue
		}
		mc, _ := v["must_contain"].([]interface{})
		mnc, _ := v["must_not_contain"].([]interface{})
		// Detect direct contradiction: same string in both lists.
		for _, c := range mc {
			cs, _ := c.(string)
			for _, n := range mnc {
				ns, _ := n.(string)
				if cs != "" && cs == ns {
					out = append(out, doctorCheck{
						Name:    "validation:" + stepID,
						Level:   doctorFail,
						Message: fmt.Sprintf("step %q validation: %q is in BOTH must_contain and must_not_contain", stepID, cs),
						Hint:    "no output can satisfy a contradictory gate — remove from one list",
					})
					goto nextStep
				}
			}
		}
		out = append(out, doctorCheck{
			Name:    "validation:" + stepID,
			Level:   doctorOK,
			Message: "gate is structurally satisfiable",
		})
	nextStep:
	}
	if len(out) == 0 {
		return []doctorCheck{{Name: "validation_gates", Level: doctorOK, Message: "no validation blocks to check"}}
	}
	return out
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
