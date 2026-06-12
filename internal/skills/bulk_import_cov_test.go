package skills

// Coverage-focused tests for bulk_import.go. Internal package tests so
// the unexported helpers (validateGitRef, pathMatchesAny) and the
// BulkImport walk paths are reachable without production changes.

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
)

// newCovTestDB opens a temp SQLite database with the full migration set
// applied. Mirrors setupSkillTestDB from the external test package
// (which is not visible from the internal one).
func newCovTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open("file:" + filepath.Join(dir, "cov.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	logger := covTestLogger()
	if err := database.Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db.DB
}

func covTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func newCovImporter(t *testing.T) *Importer {
	t.Helper()
	return NewImporter(newCovTestDB(t), covTestLogger())
}

// writeSkill writes a SKILL.md under root/rel with the given body.
func writeSkill(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func skillMD(name, license string) string {
	return "---\nname: " + name + "\nlicense: " + license +
		"\ndescription: A bulk import test fixture skill.\n---\n# " + name + "\nInstructions.\n"
}

func TestValidateGitRef_Table(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		ref     string
		wantErr string // empty = valid
	}{
		{"branch", "main", ""},
		{"tag", "v1.2.3", ""},
		{"nested", "feature/wake-gates", ""},
		{"sha", "f22c232e", ""},
		{"underscore dot", "rel_1.0", ""},
		{"empty", "", "must not be empty"},
		{"too long", strings.Repeat("a", 256), "too long"},
		{"leading dash", "-upload-pack=evil", "must not start with '-'"},
		{"option flag", "--branch", "must not start with '-'"},
		{"space", "a b", "disallowed char"},
		{"nul", "a\x00b", "disallowed char"},
		{"backtick", "a`b", "disallowed char"},
		{"dollar", "a$b", "disallowed char"},
		{"dotdot", "a..b", "must not contain '..'"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateGitRef(c.ref)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("validateGitRef(%q) = %v, want nil", c.ref, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("validateGitRef(%q) = %v, want substring %q", c.ref, err, c.wantErr)
			}
		})
	}
}

func TestPathMatchesAny(t *testing.T) {
	t.Parallel()
	cases := []struct {
		p     string
		globs []string
		want  bool
	}{
		{"a/SKILL.md", []string{"a/*"}, true},
		{"a/SKILL.md", []string{"b/*", "a/SKILL.md"}, true},
		{"a/b/SKILL.md", []string{"a/*"}, false}, // filepath.Match: * does not cross separators
		{"SKILL.md", []string{"*.md"}, true},
		{"a/SKILL.md", nil, false},
		{"a/SKILL.md", []string{"[invalid"}, false}, // bad pattern is ignored, not an error
	}
	for _, c := range cases {
		if got := pathMatchesAny(c.p, c.globs); got != c.want {
			t.Errorf("pathMatchesAny(%q, %v) = %v, want %v", c.p, c.globs, got, c.want)
		}
	}
}

func TestBulkImport_RequestShapeErrors(t *testing.T) {
	imp := newCovImporter(t)
	ctx := context.Background()

	if _, err := imp.BulkImport(ctx, BulkImportRequest{}); err == nil ||
		!strings.Contains(err.Error(), "requires git_url or local_path") {
		t.Fatalf("empty request: got %v, want 'requires git_url or local_path'", err)
	}
	_, err := imp.BulkImport(ctx, BulkImportRequest{GitURL: "https://github.com/o/r", LocalPath: "/tmp/x"})
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("both sources: got %v, want 'not both'", err)
	}
}

