package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/robfig/cron/v3"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// The tour catalogue is checked offline before it ever reaches a server: a
// routine that does not validate, reaches for a model, or names a folder that
// does not exist would only surface as a seed failure on a dev slot.
func TestUITourRoutinesAreValidAgentlessAndUnique(t *testing.T) {
	defaultSlugs := map[string]bool{}
	for _, r := range seeddata.Routines {
		defaultSlugs[r.Slug] = true
	}
	folders := map[string]bool{}
	for _, f := range seeddata.TourFolders {
		folders[f.Tag] = true
	}
	seen := map[string]bool{}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	var waiting, failed, scheduled int
	for _, r := range seeddata.TourRoutines {
		t.Run(r.Slug, func(t *testing.T) {
			if seen[r.Slug] || defaultSlugs[r.Slug] {
				t.Fatalf("slug %q collides with another seeded routine", r.Slug)
			}
			seen[r.Slug] = true
			raw, err := json.Marshal(r.Definition)
			if err != nil {
				t.Fatal(err)
			}
			dsl, err := pipeline.Parse(raw)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if err := pipeline.Validate(dsl, map[string]struct{}{}, map[string]struct{}{}); err != nil {
				t.Fatalf("validate: %v", err)
			}
			if !dsl.Agentless {
				t.Fatal("tour routines must be agentless (no provider credentials, no model spend when schedules fire)")
			}
			if !folders[r.Folder] {
				t.Fatalf("folder %q is not one of the tour folders", r.Folder)
			}
			if r.Schedule != nil {
				scheduled++
				if _, err := parser.Parse(r.Schedule.Cron); err != nil {
					t.Fatalf("cron %q: %v", r.Schedule.Cron, err)
				}
				if err := pipeline.ValidateFormInputs(dsl, r.Schedule.Inputs); err != nil {
					t.Fatalf("schedule inputs: %v", err)
				}
			}
			for i, run := range r.Runs {
				switch run.Expect {
				case "completed", "failed", "waiting":
				default:
					t.Fatalf("run %d expects %q", i, run.Expect)
				}
				if run.ViaSchedule && r.Schedule == nil {
					t.Fatalf("run %d fires a schedule the routine does not have", i)
				}
				if run.Inputs != nil {
					if err := pipeline.ValidateFormInputs(dsl, run.Inputs); err != nil {
						t.Fatalf("run %d inputs: %v", i, err)
					}
				}
				if run.Expect == "waiting" {
					waiting++
				}
				if run.Expect == "failed" {
					failed++
				}
			}
		})
	}
	if waiting == 0 || failed == 0 || scheduled < 6 {
		t.Fatalf("tour history needs a waiting run, a failed run and at least six schedules; got waiting=%d failed=%d scheduled=%d", waiting, failed, scheduled)
	}
}

func TestUITourIssuesAreReachableAndReadLikeWork(t *testing.T) {
	if n := len(seeddata.TourIssues); n < 15 || n > 25 {
		t.Fatalf("tour board has %d issues, want 15-25", n)
	}
	projects := map[string]bool{}
	for _, p := range seeddata.TourProjects {
		projects[p.Name] = true
	}
	labels := map[string]bool{}
	for _, l := range seeddata.TourLabels {
		labels[l.Name] = true
	}
	agents := map[string]string{}
	for _, a := range seeddata.Agents {
		agents[a.Slug] = a.CrewSlug
	}
	titles := map[string]bool{}
	for _, is := range seeddata.TourIssues {
		t.Run(is.Title, func(t *testing.T) {
			if titles[is.Title] {
				t.Fatal("duplicate title: idempotency matches issues by title")
			}
			titles[is.Title] = true
			if !projects[is.Project] {
				t.Fatalf("project %q is not in TourProjects", is.Project)
			}
			if seeddata.StatusPath(is.State) == nil {
				t.Fatalf("state %q is not reachable from BACKLOG", is.State)
			}
			if is.Assignee != "" && agents[is.Assignee] != is.CrewSlug {
				t.Fatalf("assignee %q is not an agent of crew %q", is.Assignee, is.CrewSlug)
			}
			for _, l := range is.Labels {
				if !labels[l] {
					t.Fatalf("label %q is not in TourLabels", l)
				}
			}
			for _, c := range is.Comments {
				if strings.Contains(c, "@") {
					t.Fatalf("comment %q could mention an agent and wake it", c)
				}
			}
		})
	}
}

func TestCheckUITourFlags(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"tour alone", []string{"--ui-tour"}, ""},
		{"offline demo is compatible", []string{"--ui-tour", "--offline-demo"}, ""},
		{"password is compatible", []string{"--ui-tour", "--password", "x"}, ""},
		{"nuke is refused", []string{"--ui-tour", "--nuke"}, "--nuke"},
		{"team chat is refused", []string{"--ui-tour", "--with-team-chat"}, "--with-team-chat"},
		{"smoke test is refused", []string{"--ui-tour", "--smoke-test"}, "--smoke-test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A fresh flag set shaped like seed's: parsing into seedCmd's own
			// flags would leak values into other tests.
			cmd := &cobra.Command{Use: "seed"}
			seedCmd.Flags().VisitAll(func(f *pflag.Flag) {
				if f.Value.Type() == "bool" {
					cmd.Flags().Bool(f.Name, false, "")
				} else {
					cmd.Flags().String(f.Name, "", "")
				}
			})
			if err := cmd.Flags().Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			err := checkUITourFlags(cmd)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected refusal: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want one naming %s", err, tc.wantErr)
			}
		})
	}
}
