package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// assignee_write_invariant_test.go — the build-time invariant that closes the
// "forgotten assignee validation" class.
//
// missions.assignee_id is polymorphic: assignee_type picks whether it points
// into users or agents, so it cannot carry a foreign key. Contrast crew_id on
// the very same CREATE TABLE (internal/database/migrate_consts_v01_init.go /
// migrate_consts_v42_v45.go), which does have one. The database cannot reject
// a cross-workspace assignee_id — the only guard is validateAssigneeWorkspace
// (issue_handler.go), called by hand at every write site. #1532 and #1541
// fixed five call sites that had skipped it (issue_handler_create.go,
// issue_handler_update.go, issue_handler_bulk.go, recurring_issue_handler.go,
// triage_handler.go). Designing this invariant surfaced a 6th
// (issue_create_core.go's insertIssueTx, reachable unvalidated through
// InternalIssueHandler.Create) and a 7th (issue_handler_workflow.go's Review
// reassignment, reachable through a guessable agent slug rather than a CUID)
// — both fixed alongside this test.
//
// Granularity: PER FUNCTION, not per file, and not a fixed line window. An
// earlier version of this test kept file-level state (`strings.Contains(src,
// "validateAssigneeWorkspace")` anywhere in the whole file). Adversarial
// review reproduced a false PASS against it: a NEW, unvalidated function
// added to issue_handler_bulk.go — a file that already calls the helper from
// BulkUpdate — went undetected, because the check never looked at which
// function actually calls it. That is the same failure family as
// route_read_scope_invariant_test.go's fixed-line-window bug (#1533, 76/219
// routes slipped past because a neighbour's wrapper was in the window) —
// different granularity, same root cause: the check's scope was wider than
// the guarantee it was supposed to prove. So this scan parses each file with
// go/parser and, for every top-level function or method, inspects only THAT
// function's own AST subtree (which naturally includes any closures declared
// inside it, but not sibling functions) for both the write signal and the
// validateAssigneeWorkspace call. A write in one function can never be
// excused by a call in another function in the same file, no matter how
// close together they sit in the source.
//
// A function "writes assignee_id" when its body contains either an
// UpdateBuilder call shaped like `X.Set("assignee_id", ...)`, or a backtick
// SQL string literal that is an INSERT or UPDATE statement mentioning the
// column. Reading assignee_id — a SELECT, a WHERE filter, the assignee-name
// resolution subqueries in issue_handler.go / issues_internal.go /
// project_handler.go — needs no validation and must not be flagged.
// `X.SetNull("assignee_id")` (nulling out an assignee) is deliberately not a
// write signal: it can never introduce a foreign workspace's id.

const validateAssigneeWorkspaceFn = "validateAssigneeWorkspace"

// sqlWriteVerb matches the leading SQL verb of a raw string literal that has
// already been confirmed to mention assignee_id.
var sqlWriteVerb = regexp.MustCompile(`^\s*(INSERT|UPDATE)\b`)

// assigneeWriteFunc is one function whose body writes assignee_id.
type assigneeWriteFunc struct {
	file       string
	name       string
	declLine   int
	writeLines []int
	validates  bool
}

func (f *assigneeWriteFunc) key() string { return f.file + ":" + f.name }

// knownAssigneeWriteFuncs is the floor this scan must reproduce on this
// tree: every function #1532/#1541 fixed, plus insertIssueTx — the shared
// chokepoint where the 6th unguarded write (InternalIssueHandler.Create, in
// issues_internal.go, which calls insertIssueTx) was found and fixed. If any
// of these drops out of the scan result, the matching logic itself has
// drifted — see TestAssigneeWriteScanFindsKnownWriters.
var knownAssigneeWriteFuncs = []string{
	"issue_handler_create.go:Create",
	"issue_handler_update.go:Update",
	"issue_handler_bulk.go:BulkUpdate",
	"recurring_issue_handler.go:Create",
	"recurring_issue_handler.go:Update",
	"triage_handler.go:CreateRule",
	"triage_handler.go:UpdateRule",
	"issue_create_core.go:insertIssueTx",
}