func TestBulkImport_LocalWalk_MixedTree(t *testing.T) {
	imp := newCovImporter(t)
	root := t.TempDir()

	writeSkill(t, root, "good/SKILL.md", skillMD("bulk-good-skill", "MIT"))
	writeSkill(t, root, "broken/SKILL.md", "no frontmatter at all")
	writeSkill(t, root, "gpl/SKILL.md", skillMD("bulk-gpl-skill", "GPL-3.0"))
	writeSkill(t, root, "big/SKILL.md", strings.Repeat("x", int(maxSkillFileBytes)+1))
	// Hidden in pruned directories — must never be found.
	writeSkill(t, root, "node_modules/pkg/SKILL.md", skillMD("bulk-hidden-nm", "MIT"))
	writeSkill(t, root, ".git/SKILL.md", skillMD("bulk-hidden-git", "MIT"))
	writeSkill(t, root, "vendor/dep/SKILL.md", skillMD("bulk-hidden-vendor", "MIT"))
	// Non-SKILL.md files are ignored.
	writeSkill(t, root, "good/README.md", "# readme")

	res, err := imp.BulkImport(context.Background(), BulkImportRequest{LocalPath: root})
	if err != nil {
		t.Fatalf("BulkImport: %v", err)
	}
	if res.Source != root {
		t.Errorf("Source = %q, want %q", res.Source, root)
	}
	if res.TotalFound != 4 {
		t.Errorf("TotalFound = %d, want 4", res.TotalFound)
	}
	if res.TotalImported != 1 || len(res.Skills) != 1 {
		t.Fatalf("TotalImported = %d (skills %d), want 1", res.TotalImported, len(res.Skills))
	}
	if res.Truncated {
		t.Error("Truncated = true, want false")
	}
	if got := res.Skills[0].Slug; got != "bulk-good-skill" {
		t.Errorf("imported slug = %q, want bulk-good-skill", got)
	}

	reasonByPath := map[string]string{}
	for _, s := range res.Skipped {
		reasonByPath[s.Path] = s.Reason
	}
	if r := reasonByPath["broken/SKILL.md"]; !strings.Contains(r, "parse:") {
		t.Errorf("broken skip reason = %q, want parse:", r)
	}
	if r := reasonByPath["gpl/SKILL.md"]; !strings.Contains(r, "license") {
		t.Errorf("gpl skip reason = %q, want license mention", r)
	}
	if r := reasonByPath["big/SKILL.md"]; !strings.Contains(r, "exceeds") {
		t.Errorf("big skip reason = %q, want size mention", r)
	}

	// Default vendor for local imports is "local"; homepage records the source.
	var vendor, homepage string
	err = imp.db.QueryRow("SELECT vendor, homepage FROM skills WHERE slug = ?", "bulk-good-skill").
		Scan(&vendor, &homepage)
	if err != nil {
		t.Fatalf("query imported row: %v", err)
	}
	if vendor != "local" {
		t.Errorf("vendor = %q, want local", vendor)
	}
	if homepage != root {
		t.Errorf("homepage = %q, want %q", homepage, root)
	}
}

func TestBulkImport_AllowUnsafeLicense(t *testing.T) {
	imp := newCovImporter(t)
	root := t.TempDir()
	writeSkill(t, root, "gpl/SKILL.md", skillMD("bulk-unsafe-skill", "GPL-3.0"))

	res, err := imp.BulkImport(context.Background(), BulkImportRequest{
		LocalPath:          root,
		AllowUnsafeLicense: true,
	})
	if err != nil {
		t.Fatalf("BulkImport: %v", err)
	}
	if res.TotalImported != 1 {
		t.Fatalf("TotalImported = %d, want 1 (skipped: %+v)", res.TotalImported, res.Skipped)
	}
}

func TestBulkImport_VendorOverride(t *testing.T) {
	imp := newCovImporter(t)
	root := t.TempDir()
	writeSkill(t, root, "a/SKILL.md", skillMD("bulk-vendored-skill", "MIT"))

	res, err := imp.BulkImport(context.Background(), BulkImportRequest{
		LocalPath: root,
		Vendor:    "acme",
	})
	if err != nil || res.TotalImported != 1 {
		t.Fatalf("BulkImport: imported=%d err=%v", res.TotalImported, err)
	}
	var vendor string
	if err := imp.db.QueryRow("SELECT vendor FROM skills WHERE slug = ?", "bulk-vendored-skill").Scan(&vendor); err != nil {
		t.Fatalf("query: %v", err)
	}
	if vendor != "acme" {
		t.Errorf("vendor = %q, want acme", vendor)
	}
}

