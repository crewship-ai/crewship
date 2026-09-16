// Command deps-drift diffs the resolved versions of DIRECT dependencies
// between two revisions of the repository and renders the drift as a
// markdown table. It exists because of #2237: two Dependabot PRs moved
// `@sentry/nextjs` 10.71.0 → 10.72.0 without either PR's `package.json`
// asking for it — Dependabot regenerates the whole lockfile per branch — and
// nothing in the review path was positioned to notice. CodeRabbit is
// configured not to read bot PRs and not to read `pnpm-lock.yaml` at all, and
// a 12,000-line generated file is not something a person reads either.
//
// Run from the repository root:
//
//	go run ./scripts/deps-drift -base origin/main -head HEAD
//	go run ./scripts/deps-drift -base /path/to/base-checkout -head .
//
// Each of -base and -head is either a directory or a git revision; a revision
// is read with `git show <rev>:<file>` so neither side needs a checkout, an
// install, or a build. That is the whole reason this parses `pnpm-lock.yaml`
// and `go.mod` directly instead of shelling out to `pnpm list` or
// `go list -m`: both of those need the dependency tree materialised at BOTH
// revisions, which on CI means a second `pnpm install` for a table that the
// lockfile already contains verbatim. The `importers` section of a v9
// lockfile carries, per direct dependency, exactly the two facts this tool
// compares — the `specifier` copied from `package.json` and the `version`
// pnpm resolved it to. pnpm's own `--frozen-lockfile` refuses to install when
// the specifier disagrees with `package.json`, and CI installs that way, so
// the specifier in the lockfile is `package.json`'s spec as far as any build
// is concerned.
//
// For Go the two facts live in one file. With a tidy `go.mod` (which
// `-mod=readonly`, the default, enforces on every build) the version in a
// direct `require` line IS the selected version — minimal version selection
// never picks a lower one, and a higher transitive requirement would have
// forced the line to move. The one thing that changes a direct module's
// resolution without touching its require line is a `replace` directive, so
// that is what "resolved" means here: the replacement target when one
// applies, the required version otherwise. `go.sum` cannot move a direct
// dependency on its own; it is in the workflow's path filter only so a
// go.sum-only PR still gets its (empty) table.
//
// The rule, and the only thing that fails the run:
//
//	A direct dependency's resolved version changed while its spec did not.
//
// Everything else — declared bumps, added or removed dependencies, spec
// widened with nothing moving — is printed and passes. A normal Dependabot
// group bumps spec and resolution together, so it stays green; undeclared
// drift is the anomaly, and it is narrow enough to block on. -allow-drift
// (the `deps-drift-ok` PR label in CI) downgrades that failure to a warning
// while still printing the table.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
	"gopkg.in/yaml.v3"
)

const (
	lockfileName = "pnpm-lock.yaml"
	goModName    = "go.mod"
)

// verdict is one row's classification. Only verdictUndeclared can fail the
// run; the others are informational.
type verdict string

const (
	// verdictDeclared: the spec moved and the resolution moved with it — a
	// bump somebody asked for. Also covers a spec change with an unchanged
	// resolution (the range was widened and nothing new satisfied it).
	verdictDeclared verdict = "declared"
	// verdictUndeclared: the resolution moved while the spec stayed put.
	// This is the #2237 case and the one failing verdict.
	verdictUndeclared verdict = "UNDECLARED"
	verdictAdded      verdict = "added"
	verdictRemoved    verdict = "removed"
)

// dep is one direct dependency as one side of the comparison sees it.
type dep struct {
	// Spec is what the manifest asked for: a semver range for npm, the
	// required version for Go.
	Spec string
	// Resolved is what the lockfile / go.mod actually selected: a concrete
	// version for npm (peer-suffix stripped), the require version or the
	// replace target for Go.
	Resolved string
}

// row is one line of the drift table.
type row struct {
	Ecosystem  string
	Name       string
	Base, Head dep
	Verdict    verdict
}

// side is one revision's view of both manifests.
type side struct {
	JS map[string]dep
	Go map[string]dep
}

