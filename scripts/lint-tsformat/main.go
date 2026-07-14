// Command lint-tsformat is a proximity lint that keeps `internal/tsformat`
// writers honest (#990, #1073).
//
// internal/tsformat's own doc comment states the rule this enforces:
//
//	Rule of thumb: any `.Format(time.RFC3339Nano)` whose result lands in
//	a SQL comparison or ORDER BY belongs here instead.
//
// time.RFC3339Nano truncates trailing zeros in the fractional seconds, so
// two timestamps written with it inside the same second can render at
// different widths and string-compare in the wrong lexicographic order —
// exactly the class of bug #990 found in the quartermaster online sampler.
// tsformat.Format is the fixed-width, always-sortable replacement.
//
// This tool can't prove a given RFC3339Nano value actually flows into a
// SQL comparison (that needs real dataflow analysis), so it uses a cheap
// but effective heuristic instead: flag any `RFC3339Nano` token that
// appears within `window` lines of an `ExecContext(`/`QueryContext(` call
// in the same file. That's loose by design — it forces a reviewer (or the
// author) to either convert the write to tsformat.Format, or leave a
// `tsformat:allow: <reason>` comment on the RFC3339Nano line to justify it
// (e.g. genuinely a read-only time.Parse, or a value that never reaches a
// comparison).
//
// # Scope: new code only
//
// A repo-wide scan of `internal/` today (before #1073a/#1073b land their
// data fixes) turns up dozens of pre-existing hits — mostly legitimate
// time.Parse(time.RFC3339Nano, ...) reads, plus the known-bad writers those
// two PRs are converting. Baselining every one of them as an allowlist
// would make this PR's diff enormous and would need hand-editing every
// time a/b touch a listed line. Instead, this lint only reports hits
// where at least one side of the pair (the RFC3339Nano line or the
// Exec/QueryContext line) was ADDED relative to the base ref — i.e. it
// only judges code introduced by the current branch/PR. That keeps this
// PR green on the current tree independent of #1073a/#1073b merge order,
// while still catching any NEW regression to the broken pattern from this
// point forward. Existing debt is left to a/b; run with --full for a
// whole-tree audit (useful for those two PRs, not wired into CI).
//
// Usage:
//
//	go run ./scripts/lint-tsformat [base-ref]   # CI default: base-ref defaults to origin/main
//	go run ./scripts/lint-tsformat --full        # whole-tree audit, ignores base-ref/diff scoping
//
// Exits non-zero and lists offenders if any proximity hit is found.
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// window is the proximity threshold, in lines, used to associate an
// RFC3339Nano occurrence with a nearby SQL exec/query call. Chosen from
// surveying real offenders in this repo: the timestamp is almost always
// formatted either on the same line as the call, a few lines above while
// building args, or up to ~15 lines above when the format happens near
// the top of a short helper and the call trails it (e.g.
// internal/pipeline/store.go's Create/Update helpers). 20 lines comfortably
// covers "same function" without ballooning to whole-file scope.
const window = 20

// allowComment lets an author justify a hit inline instead of converting
// it, e.g.:
//
//	parsed, _ := time.Parse(time.RFC3339Nano, s) // tsformat:allow: read-only parse, not a SQL comparison
const allowComment = "tsformat:allow"

var (
	rfcRe = regexp.MustCompile(`RFC3339Nano`)
	sqlRe = regexp.MustCompile(`\b(ExecContext|QueryContext)\(`)
)

// hit is one proximity pairing between an RFC3339Nano line and a nearby
// SQL Exec/QueryContext line, found by findProximityHits.
type hit struct {
	rfcLine int // 1-based
	sqlLine int
	sqlCall string // "ExecContext" or "QueryContext"
}

// findProximityHits scans file lines for every RFC3339Nano occurrence
// paired with every ExecContext/QueryContext call within `window` lines,
// skipping any RFC3339Nano line that carries an allowComment.
func findProximityHits(lines []string) []hit {
	var rfcLines []int
	for i, l := range lines {
		if rfcRe.MatchString(l) && !strings.Contains(l, allowComment) {
			rfcLines = append(rfcLines, i+1)
		}
	}
	if len(rfcLines) == 0 {
		return nil
	}

	type sqlOcc struct {
		line int
		call string
	}
	var sqlLines []sqlOcc
	for i, l := range lines {
		if m := sqlRe.FindStringSubmatch(l); m != nil {
			sqlLines = append(sqlLines, sqlOcc{line: i + 1, call: m[1]})
		}
	}
	if len(sqlLines) == 0 {
		return nil
	}

	var hits []hit
	for _, rl := range rfcLines {
		for _, sl := range sqlLines {
			d := rl - sl.line
			if d < 0 {
				d = -d
			}
			if d <= window {
				hits = append(hits, hit{rfcLine: rl, sqlLine: sl.line, sqlCall: sl.call})
			}
		}
	}
	return hits
}

// violation is one reported offense: a proximity hit where at least one
// side was newly added relative to the base ref (or addedLines is nil,
// meaning "report everything" for --full mode).
type violation struct {
	file    string
	rfcLine int
	sqlLine int
	sqlCall string
}

