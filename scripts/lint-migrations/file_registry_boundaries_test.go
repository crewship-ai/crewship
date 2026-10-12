package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileMigrationsProtectShippedSQLAndPermitAppend(t *testing.T) {
	dir := t.TempDir()
	const first = fileMigrationRoot + "/20260101000000_first.sql"
	const second = fileMigrationRoot + "/20260102000000_second.sql"
	initRepo(t, dir, map[string]string{migrationsFile: baseMigrateGo, first: "CREATE TABLE one(id TEXT);\n", second: "CREATE TABLE two(id TEXT);\n", fileMigrationRoot + "/README.md": "not a migration"})
	t.Chdir(dir)
	base, err := listBaseMigrationFiles("HEAD")
	if err != nil || len(base) != 2 || string(base[first]) != "CREATE TABLE one(id TEXT);\n" {
		t.Fatalf("base registry: %v %v", base, err)
	}
	if err := os.WriteFile(fileMigrationRoot+"/20260103000000_third.sql", []byte("CREATE TABLE three(id TEXT);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if findings, err := checkFileMigrations("HEAD"); err != nil || len(findings) != 0 {
		t.Fatalf("append rejected: %v %v", findings, err)
	}
	if err := os.WriteFile(first, []byte("CREATE TABLE one(id INTEGER);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(second); err != nil {
		t.Fatal(err)
	}
	findings, err := checkFileMigrations("HEAD")
	if err != nil || len(findings) != 2 {
		t.Fatalf("drift findings: %v %v", findings, err)
	}
	if !strings.Contains(findings[0], first+" CHANGED") || !strings.Contains(findings[0], "base sha256="+shortSum(base[first])) || !strings.Contains(findings[1], second+" was REMOVED") {
		t.Fatalf("incomplete or unsorted diagnostics: %v", findings)
	}
	code, stderr := runLintHelper(t, dir)
	if code != 1 || !strings.Contains(stderr, "2 violation(s)") || !strings.Contains(stderr, "CHANGED") || !strings.Contains(stderr, "REMOVED") {
		t.Fatalf("CLI missed file registry drift: %d %s", code, stderr)
	}
}

func TestFileMigrationsDistinguishInvalidRefAndUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	const file = fileMigrationRoot + "/20260101000000_first.sql"
	initRepo(t, dir, map[string]string{migrationsFile: baseMigrateGo, file: "SELECT 1;"})
	t.Chdir(dir)
	if _, err := listBaseMigrationFiles("missing-fixture-ref"); err == nil || !strings.Contains(err.Error(), "git ls-tree") {
		t.Fatalf("invalid base silently accepted: %v", err)
	}
	if _, err := checkFileMigrations("missing-fixture-ref"); err == nil {
		t.Fatal("invalid reference accepted by registry check")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(file, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := checkFileMigrations("HEAD"); err == nil || !strings.Contains(err.Error(), "read "+file) {
		t.Fatalf("unreadable SQL treated as removal: %v", err)
	}
	code, stderr := runLintHelper(t, dir)
	if code != 2 || !strings.Contains(stderr, "check file migrations:") {
		t.Fatalf("CLI storage failure: %d %s", code, stderr)
	}
}

func TestMigrationSourcesIgnoreBrokenSiblingButKeepRegistry(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "internal/database"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("absent", filepath.Join(dir, "internal/database/missing.go")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	got := loadSiblingSources([]byte(baseMigrateGo))
	if len(got) != 1 || string(got[migrationsFile]) != baseMigrateGo {
		t.Fatalf("unreadable sibling replaced registry: %v", got)
	}
}
