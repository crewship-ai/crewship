// ai-reconnect-client simulates native MCP client edits in a real subprocess.
// It deliberately uses only Go's standard library so the reconnect regression
// fixture does not require a shell or external file utilities.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 3 || os.Args[1] != "mcp" {
		return fmt.Errorf("expected mcp get, remove or add")
	}
	config := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), ".claude.json")
	if strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe") == "codex" {
		config = filepath.Join(os.Getenv("CODEX_HOME"), "config.toml")
	}
	mode := os.Getenv("FIXTURE_MODE")
	copyConfig := func(name string) error {
		data, err := os.ReadFile(filepath.Join(os.Getenv("FIXTURE_DIR"), name))
		if err != nil {
			return err
		}
		return os.WriteFile(config, data, 0600)
	}
	switch os.Args[2] {
	case "get":
		data, err := os.ReadFile(config)
		if err != nil || len(data) == 0 {
			return fmt.Errorf("No MCP server named 'crewship' found.")
		}
		_, err = os.Stdout.Write(data)
		return err
	case "remove":
		if mode == "remove-fails" {
			return fmt.Errorf("fixture remove failure")
		}
		return copyConfig("empty.json")
	case "add":
		switch mode {
		case "add-fails":
			return fmt.Errorf("fixture add failure")
		case "verify-fails":
			return copyConfig("empty.json")
		case "verify-error":
			return os.WriteFile(config, []byte("{broken\n"), 0600)
		}
		if err := copyConfig("desired.json"); err != nil {
			return err
		}
		raceTarget := ""
		if mode == "receipt-race" {
			raceTarget = os.Getenv("FIXTURE_RECEIPT")
		} else if mode == "race" {
			raceTarget = os.Getenv("FIXTURE_ORIGINAL")
		}
		if raceTarget != "" {
			return os.WriteFile(raceTarget, []byte("{\"custom\":\"concurrent-change\"}\n"), 0600)
		}
		return nil
	default:
		return fmt.Errorf("unexpected MCP action %q", os.Args[2])
	}
}
