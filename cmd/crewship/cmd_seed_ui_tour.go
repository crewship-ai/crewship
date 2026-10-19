package main

// `crewship seed --ui-tour` — the additive tour profile (seeddata/ui_tour.go).
//
// It runs AFTER a default seed (on dev slots: after `crew reset --yes`) and
// only adds: scheduled routines grouped by tag, their run history, a longer
// issue board, and the inbox and journal rows those writes produce. Every
// write goes through the HTTP API the product uses; nothing is written to a
// database directly. Each step looks before it writes — routine by slug,
// schedule by routine + name, history by "the routine has runs", project and
// label by name, issue by title in its crew — so a second run adds nothing.
//
// The tour's routines are agentless, so it needs neither provider
// credentials nor model calls, and it does not provision containers.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// uiTourExclusiveFlags are the seed flags that make no sense with the tour:
// it is additive (never --nuke) and seeds nothing the default phases do.
var uiTourExclusiveFlags = []string{
	"nuke", "skip-issues", "with-evals", "smoke-test", "test-backup", "wait-provision",
	"with-memory", "with-team-chat", "with-users", "codex-auth-file",
}

// checkUITourFlags refuses a combination before anything is written.
func checkUITourFlags(cmd *cobra.Command) error {
	for _, name := range uiTourExclusiveFlags {
		if f := cmd.Flags().Lookup(name); f != nil && f.Changed {
			return fmt.Errorf("--ui-tour is additive and cannot be combined with --%s; run `crewship seed` for the default demo first (on dev slots `crew reset --yes`), then `crewship seed --ui-tour`", name)
		}
	}
	return nil
}

// uiTourStats counts what one tour pass created; a second pass reports zeros.
type uiTourStats struct {
	Routines, Tags, Schedules, Runs, Projects, Labels, Issues, Comments int
}

// tourCrewSlugs are the default demo crews the tour builds on.
var tourCrewSlugs = []string{"engineering", "quality", "ops"}

func seedUITour(ctx context.Context, client *cli.Client) (uiTourStats, error) {
	var stats uiTourStats
	wsID := client.GetWorkspaceID()
	if wsID == "" {
		return stats, fmt.Errorf("ui tour: workspace not set on the client")
	}
	crewIDs, err := tourListBySlug(client, "/api/v1/crews")
	if err != nil {
		return stats, fmt.Errorf("ui tour: list crews: %w", err)
	}
	var missing []string
	for _, slug := range tourCrewSlugs {
		if crewIDs[slug] == "" {
			missing = append(missing, slug)
		}
	}
	if len(missing) > 0 {
		return stats, fmt.Errorf("ui tour builds on the default demo crews, missing: %s; run `crewship seed` first (on dev slots `crew reset --yes`)", strings.Join(missing, ", "))
	}
	agentIDs, err := tourListBySlug(client, "/api/v1/agents?limit=500")
	if err != nil {
		return stats, fmt.Errorf("ui tour: list agents: %w", err)
	}

	fmt.Fprintln(os.Stderr, "UI tour: routines, schedules and folders...")
	scheduleIDs, err := seedTourRoutines(ctx, client, wsID, crewIDs, agentIDs, &stats)
	if err != nil {
		return stats, err
	}
	fmt.Fprintln(os.Stderr, "UI tour: run history...")
	if err := seedTourRuns(ctx, client, wsID, scheduleIDs, &stats); err != nil {
		return stats, err
	}
	fmt.Fprintln(os.Stderr, "UI tour: issues...")
	if err := seedTourIssues(ctx, client, crewIDs, agentIDs, &stats); err != nil {
		return stats, err
	}
	return stats, nil
}

// tourListBySlug maps slug → id for a list endpoint that returns a JSON array.
func tourListBySlug(client *cli.Client, path string) (map[string]string, error) {
	resp, err := client.Get(path)
	if err != nil {
		return nil, err
	}
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var items []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	if err := cli.ReadJSON(resp, &items); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(items))
	for _, it := range items {
		if it.Slug != "" {
			out[it.Slug] = it.ID
		}
	}
	return out, nil
}

