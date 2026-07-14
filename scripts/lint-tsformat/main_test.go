package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// --- Core proximity logic: no git involved. ---

func TestFindProximityHits_Positive(t *testing.T) {
	t.Parallel()
	// RFC3339Nano formatted a couple of lines above an ExecContext call —
	// the exact broken pattern #990/#1073 is about.
	src := `package foo

func write(db *sql.DB, t time.Time) error {
	ts := t.UTC().Format(time.RFC3339Nano)
	_, err := db.ExecContext(ctx, "UPDATE runs SET ended_at = ? WHERE id = ?", ts, id)
	return err
}
`
	hits := findProximityHits(strings.Split(src, "\n"))
	if len(hits) == 0 {
		t.Fatalf("expected at least one proximity hit, got none")
	}
	h := hits[0]
	if h.sqlCall != "ExecContext" {
		t.Errorf("sqlCall = %q, want ExecContext", h.sqlCall)
	}
	if h.rfcLine >= h.sqlLine {
		t.Errorf("rfcLine (%d) should precede sqlLine (%d) in this fixture", h.rfcLine, h.sqlLine)
	}
}

func TestFindProximityHits_Negative_FarApart(t *testing.T) {
	t.Parallel()
	// RFC3339Nano used far (> window lines) from the nearest SQL call —
	// should NOT be flagged.
	var b strings.Builder
	b.WriteString("package foo\n\n")
	b.WriteString("func parseOnly(s string) (time.Time, error) {\n")
	b.WriteString("\treturn time.Parse(time.RFC3339Nano, s)\n")
	b.WriteString("}\n\n")
	for i := 0; i < window+10; i++ {
		b.WriteString("// padding line to push the SQL call out of the proximity window\n")
	}
	b.WriteString("func write(db *sql.DB) error {\n")
	b.WriteString("\t_, err := db.ExecContext(ctx, \"SELECT 1\")\n")
	b.WriteString("\treturn err\n")
	b.WriteString("}\n")

	hits := findProximityHits(strings.Split(b.String(), "\n"))
	if len(hits) != 0 {
		t.Fatalf("expected no hits for far-apart occurrences, got %+v", hits)
	}
}

func TestFindProximityHits_Negative_TsformatCallNearby(t *testing.T) {
	t.Parallel()
	// tsformat.Format(...) near an ExecContext call is the CORRECT
	// pattern — it must not trip the lint (tsformat.Format never
	// contains the literal token "RFC3339Nano").
	src := `package foo

func write(db *sql.DB, t time.Time) error {
	ts := tsformat.Format(t)
	_, err := db.ExecContext(ctx, "UPDATE runs SET ended_at = ? WHERE id = ?", ts, id)
	return err
}
`
	hits := findProximityHits(strings.Split(src, "\n"))
	if len(hits) != 0 {
		t.Fatalf("expected no hits when using tsformat.Format, got %+v", hits)
	}
}

func TestFindProximityHits_AllowComment_Suppresses(t *testing.T) {
	t.Parallel()
	src := `package foo

func write(db *sql.DB, t time.Time) error {
	ts := t.UTC().Format(time.RFC3339Nano) // tsformat:allow: read-only diagnostic log, never compared in SQL
	_, err := db.ExecContext(ctx, "INSERT INTO audit_log(ts) VALUES(?)", ts)
	return err
}
`
	hits := findProximityHits(strings.Split(src, "\n"))
	if len(hits) != 0 {
		t.Fatalf("expected allowComment to suppress the hit, got %+v", hits)
	}
}

func TestCheckFile_FiltersByAddedLines(t *testing.T) {
	t.Parallel()
	src := `package foo

func write(db *sql.DB, t time.Time) error {
	ts := t.UTC().Format(time.RFC3339Nano)
	_, err := db.ExecContext(ctx, "UPDATE runs SET ended_at = ? WHERE id = ?", ts, id)
	return err
}
`
	// Neither line touched by this diff -> no violation reported, even
	// though the raw proximity hit exists (mirrors "pre-existing debt,
	// left to #1073a/#1073b").
	if got := checkFile("x.go", []byte(src), map[int]bool{1: true}); len(got) != 0 {
		t.Fatalf("expected no violations when neither line was added, got %+v", got)
	}

	// The RFC3339Nano line (4) was added by this diff -> violation.
	got := checkFile("x.go", []byte(src), map[int]bool{4: true})
	if len(got) == 0 {
		t.Fatalf("expected a violation when the RFC3339Nano line was added")
	}

	// nil addedLines (--full mode) always reports the raw hit.
	if got := checkFile("x.go", []byte(src), nil); len(got) == 0 {
		t.Fatalf("expected a violation in full-tree mode regardless of addedLines")
	}
}