// assigneeWritesWithoutHelper are write sites that persist assignee_id
// without calling validateAssigneeWorkspace themselves, because the value is
// already workspace-scoped by construction before it reaches the write —
// proven by a different mechanism, not by the helper. Keyed by
// "file.go:FuncName", matching the granularity of the scan itself: an
// allowlist entry excuses exactly one function, never the rest of its file.
// Each entry needs a reason a reviewer can check, mirroring
// route_read_scope_invariant_test.go's readRoutesWithoutWorkspace: adding to
// this map is a deliberate, reviewed claim, not a way to silence the test.
var assigneeWritesWithoutHelper = map[string]string{
	"assignments_run.go:Create": "target.ID is resolved via `SELECT ... FROM agents WHERE a.slug = ? AND " +
		"a.crew_id = ?`, and body.CrewID was already proven bound to the calling token's workspace by " +
		"assertBoundCrewWorkspaceDB earlier in this same function — scoped by construction before the UPDATE " +
		"ever runs, not by the helper",
	"issue_handler_workflow.go:Review": "reassign_to's agent lookup filters `WHERE slug = ? AND workspace_id = ?` " +
		"— agents.slug is only UNIQUE(workspace_id, slug), never globally unique, so the workspace filter on the " +
		"resolving query is itself both the security fix (can't resolve outside wsID) and the fix for a " +
		"LIMIT-1-over-a-non-unique-slug correctness bug found alongside it; a validateAssigneeWorkspace call " +
		"afterward would only re-prove what the query already guarantees",
	"triage_handler.go:Process": "rule.AssigneeID is copied onto a matching BACKLOG issue from the triage_rules " +
		"row already loaded earlier in this same request — that row's assignee_id was validated once, when the " +
		"rule was created or updated (CreateRule / UpdateRule in this file, both in " +
		"knownAssigneeWriteFuncs), the same copy-forward-from-an-already-validated-row shape as " +
		"recurring_issue_dispatcher.go forwarding a recurring_issues row into insertIssueTx",
}

// TestEveryAssigneeWriteValidatesWorkspace is the invariant: a function that
// writes assignee_id either calls validateAssigneeWorkspace itself, or is
// excused above with a reason. A new write site — even one added to a file
// that already validates elsewhere — fails here unless ITS OWN function body
// makes the call.
func TestEveryAssigneeWriteValidatesWorkspace(t *testing.T) {
	writers := scanAssigneeWriteFuncs(t)

	var offenders []string
	for key, fn := range writers {
		if _, ok := assigneeWritesWithoutHelper[key]; ok {
			continue
		}
		if fn.validates {
			continue
		}
		for _, line := range fn.writeLines {
			offenders = append(offenders, formatOffender(fn.file, line,
				fn.name+"() writes assignee_id but never calls validateAssigneeWorkspace in its own body"))
		}
	}
	sort.Strings(offenders)

	if len(offenders) > 0 {
		t.Fatalf(`%d assignee_id write(s) are not backed by a workspace validation in the SAME function:
%s

assignee_id is polymorphic (assignee_type picks users vs agents), so it can't
carry a foreign key — the database cannot reject a cross-workspace value, so
every write path is on its own to check it by hand. #1532/#1541 fixed five
call sites that had skipped this; this invariant is what keeps a new one —
in a new function, OR a new function dropped into an already-validated
file — from landing quietly.

Fix one of these ways:
  - call validateAssigneeWorkspace(ctx, q, assigneeType, assigneeID, wsID)
    inside the SAME function before persisting (see issue_handler_create.go
    for the pattern) — a call elsewhere in the file does not count, and
    should not, or this check could not have caught the regression it was
    written to catch, or
  - add "file.go:FuncName" to assigneeWritesWithoutHelper in this file WITH
    a reason proving the value is already workspace-scoped by some other
    mechanism before it reaches this write.
Do not add it to the map to make this test pass — the map is a review record.`,
			len(offenders), strings.Join(offenders, "\n"))
	}
}

