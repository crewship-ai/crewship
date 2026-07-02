package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// `crewship commands` is the agent-facing capability manifest: the full
// cobra tree (names, flags, arg shapes) as JSON, so an agent discovers
// what the CLI can do without scraping --help page by page.

func TestCommandsCmd_JSONManifestParsesAndCoversTree(t *testing.T) {
	saveCLIState(t)
	cliCfg = nil
	flagFormat = "json"
	commandsCmd.SetContext(context.Background())

	out, err := captureStdoutCovCli10(t, func() error {
		return commandsCmd.RunE(commandsCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}

	var manifest struct {
		Version  string `json:"version"`
		Commands []struct {
			Path     string `json:"path"`
			Short    string `json:"short"`
			Flags    []any  `json:"flags"`
			Commands []struct {
				Path string `json:"path"`
			} `json:"commands"`
		} `json:"commands"`
		GlobalFlags []struct {
			Name string `json:"name"`
		} `json:"global_flags"`
	}
	if jerr := json.Unmarshal([]byte(out), &manifest); jerr != nil {
		t.Fatalf("manifest does not parse: %v", jerr)
	}
	if len(manifest.Commands) < 30 {
		t.Errorf("manifest has %d top-level commands, expected the full tree (>=30)", len(manifest.Commands))
	}

	var sawRoutineRun, sawWait bool
	for _, c := range manifest.Commands {
		if c.Path == "wait" {
			sawWait = true
		}
		if c.Path == "routine" {
			for _, sub := range c.Commands {
				if sub.Path == "routine run" {
					sawRoutineRun = true
				}
			}
		}
	}
	if !sawWait || !sawRoutineRun {
		t.Errorf("manifest missing expected entries: wait=%t, routine run=%t", sawWait, sawRoutineRun)
	}

	var sawFormatFlag bool
	for _, gf := range manifest.GlobalFlags {
		if gf.Name == "format" {
			sawFormatFlag = true
		}
	}
	if !sawFormatFlag {
		t.Error("global_flags missing --format")
	}
}

func TestCommandsCmd_TableModeIsHumanTree(t *testing.T) {
	saveCLIState(t)
	cliCfg = nil
	flagFormat = ""
	commandsCmd.SetContext(context.Background())

	out, err := captureStdoutCovCli10(t, func() error {
		return commandsCmd.RunE(commandsCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "routine") || !strings.Contains(out, "wait") {
		t.Errorf("tree output missing commands: %q", out[:min(len(out), 400)])
	}
}