// --- Git-plumbing integration test: real throwaway repo in t.TempDir(). ---

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// TestIntegration_OnlyFlagsNewAdditions builds a tiny repo with a "main"
// branch that already contains an RFC3339Nano/ExecContext pair (simulating
// today's pre-#1073a/b debt), then a feature branch that adds a SECOND,
// brand-new offending pair elsewhere in the same file. The lint must
// report ONLY the new pair, proving the diff-scoping keeps the tool green
// on pre-existing code while still catching new regressions.
func TestIntegration_OnlyFlagsNewAdditions(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "test")

	if err := os.MkdirAll(filepath.Join(dir, "internal", "widget"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "internal", "widget", "store.go")

	base := `package widget

func writeOld(db *sql.DB, t time.Time) error {
	ts := t.UTC().Format(time.RFC3339Nano)
	_, err := db.ExecContext(ctx, "UPDATE old SET t = ?", ts)
	return err
}
`
	if err := os.WriteFile(target, []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "base")
	runGit(t, dir, "branch", "-q", "-m", "main") // rename current branch to "main"

	runGit(t, dir, "checkout", "-q", "-b", "feature")
	// Pad with more than `window` lines so writeNew's pair doesn't
	// cross-pair with writeOld's — this test is about scoping to added
	// lines, not the proximity radius itself (covered by
	// TestFindProximityHits_* above).
	var padded strings.Builder
	padded.WriteString(base)
	padded.WriteString("\n")
	for i := 0; i < window+5; i++ {
		padded.WriteString("// padding to keep writeNew out of writeOld's proximity window\n")
	}
	padded.WriteString(`
func writeNew(db *sql.DB, t time.Time) error {
	ts := t.UTC().Format(time.RFC3339Nano)
	_, err := db.ExecContext(ctx, "UPDATE new SET t = ?", ts)
	return err
}
`)
	withNewOffense := padded.String()
	if err := os.WriteFile(target, []byte(withNewOffense), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "add writeNew")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	baseSHA, err := mergeBase("main")
	if err != nil {
		t.Fatalf("mergeBase: %v", err)
	}

	files, err := changedGoFiles(baseSHA)
	if err != nil {
		t.Fatalf("changedGoFiles: %v", err)
	}
	if len(files) != 1 || files[0] != filepath.Join("internal", "widget", "store.go") {
		t.Fatalf("changedGoFiles = %v, want [internal/widget/store.go]", files)
	}

	added, err := addedLinesFromDiff(baseSHA, files[0])
	if err != nil {
		t.Fatalf("addedLinesFromDiff: %v", err)
	}

	content, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	violations := checkFile(files[0], content, added)

	if len(violations) != 1 {
		t.Fatalf("got %d violations, want exactly 1 (only the new writeNew pair): %+v", len(violations), violations)
	}
	lines := strings.Split(string(content), "\n")
	if violations[0].rfcLine < 1 || violations[0].rfcLine > len(lines) {
		t.Fatalf("sanity: violation rfcLine %d out of range (file has %d lines)", violations[0].rfcLine, len(lines))
	}
	// The flagged RFC3339Nano line must be inside writeNew, not writeOld.
	newFuncStart := strings.Index(withNewOffense, "func writeNew")
	newFuncStartLine := strings.Count(withNewOffense[:newFuncStart], "\n") + 1
	if violations[0].rfcLine <= newFuncStartLine {
		t.Fatalf("flagged rfcLine %d looks like it's inside writeOld, not writeNew (writeNew starts at line %d)",
			violations[0].rfcLine, newFuncStartLine)
	}
}
