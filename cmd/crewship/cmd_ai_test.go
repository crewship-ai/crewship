package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

func executeAITest(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newAICommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestAIEmbeddedSkillInstallation(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "crewship")
	out, err := executeAITest(t, "skill")
	if err != nil || out != crewshipAISkill {
		t.Fatalf("%q %v", out, err)
	}
	if _, err = executeAITest(t, "skill", "--directory", directory); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "SKILL.md")
	data, err := os.ReadFile(target)
	if err != nil || string(data) != crewshipAISkill {
		t.Fatalf("%v %v", data, err)
	}
	if err = installAISkill(directory); err != nil {
		t.Fatalf("idempotent installation: %v", err)
	}
	if err = os.WriteFile(target, []byte("user content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = installAISkill(directory); err == nil {
		t.Fatal("overwrote custom skill")
	}
	data, _ = os.ReadFile(target)
	if string(data) != "user content" {
		t.Fatal("custom content lost")
	}
	if err = installAISkill(""); err == nil {
		t.Fatal("accepted empty directory")
	}
	other := t.TempDir()
	if err = os.Symlink(target, filepath.Join(other, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if err = installAISkill(other); err == nil {
		t.Fatal("followed target symlink")
	}
}

func TestAIConfigAndConnectionDryRun(t *testing.T) {
	saveCLIState(t)
	flagProfile = "test"
	flagServer = ""
	flagWorkspace = ""
	cliCfg = &cli.CLIConfig{Server: "https://crewship.example", Token: "DO-NOT-EXPORT", Workspace: "team", Servers: map[string]*cli.ServerProfile{"test": {Server: "https://crewship.example", Token: "DO-NOT-EXPORT", Workspace: "team"}}}
	for _, client := range []string{"generic", "claude", "cursor", "gemini", "vscode", "codex"} {
		out, err := executeAITest(t, "config", client, "--allow-write")
		if err != nil {
			t.Fatalf("%s: %v", client, err)
		}
		if strings.Contains(out, "DO-NOT-EXPORT") || !strings.Contains(out, "--profile") || !strings.Contains(out, "--workspace") || !strings.Contains(out, "--allow-write") {
			t.Fatal(out)
		}
		if client == "codex" {
			if !strings.HasPrefix(out, "[mcp_servers.crewship]") {
				t.Fatal(out)
			}
			continue
		}
		var config map[string]map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}
		if err = json.Unmarshal([]byte(out), &config); err != nil {
			t.Fatal(err)
		}
		key := "mcpServers"
		if client == "vscode" {
			key = "servers"
		}
		if !filepath.IsAbs(config[key]["crewship"].Command) {
			t.Fatal(out)
		}
	}
	out, err := executeAITest(t, "connect", "claude", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"--scope"`) || !strings.Contains(out, `"user"`) || strings.Contains(out, "DO-NOT-EXPORT") {
		t.Fatal(out)
	}
	if _, err = executeAITest(t, "config", "unknown"); err == nil {
		t.Fatal("unknown client accepted")
	}
	flagProfile = "missing"
	if _, err = executeAITest(t, "config"); err == nil {
		t.Fatal("unknown profile silently accepted")
	}
}

func TestAIRegistrationArgumentsAreNotShell(t *testing.T) {
	executable := "/a directory/crewship $(false)"
	serverArgs := []string{"mcp", "serve", "--workspace", "team with spaces"}
	for _, client := range []string{"codex", "claude"} {
		args, err := aiRegistrationArgs(client, executable, serverArgs)
		if err != nil {
			t.Fatal(err)
		}
		idx := -1
		for i, arg := range args {
			if arg == "--" {
				idx = i
				break
			}
		}
		if idx < 0 || args[idx+1] != executable || !reflect.DeepEqual(args[idx+2:], serverArgs) {
			t.Fatal(args)
		}
	}
	if _, err := aiRegistrationArgs("sh", executable, serverArgs); err == nil {
		t.Fatal("arbitrary program accepted")
	}
}

func TestAIConfigEscapesPaths(t *testing.T) {
	executable := "C:\\Program Files\\Crewship\\crewship.exe"
	out, err := aiClientConfig("codex", executable, []string{"mcp", "serve", "a\"b\nc"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `C:\\Program Files\\Crewship\\crewship.exe`) || !strings.Contains(out, `a\"b\nc`) {
		t.Fatal(out)
	}
}

func TestAIInvalidProfileCannotBeMaskedByEnvironmentToken(t *testing.T) {
	saveCLIState(t)
	t.Setenv("CREWSHIP_TOKEN", "test-environment-token")
	cliCfg = nil
	flagProfile = "missing-profile"
	for _, cmd := range []*cobra.Command{newMCPServeCommand(), newAICommand()} {
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if cmd.Name() == "ai" {
			cmd.SetArgs([]string{"config"})
		} else {
			cmd.SetArgs([]string{})
		}
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "profile is not configured") {
			t.Fatalf("%s: %v", cmd.Name(), err)
		}
	}
}
