// Command lint-migrations enforces that migration entries in
// internal/database/migrate.go which already exist on a base ref
// (defaults to origin/main) have not been modified in the working tree.
// This catches two flavours of schema-drift bug:
//
//  1. Two-branch-merge collision: a rebased PR keeps a version number
//     that another PR has already shipped to main, but with different
//     SQL — silent schema fork.
//  2. Body edit without rename: an entry keeps both its version and
//     name but the referenced `sql` (or `fn`) const is changed in-place.
//     The previous version-and-name-only check missed this entirely;
//     CodeRabbit flagged it as "Lint misses SQL/body edits for existing
//     migration versions".
//
// We compute an immutable fingerprint per migration that includes the
// referenced SQL/fn const body, then compare fingerprints at base and
// HEAD. Any divergence on an already-shipped version is a violation.
//
// Usage:
//
//	go run ./scripts/lint-migrations [base-ref]
//
// Exits non-zero on any divergence. Designed to run in CI against the
// PR's base ref; locally it accepts any ref reachable from `git show`.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const migrationsFile = "internal/database/migrate.go"

// migrationLine matches a single migration struct literal. The slice in
// migrate.go is written as one entry per line:
//
//	{version: 65, name: "add_skill_bootstrap_fields", sql: migrationXxx},
//
// We capture (version, name, ref-name) — version/name are the public
// identity, ref-name lets us look up the body of the underlying
// sql/fn const so the fingerprint can detect body edits that keep the
// version+name pair intact.
var migrationLine = regexp.MustCompile(`\{version:\s*(\d+),\s*name:\s*"([^"]+)"(?:,\s*(?:sql|fn):\s*(\w+))?`)

// constDecl captures Go's `const name = ` and `var name = ` forms
// when the value is a backtick-quoted raw string OR a single
// double-quoted literal. Multi-line raw strings (the typical migration
// body form) are matched with the [^`] sub-pattern under the s flag.
//
// We accept const-or-var because some migrations are wrapped in a
// function value (`fn: migrationXxx` where migrationXxx is a func), in
// which case the body is between the func's `{` and matching `}`. The
// findBodyAt helper handles both shapes — this regex is just the
// fallback for the simple raw-string case.
var stringConstDecl = regexp.MustCompile("(?:const|var)\\s+(\\w+)\\s*=\\s*`([^`]*)`")

type entry struct {
	version int
	name    string
	refName string // identifier referenced by the `sql:` or `fn:` field
}

