package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// The `-f/--format` contract (#2086).
//
// `--format/-f` is a PERSISTENT flag on rootCmd, so every command in the tree
// advertises `Output format: table|json|yaml|ndjson|quiet` in its help. The
// platform's premise is that agents drive this CLI, and `-f json` is the only
// thing those pipelines can read — so a command that advertises the flag and
// then prints human prose regardless is not a cosmetic defect. It is a broken
// contract that fails silently and exits 0, which is the worst way for a
// contract to fail.
//
// The audit that opened #2086 found 17 by hand. The sweep this file automates
// found 30. The difference is the point: a hand sweep can only reach the
// commands you can invoke without arguments, and that is a minority of the
// tree.
//
// ─── what this guard checks ──────────────────────────────────────────────
//
// A command can only honour the flag if it ASKS what the format is. Two
// failures follow from that, and both are decidable from source:
//
//  1. A REPORTING command that never reaches a format-resolution entry point.
//     It cannot be honouring anything; `-f json` gets the human bytes.
//  2. A reporting command that DOES resolve the format but still writes prose
//     to stdout outside any format-gated branch — the prose then prefixes the
//     JSON and the whole stream is unparseable. (`digest enable` did exactly
//     this: three lines of advice, then a valid JSON object.)
//
// ─── why static, and why the join is dynamic ─────────────────────────────
//
// Executing every command with `-f json` and piping through a decoder covers
// only the ones you can stub a server and arguments for; the rest are skipped
// silently, which is precisely how these drifted. So the ANALYSIS is static.
// But a static command-tree reconstruction is its own source of silent gaps —
// AddCommand wiring is spread over ~300 init() funcs — so the command PATHS
// come from walking the real rootCmd, and each is joined to its source by the
// file:line runtime.FuncForPC reports for its RunE. Nothing can be missing
// from the survey, and what is surveyed is analysed exactly.
//
// ─── what this guard deliberately does NOT check ─────────────────────────
//
// `null` vs `[]` for an empty list. That is the third failure in #2086 and it
// is not a per-command property at all — it is one line in the shared encoder.
// Fixed in internal/cli.Formatter, guarded by TestJSONNilSliceEncodesAsArray
// and friends in internal/cli/formatter_nilslice_test.go.

// ─── what counts as writing to stdout ────────────────────────────────────

// stdoutSinks are the calls that put bytes on stdout. cli.PrintSuccess /
// PrintError / PrintWarning are deliberately absent: they write to STDERR,
// which is the correct place for a human status line and never pollutes a
// `-f json` pipe. That asymmetry is the whole reason a "✓ done" line is fine
// and a `fmt.Println("✓ done")` is not.
var stdoutSinks = map[string]bool{
	"fmt.Print":   true,
	"fmt.Printf":  true,
	"fmt.Println": true,
}

// formatEntryPoints are the ways a command can learn which format was asked
// for. Reaching any one of them means the command is format-AWARE; whether it
// then renders each format correctly is the per-command tests' job.
//
// cli.NewFormatter is here with a caveat enforced in isFormatEntryPoint:
// `cli.NewFormatter("table")` is a Formatter pinned to one format, which is
// the opposite of honouring the flag. `crew status` was built that way and
// reads as compliant until you notice the literal.
var formatEntryPoints = map[string]bool{
	"resolvedFormat":    true,
	"resolvedFormatter": true,
	"newFormatter":      true,
	"cli.NewFormatter":  true,
	"cli.ResolveFormat": true,
}

// isFormatEntryPoint reports whether c is a call that resolves the requested
// output format, as opposed to one that hardcodes a format.
func isFormatEntryPoint(c *ast.CallExpr) bool {
	name := callName(c)
	if !formatEntryPoints[name] {
		return false
	}
	// A constructor handed a string literal is pinned, not resolved.
	for _, arg := range c.Args {
		if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			return false
		}
	}
	return true
}

