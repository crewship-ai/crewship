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
	"sort"
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
		body, path, isFunc := findBodyWithPath(e.refName, allSourcesByPath)
		h.Write(body)
		// A `fn:` migration almost never does its work inline — it delegates
		// to helpers, and hashing only the registered function left those
		// completely outside the immutability guarantee. Every fn: migration
		// in the tree does this:
		//
		//   v152 migrationJournalHashChain      -> backfillJournalChain
		//   v159 migrationRunStepOutputs        -> backfillRunStepOutputs
		//   v161 migrationNotificationPrefs     -> widenNotificationChannelType
		//   v141 migrationNormalize...Tsformat  -> parseLegacyMemoryVersion...
		//   v144 migrationConvertDatetimeNow... -> rewriteTableDefaultLiteral,
		//                                          backfillLegacyTimestampRows
		//
		// rewriteTableDefaultLiteral is shared by v144, v148 and v161, so ONE
		// edit silently changed the behaviour of three already-shipped
		// migrations with no violation reported. Immutability that a helper
		// edit walks straight through is not immutability.
		//
		// What gets folded is the transitive closure of same-file functions
		// the body actually CALLS. Two blunter designs were tried and both
		// over-fire on legitimate work, which would get the tool switched off:
		//
		//   - hashing the whole declaring file: `migrate.go` holds the registry
		//     slice as well as some bodies, so ADDING a migration would flag
		//     every fn: migration declared beside it.
		//   - hashing every func in the file: adding a NEW migration's helper
		//     to that same file would flag the existing ones.
		//
		// Adding a migration is the most common legitimate operation there is
		// and must stay silent. Resolving calls keeps it silent while still
		// catching the case this exists for.
		//
		// Same-file only, and identifier-matched rather than parsed — this is a
		// text-level guard that has to keep working on `git show` bytes from
		// two commits, without a Go toolchain. A helper moved to a different
		// file is still outside the fence; putting it in the migration's own
		// file is the convention that keeps it inside.
		//
		// Scoped to func bodies only. `sql:` migrations are self-contained
		// backtick consts with nothing to delegate to.
		if isFunc && path != "" {
			fmt.Fprintf(h, "|helpers=%s|", path)
			h.Write(calledFuncBodies(allSourcesByPath[path], body))
		}
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
	body, _, _ := findBodyWithPath(name, allSourcesByPath)
	return body
}

// findBodyWithPath is findBody plus the path of the file the symbol was
// declared in and whether it was a func (rather than a string const). The
// fingerprint needs both so it can fold a func's whole declaring file — see
// the comment there for why.
//
// Iteration order over a map is random, so paths are walked in sorted order:
// two runs over the same sources must agree on which file declared a symbol,
// or the fingerprint would flap between commits and report phantom edits.
func findBodyWithPath(name string, allSourcesByPath map[string][]byte) ([]byte, string, bool) {
	paths := make([]string, 0, len(allSourcesByPath))
	for p := range allSourcesByPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, path := range paths {
		src := allSourcesByPath[path]
		// Fast path: backtick-quoted const / var.
		for _, m := range stringConstDecl.FindAllSubmatch(src, -1) {
			if string(m[1]) == name {
				return m[2], path, false
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
					return src[start : i+1], path, true
				}
			}
		}
	}
	return nil, "", false
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

// --- file-based migrations -------------------------------------------------
//
// The registry moved out of migrate.go: new migrations are .sql files under
// internal/database/migrations/. The slice check above cannot see them, so an
// edit to an already-shipped .sql file would sail past the very guard this
// tool exists to be. Same rules, applied to file content: a migration present
// on the base ref may not change and may not disappear.

const fileMigrationRoot = "internal/database/migrations"