// checkFile applies findProximityHits to content and filters to hits
// touching at least one line in addedLines. A nil addedLines disables
// filtering (used by --full and by direct unit tests).
func checkFile(path string, content []byte, addedLines map[int]bool) []violation {
	lines := strings.Split(string(content), "\n")
	var out []violation
	for _, h := range findProximityHits(lines) {
		if addedLines != nil && !addedLines[h.rfcLine] && !addedLines[h.sqlLine] {
			continue
		}
		out = append(out, violation{file: path, rfcLine: h.rfcLine, sqlLine: h.sqlLine, sqlCall: h.sqlCall})
	}
	return out
}

// hunkHeader matches a unified-diff hunk header, e.g. "@@ -12,3 +14,5 @@".
// The new-file start/count (group 3/4) are what we need to walk the
// added-line numbers below.
var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// addedLinesFromDiff runs `git diff --unified=0 <baseRef> -- <path>` and
// returns the set of new-file line numbers introduced or modified by
// that diff (i.e. every '+' line). Line numbers are 1-based, in the
// current (working-tree) file's coordinate space.
func addedLinesFromDiff(baseRef, path string) (map[int]bool, error) {
	cmd := exec.Command("git", "diff", "--unified=0", baseRef, "--", path)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git diff %s -- %s: %w\n%s", baseRef, path, err, stderr.String())
	}

	added := map[int]bool{}
	sc := bufio.NewScanner(&stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	newLine := 0
	inHunk := false
	for sc.Scan() {
		line := sc.Text()
		if m := hunkHeader.FindStringSubmatch(line); m != nil {
			n, err := strconv.Atoi(m[1])
			if err != nil {
				return nil, fmt.Errorf("parse hunk header %q: %w", line, err)
			}
			newLine = n
			inHunk = true
			continue
		}
		if !inHunk {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			// file header lines, never inside a hunk body but guard anyway
		case strings.HasPrefix(line, "+"):
			added[newLine] = true
			newLine++
		case strings.HasPrefix(line, "-"):
			// removed line: doesn't exist in the new file, don't advance
		case strings.HasPrefix(line, "\\"):
			// "\ No newline at end of file" — ignore
		default:
			// context line (only appears if unified>0; harmless to handle)
			newLine++
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan diff output: %w", err)
	}
	return added, nil
}

// changedGoFiles lists .go files under internal/ that differ between
// baseRef and the current working tree (committed + uncommitted).
func changedGoFiles(baseRef string) ([]string, error) {
	cmd := exec.Command("git", "diff", "--name-only", baseRef, "--", "internal")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git diff --name-only %s -- internal: %w\n%s", baseRef, err, stderr.String())
	}
	var out []string
	sc := bufio.NewScanner(&stdout)
	for sc.Scan() {
		p := strings.TrimSpace(sc.Text())
		if strings.HasSuffix(p, ".go") {
			out = append(out, p)
		}
	}
	return out, sc.Err()
}

// allGoFiles walks internal/ for every .go file, used by --full mode.
func allGoFiles() ([]string, error) {
	var out []string
	err := filepath.Walk("internal", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".go") {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}

func mergeBase(baseRef string) (string, error) {
	cmd := exec.Command("git", "merge-base", baseRef, "HEAD")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git merge-base %s HEAD: %w\n%s", baseRef, err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

func main() {
	full := len(os.Args) > 1 && os.Args[1] == "--full"

	baseRef := "origin/main"
	if len(os.Args) > 1 && !full {
		baseRef = os.Args[1]
	}

	var files []string
	var err error
	var base string // resolved merge-base, empty in --full mode

	if full {
		files, err = allGoFiles()
	} else {
		base, err = mergeBase(baseRef)
		if err != nil {
			fmt.Fprintf(os.Stderr, "lint-tsformat: %v\n", err)
			os.Exit(2)
		}
		files, err = changedGoFiles(base)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "lint-tsformat: %v\n", err)
		os.Exit(2)
	}

	var violations []violation
	scanned := 0
	for _, f := range files {
		content, rerr := os.ReadFile(f)
		if rerr != nil {
			// Deleted-in-this-diff files show up in git diff --name-only
			// but no longer exist on disk — nothing to lint.
			continue
		}
		scanned++

		var addedLines map[int]bool
		if !full {
			addedLines, err = addedLinesFromDiff(base, f)
			if err != nil {
				fmt.Fprintf(os.Stderr, "lint-tsformat: %v\n", err)
				os.Exit(2)
			}
		}
		violations = append(violations, checkFile(f, content, addedLines)...)
	}

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].file != violations[j].file {
			return violations[i].file < violations[j].file
		}
		return violations[i].rfcLine < violations[j].rfcLine
	})

	if len(violations) > 0 {
		fmt.Fprintf(os.Stderr, "lint-tsformat: %d proximity violation(s):\n", len(violations))
		for _, v := range violations {
			fmt.Fprintf(os.Stderr,
				"  - %s:%d: RFC3339Nano is within %d lines of %s at %s:%d — convert to tsformat.Format if this value is compared/ordered in SQL, or add a `// %s: <reason>` comment on the RFC3339Nano line to justify it\n",
				v.file, v.rfcLine, window, v.sqlCall, v.file, v.sqlLine, allowComment)
		}
		os.Exit(1)
	}

	mode := "diff-scoped"
	if full {
		mode = "full-tree"
	}
	fmt.Printf("lint-tsformat: ok (%s, %d file(s) scanned)\n", mode, scanned)
}