// tourExists reports whether GET path answers 200 (true) or 404 (false).
func tourExists(client *cli.Client, path string) (bool, error) {
	resp, err := client.Get(path)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if err := cli.CheckError(resp); err != nil {
		return false, err
	}
	return true, nil
}

func tourPost(client *cli.Client, path string, body, out any) (int, error) {
	resp, err := client.Post(path, body)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode == http.StatusConflict {
		resp.Body.Close()
		return resp.StatusCode, nil
	}
	if err := cli.CheckError(resp); err != nil {
		return resp.StatusCode, err
	}
	if out == nil {
		resp.Body.Close()
		return resp.StatusCode, nil
	}
	return resp.StatusCode, cli.ReadJSON(resp, out)
}

// seedTourRoutines saves each routine, files it in its folder tag and
// attaches its schedule. Returns routine slug → schedule id.
func seedTourRoutines(ctx context.Context, client *cli.Client, wsID string, crewIDs, agentIDs map[string]string, stats *uiTourStats) (map[string]string, error) {
	base := "/api/v1/workspaces/" + url.PathEscape(wsID)
	resp, err := client.Get(base + "/pipeline-schedules")
	if err != nil {
		return nil, err
	}
	if err := cli.CheckError(resp); err != nil {
		return nil, fmt.Errorf("list schedules: %w", err)
	}
	var existing []struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		TargetSlug string `json:"target_pipeline_slug"`
	}
	if err := cli.ReadJSON(resp, &existing); err != nil {
		return nil, fmt.Errorf("list schedules: %w", err)
	}
	scheduleIDs := map[string]string{}
	for _, r := range seeddata.TourRoutines {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := base + "/pipelines/" + url.PathEscape(r.Slug)
		found, err := tourExists(client, path)
		if err != nil {
			return nil, fmt.Errorf("routine %s: %w", r.Slug, err)
		}
		if !found {
			body := map[string]any{
				"slug": r.Slug, "name": r.Name, "description": r.Description,
				"definition": r.Definition, "author_crew_id": crewIDs[r.CrewSlug],
				// Same OWNER escape hatches the default seed uses: no live model
				// to test against, and hand-curated token-zero definitions.
				"skip_test_gate": true, "last_test_run_passed": true, "skip_governance_gate": true,
			}
			if id := agentIDs[r.AuthorAgentSlug]; id != "" {
				body["author_agent_id"] = id
			}
			status, err := tourPost(client, base+"/pipelines/save", body, nil)
			if err != nil {
				return nil, fmt.Errorf("routine %s: %w", r.Slug, err)
			}
			if status != http.StatusConflict {
				stats.Routines++
				fmt.Fprintf(os.Stderr, "  + Routine: %s\n", r.Name)
			}
		} else {
			fmt.Fprintf(os.Stderr, "  = Routine exists: %s\n", r.Name)
		}
		if r.Folder != "" {
			added, err := seedTourTag(client, path, r.Folder)
			if err != nil {
				return nil, fmt.Errorf("routine %s folder: %w", r.Slug, err)
			}
			if added {
				stats.Tags++
			}
		}
		if r.Schedule == nil {
			continue
		}
		for _, s := range existing {
			if s.TargetSlug == r.Slug && s.Name == r.Schedule.Name {
				scheduleIDs[r.Slug] = s.ID
			}
		}
		if scheduleIDs[r.Slug] != "" {
			continue
		}
		body := map[string]any{
			"name": r.Schedule.Name, "target_pipeline_slug": r.Slug,
			"cron_expr": r.Schedule.Cron, "timezone": r.Schedule.Timezone, "enabled": true,
		}
		if len(r.Schedule.Inputs) > 0 {
			body["inputs"] = r.Schedule.Inputs
		}
		var created struct {
			ID string `json:"id"`
		}
		if _, err := tourPost(client, base+"/pipeline-schedules", body, &created); err != nil {
			return nil, fmt.Errorf("schedule for %s: %w", r.Slug, err)
		}
		scheduleIDs[r.Slug] = created.ID
		stats.Schedules++
		fmt.Fprintf(os.Stderr, "  + Schedule: %s · %s (%s)\n", r.Name, r.Schedule.Name, r.Schedule.Cron)
	}
	return scheduleIDs, nil
}