// formatIdents are identifiers whose mere use proves format awareness, for the
// commands that branch on the raw flag instead of building a Formatter.
var formatIdents = map[string]bool{
	"flagFormat": true,
}

// ─── which commands owe machine output ───────────────────────────────────
//
// Two very different things can arrive on stdout, and conflating them is what
// makes a guard like this either useless or unactionable:
//
//   - a RESULT. The caller asked a question and the stdout bytes are the
//     answer: a config, a status, a list, a version history. If `-f json`
//     does not work, the answer is readable by a human and by nothing else.
//   - a RECEIPT for a mutation. The answer is the exit code; the line is a
//     courtesy. Those belong on stderr — which is where cli.PrintSuccess
//     already puts them — and ~100 commands still print theirs on stdout.
//     That is a real but separate cleanup, and it is not swept under the rug
//     here: TestMutationReceiptsOnStdoutDoNotGrow counts them and fails if
//     the population grows.
//
// reportingVerbs is the leaf-name half of the classification. It is a closed
// list rather than a regex so that adding a new kind of read command is a
// deliberate edit with a reviewer on it.
var reportingVerbs = map[string]bool{
	"list": true, "get": true, "show": true, "view": true, "status": true,
	"history": true, "info": true, "stats": true, "summary": true,
	"versions": true, "tree": true, "current": true, "active": true,
	"errors": true, "pending": true, "logs": true, "diff": true,
	"describe": true, "inspect": true, "search": true, "explain": true,
	"whoami": true, "capabilities": true, "schema": true, "url": true,
	"path": true, "metrics": true, "check": true, "doctor": true,
	"validate": true, "preview": true, "dry-run": true, "count": true,
}

// reportingCommands names the reporting commands whose leaf verb does not say
// so — the top-level ones mostly, where the noun IS the command.
var reportingCommands = map[string]bool{
	"crewship lint":                    true,
	"crewship logs":                    true,
	"crewship activity":                true,
	"crewship db migration-status":     true,
	"crewship paymaster subscriptions": true,
	"crewship crew config":             true,
	"crewship routine metadata":        true,
	"crewship keeper history":          true,
}

// ─── exemptions ──────────────────────────────────────────────────────────
//
// Every entry needs a reason, and the reason has to be that the command's
// stdout is not a rendering of a result at all: it is a document, a stream, a
// session or a generated script. "It would be work to fix" is not a reason.
// An entry that stops naming a real command fails
// TestFormatContractExemptionsAreAllReal below, so this table cannot rot into
// a graveyard.
var formatContractExempt = map[string]string{
	// Manifest export. Verified in cmd_export.go: both build a
	// `cli.ManifestDoc` and hand it to yaml.Marshal unconditionally, with no
	// reference to the format flag anywhere on the path. That is by design —
	// the artifact is a manifest you check into git and feed back to
	// `crewship apply`, not a rendering of a query result — so `-f json`
	// having no effect is correct rather than a defect.
	"crewship export crew": "emits a YAML manifest by design (cmd_export.go: yaml.Marshal, unconditional); the document IS the output, and `crewship apply` reads it back",
	"crewship export page": "emits a YAML manifest by design (cmd_export.go: yaml.Marshal, unconditional); the document IS the output, and `crewship apply` reads it back",

	// `crewship export workspace` writes the same kind of manifest, to a file
	// or to stdout, for the whole workspace.
	"crewship export workspace": "emits a YAML manifest by design; same contract as `export crew`/`export page`",

	// Local-filesystem path lookups: stdout is one path, for `cd $(…)`.
	// Wrapping a single string in a JSON object would break the only use.
	"crewship prompt path": "prints one filesystem path for command substitution; a JSON envelope would break `cd $(crewship prompt path x)`",

	// DOWNLOADS. stdout is the file's bytes, streamed with io.Copy, and the
	// point of the command is to be redirected or piped. `-f json` cannot
	// wrap arbitrary (possibly binary) content without destroying it, and
	// each of these already offers --out for the file-on-disk case.
	"crewship crew files get":       "streams the file's raw bytes to stdout (io.Copy); the bytes ARE the output, and --out writes them to disk",
	"crewship agent avatar show":    "streams the avatar SVG to stdout; the bytes ARE the output, and --out writes them to disk",
	"crewship memory show":          "streams a memory blob's raw content to stdout; its own help documents stdout as the raw bytes with status on stderr",
	"crewship memory versions show": "streams a memory blob's raw content to stdout; its own help documents stdout as the raw bytes with status on stderr",

	// A generated document that is already machine-readable.
	"crewship routine schema": "writes the embedded routine JSON Schema; the document IS the output and it is already valid JSON, so `-f json` has nothing to convert",

	// Streaming natural language. stdout is a live token stream from an
	// agent, delivered as it arrives — there is no document to close, and
	// buffering the whole answer to wrap it would defeat the streaming this
	// command exists to provide. Same class as `crewship ask`.
	"crewship explain": "streams an agent's answer token by token; stdout is a live stream, not a document that can be closed and encoded",
}