// TestAssigneeWriteScanFindsKnownWriters guards the guard. If the write
// shapes this scan looks for change and it stops matching, the test above
// passes vacuously — it would find nothing to complain about and report
// success. This is the more important of the two tests; the same lesson bit
// route_read_scope_invariant_test.go and route_authz_invariant_test.go's
// underlying migration twice already (#1531, #1533).
func TestAssigneeWriteScanFindsKnownWriters(t *testing.T) {
	writers := scanAssigneeWriteFuncs(t)

	const minWriters = 11 // 8 in knownAssigneeWriteFuncs + 3 in assigneeWritesWithoutHelper
	if len(writers) < minWriters {
		t.Fatalf("scan found only %d assignee_id-writing function(s), expected at least %d — "+
			"the AST matching has likely stopped recognizing the write shape, which would make "+
			"TestEveryAssigneeWriteValidatesWorkspace pass vacuously", len(writers), minWriters)
	}

	for _, want := range knownAssigneeWriteFuncs {
		if _, ok := writers[want]; !ok {
			t.Errorf("expected %s to be found as an assignee_id writer (fixed by #1532/#1541, or the "+
				"insertIssueTx chokepoint) — the scan missed it", want)
		}
	}

	// Every allowlist entry must correspond to a real detected write
	// function. A stale entry excuses nothing — the function may have
	// changed shape, been renamed, or been removed — and silently weakens
	// the invariant for whatever now occupies that name.
	for key, reason := range assigneeWritesWithoutHelper {
		if _, ok := writers[key]; !ok {
			t.Errorf("assigneeWritesWithoutHelper has a stale entry %q — scan no longer finds an "+
				"assignee_id write there; remove it", key)
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("assigneeWritesWithoutHelper[%q] has no reason", key)
		}
	}
}

// scanAssigneeWriteFuncs parses every non-test .go file in this package and
// returns, per function whose body writes assignee_id, the write signal's
// line numbers and whether that SAME function's body also calls
// validateAssigneeWorkspace.
func scanAssigneeWriteFuncs(t *testing.T) map[string]*assigneeWriteFunc {
	t.Helper()

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob *.go: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no .go files found — test is looking in the wrong directory")
	}

	fset := token.NewFileSet()
	writers := map[string]*assigneeWriteFunc{}
	scanned := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		scanned++
		astFile, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}

		for _, decl := range astFile.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}

			var writeLines []int
			validates := false
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.Ident:
					if v.Name == validateAssigneeWorkspaceFn {
						validates = true
					}
				case *ast.CallExpr:
					sel, ok := v.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "Set" {
						return true
					}
					if len(v.Args) < 1 {
						return true
					}
					lit, ok := v.Args[0].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						return true
					}
					if strings.Trim(lit.Value, `"`) == "assignee_id" {
						writeLines = append(writeLines, fset.Position(v.Pos()).Line)
					}
				case *ast.BasicLit:
					if v.Kind != token.STRING || !strings.HasPrefix(v.Value, "`") {
						return true
					}
					body := strings.Trim(v.Value, "`")
					if strings.Contains(body, "assignee_id") && sqlWriteVerb.MatchString(body) {
						writeLines = append(writeLines, fset.Position(v.Pos()).Line)
					}
				}
				return true
			})

			if len(writeLines) == 0 {
				continue
			}
			sort.Ints(writeLines)
			fn := &assigneeWriteFunc{
				file:       f,
				name:       fd.Name.Name,
				declLine:   fset.Position(fd.Pos()).Line,
				writeLines: writeLines,
				validates:  validates,
			}
			writers[fn.key()] = fn
		}
	}
	if scanned == 0 {
		t.Fatal("no non-test .go files were scanned")
	}
	return writers
}