// seedTourTag files a routine in its folder unless it is already there.
func seedTourTag(client *cli.Client, routinePath, tag string) (bool, error) {
	resp, err := client.Get(routinePath)
	if err != nil {
		return false, err
	}
	if err := cli.CheckError(resp); err != nil {
		return false, err
	}
	var routine struct {
		Tags []string `json:"tags"`
	}
	if err := cli.ReadJSON(resp, &routine); err != nil {
		return false, err
	}
	for _, t := range routine.Tags {
		if t == tag {
			return false, nil
		}
	}
	put, err := client.Put(routinePath+"/tags", map[string]any{"tags": []string{tag}})
	if err != nil {
		return false, err
	}
	defer put.Body.Close()
	if err := cli.CheckError(put); err != nil {
		return false, err
	}
	var after struct {
		Tags []string `json:"tags"`
	}
	_ = cli.ReadJSON(put, &after)
	// The routine read may not carry tags; only count a tag the PUT reports
	// as new relative to what the routine had.
	return len(after.Tags) > len(routine.Tags), nil
}

type tourRunResult struct {
	RunID          string `json:"run_id"`
	Status         string `json:"status"`
	ErrorMessage   string `json:"error_message"`
	WaitpointToken string `json:"waitpoint_token"`
}

// seedTourRuns plays each routine's history once: a routine that already has
// runs (from an earlier tour, its schedule, or a person) is left alone.
func seedTourRuns(ctx context.Context, client *cli.Client, wsID string, scheduleIDs map[string]string, stats *uiTourStats) error {
	base := "/api/v1/workspaces/" + url.PathEscape(wsID)
	for _, r := range seeddata.TourRoutines {
		if len(r.Runs) == 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		resp, err := client.Get(base + "/pipelines/" + url.PathEscape(r.Slug) + "/run-records?limit=1")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return fmt.Errorf("history of %s: %w", r.Slug, err)
		}
		var prior []struct {
			ID string `json:"id"`
		}
		if err := cli.ReadJSON(resp, &prior); err != nil {
			return fmt.Errorf("history of %s: %w", r.Slug, err)
		}
		if len(prior) > 0 {
			fmt.Fprintf(os.Stderr, "  = %s already has runs\n", r.Name)
			continue
		}
		for i, run := range r.Runs {
			res, err := seedTourRun(client, base, r, run, scheduleIDs[r.Slug])
			if err != nil {
				return fmt.Errorf("%s run %d: %w", r.Slug, i+1, err)
			}
			stats.Runs++
			line := fmt.Sprintf("  + Run: %s → %s", r.Name, strings.ToLower(res.Status))
			if res.ErrorMessage != "" {
				line += " (" + truncate(res.ErrorMessage, 70) + ")"
			}
			fmt.Fprintln(os.Stderr, line)
			// Distinct start times keep the history in the order it was
			// authored on every list that sorts by start.
			time.Sleep(250 * time.Millisecond)
		}
	}
	return nil
}

