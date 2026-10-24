package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The production BulkImport path is used. The owned fake git performs no DNS,
// network or real clone: only a local marker write and exit.
func TestRegressionBulkImportRejectsLocalhostDotBeforeGit(t *testing.T) {
	imp := newCovImporter(t)
	marker := filepath.Join(t.TempDir(), "git-called")
	quoted := "'" + strings.ReplaceAll(marker, "'", "'\\''") + "'"
	installFakeGit(t, "printf 'called\\n' > "+quoted+"\nexit 0")
	_, importErr := imp.BulkImport(context.Background(), BulkImportRequest{GitURL: "https://localhost./repo.git"})
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("unsafe localhost-dot hostname passed validation and reached git subprocess; require rejection or a guarded transport before clone")
	} else if !os.IsNotExist(err) {
		t.Fatalf("inspect owned marker: %v", err)
	}
	if importErr == nil {
		t.Fatal("localhost-dot import unexpectedly succeeded")
	}
}

// Verifies a concrete subprocess-policy gap, not an external redirect exploit.
// Only synthetic GIT_CONFIG_* values are captured; no host config is read.
func TestRegressionBulkImportIsolatesInheritedRedirectConfig(t *testing.T) {
	imp := newCovImporter(t)
	marker := filepath.Join(t.TempDir(), "git-config")
	quoted := "'" + strings.ReplaceAll(marker, "'", "'\\''") + "'"
	installFakeGit(t, "printf '%s\\n' \"${GIT_CONFIG_COUNT-}\" \"${GIT_CONFIG_KEY_0-}\" \"${GIT_CONFIG_VALUE_0-}\" > "+quoted+"\nexit 0")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "http.followRedirects")
	t.Setenv("GIT_CONFIG_VALUE_0", "true")
	_, importErr := imp.BulkImport(context.Background(), BulkImportRequest{GitURL: "https://localhost./repo.git"})
	capture, err := os.ReadFile(marker)
	if os.IsNotExist(err) && importErr != nil {
		return
	} // fail-closed before clone is valid
	if err != nil {
		t.Fatalf("read owned fake-git capture: %v", err)
	}
	if string(capture) == "1\nhttp.followRedirects\ntrue\n" {
		t.Fatal("bulk-import git subprocess inherited synthetic http.followRedirects=true override unchanged; importer does not isolate Git configuration")
	}
}
