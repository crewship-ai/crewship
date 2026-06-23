package main

// Coverage tests for the migration-drift linter. The git-facing paths
// run against throwaway repos created in t.TempDir() — never against
// the real working tree. Paths that call os.Exit are exercised by
// re-executing this test binary (standard helper-process pattern).

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const baseMigrateGo = `package database

var migrations = []migration{
	{version: 1, name: "init", sql: migrationInit},
	{version: 2, name: "add_flags", fn: migrationAddFlags},
	{version: 3, name: "inline_only"},
}

const migrationInit = ` + "`CREATE TABLE crews (id TEXT PRIMARY KEY);`" + `

func migrationAddFlags(db something) error {
	if db == nil {
		return errNil
	}
	return db.exec("ALTER TABLE crews ADD COLUMN flags TEXT")
}
`

const siblingGo = `package database

var migrationSibling = ` + "`CREATE TABLE wake_gates (id TEXT);`" + `
`

func TestParse_Entries(t *testing.T) {
	t.Parallel()
	got, err := parse([]byte(baseMigrateGo))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("parsed %d entries, want 3: %+v", len(got), got)
	}
	if e := got[1]; e.name != "init" || e.refName != "migrationInit" {
		t.Errorf("v1 = %+v, want init/migrationInit", e)
	}
	if e := got[2]; e.name != "add_flags" || e.refName != "migrationAddFlags" {
		t.Errorf("v2 = %+v, want add_flags/migrationAddFlags", e)
	}
	if e := got[3]; e.name != "inline_only" || e.refName != "" {
		t.Errorf("v3 = %+v, want inline_only with empty ref", e)
	}
}

func TestParse_DuplicateVersion(t *testing.T) {
	t.Parallel()
	src := `{version: 7, name: "a"},
{version: 7, name: "b"},`
	_, err := parse([]byte(src))
	if err == nil || !strings.Contains(err.Error(), "duplicate version 7") {
		t.Fatalf("got %v, want duplicate version error", err)
	}
}

func TestParse_VersionOverflow(t *testing.T) {
	t.Parallel()
	src := `{version: 99999999999999999999, name: "huge"},`
	_, err := parse([]byte(src))
	if err == nil || !strings.Contains(err.Error(), "parse version") {
		t.Fatalf("got %v, want parse version error", err)
	}
}

func TestFindBody(t *testing.T) {
	t.Parallel()
	sources := map[string][]byte{
		"a.go": []byte(baseMigrateGo),
		"b.go": []byte(siblingGo),
	}

	if got := findBody("migrationInit", sources); string(got) != "CREATE TABLE crews (id TEXT PRIMARY KEY);" {
		t.Errorf("const body = %q", got)
	}
	if got := findBody("migrationSibling", sources); string(got) != "CREATE TABLE wake_gates (id TEXT);" {
		t.Errorf("sibling var body = %q", got)
	}
	fn := findBody("migrationAddFlags", sources)
	if fn == nil || !bytes.HasPrefix(fn, []byte("{")) || !bytes.HasSuffix(fn, []byte("}")) {
		t.Fatalf("func body = %q, want brace-balanced block", fn)
	}
	if !bytes.Contains(fn, []byte("ALTER TABLE crews")) {
		t.Errorf("func body %q missing inner statement", fn)
	}
	// Nested braces must be balanced — the inner if-block stays inside.
	if !bytes.Contains(fn, []byte("return errNil")) {
		t.Errorf("func body %q lost nested block", fn)
	}
	if got := findBody("doesNotExist", sources); got != nil {
		t.Errorf("missing symbol returned %q, want nil", got)
	}
	// `func name(` present but no opening brace anywhere after it.
	truncated := map[string][]byte{"t.go": []byte("func brokenDecl(x int) error")}
	if got := findBody("brokenDecl", truncated); got != nil {
		t.Errorf("brace-less func returned %q, want nil", got)
	}
}