func seedTourRun(client *cli.Client, base string, r seeddata.TourRoutine, run seeddata.TourRun, scheduleID string) (tourRunResult, error) {
	var res tourRunResult
	var path string
	var body any
	if run.ViaSchedule {
		if scheduleID == "" {
			return res, fmt.Errorf("history asks for a scheduled run but the routine has no schedule")
		}
		path, body = base+"/pipeline-schedules/"+url.PathEscape(scheduleID)+"/run", map[string]any{}
	} else {
		inputs := run.Inputs
		if inputs == nil {
			inputs = map[string]interface{}{}
		}
		path, body = base+"/pipelines/"+url.PathEscape(r.Slug)+"/run", map[string]any{"inputs": inputs}
	}
	if _, err := tourPost(client, path, body, &res); err != nil {
		return res, err
	}
	// A run that is approved below first parks at its approval.
	want := run.Expect
	if run.Approve != "" {
		want = "waiting"
	}
	if got := strings.ToLower(res.Status); got != want {
		return res, fmt.Errorf("run %s ended %q, the tour expects %q (%s)", res.RunID, got, want, res.ErrorMessage)
	}
	if run.Approve == "" {
		return res, nil
	}
	if res.WaitpointToken == "" {
		return res, fmt.Errorf("run %s is waiting without an approval token", res.RunID)
	}
	if _, err := tourPost(client, base+"/pipelines/waitpoints/"+url.PathEscape(res.WaitpointToken)+"/approve",
		map[string]any{"approved": true, "action_id": run.Approve, "data": map[string]any{}, "comment": "Looks right, go ahead."}, nil); err != nil {
		return res, fmt.Errorf("approve: %w", err)
	}
	// The approved run resumes in the background; wait for it so the
	// history reads completed and a re-run never sees it mid-flight.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var rec struct {
			Status       string `json:"status"`
			ErrorMessage string `json:"error_message"`
		}
		resp, err := client.Get(base + "/pipeline-runs/" + url.PathEscape(res.RunID))
		if err != nil {
			return res, err
		}
		if err := cli.CheckError(resp); err != nil {
			return res, err
		}
		if err := cli.ReadJSON(resp, &rec); err != nil {
			return res, err
		}
		switch rec.Status {
		case run.Expect:
			res.Status = rec.Status
			return res, nil
		case "failed", "cancelled", "interrupted":
			return res, fmt.Errorf("approved run %s ended %s: %s", res.RunID, rec.Status, rec.ErrorMessage)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return res, errors.New("approved run " + res.RunID + " did not finish within 20s")
}