// ─── the walk ────────────────────────────────────────────────────────────

// commandSite is one runnable command joined to the source position of its
// handler.
type commandSite struct {
	path string // "crewship agent list"
	leaf string // "list"
	file string
	line int
}

// runnableCommands walks the real command tree and resolves each runnable
// command's handler to a source position.
//
// The position comes from runtime.FuncForPC on the RunE/Run value: for a func
// literal inside a &cobra.Command{…} that is the literal's own `func(` line,
// and for a named handler it is the declaration line. Either way it is the
// entry line of the function this guard needs to analyse, which is what makes
// the join to the AST exact rather than a name-matching heuristic.
//
// Reading Commands() sorts the child slice in place on first call; TestMain
// has already frozen the order (see cobra_sort_parallel_guard_test.go), so
// this is a pure read — but it is still why nothing in this file calls
// t.Parallel().
func runnableCommands(t *testing.T) []commandSite {
	t.Helper()
	var out []commandSite
	var walk func(*cobra.Command, string)
	walk = func(c *cobra.Command, prefix string) {
		path := strings.TrimSpace(prefix + " " + c.Name())
		var pc uintptr
		if c.RunE != nil {
			pc = reflect.ValueOf(c.RunE).Pointer()
		} else if c.Run != nil {
			pc = reflect.ValueOf(c.Run).Pointer()
		}
		if pc != 0 {
			if f := runtime.FuncForPC(pc); f != nil {
				file, line := f.FileLine(pc)
				out = append(out, commandSite{path: path, leaf: c.Name(), file: file, line: line})
			}
		}
		for _, sub := range c.Commands() {
			walk(sub, path)
		}
	}
	walk(rootCmd, "")
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// isReporting reports whether s answers a question, and therefore owes the
// caller a machine-readable answer when one is asked for.
func (s commandSite) isReporting() bool {
	return reportingVerbs[s.leaf] || reportingCommands[s.path]
}

// ─── the AST side ────────────────────────────────────────────────────────

// pkgIndex holds everything the analysis needs about cmd/crewship's source:
// the body of every function keyed by the position runtime reports for it, and
// the body of every package-level function keyed by name, so the walk can
// follow a handler into the helpers it delegates its rendering to.
type pkgIndex struct {
	fset *token.FileSet
	// byPos maps "file:line" to a function body. Both *ast.FuncDecl and
	// *ast.FuncLit are indexed; the runtime reports the `func(` line for both.
	byPos map[string]*ast.BlockStmt
	// byName maps a package-level func name to its body.
	byName map[string]*ast.BlockStmt
}

func loadPkgIndex(t *testing.T) *pkgIndex {
	t.Helper()
	idx := &pkgIndex{
		fset:   token.NewFileSet(),
		byPos:  map[string]*ast.BlockStmt{},
		byName: map[string]*ast.BlockStmt{},
	}
	for _, path := range goFiles(t, filepath.Join(repoRoot(t), "cmd", "crewship")) {
		f, err := parser.ParseFile(idx.fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			var body *ast.BlockStmt
			var pos token.Pos
			switch fn := n.(type) {
			case *ast.FuncDecl:
				if fn.Body == nil {
					return true
				}
				body, pos = fn.Body, fn.Pos()
				if fn.Recv == nil {
					idx.byName[fn.Name.Name] = fn.Body
				}
			case *ast.FuncLit:
				if fn.Body == nil {
					return true
				}
				body, pos = fn.Body, fn.Pos()
			default:
				return true
			}
			p := idx.fset.Position(pos)
			idx.byPos[posKey(p.Filename, p.Line)] = body
			return true
		})
	}
	return idx
}