func TestFingerprint(t *testing.T) {
	t.Parallel()
	e := entry{version: 1, name: "init", refName: "migrationInit"}
	src := map[string][]byte{"a.go": []byte(baseMigrateGo)}

	fp1 := fingerprint(e, src)
	fp2 := fingerprint(e, src)
	if fp1 != fp2 {
		t.Fatal("fingerprint not deterministic")
	}
	if len(fp1) != 64 {
		t.Fatalf("fingerprint length %d, want 64 hex chars", len(fp1))
	}

	// Editing the referenced body changes the fingerprint.
	edited := map[string][]byte{
		"a.go": []byte(strings.Replace(baseMigrateGo, "PRIMARY KEY", "NOT NULL", 1)),
	}
	if fingerprint(e, edited) == fp1 {
		t.Error("body edit did not change fingerprint")
	}

	// Renaming the referenced symbol changes the fingerprint even when
	// the body cannot be resolved.
	renamed := entry{version: 1, name: "init", refName: "migrationInitV2"}
	if fingerprint(renamed, src) == fp1 {
		t.Error("ref rename did not change fingerprint")
	}

	// Entries with no ref still produce a stable identity hash.
	bare := entry{version: 3, name: "inline_only"}
	if fingerprint(bare, src) != fingerprint(bare, map[string][]byte{}) {
		t.Error("ref-less fingerprint should not depend on sources")
	}
}

// initRepo creates a git repo in dir whose HEAD commit contains files.
func initRepo(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("-c", "user.email=test@crewship.test", "-c", "user.name=cov-test", "add", "-A")
	run("-c", "user.email=test@crewship.test", "-c", "user.name=cov-test",
		"-c", "commit.gpgsign=false", "commit", "-q", "-m", "base")
}

