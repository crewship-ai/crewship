package api

import (
	"os"
	"path/filepath"
	"regexp"
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
// — both fixed alongside this test. Without a build-time invariant, an eighth
// write path forgets the check exactly the way the first seven did.
//
// Scope: every non-test .go file in this package. A file "writes assignee_id"
// when it either calls an UpdateBuilder's Set("assignee_id", ...) or contains
// a backtick SQL literal whose statement is INSERT or UPDATE and mentions the
// column. Reading assignee_id — a SELECT, a WHERE filter, the assignee-name
// resolution subqueries in issue_handler.go / issues_internal.go /
// project_handler.go — needs no validation and must not be flagged; the write
// signals below only match write shapes, never SELECT.

// assigneeSetCall matches an UpdateBuilder write of assignee_id via
// `.Set("assignee_id", ...)`, on any receiver (not hardcoding the
// conventional `ub` name, so a renamed builder variable can't silently drop
// out of the scan). It deliberately does NOT match `.SetNull("assignee_id")`:
// nulling out an assignee removes it, it can never introduce a foreign
// workspace's id, so it carries none of the risk this invariant exists to
// catch. `Set\(` requires the literal characters immediately after "Set",
// so "SetNull(" can never satisfy it.
var assigneeSetCall = regexp.MustCompile(`\.Set\(\s*"assignee_id"`)

// sqlWriteVerb matches the leading SQL verb of a backtick-delimited query
// literal that has already been confirmed to mention assignee_id. Go raw
// string literals cannot contain a backtick, so splitting the file on
// backtick pairs recovers each SQL statement's exact text with no ambiguity —
// unlike a line-based lookahead window. route_read_scope_invariant_test.go's
// readRouteWrapperLookahead post-mortem is the reason to be paranoid about
// this: a fixed N-line window bled into the NEXT registration and produced a
// false PASS on 76 of 219 routes (#1533). A SQL literal has no neighbour to
// bleed into — it starts and ends at its own backticks — so that failure mode
// does not apply here, but the same class of "does the window actually
// capture what I think it captures" question still needed answering, which is
// why TestAssigneeWriteScanFindsKnownWriters (below) exists: it is the check
// that would catch this scan silently seeing nothing.
var sqlWriteVerb = regexp.MustCompile(`^\s*(INSERT|UPDATE)\b`)

var backtickLiteral = regexp.MustCompile("`([^`]*)`")

// knownAssigneeWriters is the floor this scan must reproduce on this tree:
// the five sites #1532/#1541 fixed, plus insertIssueTx — the shared
// chokepoint that creates every issue from the agent-tool-call and
// recurring-dispatcher paths, and where the 6th unguarded write
// (InternalIssueHandler.Create, in issues_internal.go, which calls
// insertIssueTx) was found and fixed. If any of these drops out of the scan
// result, the matching logic itself has drifted — see
// TestAssigneeWriteScanFindsKnownWriters.
var knownAssigneeWriters = []string{
	"issue_handler_create.go",
	"issue_handler_update.go",
	"issue_handler_bulk.go",
	"recurring_issue_handler.go",
	"triage_handler.go",
	"issue_create_core.go",
}

// assigneeWritesWithoutHelper are write sites that persist assignee_id
// without calling validateAssigneeWorkspace directly, because the value is
// already workspace-scoped by construction before it reaches the write —
// proven by a different mechanism, not by the helper. Each entry needs a
// reason a reviewer can check, exactly like route_read_scope_invariant_test.go's
// readRoutesWithoutWorkspace: adding to this map is a deliberate, reviewed
// claim, not a way to silence the test.
var assigneeWritesWithoutHelper = map[string]string{
	"assignments_run.go": "target.ID is resolved via `SELECT ... FROM agents WHERE a.slug = ? AND a.crew_id = ?` " +
		"and body.CrewID was already proven bound to the calling token's workspace by " +
		"assertBoundCrewWorkspaceDB earlier in the same handler (Create) — scoped by construction before the " +
		"UPDATE ever runs, not by the helper",
	"issue_handler_workflow.go": "reassign_to's agent lookup filters `WHERE slug = ? AND workspace_id = ?` — " +
		"agents.slug is only UNIQUE(workspace_id, slug), never globally unique, so the workspace filter on the " +
		"resolving query is itself both the security fix (can't resolve outside wsID) and the fix for a " +
		"LIMIT-1-over-a-non-unique-slug correctness bug found alongside it; a validateAssigneeWorkspace call " +
		"afterward would only re-prove what the query already guarantees",
}

// TestEveryAssigneeWriteValidatesWorkspace is the invariant: a file that
// writes assignee_id either calls validateAssigneeWorkspace itself, or is
// excused above with a reason. A new write site that is neither fails here.
func TestEveryAssigneeWriteValidatesWorkspace(t *testing.T) {
	writers := scanAssigneeWriteFiles(t)

	var offenders []string
	for file, lines := range writers {
		if _, ok := assigneeWritesWithoutHelper[file]; ok {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if strings.Contains(string(src), "validateAssigneeWorkspace") {
			continue
		}
		for _, line := range lines {
			offenders = append(offenders, formatOffender(file, line,
				"writes assignee_id but this file never calls validateAssigneeWorkspace"))
		}
	}

	if len(offenders) > 0 {
		t.Fatalf(`%d assignee_id write(s) are not backed by a workspace validation:
%s

assignee_id is polymorphic (assignee_type picks users vs agents), so it can't
carry a foreign key — the database cannot reject a cross-workspace value, so
every write path is on its own to check it by hand. #1532/#1541 fixed five
call sites that had skipped this; this invariant is what keeps a new one from
landing quietly.

Fix one of these ways:
  - call validateAssigneeWorkspace(ctx, q, assigneeType, assigneeID, wsID)
    before persisting (see issue_handler_create.go for the pattern), or
  - add the file to assigneeWritesWithoutHelper in this file WITH a reason
    proving the value is already workspace-scoped by some other mechanism
    before it reaches this write.
Do not add it to the map to make this test pass — the map is a review record.`,
			len(offenders), strings.Join(offenders, "\n"))
	}
}

// TestAssigneeWriteScanFindsKnownWriters guards the guard. If the write shapes
// this scan looks for (UpdateBuilder .Set, backtick INSERT/UPDATE literals)
// change and assigneeSetCall/sqlWriteVerb stop matching, the test above passes
// vacuously — it would find nothing to complain about and report success.
// This is the more important of the two tests; the same lesson bit
// route_read_scope_invariant_test.go and route_authz_invariant_test.go's
// underlying migration twice already (#1531, #1533).
func TestAssigneeWriteScanFindsKnownWriters(t *testing.T) {
	writers := scanAssigneeWriteFiles(t)

	const minWriters = 8 // 5 known (#1532/#1541) + issue_create_core.go (chokepoint) +
	// 2 allowlisted-by-construction (assignments_run.go, issue_handler_workflow.go)
	if len(writers) < minWriters {
		t.Fatalf("scan found only %d assignee_id-writing file(s), expected at least %d — "+
			"assigneeSetCall/sqlWriteVerb has likely stopped matching the write shape, which would make "+
			"TestEveryAssigneeWriteValidatesWorkspace pass vacuously", len(writers), minWriters)
	}

	for _, want := range knownAssigneeWriters {
		if _, ok := writers[want]; !ok {
			t.Errorf("expected %s to be found as an assignee_id writer (fixed by #1532/#1541, or the "+
				"insertIssueTx chokepoint) — the scan missed it", want)
		}
	}

	// Every allowlist entry must correspond to a real detected write file. A
	// stale entry excuses nothing — the file may have changed shape or been
	// removed — and silently weakens the invariant for whatever code now
	// occupies that filename.
	for file, reason := range assigneeWritesWithoutHelper {
		if _, ok := writers[file]; !ok {
			t.Errorf("assigneeWritesWithoutHelper has a stale entry %q — scan no longer finds an "+
				"assignee_id write there; remove it", file)
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("assigneeWritesWithoutHelper[%q] has no reason", file)
		}
	}
}

// scanAssigneeWriteFiles walks every non-test .go file in this package and
// returns, per file that writes assignee_id, the 1-based line numbers of each
// write signal found.
func scanAssigneeWriteFiles(t *testing.T) map[string][]int {
	t.Helper()

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob *.go: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no .go files found — test is looking in the wrong directory")
	}

	writers := map[string][]int{}
	scanned := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		scanned++
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		content := string(src)

		var lines []int

		// Signal 1: an UpdateBuilder write. Comment-only lines are skipped —
		// a line documenting the call is not the call.
		for i, line := range strings.Split(content, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if assigneeSetCall.MatchString(line) {
				lines = append(lines, i+1)
			}
		}

		// Signal 2: a backtick SQL literal that is an INSERT or UPDATE and
		// mentions assignee_id. A SELECT mentioning assignee_id — a filter, a
		// join, a name-resolution subquery — is a read and must not match.
		for _, m := range backtickLiteral.FindAllStringSubmatchIndex(content, -1) {
			body := content[m[2]:m[3]]
			if !strings.Contains(body, "assignee_id") {
				continue
			}
			if !sqlWriteVerb.MatchString(body) {
				continue // SELECT (or anything else) — a read, not a write
			}
			lines = append(lines, 1+strings.Count(content[:m[2]], "\n"))
		}

		if len(lines) > 0 {
			writers[f] = lines
		}
	}
	if scanned == 0 {
		t.Fatal("no non-test .go files were scanned")
	}
	return writers
}
