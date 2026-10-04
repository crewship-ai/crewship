package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/devcontainer"
)

func TestCrewLockPackIsLocalAndPreservesNativeFile(t *testing.T) {
	dir := t.TempDir()
	content := "lockfile_version = 3\n"
	if err := os.WriteFile(filepath.Join(dir, "mise.lock"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	old := crewLockPackCmd.OutOrStdout()
	crewLockPackCmd.SetOut(&out)
	defer crewLockPackCmd.SetOut(old)
	if err := crewLockPackCmd.RunE(crewLockPackCmd, []string{dir}); err != nil {
		t.Fatal(err)
	}
	var bundle devcontainer.MiseLockBundle
	if err := json.Unmarshal(out.Bytes(), &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.Files["mise.lock"] != content {
		t.Fatalf("packed=%s", out.String())
	}
}
