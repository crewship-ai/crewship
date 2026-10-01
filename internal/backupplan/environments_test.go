package backupplan

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestEnvironmentsPhase(t *testing.T) {
	failed := backup.IncompleteItem{Kind: backup.IncompleteEnvironmentFailed, Count: 1, Detail: "x"}
	cases := []struct {
		name       string
		m          *backup.Manifest
		wantStatus string
		wantDetail string
	}{
		{"no manifest", nil, "skipped", "no manifest"},
		{"no crews", &backup.Manifest{}, "skipped", "no crew container"},
		{"captured in the store", &backup.Manifest{Contents: backup.Contents{Environments: []backup.EnvironmentSummary{{Crew: "a", Bytes: 3 << 20}, {Crew: "b", Bytes: 1 << 20}}}},
			"done", "2 environment(s) captured, 4.0 MiB of image layers in the shared environment store"},
		{"captured inline", &backup.Manifest{Contents: backup.Contents{Environments: []backup.EnvironmentSummary{{Crew: "a", Bytes: 10, Inline: true}}}},
			"done", "inside the bundle"},
		{"one failed", &backup.Manifest{Contents: backup.Contents{
			Environments: []backup.EnvironmentSummary{{Crew: "a"}},
			Incomplete:   []backup.IncompleteItem{failed},
		}}, "failed", "1 environment(s) captured, 1 not captured"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, detail := environmentsPhase(tc.m)
			if status != tc.wantStatus || !strings.Contains(detail, tc.wantDetail) {
				t.Errorf("got %s %q, want %s ~%q", status, detail, tc.wantStatus, tc.wantDetail)
			}
		})
	}
}

func TestEnvCalendarStatus(t *testing.T) {
	run := func(phase string) *Run {
		return &Run{Environments: true, Phases: []Phase{{Name: PhaseCopy, Status: "done"}, {Name: PhaseEnvironments, Status: phase}}}
	}
	cases := []struct {
		name   string
		r      *Run
		status string
		want   string
	}{
		{"captured", run("done"), "done", "done"},
		{"captured on a catch-up", run("done"), "catchup", "catchup"},
		{"environment failed", run("failed"), "done", "failed"},
		{"nothing to capture", run("skipped"), "done", "skipped"},
		{"run failed", run("pending"), "failed", "failed"},
		{"run skipped", run("pending"), "skipped", "skipped"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := envCalendarStatus(tc.r, tc.status); got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestRunSpecEnvMode(t *testing.T) {
	if (RunSpec{Environments: true}).EnvMode() != backup.EnvModeComplete || (RunSpec{}).EnvMode() != backup.EnvModeFiles {
		t.Error("env mode does not follow Environments")
	}
}
