package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
)

// The default verifier exercises the same scripts and public API as the Pages.
// It leaves decisions to the operator unless --complete-demo is explicit.
func verifyBusiness(ctx context.Context, client *cli.Client, timeout time.Duration, complete bool) ([]verifyCheck, error) {
	crews, _, err := verifyListCrews(client)
	if err != nil {
		return nil, err
	}
	var checks []verifyCheck
	add := func(story, step string, err error) {
		r, d := verifyPass, "verified"
		if err != nil {
			r, d = verifyFail, err.Error()
		}
		checks = append(checks, verifyCheck{story, step, r, d})
	}
	for _, s := range seeddata.Stories {
		crewID := crews[s.Crew]
		if crewID == "" {
			add(s.Slug, "crew", fmt.Errorf("missing crew %s", s.Crew))
			continue
		}
		filesOK := true
		for _, f := range seeddata.StoryFiles {
			if f.CrewSlug != s.Crew {
				continue
			}
			want, e := seeddata.StoryFileContent(f.Source)
			if e == nil {
				var got []byte
				got, e = verifyDownloadCrewFile(ctx, client, crewID, f.Dest)
				if e == nil && !bytes.Equal(got, want) {
					e = fmt.Errorf("delivered file differs: %s", f.Dest)
				}
			}
			if e != nil {
				add(s.Slug, "files", e)
				filesOK = false
				break
			}
		}
		if !filesOK {
			continue
		}
		add(s.Slug, "files", nil)
		run, e := verifyRunRoutine(ctx, client, client.GetWorkspaceID(), "demo-"+s.Slug+"-check", timeout)
		add(s.Slug, "check routine", e)
		if e != nil {
			continue
		}
		add(s.Slug, "check outcome", verifyBusinessOutcome(client, run.ID))
		add(s.Slug, "Page provenance", verifyBusinessPage(client, s.Slug, run.ID, "records", "finding"))
		raw, ok := stepOutput(run, "inspect")
		var output struct {
			Issue   string `json:"issue" yaml:"issue"`
			Records struct {
				Rows []map[string]any `json:"rows" yaml:"rows"`
			} `json:"records" yaml:"records"`
		}
		if !ok {
			add(s.Slug, "checked records", fmt.Errorf("inspect output absent"))
			continue
		}
		e = json.Unmarshal([]byte(raw), &output)
		if e == nil && (output.Issue == "" || len(output.Records.Rows) != len(s.Rows)) {
			e = fmt.Errorf("missing Issue binding or wrong record count")
		}
		add(s.Slug, "checked records", e)
		if e != nil {
			continue
		}
		var linked issueItem
		e = verifyBusinessGet(client, "/api/v1/issues/"+url.PathEscape(output.Issue), &linked)
		if e == nil {
			var expected string
			expected, e = resolveAgentID(client, s.Agent)
			if e == nil && (linked.CrewID != crewID || linked.AssigneeID == nil || *linked.AssigneeID != expected || linked.CommentCount < 1) {
				e = fmt.Errorf("Issue must belong to its story crew, be assigned to its agent and contain the check comment")
			}
		}
		add(s.Slug, "Issue assignment and comment", e)
		add(s.Slug, "Inbox notification", verifyBusinessInbox(client, s.Project+": check completed", run.StartedAt))
		if complete {
			resolved, e := verifyBusinessResolve(ctx, client, s.Slug, timeout)
			add(s.Slug, "resolve and approve local demo", e)
			if e != nil {
				continue
			}
			add(s.Slug, "resolve outcome", verifyBusinessOutcome(client, resolved.ID))
			add(s.Slug, "resolution Page", verifyBusinessPage(client, s.Slug, resolved.ID, "outcome", "resolved-records"))
			var issue struct {
				Status string `json:"status" yaml:"status"`
			}
			e = verifyBusinessGet(client, "/api/v1/issues/"+url.PathEscape(output.Issue), &issue)
			if e == nil && issue.Status != "DONE" {
				e = fmt.Errorf("Issue %s is %s, expected DONE", output.Issue, issue.Status)
			}
			add(s.Slug, "Issue completed", e)
			artifact, e := verifyDownloadCrewFile(ctx, client, crewID, "shared/demo/business/outbox/"+s.Slug+".json")
			if e == nil {
				var a struct {
					Story    string           `json:"story" yaml:"story"`
					Issue    string           `json:"issue" yaml:"issue"`
					Text     string           `json:"text" yaml:"text"`
					Delivery string           `json:"delivery" yaml:"delivery"`
					Evidence []map[string]any `json:"evidence" yaml:"evidence"`
				}
				e = json.Unmarshal(artifact, &a)
				if e == nil && (a.Story != s.Slug || a.Issue != output.Issue) {
					e = fmt.Errorf("outbox artifact does not match story and Issue")
				}
				if e == nil && (a.Text == "" || a.Delivery != "local demo only" || len(a.Evidence) != 1 || a.Evidence[0]["id"] == nil) {
					e = fmt.Errorf("outbox must contain proposed text, local-delivery label and the source finding")
				}
			}
			add(s.Slug, "local evidence", e)
		}
	}
	return checks, nil
}

