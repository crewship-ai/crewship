package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// snapshotFlags records the value + Changed state of every flag on cmd and
// restores both at test end. The RunE tests below mutate package-global Cobra
// command flags (Set + flipping .Changed); without restoring .Changed a flag
// left "changed" leaks into sibling tests that branch on flags.Changed().
// Pairs with saveCLIState, which covers the config globals.
func snapshotFlags(t *testing.T, cmd *cobra.Command) {
	t.Helper()
	type saved struct {
		val     string
		changed bool
	}
	orig := map[string]saved{}
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		orig[f.Name] = saved{f.Value.String(), f.Changed}
	})
	t.Cleanup(func() {
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			if s, ok := orig[f.Name]; ok {
				_ = f.Value.Set(s.val)
				f.Changed = s.changed
			}
		})
	})
}

// ── triage ──────────────────────────────────────────────────────────────────

// TestTriageCmdStructure pins the full triage subcommand set. create/update/
// delete closed the CLI↔API parity gap — the REST surface has full CRUD on
// /api/v1/triage-rules but the CLI shipped list+process only.
func TestTriageCmdStructure(t *testing.T) {
	t.Parallel()

	have := map[string]bool{}
	for _, sub := range triageCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"list", "process", "create", "update", "delete"} {
		if !have[want] {
			t.Errorf("triage missing subcommand %q; have %v", want, have)
		}
	}
}

func TestTriageCreate_Flags(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"name", "pattern", "match-type", "crew", "assignee", "priority", "project", "labels"} {
		if triageCreateCmd.Flags().Lookup(name) == nil {
			t.Errorf("triage create missing --%s flag", name)
		}
	}
}

func TestTriageCreateRunE_RequiresName(t *testing.T) {
	saveCLIState(t)
	snapshotFlags(t, triageCreateCmd)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "ws-1"}
	_ = triageCreateCmd.Flags().Set("name", "")
	_ = triageCreateCmd.Flags().Set("pattern", "bug")
	_ = triageCreateCmd.Flags().Set("match-type", "contains")

	err := triageCreateCmd.RunE(triageCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--name is required") {
		t.Errorf("expected --name required; got %v", err)
	}
}

func TestTriageCreateRunE_RequiresPattern(t *testing.T) {
	saveCLIState(t)
	snapshotFlags(t, triageCreateCmd)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "ws-1"}
	_ = triageCreateCmd.Flags().Set("name", "Bugs")
	_ = triageCreateCmd.Flags().Set("pattern", "")
	_ = triageCreateCmd.Flags().Set("match-type", "contains")

	err := triageCreateCmd.RunE(triageCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--pattern is required") {
		t.Errorf("expected --pattern required; got %v", err)
	}
}

func TestTriageCreateRunE_RejectsBadMatchType(t *testing.T) {
	saveCLIState(t)
	snapshotFlags(t, triageCreateCmd)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "ws-1"}
	_ = triageCreateCmd.Flags().Set("name", "Bugs")
	_ = triageCreateCmd.Flags().Set("pattern", "bug")
	_ = triageCreateCmd.Flags().Set("match-type", "fuzzy")

	err := triageCreateCmd.RunE(triageCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "match-type") {
		t.Errorf("expected match-type validation error; got %v", err)
	}
}

func TestTriageUpdate_Args(t *testing.T) {
	t.Parallel()
	if err := triageUpdateCmd.Args(triageUpdateCmd, []string{}); err == nil {
		t.Error("update with 0 args should fail Args")
	}
	if err := triageUpdateCmd.Args(triageUpdateCmd, []string{"rule-1"}); err != nil {
		t.Errorf("update with 1 arg should pass Args; got %v", err)
	}
}

func TestTriageUpdateRunE_NoFields(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "ws-1"}
	snapshotFlags(t, triageUpdateCmd)
	for _, f := range []string{"name", "pattern", "match-type", "crew", "assignee", "priority", "project", "labels", "position", "enabled"} {
		if lk := triageUpdateCmd.Flags().Lookup(f); lk != nil {
			lk.Changed = false
		}
	}
	err := triageUpdateCmd.RunE(triageUpdateCmd, []string{"rule-1"})
	if err == nil || !strings.Contains(err.Error(), "no fields to update") {
		t.Errorf("expected 'no fields to update'; got %v", err)
	}
}

func TestTriageDelete_Args(t *testing.T) {
	t.Parallel()
	if err := triageDeleteCmd.Args(triageDeleteCmd, []string{}); err == nil {
		t.Error("delete with 0 args should fail Args")
	}
	if err := triageDeleteCmd.Args(triageDeleteCmd, []string{"rule-1"}); err != nil {
		t.Errorf("delete with 1 arg should pass Args; got %v", err)
	}
}

// ── recurring ─────────────────────────────────────────────────────────────

func TestRecurringCmdStructure(t *testing.T) {
	t.Parallel()
	have := map[string]bool{}
	for _, sub := range recurringCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"list", "create", "update", "delete"} {
		if !have[want] {
			t.Errorf("recurring missing subcommand %q; have %v", want, have)
		}
	}
}

func TestRecurringCreate_Flags(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"crew", "title", "cron", "description", "priority", "project", "milestone", "assignee-type", "assignee", "labels"} {
		if recurringCreateCmd.Flags().Lookup(name) == nil {
			t.Errorf("recurring create missing --%s flag", name)
		}
	}
}