// source is where one side's files come from: a checked-out directory, or a
// git revision read through `git show`. Both are read lazily and a missing
// file (a revision before the lockfile existed) reads as an empty manifest.
type source struct {
	dir string
	rev string
}

func (s source) String() string {
	if s.dir != "" {
		return s.dir
	}
	return s.rev
}

// read returns the file's bytes, or nil (no error) when the file is absent.
func (s source) read(name string) ([]byte, error) {
	if s.dir != "" {
		b, err := os.ReadFile(filepath.Join(s.dir, name))
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return b, err
	}
	cmd := exec.Command("git", "show", s.rev+":"+name)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := stderr.String()
		// Both messages are git's, for "the revision resolves but has no
		// such file". A revision that does not resolve at all is a real
		// error and must stay one: a table computed against nothing would
		// read as "no drift", the exact fail-green shape this guard exists
		// to remove.
		if strings.Contains(msg, "does not exist in") || strings.Contains(msg, "exists on disk, but not in") {
			return nil, nil
		}
		return nil, fmt.Errorf("git show %s:%s: %v: %s", s.rev, name, err, strings.TrimSpace(msg))
	}
	return out, nil
}

// parseSource turns a flag value into a source: an existing directory is
// read as files, anything else is handed to git as a revision.
func parseSource(v string) source {
	if st, err := os.Stat(v); err == nil && st.IsDir() {
		return source{dir: v}
	}
	return source{rev: v}
}

func load(s source) (side, error) {
	var out side
	lock, err := s.read(lockfileName)
	if err != nil {
		return out, err
	}
	if out.JS, err = parseLockfile(lock); err != nil {
		return out, fmt.Errorf("%s at %s: %w", lockfileName, s, err)
	}
	mod, err := s.read(goModName)
	if err != nil {
		return out, err
	}
	if out.Go, err = parseGoMod(mod); err != nil {
		return out, fmt.Errorf("%s at %s: %w", goModName, s, err)
	}
	return out, nil
}

// ── pnpm-lock.yaml ──

// lockfile is the slice of a v9 `pnpm-lock.yaml` this tool reads. Everything
// under `packages:` and `snapshots:` — the tens of thousands of transitive
// lines — is ignored by the decoder; only `importers` describes what the
// workspace's own manifests asked for and got.
type lockfile struct {
	Importers map[string]lockImporter `yaml:"importers"`
}

type lockImporter struct {
	Dependencies         map[string]lockDep `yaml:"dependencies"`
	DevDependencies      map[string]lockDep `yaml:"devDependencies"`
	OptionalDependencies map[string]lockDep `yaml:"optionalDependencies"`
}

type lockDep struct {
	Specifier string `yaml:"specifier"`
	Version   string `yaml:"version"`
}

// parseLockfile returns the direct dependencies of every importer, keyed by
// package name — prefixed with the importer path when the workspace has more
// than the root importer, so two packages depending on the same name stay
// two rows. Which block a dependency sits in (prod, dev, optional) is not part
// of the key: moving `x` from dependencies to devDependencies at the same
// version is not drift.
func parseLockfile(b []byte) (map[string]dep, error) {
	deps := map[string]dep{}
	if len(bytes.TrimSpace(b)) == 0 {
		return deps, nil
	}
	var lf lockfile
	if err := yaml.Unmarshal(b, &lf); err != nil {
		return nil, err
	}
	for importer, imp := range lf.Importers {
		prefix := ""
		if importer != "." {
			prefix = importer + ":"
		}
		for _, block := range []map[string]lockDep{imp.Dependencies, imp.DevDependencies, imp.OptionalDependencies} {
			for name, d := range block {
				deps[prefix+name] = dep{Spec: d.Specifier, Resolved: bareVersion(d.Version)}
			}
		}
	}
	return deps, nil
}

// bareVersion strips pnpm's peer-dependency suffix: the lockfile writes
// `9.4.2(@dicebear/core@9.4.3)` when a package was resolved against a
// specific peer. The suffix changes whenever the PEER moves, and the peer is
// itself a direct dependency with its own row, so keeping it would report
// one bump twice. `link:` and URL versions carry no suffix and pass through.
func bareVersion(v string) string {
	if i := strings.IndexByte(v, '('); i > 0 {
		return v[:i]
	}
	return v
}

