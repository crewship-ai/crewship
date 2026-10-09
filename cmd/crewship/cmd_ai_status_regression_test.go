package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAIStatusCommandRegistrationDiagnostics(t *testing.T) {
	binary := buildCrewshipBinary(t)
	fixture := buildAIClientFixture(t)
	for _, client := range []string{"claude", "codex"} {
		for _, state := range []string{"owned", "modified", "missing-executable"} {
			t.Run(client+"/"+state, func(t *testing.T) {
				dir := t.TempDir()
				t.Setenv("CLAUDE_CONFIG_DIR", dir)
				t.Setenv("CODEX_HOME", dir)
				t.Setenv("CREWSHIP_CONFIG", filepath.Join(dir, "cli.yaml"))
				clientName := client
				if runtime.GOOS == "windows" {
					clientName += ".exe"
				}
				if err := os.WriteFile(filepath.Join(dir, clientName), fixture, 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", dir)
				command := filepath.Join(dir, "crewship-executable")
				if err := os.WriteFile(command, fixture, 0700); err != nil {
					t.Fatal(err)
				}
				entry := aiEntry{Type: "stdio", Command: command, Args: []string{"mcp", "serve", "--fixture-secret=never-print-argument"}}
				config, err := aiClientContext(client)
				if err != nil {
					t.Fatal(err)
				}
				save := func() {
					t.Helper()
					var value any = map[string]any{"mcpServers": map[string]any{"crewship": entry}, "other_secret": "never-print-setting"}
					if client == "codex" {
						value = map[string]any{"transport": entry, "other_secret": "never-print-setting"}
					}
					data, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(config, data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				save()
				actual, found, err := inspectAIEntry(context.Background(), client)
				if err != nil || !found {
					t.Fatalf("registration fixture: %v %v", found, err)
				}
				if err := saveAIReceipt(client, actual); err != nil {
					t.Fatal(err)
				}
				if state == "modified" {
					entry.Env = map[string]string{"SECRET": "never-print-environment"}
					save()
				}
				if state == "missing-executable" {
					if err := os.Remove(command); err != nil {
						t.Fatal(err)
					}
				}
				child := exec.Command(binary, "ai", "status", client, "--format", "json")
				child.Env = os.Environ()
				output, err := child.CombinedOutput()
				if err != nil {
					t.Fatalf("ai status: %v\n%s", err, output)
				}
				for _, secret := range []string{"never-print-argument", "never-print-setting", "never-print-environment"} {
					if strings.Contains(string(output), secret) {
						t.Fatalf("status leaked %s", secret)
					}
				}
				var reports []struct {
					Client     string `json:"client"`
					Registered bool   `json:"registered"`
					Owned      bool   `json:"owned"`
					Exists     bool   `json:"executable_exists"`
					Problem    string `json:"problem"`
				}
				if err := json.Unmarshal(output, &reports); err != nil {
					t.Fatalf("status output is not JSON: %v\n%s", err, output)
				}
				if len(reports) != 1 {
					t.Fatalf("reports: %s", output)
				}
				report := reports[0]
				if report.Client != client || !report.Registered || report.Owned != (state != "modified") || report.Exists != (state != "missing-executable") {
					t.Fatalf("incorrect diagnostics: %s", output)
				}
				switch state {
				case "owned":
					if report.Problem != "" {
						t.Fatalf("unexpected problem: %s", output)
					}
				case "modified":
					if !strings.Contains(report.Problem, "unmanaged or changed") {
						t.Fatalf("missing ownership diagnostic: %s", output)
					}
				case "missing-executable":
					if !strings.Contains(report.Problem, "missing or not executable") {
						t.Fatalf("missing executable diagnostic: %s", output)
					}
				}
			})
		}
	}
}
