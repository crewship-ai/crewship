package backupplan

import (
	"strings"
	"testing"
	"time"
)

func TestBackupMetadataOperationsDoNotAcknowledgeClosedStorage(t *testing.T) {
	db := busyFixtureDB(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	for name, operation := range map[string]func() error{
		"list plans":             func() error { _, err := ListPlans(ctx, db); return err },
		"get plan":               func() error { _, err := GetPlan(ctx, db, "plan"); return err },
		"insert plan":            func() error { return InsertPlan(ctx, db, &Plan{}, "actor", now) },
		"update plan":            func() error { return UpdatePlan(ctx, db, &Plan{ID: "plan"}, "actor", now) },
		"delete plan":            func() error { return DeletePlan(ctx, db, "plan") },
		"get run":                func() error { _, err := GetRun(ctx, db, "run"); return err },
		"list runs":              func() error { _, err := ListRuns(ctx, db, RunFilter{}); return err },
		"get run view":           func() error { _, err := GetRunView(ctx, db, nil, "run"); return err },
		"list run views":         func() error { _, err := ListRunViews(ctx, db, nil, RunFilter{}); return err },
		"settings read":          func() error { _, err := LoadSettings(ctx, db); return err },
		"settings write":         func() error { return SaveSettings(ctx, db, DefaultSettings(), "actor", now) },
		"list recipients":        func() error { _, err := ListRecipients(ctx, db); return err },
		"get recipient":          func() error { _, err := GetRecipient(ctx, db, "recipient"); return err },
		"delete recipient":       func() error { _, err := DeleteRecipient(ctx, db, db, "recipient"); return err },
		"list destinations":      func() error { _, err := ListDestinations(ctx, db); return err },
		"delete destination":     func() error { _, err := DeleteDestination(ctx, db, db, "destination"); return err },
		"record connection test": func() error { return RecordDestinationTest(ctx, db, "destination", now, nil) },
		"record copy":            func() error { return RecordCopy(ctx, db, Copy{BundlePath: "/backup"}) },
		"list copies":            func() error { _, err := CopiesOf(ctx, db, "/backup"); return err },
		"raise incident": func() error {
			_, _, err := RaiseIncident(ctx, db, "plan", "failed", "failure", "run", now, true)
			return err
		},
		"resolve incident":        func() error { _, err := ResolveIncidents(ctx, db, "plan", []string{"failed"}, now); return err },
		"get incident":            func() error { _, err := GetIncident(ctx, db, "incident"); return err },
		"set incident deliveries": func() error { return SetIncidentInboxItems(ctx, db, "incident", nil) },
		"list incidents":          func() error { _, err := ListIncidents(ctx, db, IncidentFilter{}); return err },
		"workspace inventory":     func() error { _, err := LiveWorkspaceIDs(ctx, db); return err },
		"overview":                func() error { _, err := BuildOverview(ctx, db, OverviewInput{Scope: ScopeInstance}, now); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := operation(); err == nil || !strings.Contains(err.Error(), "closed") {
				t.Fatalf("storage failure disappeared or was misclassified: %v", err)
			}
		})
	}
}
