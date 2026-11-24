package nfpm

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDebUpgradePreservesLocalFiles(t *testing.T) {
	root := t.TempDir()
	cli := filepath.Join(root, "usr/libexec/crewship/crewship-agent-cli")
	if err := os.MkdirAll(cli, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cli, "crewship-agent-cli"), []byte("old executable"), 0755); err != nil {
		t.Fatal(err)
	}
	upgrade := func() error {
		cmd := exec.Command("/bin/sh", "preinstall.sh", "upgrade", "1", "2")
		cmd.Env = append(os.Environ(), "DPKG_ROOT="+root)
		return cmd.Run()
	}
	if err := upgrade(); err != nil {
		t.Fatalf("standard upgrade: %v", err)
	}
	sentinel := filepath.Join(cli, ".local-setting")
	if err := os.WriteFile(sentinel, []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	if upgrade() == nil {
		t.Fatal("permitted unpack over local additions")
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep me" {
		t.Fatal("local addition lost")
	}
}