// ── go.mod ──

// parseGoMod returns the direct requirements of a go.mod, with `replace`
// applied to the resolved side. `// indirect` requirements are not direct
// dependencies and are skipped; a replace that targets an indirect module
// therefore does not appear either.
func parseGoMod(b []byte) (map[string]dep, error) {
	deps := map[string]dep{}
	if len(bytes.TrimSpace(b)) == 0 {
		return deps, nil
	}
	f, err := modfile.Parse(goModName, b, nil)
	if err != nil {
		return nil, err
	}
	// A versionless replace (`a => b v1`) applies to every version of `a`; a
	// versioned one (`a v1 => b v2`) only to that version. Versioned wins
	// when both are present, as in the go command.
	replaceAll := map[string]string{}
	replaceAt := map[string]string{}
	for _, r := range f.Replace {
		target := r.New.Path
		if r.New.Version != "" {
			target += "@" + r.New.Version
		}
		if r.Old.Version == "" {
			replaceAll[r.Old.Path] = target
		} else {
			replaceAt[r.Old.Path+"@"+r.Old.Version] = target
		}
	}
	for _, r := range f.Require {
		if r.Indirect {
			continue
		}
		resolved := r.Mod.Version
		if t, ok := replaceAt[r.Mod.Path+"@"+r.Mod.Version]; ok {
			resolved = "=> " + t
		} else if t, ok := replaceAll[r.Mod.Path]; ok {
			resolved = "=> " + t
		}
		deps[r.Mod.Path] = dep{Spec: r.Mod.Version, Resolved: resolved}
	}
	return deps, nil
}

// ── diff ──

