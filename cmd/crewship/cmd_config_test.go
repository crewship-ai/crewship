package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"gopkg.in/yaml.v3"
)

func TestValueOrDefault(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		v, def string
		want   string
	}{
		{"empty_returns_default", "", "default-val", "default-val"},
		{"value_returns_value", "actual", "default-val", "actual"},
		{"whitespace_is_not_treated_as_empty", " ", "default-val", " "},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := valueOrDefault(tc.v, tc.def); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestConfigCmdStructure(t *testing.T) {
	t.Parallel()

	if configCmd.Use != "config" {
		t.Errorf("config Use: got %q", configCmd.Use)
	}

	have := map[string]bool{}
	for _, sub := range configCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"show", "set"} {
		if !have[want] {
			t.Errorf("config missing subcommand %q; have %v", want, have)
		}
	}
}

func TestConfigSetArgsValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"zero args", []string{}, true},
		{"one arg", []string{"server"}, true},
		{"two args", []string{"server", "http://x"}, false},
		{"three args", []string{"server", "http://x", "extra"}, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := configSetCmd.Args(configSetCmd, tc.args)
			if tc.wantErr && err == nil {
				t.Errorf("args=%v: expected Args error, got nil", tc.args)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("args=%v: expected no error, got %v", tc.args, err)
			}
		})
	}
}

// redirectConfigHome points DefaultConfigDir at an isolated temp dir by
// setting HOME (and USERPROFILE for Windows compat). Returns the config file
// path the CLI will read/write.
func redirectConfigHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	return filepath.Join(dir, ".crewship", "cli-config.yaml")
}

func TestConfigSetRunE_RejectsUnknownKey(t *testing.T) {
	redirectConfigHome(t)

	err := configSetCmd.RunE(configSetCmd, []string{"bogus", "value"})
	if err == nil || !strings.Contains(err.Error(), "unknown config key") {
		t.Errorf("expected 'unknown config key' error; got %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error should name the bogus key; got %v", err)
	}
}

func TestConfigSetRunE_RejectsInvalidFormat(t *testing.T) {
	redirectConfigHome(t)

	err := configSetCmd.RunE(configSetCmd, []string{"format", "xml"})
	if err == nil || !strings.Contains(err.Error(), "invalid format") {
		t.Errorf("expected 'invalid format' error; got %v", err)
	}
}

func TestConfigSetRunE_PersistsServer(t *testing.T) {
	path := redirectConfigHome(t)

	if err := configSetCmd.RunE(configSetCmd, []string{"server", "http://example:8080"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg cli.CLIConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Server != "http://example:8080" {
		t.Errorf("Server: got %q", cfg.Server)
	}
	// File permissions must be 0600 — config holds the token.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("config file perms: got %v want 0600", info.Mode().Perm())
	}
}

// TestConfigSetRunE_PersistsFormat exercises all 4 accepted format values
// and verifies they round-trip through save/load.
func TestConfigSetRunE_PersistsFormat(t *testing.T) {
	for _, format := range []string{"table", "json", "yaml", "quiet"} {
		format := format
		t.Run(format, func(t *testing.T) {
			redirectConfigHome(t)

			if err := configSetCmd.RunE(configSetCmd, []string{"format", format}); err != nil {
				t.Fatalf("RunE: %v", err)
			}
			cfg, err := cli.LoadConfig()
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if cfg.Format != format {
				t.Errorf("Format: got %q want %q", cfg.Format, format)
			}
		})
	}
}

// TestConfigSetRunE_PreservesOtherFields sets one key and verifies other
// pre-existing fields survive. Regression guard against a code path that
// accidentally resets the entire config.
func TestConfigSetRunE_PreservesOtherFields(t *testing.T) {
	path := redirectConfigHome(t)

	// Seed the config with a token and workspace; set should only touch server.
	seed := cli.CLIConfig{Token: "existing-token", Workspace: "existing-ws"}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, _ := yaml.Marshal(&seed)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	if err := configSetCmd.RunE(configSetCmd, []string{"server", "http://new"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	cfg, err := cli.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Server != "http://new" {
		t.Errorf("Server: got %q", cfg.Server)
	}
	if cfg.Token != "existing-token" {
		t.Errorf("Token was clobbered: got %q", cfg.Token)
	}
	if cfg.Workspace != "existing-ws" {
		t.Errorf("Workspace was clobbered: got %q", cfg.Workspace)
	}
}

func TestConfigShowRunE_NoCrashWithMissingFile(t *testing.T) {
	redirectConfigHome(t) // HOME points at empty temp dir — no config file exists yet

	// LoadConfig returns an empty CLIConfig when the file doesn't exist, so
	// show must render happily without erroring out.
	if err := configShowCmd.RunE(configShowCmd, nil); err != nil {
		t.Errorf("configShow RunE with missing config: %v", err)
	}
}
