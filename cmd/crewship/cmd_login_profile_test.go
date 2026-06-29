package main

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestPersistCredentialLegacy(t *testing.T) {
	path := redirectConfigHome(t)
	if err := persistCredential("https://dev1.example", "tok1", ""); err != nil {
		t.Fatalf("persist: %v", err)
	}
	cfg := readCfg(t, path)
	if cfg.Server != "https://dev1.example" || cfg.Token != "tok1" {
		t.Errorf("legacy creds not saved to top-level: %+v", cfg)
	}
	if len(cfg.Servers) != 0 {
		t.Errorf("legacy login must not create profiles, got %+v", cfg.Servers)
	}
}

func TestPersistCredentialProfile(t *testing.T) {
	path := redirectConfigHome(t)
	if err := persistCredential("https://dev2.example", "tok2", "dev2"); err != nil {
		t.Fatalf("persist: %v", err)
	}
	cfg := readCfg(t, path)
	p := cfg.Servers["dev2"]
	if p == nil || p.Token != "tok2" || p.Server != "https://dev2.example" {
		t.Fatalf("profile creds not saved: %+v", cfg.Servers)
	}
	if cfg.Current != "dev2" {
		t.Errorf("login --profile should set current, got %q", cfg.Current)
	}
	if cfg.Token != "" {
		t.Errorf("profile login leaked into top-level token: %q", cfg.Token)
	}
}

func TestPersistCredentialPreservesProfileWorkspace(t *testing.T) {
	path := redirectConfigHome(t)
	// Pre-seed dev2 with a workspace, as `crewship server add --workspace` would.
	seed := &cli.CLIConfig{
		Servers: map[string]*cli.ServerProfile{
			"dev2": {Server: "https://old", Workspace: "ws2"},
		},
	}
	if err := cli.SaveConfig(seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := persistCredential("https://dev2.example", "tok2", "dev2"); err != nil {
		t.Fatalf("persist: %v", err)
	}
	cfg := readCfg(t, path)
	p := cfg.Servers["dev2"]
	if p.Workspace != "ws2" {
		t.Errorf("login wiped the profile's pre-set workspace: %+v", p)
	}
	if p.Server != "https://dev2.example" || p.Token != "tok2" {
		t.Errorf("login did not update server/token: %+v", p)
	}
}