func TestBulkImport_DryRun(t *testing.T) {
	imp := newCovImporter(t)
	root := t.TempDir()
	writeSkill(t, root, "a/SKILL.md", skillMD("bulk-dry-skill", "MIT"))

	res, err := imp.BulkImport(context.Background(), BulkImportRequest{LocalPath: root, DryRun: true})
	if err != nil {
		t.Fatalf("BulkImport: %v", err)
	}
	if res.TotalImported != 0 || len(res.Skipped) != 1 {
		t.Fatalf("imported=%d skipped=%d, want 0/1", res.TotalImported, len(res.Skipped))
	}
	if res.Skipped[0].Reason != "dry-run" {
		t.Errorf("reason = %q, want dry-run", res.Skipped[0].Reason)
	}
	if res.Skipped[0].Slug != "bulk-dry-skill" {
		t.Errorf("slug = %q, want bulk-dry-skill", res.Skipped[0].Slug)
	}
	var n int
	if err := imp.db.QueryRow("SELECT COUNT(*) FROM skills WHERE slug = ?", "bulk-dry-skill").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("dry-run wrote %d rows, want 0", n)
	}
}

func TestBulkImport_PathsFilter(t *testing.T) {
	imp := newCovImporter(t)
	root := t.TempDir()
	writeSkill(t, root, "a/SKILL.md", skillMD("bulk-filter-a", "MIT"))
	writeSkill(t, root, "b/SKILL.md", skillMD("bulk-filter-b", "MIT"))

	res, err := imp.BulkImport(context.Background(), BulkImportRequest{
		LocalPath: root,
		Paths:     []string{"a/*"},
	})
	if err != nil {
		t.Fatalf("BulkImport: %v", err)
	}
	// Filtered-out files do not count toward TotalFound.
	if res.TotalFound != 1 || res.TotalImported != 1 {
		t.Fatalf("found=%d imported=%d, want 1/1", res.TotalFound, res.TotalImported)
	}
	if res.Skills[0].Slug != "bulk-filter-a" {
		t.Errorf("slug = %q, want bulk-filter-a", res.Skills[0].Slug)
	}
}

func TestBulkImport_Truncated(t *testing.T) {
	imp := newCovImporter(t)
	root := t.TempDir()
	// maxBulkSkills+1 unparseable files: the cap check runs before
	// parse, so empty bodies keep the test cheap while still tripping
	// the truncation guard.
	for i := 0; i <= maxBulkSkills; i++ {
		writeSkill(t, root, filepath.Join("d", fmt.Sprintf("s%03d", i), "SKILL.md"), "")
	}
	res, err := imp.BulkImport(context.Background(), BulkImportRequest{LocalPath: root})
	if err != nil {
		t.Fatalf("BulkImport: %v", err)
	}
	if !res.Truncated {
		t.Fatal("Truncated = false, want true")
	}
	if res.TotalFound != maxBulkSkills+1 {
		t.Errorf("TotalFound = %d, want %d", res.TotalFound, maxBulkSkills+1)
	}
}

func TestBulkImport_UpsertErrorIsSkipped(t *testing.T) {
	imp := newCovImporter(t)
	// Seed a BUNDLED row so the per-skill upsert refuses the overwrite.
	_, err := imp.db.Exec(`INSERT INTO skills (id, name, slug, display_name, source, category, content,
			credential_requirements, tags, version, scan_status)
		VALUES ('sk_bundled_cov', 'Bundled Cov', 'bulk-bundled-skill', 'Bundled Cov', 'BUNDLED', 'CUSTOM', 'x',
			'[]', '[]', '1.0.0', 'CLEAN')`)
	if err != nil {
		t.Fatalf("seed bundled row: %v", err)
	}

	root := t.TempDir()
	writeSkill(t, root, "a/SKILL.md", skillMD("bulk-bundled-skill", "MIT"))

	res, err := imp.BulkImport(context.Background(), BulkImportRequest{LocalPath: root})
	if err != nil {
		t.Fatalf("BulkImport: %v", err)
	}
	if res.TotalImported != 0 || len(res.Skipped) != 1 {
		t.Fatalf("imported=%d skipped=%d, want 0/1", res.TotalImported, len(res.Skipped))
	}
	if r := res.Skipped[0].Reason; !strings.Contains(r, "upsert:") || !strings.Contains(r, "BUNDLED") {
		t.Errorf("reason = %q, want upsert + BUNDLED mention", r)
	}
}

