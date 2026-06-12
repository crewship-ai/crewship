package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// covLintEnv isolates the lint command's filesystem inputs: a temp
// config file (via CREWSHIP_CONFIG) and a temp HOME (for promptDir).
// Returns the prompts dir path (not created — tests opt in).
func covLintEnv(t *testing.T, configYAML string) string {
	t.Helper()
	covSaveState(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	t.Setenv("CREWSHIP_CONFIG", cfgPath)
	if configYAML != "" {
		if err := os.WriteFile(cfgPath, []byte(configYAML), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}
	return filepath.Join(home, ".crewship", "prompts")
}

func TestLintRunE_MissingConfigWarnsOnly(t *testing.T) {
	covLintEnv(t, "") // config file absent

	out := covCaptureStdoutCli9(t, func() {
		if err := lintCmd.RunE(lintCmd, nil); err != nil {
			t.Errorf("warnings alone must not fail: %v", err)
		}
	})
	if !strings.Contains(out, "config file does not exist") {
		t.Errorf("missing-config warning absent:\n%s", out)
	}
	if !strings.Contains(out, "0 error(s), 1 warning(s)") {
		t.Errorf("tally wrong:\n%s", out)
	}
}

func TestLintRunE_StrictPromotesWarnings(t *testing.T) {
	covLintEnv(t, "")
	covSetFlagCli9(t, lintCmd, "strict", "true")

	var err error
	_ = covCaptureStdoutCli9(t, func() { err = lintCmd.RunE(lintCmd, nil) })
	if err == nil || !strings.Contains(err.Error(), "lint failed") {
		t.Errorf("--strict should fail on warnings; got %v", err)
	}
}

func TestLintRunE_InvalidYAMLFails(t *testing.T) {
	covLintEnv(t, "server: [unclosed")

	var err error
	out := covCaptureStdoutCli9(t, func() { err = lintCmd.RunE(lintCmd, nil) })
	if err == nil || !strings.Contains(err.Error(), "lint failed") {
		t.Errorf("invalid YAML should fail lint; got %v", err)
	}
	if !strings.Contains(out, "invalid YAML") {
		t.Errorf("missing invalid-YAML line:\n%s", out)
	}
}

func TestLintRunE_UnknownKeyAndBadMarkdown(t *testing.T) {
	covLintEnv(t, "server: http://x\ndeafult_agent: viktor\nmarkdown: sometimes\n")

	var err error
	out := covCaptureStdoutCli9(t, func() { err = lintCmd.RunE(lintCmd, nil) })
	if err == nil {
		t.Error("invalid markdown value is an error → lint must fail")
	}
	if !strings.Contains(out, `unknown config key "deafult_agent"`) {
		t.Errorf("typo'd key not flagged:\n%s", out)
	}
	if !strings.Contains(out, `invalid markdown value "sometimes"`) {
		t.Errorf("bad markdown value not flagged:\n%s", out)
	}
}

func TestLintRunE_CleanConfigPasses(t *testing.T) {
	covLintEnv(t, "server: http://x\nworkspace: ws\nformat: table\nmarkdown: auto\n")

	out := covCaptureStdoutCli9(t, func() {
		if err := lintCmd.RunE(lintCmd, nil); err != nil {
			t.Errorf("clean config should pass: %v", err)
		}
	})
	if !strings.Contains(out, "0 error(s), 0 warning(s)") {
		t.Errorf("clean run tally wrong:\n%s", out)
	}
}

func TestLintRunE_UnresolvableHomeFailsBothPasses(t *testing.T) {
	covSaveState(t)
	// No CREWSHIP_CONFIG and no HOME → both DefaultConfigPath and
	// promptDir fail → two "could not resolve" errors.
	t.Setenv("CREWSHIP_CONFIG", "")
	t.Setenv("HOME", "")

	var err error
	out := covCaptureStdoutCli9(t, func() { err = lintCmd.RunE(lintCmd, nil) })
	if err == nil || !strings.Contains(err.Error(), "lint failed") {
		t.Errorf("expected lint failure; got %v", err)
	}
	if !strings.Contains(out, "could not resolve config path") {
		t.Errorf("config-path error missing:\n%s", out)
	}
	if !strings.Contains(out, "could not resolve prompts dir") {
		t.Errorf("prompts-dir error missing:\n%s", out)
	}
}

func TestLintRunE_ConfigReadFailure(t *testing.T) {
	covSaveState(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Point CREWSHIP_CONFIG at a directory: os.ReadFile fails with a
	// non-NotExist error → the "read failed" branch.
	t.Setenv("CREWSHIP_CONFIG", t.TempDir())

	var err error
	out := covCaptureStdoutCli9(t, func() { err = lintCmd.RunE(lintCmd, nil) })
	if err == nil {
		t.Error("unreadable config is an error → lint must fail")
	}
	if !strings.Contains(out, "read failed") {
		t.Errorf("read-failure line missing:\n%s", out)
	}
}

func TestLintRunE_PromptsDirReadFailure(t *testing.T) {
	promptsDir := covLintEnv(t, "server: http://x\n")
	// Create promptsDir's path as a FILE so os.ReadDir errors with
	// ENOTDIR (not IsNotExist) → the prompts "read failed" branch.
	if err := os.MkdirAll(filepath.Dir(promptsDir), 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	if err := os.WriteFile(promptsDir, []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}

	var err error
	out := covCaptureStdoutCli9(t, func() { err = lintCmd.RunE(lintCmd, nil) })
	if err == nil {
		t.Error("unreadable prompts dir is an error → lint must fail")
	}
	if !strings.Contains(out, "read failed") {
		t.Errorf("prompts read-failure line missing:\n%s", out)
	}
}

func TestLintRunE_PromptLibraryFindings(t *testing.T) {
	promptsDir := covLintEnv(t, "server: http://x\n")
	if err := os.MkdirAll(filepath.Join(promptsDir, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writes := map[string]string{
		"notes.txt":    "not a prompt",
		"bad name!.md": "body",
		"empty.md":     "",
		"good.md":      "review this code",
	}
	for name, body := range writes {
		if err := os.WriteFile(filepath.Join(promptsDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	var err error
	out := covCaptureStdoutCli9(t, func() { err = lintCmd.RunE(lintCmd, nil) })
	// "bad name!" is an invalid prompt name → error → lint fails.
	if err == nil || !strings.Contains(err.Error(), "lint failed") {
		t.Errorf("invalid prompt name should fail lint; got %v", err)
	}
	for _, want := range []string{
		"subdirectories are ignored",
		"prompt files must end in .md",
		"invalid prompt name",
		"prompt is empty (0 bytes)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt finding missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "good.md") {
		t.Errorf("valid prompt should not be flagged:\n%s", out)
	}
}