func TestRecurringCreateRunE_RequiresCrew(t *testing.T) {
	saveCLIState(t)
	snapshotFlags(t, recurringCreateCmd)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "ws-1"}
	_ = recurringCreateCmd.Flags().Set("crew", "")
	_ = recurringCreateCmd.Flags().Set("title", "Daily standup")
	_ = recurringCreateCmd.Flags().Set("cron", "0 9 * * *")

	err := recurringCreateCmd.RunE(recurringCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--crew is required") {
		t.Errorf("expected --crew required; got %v", err)
	}
}

func TestRecurringCreateRunE_RequiresTitle(t *testing.T) {
	saveCLIState(t)
	snapshotFlags(t, recurringCreateCmd)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "ws-1"}
	_ = recurringCreateCmd.Flags().Set("crew", "crew-1")
	_ = recurringCreateCmd.Flags().Set("title", "")
	_ = recurringCreateCmd.Flags().Set("cron", "0 9 * * *")

	err := recurringCreateCmd.RunE(recurringCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--title is required") {
		t.Errorf("expected --title required; got %v", err)
	}
}

func TestRecurringCreateRunE_RequiresCron(t *testing.T) {
	saveCLIState(t)
	snapshotFlags(t, recurringCreateCmd)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "ws-1"}
	_ = recurringCreateCmd.Flags().Set("crew", "crew-1")
	_ = recurringCreateCmd.Flags().Set("title", "Daily standup")
	_ = recurringCreateCmd.Flags().Set("cron", "")

	err := recurringCreateCmd.RunE(recurringCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--cron is required") {
		t.Errorf("expected --cron required; got %v", err)
	}
}

func TestRecurringUpdate_Args(t *testing.T) {
	t.Parallel()
	if err := recurringUpdateCmd.Args(recurringUpdateCmd, []string{}); err == nil {
		t.Error("update with 0 args should fail Args")
	}
	if err := recurringUpdateCmd.Args(recurringUpdateCmd, []string{"rec-1"}); err != nil {
		t.Errorf("update with 1 arg should pass Args; got %v", err)
	}
}

func TestRecurringUpdateRunE_NoFields(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "ws-1"}
	snapshotFlags(t, recurringUpdateCmd)
	for _, f := range []string{"crew", "title", "description", "priority", "project", "milestone", "assignee-type", "assignee", "labels", "cron", "enabled"} {
		if lk := recurringUpdateCmd.Flags().Lookup(f); lk != nil {
			lk.Changed = false
		}
	}
	err := recurringUpdateCmd.RunE(recurringUpdateCmd, []string{"rec-1"})
	if err == nil || !strings.Contains(err.Error(), "no fields to update") {
		t.Errorf("expected 'no fields to update'; got %v", err)
	}
}

// ── saved-view ────────────────────────────────────────────────────────────

func TestSavedViewCmdStructure(t *testing.T) {
	t.Parallel()
	have := map[string]bool{}
	for _, sub := range savedViewCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"list", "create", "update", "delete"} {
		if !have[want] {
			t.Errorf("saved-view missing subcommand %q; have %v", want, have)
		}
	}
}

func TestSavedViewCreate_Flags(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"name", "filters", "sort", "view-type", "shared"} {
		if savedViewCreateCmd.Flags().Lookup(name) == nil {
			t.Errorf("saved-view create missing --%s flag", name)
		}
	}
}

func TestSavedViewCreateRunE_RequiresName(t *testing.T) {
	saveCLIState(t)
	snapshotFlags(t, savedViewCreateCmd)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "ws-1"}
	_ = savedViewCreateCmd.Flags().Set("name", "")
	_ = savedViewCreateCmd.Flags().Set("filters", `{"status":"open"}`)

	err := savedViewCreateCmd.RunE(savedViewCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--name is required") {
		t.Errorf("expected --name required; got %v", err)
	}
}

func TestSavedViewCreateRunE_RequiresFilters(t *testing.T) {
	saveCLIState(t)
	snapshotFlags(t, savedViewCreateCmd)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "ws-1"}
	_ = savedViewCreateCmd.Flags().Set("name", "My open issues")
	_ = savedViewCreateCmd.Flags().Set("filters", "")

	err := savedViewCreateCmd.RunE(savedViewCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--filters is required") {
		t.Errorf("expected --filters required; got %v", err)
	}
}

func TestSavedViewCreateRunE_RejectsInvalidFilters(t *testing.T) {
	saveCLIState(t)
	snapshotFlags(t, savedViewCreateCmd)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "ws-1"}
	_ = savedViewCreateCmd.Flags().Set("name", "My open issues")
	_ = savedViewCreateCmd.Flags().Set("filters", "not-json")

	err := savedViewCreateCmd.RunE(savedViewCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--filters") {
		t.Errorf("expected invalid --filters error; got %v", err)
	}
}

func TestSavedViewUpdate_Args(t *testing.T) {
	t.Parallel()
	if err := savedViewUpdateCmd.Args(savedViewUpdateCmd, []string{}); err == nil {
		t.Error("update with 0 args should fail Args")
	}
	if err := savedViewUpdateCmd.Args(savedViewUpdateCmd, []string{"sv-1"}); err != nil {
		t.Errorf("update with 1 arg should pass Args; got %v", err)
	}
}

func TestSavedViewUpdateRunE_NoFields(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "ws-1"}
	snapshotFlags(t, savedViewUpdateCmd)
	for _, f := range []string{"name", "filters", "sort", "view-type", "default", "shared"} {
		if lk := savedViewUpdateCmd.Flags().Lookup(f); lk != nil {
			lk.Changed = false
		}
	}
	err := savedViewUpdateCmd.RunE(savedViewUpdateCmd, []string{"sv-1"})
	if err == nil || !strings.Contains(err.Error(), "no fields to update") {
		t.Errorf("expected 'no fields to update'; got %v", err)
	}
}