// diff compares one ecosystem's two sides and returns only the rows that
// differ, sorted by name. Unchanged dependencies are counted, not listed.
func diff(ecosystem string, base, head map[string]dep) (rows []row, unchanged int) {
	names := map[string]bool{}
	for n := range base {
		names[n] = true
	}
	for n := range head {
		names[n] = true
	}
	for n := range names {
		b, inBase := base[n]
		h, inHead := head[n]
		r := row{Ecosystem: ecosystem, Name: n, Base: b, Head: h}
		switch {
		case !inBase:
			r.Verdict = verdictAdded
		case !inHead:
			r.Verdict = verdictRemoved
		case b == h:
			unchanged++
			continue
		case b.Resolved != h.Resolved && b.Spec == h.Spec:
			r.Verdict = verdictUndeclared
		default:
			r.Verdict = verdictDeclared
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, unchanged
}

// report is the whole comparison, ready to render.
type report struct {
	Base, Head string
	Rows       []row
	Unchanged  int
	Compared   int
}

func (r report) undeclared() []row {
	var out []row
	for _, x := range r.Rows {
		if x.Verdict == verdictUndeclared {
			out = append(out, x)
		}
	}
	return out
}

func compare(baseName, headName string, base, head side) report {
	rep := report{Base: baseName, Head: headName}
	js, jsSame := diff("npm", base.JS, head.JS)
	gomod, goSame := diff("go", base.Go, head.Go)
	rep.Rows = append(js, gomod...)
	rep.Unchanged = jsSame + goSame
	rep.Compared = len(head.JS) + len(head.Go)
	return rep
}

// ── rendering ──

func cell(s string) string {
	if s == "" {
		return "—"
	}
	return "`" + strings.ReplaceAll(s, "|", "\\|") + "`"
}

// markdown renders the report for a job summary or a PR comment. The marker
// argument, when non-empty, is written first so the comment can be found and
// updated in place on the next run.
func markdown(w io.Writer, rep report, marker string, allowDrift bool) {
	if marker != "" {
		fmt.Fprintln(w, marker)
	}
	und := rep.undeclared()
	switch {
	case len(und) > 0 && allowDrift:
		fmt.Fprintf(w, "### ⚠️ Direct dependency drift: %d undeclared (allowed by label)\n\n", len(und))
	case len(und) > 0:
		fmt.Fprintf(w, "### ❌ Direct dependency drift: %d undeclared\n\n", len(und))
	case len(rep.Rows) > 0:
		fmt.Fprintf(w, "### ✅ Direct dependency drift: %d change(s), all declared\n\n", len(rep.Rows))
	default:
		fmt.Fprintln(w, "### ✅ Direct dependency drift: none")
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "`%s` → `%s` · %d direct dependencies compared, %d unchanged.\n\n", rep.Base, rep.Head, rep.Compared, rep.Unchanged)
	if len(rep.Rows) == 0 {
		return
	}
	fmt.Fprintln(w, "| Ecosystem | Dependency | Base | Head | Spec (base) | Spec (head) | Verdict |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|---|")
	for _, r := range rep.Rows {
		v := string(r.Verdict)
		if r.Verdict == verdictUndeclared {
			v = "**" + v + "**"
		}
		fmt.Fprintf(w, "| %s | `%s` | %s | %s | %s | %s | %s |\n",
			r.Ecosystem, r.Name, cell(r.Base.Resolved), cell(r.Head.Resolved), cell(r.Base.Spec), cell(r.Head.Spec), v)
	}
	fmt.Fprintln(w)
	if len(und) > 0 {
		fmt.Fprintln(w, "**UNDECLARED** means the resolved version moved while the spec in `package.json` / `go.mod` did not — nothing in this PR asked for that version (#2237). Either pin the spec so the bump is declared, regenerate the lockfile against the base without it, or apply the `deps-drift-ok` label if the drift is wanted.")
	} else {
		fmt.Fprintln(w, "Every resolution change here was asked for by a matching spec change. Added and removed dependencies are listed for the record; they never fail this check.")
	}
}

// annotations writes GitHub workflow commands (or plain lines when not on
// Actions — the syntax is harmless either way) naming each undeclared row.
func annotations(w io.Writer, rep report, allowDrift bool) {
	level := "error"
	if allowDrift {
		level = "warning"
	}
	for _, r := range rep.undeclared() {
		fmt.Fprintf(w, "::%s::%s %s resolved %s → %s while its spec stayed %s (undeclared drift, #2237)\n",
			level, r.Ecosystem, r.Name, r.Base.Resolved, r.Head.Resolved, r.Base.Spec)
	}
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("deps-drift", flag.ContinueOnError)
	fs.SetOutput(stderr)
	baseFlag := fs.String("base", "", "base side: a directory or a git revision (required)")
	headFlag := fs.String("head", ".", "head side: a directory or a git revision")
	summary := fs.String("summary", "", "append the markdown report to this file as well as stdout")
	marker := fs.String("marker", "", "HTML comment written at the top of the markdown so a PR comment can be updated in place")
	allow := fs.Bool("allow-drift", false, "report undeclared drift as a warning instead of failing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *baseFlag == "" {
		fmt.Fprintln(stderr, "deps-drift: -base is required")
		fs.Usage()
		return 2
	}
	baseSrc, headSrc := parseSource(*baseFlag), parseSource(*headFlag)
	base, err := load(baseSrc)
	if err != nil {
		fmt.Fprintf(stderr, "::error::deps-drift: base: %v\n", err)
		return 2
	}
	head, err := load(headSrc)
	if err != nil {
		fmt.Fprintf(stderr, "::error::deps-drift: head: %v\n", err)
		return 2
	}
	rep := compare(baseSrc.String(), headSrc.String(), base, head)

	var md bytes.Buffer
	markdown(&md, rep, *marker, *allow)
	if _, err := stdout.Write(md.Bytes()); err != nil {
		fmt.Fprintf(stderr, "deps-drift: %v\n", err)
		return 2
	}
	if *summary != "" {
		f, err := os.OpenFile(*summary, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintf(stderr, "deps-drift: %v\n", err)
			return 2
		}
		_, werr := f.Write(md.Bytes())
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			fmt.Fprintf(stderr, "deps-drift: %v\n", werr)
			return 2
		}
	}
	annotations(stderr, rep, *allow)
	if len(rep.undeclared()) > 0 && !*allow {
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
