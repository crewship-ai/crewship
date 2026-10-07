//go:build !clionly

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Exercise the actual startup ordering: legacy content is detected before
// generating secrets, creating the selected storage, or opening the DB.
func TestStartLegacyGuardPrecedesBootstrapWrites(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "output")
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(output, "existing-agent-file")
	if err := os.WriteFile(artifact, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "new-store")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("CREWSHIP_SKIP_SIDECAR", "1")
	t.Setenv("CREWSHIP_STORAGE_BASE_PATH", target)
	t.Setenv("CREWSHIP_LOG_PATH", "")
	t.Setenv("CREWSHIP_STORAGE_MEMORY_ROOT", "")
	t.Setenv("CREWSHIP_BOLT_PATH", "")
	t.Setenv("CREWSHIP_SOCKET_PATH", "")
	cmd := &cobra.Command{}
	cmd.Flags().String("config", "", "")
	cmd.Flags().String("db", "", "")
	cmd.Flags().String("data-dir", root, "")
	cmd.Flags().Bool("no-docker", true, "")
	err := startCmd.RunE(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "refusing to abandon") {
		t.Fatalf("got %v", err)
	}
	for _, path := range []string{target, filepath.Join(root, "secrets.env"), filepath.Join(root, "crewship.db"), filepath.Join(root, "state.db"), filepath.Join(root, "logs")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("startup wrote %s before rejecting layout: %v", path, err)
		}
	}
	data, err := os.ReadFile(artifact)
	if err != nil || string(data) != "retained" {
		t.Fatalf("legacy artifact changed: %q, %v", data, err)
	}
}

func TestStartDataDirFlagRestoresPublishedEnvironmentOnFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "selected")
	t.Setenv("CREWSHIP_HOME", "")
	t.Setenv("CREWSHIP_DATA_DIR", "")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("CREWSHIP_SKIP_SIDECAR", "1")
	t.Setenv("ENCRYPTION_KEY", "invalid-bootstrap-key")
	for _, name := range []string{"CREWSHIP_STORAGE_BASE_PATH", "CREWSHIP_LOG_PATH", "CREWSHIP_STORAGE_MEMORY_ROOT", "CREWSHIP_BOLT_PATH", "CREWSHIP_SOCKET_PATH"} {
		t.Setenv(name, "")
	}
	cmd := &cobra.Command{}
	cmd.Flags().String("config", "", "")
	cmd.Flags().String("db", "", "")
	cmd.Flags().String("data-dir", root, "")
	cmd.Flags().Bool("no-docker", true, "")
	err := startCmd.RunE(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "bootstrap secrets") {
		t.Fatalf("got %v", err)
	}
	if os.Getenv("CREWSHIP_HOME") != "" || os.Getenv("CREWSHIP_DATA_DIR") != "" {
		t.Fatal("installation root leaked into caller environment")
	}
	if _, err := os.Stat(filepath.Join(root, "output")); err != nil {
		t.Fatal("selected root not provisioned", err)
	}
	if _, err := os.Stat(filepath.Join(root, "crewship.db")); !os.IsNotExist(err) {
		t.Fatal("database created before secret validation", err)
	}
}

func TestRelativeDatabaseURL(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"file:./crewship.db", true}, {"file:crewship.db?_pragma=journal_mode(WAL)", true},
		{"file:/var/lib/crewship/crewship.db", false}, {"file::memory:?cache=shared", false},
		{"file:fixture?mode=memory", false}, {":memory:", false}, {"", false},
	} {
		if got := relativeDatabaseURL(tc.value); got != tc.want {
			t.Errorf("relativeDatabaseURL(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}
