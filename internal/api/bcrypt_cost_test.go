package api

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"golang.org/x/crypto/bcrypt"
)

// bcrypt_cost_test.go — the guard rails that make lowering bcrypt for the
// test binary a safe trade rather than a silent downgrade for real users.
//
// #2031 lowered the cost the *test* binary hashes with, because production
// strength under -race was the largest single line item in this package's
// suite and it had eaten the CI job's 30-minute budget. Four properties have
// to hold for that to stay honest, and each has a test below:
//
//  1. production still hashes at 12                (ProductionValueIsPinned)
//  2. the test binary really is lowered            (TestBinaryLowersIt)
//  3. no call site writes its own number instead   (EverySiteReadsTheVar)
//  4. nothing outside the test binary writes the var (OnlyTheTestBinaryWrites)
//
// Together they mean a server always runs at ProductionBcryptCost regardless
// of what the test binary does, and that "make it fast" cannot be achieved by
// quietly weakening a handler.

// TestBcryptCost_ProductionValueIsPinned is the tripwire on the security
// property itself. bcrypt's cost is a power of two, so each step down halves
// what an offline attacker pays for a stolen `users` table; 12 is the value
// Crewship ships and the value every stored hash was produced with.
//
// If you are here because this test failed: lowering the cost for the test
// binary does NOT require touching this constant — TestMain lowers the
// bcryptCost var instead. Changing this number changes it for real users.
func TestBcryptCost_ProductionValueIsPinned(t *testing.T) {
	t.Parallel()
	const want = 12
	if ProductionBcryptCost != want {
		t.Fatalf("ProductionBcryptCost = %d, want %d — this is the work factor real users' passwords are hashed with; "+
			"the test binary lowers the bcryptCost var in TestMain and never needs this constant changed",
			ProductionBcryptCost, want)
	}
	// Pin the meaning, not just the number: a hash generated at the
	// production cost must report that cost back.
	h, err := bcrypt.GenerateFromPassword([]byte("pin-the-production-cost"), ProductionBcryptCost)
	if err != nil {
		t.Fatalf("generate at production cost: %v", err)
	}
	got, err := bcrypt.Cost(h)
	if err != nil {
		t.Fatalf("read cost back: %v", err)
	}
	if got != want {
		t.Errorf("hash generated at ProductionBcryptCost reports cost %d, want %d", got, want)
	}
}

// TestBcryptCost_TestBinaryLowersIt is the other half of the pin, and it is
// the one that keeps CI from silently sliding back into #2031. If someone
// deletes the lowerBcryptCostForTests call from TestMain, nothing fails — the
// suite just gets ~10 minutes slower under -race and the next author gets a
// timeout panic naming their PR. So assert it here, where the failure message
// can say what actually happened.
func TestBcryptCost_TestBinaryLowersIt(t *testing.T) {
	t.Parallel()
	if bcryptCost >= ProductionBcryptCost {
		t.Fatalf("bcryptCost = %d in the test binary, want it lowered below ProductionBcryptCost (%d): "+
			"TestMain must call lowerBcryptCostForTests. At production strength this package's suite "+
			"spends roughly ten extra minutes under -race on key stretching that proves nothing about "+
			"the handlers — see #2031", bcryptCost, ProductionBcryptCost)
	}
}

// TestBcryptCost_EverySiteReadsTheVar closes the obvious hole in the seam: a
// new handler that hashes a password with its own literal keeps working, and
// keeps being slow, and is invisible to every other test here. Before #2031
// the literal 12 was copy-pasted at six sites — which is exactly how it
// became impossible to change in one place.
//
// The check is per call expression across the package's non-test sources: any
// bcrypt.GenerateFromPassword must pass the identifier bcryptCost as its cost
// argument. Nothing else is accepted, not even ProductionBcryptCost — a site
// that reads the constant directly is a site the test binary cannot lower.
func TestBcryptCost_EverySiteReadsTheVar(t *testing.T) {
	t.Parallel()

	var offenders []string
	forEachNonTestFile(t, func(fset *token.FileSet, file *ast.File) {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isPkgCall(call.Fun, "bcrypt", "GenerateFromPassword") {
				return true
			}
			if len(call.Args) != 2 {
				offenders = append(offenders, srcPos(fset, call.Pos())+
					": bcrypt.GenerateFromPassword with an unexpected number of arguments")
				return true
			}
			if id, ok := call.Args[1].(*ast.Ident); ok && id.Name == "bcryptCost" {
				return true
			}
			offenders = append(offenders, srcPos(fset, call.Args[1].Pos())+
				": cost argument is "+srcText(fset, call.Args[1])+", want the bcryptCost var")
			return true
		})
	})

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("bcrypt.GenerateFromPassword must take its cost from the package-level bcryptCost var "+
			"(bcrypt_cost.go), so production strength and the test binary's cost are decided in one "+
			"place:\n  %s", strings.Join(offenders, "\n  "))
	}
}

