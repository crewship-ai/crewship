package main

import (
	"os"
	"strings"
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

// #1210: a directory_profiles cwd match silently outranks the persisted
// `server use` default with zero indication why. `server current` must
// name the layer that actually won, and — when it's a directory override —
// say what the persisted default would otherwise have been.
func TestServerCurrentSurfacesDirectoryOverride(t *testing.T) {
	path := redirectConfigHome(t)
	oldProfile := flagProfile
	flagProfile = ""
	t.Cleanup(func() { flagProfile = oldProfile })
	// This dev machine's real shell exports CREWSHIP_PROFILE (per-clone
	// dev-slot routing, see CLAUDE.md) — clear it so the test exercises
	// the directory-vs-persisted precedence in isolation.
	t.Setenv("CREWSHIP_PROFILE", "")

	cfg, err := cli.LoadConfig()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cfg.Current = "dev3-test"
	cfg.Servers = map[string]*cli.ServerProfile{
		"dev3":      {Server: "https://dev3.example", Token: "t"},
		"dev3-test": {Server: "https://dev3-test.example", Token: "t2"},
	}
	cfg.DirectoryProfiles = map[string]string{"/work/crewship_3": "dev3"}
	if err := cli.SaveConfig(cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	_ = path

	cli.SetWorkingDir("/work/crewship_3/sub")
	t.Cleanup(func() { cli.SetWorkingDir("") })

	out, err := captureStdout(t, func() error {
		return serverCurrentCmd.RunE(serverCurrentCmd, nil)
	})
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	if !strings.Contains(out, "dev3") {
		t.Errorf("output should show the winning (directory-mapped) profile dev3: %q", out)
	}
	if !strings.Contains(out, "directory override") {
		t.Errorf("output should explain a directory override won: %q", out)
	}
	if !strings.Contains(out, "/work/crewship_3") {
		t.Errorf("output should name the matched directory: %q", out)
	}
	if !strings.Contains(out, "dev3-test") {
		t.Errorf("output should mention the persisted default dev3-test that got overridden: %q", out)
	}
}

// Outside any directory-mapped clone, `server current` should show the
// persisted default plainly, with no directory-override hint.
func TestServerCurrentPlainWhenNoDirectoryOverride(t *testing.T) {
	redirectConfigHome(t)
	oldProfile := flagProfile
	flagProfile = ""
	t.Cleanup(func() { flagProfile = oldProfile })
	t.Setenv("CREWSHIP_PROFILE", "")

	cfg, err := cli.LoadConfig()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cfg.Current = "dev3-test"
	cfg.Servers = map[string]*cli.ServerProfile{
		"dev3-test": {Server: "https://dev3-test.example", Token: "t2"},
	}
	if err := cli.SaveConfig(cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	cli.SetWorkingDir("/tmp")
	t.Cleanup(func() { cli.SetWorkingDir("") })

	out, err := captureStdout(t, func() error {
		return serverCurrentCmd.RunE(serverCurrentCmd, nil)
	})
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	if !strings.Contains(out, "dev3-test") {
		t.Errorf("output should show active profile dev3-test: %q", out)
	}
	if strings.Contains(out, "directory override") {
		t.Errorf("no directory override in play — output should not mention one: %q", out)
	}
}

// #1210: `server list`'s active marker (*) should indicate when the
// asterisked profile is active because of a directory override rather
// than the persisted `server use` default, so the two aren't visually
// indistinguishable.
func TestServerListMarksDirectoryOverride(t *testing.T) {
	redirectConfigHome(t)
	oldProfile := flagProfile
	flagProfile = ""
	t.Cleanup(func() { flagProfile = oldProfile })
	t.Setenv("CREWSHIP_PROFILE", "")

	cfg, err := cli.LoadConfig()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cfg.Current = "dev3-test"
	cfg.Servers = map[string]*cli.ServerProfile{
		"dev3":      {Server: "https://dev3.example", Token: "t"},
		"dev3-test": {Server: "https://dev3-test.example", Token: "t2"},
	}
	cfg.DirectoryProfiles = map[string]string{"/work/crewship_3": "dev3"}
	if err := cli.SaveConfig(cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	cli.SetWorkingDir("/work/crewship_3/sub")
	t.Cleanup(func() { cli.SetWorkingDir("") })

	out, err := captureStdout(t, func() error {
		return serverListCmd.RunE(serverListCmd, nil)
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	lines := strings.Split(out, "\n")
	var dev3Line, dev3TestLine string
	for _, l := range lines {
		if strings.Contains(l, "dev3-test") {
			dev3TestLine = l
		} else if strings.Contains(l, "dev3") {
			dev3Line = l
		}
	}
	if !strings.Contains(dev3Line, "*") {
		t.Errorf("dev3 should carry the active marker: %q", dev3Line)
	}
	if !strings.Contains(dev3Line, "*d") {
		t.Errorf("dev3's marker should hint it's a directory override (*d): %q", dev3Line)
	}
	if strings.Contains(dev3TestLine, "*") {
		t.Errorf("dev3-test (persisted default, overridden) should not carry the active marker: %q", dev3TestLine)
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