// listBaseMigrationFiles returns path → content for every .sql file under the
// migrations root as of baseRef. An absent root (base predates the scheme) is
// not an error — there is simply nothing to compare.
func listBaseMigrationFiles(baseRef string) (map[string][]byte, error) {
	cmd := exec.Command("git", "ls-tree", "-r", "--name-only", baseRef, "--", fileMigrationRoot)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git ls-tree %s: %v (%s)", baseRef, err, errBuf.String())
	}

	files := map[string][]byte{}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		path := strings.TrimSpace(line)
		if path == "" || !strings.HasSuffix(path, ".sql") {
			continue
		}
		show := exec.Command("git", "show", baseRef+":"+path)
		var body, showErr bytes.Buffer
		show.Stdout = &body
		show.Stderr = &showErr
		if err := show.Run(); err != nil {
			return nil, fmt.Errorf("git show %s:%s: %v (%s)", baseRef, path, err, showErr.String())
		}
		files[path] = body.Bytes()
	}
	return files, nil
}

// checkFileMigrations compares each base .sql migration against the working
// tree, returning one message per violation.
func checkFileMigrations(baseRef string) ([]string, error) {
	base, err := listBaseMigrationFiles(baseRef)
	if err != nil {
		return nil, err
	}

	var violations []string
	for path, baseBody := range base {
		headBody, readErr := os.ReadFile(path)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				violations = append(violations, fmt.Sprintf(
					"%s was REMOVED — migrations on %s must be append-only; a database that "+
						"applied it would have a ledger row this binary cannot explain",
					path, baseRef))
				continue
			}
			return nil, fmt.Errorf("read %s: %w", path, readErr)
		}
		if !bytes.Equal(baseBody, headBody) {
			violations = append(violations, fmt.Sprintf(
				"%s CHANGED — an already-released migration must never be edited; every "+
					"database that already applied it keeps the old schema while new ones get "+
					"the new one. Add another migration instead (base sha256=%s head=%s)",
				path, shortSum(baseBody), shortSum(headBody)))
		}
	}
	sort.Strings(violations)
	return violations, nil
}

func shortSum(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
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

	fileViolations, err := checkFileMigrations(baseRef)
	if err != nil {
		fmt.Fprintf(os.Stderr, "check file migrations: %v\n", err)
		os.Exit(2)
	}
	violations = append(violations, fileViolations...)

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

// topLevelFuncs indexes every top-level func body in src by name. Method
// declarations (`func (r *T) Name(`) are skipped — the migration bodies this
// guards are all plain functions.
func topLevelFuncs(src []byte) map[string]string {
	out := map[string]string{}
	for i := 0; i+5 < len(src); i++ {
		if !bytes.HasPrefix(src[i:], []byte("func ")) {
			continue
		}
		if i > 0 && src[i-1] != '\n' {
			continue
		}
		rest := src[i+len("func "):]
		paren := bytes.IndexByte(rest, '(')
		if paren < 0 {
			continue
		}
		name := strings.TrimSpace(string(rest[:paren]))
		if name == "" || strings.ContainsAny(name, " \t)") {
			continue
		}
		brace := bytes.IndexByte(src[i:], '{')
		if brace < 0 {
			continue
		}
		start := i + brace
		depth := 0
		for j := start; j < len(src); j++ {
			switch src[j] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					out[name] = string(src[start : j+1])
					i = j
					j = len(src)
				}
			}
		}
	}
	return out
}

var identifier = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// calledFuncBodies returns the bodies of every same-file function reachable
// from root, name-sorted so the digest does not depend on declaration order.
//
// "Reachable" is identifier matching, not real call analysis: any word in a
// body that names a top-level func in the same file counts as a call. That
// over-approximates (a local variable sharing a helper's name pulls it in) and
// over-approximating is the safe direction here — it can only widen what the
// immutability guarantee covers, never narrow it.
func calledFuncBodies(src []byte, root []byte) []byte {
	funcs := topLevelFuncs(src)
	if len(funcs) == 0 {
		return nil
	}
	seen := map[string]bool{}
	queue := []string{}
	visit := func(body []byte) {
		for _, id := range identifier.FindAll(body, -1) {
			n := string(id)
			if _, ok := funcs[n]; ok && !seen[n] {
				seen[n] = true
				queue = append(queue, n)
			}
		}
	}
	visit(root)
	for i := 0; i < len(queue); i++ {
		visit([]byte(funcs[queue[i]]))
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	var out bytes.Buffer
	for _, n := range names {
		out.WriteString(n)
		out.WriteByte(0)
		out.WriteString(funcs[n])
		out.WriteByte(0)
	}
	return out.Bytes()
}