func posKey(file string, line int) string {
	abs, err := filepath.Abs(file)
	if err != nil {
		abs = file
	}
	return abs + ":" + strconv.Itoa(line)
}

// callName renders a call's callee as "fmt.Println" / "newFormatter" /
// "f.JSON". Anything more exotic (a call on a call, an index expression)
// renders empty and is ignored.
func callName(c *ast.CallExpr) string {
	switch fn := c.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		if x, ok := fn.X.(*ast.Ident); ok {
			return x.Name + "." + fn.Sel.Name
		}
		return "." + fn.Sel.Name
	}
	return ""
}

// analysis is what the call-graph walk found under one handler.
type analysis struct {
	printsToStdout bool
	formatAware    bool
	// prefixProse are stdout writes the handler makes BEFORE it has any idea
	// what format was asked for — see TestNoProseBeforeTheFormatIsKnown.
	// Rendered as "file:line" for the failure message.
	prefixProse []string
}

// analyse walks body and the package-level functions it calls, to a bounded
// depth, recording where it writes to stdout and whether it ever resolves the
// output format.
//
// Depth is bounded rather than exhaustive on purpose: a print reached six
// frames down through generic plumbing is not the pattern this guard is
// about, and an unbounded walk over a 686-file package turns every
// mutually-recursive helper pair into a hang.
func (idx *pkgIndex) analyse(body *ast.BlockStmt) analysis {
	var res analysis
	seen := map[*ast.BlockStmt]bool{}

	// Offsets, within the handler's own body, of every stdout write and of the
	// first moment the handler learns what format was asked for. Compared at
	// the end: bytes written before that moment cannot be conditional on it.
	var printPos []token.Position
	firstGate := -1
	noteGate := func(pos token.Pos) {
		if off := idx.fset.Position(pos).Offset; firstGate < 0 || off < firstGate {
			firstGate = off
		}
	}

	var walk func(*ast.BlockStmt, int)
	walk = func(b *ast.BlockStmt, depth int) {
		if b == nil || seen[b] || depth > 4 {
			return
		}
		seen[b] = true
		ast.Inspect(b, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.Ident:
				if formatIdents[node.Name] {
					res.formatAware = true
					if depth == 0 {
						noteGate(node.Pos())
					}
				}
			case *ast.SwitchStmt:
				if depth == 0 && mentionsFormat(node.Tag) {
					noteGate(node.Pos())
				}
			case *ast.IfStmt:
				if depth == 0 && mentionsFormat(node.Cond) {
					noteGate(node.Pos())
				}
			case *ast.CallExpr:
				name := callName(node)
				if name == "" {
					return true
				}
				if isFormatEntryPoint(node) {
					res.formatAware = true
					if depth == 0 {
						noteGate(node.Pos())
					}
				}
				// fmt.Fprint*(os.Stdout, …), tabwriter.NewWriter(os.Stdout, …)
				// and json.NewEncoder(os.Stdout) are stdout too, just spelled
				// with an explicit writer.
				if stdoutSinks[name] || writesToOsStdout(node) {
					res.printsToStdout = true
					if depth == 0 {
						// The CLOSING paren, not the opening one. A call's
						// own Pos() precedes its arguments, so a handler that
						// resolves the format INSIDE the call that consumes it
						// —
						//	runKeeperEval(ctx, db, opts{Format: resolvedFormat(cmd)}, os.Stdout, …)
						// — would otherwise look like a write that happened
						// before the format was known, which is a false
						// accusation and the fastest way to get a guard
						// deleted.
						printPos = append(printPos, idx.fset.Position(node.Rparen))
					}
				}
				// Follow package-level helpers — a handler that delegates its
				// whole rendering to printFoo(…) has the same defect one frame
				// down.
				if sub, ok := idx.byName[name]; ok {
					walk(sub, depth+1)
				}
			}
			return true
		})
	}
	walk(body, 0)

	if res.formatAware && firstGate >= 0 {
		for _, p := range printPos {
			if p.Offset < firstGate {
				res.prefixProse = append(res.prefixProse,
					filepath.Base(p.Filename)+":"+strconv.Itoa(p.Line))
			}
		}
	}
	return res
}

