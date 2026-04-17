package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestValidateHexColor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		color  string
		wantOK bool
	}{
		// Valid forms.
		{"short_lower", "#abc", true},
		{"short_upper", "#ABC", true},
		{"long_lower", "#aabbcc", true},
		{"long_upper", "#3B82F6", true},
		{"long_mixed", "#3b82F6", true},
		// Invalid forms.
		{"no_hash", "abc", false},
		{"four_digits", "#abcd", false},
		{"five_digits", "#abcde", false},
		{"too_long", "#abcdef1", false},
		{"non_hex", "#xyzxyz", false},
		{"empty", "", false},
		{"hash_only", "#", false},
		{"leading_space", " #abc", false},
		{"trailing_space", "#abc ", false},
		// rgb()-style rejected.
		{"rgb_func", "rgb(1,2,3)", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateHexColor(tc.color)
			if tc.wantOK && err != nil {
				t.Errorf("%q: expected valid, got %v", tc.color, err)
			}
			if !tc.wantOK && err == nil {
				t.Errorf("%q: expected error, got nil", tc.color)
			}
			if !tc.wantOK && err != nil {
				// Error message should quote the bad value for clarity.
				if !strings.Contains(err.Error(), tc.color) && tc.color != "" {
					t.Errorf("error should name the bad color; got %v", err)
				}
			}
		})
	}
}

func TestLabelCmdStructure(t *testing.T) {
	t.Parallel()

	if labelCmd.Use != "label" {
		t.Errorf("label Use: got %q", labelCmd.Use)
	}
	// `labels` alias — plural form is common muscle memory.
	found := false
	for _, a := range labelCmd.Aliases {
		if a == "labels" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("label should alias 'labels'; got %v", labelCmd.Aliases)
	}

	have := map[string]bool{}
	for _, sub := range labelCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"list", "create", "update", "delete"} {
		if !have[want] {
			t.Errorf("label missing subcommand %q; have %v", want, have)
		}
	}
}

func TestLabelDeleteAliases(t *testing.T) {
	t.Parallel()

	want := map[string]bool{"remove": false, "rm": false}
	for _, a := range labelDeleteCmd.Aliases {
		if _, ok := want[a]; ok {
			want[a] = true
		}
	}
	for alias, seen := range want {
		if !seen {
			t.Errorf("label delete missing alias %q; got %v", alias, labelDeleteCmd.Aliases)
		}
	}
}

func TestLabelListRunE_AuthPaths(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	err := labelListCmd.RunE(labelListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected 'not logged in'; got %v", err)
	}

	cliCfg = &cli.CLIConfig{Token: "fake-token"}
	flagWorkspace = ""
	t.Setenv("CREWSHIP_WORKSPACE", "")
	err = labelListCmd.RunE(labelListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error; got %v", err)
	}
}

func TestLabelCreateRunE_RequiresName(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "ws-1"}
	_ = labelCreateCmd.Flags().Set("name", "")
	_ = labelCreateCmd.Flags().Set("color", "#3B82F6")

	err := labelCreateCmd.RunE(labelCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--name is required") {
		t.Errorf("expected --name required; got %v", err)
	}
}

func TestLabelCreateRunE_RequiresColor(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "ws-1"}
	_ = labelCreateCmd.Flags().Set("name", "Bug")
	_ = labelCreateCmd.Flags().Set("color", "")

	err := labelCreateCmd.RunE(labelCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--color is required") {
		t.Errorf("expected --color required; got %v", err)
	}
}

func TestLabelCreateRunE_RejectsInvalidColor(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "ws-1"}
	_ = labelCreateCmd.Flags().Set("name", "Bug")
	_ = labelCreateCmd.Flags().Set("color", "not-a-color")
	t.Cleanup(func() {
		_ = labelCreateCmd.Flags().Set("name", "")
		_ = labelCreateCmd.Flags().Set("color", "")
	})

	err := labelCreateCmd.RunE(labelCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid --color") {
		t.Errorf("expected invalid color error; got %v", err)
	}
}

func TestLabelUpdateArgsValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"zero args", []string{}, true},
		{"one arg", []string{"label-123"}, false},
		{"two args", []string{"a", "b"}, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := labelUpdateCmd.Args(labelUpdateCmd, tc.args)
			if tc.wantErr && err == nil {
				t.Errorf("args=%v: expected error", tc.args)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("args=%v: expected no error, got %v", tc.args, err)
			}
		})
	}
}

func TestLabelUpdateRunE_NoFieldsChanged(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "ws-1"}
	// Explicitly reset each flag to make .Changed() false.
	for _, f := range []string{"name", "color", "group"} {
		// Re-defining the flags via Set doesn't clear Changed; we need to
		// re-register default. Cobra has no "reset changed" API, so we
		// reach into the underlying pflag and zero the Changed bit.
		labelUpdateCmd.Flags().Lookup(f).Changed = false
	}

	err := labelUpdateCmd.RunE(labelUpdateCmd, []string{"label-123"})
	if err == nil || !strings.Contains(err.Error(), "no fields to update") {
		t.Errorf("expected 'no fields to update'; got %v", err)
	}
}

func TestLabelUpdateRunE_RejectsInvalidColor(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "ws-1"}
	// Mark --color as changed and set an invalid value.
	if err := labelUpdateCmd.Flags().Set("color", "not-hex"); err != nil {
		t.Fatalf("set --color: %v", err)
	}
	t.Cleanup(func() {
		labelUpdateCmd.Flags().Lookup("color").Changed = false
	})

	err := labelUpdateCmd.RunE(labelUpdateCmd, []string{"label-123"})
	if err == nil || !strings.Contains(err.Error(), "invalid --color") {
		t.Errorf("expected invalid color error; got %v", err)
	}
}

func TestLabelDeleteArgsValidation(t *testing.T) {
	t.Parallel()

	if err := labelDeleteCmd.Args(labelDeleteCmd, []string{}); err == nil {
		t.Error("delete with 0 args should fail Args")
	}
	if err := labelDeleteCmd.Args(labelDeleteCmd, []string{"x"}); err != nil {
		t.Errorf("delete with 1 arg should pass Args; got %v", err)
	}
	if err := labelDeleteCmd.Args(labelDeleteCmd, []string{"a", "b"}); err == nil {
		t.Error("delete with 2 args should fail Args")
	}
}
