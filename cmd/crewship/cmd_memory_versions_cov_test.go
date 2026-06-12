//go:build !clionly

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/memory"
)

const (
	covMemWorkspace = "ws_cov_mem"
	covMemPath      = "crew:crew_cov/learned-2026-06-01.md"
)

// covSeedMemoryVersion migrates a fresh SQLite DB, records one memory
// version with the given content and points DATABASE_URL at it.
// Returns the recorded sha and the blob root used.
func covSeedMemoryVersion(t *testing.T, content string) (sha, blobRoot string) {
	t.Helper()
	dbURL := initTestDB(t)
	blobRoot = filepath.Join(t.TempDir(), "memory", "versions")

	db, err := database.Open(dbURL)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// memory_versions.workspace_id carries an FK to workspaces.
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Cov WS', 'cov-ws')`, covMemWorkspace); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	res, err := memory.RecordVersion(context.Background(), db.DB, memory.VersionRecord{
		WorkspaceID: covMemWorkspace,
		Path:        covMemPath,
		Tier:        memory.TierLearned,
		Content:     []byte(content),
		WrittenBy:   "seed-test",
		BlobRoot:    blobRoot,
	})
	if err != nil {
		t.Fatalf("record version: %v", err)
	}

	t.Setenv("DATABASE_URL", dbURL)
	return res.Sha256, blobRoot
}

func covMemLogCmd() *cobra.Command {
	c := &cobra.Command{Use: "log", RunE: runMemoryLog}
	c.Flags().Int("limit", 20, "")
	c.Flags().String("format", "json", "")
	return c
}

func covMemRestoreCmd() *cobra.Command {
	c := &cobra.Command{Use: "restore", RunE: runMemoryRestore}
	c.Flags().String("blob-root", "", "")
	c.Flags().String("user", "", "")
	c.Flags().String("tier", "learned", "")
	c.Flags().Bool("force", false, "")
	return c
}

func TestRunMemoryLog_JSONOutput(t *testing.T) {
	sha, _ := covSeedMemoryVersion(t, "alpha learned content\n")

	c := covMemLogCmd()
	out, err := covCaptureStdoutCli4(t, func() error {
		return c.RunE(c, []string{covMemWorkspace, covMemPath})
	})
	if err != nil {
		t.Fatalf("runMemoryLog: %v", err)
	}
	var entries []struct {
		Sha256    string `json:"sha256"`
		WrittenBy string `json:"written_by"`
		Bytes     int    `json:"bytes"`
	}
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("json output unparseable: %v\n%q", err, out)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].Sha256 != sha || entries[0].WrittenBy != "seed-test" {
		t.Errorf("entry = %+v, want sha %s by seed-test", entries[0], sha)
	}
	if entries[0].Bytes != len("alpha learned content\n") {
		t.Errorf("bytes = %d", entries[0].Bytes)
	}
}

func TestRunMemoryLog_TextFormatAndUnknownFormat(t *testing.T) {
	sha, _ := covSeedMemoryVersion(t, "text format content")

	c := covMemLogCmd()
	if err := c.Flags().Set("format", "text"); err != nil {
		t.Fatal(err)
	}
	out, err := covCaptureStdoutCli4(t, func() error {
		return c.RunE(c, []string{covMemWorkspace, covMemPath})
	})
	if err != nil {
		t.Fatalf("text format: %v", err)
	}
	// git-log-style one-liner: 12-char sha prefix + writer.
	if !strings.Contains(out, sha[:12]) || !strings.Contains(out, "seed-test") {
		t.Errorf("text row missing: %q", out)
	}
	if strings.Contains(out, sha) {
		t.Errorf("text mode should truncate the sha to 12 chars: %q", out)
	}

	if err := c.Flags().Set("format", "xml"); err != nil {
		t.Fatal(err)
	}
	err = c.RunE(c, []string{covMemWorkspace, covMemPath})
	if err == nil || !strings.Contains(err.Error(), `unknown --format "xml"`) {
		t.Fatalf("want unknown-format error, got %v", err)
	}
}

func TestRunMemoryLog_NoVersionsIsNotAnError(t *testing.T) {
	covSeedMemoryVersion(t, "irrelevant")

	c := covMemLogCmd()
	out, err := covCaptureStdoutCli4(t, func() error {
		return c.RunE(c, []string{covMemWorkspace, "crew:other/none.md"})
	})
	if err != nil {
		t.Fatalf("want nil error for empty chain, got %v", err)
	}
	if !strings.Contains(out, "no versions for") {
		t.Errorf("empty-chain notice missing: %q", out)
	}
}

func TestRunMemoryLog_DatabaseMissing(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("CREWSHIP_DATA_DIR", t.TempDir()) // dir exists, crewship.db does not

	c := covMemLogCmd()
	err := c.RunE(c, []string{covMemWorkspace, covMemPath})
	if err == nil || !strings.Contains(err.Error(), "database not found") {
		t.Fatalf("want database-not-found, got %v", err)
	}
}