func TestBulkImport_GitRequiresGitInPath(t *testing.T) {
	imp := newCovImporter(t)
	t.Setenv("PATH", t.TempDir()) // empty dir — no git binary

	_, err := imp.BulkImport(context.Background(), BulkImportRequest{GitURL: "https://github.com/o/r.git"})
	if err == nil || !strings.Contains(err.Error(), "requires `git` in PATH") {
		t.Fatalf("got %v, want 'requires `git` in PATH'", err)
	}
}

// installFakeGit puts a stub `git` first in PATH so the clone branch can
// be exercised hermetically (no network). The script's behaviour is the
// given shell body; the clone destination directory is the last argv.
func installFakeGit(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nfor a in \"$@\"; do dest=\"$a\"; done\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
}

func TestBulkImport_GitURLValidationError(t *testing.T) {
	imp := newCovImporter(t)
	installFakeGit(t, "exit 0")

	_, err := imp.BulkImport(context.Background(), BulkImportRequest{GitURL: "http://github.com/o/r.git"})
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("got %v, want https-only error", err)
	}
}

func TestBulkImport_GitRefValidationError(t *testing.T) {
	imp := newCovImporter(t)
	installFakeGit(t, "exit 0")

	_, err := imp.BulkImport(context.Background(), BulkImportRequest{
		GitURL: "https://github.com/o/r.git",
		GitRef: "--upload-pack=evil",
	})
	if err == nil || !strings.Contains(err.Error(), "must not start with '-'") {
		t.Fatalf("got %v, want ref validation error", err)
	}
}

func TestBulkImport_GitCloneFailure(t *testing.T) {
	imp := newCovImporter(t)
	installFakeGit(t, "echo boom >&2\nexit 1")

	_, err := imp.BulkImport(context.Background(), BulkImportRequest{GitURL: "https://github.com/o/r.git"})
	if err == nil || !strings.Contains(err.Error(), "git clone failed") {
		t.Fatalf("got %v, want 'git clone failed'", err)
	}
	// The error must reference the (redacted) URL, never raw output.
	if !strings.Contains(err.Error(), "https://github.com/o/r.git") {
		t.Errorf("error %q should name the redacted clone URL", err)
	}
	if strings.Contains(err.Error(), "boom") {
		t.Errorf("error %q leaked git output", err)
	}
}

func TestBulkImport_GitCloneSuccess(t *testing.T) {
	imp := newCovImporter(t)
	installFakeGit(t, `cat > "$dest/SKILL.md" <<'EOF'
---
name: bulk-git-skill
license: MIT
description: Cloned by the fake git stub in tests.
---
# Git Skill
Instructions.
EOF`)

	res, err := imp.BulkImport(context.Background(), BulkImportRequest{
		GitURL: "https://github.com/o/r.git",
		GitRef: "main",
	})
	if err != nil {
		t.Fatalf("BulkImport: %v", err)
	}
	if res.TotalImported != 1 || len(res.Skills) != 1 {
		t.Fatalf("imported=%d, want 1 (skipped: %+v)", res.TotalImported, res.Skipped)
	}
	if res.Source != "https://github.com/o/r.git" {
		t.Errorf("Source = %q, want redacted clone URL", res.Source)
	}
	// Git imports default the vendor to "community".
	var vendor string
	if err := imp.db.QueryRow("SELECT vendor FROM skills WHERE slug = ?", "bulk-git-skill").Scan(&vendor); err != nil {
		t.Fatalf("query: %v", err)
	}
	if vendor != "community" {
		t.Errorf("vendor = %q, want community", vendor)
	}
}
