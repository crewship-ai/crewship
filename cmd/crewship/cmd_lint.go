package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// lintCmd is a static-analysis pass over the user's local CLI footprint.
// Catches the boring class of mistakes that block AI workflows but
// don't surface as runtime errors:
//
//   - cli-config.yaml uses a removed/typo'd key
//   - prompt-library file has an invalid name (would fail at use-time)
//   - prompt-library file is empty (probably accidental)
//   - default-agent points to a slug we can verify locally without auth
//
// Goal: catch problems at "crewship lint" time, not at "crewship ask" time.
var lintCmd = &cobra.Command{
	Use:   "lint",
	Short: "Static-analyse local CLI configuration and prompt library",
	Long: `Validate ~/.crewship/cli-config.yaml and ~/.crewship/prompts/* for
common mistakes. Read-only — never modifies anything.

Exit code is non-zero when at least one error is found (warnings don't
fail the run), so this is safe to wire into shell init scripts.

Examples:
  crewship lint
  crewship lint --strict        # warnings also fail`,
	RunE: func(cmd *cobra.Command, args []string) error {
		strict, _ := cmd.Flags().GetBool("strict")

		errs, warns := 0, 0
		emit := func(severity, file, msg string) {
			color := cli.Yellow
			if severity == "error" {
				color = cli.Red
				errs++
			} else {
				warns++
			}
			fmt.Printf("  %s%-7s%s  %s — %s\n", color, severity, cli.Reset, file, msg)
		}

		fmt.Printf("%sLinting local CLI footprint%s\n", cli.Bold, cli.Reset)

		lintConfig(emit)
		lintPromptLibrary(emit)

		fmt.Printf("\n%sresult:%s %d error(s), %d warning(s)\n", cli.Bold, cli.Reset, errs, warns)
		if errs > 0 || (strict && warns > 0) {
			return fmt.Errorf("lint failed")
		}
		return nil
	},
}

// lintConfig validates the YAML config file shape and content.
func lintConfig(emit func(severity, file, msg string)) {
	path, err := cli.DefaultConfigPath()
	if err != nil {
		emit("error", "config", "could not resolve config path: "+err.Error())
		return
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		emit("warn", path, "config file does not exist (run `crewship login`)")
		return
	}
	if err != nil {
		emit("error", path, "read failed: "+err.Error())
		return
	}

	// Parse into a generic map to detect unknown keys — `cli.CLIConfig`
	// silently ignores fields that don't match its tags, so a typo like
	// "deafult_agent" passes unnoticed without this check.
	var generic map[string]any
	if err := yaml.Unmarshal(data, &generic); err != nil {
		emit("error", path, "invalid YAML: "+err.Error())
		return
	}
	knownKeys := map[string]bool{
		"server": true, "workspace": true, "token": true,
		"format": true, "default_agent": true, "markdown": true,
	}
	for k := range generic {
		if !knownKeys[k] {
			emit("warn", path, fmt.Sprintf("unknown config key %q (typo? supported: server, workspace, token, format, default_agent, markdown)", k))
		}
	}

	// Markdown value sanity.
	if v, ok := generic["markdown"].(string); ok {
		switch strings.ToLower(v) {
		case "auto", "on", "off", "true", "false", "1", "0", "yes", "no", "":
		default:
			emit("error", path, fmt.Sprintf("invalid markdown value %q (allowed: auto|on|off)", v))
		}
	}
}

// lintPromptLibrary checks every saved prompt for invalid names and
// empty bodies. Bypass-the-validator names (created via direct fs writes
// — `cp`, `mv`, an editor) get flagged so they don't blow up at use time.
func lintPromptLibrary(emit func(severity, file, msg string)) {
	dir, err := promptDir()
	if err != nil {
		emit("error", "prompts", "could not resolve prompts dir: "+err.Error())
		return
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		// No prompts yet — nothing to lint.
		return
	}
	if err != nil {
		emit("error", dir, "read failed: "+err.Error())
		return
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if e.IsDir() {
			emit("warn", path, "subdirectories are ignored — flatten layout for prompt CLI to find these")
			continue
		}
		if !strings.HasSuffix(e.Name(), ".md") {
			emit("warn", path, "ignored — prompt files must end in .md")
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		if err := validatePromptName(name); err != nil {
			emit("error", path, fmt.Sprintf("invalid prompt name (%v) — won't be reachable via `crewship prompt use`", err))
			continue
		}
		info, err := e.Info()
		if err != nil {
			emit("warn", path, "stat failed: "+err.Error())
			continue
		}
		if info.Size() == 0 {
			emit("warn", path, "prompt is empty (0 bytes) — accidental?")
		}
	}
}

func init() {
	lintCmd.Flags().Bool("strict", false, "Warnings also produce non-zero exit (suitable for CI)")
}