func TestRunMemoryShow_StreamsBlobToStdout(t *testing.T) {
	const content = "## learned\n- always run the tests\n"
	sha, _ := covSeedMemoryVersion(t, content)

	c := &cobra.Command{Use: "show", RunE: runMemoryShow}
	out, err := covCaptureStdoutCli4(t, func() error {
		return c.RunE(c, []string{covMemWorkspace, covMemPath, sha})
	})
	if err != nil {
		t.Fatalf("runMemoryShow: %v", err)
	}
	if out != content {
		t.Errorf("stdout = %q, want raw blob %q", out, content)
	}
}

func TestRunMemoryRestore_InvalidTier(t *testing.T) {
	sha, blobRoot := covSeedMemoryVersion(t, "x")
	c := covMemRestoreCmd()
	covSetFlagsCli4(t, c, map[string]string{"blob-root": blobRoot, "tier": "bogus"})
	err := c.RunE(c, []string{covMemWorkspace, covMemPath, sha, filepath.Join(filepath.Dir(blobRoot), "f.md")})
	if err == nil || !strings.Contains(err.Error(), `invalid --tier "bogus"`) {
		t.Fatalf("want invalid-tier error, got %v", err)
	}
}

func TestRunMemoryRestore_RefusesPathOutsideRoot(t *testing.T) {
	sha, blobRoot := covSeedMemoryVersion(t, "x")
	c := covMemRestoreCmd()
	covSetFlagsCli4(t, c, map[string]string{"blob-root": blobRoot})
	outside := filepath.Join(t.TempDir(), "escape.md")
	err := c.RunE(c, []string{covMemWorkspace, covMemPath, sha, outside})
	if err == nil || !strings.Contains(err.Error(), "refusing to restore") {
		t.Fatalf("want containment refusal, got %v", err)
	}
	if _, statErr := os.Stat(outside); !os.IsNotExist(statErr) {
		t.Errorf("refused restore must not write the file (stat err = %v)", statErr)
	}
}