// Technical completion alone is insufficient: a missing agent hand-off can
// leave status=completed while the authoritative outcome is FAILED.
func verifyBusinessOutcome(client *cli.Client, runID string) error {
	var run struct {
		Outcome string `json:"outcome" yaml:"outcome"`
		Error   string `json:"error_message" yaml:"error_message"`
	}
	path := "/api/v1/workspaces/" + url.PathEscape(client.GetWorkspaceID()) + "/pipeline-runs/" + url.PathEscape(runID)
	if err := verifyBusinessGet(client, path, &run); err != nil {
		return err
	}
	if run.Outcome != "SUCCEEDED" || run.Error != "" {
		return fmt.Errorf("run %s outcome=%s: %s", runID, run.Outcome, run.Error)
	}
	return nil
}
func verifyBusinessGet(client *cli.Client, path string, out any) error {
	r, e := client.Get(path)
	if e != nil {
		return e
	}
	defer r.Body.Close()
	if e = cli.CheckError(r); e != nil {
		return e
	}
	return json.NewDecoder(r.Body).Decode(out)
}
func verifyBusinessPage(client *cli.Client, story, run string, ids ...string) error {
	var p struct {
		Panels []struct {
			ID         string `json:"id" yaml:"id"`
			Provenance struct {
				RunID string `json:"run_id" yaml:"run_id"`
			} `json:"provenance" yaml:"provenance"`
		} `json:"panels" yaml:"panels"`
	}
	if e := verifyBusinessGet(client, "/api/v1/pages/"+url.PathEscape("demo-"+story), &p); e != nil {
		return e
	}
	for _, id := range ids {
		found := false
		for _, panel := range p.Panels {
			if panel.ID == id && panel.Provenance.RunID == run {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("panel %s was not written by run %s", id, run)
		}
	}
	return nil
}
func verifyBusinessInbox(client *cli.Client, title, since string) error {
	var list struct {
		Rows []struct {
			Title   string `json:"title" yaml:"title"`
			Created string `json:"created_at" yaml:"created_at"`
		} `json:"rows" yaml:"rows"`
	}
	if e := verifyBusinessGet(client, "/api/v1/inbox?state=all&limit=100", &list); e != nil {
		return e
	}
	start, _ := time.Parse(time.RFC3339, since)
	for _, r := range list.Rows {
		at, _ := time.Parse(time.RFC3339, r.Created)
		if r.Title == title && !at.Before(start.Add(-time.Second)) {
			return nil
		}
	}
	return fmt.Errorf("current run notification not found: %s", title)
}
func verifyBusinessResolve(ctx context.Context, client *cli.Client, story string, timeout time.Duration) (*cli.PipelineRunDetail, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client = client.WithContext(ctx)
	ws := client.GetWorkspaceID()
	slug := "demo-" + story + "-resolve"
	since := time.Now()
	r, e := client.Post("/api/v1/workspaces/"+ws+"/pipelines/"+url.PathEscape(slug)+"/run", map[string]any{"inputs": map[string]any{}, "delay_seconds": 1})
	if e != nil {
		return nil, e
	}
	if e = cli.CheckError(r); e != nil {
		r.Body.Close()
		return nil, e
	}
	r.Body.Close()
	id, e := awaitParkedRun(ctx, client, ws, slug, since)
	if e != nil {
		return nil, e
	}
	for {
		run, e := client.GetPipelineRun(ctx, id)
		if e != nil {
			return nil, e
		}
		if run.IsTerminal() {
			if run.Status != "completed" {
				return nil, fmt.Errorf("%s: %s at %s: %s", id, run.Status, run.FailedAtStep, run.ErrorMessage)
			}
			return run, nil
		}
		var waits []waitpointRow
		if e = verifyBusinessGet(client, "/api/v1/workspaces/"+ws+"/pipelines/waitpoints", &waits); e != nil {
			return nil, e
		}
		for _, w := range waits {
			if w.PipelineRunID != id {
				continue
			}
			r, e = client.Post("/api/v1/workspaces/"+ws+"/pipelines/waitpoints/"+url.PathEscape(w.Token)+"/approve", map[string]any{"approved": true, "action_id": "approve", "data": map[string]any{}, "comment": "Demo acceptance test: approve local artifact only"})
			if e != nil {
				return nil, e
			}
			e = cli.CheckError(r)
			r.Body.Close()
			if e != nil {
				return nil, e
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
