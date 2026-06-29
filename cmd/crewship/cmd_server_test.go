package main

import (
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"gopkg.in/yaml.v3"
)

func readCfg(t *testing.T, path string) *cli.CLIConfig {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cfg: %v", err)
	}
	var c cli.CLIConfig
	if err := yaml.Unmarshal(data, &c); err != nil {
		t.Fatalf("unmarshal cfg: %v", err)
	}
	return &c
}

func TestServerCmdStructure(t *testing.T) {
	have := map[string]bool{}
	for _, sub := range serverCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"list", "use", "add", "remove", "current"} {
		if !have[want] {
			t.Errorf("server missing subcommand %q; have %v", want, have)
		}
	}
}

func TestServerAddPersistsAndSetsCurrent(t *testing.T) {
	path := redirectConfigHome(t)
	old := flagServer
	flagServer = "https://crewship-dev1.example"
	t.Cleanup(func() { flagServer = old })

	if err := serverAddCmd.RunE(serverAddCmd, []string{"dev1"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	cfg := readCfg(t, path)
	if cfg.Servers["dev1"] == nil || cfg.Servers["dev1"].Server != "https://crewship-dev1.example" {
		t.Fatalf("profile not saved: %+v", cfg.Servers)
	}
	if cfg.Current != "dev1" {
		t.Errorf("first added profile should become current, got %q", cfg.Current)
	}
}

func TestServerAddRequiresURL(t *testing.T) {
	redirectConfigHome(t)
	old := flagServer
	flagServer = ""
	t.Cleanup(func() { flagServer = old })
	if err := serverAddCmd.RunE(serverAddCmd, []string{"dev1"}); err == nil {
		t.Errorf("expected error when --server empty")
	}
}

func TestServerAddRejectsBadURL(t *testing.T) {
	redirectConfigHome(t)
	old := flagServer
	flagServer = "not-a-url"
	t.Cleanup(func() { flagServer = old })
	if err := serverAddCmd.RunE(serverAddCmd, []string{"dev1"}); err == nil {
		t.Errorf("expected error for malformed --server URL")
	}
}

func TestServerAddBindsDirectory(t *testing.T) {
	path := redirectConfigHome(t)
	oldServer, oldDir := flagServer, serverAddDir
	flagServer = "https://dev1.example"
	serverAddDir = "/work/crewship_1"
	t.Cleanup(func() { flagServer, serverAddDir = oldServer, oldDir })

	if err := serverAddCmd.RunE(serverAddCmd, []string{"dev1"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	cfg := readCfg(t, path)
	if cfg.DirectoryProfiles["/work/crewship_1"] != "dev1" {
		t.Errorf("--dir did not bind directory: %+v", cfg.DirectoryProfiles)
	}
}

func TestServerRemovePrunesDirBindings(t *testing.T) {
	path := redirectConfigHome(t)
	oldServer, oldDir := flagServer, serverAddDir
	flagServer = "https://dev1.example"
	serverAddDir = "/work/crewship_1"
	t.Cleanup(func() { flagServer, serverAddDir = oldServer, oldDir })

	if err := serverAddCmd.RunE(serverAddCmd, []string{"dev1"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := serverRemoveCmd.RunE(serverRemoveCmd, []string{"dev1"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	cfg := readCfg(t, path)
	if _, ok := cfg.DirectoryProfiles["/work/crewship_1"]; ok {
		t.Errorf("remove left a dangling directory binding: %+v", cfg.DirectoryProfiles)
	}
}

func TestServerUseRejectsUnknown(t *testing.T) {
	redirectConfigHome(t)
	if err := serverUseCmd.RunE(serverUseCmd, []string{"ghost"}); err == nil {
		t.Errorf("expected error for unknown profile")
	}
}

func TestServerUsePersists(t *testing.T) {
	path := redirectConfigHome(t)
	old := flagServer
	t.Cleanup(func() { flagServer = old })

	flagServer = "https://dev1.example"
	if err := serverAddCmd.RunE(serverAddCmd, []string{"dev1"}); err != nil {
		t.Fatal(err)
	}
	flagServer = "https://dev2.example"
	if err := serverAddCmd.RunE(serverAddCmd, []string{"dev2"}); err != nil {
		t.Fatal(err)
	}
	if err := serverUseCmd.RunE(serverUseCmd, []string{"dev2"}); err != nil {
		t.Fatalf("use: %v", err)
	}
	if cfg := readCfg(t, path); cfg.Current != "dev2" {
		t.Errorf("current = %q, want dev2", cfg.Current)
	}
}

func TestServerRemoveClearsCurrent(t *testing.T) {
	path := redirectConfigHome(t)
	old := flagServer
	t.Cleanup(func() { flagServer = old })

	flagServer = "https://dev1.example"
	if err := serverAddCmd.RunE(serverAddCmd, []string{"dev1"}); err != nil {
		t.Fatal(err)
	}
	if err := serverRemoveCmd.RunE(serverRemoveCmd, []string{"dev1"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	cfg := readCfg(t, path)
	if cfg.Servers["dev1"] != nil {
		t.Errorf("profile not removed: %+v", cfg.Servers)
	}
	if cfg.Current != "" {
		t.Errorf("current should clear when the active profile is removed, got %q", cfg.Current)
	}
}
