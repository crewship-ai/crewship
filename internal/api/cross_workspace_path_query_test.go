package api

// cross_workspace_path_query_test.go — the fence hole that opens when the
// middleware and the handler disagree about which workspace this is.
//
// RequireWorkspace resolves the tenant from three sources in priority order:
// ?workspace_id= (query), then {workspaceId} (path), then X-Workspace-ID
// (header) — see middleware.go. All three are attacker-controlled, which is
// fine, because all three are validated against membership. What is NOT fine is
// a handler that re-reads one of them for itself: on a route that carries
// {workspaceId} in its path, a caller can pass their OWN workspace in the query
// (so the membership check passes) and SOMEONE ELSE'S in the path. The
// middleware says A. The handler, if it reads the path, says B.
//
// That is not hypothetical. POST /workspaces/{workspaceId}/skills/generate read
// the path, so an OWNER of A could send
//
//	POST /api/v1/workspaces/<B>/skills/generate?workspace_id=<A>
//
// and the handler would evaluate A's role against workspace B and then look up,
// decrypt, and spend B's Anthropic API key. The fix is one line — take the
// workspace from the context, which is the only value membership was checked
// against — and this file pins both the specific route and the whole class.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestCrossWorkspaceFence_SkillsGenerate_PathQueryDivergence is the live
// reproduction. Only the victim's workspace holds an Anthropic credential, so
// the response says which workspace the handler actually looked in:
//
//	412 "needs an Anthropic API key"  → it looked in the attacker's (correct)
//	anything else                     → it reached the victim's credential
//
// The assertion needs no network and no LLM stub: the credential lookup happens
// before the provider call, and its failure mode is distinctive.
func TestCrossWorkspaceFence_SkillsGenerate_PathQueryDivergence(t *testing.T) {
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	attacker := fenceSeedTenant(t, db, "a")
	victim := fenceSeedTenant(t, db, "b")

	// Only the victim has an ANTHROPIC API_KEY. fenceSeedTenant's credential is
	// provider NONE, so neither tenant has one until this insert.
	if _, err := db.Exec(`INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		VALUES ('cred-anthropic-victim', ?, 'victim anthropic', 'enc', 'API_KEY', 'ANTHROPIC', 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
		victim.wsID, victim.userID); err != nil {
		t.Fatalf("seed victim anthropic credential: %v", err)
	}

	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger(),
		WithOutputBasePath(t.TempDir()))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	// Path names the victim's workspace; query names the attacker's own, which
	// is what RequireWorkspace validates. The divergence is the attack.
	url := "/api/v1/workspaces/" + victim.wsID + "/skills/generate?workspace_id=" + attacker.wsID
	req := httptest.NewRequest("POST", url,
		strings.NewReader(`{"slug":"grafted-skill","prompt":"write something"}`))
	req.Header.Set("Authorization", "Bearer "+attacker.token)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code == http.StatusPreconditionFailed {
		return // handler looked in the attacker's workspace — the fence held
	}
	t.Fatalf("LEAKED: POST /workspaces/{victim}/skills/generate?workspace_id={attacker} returned %d, want 412.\n"+
		"412 means the handler looked for an Anthropic credential in the ATTACKER's workspace (there is none).\n"+
		"Any other status means it resolved the workspace from the path and reached the VICTIM's credential.\nbody=%s",
		rr.Code, fenceTrim(rr.Body.String()))
}

// pathWorkspaceAllowlist records every place in this package that may read the
// workspace id straight out of the URL path, with the reason it is safe.
// Anything not listed here is a handler deriving the tenant from a source that
// was never checked for membership.
var pathWorkspaceAllowlist = map[string]string{
	// RequireWorkspace / OptionalWorkspaceRole: this IS the resolution step —
	// the value read here is validated against workspace_members immediately
	// below and only then written to the context.
	"middleware.go": "the middleware that resolves and validates the workspace",
	// Presence check only: the value is never used to scope anything (skills
	// are instance-wide), it just rejects a malformed route match.
	"skills_bulk_import.go": "emptiness check only; the value is not used to scope any query",
}

// TestNoHandlerReadsWorkspaceFromPath is the class-level guard. It parses the
// package (AST, not text — a "N lines around the match" window is guesswork and
// this repo has three recorded false passes from exactly that) and fails on any
// r.PathValue("workspaceId") outside the allowlist above.
//
// Written to the three-case discipline the audit asks of every source guard:
//   - RED: a new unlisted read fails the test, naming file:line.
//   - No false positive: the two allowlisted files stay green.
//   - No vacuous pass: every allowlist entry must actually be found, so a
//     renamed file or a broken matcher fails loudly instead of reporting
//     "nothing to check" as success.
func TestNoHandlerReadsWorkspaceFromPath(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	fset := token.NewFileSet()
	found := map[string]int{} // file -> occurrences
	var offenders []string
	scanned := 0

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		file, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		scanned++

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "PathValue" {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			arg, err := strconv.Unquote(lit.Value)
			if err != nil || arg != "workspaceId" {
				return true
			}
			found[name]++
			if _, allowed := pathWorkspaceAllowlist[name]; !allowed {
				offenders = append(offenders, fset.Position(call.Pos()).String())
			}
			return true
		})
	}

	if scanned < 50 {
		t.Fatalf("only %d source files scanned — the walker is broken, not the package clean (vacuous pass guard)", scanned)
	}
	for file, reason := range pathWorkspaceAllowlist {
		if found[file] == 0 {
			t.Errorf("allowlisted %s (%s) no longer contains r.PathValue(\"workspaceId\") — "+
				"if the read is genuinely gone, drop the entry; if the file was renamed, update it. "+
				"Leaving a stale entry means this guard silently stops matching.", file, reason)
		}
	}
	for _, pos := range offenders {
		t.Errorf("%s: reads r.PathValue(\"workspaceId\").\n"+
			"RequireWorkspace resolves the tenant with the QUERY parameter taking priority over the path, "+
			"so a caller can pass their own workspace in ?workspace_id= (which is what membership is checked against) "+
			"and someone else's in the path. Use WorkspaceIDFromContext(r.Context()) — the only value that was validated. "+
			"If this read is genuinely safe, add the file to pathWorkspaceAllowlist with the reason.", pos)
	}
}