// TestBcryptCost_OnlyTheTestBinaryWrites is what makes "a server always runs
// at ProductionBcryptCost" a fact rather than a convention. bcryptCost is a
// var, so in principle any code could lower it at runtime — a config knob, a
// "dev mode" fast path, a benchmark helper left in a non-test file. None of
// those exist, and this keeps it that way: the only assignment allowed lives
// in a _test.go file, which never links into the shipped binary.
func TestBcryptCost_OnlyTheTestBinaryWrites(t *testing.T) {
	t.Parallel()

	var offenders []string
	note := func(fset *token.FileSet, pos token.Pos) {
		offenders = append(offenders, srcPos(fset, pos))
	}
	forEachNonTestFile(t, func(fset *token.FileSet, file *ast.File) {
		ast.Inspect(file, func(n ast.Node) bool {
			switch s := n.(type) {
			case *ast.AssignStmt:
				for _, lhs := range s.Lhs {
					if id, ok := lhs.(*ast.Ident); ok && id.Name == "bcryptCost" {
						note(fset, s.Pos())
					}
				}
			case *ast.IncDecStmt:
				if id, ok := s.X.(*ast.Ident); ok && id.Name == "bcryptCost" {
					note(fset, s.Pos())
				}
			case *ast.UnaryExpr:
				// &bcryptCost hands a writer to somebody else.
				if s.Op == token.AND {
					if id, ok := s.X.(*ast.Ident); ok && id.Name == "bcryptCost" {
						note(fset, s.Pos())
					}
				}
			}
			return true
		})
	})

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("bcryptCost is written outside the test binary — production would no longer be guaranteed to "+
			"hash at ProductionBcryptCost:\n  %s", strings.Join(offenders, "\n  "))
	}
}

// TestSignup_StoresARealBcryptHashAtTheConfiguredCost answers the question a
// lowered cost invites: does the signup test still test signup? Yes — the
// handler still runs bcrypt, still stores a `$2a$` hash, that hash still only
// verifies against the password that was submitted, and its cost is the one
// the package is configured with. What the test binary stops paying for is
// the key stretching, which is a property of the deployment, not of this
// handler's logic.
func TestSignup_StoresARealBcryptHashAtTheConfiguredCost(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	h := NewAuthHandler(db, newTestLogger(), newTestJWTValidator(t), sessions.NewDBStore(db), true)

	rr := signupForEnumTest(t, h, "hashed@example.com")
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body=%s", rr.Code, rr.Body.String())
	}

	var stored string
	if err := db.QueryRow(`SELECT hashed_password FROM users WHERE email = 'hashed@example.com'`).Scan(&stored); err != nil {
		t.Fatalf("read stored password: %v", err)
	}
	if !strings.HasPrefix(stored, "$2") {
		t.Fatalf("stored password %q is not a bcrypt hash — signup is no longer hashing", stored)
	}
	// signupForEnumTest submits "longenough1".
	if err := bcrypt.CompareHashAndPassword([]byte(stored), []byte("longenough1")); err != nil {
		t.Errorf("stored hash does not verify against the submitted password: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(stored), []byte("longenough2")); err == nil {
		t.Error("stored hash verifies against the WRONG password — it is not a real bcrypt hash")
	}
	cost, err := bcrypt.Cost([]byte(stored))
	if err != nil {
		t.Fatalf("read cost of stored hash: %v", err)
	}
	if cost != bcryptCost {
		t.Errorf("stored hash cost = %d, want %d (the package's configured cost)", cost, bcryptCost)
	}
}

// ── scanning helpers ───────────────────────────────────────────────────────

// forEachNonTestFile parses every non-test .go file in this package's
// directory and hands it to fn. Test files are excluded on purpose: they are
// where the cost is allowed to be lowered, and they never link into a
// released binary.
func forEachNonTestFile(t *testing.T, fn func(fset *token.FileSet, file *ast.File)) {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	seen := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		seen++
		fn(fset, file)
	}
	// A scan that silently matched nothing is a scan that proves nothing —
	// the same failure mode route_read_scope_invariant_test.go was fixed for.
	if seen == 0 {
		t.Fatal("scanned no non-test .go files; the invariant proved nothing")
	}
}

func isPkgCall(fun ast.Expr, pkg, name string) bool {
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != name {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg
}

func srcPos(fset *token.FileSet, pos token.Pos) string {
	p := fset.Position(pos)
	return filepath.Base(p.Filename) + ":" + strconv.Itoa(p.Line)
}

func srcText(fset *token.FileSet, e ast.Expr) string {
	var b strings.Builder
	if err := printer.Fprint(&b, fset, e); err != nil {
		return "<unprintable>"
	}
	return b.String()
}