// mentionsFormat reports whether expr names an output format anywhere inside
// it — the tag of `switch f.Format {…}`, the condition of `if format == "json"`.
func mentionsFormat(expr ast.Expr) bool {
	if expr == nil {
		return false
	}
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.Ident:
			l := strings.ToLower(v.Name)
			if strings.Contains(l, "format") || l == "json" || l == "quiet" {
				found = true
			}
		case *ast.SelectorExpr:
			if strings.Contains(strings.ToLower(v.Sel.Name), "format") {
				found = true
			}
		}
		return true
	})
	return found
}

// writesToOsStdout reports whether c reaches stdout through an explicit
// writer rather than through fmt.Print*:
//
//	fmt.Fprintf(os.Stdout, …)          // the writer as an argument
//	tabwriter.NewWriter(os.Stdout, …)
//	json.NewEncoder(os.Stdout)
//	cmd.OutOrStdout()                  // Cobra's writer, which IS stdout
//
// The last one matters more than it looks: `credential field list` and
// `persona view` both write their prose through cmd.OutOrStdout(), and a
// check that only knew about os.Stdout scored them clean.
func writesToOsStdout(c *ast.CallExpr) bool {
	if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "OutOrStdout" {
		return true
	}
	for _, arg := range c.Args {
		sel, ok := arg.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		if x, ok := sel.X.(*ast.Ident); ok && x.Name == "os" && sel.Sel.Name == "Stdout" {
			return true
		}
	}
	return false
}

// analyseAll runs the analysis over every runnable command, returning the
// sites alongside their results. Shared by all the guards below so the tree is
// walked once per test rather than once per assertion.
func analyseAll(t *testing.T) ([]commandSite, map[string]analysis) {
	t.Helper()
	idx := loadPkgIndex(t)
	sites := runnableCommands(t)

	// Guard the guard. If either half of the join goes vacuous, every command
	// silently looks clean and these tests pass while checking nothing — the
	// exact failure mode that makes a guard worse than none.
	if len(sites) < 300 {
		t.Fatalf("only %d runnable commands found — the rootCmd walk went vacuous", len(sites))
	}
	out := make(map[string]analysis, len(sites))
	resolved := 0
	for _, s := range sites {
		body, ok := idx.byPos[posKey(s.file, s.line)]
		if !ok {
			continue
		}
		resolved++
		out[s.path] = idx.analyse(body)
	}
	if resolved < len(sites)*3/4 {
		t.Fatalf("resolved only %d of %d handlers to source — the runtime→AST join broke, "+
			"so these guards are not checking what they claim to", resolved, len(sites))
	}
	return sites, out
}

// ─── guard 1: reporting commands must resolve the format ─────────────────

