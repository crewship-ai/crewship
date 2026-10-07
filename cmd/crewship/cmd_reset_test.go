//go:build !clionly

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

func resetFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"CREWSHIP_HOME", "CREWSHIP_DATA_DIR", "DATABASE_URL", "CREWSHIP_STORAGE_BASE_PATH", "CREWSHIP_LOG_PATH", "CREWSHIP_STORAGE_MEMORY_ROOT", "CREWSHIP_BOLT_PATH", "CREWSHIP_SOCKET_PATH", "CREWSHIP_PAGE_PROJECTS_PATH"} {
		t.Setenv(name, "")
	}
	t.Setenv("CREWSHIP_SKIP_SIDECAR", "1")
	db, err := database.Open("file:" + filepath.Join(root, "crewship.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(context.Background(), db.DB, covLogger()); err != nil {
		t.Fatal(err)
	}
	if err := database.RunPostDeployMigrations(context.Background(), db.DB, covLogger()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,full_name,hashed_password) VALUES('reset-user','reset@example.test','Reset','hash')`); err != nil {
		t.Fatal(err)
	}
	identity, err := resourcelifecycle.LoadIdentity(context.Background(), root, db.DB, "file:"+filepath.Join(root, "crewship.db"))
	if err != nil {
		t.Fatal(err)
	}
	id := identity.ID
	identity.Close()
	db.Close()
	for _, dir := range []string{"output", "memory", "chats", "skills", "logs", "backups", "config"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "artifact"), []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "secrets.env"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "state.db"), []byte("old-bolt-content"), 0600); err != nil {
		t.Fatal(err)
	}
	return root, id
}
func resetCommand(root string) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.Flags().String("data-dir", root, "")
	cmd.Flags().String("config", "", "")
	cmd.Flags().String("db", "", "")
	cmd.Flags().Bool("data", true, "")
	cmd.Flags().Bool("plan", false, "")
	return cmd
}
func TestOfflineResetRetainsIdentityAndManagementFilesAndRetries(t *testing.T) {
	root, id := resetFixture(t)
	old := resetDockerResources
	t.Cleanup(func() { resetDockerResources = old })
	calls := 0
	resetDockerResources = func(_ context.Context, instance string) error {
		if instance != id {
			t.Fatal("wrong Docker ownership")
		}
		calls++
		return nil
	}
	outside := t.TempDir()
	protected := filepath.Join(outside, "keep")
	os.WriteFile(protected, []byte("keep"), 0600)
	if err := os.Symlink(outside, filepath.Join(root, "output", "external")); err != nil {
		t.Skip(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := runReset(resetCommand(root), nil); err != nil {
			t.Fatal(err)
		}
		db, err := database.Open("file:" + filepath.Join(root, "crewship.db"))
		if err != nil {
			t.Fatal(err)
		}
		var users int
		if err := db.QueryRow(`SELECT count(*) FROM users`).Scan(&users); err != nil {
			t.Fatal(err)
		}
		if users != 0 {
			t.Fatal("users retained")
		}
		identity, err := resourcelifecycle.AcquireExistingIdentity(context.Background(), root, db.DB, "file:"+filepath.Join(root, "crewship.db"))
		if err != nil {
			t.Fatal(err)
		}
		if identity.ID != id {
			t.Fatal("identity changed")
		}
		identity.Close()
		db.Close()
	}
	if err := database.CheckResetPending(root); err != nil {
		t.Fatal("completed reset still blocked", err)
	}
	if calls != 2 {
		t.Fatal("cleanup did not retry")
	}
	for _, dir := range []string{"output", "memory", "chats", "skills"} {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil || len(entries) != 0 {
			t.Fatal("store not cleared", dir, err)
		}
	}
	for _, path := range []string{"logs/artifact", "backups/artifact", "config/artifact", "secrets.env"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err != nil {
			t.Fatal("preserved file lost", path, err)
		}
	}
	if contents, err := os.ReadFile(protected); err != nil || string(contents) != "keep" {
		t.Fatal("escaped symlink target touched", err)
	}
	if stat, err := os.Stat(filepath.Join(root, "state.db")); err != nil || stat.Size() != 0 {
		t.Fatal("Bolt not reset", err)
	}
}
func TestOfflineResetDockerFailureAndRunningInstallationKeepData(t *testing.T) {
	root, _ := resetFixture(t)
	old := resetDockerResources
	t.Cleanup(func() { resetDockerResources = old })
	resetDockerResources = func(context.Context, string) error { return errors.New("inventory unavailable") }
	if err := runReset(resetCommand(root), nil); err == nil || !strings.Contains(err.Error(), "inventory unavailable") {
		t.Fatal(err)
	}
	if err := database.CheckResetPending(root); err == nil {
		t.Fatal("failed reset allows startup")
	}
	if _, err := os.Stat(filepath.Join(root, "output", "artifact")); err != nil {
		t.Fatal("Docker failure deleted data", err)
	}
	live, err := database.AcquireInstallationOperation(root)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	if err := runReset(resetCommand(root), nil); err == nil || !strings.Contains(err.Error(), "installation is running") {
		t.Fatal(err)
	}
}
func TestResetRefusesExternalOrPreservedStore(t *testing.T) {
	root, _ := resetFixture(t)
	for _, path := range []string{t.TempDir(), root, filepath.Join(root, "logs"), filepath.Join(root, "custom-unproven")} {
		t.Setenv("CREWSHIP_STORAGE_BASE_PATH", path)
		if _, err := prepareReset(resetCommand(root)); err == nil {
			t.Errorf("accepted unsafe store %s", path)
		}
	}
}

func TestResetRefusesHardlinkedDatabase(t *testing.T) {
	root, _ := resetFixture(t)
	outside := filepath.Join(t.TempDir(), "shared.db")
	if err := os.Link(filepath.Join(root, "crewship.db"), outside); err != nil {
		t.Skip(err)
	}
	if _, err := prepareReset(resetCommand(root)); err == nil || !strings.Contains(err.Error(), "linked") {
		t.Fatal(err)
	}
}

func TestResetRejectsRelativeDatabaseTarget(t *testing.T) {
	root, _ := resetFixture(t)
	t.Setenv("DATABASE_URL", "file:./crewship.db")
	if _, err := prepareReset(resetCommand(root)); err == nil || !strings.Contains(err.Error(), "absolute SQLite") {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", "")
	cmd := resetCommand(root)
	if err := cmd.Flags().Set("db", "file:crewship.db"); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareReset(cmd); err == nil || !strings.Contains(err.Error(), "absolute SQLite") {
		t.Fatal(err)
	}
	if err := database.CheckResetPending(root); err != nil {
		t.Fatal("validation created reset marker", err)
	}
	if _, err := os.Stat(filepath.Join(root, "output", "artifact")); err != nil {
		t.Fatal("validation deleted data", err)
	}
}

func TestResetSchemaMismatchPrecedesDestructionAndMarker(t *testing.T) {
	root, _ := resetFixture(t)
	db, err := database.Open("file:" + filepath.Join(root, "crewship.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE incompatible_extra_table(id TEXT)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	old := resetDockerResources
	t.Cleanup(func() { resetDockerResources = old })
	calls := 0
	resetDockerResources = func(context.Context, string) error { calls++; return nil }
	if err := runReset(resetCommand(root), nil); err == nil || !strings.Contains(err.Error(), "schema") {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("schema mismatch reached Docker cleanup")
	}
	if err := database.CheckResetPending(root); err != nil {
		t.Fatal("schema mismatch prevents upgrade startup", err)
	}
	if _, err := os.Stat(filepath.Join(root, "output", "artifact")); err != nil {
		t.Fatal("schema mismatch deleted data", err)
	}
}

func TestResetRejectsRelativeLegacyRoot(t *testing.T) {
	root, _ := resetFixture(t)
	t.Setenv("CREWSHIP_DATA_DIR", "./state")
	cmd := resetCommand("")
	if _, err := prepareReset(cmd); err == nil || !strings.Contains(err.Error(), "absolute CREWSHIP_DATA_DIR") {
		t.Fatal(err)
	}
	// Explicit absolute flag safely takes precedence over an unrelated legacy env.
	if _, err := prepareReset(resetCommand(root)); err != nil {
		t.Fatal(err)
	}
}

func TestResetPreservesManagementFilesAndConfigAliases(t *testing.T) {
	root, _ := resetFixture(t)
	for _, name := range []string{"secrets.env", "initial_setup_token", "cli-config.yaml"} {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte("preserve"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CREWSHIP_BOLT_PATH", path)
		if _, err := prepareReset(resetCommand(root)); err == nil {
			t.Fatalf("accepted Bolt at preserved %s", name)
		}
		contents, err := os.ReadFile(path)
		if err != nil || string(contents) != "preserve" {
			t.Fatal("management file changed", name, err)
		}
	}
	t.Setenv("CREWSHIP_BOLT_PATH", "")
	configPath := filepath.Join(root, "server.yaml")
	if err := os.WriteFile(configPath, []byte("logging:\n  level: info\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := resetCommand(root)
	if err := cmd.Flags().Set("config", configPath); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREWSHIP_BOLT_PATH", configPath)
	if _, err := prepareReset(cmd); err == nil {
		t.Fatal("accepted Bolt at actual YAML config")
	}
	t.Setenv("CREWSHIP_BOLT_PATH", "")
	nestedConfig := filepath.Join(root, "output", "server.yaml")
	if err := os.WriteFile(nestedConfig, []byte("logging:\n  level: info\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("config", nestedConfig); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareReset(cmd); err == nil {
		t.Fatal("accepted clearing config parent store")
	}
	target := filepath.Join(root, "aliased-secret")
	if err := os.WriteFile(target, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "secrets.env")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "secrets.env")); err != nil {
		t.Skip(err)
	}
	t.Setenv("CREWSHIP_BOLT_PATH", target)
	if _, err := prepareReset(resetCommand(root)); err == nil {
		t.Fatal("accepted Bolt at secrets symlink target")
	}
}

func TestResetRefusesRelativeConfig(t *testing.T) {
	cmd := resetCommand(t.TempDir())
	if err := cmd.Flags().Set("config", "config.yaml"); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareReset(cmd); err == nil || !strings.Contains(err.Error(), "absolute --config") {
		t.Fatal(err)
	}
}

func TestResetPreviewFormatAndSuccessReceiptStreams(t *testing.T) {
	saveCLIState(t)
	flagFormat = "json"
	root, _ := resetFixture(t)
	old := resetDockerResources
	t.Cleanup(func() { resetDockerResources = old })
	calls := 0
	resetDockerResources = func(context.Context, string) error { calls++; return nil }
	preview := resetCommand(root)
	if err := preview.Flags().Set("plan", "true"); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	preview.SetOut(&stdout)
	preview.SetErr(&stderr)
	if err := runReset(preview, nil); err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("plan is not JSON: %q: %v", stdout.String(), err)
	}
	if report["installation"] != root || calls != 0 || stderr.Len() != 0 {
		t.Fatalf("invalid preview: %+v, Docker calls %d, stderr %q", report, calls, stderr.String())
	}
	stdout.Reset()
	reset := resetCommand(root)
	reset.SetOut(&stdout)
	reset.SetErr(&stderr)
	if err := runReset(reset, nil); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "Application data reset") {
		t.Fatalf("mutation receipt must only go to stderr: stdout %q, stderr %q", stdout.String(), stderr.String())
	}
}
