package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// setupSlashHome points $HOME at a temp dir and returns the slash
// commands directory path (~/.crewship/commands) without creating it.
func setupSlashHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return filepath.Join(home, ".crewship", "commands")
}

// removeRootCommandByName detaches a dynamically registered slash
// command from rootCmd so the global command tree stays pristine for
// other tests.
func removeRootCommandByName(t *testing.T, name string) {
	t.Helper()
	for _, c := range rootCmd.Commands() {
		if c.Name() == name {
			rootCmd.RemoveCommand(c)
			return
		}
	}
}

func TestRegisterSlashCommands_MountsAndSkipsShadows(t *testing.T) {
	dir := setupSlashHome(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// One valid command with a unique name…
	valid := "---\ndescription: cov test command\n---\nDo the thing with $args\n"
	if err := os.WriteFile(filepath.Join(dir, "zz-covtest.md"), []byte(valid), 0o644); err != nil {
		t.Fatal(err)
	}
	// …and one that shadows the built-in `version` command.
	shadow := "---\ndescription: evil shadow\n---\nshadow body\n"
	if err := os.WriteFile(filepath.Join(dir, "version.md"), []byte(shadow), 0o644); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	t.Cleanup(func() {
		rootCmd.SetErr(nil)
		removeRootCommandByName(t, "zz-covtest")
	})

	registerSlashCommands()

	var mounted, versionCount int
	for _, c := range rootCmd.Commands() {
		switch c.Name() {
		case "zz-covtest":
			mounted++
		case "version":
			versionCount++
		}
	}
	if mounted != 1 {
		t.Errorf("zz-covtest mounted %d times, want 1", mounted)
	}
	if versionCount != 1 {
		t.Errorf("version registered %d times, want exactly the built-in", versionCount)
	}
	if !strings.Contains(stderr.String(), "shadows built-in command") {
		t.Errorf("stderr should warn about the shadow; got %q", stderr.String())
	}
}

func TestRegisterSlashCommands_LoadErrorDegradesToWarning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Make ~/.crewship/commands a regular FILE so os.ReadDir fails with a
	// non-NotExist error → LoadSlashCommands returns an error.
	if err := os.MkdirAll(filepath.Join(home, ".crewship"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".crewship", "commands"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	before := len(rootCmd.Commands())
	registerSlashCommands() // must not panic, must not mount anything
	if after := len(rootCmd.Commands()); after != before {
		t.Errorf("command count changed %d → %d; load failure must not mount commands", before, after)
	}
}

func TestMakeSlashCobra_Structure(t *testing.T) {
	t.Parallel()

	sc := cli.SlashCommand{
		Name:   "review",
		Source: "/tmp/review.md",
		Vars:   []string{"target"},
		Body:   "Review $target",
	}
	c := makeSlashCobra(sc)
	if c.Use != "review [args...]" {
		t.Errorf("Use: got %q", c.Use)
	}
	// Empty description falls back to the source path.
	if c.Short != "User-defined command from /tmp/review.md" {
		t.Errorf("Short: got %q", c.Short)
	}
	if !strings.Contains(c.Long, "Loaded from: /tmp/review.md") {
		t.Errorf("Long: got %q", c.Long)
	}
	if !strings.Contains(c.Long, "target") {
		t.Errorf("Long should list template vars; got %q", c.Long)
	}

	withDesc := makeSlashCobra(cli.SlashCommand{Name: "x", Description: "my desc", Body: "b"})
	if withDesc.Short != "my desc" {
		t.Errorf("Short with description: got %q", withDesc.Short)
	}
}

func TestMakeSlashCobra_RunE_EmptyBody(t *testing.T) {
	saveCLIState(t)

	c := makeSlashCobra(cli.SlashCommand{Name: "empty", Body: "   "})
	err := c.RunE(c, nil)
	if err == nil || !strings.Contains(err.Error(), "rendered empty body") {
		t.Errorf("got %v; want rendered-empty-body error", err)
	}
}

func TestMakeSlashCobra_RunE_InvalidEffort(t *testing.T) {
	saveCLIState(t)
	t.Cleanup(ResetAIFirstLatches)

	c := makeSlashCobra(cli.SlashCommand{Name: "e", Body: "do it", Effort: "bogus"})
	err := c.RunE(c, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid --effort") {
		t.Errorf("got %v; want invalid --effort error", err)
	}
}

func TestMakeSlashCobra_RunE_DispatchesToAsk(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{} // not logged in → ask fails fast after dispatch
	t.Cleanup(func() {
		ResetAIFirstLatches()
		_ = askCmd.Flags().Set("agent", "")
		_ = askCmd.Flags().Set("prompt", "")
	})

	sc := cli.SlashCommand{
		Name:   "summarize",
		Agent:  "viktor",
		Effort: "high",
		Plan:   true,
		Body:   "Summarize $args carefully",
	}
	c := makeSlashCobra(sc)
	err := c.RunE(c, []string{"the", "diff"})
	// Reaching ask's auth gate proves the dispatch happened.
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("got %v; want ask's not-logged-in error", err)
	}

	if got, _ := askCmd.Flags().GetString("agent"); got != "viktor" {
		t.Errorf("ask --agent: got %q want viktor", got)
	}
	prompt, _ := askCmd.Flags().GetString("prompt")
	if !strings.Contains(prompt, "Summarize the diff carefully") {
		t.Errorf("ask --prompt: got %q; want rendered body", prompt)
	}
	// Plan mode prepends the plan-mode prefix to the body.
	if !strings.Contains(prompt, "[plan-mode]") {
		t.Errorf("ask --prompt should carry the plan-mode prefix; got %q", prompt)
	}
}

func TestMakeSlashCobra_RunE_NilAskRunE(t *testing.T) {
	saveCLIState(t)
	orig := askCmd.RunE
	askCmd.RunE = nil
	t.Cleanup(func() {
		askCmd.RunE = orig
		ResetAIFirstLatches()
		_ = askCmd.Flags().Set("prompt", "")
	})

	c := makeSlashCobra(cli.SlashCommand{Name: "n", Body: "hello"})
	err := c.RunE(c, nil)
	if err == nil || !strings.Contains(err.Error(), "ask command has no RunE") {
		t.Errorf("got %v; want internal no-RunE error", err)
	}
}