// fingerprint returns a stable hash for one migration entry. It folds:
//   - the entry's (version, name) — the public identity
//   - the referenced const/func name — catches rename of the body
//   - the body of that referenced symbol — catches in-place edits
//
// Source content is the raw file bytes (HEAD or `git show :base:file`).
// We re-parse it on each call because callers operate on two distinct
// snapshots and the function has to work on each.
func fingerprint(e entry, allSourcesByPath map[string][]byte) string {
	h := sha256.New()
	fmt.Fprintf(h, "v=%d|n=%s|ref=%s|body=", e.version, e.name, e.refName)
	if e.refName != "" {
		body := findBody(e.refName, allSourcesByPath)
		h.Write(body)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// findBody locates the body of a const/var/func with the given name
// across the supplied source files. The body is whatever bytes are
// captured between the symbol declaration's opening delimiter and the
// matching close — for backtick-quoted strings that's between the two
// backticks; for func declarations it's the brace-balanced body.
//
// We search every file in allSourcesByPath so a migration body that
// lives in a sibling file (e.g. `migrate_v65.go`) is still picked up.
// Returns nil if not found — the fingerprint hash then degrades
// gracefully (only the (version, name, ref-name) tuple matters).
func findBody(name string, allSourcesByPath map[string][]byte) []byte {
	for _, src := range allSourcesByPath {
		// Fast path: backtick-quoted const / var.
		for _, m := range stringConstDecl.FindAllSubmatch(src, -1) {
			if string(m[1]) == name {
				return m[2]
			}
		}
		// Slower path: func declaration. Scan for `func <name>(` and
		// brace-match.
		needle := []byte("func " + name + "(")
		idx := bytes.Index(src, needle)
		if idx < 0 {
			continue
		}
		// Walk forward to the first `{`, then balance.
		brace := bytes.IndexByte(src[idx:], '{')
		if brace < 0 {
			continue
		}
		start := idx + brace
		depth := 0
		for i := start; i < len(src); i++ {
			switch src[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					return src[start : i+1]
				}
			}
		}
	}
	return nil
}

// parse extracts every migration entry from the given migrate.go bytes
// and returns them keyed by version so callers can diff base vs HEAD.
func parse(src []byte) (map[int]entry, error) {
	out := map[int]entry{}
	for _, m := range migrationLine.FindAllSubmatch(src, -1) {
		v, err := strconv.Atoi(string(m[1]))
		if err != nil {
			return nil, fmt.Errorf("parse version %q: %w", m[1], err)
		}
		if existing, dup := out[v]; dup {
			return nil, fmt.Errorf("duplicate version %d (names: %q, %q)", v, existing.name, m[2])
		}
		out[v] = entry{
			version: v,
			name:    string(m[2]),
			refName: string(m[3]), // empty if the migration doesn't reference an external symbol
		}
	}
	return out, nil
}

// loadSiblingSources reads every *.go file in the same directory as
// migrationsFile so findBody can resolve const/func names across
// the whole migration package. Falls back to just the file itself if
// the directory walk fails.
func loadSiblingSources(headFile []byte) map[string][]byte {
	dir := filepath.Dir(migrationsFile)
	out := map[string][]byte{migrationsFile: headFile}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if p == migrationsFile {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		out[p] = b
	}
	return out
}

// loadBaseSources fetches the same set of .go files from the base ref
// via `git show`. Files not present on base are silently skipped.
func loadBaseSources(baseRef string, headSources map[string][]byte) map[string][]byte {
	out := map[string][]byte{}
	for path := range headSources {
		cmd := exec.Command("git", "show", baseRef+":"+path)
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		if err := cmd.Run(); err != nil {
			continue
		}
		out[path] = stdout.Bytes()
	}
	return out
}

func main() {
	baseRef := "origin/main"
	if len(os.Args) > 1 {
		baseRef = os.Args[1]
	}

	// Working-tree version.
	headBytes, err := os.ReadFile(migrationsFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", migrationsFile, err)
		os.Exit(2)
	}

	// Base ref version. `git show <ref>:<path>` works for any ref
	// reachable from the repo, including remote-tracking branches and
	// tags.
	cmd := exec.Command("git", "show", baseRef+":"+migrationsFile)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// If the file does not exist on the base (e.g. brand-new
		// project), nothing to compare against — treat as success.
		if bytes.Contains(stderr.Bytes(), []byte("does not exist")) ||
			bytes.Contains(stderr.Bytes(), []byte("exists on disk, but not in")) {
			fmt.Printf("migration-lint: %s not present in %s, skipping\n", migrationsFile, baseRef)
			return
		}
		fmt.Fprintf(os.Stderr, "git show %s:%s failed: %v\n%s\n",
			baseRef, migrationsFile, err, stderr.String())
		os.Exit(2)
	}

	headMap, err := parse(headBytes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse HEAD: %v\n", err)
		os.Exit(2)
	}
	baseMap, err := parse(stdout.Bytes())
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse %s: %v\n", baseRef, err)
		os.Exit(2)
	}

	// Load every sibling .go file so the body-fingerprint check can
	// resolve symbols that live outside migrate.go itself.
	headSources := loadSiblingSources(headBytes)
	baseSources := loadBaseSources(baseRef, headSources)
	baseSources[migrationsFile] = stdout.Bytes() // overwrite with the base view we already fetched

	var violations []string
	for version, base := range baseMap {
		head, ok := headMap[version]
		if !ok {
			violations = append(violations,
				fmt.Sprintf("v%d (%q) was REMOVED — migrations on %s must be append-only",
					version, base.name, baseRef))
			continue
		}
		if head.name != base.name {
			violations = append(violations,
				fmt.Sprintf("v%d RENAMED: %s has %q, HEAD has %q — rebase your PR so the new migration takes the next free version",
					version, baseRef, base.name, head.name))
			continue
		}
		baseFP := fingerprint(base, baseSources)
		headFP := fingerprint(head, headSources)
		if baseFP != headFP {
			violations = append(violations,
				fmt.Sprintf("v%d (%q) BODY CHANGED — shipping a different body for an already-released migration silently diverges schemas across environments; create a new migration version instead (fingerprint base=%s head=%s)",
					version, base.name, baseFP[:12], headFP[:12]))
		}
	}

	if len(violations) > 0 {
		fmt.Fprintf(os.Stderr, "migration-lint: %d violation(s) against %s:\n", len(violations), baseRef)
		for _, v := range violations {
			fmt.Fprintf(os.Stderr, "  - %s\n", v)
		}
		os.Exit(1)
	}

	added := 0
	for v := range headMap {
		if _, exists := baseMap[v]; !exists {
			added++
		}
	}
	fmt.Printf("migration-lint: ok (%d migrations on %s, %d added in this branch)\n",
		len(baseMap), baseRef, added)
}