func TestLoadSiblingSources(t *testing.T) {
	dir := t.TempDir()
	dbDir := filepath.Join(dir, "internal", "database")
	if err := os.MkdirAll(filepath.Join(dbDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("internal/database/migrate.go", baseMigrateGo)
	mustWrite("internal/database/migrate_extra.go", siblingGo)
	mustWrite("internal/database/notes.txt", "not go")
	mustWrite("internal/database/sub/nested.go", "package sub")
	t.Chdir(dir)

	head := []byte(baseMigrateGo)
	got := loadSiblingSources(head)
	if string(got[migrationsFile]) != baseMigrateGo {
		t.Error("migrate.go content not carried through")
	}
	if string(got[filepath.Join("internal", "database", "migrate_extra.go")]) != siblingGo {
		t.Errorf("sibling .go file not loaded; keys: %v", keys(got))
	}
	if len(got) != 2 {
		t.Errorf("loaded %d files (%v), want exactly migrate.go + sibling", len(got), keys(got))
	}
}

func TestLoadSiblingSources_DirMissingFallsBack(t *testing.T) {
	t.Chdir(t.TempDir()) // no internal/database here
	head := []byte("head-bytes")
	got := loadSiblingSources(head)
	if len(got) != 1 || string(got[migrationsFile]) != "head-bytes" {
		t.Fatalf("fallback map = %v, want only migrationsFile", keys(got))
	}
}

func TestLoadBaseSources(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir, map[string]string{
		"internal/database/migrate.go": baseMigrateGo,
	})
	// A sibling that exists only in the working tree, not on HEAD.
	if err := os.WriteFile(filepath.Join(dir, "internal", "database", "migrate_extra.go"), []byte(siblingGo), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	headSources := loadSiblingSources([]byte(baseMigrateGo))
	got := loadBaseSources("HEAD", headSources)
	if string(got[migrationsFile]) != baseMigrateGo {
		t.Errorf("base migrate.go missing; keys: %v", keys(got))
	}
	if _, ok := got[filepath.Join("internal", "database", "migrate_extra.go")]; ok {
		t.Error("file absent on base ref must be skipped")
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// captureStdout redirects os.Stdout for the duration of fn.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()

	outCh := make(chan string, 1)
	go func() {
		defer r.Close()
		b, _ := io.ReadAll(r)
		outCh <- string(b)
	}()
	fn()
	w.Close()
	return <-outCh
}

func TestMain_OKWithAddedMigration(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir, map[string]string{
		"internal/database/migrate.go": baseMigrateGo,
	})
	// Append a brand-new migration in the working tree — allowed.
	head := strings.Replace(baseMigrateGo,
		"{version: 3, name: \"inline_only\"},",
		"{version: 3, name: \"inline_only\"},\n\t{version: 4, name: \"wake_gates\"},", 1)
	if err := os.WriteFile(filepath.Join(dir, "internal", "database", "migrate.go"), []byte(head), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	oldArgs := os.Args
	os.Args = []string{"lint-migrations", "HEAD"}
	defer func() { os.Args = oldArgs }()

	out := captureStdout(t, main)
	if !strings.Contains(out, "migration-lint: ok") {
		t.Fatalf("stdout = %q, want ok line", out)
	}
	if !strings.Contains(out, "3 migrations on HEAD, 1 added") {
		t.Errorf("stdout = %q, want 3 base migrations and 1 added", out)
	}
}

func TestMain_SkipsWhenFileNotOnBase(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir, map[string]string{"README.md": "no migrations yet"})
	// Working tree gains the file after the base commit.
	if err := os.MkdirAll(filepath.Join(dir, "internal", "database"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "database", "migrate.go"), []byte(baseMigrateGo), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	oldArgs := os.Args
	os.Args = []string{"lint-migrations", "HEAD"}
	defer func() { os.Args = oldArgs }()

	out := captureStdout(t, main)
	if !strings.Contains(out, "not present in HEAD, skipping") {
		t.Fatalf("stdout = %q, want skip message", out)
	}
}

// TestLintMigrationsHelper is not a real test: it is the re-exec target
// for the os.Exit paths below. It runs main() inside the directory given
// by CREWSHIP_LINT_DIR and lets main's os.Exit terminate the process.
func TestLintMigrationsHelper(t *testing.T) {
	dir := os.Getenv("CREWSHIP_LINT_DIR")
	if dir == "" {
		t.Skip("helper process only")
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	os.Args = []string{"lint-migrations", "HEAD"}
	main()
}

// runLintHelper re-executes the test binary so main's os.Exit codes can
// be observed without killing the parent test process.
func runLintHelper(t *testing.T, dir string) (exitCode int, stderr string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestLintMigrationsHelper$", "-test.v=false")
	cmd.Env = append(os.Environ(), "CREWSHIP_LINT_DIR="+dir)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err == nil {
		return 0, errBuf.String()
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), errBuf.String()
	}
	t.Fatalf("helper run failed without exit code: %v", err)
	return -1, ""
}

func TestMain_ViolationsExitOne(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir, map[string]string{
		"internal/database/migrate.go": baseMigrateGo,
	})
	// Three flavours of drift at once:
	//  - v1 keeps version+name but the referenced SQL body changes
	//  - v2 is renamed
	//  - v3 is removed entirely
	head := baseMigrateGo
	head = strings.Replace(head, "PRIMARY KEY", "NOT NULL", 1)                                          // body edit
	head = strings.Replace(head, `{version: 2, name: "add_flags"`, `{version: 2, name: "add_flag2"`, 1) // rename
	head = strings.Replace(head, "\t{version: 3, name: \"inline_only\"},\n", "", 1)                     // removal
	if err := os.WriteFile(filepath.Join(dir, "internal", "database", "migrate.go"), []byte(head), 0o644); err != nil {
		t.Fatal(err)
	}

	code, stderr := runLintHelper(t, dir)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "3 violation(s)") {
		t.Errorf("stderr = %q, want 3 violations", stderr)
	}
	if !strings.Contains(stderr, "BODY CHANGED") {
		t.Errorf("stderr missing BODY CHANGED finding:\n%s", stderr)
	}
	if !strings.Contains(stderr, "RENAMED") {
		t.Errorf("stderr missing RENAMED finding:\n%s", stderr)
	}
	if !strings.Contains(stderr, "REMOVED") {
		t.Errorf("stderr missing REMOVED finding:\n%s", stderr)
	}
}

func TestMain_MissingWorkingFileExitTwo(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir, map[string]string{"README.md": "empty"})
	// No internal/database/migrate.go in the working tree at all.
	code, stderr := runLintHelper(t, dir)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "read internal/database/migrate.go") {
		t.Errorf("stderr = %q, want read error mention", stderr)
	}
}

func TestMain_DuplicateVersionOnHeadExitTwo(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir, map[string]string{
		"internal/database/migrate.go": baseMigrateGo,
	})
	dup := baseMigrateGo + "\nvar more = []migration{\n\t{version: 1, name: \"dup\"},\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "internal", "database", "migrate.go"), []byte(dup), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stderr := runLintHelper(t, dir)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "parse HEAD") || !strings.Contains(stderr, "duplicate version 1") {
		t.Errorf("stderr = %q, want parse HEAD duplicate error", stderr)
	}
}