// TestEveryReportingCommandHonoursTheFormatFlag is the guard. `-f json` is
// advertised on every command in the tree; a command that answers a question
// and never asks what format the answer should be in is advertising something
// it cannot deliver.
func TestEveryReportingCommandHonoursTheFormatFlag(t *testing.T) {
	sites, results := analyseAll(t)

	var violations []string
	for _, s := range sites {
		res, ok := results[s.path]
		if !ok || !s.isReporting() {
			continue
		}
		if _, exempt := formatContractExempt[s.path]; exempt {
			continue
		}
		if res.printsToStdout && !res.formatAware {
			violations = append(violations, s.path+"  ("+filepath.Base(s.file)+":"+strconv.Itoa(s.line)+")")
		}
	}

	if len(violations) > 0 {
		sort.Strings(violations)
		t.Errorf("%d reporting command(s) write to stdout without ever resolving the output "+
			"format, so `-f json` on them emits human text, exits 0, and every pipeline "+
			"reading them breaks silently (#2086):\n  %s\n\nFix: render through the shared "+
			"formatter. resolvedFormatter(cmd).AutoHuman(payload, func() { …existing human "+
			"output… }) keeps the human bytes byte-identical while making json/yaml/ndjson "+
			"real. If the command genuinely has no machine representation, add it to "+
			"formatContractExempt with the reason.",
			len(violations), strings.Join(violations, "\n  "))
	}
}

// ─── guard 2: no prose in front of the JSON ──────────────────────────────

// TestNoProseBeforeTheFormatIsKnown catches the subtler half. A command that
// resolves the format and renders a perfectly good JSON document still breaks
// the contract if it printed three lines of advice to stdout on the way there:
// the stream as a whole stops parsing, and `jq` fails on output that contains
// valid JSON. `crewship digest enable` shipped exactly that — three advisory
// lines, then a correct `digestEnableResult`.
//
// The rule is deliberately narrow, because the obvious wider one is unsound.
// "A print outside a format-gated branch" looks right and is not: the standard
// idiom in this tree is
//
//	switch f.Format {
//	case "json":  return f.JSON(rows)
//	…
//	}
//	fmt.Println("No routines registered yet.")   // human-only, by early return
//
// where the human prints are lexically unguarded and semantically fine. Flow
// analysis would be needed to tell those apart, so this guard checks only the
// half that needs none: a write that happens at a source position BEFORE the
// handler has resolved the format at all cannot possibly be conditional on it.
//
// KNOWN LIMIT, so a pass is not over-read: prose printed AFTER the format is
// resolved but still unconditionally — `f := resolvedFormatter(cmd);
// fmt.Println("hi"); return f.JSON(x)` — is not reported. Under-reporting is
// the safe direction for a guard; a false accusation costs more than a missed
// one, and the per-command execution tests (cmd_*_format_test.go) cover the
// commands where it matters.
func TestNoProseBeforeTheFormatIsKnown(t *testing.T) {
	sites, results := analyseAll(t)

	var violations []string
	for _, s := range sites {
		res, ok := results[s.path]
		if !ok || len(res.prefixProse) == 0 {
			continue
		}
		if _, exempt := formatContractExempt[s.path]; exempt {
			continue
		}
		violations = append(violations, s.path+"  (stdout write at "+
			strings.Join(res.prefixProse, ", ")+", before the format is resolved)")
	}

	if len(violations) > 0 {
		sort.Strings(violations)
		t.Errorf("%d command(s) write to stdout before they have resolved the output format, "+
			"so under `-f json` the prose prefixes the JSON document and the whole stream "+
			"stops parsing (#2086):\n  %s\n\nFix: move the line to stderr (cli.PrintSuccess / "+
			"fmt.Fprintln(os.Stderr, …)) if it is a human courtesy, or into the human renderer "+
			"passed to AutoHuman if it is part of the human rendering.",
			len(violations), strings.Join(violations, "\n  "))
	}
}