// seedTourIssues creates the tour board: labels and projects by name, then
// each issue unless one with the same title already lives in its crew.
func seedTourIssues(ctx context.Context, client *cli.Client, crewIDs, agentIDs map[string]string, stats *uiTourStats) error {
	labelIDs := map[string]string{}
	for _, l := range seeddata.TourLabels {
		id, err := tourFindByName(client, "/api/v1/labels", l.Name)
		if err != nil {
			return fmt.Errorf("label %s: %w", l.Name, err)
		}
		if id == "" {
			var created struct {
				ID string `json:"id"`
			}
			if _, err := tourPost(client, "/api/v1/labels", l, &created); err != nil {
				return fmt.Errorf("label %s: %w", l.Name, err)
			}
			if id = created.ID; id == "" {
				if id, err = resolveByName(client, "/api/v1/labels", l.Name); err != nil {
					return fmt.Errorf("label %s: %w", l.Name, err)
				}
			} else {
				stats.Labels++
				fmt.Fprintf(os.Stderr, "  + Label: %s\n", l.Name)
			}
		}
		labelIDs[l.Name] = id
	}
	projectIDs := map[string]string{}
	for _, p := range seeddata.TourProjects {
		id, err := tourFindByName(client, "/api/v1/projects", p.Name)
		if err != nil {
			return fmt.Errorf("project %s: %w", p.Name, err)
		}
		if id == "" {
			var created struct {
				ID string `json:"id"`
			}
			status, err := tourPost(client, "/api/v1/projects", map[string]any{
				"name": p.Name, "color": p.Color, "icon": p.Icon, "status": p.Status, "priority": p.Priority,
			}, &created)
			if err != nil {
				return fmt.Errorf("project %s: %w", p.Name, err)
			}
			if status == http.StatusConflict || created.ID == "" {
				if id, err = resolveByName(client, "/api/v1/projects", p.Name); err != nil {
					return fmt.Errorf("project %s: %w", p.Name, err)
				}
			} else {
				id = created.ID
				stats.Projects++
				fmt.Fprintf(os.Stderr, "  + Project: %s\n", p.Name)
			}
		}
		projectIDs[p.Name] = id
	}

	for _, def := range seeddata.TourIssues {
		if err := ctx.Err(); err != nil {
			return err
		}
		crewID := crewIDs[def.CrewSlug]
		exists, err := tourIssueExists(client, crewID, def.Title)
		if err != nil {
			return fmt.Errorf("issue %q: %w", def.Title, err)
		}
		if exists {
			continue
		}
		body := map[string]any{"title": def.Title, "description": def.Description, "priority": def.Priority}
		if pid := projectIDs[def.Project]; pid != "" {
			body["project_id"] = pid
		}
		var created struct {
			ID         string  `json:"id"`
			Identifier *string `json:"identifier"`
		}
		issuesPath := "/api/v1/crews/" + url.PathEscape(crewID) + "/issues"
		if _, err := tourPost(client, issuesPath, body, &created); err != nil {
			return fmt.Errorf("issue %q: %w", def.Title, err)
		}
		if created.Identifier == nil || *created.Identifier == "" {
			return fmt.Errorf("issue %q: the server returned no identifier", def.Title)
		}
		ident := *created.Identifier
		stats.Issues++
		fmt.Fprintf(os.Stderr, "  + %s: %s (%s)\n", ident, truncate(def.Title, 55), strings.ToLower(def.State))
		issuePath := issuesPath + "/" + url.PathEscape(ident)
		if len(def.Labels) > 0 {
			if err := attachIssueLabels(client, created.ID, def.Labels, labelIDs); err != nil {
				return fmt.Errorf("%s labels: %w", ident, err)
			}
		}
		if def.Assignee != "" {
			if aid := agentIDs[def.Assignee]; aid != "" {
				if err := tourPatch(client, issuePath, map[string]string{"assignee_type": "agent", "assignee_id": aid}); err != nil {
					return fmt.Errorf("%s assign %s: %w", ident, def.Assignee, err)
				}
			} else {
				fmt.Fprintf(os.Stderr, "    ! agent %q not found, %s stays unassigned\n", def.Assignee, ident)
			}
		}
		for _, status := range seeddata.StatusPath(def.State) {
			if err := tourPatch(client, issuePath, map[string]string{"status": status}); err != nil {
				return fmt.Errorf("%s → %s: %w", ident, status, err)
			}
		}
		for _, comment := range def.Comments {
			if _, err := tourPost(client, issuePath+"/comments", map[string]string{"body": comment}, nil); err != nil {
				return fmt.Errorf("%s comment: %w", ident, err)
			}
			stats.Comments++
		}
	}
	return nil
}

// tourFindByName returns the id of the named item, or "" when there is none.
func tourFindByName(client *cli.Client, path, name string) (string, error) {
	resp, err := client.Get(path)
	if err != nil {
		return "", err
	}
	if err := cli.CheckError(resp); err != nil {
		return "", err
	}
	var items []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := cli.ReadJSON(resp, &items); err != nil {
		return "", err
	}
	for _, it := range items {
		if it.Name == name {
			return it.ID, nil
		}
	}
	return "", nil
}

func tourPatch(client *cli.Client, path string, body any) error {
	resp, err := client.Patch(path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return cli.CheckError(resp)
}

func tourIssueExists(client *cli.Client, crewID, title string) (bool, error) {
	resp, err := client.Get("/api/v1/issues?q=" + url.QueryEscape(title) + "&limit=100")
	if err != nil {
		return false, err
	}
	if err := cli.CheckError(resp); err != nil {
		return false, err
	}
	var items []issueItem
	if err := cli.ReadJSON(resp, &items); err != nil {
		return false, err
	}
	for _, it := range items {
		if it.Title == title && it.CrewID == crewID {
			return true, nil
		}
	}
	return false, nil
}
