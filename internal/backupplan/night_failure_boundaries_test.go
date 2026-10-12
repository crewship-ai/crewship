package backupplan

import (
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestNightHistoryPreservesFailuresAndRetryResolution(t *testing.T) {
	now := mustTime(t, "2026-10-03T12:00:00Z")
	retry := now.Add(-time.Hour)
	for _, tc := range []struct {
		name           string
		run            Run
		status, detail string
	}{
		{"failed", Run{Status: StatusFailed, Error: "disk full"}, "failed", "disk full"},
		{"interrupted", Run{Status: StatusInterrupted, Error: "server stopped"}, "failed", "server stopped"},
		{"retried interruption", Run{Status: StatusInterrupted, RetriedAt: &retry, Error: "server stopped"}, "none", ""},
		{"skipped", Run{Status: StatusSkipped, Error: "workspace busy"}, "skipped", "workspace busy"},
		{"incomplete without details", Run{Status: StatusIncomplete}, "incomplete", "created · incomplete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.run.StartedAt = now
			nights := buildNights([]*Run{&tc.run}, nil, now, time.UTC)
			got := nights[len(nights)-1]
			if len(nights) != 14 || got.Status != tc.status || got.Proof != 0 {
				t.Fatalf("history: %+v", nights)
			}
			if tc.detail == "" {
				if got.Detail != nil {
					t.Fatalf("resolved failure retained: %q", *got.Detail)
				}
			} else if got.Detail == nil || *got.Detail != tc.detail {
				t.Fatalf("failure detail: %+v", got)
			}
		})
	}
	// A catch-up belongs to the missed night, carrying proof from its later bundle.
	due := now.AddDate(0, 0, -1)
	nights := buildNights([]*Run{{Status: StatusDone, Trigger: TriggerCatchup, DueAt: &due, StartedAt: now, BundlePath: "/owned/backup"}}, []backup.CatalogEntry{{FilePath: "/owned/backup", CreatedAt: now, ProofLevel: backup.ProofContents}}, now, time.UTC)
	if got := nights[12]; got.Status != "late" || got.Proof != backup.ProofContents || got.Detail == nil {
		t.Fatalf("missed night lost catch-up proof: %+v", got)
	}
	if got := nights[13]; got.Status != "ok" || got.Proof != backup.ProofContents {
		t.Fatalf("bundle night: %+v", got)
	}
}