// ─── guard 3: the mutation-receipt population does not grow ──────────────

// mutationReceiptBudget is the number of NON-reporting commands that print
// their "✓ done" receipt on stdout instead of stderr. Every one of them makes
// `crewship <mutate> -f json | jq` fail, so the number should only ever fall.
//
// It is a budget rather than a list because the population is large and moves
// in unrelated PRs; a checked-in list of 120 command names would conflict with
// every one of them and teach people to edit the guard rather than the code.
// The exact members are always one `go test -run …Receipts -v` away.
//
// The number tracks main, so a merge that adds a mutation command printing its
// receipt on stdout will fail this on the NEXT branch that rebases — which is
// the guard doing its job late rather than not at all. `crewship integration
// access` is the most recent example (#2079). The correct response is almost
// always the one-line change to cli.PrintSuccess, not a bump here.
const mutationReceiptBudget = 122

// TestMutationReceiptsOnStdoutDoNotGrow keeps the other half of #2086 visible.
// These are not contract-clean — a receipt on stdout breaks `-f json` just as
// thoroughly — they are simply a much larger, much more mechanical cleanup
// than the reporting commands, and lumping them together produced a 138-line
// failure nobody could act on. Ratcheting the count means the population can
// only shrink, and finishing the cleanup is a one-line edit here.
func TestMutationReceiptsOnStdoutDoNotGrow(t *testing.T) {
	sites, results := analyseAll(t)

	var offenders []string
	for _, s := range sites {
		res, ok := results[s.path]
		if !ok || s.isReporting() {
			continue
		}
		if _, exempt := formatContractExempt[s.path]; exempt {
			continue
		}
		if res.printsToStdout && !res.formatAware {
			offenders = append(offenders, s.path)
		}
	}
	sort.Strings(offenders)
	t.Logf("%d mutation commands print their receipt on stdout:\n  %s",
		len(offenders), strings.Join(offenders, "\n  "))

	if len(offenders) > mutationReceiptBudget {
		t.Errorf("%d mutation commands print to stdout without resolving the output format, "+
			"over the budget of %d — a receipt on stdout breaks `crewship <cmd> -f json | jq` "+
			"exactly as badly as a report does (#2086). Move the line to stderr with "+
			"cli.PrintSuccess. Do not raise the budget.",
			len(offenders), mutationReceiptBudget)
	}
	// Ratchet down. A budget left sitting above the real number is slack the
	// population will quietly grow back into, and the whole value of a ratchet
	// is that it does not have any. The band is wide enough that a PR removing
	// a handful of commands does not fail on arithmetic.
	if len(offenders) < mutationReceiptBudget-15 {
		t.Errorf("only %d mutation commands still print their receipt on stdout, but "+
			"mutationReceiptBudget is %d. Lower the budget to %d so the ground you "+
			"reclaimed stays reclaimed — this is the good failure.",
			len(offenders), mutationReceiptBudget, len(offenders))
	}
}

// ─── guard 4: the exemption table stays honest ───────────────────────────

// TestFormatContractExemptionsAreAllReal stops the exemption table from
// becoming a graveyard: an entry naming a command that no longer exists is a
// hole nobody can see, and a blank reason is an exemption nobody can review.
func TestFormatContractExemptionsAreAllReal(t *testing.T) {
	have := map[string]bool{}
	for _, s := range runnableCommands(t) {
		have[s.path] = true
	}
	for path, reason := range formatContractExempt {
		if !have[path] {
			t.Errorf("formatContractExempt lists %q, which is not a runnable command — "+
				"remove the entry (reason on file: %s)", path, reason)
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("formatContractExempt[%q] has no reason", path)
		}
	}
	for path := range reportingCommands {
		if !have[path] {
			t.Errorf("reportingCommands lists %q, which is not a runnable command", path)
		}
	}
}