func TestRunMemoryRestore_HappyPath_WritesFileAndAuditRow(t *testing.T) {
	const content = "restore me please\n"
	sha, blobRoot := covSeedMemoryVersion(t, content)

	// Canonical target inside {data}/memory — one level above blobRoot.
	canonical := filepath.Join(filepath.Dir(blobRoot), "topics", "crew_cov", "learned-2026-06-01.md")

	c := covMemRestoreCmd()
	covSetFlagsCli4(t, c, map[string]string{
		"blob-root": blobRoot,
		"user":      "operator-7",
	})
	out, err := covCaptureStdoutCli4(t, func() error {
		return c.RunE(c, []string{covMemWorkspace, covMemPath, sha, canonical})
	})
	if err != nil {
		t.Fatalf("runMemoryRestore: %v", err)
	}
	if !strings.Contains(out, "restored "+covMemWorkspace) || !strings.Contains(out, canonical) {
		t.Errorf("restore confirmation missing: %q", out)
	}

	got, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(got) != content {
		t.Errorf("restored content = %q, want %q", got, content)
	}

	// The audit chain gained a forward-only row written by operator-7
	// whose parent is the original version.
	db, err := database.Open(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()
	entries, err := memory.LogVersions(context.Background(), db.DB, covMemWorkspace, covMemPath, 10)
	if err != nil {
		t.Fatalf("log versions: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("audit rows = %d, want 2 (original + restore)", len(entries))
	}
	var restoreRow bool
	for _, e := range entries {
		if e.WrittenBy == "operator-7" {
			restoreRow = true
			if e.Sha256 != sha {
				t.Errorf("restore row sha = %s, want %s (content unchanged)", e.Sha256, sha)
			}
			if e.ParentSha != sha {
				t.Errorf("restore row parent = %s, want %s", e.ParentSha, sha)
			}
		}
	}
	if !restoreRow {
		t.Errorf("no audit row attributed to operator-7: %+v", entries)
	}
}

func TestRunMemoryRestore_ForceBypassesGuard(t *testing.T) {
	const content = "forced restore\n"
	sha, blobRoot := covSeedMemoryVersion(t, content)
	outside := filepath.Join(t.TempDir(), "outside-tree.md")

	c := covMemRestoreCmd()
	covSetFlagsCli4(t, c, map[string]string{
		"blob-root": blobRoot,
		"force":     "true",
	})
	_, err := covCaptureStdoutCli4(t, func() error {
		return c.RunE(c, []string{covMemWorkspace, covMemPath, sha, outside})
	})
	if err != nil {
		t.Fatalf("forced restore: %v", err)
	}
	got, err := os.ReadFile(outside)
	if err != nil || string(got) != content {
		t.Errorf("forced restore did not write target: %v / %q", err, got)
	}
}

// ─── remaining error branches ────────────────────────────────────────

func TestDefaultBlobRoot_HomeFallback(t *testing.T) {
	t.Setenv("CREWSHIP_DATA_DIR", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir on this runner: %v", err)
	}
	got, err := defaultBlobRoot()
	if err != nil {
		t.Fatalf("defaultBlobRoot: %v", err)
	}
	if got != home+"/.crewship/memory/versions" {
		t.Errorf("got %q", got)
	}
}

func TestDefaultBlobRoot_NoHomeErrors(t *testing.T) {
	t.Setenv("CREWSHIP_DATA_DIR", "")
	t.Setenv("HOME", "")
	if _, err := defaultBlobRoot(); err == nil {
		t.Error("want error when HOME and CREWSHIP_DATA_DIR are both unset")
	}
}

func TestRunMemoryLog_QueryErrorOnUnmigratedDB(t *testing.T) {
	// A reachable SQLite file WITHOUT the schema: openAdminDB succeeds,
	// the memory_versions query fails.
	dir := t.TempDir()
	dbURL := "file:" + filepath.Join(dir, "empty.db")
	db, err := database.Open(dbURL)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.Close()
	t.Setenv("DATABASE_URL", dbURL)

	c := covMemLogCmd()
	err = c.RunE(c, []string{covMemWorkspace, covMemPath})
	if err == nil || !strings.Contains(err.Error(), "log versions") {
		t.Fatalf("want query error, got %v", err)
	}
}

func TestRunMemoryShow_DatabaseMissing(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("CREWSHIP_DATA_DIR", t.TempDir())
	c := &cobra.Command{Use: "show", RunE: runMemoryShow}
	err := c.RunE(c, []string{covMemWorkspace, covMemPath, "deadbeef"})
	if err == nil || !strings.Contains(err.Error(), "database not found") {
		t.Fatalf("want database-not-found, got %v", err)
	}
}

func TestRunMemoryShow_BlobMissingSurfacesReadError(t *testing.T) {
	// Row exists but the content-addressed blob was deleted (e.g. a
	// retention sweep leaked the row) → non-notfound error, no os.Exit.
	sha, blobRoot := covSeedMemoryVersion(t, "vanishing content")
	blobPath := filepath.Join(blobRoot, sha[:2], sha)
	if err := os.Remove(blobPath); err != nil {
		t.Fatalf("remove blob: %v", err)
	}

	c := &cobra.Command{Use: "show", RunE: runMemoryShow}
	err := c.RunE(c, []string{covMemWorkspace, covMemPath, sha})
	if err == nil || !strings.Contains(err.Error(), "read blob") {
		t.Fatalf("want read-blob error, got %v", err)
	}
}

func TestRunMemoryRestore_DatabaseMissing(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	dataDir := t.TempDir()
	t.Setenv("CREWSHIP_DATA_DIR", dataDir)

	c := covMemRestoreCmd()
	// Guards must pass first: default blob root = {data}/memory/versions,
	// canonical path inside {data}/memory.
	canonical := filepath.Join(dataDir, "memory", "f.md")
	err := c.RunE(c, []string{covMemWorkspace, covMemPath, "deadbeef", canonical})
	if err == nil || !strings.Contains(err.Error(), "database not found") {
		t.Fatalf("want database-not-found, got %v", err)
	}
}

func TestRunMemoryRestore_BlobMissingSurfacesError(t *testing.T) {
	sha, blobRoot := covSeedMemoryVersion(t, "gone content")
	if err := os.Remove(filepath.Join(blobRoot, sha[:2], sha)); err != nil {
		t.Fatalf("remove blob: %v", err)
	}
	canonical := filepath.Join(filepath.Dir(blobRoot), "f.md")

	c := covMemRestoreCmd()
	covSetFlagsCli4(t, c, map[string]string{"blob-root": blobRoot})
	err := c.RunE(c, []string{covMemWorkspace, covMemPath, sha, canonical})
	if err == nil || !strings.Contains(err.Error(), "read blob") {
		t.Fatalf("want read-blob error, got %v", err)
	}
	if _, statErr := os.Stat(canonical); !os.IsNotExist(statErr) {
		t.Errorf("failed restore must not leave the canonical file behind")
	}
}

func TestRunMemoryRestore_DefaultBlobRootFromDataDir(t *testing.T) {
	const content = "default-root restore\n"
	sha, blobRoot := covSeedMemoryVersion(t, content)
	// Point CREWSHIP_DATA_DIR at the dir whose memory/versions IS the
	// seeded blob root, so the --blob-root default resolves to it.
	dataDir := filepath.Dir(filepath.Dir(blobRoot)) // strip /memory/versions
	t.Setenv("CREWSHIP_DATA_DIR", dataDir)

	canonical := filepath.Join(dataDir, "memory", "restored-by-default.md")
	c := covMemRestoreCmd()
	// NOTE: --blob-root deliberately left empty.
	if _, err := covCaptureStdoutCli4(t, func() error {
		return c.RunE(c, []string{covMemWorkspace, covMemPath, sha, canonical})
	}); err != nil {
		t.Fatalf("restore with default blob root: %v", err)
	}
	got, err := os.ReadFile(canonical)
	if err != nil || string(got) != content {
		t.Errorf("restored file = %q / %v", got, err)
	}
}
